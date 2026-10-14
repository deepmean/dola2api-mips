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

// The two codes mean "this session cannot generate right now", which is worth
// reporting distinctly: an operator who sees it waits for the allowance to reset
// or adds a session, rather than looking for a bug in the request.
func TestQuotaRefusalsAreRecognised(t *testing.T) {
	for _, code := range []string{"710022002", "710022004"} {
		stream := fmt.Sprintf("id: 0\nevent: STREAM_ERROR\ndata: {\"error_code\":%s,\"error_msg\":\"refused\"}\n\n", code)
		_, err := readAck(bufio.NewReader(strings.NewReader(stream)))
		if err == nil {
			t.Fatalf("%s was reported as success", code)
		}
		if !errors.Is(err, ErrUpstreamRateLimited) {
			t.Errorf("%s was not classified: %v", code, err)
		}
		if !strings.Contains(err.Error(), code) {
			t.Errorf("%s is missing from the message: %v", code, err)
		}
	}
}

// Any other refusal is a different condition and must not be folded in with it.
func TestOtherRefusalsAreNotClassifiedAsQuota(t *testing.T) {
	stream := "id: 0\nevent: STREAM_ERROR\ndata: {\"error_code\":710012001,\"error_msg\":\"login invalid\"}\n\n"
	_, err := readAck(bufio.NewReader(strings.NewReader(stream)))
	if err == nil {
		t.Fatal("a refusal was reported as success")
	}
	if errors.Is(err, ErrUpstreamRateLimited) {
		t.Errorf("a login failure was classified as a quota refusal: %v", err)
	}
}

// quotaExecutor refuses every submit the way a session with no allowance left does.
type quotaExecutor struct {
	mu    sync.Mutex
	calls int
}

func (f *quotaExecutor) Name() string { return "out-of-quota" }

func (f *quotaExecutor) Submit(context.Context, string, SubmitRequest) (string, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	return "", fmt.Errorf("%w: 710022004 rate limited", ErrUpstreamRateLimited)
}

func (f *quotaExecutor) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// A refusal of this kind is a property of the session, so every session must
// still be tried before the task gives up. Polling the one conversation a submit
// did create showed why: the reply was 浠婂ぉ鐨勭敓鎴愭鏁稿凡缍撻仈鍒颁笂闄?鈥?the account's
// daily allowance is spent, which is per-session, not a statement about the path.
func TestPoolStillFailsOverOnAQuotaRefusal(t *testing.T) {
	exec := &quotaExecutor{}
	pool, _, _ := newTestPoolWithAccounts(t, exec,
		`[{"label":"a","cookie":"sessionid=aaa"},{"label":"b","cookie":"sessionid=bbb"},{"label":"c","cookie":"sessionid=ccc"}]`)
	pool.cfg.SubmitInterval = 0
	ctx, cancel := newTestContext()
	defer cancel()
	pool.Start(ctx)
	defer pool.Close()

	task, err := pool.Submit(SubmitRequest{Prompt: "a cat"})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	done := waitForState(t, pool, task.ID, TaskFailed)

	if exec.callCount() != 3 {
		t.Errorf("the executor was called %d time(s), want one per session: a spent allowance "+
			"is per-session, so the other two might still have one", exec.callCount())
	}
	if !strings.Contains(done.Error, "every session failed") {
		t.Errorf("error = %q, want it to say every session was tried", done.Error)
	}
}

// A per-session fault must still fail over: the classification must not have
// disabled the failover that already worked.
// An upstream-wide refusal must not park the sessions.
//
// Measured: the browser hits the same codes, and all four sessions return the
// same one. Parking them anyway drains the pool one session at a time until
// nothing is available, and the service then reports "no session available" — a
// symptom that hides its own cause. That is exactly what happened on the device:
// a client got 503 "no session available" while every session was in fact fine.
func TestUpstreamWideRefusalDoesNotParkSessions(t *testing.T) {
	exec := &quotaExecutor{}
	pool, accounts, _ := newTestPoolWithAccounts(t, exec,
		`[{"label":"a","cookie":"sessionid=aaa"},{"label":"b","cookie":"sessionid=bbb"},{"label":"c","cookie":"sessionid=ccc"}]`)
	pool.cfg.SubmitInterval = 0
	ctx, cancel := newTestContext()
	defer cancel()
	pool.Start(ctx)
	defer pool.Close()

	task, err := pool.Submit(SubmitRequest{Prompt: "a cat"})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	waitForState(t, pool, task.ID, TaskFailed)

	if !accounts.Available(time.Now()) {
		t.Error("an upstream-wide refusal parked every session, which drains the pool " +
			"and turns a clear cause into 'no session available'")
	}
	for _, entry := range accounts.Health(time.Now()) {
		m := entry.(map[string]any)
		if remaining := m["cooldown_remaining"].(int); remaining != 0 {
			t.Errorf("%v is parked for %ds after an upstream-wide refusal", m["label"], remaining)
		}
	}
}

// A per-session fault must still park the session that caused it, or a broken
// session is retried at the same rate forever.
func TestPerSessionFaultStillParksTheSession(t *testing.T) {
	exec := &fakeExecutor{err: errTest}
	pool, accounts, _ := newTestPoolWithAccounts(t, exec,
		`[{"label":"a","cookie":"sessionid=aaa"},{"label":"b","cookie":"sessionid=bbb"}]`)
	pool.cfg.SubmitInterval = 0
	ctx, cancel := newTestContext()
	defer cancel()
	pool.Start(ctx)
	defer pool.Close()

	task, err := pool.Submit(SubmitRequest{Prompt: "a cat"})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	waitForState(t, pool, task.ID, TaskFailed)

	for _, entry := range accounts.Health(time.Now()) {
		m := entry.(map[string]any)
		if remaining := m["cooldown_remaining"].(int); remaining == 0 {
			t.Errorf("%v was not parked after a per-session fault", m["label"])
		}
	}
}

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
	// A little tolerance: the timer fires at the interval and the two timestamps
	// are taken either side of the actual request, so the gap lands on the
	// boundary and clock granularity can put it a hair under.
	if tolerance := 20 * time.Millisecond; gap < interval-tolerance {
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
