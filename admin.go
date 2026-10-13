package main

import (
	"net/http"
	"strconv"
	"time"
)

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

// registerAdmin adds the operator routes. They require the admin key, not an
// ordinary API key: a caller who can reload accounts can point the service at a
// different session entirely.
func (a *api) registerAdmin(mux *http.ServeMux) {
	mux.HandleFunc("GET /admin/status", a.withAdmin(a.handleAdminStatus))
	mux.HandleFunc("POST /admin/reload", a.withAdmin(a.handleAdminReload))
	mux.HandleFunc("GET /admin/tasks", a.withAdmin(a.handleAdminTasks))
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
