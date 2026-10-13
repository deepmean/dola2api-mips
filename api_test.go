package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newTestAPI builds an API over a pool whose executor is fake.
func newTestAPI(t *testing.T, exec Executor, cfg Config) (*api, *Pool, *AccountStore) {
	t.Helper()
	pool, accounts, _ := newTestPool(t, exec, cfg)
	cfg.APIKeys = []string{"sk-test"}
	cfg.AdminKey = "admin-test"

	egress, err := NewEgress(Config{EgressMode: "direct"})
	if err != nil {
		t.Fatalf("NewEgress: %v", err)
	}
	t.Cleanup(egress.Close)
	retriever, err := NewRetriever(Config{StoreMode: "redirect", MirrorDir: t.TempDir()}, egress)
	if err != nil {
		t.Fatalf("NewRetriever: %v", err)
	}
	guard := NewGuard(0, func() {}, false)
	return newAPI(cfg, guard, pool, retriever), pool, accounts
}

func doRequest(t *testing.T, handler http.Handler, method, path, body, token string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestAPIRefusesWithoutKeys(t *testing.T) {
	exec := &fakeExecutor{conversationID: "conv-1"}
	pool, _, _ := newTestPool(t, exec, Config{})
	cfg := Config{} // no keys at all
	api := newAPI(cfg, NewGuard(0, func() {}, false), pool, nil)

	rec := doRequest(t, apiRouter(api), http.MethodPost, "/v1/videos", `{"prompt":"a cat"}`, "")
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 when no keys are configured", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "no_api_keys") {
		t.Errorf("body = %s", rec.Body.String())
	}
}

func TestAPIRejectsBadTokens(t *testing.T) {
	exec := &fakeExecutor{conversationID: "conv-1"}
	api, _, _ := newTestAPI(t, exec, Config{})
	router := apiRouter(api)

	cases := []struct {
		name  string
		token string
	}{
		{"no token", ""},
		{"wrong token", "sk-wrong"},
		{"prefix only", "Bearer"},
	}
	for _, c := range cases {
		rec := doRequest(t, router, http.MethodPost, "/v1/videos", `{"prompt":"a cat"}`, c.token)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: status = %d, want 401", c.name, rec.Code)
		}
		if got := rec.Header().Get("WWW-Authenticate"); got == "" {
			t.Errorf("%s: no WWW-Authenticate header", c.name)
		}
	}
}

func TestAPIAcceptsAValidKeyAndReturnsATask(t *testing.T) {
	exec := &fakeExecutor{conversationID: "conv-1"}
	api, pool, _ := newTestAPI(t, exec, Config{})
	ctx, cancel := newTestContext()
	defer cancel()
	pool.Start(ctx)
	defer pool.Close()

	rec := doRequest(t, apiRouter(api), http.MethodPost, "/v1/videos",
		`{"prompt":"a cat","ratio":"9:16","duration":10}`, "sk-test")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202: %s", rec.Code, rec.Body.String())
	}
	var task apiTask
	if err := json.Unmarshal(rec.Body.Bytes(), &task); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if task.ID == "" {
		t.Error("no task id was returned")
	}
	if task.State != TaskPending && task.State != TaskRunning {
		t.Errorf("state = %q, want pending or running", task.State)
	}

	// Polling the task should eventually surface the video.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		getRec := doRequest(t, apiRouter(api), http.MethodGet, "/v1/videos/"+task.ID, "", "sk-test")
		if getRec.Code != http.StatusOK {
			t.Fatalf("GET status = %d", getRec.Code)
		}
		var polled apiTask
		if err := json.Unmarshal(getRec.Body.Bytes(), &polled); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if polled.State == TaskSucceeded {
			if len(polled.Videos) != 1 {
				t.Fatalf("videos = %+v", polled.Videos)
			}
			if polled.Videos[0].URL == "" {
				t.Error("the video has no URL")
			}
			if polled.Videos[0].ContentURL != "/v1/videos/"+task.ID+"/content" {
				t.Errorf("content_url = %q", polled.Videos[0].ContentURL)
			}
			return
		}
		if polled.State == TaskFailed {
			t.Fatalf("task failed: %s", polled.Error)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the task never finished")
}

func TestAPIValidatesTheRequest(t *testing.T) {
	exec := &fakeExecutor{conversationID: "conv-1"}
	api, _, _ := newTestAPI(t, exec, Config{})
	router := apiRouter(api)

	cases := []struct {
		name string
		body string
		want int
	}{
		{"not json", `nonsense`, http.StatusBadRequest},
		{"no prompt", `{"ratio":"9:16"}`, http.StatusBadRequest},
		{"blank prompt", `{"prompt":"   "}`, http.StatusBadRequest},
		{"bad ratio", `{"prompt":"x","ratio":"sixteen by nine"}`, http.StatusBadRequest},
		{"ratio with letters", `{"prompt":"x","ratio":"9:x"}`, http.StatusBadRequest},
		{"duration too long", `{"prompt":"x","duration":600}`, http.StatusBadRequest},
		{"duration negative", `{"prompt":"x","duration":-5}`, http.StatusBadRequest},
		{"model too long", `{"prompt":"x","model":"` + strings.Repeat("m", 100) + `"}`, http.StatusBadRequest},
	}
	for _, c := range cases {
		rec := doRequest(t, router, http.MethodPost, "/v1/videos", c.body, "sk-test")
		if rec.Code != c.want {
			t.Errorf("%s: status = %d, want %d (%s)", c.name, rec.Code, c.want, rec.Body.String())
		}
	}
}

func TestAPIAppliesDefaults(t *testing.T) {
	exec := &fakeExecutor{conversationID: "conv-1"}
	api, pool, _ := newTestAPI(t, exec, Config{})
	ctx, cancel := newTestContext()
	defer cancel()
	pool.Start(ctx)
	defer pool.Close()

	rec := doRequest(t, apiRouter(api), http.MethodPost, "/v1/videos", `{"prompt":"a cat"}`, "sk-test")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var task apiTask
	_ = json.Unmarshal(rec.Body.Bytes(), &task)

	stored, ok := pool.Get(task.ID)
	if !ok {
		t.Fatal("the task is not in the pool")
	}
	if stored.Request.Ratio != "9:16" || stored.Request.Model != "seedance_v2.0" || stored.Request.Duration != 10 {
		t.Errorf("defaults were not applied: %+v", stored.Request)
	}
}

func TestAPIGetUnknownTask(t *testing.T) {
	exec := &fakeExecutor{conversationID: "conv-1"}
	api, _, _ := newTestAPI(t, exec, Config{})
	rec := doRequest(t, apiRouter(api), http.MethodGet, "/v1/videos/nope", "", "sk-test")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestAPIContentBeforeTheVideoExists(t *testing.T) {
	exec := &fakeExecutor{conversationID: "conv-1", block: make(chan struct{})}
	api, pool, _ := newTestAPI(t, exec, Config{})
	ctx, cancel := newTestContext()
	defer cancel()
	pool.Start(ctx)
	defer pool.Close()

	rec := doRequest(t, apiRouter(api), http.MethodPost, "/v1/videos", `{"prompt":"a cat"}`, "sk-test")
	var task apiTask
	_ = json.Unmarshal(rec.Body.Bytes(), &task)

	contentRec := doRequest(t, apiRouter(api), http.MethodGet, "/v1/videos/"+task.ID+"/content", "", "sk-test")
	if contentRec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409 before the video exists", contentRec.Code)
	}
}

func TestAPIAdminKeyAlsoAuthorizes(t *testing.T) {
	exec := &fakeExecutor{conversationID: "conv-1"}
	api, _, _ := newTestAPI(t, exec, Config{})
	rec := doRequest(t, apiRouter(api), http.MethodGet, "/v1/videos/anything", "", "admin-test")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (authorised, task absent)", rec.Code)
	}
}

func TestAPIRejectsAnOversizedBody(t *testing.T) {
	exec := &fakeExecutor{conversationID: "conv-1"}
	api, _, _ := newTestAPI(t, exec, Config{})
	// The reader is capped, so a body past the cap is truncated and then fails
	// to parse; either way it must not be accepted.
	huge := `{"prompt":"` + strings.Repeat("x", maxAPIRequestBytes+1024) + `"}`
	rec := doRequest(t, apiRouter(api), http.MethodPost, "/v1/videos", huge, "sk-test")
	if rec.Code == http.StatusAccepted {
		t.Error("an oversized body was accepted")
	}
}

func TestAPIHealthStaysOpen(t *testing.T) {
	cfg := Config{APIKeys: []string{"sk-test"}}
	guard := NewGuard(0, func() {}, false)
	srv := newServer(cfg, guard)
	srv.egressState = func() egressHealth { return egressHealth{Mode: "relay", Active: true} }

	rec := doRequest(t, srv.mux(), http.MethodGet, "/health", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("health status = %d, want 200 without a key", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "sk-test") {
		t.Error("/health leaked an API key")
	}
}

// apiRouter mounts the API the way the server does.
func apiRouter(a *api) http.Handler {
	mux := http.NewServeMux()
	a.register(mux)
	return mux
}

func newTestContext() (context.Context, context.CancelFunc) {
	return context.WithCancel(context.Background())
}
