// Command dola2api is a router-sized port of the dola-render-gateway video API.
//
// It keeps the protocol path (HTTP submit + chain polling) and drops the
// Playwright/Chromium path, which cannot run on the MIPS32 target. All upstream
// traffic leaves through the egress layer so the router's real address never
// reaches the upstream API.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// version is overridden at build time with -ldflags "-X main.version=...".
var version = "dev"

// egressHealth is the egress block of /health. It never exposes URLs or keys:
// only whether a path is configured, and whether it is currently degraded.
type egressHealth struct {
	Mode     string `json:"mode"`
	Active   bool   `json:"active"`
	Degraded bool   `json:"degraded"`
	Strict   bool   `json:"strict"`
}

type reloadHealth struct {
	LastError string `json:"last_error"`
}

type healthResponse struct {
	OK              bool         `json:"ok"`
	Version         string       `json:"version"`
	Accounts        []any        `json:"accounts"`
	Available       bool         `json:"available"`
	PendingTasks    int          `json:"pending_tasks"`
	MaxPendingTasks int          `json:"max_pending_tasks"`
	Executor        string       `json:"executor"`
	Egress          egressHealth `json:"egress"`
	Mem             MemSnapshot  `json:"mem"`
	Reload          reloadHealth `json:"reload"`
}

// server bundles the pieces /health reports on, so later tasks (pool, store,
// egress) can be wired in without reshaping the handler.
type server struct {
	cfg   Config
	guard *Guard

	// Populated by later tasks.
	accounts     func() []any
	available    func() bool
	pendingTasks func() int
	egressState  func() egressHealth
	reloadError  func() string
}

func newServer(cfg Config, guard *Guard) *server {
	return &server{cfg: cfg, guard: guard}
}

func (s *server) mux() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.handleHealth)
	return mux
}

func (s *server) handleHealth(w http.ResponseWriter, r *http.Request) {
	eg := egressHealth{
		Mode:   s.cfg.EgressMode,
		Active: s.cfg.EgressActive(),
		Strict: s.cfg.StrictProxy,
	}
	if s.egressState != nil {
		eg = s.egressState()
	}
	accounts := []any{}
	if s.accounts != nil {
		accounts = s.accounts()
	}
	available := false
	if s.available != nil {
		available = s.available()
	}
	pending := 0
	if s.pendingTasks != nil {
		pending = s.pendingTasks()
	}
	reloadErr := ""
	if s.reloadError != nil {
		reloadErr = s.reloadError()
	}

	resp := healthResponse{
		OK:              true,
		Version:         version,
		Accounts:        accounts,
		Available:       available,
		PendingTasks:    pending,
		MaxPendingTasks: s.cfg.MaxPendingTasks,
		Executor:        s.cfg.Executor,
		Egress:          eg,
		Mem:             s.guard.Snapshot(),
		Reload:          reloadHealth{LastError: reloadErr},
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		log.Printf("health: encode: %v", err)
	}
}

func main() {
	cfg, err := LoadConfig(os.Getenv)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	guard := NewGuardFromConfig(cfg)
	srv := newServer(cfg, guard)

	raw, err := net.Listen("tcp", cfg.Addr())
	if err != nil {
		log.Fatalf("listen %s: %v", cfg.Addr(), err)
	}
	// Bound inbound sockets: memory must not scale with the number of clients
	// holding a connection open.
	ln := LimitListener(raw, cfg.MaxConnections)

	httpSrv := &http.Server{
		Handler:           srv.mux(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    cfg.MaxHeaderBytes,
		ErrorLog:          log.New(os.Stderr, "http: ", log.LstdFlags),
	}

	// SIGHUP will reload accounts.json and proxies.json without a restart once
	// the reloader lands; ignoring it now keeps a stray signal from killing the
	// service (SIGHUP's default action is termination).
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		for sig := range sigs {
			switch sig {
			case syscall.SIGHUP:
				log.Printf("SIGHUP received; hot reload is not wired yet, ignoring")
			default:
				log.Printf("%s received; shutting down", sig)
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if err := httpSrv.Shutdown(ctx); err != nil {
					log.Printf("shutdown: %v", err)
				}
				return
			}
		}
	}()

	log.Printf("dola2api %s listening on %s (egress=%s strict=%t executor=%s max_conns=%d heap_soft_limit=%d)",
		version, cfg.Addr(), cfg.EgressMode, cfg.StrictProxy, cfg.Executor, cfg.MaxConnections, cfg.HeapSoftLimit)

	if err := httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("serve: %v", err)
	}
	log.Printf("stopped")
}
