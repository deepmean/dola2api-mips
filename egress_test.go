package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// recordingUpstream is a stand-in for the upstream API.
type recordingUpstream struct {
	srv *httptest.Server

	mu      sync.Mutex
	method  string
	path    string
	headers http.Header
	body    string
	hits    int
}

func newUpstream(t *testing.T, status int) *recordingUpstream {
	t.Helper()
	u := &recordingUpstream{}
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		u.mu.Lock()
		u.method, u.path, u.headers, u.body = r.Method, r.URL.RequestURI(), r.Header.Clone(), string(raw)
		u.hits++
		u.mu.Unlock()
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(u.srv.Close)
	return u
}

func (u *recordingUpstream) snapshot() (method, path, body string, headers http.Header, hits int) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.method, u.path, u.body, u.headers, u.hits
}

// relayServer forwards the request the way the real relay does: it reads
// x-relay-target and x-relay-path and re-issues the request with the same method.
func relayServer(t *testing.T, hits *int32) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		if hits != nil {
			*hits++
		}
		mu.Unlock()

		target := r.Header.Get("x-relay-target")
		path := r.Header.Get("x-relay-path")
		if target == "" || path == "" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte("missing relay headers"))
			return
		}
		raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		fwd, err := http.NewRequest(r.Method, target+path, strings.NewReader(string(raw)))
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		// Forward the headers the real relay forwards. X-Relay-* must NOT be
		// among them, which is what the leak assertion checks.
		for _, name := range []string{"Content-Type", "Cookie", "User-Agent", "Referer"} {
			if v := r.Header.Get(name); v != "" {
				fwd.Header.Set(name, v)
			}
		}
		resp, err := http.DefaultClient.Do(fwd)
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(err.Error()))
			return
		}
		defer resp.Body.Close()
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func testConfig() Config {
	return Config{
		EgressMode:       "relay",
		EgressRotate:     "none",
		StrictProxy:      true,
		RelayMaxBody:     4 << 20,
		FirstByteTimeout: 5 * time.Second,
	}
}

func TestEgressRelayForwardsMethodHeadersAndPath(t *testing.T) {
	upstream := newUpstream(t, http.StatusOK)
	var relayHits int32
	relay := relayServer(t, &relayHits)

	cfg := testConfig()
	cfg.RelayURL = relay.URL
	cfg.NoProxy = nil // 127.0.0.1 is a default bypass; the relay tests need it off

	e, err := NewEgress(cfg)
	if err != nil {
		t.Fatalf("NewEgress: %v", err)
	}
	defer e.Close()

	hdr := http.Header{}
	hdr.Set("Content-Type", "application/json")
	hdr.Set("Cookie", "sessionid=abc")
	resp, err := e.Do(context.Background(), http.MethodPost,
		upstream.srv.URL+"/chat/completion?aid=495671&x=1", hdr, []byte(`{"hello":1}`))
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()

	if relayHits != 1 {
		t.Errorf("relay hits = %d, want 1", relayHits)
	}
	method, path, body, headers, hits := upstream.snapshot()
	if hits != 1 {
		t.Fatalf("upstream hits = %d, want 1", hits)
	}
	if method != http.MethodPost {
		t.Errorf("method = %s, want POST (the relay must preserve it)", method)
	}
	if path != "/chat/completion?aid=495671&x=1" {
		t.Errorf("path = %q, want the target path and query", path)
	}
	if body != `{"hello":1}` {
		t.Errorf("body = %q, want it forwarded", body)
	}
	if got := headers.Get("Cookie"); got != "sessionid=abc" {
		t.Errorf("Cookie = %q, want it forwarded", got)
	}
	if got := headers.Get("X-Relay-Target"); got != "" {
		t.Errorf("X-Relay-Target leaked upstream as %q", got)
	}
}

// Strict mode is the safety property: a failing relay must not cause a direct
// retry, because that would leak the router's real address to the upstream.
func TestEgressStrictDoesNotFallBackToDirect(t *testing.T) {
	upstream := newUpstream(t, http.StatusOK)
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer dead.Close()

	cfg := testConfig()
	cfg.RelayURL = dead.URL
	cfg.StrictProxy = true
	cfg.NoProxy = nil

	e, err := NewEgress(cfg)
	if err != nil {
		t.Fatalf("NewEgress: %v", err)
	}
	defer e.Close()

	// The relay answers 500, which is a response rather than a transport error,
	// so this asserts the other half: a 500 is passed back, never retried direct.
	resp, err := e.Do(context.Background(), http.MethodGet, upstream.srv.URL+"/robots.txt", nil, nil)
	if err == nil {
		resp.Body.Close()
		if resp.StatusCode != http.StatusInternalServerError {
			t.Errorf("status = %d, want the relay's 500 to be passed through", resp.StatusCode)
		}
	}
	if _, _, _, _, hits := upstream.snapshot(); hits != 0 {
		t.Errorf("upstream was reached directly %d time(s) while the relay was failing", hits)
	}
}

// A relay that cannot be dialled at all is the case strict mode exists for.
func TestEgressStrictRefusesWhenRelayIsUnreachable(t *testing.T) {
	upstream := newUpstream(t, http.StatusOK)

	cfg := testConfig()
	cfg.RelayURL = "http://127.0.0.1:1/" // nothing listens here
	cfg.StrictProxy = true
	cfg.NoProxy = nil
	cfg.FirstByteTimeout = 2 * time.Second

	e, err := NewEgress(cfg)
	if err != nil {
		t.Fatalf("NewEgress: %v", err)
	}
	defer e.Close()

	_, err = e.Do(context.Background(), http.MethodGet, upstream.srv.URL+"/robots.txt", nil, nil)
	if err == nil {
		t.Fatal("Do succeeded with an unreachable relay in strict mode")
	}
	if !strings.Contains(err.Error(), "strict") {
		t.Errorf("error %q does not say why it refused to fall back", err)
	}
	if _, _, _, _, hits := upstream.snapshot(); hits != 0 {
		t.Errorf("upstream was reached %d time(s) despite strict mode", hits)
	}
}

// Non-strict mode is opt-in and does fall back; this pins that it is a choice.
func TestEgressNonStrictFallsBackToDirect(t *testing.T) {
	upstream := newUpstream(t, http.StatusOK)

	cfg := testConfig()
	cfg.RelayURL = "http://127.0.0.1:1/"
	cfg.StrictProxy = false
	cfg.NoProxy = nil
	cfg.FirstByteTimeout = 2 * time.Second

	e, err := NewEgress(cfg)
	if err != nil {
		t.Fatalf("NewEgress: %v", err)
	}
	defer e.Close()

	resp, err := e.Do(context.Background(), http.MethodGet, upstream.srv.URL+"/robots.txt", nil, nil)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if _, _, _, _, hits := upstream.snapshot(); hits != 1 {
		t.Errorf("upstream hits = %d, want the direct fallback", hits)
	}
}

// A no-proxy host must bypass the relay entirely, even in strict relay mode.
func TestEgressNoProxyHostBypassesRelay(t *testing.T) {
	upstream := newUpstream(t, http.StatusOK)
	var relayHits int32
	relay := relayServer(t, &relayHits)

	cfg := testConfig()
	cfg.RelayURL = relay.URL
	cfg.NoProxy = []string{"127.0.0.1"}
	cfg.StrictProxy = true

	e, err := NewEgress(cfg)
	if err != nil {
		t.Fatalf("NewEgress: %v", err)
	}
	defer e.Close()

	resp, err := e.Do(context.Background(), http.MethodGet, upstream.srv.URL+"/media.mp4", nil, nil)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if relayHits != 0 {
		t.Errorf("relay was used %d time(s) for a no-proxy host", relayHits)
	}
	if _, _, _, _, hits := upstream.snapshot(); hits != 1 {
		t.Errorf("upstream hits = %d, want 1 direct", hits)
	}
}

// The relay refuses bodies over ~4.5MB, so an oversized body must fail before a
// byte leaves the router rather than after a wasted upload.
func TestEgressRelayRejectsOversizedBody(t *testing.T) {
	upstream := newUpstream(t, http.StatusOK)
	var relayHits int32
	relay := relayServer(t, &relayHits)

	cfg := testConfig()
	cfg.RelayURL = relay.URL
	cfg.RelayMaxBody = 1024
	cfg.NoProxy = nil

	e, err := NewEgress(cfg)
	if err != nil {
		t.Fatalf("NewEgress: %v", err)
	}
	defer e.Close()

	_, err = e.Do(context.Background(), http.MethodPost, upstream.srv.URL+"/upload",
		nil, make([]byte, 2048))
	if err == nil {
		t.Fatal("an oversized body was accepted")
	}
	if !strings.Contains(err.Error(), "relay limit") {
		t.Errorf("error %q does not mention the relay limit", err)
	}
	if relayHits != 0 {
		t.Errorf("relay was contacted %d time(s) for a body it would refuse", relayHits)
	}
}

func TestEgressRotateRoundRobin(t *testing.T) {
	upstream := newUpstream(t, http.StatusOK)
	var first, second int32
	relayA := relayServer(t, &first)
	relayB := relayServer(t, &second)

	cfg := testConfig()
	cfg.RelayURL = relayA.URL + "," + relayB.URL
	cfg.EgressRotate = "round-robin"
	cfg.NoProxy = nil

	e, err := NewEgress(cfg)
	if err != nil {
		t.Fatalf("NewEgress: %v", err)
	}
	defer e.Close()

	for i := 0; i < 4; i++ {
		resp, err := e.Do(context.Background(), http.MethodGet, upstream.srv.URL+"/robots.txt", nil, nil)
		if err != nil {
			t.Fatalf("Do %d: %v", i, err)
		}
		resp.Body.Close()
	}
	if first == 0 || second == 0 {
		t.Errorf("round-robin used relay A %d time(s) and B %d time(s); both should be used", first, second)
	}
}

func TestEgressStateReportsModeAndStrict(t *testing.T) {
	cfg := testConfig()
	cfg.RelayURL = "http://relay.invalid/"
	cfg.StrictProxy = true
	cfg.NoProxy = nil

	e, err := NewEgress(cfg)
	if err != nil {
		t.Fatalf("NewEgress: %v", err)
	}
	defer e.Close()

	st := e.State()
	if st.Mode != "relay" || !st.Active || !st.Strict {
		t.Errorf("State() = %+v, want relay/active/strict", st)
	}
	if st.Degraded {
		t.Error("a fresh egress reports itself degraded")
	}
}

func TestEgressModeNoneIsInactive(t *testing.T) {
	e, err := NewEgress(Config{EgressMode: "none", FirstByteTimeout: time.Second})
	if err != nil {
		t.Fatalf("NewEgress: %v", err)
	}
	defer e.Close()
	if st := e.State(); st.Active {
		t.Errorf("State().Active = true for mode none")
	}
}

func TestEgressRelayModeNeedsARelay(t *testing.T) {
	if _, err := NewEgress(Config{EgressMode: "relay"}); err == nil {
		t.Fatal("relay mode without DOLA_RELAY_URL was accepted")
	}
}

func TestMatchHostPattern(t *testing.T) {
	cases := []struct {
		pattern string
		host    string
		want    bool
	}{
		{"127.0.0.1", "127.0.0.1", true},
		{"127.0.0.1", "127.0.0.2", false},
		{"*.volces.com", "foo.volces.com", true},
		{"*.volces.com", "a.b.volces.com", true},
		{"*.volces.com", "volces.com", false},
		{"*.volces.com", "notvolces.com", false},
		{"192.168.*", "192.168.1.10", true},
		{"192.168.*", "10.0.0.1", false},
		{"*.byteimg.com", "p3.byteimg.com", true},
		{"", "example.com", false},
		{"*", "anything.example", true},
	}
	for _, c := range cases {
		if got := matchHostPattern(c.pattern, c.host); got != c.want {
			t.Errorf("matchHostPattern(%q, %q) = %t, want %t", c.pattern, c.host, got, c.want)
		}
	}
}

// A no-proxy pattern with mixed case must still match, since hostnames are
// case-insensitive.
func TestHostBypassesIsCaseInsensitive(t *testing.T) {
	e := &Egress{noProxy: []string{"*.Volces.COM", "192.168.*"}}
	if !e.hostBypasses("Foo.VOLCES.com") {
		t.Error("case-insensitive no-proxy match failed")
	}
	if !e.hostBypasses("192.168.7.7") {
		t.Error("prefix pattern failed")
	}
	if e.hostBypasses("example.com") {
		t.Error("unrelated host matched a no-proxy pattern")
	}
}
