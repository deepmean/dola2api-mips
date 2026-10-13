package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"sync"
	"time"
)

// TaskState is where a task is in its life.
type TaskState string

const (
	TaskPending   TaskState = "pending"
	TaskRunning   TaskState = "running"
	TaskSucceeded TaskState = "succeeded"
	TaskFailed    TaskState = "failed"
)

// Task is one generation request and its outcome.
type Task struct {
	ID      string
	Request SubmitRequest

	State          TaskState
	ConversationID string
	Media          []Media
	Error          string

	CreatedAt  time.Time
	StartedAt  time.Time
	FinishedAt time.Time

	// seq orders tasks for listing. It exists because CreatedAt is not a
	// reliable ordering: several tasks can be created within one clock tick,
	// which makes a sort by timestamp non-deterministic.
	seq uint64
}

// ErrPoolFull means the pending queue is at its configured bound.
var ErrPoolFull = errors.New("pool: too many pending tasks")

// ErrPoolClosed means the pool is shutting down.
var ErrPoolClosed = errors.New("pool: closed")

// Pool turns requests into generated media, one upstream submission at a time.
//
// The concurrency bound is deliberate rather than incidental: the target router
// has 250MB of RAM and the upstream rate-limits anyway, so a second worker buys
// nothing and costs memory. The pending bound exists for the same reason 閳?a
// queue that grows without limit is a way to run out of memory from the outside.
type Pool struct {
	cfg      Config
	accounts *AccountStore
	exec     Executor
	upstream *Upstream
	logger   *log.Logger

	mu      sync.Mutex
	tasks   map[string]*Task
	queue   []*Task
	running int
	closed  bool
	nextSeq uint64
	wake    chan struct{}
	done    chan struct{}
}

// NewPool builds a pool. Call Start to run it.
func NewPool(cfg Config, accounts *AccountStore, exec Executor, upstream *Upstream, logger *log.Logger) *Pool {
	if logger == nil {
		logger = log.Default()
	}
	return &Pool{
		cfg:      cfg,
		accounts: accounts,
		exec:     exec,
		upstream: upstream,
		logger:   logger,
		tasks:    map[string]*Task{},
		wake:     make(chan struct{}, 1),
		done:     make(chan struct{}),
	}
}

// Start launches the workers. It returns immediately.
func (p *Pool) Start(ctx context.Context) {
	workers := p.cfg.MaxConcurrency
	if workers < 1 {
		workers = 1
	}
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p.worker(ctx)
		}()
	}
	go func() {
		wg.Wait()
		close(p.done)
	}()
}

// Close stops accepting work and waits for the running task to finish.
func (p *Pool) Close() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	p.mu.Unlock()
	p.signal()
	<-p.done
}

func (p *Pool) signal() {
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

// Submit queues a request and returns a snapshot of the new task.
//
// The snapshot is taken while the lock is held. Returning the live pointer
// instead would let a caller read fields the worker is writing, which is a data
// race the race detector caught in exactly that form: the API dereferenced the
// task to render it while a worker was setting its state.
func (p *Pool) Submit(req SubmitRequest) (Task, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		return Task{}, ErrPoolClosed
	}
	if len(p.queue)+p.running >= p.cfg.MaxPendingTasks {
		return Task{}, ErrPoolFull
	}

	p.nextSeq++
	task := &Task{
		ID:        newUUID(),
		Request:   req,
		State:     TaskPending,
		CreatedAt: time.Now(),
		seq:       p.nextSeq,
	}
	p.tasks[task.ID] = task
	p.queue = append(p.queue, task)
	p.evictLocked()
	p.signal()
	return *task, nil
}

// Get returns a snapshot of a task.
func (p *Pool) Get(id string) (Task, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	task, ok := p.tasks[id]
	if !ok {
		return Task{}, false
	}
	snapshot := *task
	snapshot.Media = append([]Media(nil), task.Media...)
	return snapshot, true
}

// Pending reports queued plus running tasks, for /health.
func (p *Pool) Pending() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.queue) + p.running
}

// List returns up to limit task snapshots, newest first.
func (p *Pool) List(limit int) []Task {
	if limit <= 0 {
		limit = 50
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	out := make([]Task, 0, len(p.tasks))
	for _, task := range p.tasks {
		snapshot := *task
		snapshot.Media = append([]Media(nil), task.Media...)
		out = append(out, snapshot)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].seq > out[j].seq })
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// worker runs one task at a time.
func (p *Pool) worker(ctx context.Context) {
	for {
		task := p.take(ctx)
		if task == nil {
			return
		}
		p.run(ctx, task)
	}
}

// take returns the next queued task, or nil when the pool is closed.
func (p *Pool) take(ctx context.Context) *Task {
	for {
		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			return nil
		}
		if len(p.queue) > 0 {
			task := p.queue[0]
			p.queue = p.queue[1:]
			task.State = TaskRunning
			task.StartedAt = time.Now()
			p.running++
			p.mu.Unlock()
			return task
		}
		p.mu.Unlock()

		select {
		case <-ctx.Done():
			return nil
		case <-p.wake:
		}
	}
}

// run performs one task from submission through polling to retrieval.
func (p *Pool) run(ctx context.Context, task *Task) {
	defer func() {
		p.mu.Lock()
		p.running--
		if task.FinishedAt.IsZero() {
			task.FinishedAt = time.Now()
		}
		if task.State == TaskRunning {
			task.State = TaskFailed
			task.Error = "task ended without a result"
		}
		p.mu.Unlock()
		p.signal()
	}()

	// Failover: try another session before failing the task.
	//
	// Each failed attempt parks the session that failed, so the next Pick cannot
	// return it and the retry genuinely moves on. Attempts are bounded because a
	// pool of broken sessions must not turn one request into an unbounded loop.
	attempts := p.cfg.SubmitAttempts
	if attempts < 1 {
		attempts = 1
	}

	var (
		account        *Account
		conversationID string
		lastErr        error
	)
	for attempt := 1; attempt <= attempts; attempt++ {
		candidate, err := p.accounts.Pick(time.Now())
		if err != nil {
			if lastErr != nil {
				p.fail(task, fmt.Errorf("every session failed (tried %d); last error: %w", attempt-1, lastErr))
			} else {
				p.fail(task, fmt.Errorf("no session available: %w", err))
			}
			return
		}

		// The submit and the polling each get their own deadline: a submit that
		// hangs must not eat the time budget for waiting on the result.
		submitCtx, cancelSubmit := context.WithTimeout(ctx, p.cfg.SubmitDeadline)
		conversationID, err = p.exec.Submit(submitCtx, candidate.Cookie, task.Request)
		cancelSubmit()
		if err == nil {
			account, lastErr = candidate, nil
			break
		}

		lastErr = err
		p.accounts.RecordFailure(candidate, time.Now(), err)
		p.logger.Printf("task %s: attempt %d/%d with %q failed: %v",
			task.ID, attempt, attempts, candidate.Label, err)
	}
	if lastErr != nil {
		p.fail(task, lastErr)
		return
	}

	p.mu.Lock()
	task.ConversationID = conversationID
	p.mu.Unlock()

	media, err := p.awaitMedia(ctx, account.Cookie, conversationID)
	if err != nil {
		p.accounts.RecordFailure(account, time.Now(), err)
		p.fail(task, err)
		return
	}

	p.accounts.RecordSuccess(account, time.Now())
	p.mu.Lock()
	task.Media = media
	task.State = TaskSucceeded
	task.FinishedAt = time.Now()
	p.mu.Unlock()
	p.logger.Printf("task %s: %d item(s) from conversation %s", task.ID, len(media), conversationID)
}

// awaitMedia polls until the conversation carries a result or the budget runs out.
func (p *Pool) awaitMedia(ctx context.Context, cookie, conversationID string) ([]Media, error) {
	deadline := time.Now().Add(p.cfg.VideoTimeout)
	ticker := time.NewTicker(p.cfg.PollInterval)
	defer ticker.Stop()

	var lastErr error
	for {
		pollCtx, cancel := context.WithTimeout(ctx, p.cfg.FirstByteTimeout)
		result, err := p.upstream.Poll(pollCtx, cookie, conversationID)
		cancel()

		switch {
		case err != nil:
			lastErr = err
		case len(result.Media) > 0:
			return result.Media, nil
		}

		if time.Now().After(deadline) {
			if lastErr != nil {
				return nil, fmt.Errorf("timed out waiting for the result (last poll error: %w)", lastErr)
			}
			return nil, errors.New("timed out waiting for the result")
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

func (p *Pool) fail(task *Task, err error) {
	p.mu.Lock()
	task.State = TaskFailed
	task.Error = err.Error()
	task.FinishedAt = time.Now()
	p.mu.Unlock()
	p.logger.Printf("task %s failed: %v", task.ID, err)
}

// evictLocked drops finished tasks past their TTL, then the oldest ones if the
// map is still over its bound. Callers hold the lock.
func (p *Pool) evictLocked() {
	cutoff := time.Now().Add(-p.cfg.TaskTTL)
	for id, task := range p.tasks {
		if task.FinishedAt.IsZero() {
			continue
		}
		if task.FinishedAt.Before(cutoff) {
			delete(p.tasks, id)
		}
	}

	// A hard bound as well, so a burst of short tasks cannot grow the map
	// faster than the TTL clears it.
	maxTasks := p.cfg.MaxPendingTasks * 4
	if maxTasks < 32 {
		maxTasks = 32
	}
	for len(p.tasks) > maxTasks {
		oldestID := ""
		var oldest time.Time
		for id, task := range p.tasks {
			if task.State == TaskPending || task.State == TaskRunning {
				continue
			}
			if oldestID == "" || task.FinishedAt.Before(oldest) {
				oldestID, oldest = id, task.FinishedAt
			}
		}
		if oldestID == "" {
			return
		}
		delete(p.tasks, oldestID)
	}
}
