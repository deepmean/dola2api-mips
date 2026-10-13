package main

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
)

func TestAdminRequiresTheAdminKeyNotAnAPIKey(t *testing.T) {
	exec := &fakeExecutor{conversationID: "conv-1"}
	api, _, _ := newTestAPI(t, exec, Config{})
	router := apiRouter(api)

	// An ordinary API key must not reach the operator routes: whoever can
	// reload accounts can point the service at a different session entirely.
	cases := []struct {
		name  string
		token string
		want  int
	}{
		{"no token", "", http.StatusUnauthorized},
		{"api key", "sk-test", http.StatusUnauthorized},
		{"wrong admin key", "admin-wrong", http.StatusUnauthorized},
		{"admin key", "admin-test", http.StatusOK},
	}
	for _, c := range cases {
		rec := doRequest(t, router, http.MethodGet, "/admin/status", "", c.token)
		if rec.Code != c.want {
			t.Errorf("%s: status = %d, want %d", c.name, rec.Code, c.want)
		}
	}
}

func TestAdminRefusesWhenNoAdminKeyIsConfigured(t *testing.T) {
	exec := &fakeExecutor{conversationID: "conv-1"}
	pool, accounts, _ := newTestPool(t, exec, Config{})
	cfg := Config{APIKeys: []string{"sk-test"}} // deliberately no admin key
	api := newAPI(cfg, NewGuard(0, func() {}, false), pool, nil, accounts, nil)

	rec := doRequest(t, apiRouter(api), http.MethodGet, "/admin/status", "", "anything")
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 when no admin key is set", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "no_admin_key") {
		t.Errorf("body = %s", rec.Body.String())
	}
}

func TestAdminStatusReportsTheWholePicture(t *testing.T) {
	exec := &fakeExecutor{conversationID: "conv-1"}
	api, _, _ := newTestAPI(t, exec, Config{})
	api.egressState = func() egressHealth {
		return egressHealth{Mode: "relay", Active: true, Strict: true}
	}

	rec := doRequest(t, apiRouter(api), http.MethodGet, "/admin/status", "", "admin-test")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var status adminStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !status.OK || status.Egress.Mode != "relay" || !status.Egress.Strict {
		t.Errorf("status = %+v", status)
	}
	if len(status.Accounts) != 1 {
		t.Errorf("accounts = %v, want the loaded one", status.Accounts)
	}
	if status.MaxQueue != 4 {
		t.Errorf("max_pending_tasks = %d, want the configured 4", status.MaxQueue)
	}
	if status.Mem.SoftLimit != 0 {
		t.Errorf("soft limit = %d, want the guard's value", status.Mem.SoftLimit)
	}
	if strings.Contains(rec.Body.String(), "sessionid=") {
		t.Error("/admin/status leaked a session cookie")
	}
}

func TestAdminReloadRereadsTheAccountsFile(t *testing.T) {
	exec := &fakeExecutor{conversationID: "conv-1"}
	api, _, accounts := newTestAPI(t, exec, Config{})
	router := apiRouter(api)

	// Add a second session on disk, then reload through the API rather than a
	// signal, which is what an operator over the network would do.
	if err := os.WriteFile(accounts.cfg.AccountsFile, []byte(`[
	  {"label":"a","cookie":"sessionid=aaa"},
	  {"label":"b","cookie":"sessionid=bbb"}
	]`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	rec := doRequest(t, router, http.MethodPost, "/admin/reload", "", "admin-test")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var result adminReloadResult
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !result.OK || result.Accounts != 2 {
		t.Errorf("result = %+v, want two accounts", result)
	}
	if len(accounts.Cookies()) != 2 {
		t.Errorf("the store has %d sessions after the reload", len(accounts.Cookies()))
	}
}

func TestAdminReloadReportsABadFileWithoutFailing(t *testing.T) {
	exec := &fakeExecutor{conversationID: "conv-1"}
	api, _, accounts := newTestAPI(t, exec, Config{})
	router := apiRouter(api)

	if err := os.WriteFile(accounts.cfg.AccountsFile, []byte(`{ not json`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	rec := doRequest(t, router, http.MethodPost, "/admin/reload", "", "admin-test")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 with the reason in the body", rec.Code)
	}
	var result adminReloadResult
	_ = json.Unmarshal(rec.Body.Bytes(), &result)
	if result.OK {
		t.Error("a failed reload was reported as ok")
	}
	if result.Error == "" {
		t.Error("no reason was reported")
	}
	if len(accounts.Cookies()) != 1 {
		t.Errorf("a failed reload changed the running set: %d sessions", len(accounts.Cookies()))
	}
}

func TestAdminTasksListsAndBounds(t *testing.T) {
	exec := &fakeExecutor{conversationID: "conv-1"}
	api, pool, _ := newTestAPI(t, exec, Config{})
	ctx, cancel := newTestContext()
	defer cancel()
	pool.Start(ctx)
	defer pool.Close()
	router := apiRouter(api)

	if _, err := pool.Submit(SubmitRequest{Prompt: "a cat"}); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	rec := doRequest(t, router, http.MethodGet, "/admin/tasks", "", "admin-test")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var listed struct {
		Tasks []apiTask `json:"tasks"`
		Count int       `json:"count"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if listed.Count != 1 || len(listed.Tasks) != 1 {
		t.Errorf("listed %+v, want the one task", listed)
	}
	if strings.Contains(rec.Body.String(), "sessionid=") {
		t.Error("/admin/tasks leaked a session cookie")
	}

	bad := doRequest(t, router, http.MethodGet, "/admin/tasks?limit=0", "", "admin-test")
	if bad.Code != http.StatusBadRequest {
		t.Errorf("limit=0 gave %d, want 400", bad.Code)
	}
	tooBig := doRequest(t, router, http.MethodGet, "/admin/tasks?limit=100000", "", "admin-test")
	if tooBig.Code != http.StatusBadRequest {
		t.Errorf("an oversized limit gave %d, want 400", tooBig.Code)
	}
}

func TestPoolListIsNewestFirst(t *testing.T) {
	exec := &fakeExecutor{conversationID: "conv-1", block: make(chan struct{})}
	pool, _, _ := newTestPool(t, exec, Config{})
	defer close(exec.block)

	var ids []string
	for i := 0; i < 3; i++ {
		task, err := pool.Submit(SubmitRequest{Prompt: "a cat"})
		if err != nil {
			t.Fatalf("Submit %d: %v", i, err)
		}
		ids = append(ids, task.ID)
	}

	listed := pool.List(10)
	if len(listed) != 3 {
		t.Fatalf("List returned %d tasks, want 3", len(listed))
	}
	if listed[0].ID != ids[2] {
		t.Errorf("List is not newest first: got %s, want %s", listed[0].ID, ids[2])
	}
	if got := pool.List(1); len(got) != 1 {
		t.Errorf("List(1) returned %d tasks", len(got))
	}
}
