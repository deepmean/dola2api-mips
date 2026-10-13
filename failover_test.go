package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestModelsEndpointListsTheConfiguredModels(t *testing.T) {
	exec := &fakeExecutor{conversationID: "conv-1"}
	api, _, _ := newTestAPI(t, exec, Config{})

	rec := doRequest(t, apiRouter(api), http.MethodGet, "/v1/models", "", "sk-test")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (this was a 404 before the endpoint existed)", rec.Code)
	}
	var listed modelListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if listed.Object != "list" {
		t.Errorf("object = %q, want list", listed.Object)
	}
	if len(listed.Data) != 1 {
		t.Fatalf("models = %+v, want the single default", listed.Data)
	}
	model := listed.Data[0]
	if model.ID != defaultVideoModel {
		t.Errorf("id = %q, want %q", model.ID, defaultVideoModel)
	}
	if model.Object != "model" || model.OwnedBy == "" {
		t.Errorf("model = %+v", model)
	}
	if len(model.Ratios) == 0 || len(model.Durations) == 0 {
		t.Errorf("the listing does not describe what the service accepts: %+v", model)
	}
}

func TestModelsEndpointHonoursTheConfiguredList(t *testing.T) {
	exec := &fakeExecutor{conversationID: "conv-1"}
	cfg := Config{VideoModels: []string{"seedance_v2.0", "seedance_v2.5"}}
	api, _, _ := newTestAPI(t, exec, cfg)

	rec := doRequest(t, apiRouter(api), http.MethodGet, "/v1/models", "", "sk-test")
	var listed modelListResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &listed)
	if len(listed.Data) != 2 {
		t.Fatalf("models = %+v, want both configured", listed.Data)
	}
}

func TestModelsEndpointRequiresAKey(t *testing.T) {
	exec := &fakeExecutor{conversationID: "conv-1"}
	api, _, _ := newTestAPI(t, exec, Config{})

	rec := doRequest(t, apiRouter(api), http.MethodGet, "/v1/models", "", "")
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401; leaving one route open is how a service ends up half-protected", rec.Code)
	}
}

func TestCreateRejectsAnUnknownModel(t *testing.T) {
	exec := &fakeExecutor{conversationID: "conv-1"}
	api, _, _ := newTestAPI(t, exec, Config{})

	rec := doRequest(t, apiRouter(api), http.MethodPost, "/v1/videos",
		`{"prompt":"a cat","model":"not-a-model"}`, "sk-test")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for an unknown model", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "/v1/models") {
		t.Errorf("the error does not point at the listing: %s", rec.Body.String())
	}
}

func TestCreateAcceptsAConfiguredModel(t *testing.T) {
	exec := &fakeExecutor{conversationID: "conv-1"}
	cfg := Config{VideoModels: []string{"seedance_v2.5"}}
	api, pool, _ := newTestAPI(t, exec, cfg)
	ctx, cancel := newTestContext()
	defer cancel()
	pool.Start(ctx)
	defer pool.Close()

	rec := doRequest(t, apiRouter(api), http.MethodPost, "/v1/videos",
		`{"prompt":"a cat","model":"seedance_v2.5"}`, "sk-test")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
}

// Failover: a task must move to another session rather than failing on the
// first refusal, and the session that refused must be parked so the retry
// genuinely moves on.
func TestPoolFailsOverToAnotherSession(t *testing.T) {
	exec := &failFirstExecutor{
		failing:  "sessionid=aaa",
		convID:   "conv-ok",
		failWith: errTest,
	}
	pool, accounts, _ := newTestPoolWithAccounts(t, exec,
		`[{"label":"bad","cookie":"sessionid=aaa"},{"label":"good","cookie":"sessionid=bbb"}]`)
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
		t.Errorf("conversation = %q, want the one the working session returned", done.ConversationID)
	}
	if exec.calls() < 2 {
		t.Errorf("the executor was called %d time(s); failover should have retried", exec.calls())
	}

	// The session that failed is parked, so it will not be picked again.
	health := accounts.Health(time.Now())
	for _, entry := range health {
		m := entry.(map[string]any)
		if m["label"] == "bad" && m["available"] != false {
			t.Errorf("the failing session was not parked: %v", m)
		}
	}
}

// With failover disabled, the first refusal fails the task.
func TestPoolWithoutFailoverFailsOnTheFirstRefusal(t *testing.T) {
	exec := &fakeExecutor{err: errTest}
	pool, _, _ := newTestPoolWithAccounts(t, exec,
		`[{"label":"a","cookie":"sessionid=aaa"},{"label":"b","cookie":"sessionid=bbb"}]`)
	pool.cfg.SubmitAttempts = 1
	ctx, cancel := newTestContext()
	defer cancel()
	pool.Start(ctx)
	defer pool.Close()

	task, err := pool.Submit(SubmitRequest{Prompt: "a cat"})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	done := waitForState(t, pool, task.ID, TaskFailed)
	if !strings.Contains(done.Error, errTest.Error()) {
		t.Errorf("error = %q", done.Error)
	}
	if exec.callCount() != 1 {
		t.Errorf("the executor was called %d time(s) with failover disabled", exec.callCount())
	}
}

// Every session refusing must report the last reason and say how many were
// tried, rather than looping or claiming there was no session at all.
func TestPoolReportsWhenEverySessionFails(t *testing.T) {
	exec := &fakeExecutor{err: errTest}
	pool, _, _ := newTestPoolWithAccounts(t, exec,
		`[{"label":"a","cookie":"sessionid=aaa"},{"label":"b","cookie":"sessionid=bbb"}]`)
	ctx, cancel := newTestContext()
	defer cancel()
	pool.Start(ctx)
	defer pool.Close()

	task, err := pool.Submit(SubmitRequest{Prompt: "a cat"})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	done := waitForState(t, pool, task.ID, TaskFailed)
	if !strings.Contains(done.Error, "every session failed") {
		t.Errorf("error = %q, want it to say every session was tried", done.Error)
	}
	if !strings.Contains(done.Error, errTest.Error()) {
		t.Errorf("error = %q, want the last cause", done.Error)
	}
	if exec.callCount() != 2 {
		t.Errorf("the executor was called %d time(s), want one per session", exec.callCount())
	}
}

func TestAccountStrategyRoundRobinAndRandom(t *testing.T) {
	cfg := testAccountsConfig(t)
	writeAccounts(t, cfg, `[
	  {"label":"a","cookie":"sessionid=aaa"},
	  {"label":"b","cookie":"sessionid=bbb"},
	  {"label":"c","cookie":"sessionid=ccc"}
	]`)

	cfg.AccountStrategy = "round-robin"
	store := NewAccountStore(cfg)
	if err := store.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	now := time.Now()
	seen := map[string]int{}
	for i := 0; i < 6; i++ {
		a, err := store.Pick(now)
		if err != nil {
			t.Fatalf("Pick %d: %v", i, err)
		}
		seen[a.Label]++
	}
	if len(seen) != 3 {
		t.Errorf("round-robin used %d distinct sessions in 6 picks: %v", len(seen), seen)
	}
	for label, count := range seen {
		if count != 2 {
			t.Errorf("round-robin gave %q %d of 6 picks, want 2", label, count)
		}
	}

	cfg.AccountStrategy = "random"
	random := NewAccountStore(cfg)
	if err := random.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	for i := 0; i < 20; i++ {
		if _, err := random.Pick(now); err != nil {
			t.Fatalf("random Pick %d: %v", i, err)
		}
	}
}

// Round-robin must skip a parked session rather than returning it.
func TestAccountStrategySkipsParkedSessions(t *testing.T) {
	cfg := testAccountsConfig(t)
	cfg.AccountStrategy = "round-robin"
	writeAccounts(t, cfg, `[
	  {"label":"a","cookie":"sessionid=aaa"},
	  {"label":"b","cookie":"sessionid=bbb"}
	]`)
	store := NewAccountStore(cfg)
	if err := store.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	now := time.Now()

	first, err := store.Pick(now)
	if err != nil {
		t.Fatalf("Pick: %v", err)
	}
	store.RecordFailure(first, now, errTest)

	for i := 0; i < 4; i++ {
		next, err := store.Pick(now)
		if err != nil {
			t.Fatalf("Pick after parking: %v", err)
		}
		if next.Label == first.Label {
			t.Fatalf("a parked session was picked again: %q", next.Label)
		}
	}
}

// A session that keeps failing is parked for longer each time, so a broken
// session is not retried at the same rate forever.
func TestConsecutiveFailuresBackOff(t *testing.T) {
	cfg := testAccountsConfig(t)
	cfg.CooldownSec = 60
	writeAccounts(t, cfg, `[{"label":"a","cookie":"sessionid=aaa"}]`)
	store := NewAccountStore(cfg)
	if err := store.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	now := time.Now()
	acct, err := store.Pick(now)
	if err != nil {
		t.Fatalf("Pick: %v", err)
	}

	remaining := func() int {
		return store.Health(now)[0].(map[string]any)["cooldown_remaining"].(int)
	}

	store.RecordFailure(acct, now, errTest)
	first := remaining()
	store.RecordFailure(acct, now, errTest)
	second := remaining()
	store.RecordFailure(acct, now, errTest)
	third := remaining()

	if !(first < second && second < third) {
		t.Errorf("cooldown did not grow: %d then %d then %d", first, second, third)
	}
	if third > 60*maxCooldownMultiplier+1 {
		t.Errorf("cooldown %ds exceeds the cap of %dx the base", third, maxCooldownMultiplier)
	}

	// A success clears the backoff.
	store.RecordSuccess(acct, now)
	store.RecordFailure(acct, now, errTest)
	if got := remaining(); got > first+1 {
		t.Errorf("a success did not reset the backoff: %ds, want about %ds", got, first)
	}
}

// failFirstExecutor refuses for one specific cookie and accepts everything else,
// which is the shape of a session that has been rate-limited.
type failFirstExecutor struct {
	mu       sync.Mutex
	failing  string
	convID   string
	failWith error
	n        int
}

func (f *failFirstExecutor) Name() string { return "fail-first" }

func (f *failFirstExecutor) Submit(_ context.Context, cookie string, _ SubmitRequest) (string, error) {
	f.mu.Lock()
	f.n++
	f.mu.Unlock()
	if strings.Contains(cookie, f.failing) {
		return "", f.failWith
	}
	return f.convID, nil
}

func (f *failFirstExecutor) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.n
}

// newTestPoolWithAccounts is newTestPool with a caller-supplied account list.
func newTestPoolWithAccounts(t *testing.T, exec Executor, accountsJSON string) (*Pool, *AccountStore, *httptest.Server) {
	t.Helper()
	pool, accounts, srv := newTestPool(t, exec, Config{})
	if err := os.WriteFile(accounts.cfg.AccountsFile, []byte(accountsJSON), 0o600); err != nil {
		t.Fatalf("write accounts: %v", err)
	}
	if err := accounts.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	return pool, accounts, srv
}
