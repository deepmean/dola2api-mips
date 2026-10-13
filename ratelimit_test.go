package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// The two refusal codes mean "too many submits", and a retry spends the same
// budget for the same answer. Measured: the first submit of a session is
// accepted and the next three are refused, across every account.
func TestRateLimitedRefusalsAreRecognised(t *testing.T) {
	for _, code := range []string{"710022002", "710022004"} {
		stream := fmt.Sprintf("id: 0\nevent: STREAM_ERROR\ndata: {\"error_code\":%s,\"error_msg\":\"refused\"}\n\n", code)
		_, err := readAck(bufio.NewReader(strings.NewReader(stream)))
		if err == nil {
			t.Fatalf("%s was reported as success", code)
		}
		if !errors.Is(err, ErrUpstreamRateLimited) {
			t.Errorf("%s was not classified as a rate limit: %v", code, err)
		}
		if !strings.Contains(err.Error(), code) {
			t.Errorf("%s is missing from the message: %v", code, err)
		}
	}
}

// Any other refusal is about the session, so failover is the right response and
// it must not be classified as a rate limit.
func TestOtherRefusalsAreNotRateLimits(t *testing.T) {
	stream := "id: 0\nevent: STREAM_ERROR\ndata: {\"error_code\":710012001,\"error_msg\":\"login invalid\"}\n\n"
	_, err := readAck(bufio.NewReader(strings.NewReader(stream)))
	if err == nil {
		t.Fatal("a refusal was reported as success")
	}
	if errors.Is(err, ErrUpstreamRateLimited) {
		t.Errorf("a login failure was classified as a rate limit: %v", err)
	}
}

// rateLimitExecutor refuses every submit the way the upstream does when it is
// refusing by rate rather than by session.
type rateLimitExecutor struct {
	mu    sync.Mutex
	calls int
}

func (f *rateLimitExecutor) Name() string { return "rate-limited" }

func (f *rateLimitExecutor) Submit(context.Context, string, SubmitRequest) (string, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	return "", fmt.Errorf("%w: 710022004 rate limited", ErrUpstreamRateLimited)
}

func (f *rateLimitExecutor) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// The point of the classification: three sessions that are all refused for the
// same rate reason must cost one submit, not three. Measured on the device, the
// failover burned three submits and then failed anyway.
func TestPoolDoesNotFailOverOnARateLimitRefusal(t *testing.T) {
	exec := &rateLimitExecutor{}
	pool, _, _ := newTestPoolWithAccounts(t, exec,
		`[{"label":"a","cookie":"sessionid=aaa"},{"label":"b","cookie":"sessionid=bbb"},{"label":"c","cookie":"sessionid=ccc"}]`)
	pool.cfg.SubmitInterval = 0 // pacing is a separate behaviour
	ctx, cancel := newTestContext()
	defer cancel()
	pool.Start(ctx)
	defer pool.Close()

	task, err := pool.Submit(SubmitRequest{Prompt: "a cat"})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	done := waitForState(t, pool, task.ID, TaskFailed)

	if exec.callCount() != 1 {
		t.Errorf("the executor was called %d time(s); a rate limit is not a per-session fault, "+
			"so retrying spends the same budget for the same answer", exec.callCount())
	}
	if !errors.Is(errors.New(done.Error), ErrUpstreamRateLimited) &&
		!strings.Contains(done.Error, "rate") {
		t.Errorf("error = %q, want it to say the refusal was about rate", done.Error)
	}
	if !strings.Contains(done.Error, "another session would not help") {
		t.Errorf("error = %q, want it to explain why there was no failover", done.Error)
	}
}

// A per-session fault must still fail over: the classification must not have
// disabled the failover that already worked.
func TestPoolStillFailsOverOnAPerSessionFault(t *testing.T) {
	exec := &failFirstExecutor{failing: "sessionid=aaa", convID: "conv-ok", failWith: errTest}
	pool, _, _ := newTestPoolWithAccounts(t, exec,
		`[{"label":"bad","cookie":"sessionid=aaa"},{"label":"good","cookie":"sessionid=bbb"}]`)
	pool.cfg.SubmitInterval = 0
	ctx, cancel := newTestContext()
	defer cancel()
	pool.Start(ctx)
	defer pool.Close()

	task, err := pool.Submit(SubmitRequest{Prompt: "a cat"})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	done := waitForState(t, pool, task.ID, TaskSucceeded)
	if done.ConversationID != "conv-ok" {
		t.Errorf("conversation = %q", done.ConversationID)
	}
	if exec.calls() < 2 {
		t.Errorf("the executor was called %d time(s), want a retry", exec.calls())
	}
}

// timedExecutor records when each submit went out.
type timedExecutor struct {
	mu      sync.Mutex
	stamps  []time.Time
	convID  string
	onFirst func()
}

func (f *timedExecutor) Name() string { return "timed" }

func (f *timedExecutor) Submit(context.Context, string, SubmitRequest) (string, error) {
	f.mu.Lock()
	f.stamps = append(f.stamps, time.Now())
	first := len(f.stamps) == 1
	f.mu.Unlock()
	if first && f.onFirst != nil {
		f.onFirst()
	}
	return f.convID, nil
}

func (f *timedExecutor) times() []time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]time.Time(nil), f.stamps...)
}

// Submits must be spaced out: the refusal is about submit rate, so sending them
// back to back is what triggers it.
func TestPoolPacesSubmits(t *testing.T) {
	const interval = 400 * time.Millisecond
	exec := &timedExecutor{convID: "conv-1"}
	pool, _, _ := newTestPoolWithAccounts(t, exec, `[{"label":"a","cookie":"sessionid=aaa"}]`)
	pool.cfg.SubmitInterval = interval
	ctx, cancel := newTestContext()
	defer cancel()
	pool.Start(ctx)
	defer pool.Close()

	first, err := pool.Submit(SubmitRequest{Prompt: "a cat"})
	if err != nil {
		t.Fatalf("Submit 1: %v", err)
	}
	waitForState(t, pool, first.ID, TaskSucceeded)

	second, err := pool.Submit(SubmitRequest{Prompt: "a dog"})
	if err != nil {
		t.Fatalf("Submit 2: %v", err)
	}
	waitForState(t, pool, second.ID, TaskSucceeded)

	stamps := exec.times()
	if len(stamps) != 2 {
		t.Fatalf("submits = %d, want 2", len(stamps))
	}
	gap := stamps[1].Sub(stamps[0])
	if gap < interval {
		t.Errorf("the two submits were %s apart, want at least %s", gap.Round(time.Millisecond), interval)
	}
}

// Pacing must be off by default in tests that do not ask for it, so it does not
// slow every other test down.
func TestPacingIsSkippedWhenTheIntervalIsZero(t *testing.T) {
	exec := &timedExecutor{convID: "conv-1"}
	pool, _, _ := newTestPoolWithAccounts(t, exec, `[{"label":"a","cookie":"sessionid=aaa"}]`)
	pool.cfg.SubmitInterval = 0
	ctx, cancel := newTestContext()
	defer cancel()
	pool.Start(ctx)
	defer pool.Close()

	start := time.Now()
	task, err := pool.Submit(SubmitRequest{Prompt: "a cat"})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	waitForState(t, pool, task.ID, TaskSucceeded)
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("a task took %s with pacing off", elapsed.Round(time.Millisecond))
	}
}
