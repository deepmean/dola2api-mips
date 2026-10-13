package main

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

// apiRequest is the body of POST /v1/videos.
type apiRequest struct {
	Prompt   string `json:"prompt"`
	Ratio    string `json:"ratio"`
	Model    string `json:"model"`
	Duration int    `json:"duration"`
}

// apiVideo is one delivered video in a task response.
type apiVideo struct {
	// URL is the direct CDN link, always present.
	URL string `json:"url"`
	// ContentURL is this service's own retrieval endpoint, which follows
	// DOLA_STORE_MODE.
	ContentURL string `json:"content_url"`
	Cover      string `json:"cover,omitempty"`
	Model      string `json:"model,omitempty"`
}

// apiTask is the response for a submitted request.
type apiTask struct {
	ID             string     `json:"id"`
	State          TaskState  `json:"state"`
	ConversationID string     `json:"conversation_id,omitempty"`
	Videos         []apiVideo `json:"videos"`
	Error          string     `json:"error,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	FinishedAt     *time.Time `json:"finished_at,omitempty"`
}

// apiError is the shape of every failure response.
type apiError struct {
	Error string `json:"error"`
	Code  string `json:"code,omitempty"`
}

// maxAPIRequestBytes bounds a JSON body. Reference images arrive as data URLs
// and are capped separately by DOLA_REFERENCE_IMAGE_MAX_BYTES.
const maxAPIRequestBytes = 8 << 20

// authenticator checks the bearer token on a request.
//
// An empty key list fails closed: a service that answers anyone when it has not
// been given a key is a service that will be exposed by accident. /health is
// deliberately exempt, because monitoring needs it and it carries no secrets.
type authenticator struct {
	keys     []string
	adminKey string
}

func newAuthenticator(cfg Config) *authenticator {
	return &authenticator{keys: cfg.APIKeys, adminKey: cfg.AdminKey}
}

// configured reports whether any key was set at all.
func (a *authenticator) configured() bool { return len(a.keys) > 0 }

// authorize accepts any configured API key, or the admin key.
func (a *authenticator) authorize(r *http.Request) bool {
	token := bearerToken(r)
	if token == "" {
		return false
	}
	if a.adminKey != "" && constantTimeEquals(token, a.adminKey) {
		return true
	}
	for _, key := range a.keys {
		if constantTimeEquals(token, key) {
			return true
		}
	}
	return false
}

// authorizeAdmin accepts only the admin key.
func (a *authenticator) authorizeAdmin(r *http.Request) bool {
	if a.adminKey == "" {
		return false
	}
	return constantTimeEquals(bearerToken(r), a.adminKey)
}

// bearerToken reads an Authorization: Bearer value, falling back to a plain
// token so a simple curl does not need the prefix.
func bearerToken(r *http.Request) string {
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	if header == "" {
		return ""
	}
	if len(header) > 7 && strings.EqualFold(header[:7], "bearer ") {
		return strings.TrimSpace(header[7:])
	}
	return header
}

// constantTimeEquals compares without leaking length or content through timing.
func constantTimeEquals(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// api bundles the handlers' dependencies.
type api struct {
	cfg       Config
	guard     *Guard
	auth      *authenticator
	pool      *Pool
	retriever *Retriever
}

func newAPI(cfg Config, guard *Guard, pool *Pool, retriever *Retriever) *api {
	return &api{cfg: cfg, guard: guard, auth: newAuthenticator(cfg), pool: pool, retriever: retriever}
}

// register adds the API routes to mux.
func (a *api) register(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/videos", a.withAuth(a.handleCreate))
	mux.HandleFunc("GET /v1/videos/{id}", a.withAuth(a.handleGet))
	mux.HandleFunc("GET /v1/videos/{id}/content", a.withAuth(a.handleContent))
}

// withAuth wraps a handler with the key check and the heap soft limit.
func (a *api) withAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !a.auth.configured() {
			writeError(w, http.StatusServiceUnavailable,
				"no API keys configured; set DOLA_API_KEYS", "no_api_keys")
			return
		}
		if !a.auth.authorize(r) {
			// Do not say whether the key was missing or wrong.
			w.Header().Set("WWW-Authenticate", `Bearer realm="dola2api"`)
			writeError(w, http.StatusUnauthorized, "unauthorized", "unauthorized")
			return
		}
		// Fail this request rather than risk an OOM kill taking out sshd.
		if err := a.guard.Check(); err != nil {
			writeError(w, http.StatusInsufficientStorage, err.Error(), "heap_soft_limit")
			return
		}
		next(w, r)
	}
}

// handleCreate submits a request and returns the task to poll.
func (a *api) handleCreate(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxAPIRequestBytes))
	if err != nil {
		writeError(w, http.StatusBadRequest, "could not read the request body", "bad_body")
		return
	}
	var req apiRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error(), "bad_json")
		return
	}
	submit, err := a.validate(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error(), "invalid_request")
		return
	}

	task, err := a.pool.Submit(submit)
	switch {
	case errors.Is(err, ErrPoolFull):
		w.Header().Set("Retry-After", "30")
		writeError(w, http.StatusTooManyRequests, err.Error(), "pool_full")
		return
	case errors.Is(err, ErrPoolClosed):
		writeError(w, http.StatusServiceUnavailable, err.Error(), "shutting_down")
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error(), "submit_failed")
		return
	}

	// The task runs asynchronously, so 202 with a task to poll.
	writeJSON(w, http.StatusAccepted, a.toAPITask(*task))
}

// handleGet returns a task's state.
func (a *api) handleGet(w http.ResponseWriter, r *http.Request) {
	task, ok := a.pool.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "no such task", "not_found")
		return
	}
	writeJSON(w, http.StatusOK, a.toAPITask(task))
}

// handleContent delivers the video of a finished task.
func (a *api) handleContent(w http.ResponseWriter, r *http.Request) {
	task, ok := a.pool.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "no such task", "not_found")
		return
	}
	if task.State != TaskSucceeded || len(task.Videos) == 0 {
		writeError(w, http.StatusConflict,
			"the task has no video yet (state: "+string(task.State)+")", "not_ready")
		return
	}
	if err := a.retriever.Deliver(r.Context(), w, task.Videos[0]); err != nil {
		writeError(w, http.StatusBadGateway, err.Error(), "delivery_failed")
		return
	}
	// The response body is on its way out; this is the task boundary the guard
	// exists for.
	a.guard.AfterJob()
}

// validate turns a request into a SubmitRequest, filling in defaults.
func (a *api) validate(req apiRequest) (SubmitRequest, error) {
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		return SubmitRequest{}, errors.New("prompt is required")
	}
	if len(prompt) > 4000 {
		return SubmitRequest{}, errors.New("prompt is too long (4000 characters maximum)")
	}

	ratio := strings.TrimSpace(req.Ratio)
	if ratio == "" {
		ratio = "9:16"
	}
	if !validRatio(ratio) {
		return SubmitRequest{}, errors.New(`ratio must look like "9:16"`)
	}

	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = "seedance_v2.0"
	}
	if len(model) > 64 {
		return SubmitRequest{}, errors.New("model name is too long")
	}

	duration := req.Duration
	if duration == 0 {
		duration = 10
	}
	if duration < 1 || duration > 60 {
		return SubmitRequest{}, errors.New("duration must be between 1 and 60 seconds")
	}

	return SubmitRequest{Prompt: prompt, Ratio: ratio, Model: model, Duration: duration}, nil
}

// validRatio accepts the aspect ratios the upstream offers.
func validRatio(ratio string) bool {
	parts := strings.Split(ratio, ":")
	if len(parts) != 2 {
		return false
	}
	for _, part := range parts {
		if part == "" || len(part) > 3 {
			return false
		}
		for _, c := range part {
			if c < '0' || c > '9' {
				return false
			}
		}
	}
	return true
}

// toAPITask renders a task for the client. The upstream URLs are never replaced
// by anything the client could not verify, so they are passed through as-is.
func (a *api) toAPITask(task Task) apiTask {
	out := apiTask{
		ID:             task.ID,
		State:          task.State,
		ConversationID: task.ConversationID,
		Error:          task.Error,
		CreatedAt:      task.CreatedAt,
		Videos:         []apiVideo{},
	}
	if !task.FinishedAt.IsZero() {
		finished := task.FinishedAt
		out.FinishedAt = &finished
	}
	for _, video := range task.Videos {
		out.Videos = append(out.Videos, apiVideo{
			URL:        bestURL(video),
			ContentURL: "/v1/videos/" + task.ID + "/content",
			Cover:      video.Cover,
			Model:      video.Model,
		})
	}
	return out
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, message, code string) {
	writeJSON(w, status, apiError{Error: message, Code: code})
}
