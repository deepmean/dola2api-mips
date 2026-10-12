package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func getHealth(t *testing.T, srv *server) healthResponse {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.mux().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("/health status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}

	var resp healthResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode /health: %v (body=%s)", err, rec.Body.String())
	}
	return resp
}

// An empty pool must still answer 200 with a usable shape: this is what the
// deploy script and the 4G reachability check curl.
func TestHealthEmptyPool(t *testing.T) {
	cfg, err := LoadConfig(envMap(nil))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	srv := newServer(cfg, NewGuardFromConfig(cfg))

	resp := getHealth(t, srv)

	if !resp.OK {
		t.Error("ok = false, want true")
	}
	if resp.PendingTasks != 0 {
		t.Errorf("pending_tasks = %d, want 0", resp.PendingTasks)
	}
	if resp.MaxPendingTasks != cfg.MaxPendingTasks {
		t.Errorf("max_pending_tasks = %d, want %d", resp.MaxPendingTasks, cfg.MaxPendingTasks)
	}
	if resp.Available {
		t.Error("available = true with an empty pool, want false")
	}
	if resp.Accounts == nil {
		t.Error("accounts should be an empty array, not null")
	}
	if resp.Egress.Mode != "relay" {
		t.Errorf("egress.mode = %q, want relay", resp.Egress.Mode)
	}
	if !resp.Egress.Strict {
		t.Error("egress.strict = false, want true (strict proxy is the default)")
	}
	if !resp.Egress.Active {
		t.Error("egress.active = false with a relay configured")
	}
	if resp.Mem.HeapAlloc == 0 {
		t.Error("mem.heap_alloc = 0 in a running program")
	}
	if resp.Mem.SoftLimit != uint64(cfg.HeapSoftLimit) {
		t.Errorf("mem.soft_limit = %d, want %d", resp.Mem.SoftLimit, cfg.HeapSoftLimit)
	}
	if resp.Reload.LastError != "" {
		t.Errorf("reload.last_error = %q, want empty", resp.Reload.LastError)
	}
	if resp.Executor != "protocol" {
		t.Errorf("executor = %q, want protocol", resp.Executor)
	}
}

// /health must never leak credentials or egress endpoints; it is reachable from
// the WAN in the deployed setup.
func TestHealthDoesNotLeakSecrets(t *testing.T) {
	env := map[string]string{
		"DOLA_RELAY_URL":     "https://relay.example/secret-path",
		"DOLA_RELAY_KEY":     "super-secret-relay-key",
		"DOLA_API_KEYS":      "sk-secret-api-key",
		"DOLA_ADMIN_KEY":     "secret-admin-key",
		"DOLA_PROXY_URL":     "",
		"DOLA_SIDECAR_TOKEN": "secret-sidecar-token",
	}
	cfg, err := LoadConfig(envMap(env))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	srv := newServer(cfg, NewGuardFromConfig(cfg))

	rec := httptest.NewRecorder()
	srv.mux().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	body := rec.Body.String()

	for name, secret := range env {
		if secret == "" {
			continue
		}
		if contains(body, secret) {
			t.Errorf("/health leaked %s value", name)
		}
	}
	for _, key := range []string{"relay_url", "relay_key", "api_key", "admin_key", "proxy_url", "token"} {
		if contains(body, key) {
			t.Errorf("/health response contains a %q field; only mode/active/degraded/strict are allowed", key)
		}
	}
}

// Later tasks install hooks; the handler must pass their values straight
// through rather than reading package globals.
func TestHealthUsesInjectedHooks(t *testing.T) {
	cfg, err := LoadConfig(envMap(nil))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	srv := newServer(cfg, NewGuardFromConfig(cfg))
	srv.accounts = func() []any { return []any{map[string]any{"name": "acc1", "used_today": 1}} }
	srv.available = func() bool { return true }
	srv.pendingTasks = func() int { return 3 }
	srv.egressState = func() egressHealth {
		return egressHealth{Mode: "relay", Active: true, Degraded: true, Strict: true}
	}
	srv.reloadError = func() string { return "accounts.json: unexpected end of JSON input" }

	resp := getHealth(t, srv)

	if len(resp.Accounts) != 1 {
		t.Errorf("accounts = %v, want one entry", resp.Accounts)
	}
	if !resp.Available {
		t.Error("available should come from the injected hook")
	}
	if resp.PendingTasks != 3 {
		t.Errorf("pending_tasks = %d, want 3", resp.PendingTasks)
	}
	if !resp.Egress.Degraded {
		t.Error("egress.degraded should come from the injected hook")
	}
	if resp.Reload.LastError == "" {
		t.Error("reload.last_error should come from the injected hook")
	}
}

// Only GET /health exists at this stage; anything else must 404/405 rather than
// falling through to a handler.
func TestHealthMethodAndPathRouting(t *testing.T) {
	cfg, err := LoadConfig(envMap(nil))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	srv := newServer(cfg, NewGuardFromConfig(cfg))

	rec := httptest.NewRecorder()
	srv.mux().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/health", nil))
	if rec.Code == http.StatusOK {
		t.Error("POST /health should not succeed")
	}

	rec = httptest.NewRecorder()
	srv.mux().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/nope", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("GET /nope status = %d, want 404", rec.Code)
	}
}

func contains(haystack, needle string) bool {
	if needle == "" {
		return false
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
