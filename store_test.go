package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestRetriever(t *testing.T, cfg Config, cdn *httptest.Server) *Retriever {
	t.Helper()
	egress, err := NewEgress(Config{EgressMode: "direct", FirstByteTimeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("NewEgress: %v", err)
	}
	t.Cleanup(egress.Close)
	if cfg.MirrorDir == "" {
		cfg.MirrorDir = t.TempDir()
	}
	r, err := NewRetriever(cfg, egress)
	if err != nil {
		t.Fatalf("NewRetriever: %v", err)
	}
	_ = cdn
	return r
}

func cdnServer(t *testing.T, body string, status int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestRetrieverRedirectSendsNoBytes(t *testing.T) {
	cdn := cdnServer(t, "VIDEO-BYTES", http.StatusOK)
	r := newTestRetriever(t, Config{StoreMode: "redirect"}, cdn)

	rec := httptest.NewRecorder()
	if err := r.Deliver(context.Background(), rec, Video{UnwatermarkedURL: cdn.URL + "/a.mp4"}); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if rec.Code != http.StatusFound {
		t.Errorf("status = %d, want 302", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != cdn.URL+"/a.mp4" {
		t.Errorf("Location = %q", got)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("redirect wrote %d bytes of body", rec.Body.Len())
	}
}

func TestRetrieverPipeStreamsTheVideo(t *testing.T) {
	cdn := cdnServer(t, "VIDEO-BYTES", http.StatusOK)
	r := newTestRetriever(t, Config{StoreMode: "pipe"}, cdn)

	rec := httptest.NewRecorder()
	if err := r.Deliver(context.Background(), rec, Video{UnwatermarkedURL: cdn.URL + "/a.mp4"}); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if rec.Body.String() != "VIDEO-BYTES" {
		t.Errorf("body = %q", rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "video/mp4" {
		t.Errorf("Content-Type = %q", ct)
	}
}

func TestRetrieverMirrorDownloadsOnceAndReuses(t *testing.T) {
	hits := 0
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "video/mp4")
		_, _ = w.Write([]byte("VIDEO-BYTES"))
	}))
	defer cdn.Close()

	dir := t.TempDir()
	r := newTestRetriever(t, Config{StoreMode: "mirror", MirrorDir: dir, MirrorTTL: time.Hour}, cdn)

	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		if err := r.Deliver(context.Background(), rec, Video{UnwatermarkedURL: cdn.URL + "/a.mp4"}); err != nil {
			t.Fatalf("Deliver %d: %v", i, err)
		}
		if rec.Body.String() != "VIDEO-BYTES" {
			t.Errorf("body %d = %q", i, rec.Body.String())
		}
	}
	if hits != 1 {
		t.Errorf("the CDN was fetched %d time(s), want 1 (second serve must come from disk)", hits)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read mirror dir: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("mirror dir has %d entries, want 1", len(entries))
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".part") {
			t.Errorf("a partial download was left behind: %s", e.Name())
		}
	}
}

func TestRetrieverMirrorSweepsExpiredFiles(t *testing.T) {
	cdn := cdnServer(t, "VIDEO-BYTES", http.StatusOK)
	dir := t.TempDir()
	r := newTestRetriever(t, Config{StoreMode: "mirror", MirrorDir: dir, MirrorTTL: time.Nanosecond}, cdn)

	rec := httptest.NewRecorder()
	if err := r.Deliver(context.Background(), rec, Video{UnwatermarkedURL: cdn.URL + "/a.mp4"}); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	time.Sleep(2 * time.Millisecond)

	if removed := r.Sweep(); removed != 1 {
		t.Errorf("Sweep removed %d file(s), want 1", removed)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("mirror dir still has %d entries after the sweep", len(entries))
	}
}

func TestRetrieverMirrorRejectsOversizedVideo(t *testing.T) {
	cdn := cdnServer(t, strings.Repeat("x", 4096), http.StatusOK)
	dir := t.TempDir()
	r := newTestRetriever(t, Config{StoreMode: "mirror", MirrorDir: dir, MirrorMaxBytes: 128, MirrorTTL: time.Hour}, cdn)

	rec := httptest.NewRecorder()
	err := r.Deliver(context.Background(), rec, Video{UnwatermarkedURL: cdn.URL + "/a.mp4"})
	if err == nil {
		t.Fatal("an oversized video was mirrored")
	}
	if !strings.Contains(err.Error(), "MIRROR_MAX_BYTES") {
		t.Errorf("error %q does not name the limit", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("a rejected download left %d file(s) behind", len(entries))
	}
}

func TestRetrieverMirrorRefusesToStartWithABadDir(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	egress, err := NewEgress(Config{EgressMode: "direct"})
	if err != nil {
		t.Fatalf("NewEgress: %v", err)
	}
	defer egress.Close()

	if _, err := NewRetriever(Config{StoreMode: "mirror", MirrorDir: blocker}, egress); err == nil {
		t.Error("mirror mode accepted a path that is not a directory")
	}
}

func TestRetrieverReportsUpstreamErrors(t *testing.T) {
	cdn := cdnServer(t, "nope", http.StatusNotFound)
	r := newTestRetriever(t, Config{StoreMode: "pipe"}, cdn)

	rec := httptest.NewRecorder()
	err := r.Deliver(context.Background(), rec, Video{UnwatermarkedURL: cdn.URL + "/a.mp4"})
	if err == nil {
		t.Fatal("a 404 from the CDN was reported as success")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("error %q does not carry the status", err)
	}
}

func TestRetrieverPrefersTheUnwatermarkedURL(t *testing.T) {
	r := newTestRetriever(t, Config{StoreMode: "redirect"}, nil)
	rec := httptest.NewRecorder()
	if err := r.Deliver(context.Background(), rec, Video{
		DownloadURL:      "https://cdn/watermarked.mp4",
		UnwatermarkedURL: "https://cdn/original.mp4",
	}); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if got := rec.Header().Get("Location"); got != "https://cdn/original.mp4" {
		t.Errorf("Location = %q, want the unwatermarked original", got)
	}
}

func TestRetrieverRejectsAVideoWithoutAURL(t *testing.T) {
	r := newTestRetriever(t, Config{StoreMode: "redirect"}, nil)
	if err := r.Deliver(context.Background(), httptest.NewRecorder(), Video{}); err == nil {
		t.Error("a video with no URL was delivered")
	}
}

func TestValidVideoURL(t *testing.T) {
	cases := []struct {
		raw  string
		want bool
	}{
		{"https://cdn.example/a.mp4", true},
		{"http://cdn.example/a.mp4", false}, // plain http is not what the API returns
		{"javascript:alert(1)", false},
		{"", false},
		{"not a url", false},
	}
	for _, c := range cases {
		if got := ValidVideoURL(c.raw); got != c.want {
			t.Errorf("ValidVideoURL(%q) = %t, want %t", c.raw, got, c.want)
		}
	}
}
