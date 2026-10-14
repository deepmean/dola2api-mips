package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"time"
)

// panelHTML is the operator page. It is embedded rather than served from disk
// so the deployment stays one binary plus two files the operator owns.
//
//go:embed panel.html
var panelHTML []byte

// addAccountRequest is the body of POST /admin/accounts.
type addAccountRequest struct {
	Label  string `json:"label"`
	Cookie string `json:"cookie"`
}

// addAccountResult reports what happened, including whether the session was
// verified before being saved.
type addAccountResult struct {
	OK       bool   `json:"ok"`
	Accounts int    `json:"accounts"`
	Verified bool   `json:"verified"`
	Error    string `json:"error,omitempty"`
}

// registerAdmin adds the operator routes. They require the admin key, not an
// ordinary API key: a caller who can reload accounts can point the service at a
// different session entirely.
//
// The page itself is the one route without a key, because a browser cannot send
// a bearer token when navigating. It carries no secrets — the key is typed into
// it and kept in the browser — and every call it makes is authenticated.
func (a *api) registerAdmin(mux *http.ServeMux) {
	mux.HandleFunc("GET /admin", a.handlePanel)
	mux.HandleFunc("GET /admin/status", a.withAdmin(a.handleAdminStatus))
	mux.HandleFunc("POST /admin/reload", a.withAdmin(a.handleAdminReload))
	mux.HandleFunc("POST /admin/reset", a.withAdmin(a.handleAdminReset))
	mux.HandleFunc("GET /admin/tasks", a.withAdmin(a.handleAdminTasks))
	mux.HandleFunc("POST /admin/accounts", a.withAdmin(a.handleAddAccount))
	mux.HandleFunc("DELETE /admin/accounts/{label}", a.withAdmin(a.handleRemoveAccount))
}

// handlePanel serves the operator page.
func (a *api) handlePanel(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(panelHTML)
}

// handleAddAccount verifies a session and saves it.
//
// Verification happens first on purpose: a cookie that does not work would
// otherwise be written, picked up by a reload, and then look like a rate limit
// when the account got parked.
func (a *api) handleAddAccount(w http.ResponseWriter, r *http.Request) {
	if a.accounts == nil {
		writeError(w, http.StatusServiceUnavailable, "no account store", "no_accounts")
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 256<<10))
	if err != nil {
		writeError(w, http.StatusBadRequest, "could not read the body", "bad_body")
		return
	}
	var req addAccountRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error(), "bad_json")
		return
	}

	verified := false
	if a.upstream != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
		err := a.upstream.VerifySession(ctx, req.Cookie)
		cancel()
		if err != nil {
			// Not saved, so the operator can paste a different one.
			writeJSON(w, http.StatusBadRequest, addAccountResult{OK: false, Error: err.Error()})
			return
		}
		verified = true
	}

	if err := a.accounts.Upsert(req.Label, req.Cookie); err != nil {
		writeJSON(w, http.StatusInternalServerError, addAccountResult{OK: false, Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, addAccountResult{
		OK: true, Verified: verified, Accounts: len(a.accounts.Cookies()),
	})
}

// handleRemoveAccount drops a session.
func (a *api) handleRemoveAccount(w http.ResponseWriter, r *http.Request) {
	if a.accounts == nil {
		writeError(w, http.StatusServiceUnavailable, "no account store", "no_accounts")
		return
	}
	label := r.PathValue("label")
	if err := a.accounts.Remove(label); err != nil {
		writeJSON(w, http.StatusNotFound, addAccountResult{OK: false, Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, addAccountResult{OK: true, Accounts: len(a.accounts.Cookies())})
}

// adminStatus is the whole picture an operator needs, in one response.
type adminStatus struct {
	OK       bool         `json:"ok"`
	Version  string       `json:"version"`
	Now      time.Time    `json:"now"`
	Executor string       `json:"executor"`
	Store    string       `json:"store_mode"`
	Egress   egressHealth `json:"egress"`
	Mem      MemSnapshot  `json:"mem"`
	Accounts []any        `json:"accounts"`
	Pending  int          `json:"pending_tasks"`
	MaxQueue int          `json:"max_pending_tasks"`
	Reload   reloadHealth `json:"reload"`
}

// adminReloadResult reports what a reload did.
type adminReloadResult struct {
	OK       bool   `json:"ok"`
	Accounts int    `json:"accounts"`
	Error    string `json:"error,omitempty"`
}

// withAdmin wraps a handler with the admin-key check.
func (a *api) withAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if a.cfg.AdminKey == "" {
			writeError(w, http.StatusServiceUnavailable,
				"no admin key configured; set DOLA_ADMIN_KEY", "no_admin_key")
			return
		}
		if !a.auth.authorizeAdmin(r) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="dola2api-admin"`)
			writeError(w, http.StatusUnauthorized, "unauthorized", "unauthorized")
			return
		}
		next(w, r)
	}
}

func (a *api) handleAdminStatus(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	status := adminStatus{
		OK:       true,
		Version:  version,
		Now:      now,
		Executor: a.cfg.Executor,
		Store:    a.retriever.Mode(),
		Mem:      a.guard.Snapshot(),
		Accounts: []any{},
		MaxQueue: a.cfg.MaxPendingTasks,
	}
	if a.egressState != nil {
		status.Egress = a.egressState()
	}
	if a.accounts != nil {
		status.Accounts = a.accounts.Health(now)
		status.Reload = reloadHealth{LastError: a.accounts.LastError()}
	}
	if a.pool != nil {
		status.Pending = a.pool.Pending()
	}
	writeJSON(w, http.StatusOK, status)
}

// handleAdminReload re-reads the accounts file without a signal.
//
// SIGHUP does the same thing, but the operator may be reaching the service over
// the network where sending a signal means an ssh session, and a reload that
// can be triggered by an authenticated request is easier to script.
// handleAdminReset releases every parked session.
//
// The pool parks sessions one after another when the refusal is about the
// upstream rather than any session, until nothing is available and the service
// reports "no session available" — a symptom that hides its own cause. This is
// the operator saying "the cause is dealt with, try now", which is otherwise
// only expressible by waiting out an exponential backoff.
func (a *api) handleAdminReset(w http.ResponseWriter, r *http.Request) {
	if a.accounts == nil {
		writeError(w, http.StatusServiceUnavailable, "no account store", "no_accounts")
		return
	}
	cleared := a.accounts.ClearCooldowns()
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "cleared": cleared, "accounts": len(a.accounts.Cookies()),
	})
}

func (a *api) handleAdminReload(w http.ResponseWriter, r *http.Request) {
	if a.accounts == nil {
		writeError(w, http.StatusServiceUnavailable, "no account store", "no_accounts")
		return
	}
	result := adminReloadResult{OK: true}
	if err := a.accounts.Reload(); err != nil {
		// A failed reload keeps the running set, so this is a 200 with the
		// reason rather than a failure of the service.
		result.OK = false
		result.Error = err.Error()
	}
	result.Accounts = len(a.accounts.Cookies())
	writeJSON(w, http.StatusOK, result)
}

// handleAdminTasks lists recent tasks. It never includes a session cookie.
func (a *api) handleAdminTasks(w http.ResponseWriter, r *http.Request) {
	if a.pool == nil {
		writeError(w, http.StatusServiceUnavailable, "no pool", "no_pool")
		return
	}
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 500 {
			writeError(w, http.StatusBadRequest, "limit must be between 1 and 500", "bad_limit")
			return
		}
		limit = parsed
	}

	tasks := a.pool.List(limit)
	out := make([]apiTask, 0, len(tasks))
	for _, task := range tasks {
		out = append(out, a.toAPITask(task))
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": out, "count": len(out)})
}
