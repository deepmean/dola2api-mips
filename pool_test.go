package main

import (
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// fakeExecutor stands in for the submit half of the pipeline.
type fakeExecutor struct {
	mu             sync.Mutex
	conversationID string
	err            error
	calls          int
	cookies        []string
	block          chan struct{} // when non-nil, Submit waits on it
}

func (f *fakeExecutor) Name() string { return "fake" }

func (f *fakeExecutor) Submit(ctx context.Context, cookie string, req SubmitRequest) (string, error) {
	f.mu.Lock()
	f.calls++
	f.cookies = append(f.cookies, cookie)
	block, convID, err := f.block, f.conversationID, f.err
	f.mu.Unlock()

	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	if err != nil {
		return "", err
	}
	return convID, nil
}

func (f *fakeExecutor) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// newTestPool builds a pool whose upstream is a fake server.
func newTestPool(t *testing.T, exec Executor, cfg Config) (*Pool, *AccountStore, *httptest.Server) {
	t.Helper()

	polls := 0
	var mu sync.Mutex
	upstreamSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		polls++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(pollFixture(t))
	}))
	t.Cleanup(upstreamSrv.Close)

	egress, err := NewEgress(Config{EgressMode: "direct", FirstByteTimeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("NewEgress: %v", err)
	}
	t.Cleanup(egress.Close)

	upstream := NewUpstream(egress)
	upstream.base = upstreamSrv.URL

	accountsCfg := cfg
	accountsCfg.AccountsFile = filepath.Join(t.TempDir(), "accounts.json")
	accountsCfg.DailyLimit = 5
	accountsCfg.CooldownSec = 60
	accountsCfg.ResetTZ = time.UTC
	if err := os.WriteFile(accountsCfg.AccountsFile,
		[]byte(`[{"label":"a","cookie":"sessionid=aaa"}]`), 0o600); err != nil {
		t.Fatalf("write accounts: %v", err)
	}
	accounts := NewAccountStore(accountsCfg)
	if err := accounts.Load(); err != nil {
		t.Fatalf("accounts.Load: %v", err)
	}

	poolCfg := cfg
	poolCfg.MaxPendingTasks = 4
	poolCfg.MaxConcurrency = 1
	// Mirror the loader's defaults. A zero here would silently disable the
	// behaviour under test rather than the test failing.
	poolCfg.SubmitAttempts = defaultSubmitAttempts
	poolCfg.AccountStrategy = defaultAccountStrategy
	poolCfg.PollInterval = 10 * time.Millisecond
	poolCfg.VideoTimeout = 2 * time.Second
	poolCfg.SubmitDeadline = 2 * time.Second
	poolCfg.FirstByteTimeout = 2 * time.Second
	poolCfg.TaskTTL = time.Minute
	poolCfg.DailyLimit = 5

	logger := log.New(io.Discard, "", 0)
	return NewPool(poolCfg, accounts, exec, upstream, logger), accounts, upstreamSrv
}

func waitForState(t *testing.T, pool *Pool, id string, want TaskState) Task {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		task, ok := pool.Get(id)
		if !ok {
			t.Fatalf("task %s disappeared", id)
		}
		if task.State == want {
			return task
		}
		if task.State == TaskFailed && want != TaskFailed {
			t.Fatalf("task failed while waiting for %s: %s", want, task.Error)
		}
		time.Sleep(5 * time.Millisecond)
	}
	task, _ := pool.Get(id)
	t.Fatalf("task stayed in %s, want %s (error: %s)", task.State, want, task.Error)
	return Task{}
}

func TestPoolRunsTaskToCompletion(t *testing.T) {
	exec := &fakeExecutor{conversationID: "conv-1"}
	pool, accounts, _ := newTestPool(t, exec, Config{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pool.Start(ctx)
	defer pool.Close()

	task, err := pool.Submit(SubmitRequest{Prompt: "a cat", Ratio: "9:16", Model: "seedance_v2.0", Duration: 10})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	done := waitForState(t, pool, task.ID, TaskSucceeded)

	if done.ConversationID != "conv-1" {
		t.Errorf("conversation id = %q", done.ConversationID)
	}
	if len(done.Media) != 1 || done.Media[0].URL == "" {
		t.Errorf("media = %+v", done.Media)
	}
	if exec.callCount() != 1 {
		t.Errorf("executor called %d time(s), want 1", exec.callCount())
	}

	// One video was generated, so the account's allowance must have moved.
	health := accounts.Health(time.Now())[0].(map[string]any)
	if health["used_today"] != 1 {
		t.Errorf("used_today = %v, want 1 after a success", health["used_today"])
	}
	if pool.Pending() != 0 {
		t.Errorf("Pending() = %d after completion", pool.Pending())
	}
}

func TestPoolRejectsWhenFull(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	exec := &fakeExecutor{conversationID: "conv-1", block: block}

	cfg := Config{}
	pool, _, _ := newTestPool(t, exec, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pool.Start(ctx)
	defer pool.Close()

	// MaxPendingTasks is 4 in the harness: one runs, so four more fit before
	// the queue is full.
	var accepted int
	for i := 0; i < 8; i++ {
		_, err := pool.Submit(SubmitRequest{Prompt: "a cat"})
		if errors.Is(err, ErrPoolFull) {
			break
		}
		if err != nil {
			t.Fatalf("Submit %d: %v", i, err)
		}
		accepted++
	}
	if accepted == 0 {
		t.Fatal("the pool accepted nothing")
	}
	if accepted > 4 {
		t.Errorf("accepted %d tasks, want the pending bound of 4 to apply", accepted)
	}
}

func TestPoolFailsTaskWhenNoSessionIsAvailable(t *testing.T) {
	exec := &fakeExecutor{conversationID: "conv-1"}
	pool, accounts, _ := newTestPool(t, exec, Config{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pool.Start(ctx)
	defer pool.Close()

	// Disable the only account, then reload so the pool sees it.
	if err := os.WriteFile(accounts.cfg.AccountsFile,
		[]byte(`[{"label":"a","cookie":"sessionid=aaa","enabled":false}]`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := accounts.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	task, err := pool.Submit(SubmitRequest{Prompt: "a cat"})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	done := waitForState(t, pool, task.ID, TaskFailed)
	if !contains(done.Error, "no session available") {
		t.Errorf("error = %q, want it to name the cause", done.Error)
	}
	if exec.callCount() != 0 {
		t.Errorf("the executor ran %d time(s) with no session", exec.callCount())
	}
}

func TestPoolCoolsDownTheAccountAfterAFailedSubmit(t *testing.T) {
	exec := &fakeExecutor{err: errors.New("upstream said no")}
	pool, accounts, _ := newTestPool(t, exec, Config{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pool.Start(ctx)
	defer pool.Close()

	task, err := pool.Submit(SubmitRequest{Prompt: "a cat"})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	done := waitForState(t, pool, task.ID, TaskFailed)
	if !contains(done.Error, "upstream said no") {
		t.Errorf("error = %q, want the executor's cause", done.Error)
	}

	health := accounts.Health(time.Now())[0].(map[string]any)
	if health["available"] != false {
		t.Error("the account was not parked after a failed submit")
	}
	if health["cooldown_remaining"].(int) <= 0 {
		t.Errorf("cooldown_remaining = %v", health["cooldown_remaining"])
	}
}

func TestPoolFailsTaskWhenTheVideoNeverArrives(t *testing.T) {
	exec := &fakeExecutor{conversationID: "conv-1"}

	empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"downlink_body":{"pull_singe_chain_downlink_body":{"messages":[]}}}`))
	}))
	defer empty.Close()

	pool, _, _ := newTestPool(t, exec, Config{})
	pool.upstream.base = empty.URL
	pool.cfg.VideoTimeout = 150 * time.Millisecond
	pool.cfg.PollInterval = 10 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pool.Start(ctx)
	defer pool.Close()

	task, err := pool.Submit(SubmitRequest{Prompt: "a cat"})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	done := waitForState(t, pool, task.ID, TaskFailed)
	if !contains(done.Error, "timed out") {
		t.Errorf("error = %q, want a timeout", done.Error)
	}
}

func TestPoolGetReturnsASnapshot(t *testing.T) {
	exec := &fakeExecutor{conversationID: "conv-1"}
	pool, _, _ := newTestPool(t, exec, Config{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pool.Start(ctx)
	defer pool.Close()

	task, err := pool.Submit(SubmitRequest{Prompt: "a cat"})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	snapshot := waitForState(t, pool, task.ID, TaskSucceeded)

	// Mutating the snapshot must not reach the pool's copy.
	snapshot.Media[0].URL = "tampered"
	again, _ := pool.Get(task.ID)
	if again.Media[0].URL == "tampered" {
		t.Error("Get returned a live reference instead of a snapshot")
	}

	if _, ok := pool.Get("no-such-task"); ok {
		t.Error("Get reported an unknown id as present")
	}
}

func TestPoolCloseStopsAcceptingWork(t *testing.T) {
	exec := &fakeExecutor{conversationID: "conv-1"}
	pool, _, _ := newTestPool(t, exec, Config{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pool.Start(ctx)

	pool.Close()
	if _, err := pool.Submit(SubmitRequest{Prompt: "a cat"}); !errors.Is(err, ErrPoolClosed) {
		t.Errorf("Submit after Close = %v, want ErrPoolClosed", err)
	}
	// Close must be idempotent: a second call must not block or panic.
	pool.Close()
}

func TestPoolEvictsFinishedTasksPastTTL(t *testing.T) {
	exec := &fakeExecutor{conversationID: "conv-1"}
	pool, _, _ := newTestPool(t, exec, Config{})
	pool.cfg.TaskTTL = time.Nanosecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pool.Start(ctx)
	defer pool.Close()

	first, err := pool.Submit(SubmitRequest{Prompt: "a cat"})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	waitForState(t, pool, first.ID, TaskSucceeded)

	// A second submission runs the eviction pass and should drop the first.
	time.Sleep(2 * time.Millisecond)
	second, err := pool.Submit(SubmitRequest{Prompt: "a dog"})
	if err != nil {
		t.Fatalf("Submit 2: %v", err)
	}
	waitForState(t, pool, second.ID, TaskSucceeded)

	if _, ok := pool.Get(first.ID); ok {
		t.Error("a finished task past its TTL was kept")
	}
}

// contains comes from health_test.go, which declares the same helper for the
// /health assertions.
