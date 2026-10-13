package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Egress is the only place in the program that opens an outbound connection.
//
// Keeping it in one file is deliberate: a CI check rejects http.Transport and
// net.Dial anywhere else, so an upstream request cannot quietly bypass the
// configured path. That matters because the direct path leaks the router's real
// address to the upstream API, which refuses mainland addresses outright.
//
// Two shapes are supported, matching the 9router semantics the relay speaks:
//
//	relay  the request is POSTed to the relay, which reads x-relay-target
//	       (scheme://host) and x-relay-path (path plus query) and forwards it.
//	       The original method is preserved; the relay limits the first byte to
//	       25s and a streaming response to 300s, and refuses bodies over ~4.5MB.
//	proxy  a plain HTTP proxy via Transport.Proxy.
//
// Strict mode is the default and is the whole point: if the relay or proxy
// fails, the request fails. It never silently retries direct.
type Egress struct {
	mode         string
	strict       bool
	rotate       string
	relays       []*url.URL
	relayKey     string
	relayMaxBody int64
	noProxy      []string
	firstByte    time.Duration

	client *http.Client

	mu       sync.Mutex
	next     int
	relayIdx int
	degraded map[string]time.Time
	rnd      *rand.Rand
}

// degradedFor is how long a relay is skipped after a failure.
const degradedFor = 60 * time.Second

// NewEgress builds the egress layer from configuration.
func NewEgress(cfg Config) (*Egress, error) {
	e := &Egress{
		mode:         cfg.EgressMode,
		strict:       cfg.StrictProxy,
		rotate:       cfg.EgressRotate,
		relayKey:     cfg.RelayKey,
		relayMaxBody: cfg.RelayMaxBody,
		noProxy:      cfg.NoProxy,
		firstByte:    cfg.FirstByteTimeout,
		degraded:     map[string]time.Time{},
		rnd:          rand.New(rand.NewSource(time.Now().UnixNano())),
	}

	for _, entry := range strings.Split(cfg.RelayURL, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		u, err := url.Parse(entry)
		if err != nil {
			return nil, fmt.Errorf("egress: relay %q: %w", entry, err)
		}
		e.relays = append(e.relays, u)
	}
	if e.mode == "relay" && len(e.relays) == 0 {
		return nil, errors.New("egress: relay mode needs DOLA_RELAY_URL")
	}

	transport := &http.Transport{
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		MaxIdleConns:          4,
		MaxIdleConnsPerHost:   2,
		IdleConnTimeout:       60 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
		ForceAttemptHTTP2:     true,
	}
	if e.mode == "proxy" {
		if cfg.ProxyURL == "" {
			return nil, errors.New("egress: proxy mode needs DOLA_PROXY_URL")
		}
		proxyURL, err := url.Parse(cfg.ProxyURL)
		if err != nil {
			return nil, fmt.Errorf("egress: proxy: %w", err)
		}
		transport.Proxy = http.ProxyURL(proxyURL)
	}

	e.client = &http.Client{
		Transport: transport,
		// No client timeout: the first-byte deadline is enforced separately so a
		// long streaming response is not cut off mid-body.
	}
	return e, nil
}

// Do sends one upstream request through the configured egress.
//
// target is the absolute upstream URL. The caller's headers are forwarded, with
// hop-by-hop headers stripped; the relay headers are added on top.
func (e *Egress) Do(ctx context.Context, method, target string, hdr http.Header, body []byte) (*http.Response, error) {
	u, err := url.Parse(target)
	if err != nil {
		return nil, fmt.Errorf("egress: bad target %q: %w", target, err)
	}

	direct := e.mode == "direct" || e.mode == "none" || e.hostBypasses(u.Hostname())
	if !direct {
		switch e.mode {
		case "relay":
			return e.doRelay(ctx, method, u, hdr, body)
		case "proxy":
			return e.doWithFirstByte(ctx, method, target, hdr, body)
		}
	}
	return e.doWithFirstByte(ctx, method, target, hdr, body)
}

// doRelay posts the request to a relay, which forwards it to the real target.
func (e *Egress) doRelay(ctx context.Context, method string, target *url.URL, hdr http.Header, body []byte) (*http.Response, error) {
	if e.relayMaxBody > 0 && int64(len(body)) > e.relayMaxBody {
		return nil, fmt.Errorf("egress: body of %d bytes exceeds the relay limit of %d", len(body), e.relayMaxBody)
	}

	relay, err := e.pickRelay()
	if err != nil {
		return nil, err
	}

	// The relay is always addressed with the caller's method: it forwards it.
	relayURL := *relay
	forward := cloneHeader(hdr)
	forward.Set("x-relay-target", target.Scheme+"://"+target.Host)
	forward.Set("x-relay-path", target.RequestURI())
	if e.relayKey != "" {
		forward.Set("x-relay-key", e.relayKey)
	}

	resp, err := e.doWithFirstByte(ctx, method, relayURL.String(), forward, body)
	if err != nil {
		e.markDegraded(relay.Host, err)
		if e.strict {
			return nil, fmt.Errorf("egress: relay %s failed (strict, not retrying direct): %w", relay.Host, err)
		}
		// Non-strict mode is opt-in and documented as leaking the real address.
		return e.doWithFirstByte(ctx, method, target.String(), hdr, body)
	}
	return resp, nil
}

// doWithFirstByte performs the request and enforces the first-byte deadline.
//
// The deadline must not outlive the header phase: cancelling after the headers
// arrive would abort the body, which for a streamed SSE response is the part
// that matters.
func (e *Egress) doWithFirstByte(ctx context.Context, method, target string, hdr http.Header, body []byte) (*http.Response, error) {
	var reader io.Reader
	if len(body) > 0 {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return nil, err
	}
	req.Header = cloneHeader(hdr)
	if len(body) > 0 && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}

	if e.firstByte <= 0 {
		return e.client.Do(req)
	}

	reqCtx, cancel := context.WithCancel(ctx)
	var mu sync.Mutex
	settled := false
	timer := time.AfterFunc(e.firstByte, func() {
		mu.Lock()
		defer mu.Unlock()
		if !settled {
			cancel()
		}
	})
	resp, err := e.client.Do(req.WithContext(reqCtx))
	mu.Lock()
	settled = true
	mu.Unlock()
	timer.Stop()
	if err != nil {
		cancel()
		return nil, err
	}
	return resp, nil
}

// pickRelay chooses a relay, skipping ones that failed recently when another is
// available.
func (e *Egress) pickRelay() (*url.URL, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if len(e.relays) == 0 {
		return nil, errors.New("egress: no relay configured")
	}
	now := time.Now()

	healthy := make([]*url.URL, 0, len(e.relays))
	for _, r := range e.relays {
		if until, bad := e.degraded[r.Host]; bad && now.Before(until) {
			continue
		}
		healthy = append(healthy, r)
	}
	if len(healthy) == 0 {
		// Everything is degraded: try anyway rather than fail outright.
		healthy = e.relays
	}

	switch e.rotate {
	case "random":
		return healthy[e.rnd.Intn(len(healthy))], nil
	case "round-robin":
		e.next++
		return healthy[e.next%len(healthy)], nil
	default:
		return healthy[0], nil
	}
}

func (e *Egress) markDegraded(host string, err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.degraded[host] = time.Now().Add(degradedFor)
}

// hostBypasses reports whether a host is on the no-proxy list, which always goes
// direct. Media CDNs and private addresses belong there: the relay cannot fetch
// them usefully, and local addresses must never leave the machine.
func (e *Egress) hostBypasses(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, pattern := range e.noProxy {
		if matchHostPattern(strings.ToLower(strings.TrimSpace(pattern)), host) {
			return true
		}
	}
	return false
}

// matchHostPattern matches a host against a pattern that may contain '*'.
//
// The defaults are "*.volces.com", "*.byteimg.com", "127.0.0.1" and "192.168.*":
// a leading '*' means a suffix match and a trailing one a prefix match.
func matchHostPattern(pattern, host string) bool {
	if pattern == "" {
		return false
	}
	parts := strings.Split(pattern, "*")
	if len(parts) == 1 {
		return pattern == host
	}
	if first := parts[0]; first != "" {
		if !strings.HasPrefix(host, first) {
			return false
		}
		host = host[len(first):]
	}
	if last := parts[len(parts)-1]; last != "" {
		if !strings.HasSuffix(host, last) {
			return false
		}
		host = host[:len(host)-len(last)]
	}
	// Any middle segments must appear in order in what is left.
	for _, mid := range parts[1 : len(parts)-1] {
		if mid == "" {
			continue
		}
		i := strings.Index(host, mid)
		if i < 0 {
			return false
		}
		host = host[i+len(mid):]
	}
	return true
}

// State reports the health block. It never exposes a URL or a key.
func (e *Egress) State() egressHealth {
	e.mu.Lock()
	defer e.mu.Unlock()

	degraded := false
	if e.mode == "relay" && len(e.relays) > 0 {
		now := time.Now()
		down := 0
		for _, r := range e.relays {
			if until, bad := e.degraded[r.Host]; bad && now.Before(until) {
				down++
			}
		}
		degraded = down == len(e.relays)
	}
	return egressHealth{
		Mode:     e.mode,
		Active:   e.mode != "none",
		Degraded: degraded,
		Strict:   e.strict,
	}
}

// Close releases idle connections.
func (e *Egress) Close() {
	if e.client != nil {
		if tr, ok := e.client.Transport.(*http.Transport); ok {
			tr.CloseIdleConnections()
		}
	}
}

// cloneHeader copies headers without sharing the backing map.
func cloneHeader(hdr http.Header) http.Header {
	out := make(http.Header, len(hdr)+3)
	for k, vs := range hdr {
		for _, v := range vs {
			out.Add(k, v)
		}
	}
	return out
}
