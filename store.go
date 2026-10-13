package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Retriever delivers generated media to a client.
//
// Three modes, and the default is the cheapest one on purpose:
//
//	redirect  hand the client the CDN URL. No bytes cross the router, no disk
//	          is used, and the CDN serves range requests. This is the default.
//	pipe      stream the video through the router. Costs a goroutine and a
//	          bounded copy buffer, and nothing else.
//	mirror    download once to DOLA_MIRROR_DIR and serve it from there until the
//	          TTL expires. Costs disk, so it is opt-in.
type Retriever struct {
	cfg    Config
	egress *Egress

	mu      sync.Mutex
	mirrors map[string]mirrorEntry
}

type mirrorEntry struct {
	path      string
	size      int64
	fetchedAt time.Time
}

// NewRetriever builds a retriever. Mirroring is only prepared when it is the
// configured mode, so the default path never touches the disk.
func NewRetriever(cfg Config, egress *Egress) (*Retriever, error) {
	r := &Retriever{cfg: cfg, egress: egress, mirrors: map[string]mirrorEntry{}}
	if cfg.StoreMode == "mirror" {
		if err := os.MkdirAll(cfg.MirrorDir, 0o755); err != nil {
			return nil, fmt.Errorf("store: mirror dir %s: %w", cfg.MirrorDir, err)
		}
	}
	return r, nil
}

// Mode reports the configured delivery mode.
func (r *Retriever) Mode() string { return r.cfg.StoreMode }

// bestURL prefers the unwatermarked original, which is what the upstream
// produces and what the watermarked download_url is derived from.
func bestURL(media Media) string {
	// Media.URL is already the best link available: the unwatermarked original for
	// a video, the full-size original for an image.
	return media.URL
}

// Deliver sends one item to the client.
func (r *Retriever) Deliver(ctx context.Context, w http.ResponseWriter, media Media) error {
	target := bestURL(media)
	if target == "" {
		return errors.New("store: the result has no URL")
	}

	switch r.cfg.StoreMode {
	case "redirect":
		// Let the CDN do the work: no bytes and no disk on the router.
		w.Header().Set("Location", target)
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusFound)
		return nil
	case "pipe":
		return r.pipe(ctx, w, target)
	case "mirror":
		return r.serveMirror(ctx, w, target)
	default:
		return fmt.Errorf("store: unknown mode %q", r.cfg.StoreMode)
	}
}

// pipe streams the item through the router with a bounded copy.
func (r *Retriever) pipe(ctx context.Context, w http.ResponseWriter, target string) error {
	resp, err := r.fetch(ctx, target)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("store: upstream returned %d for the media", resp.StatusCode)
	}

	w.Header().Set("Content-Type", contentTypeOf(resp))
	w.Header().Set("Cache-Control", "no-store")
	if resp.ContentLength > 0 {
		w.Header().Set("Content-Length", fmt.Sprint(resp.ContentLength))
	}
	w.WriteHeader(http.StatusOK)

	// io.Copy uses a fixed 32KB buffer, so this is a constant, not a function of
	// the video size.
	if _, err := io.Copy(w, resp.Body); err != nil {
		return fmt.Errorf("store: stream: %w", err)
	}
	return nil
}

// serveMirror serves from disk, downloading once when needed.
func (r *Retriever) serveMirror(ctx context.Context, w http.ResponseWriter, target string) error {
	path, err := r.ensureMirror(ctx, target)
	if err != nil {
		return err
	}
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("store: open mirror: %w", err)
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("store: stat mirror: %w", err)
	}
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Content-Length", fmt.Sprint(info.Size()))
	w.Header().Set("Cache-Control", "no-store")
	if _, err := io.Copy(w, file); err != nil {
		return fmt.Errorf("store: serve mirror: %w", err)
	}
	return nil
}

// ensureMirror returns the local path for target, downloading it if absent.
func (r *Retriever) ensureMirror(ctx context.Context, target string) (string, error) {
	key := mirrorKey(target)
	path := filepath.Join(r.cfg.MirrorDir, key+".mp4")

	r.mu.Lock()
	if entry, ok := r.mirrors[key]; ok && !r.expired(entry, time.Now()) {
		r.mu.Unlock()
		return entry.path, nil
	}
	r.mu.Unlock()

	resp, err := r.fetch(ctx, target)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("store: upstream returned %d for the media", resp.StatusCode)
	}

	// Write to a temporary name and rename, so a client never reads a partial
	// file and an interrupted download leaves nothing behind.
	temp, err := os.CreateTemp(r.cfg.MirrorDir, key+".*.part")
	if err != nil {
		return "", fmt.Errorf("store: temp file: %w", err)
	}
	tempName := temp.Name()

	limit := r.cfg.MirrorMaxBytes
	var written int64
	if limit > 0 {
		written, err = io.Copy(temp, io.LimitReader(resp.Body, limit+1))
		if err == nil && written > limit {
			err = fmt.Errorf("store: media exceeds DOLA_MIRROR_MAX_BYTES (%d)", limit)
		}
	} else {
		written, err = io.Copy(temp, resp.Body)
	}
	closeErr := temp.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(tempName)
		return "", fmt.Errorf("store: download: %w", err)
	}
	if err := os.Rename(tempName, path); err != nil {
		_ = os.Remove(tempName)
		return "", fmt.Errorf("store: finalise mirror: %w", err)
	}

	r.mu.Lock()
	r.mirrors[key] = mirrorEntry{path: path, size: written, fetchedAt: time.Now()}
	r.mu.Unlock()

	r.Sweep()
	return path, nil
}

// fetch retrieves a CDN object through the egress layer.
func (r *Retriever) fetch(ctx context.Context, target string) (*http.Response, error) {
	hdr := http.Header{}
	hdr.Set("Accept", "*/*")
	// The media CDNs are on the no-proxy list by default, so this normally goes
	// direct; that is intentional and the reason the list exists.
	return r.egress.Do(ctx, http.MethodGet, target, hdr, nil)
}

// Sweep removes mirrors past their TTL. It runs opportunistically after a
// download, so there is no background goroutine and no timer to leak.
func (r *Retriever) Sweep() int {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()
	removed := 0
	for key, entry := range r.mirrors {
		if !r.expired(entry, now) {
			continue
		}
		if err := os.Remove(entry.path); err == nil || os.IsNotExist(err) {
			delete(r.mirrors, key)
			removed++
		}
	}
	return removed
}

func (r *Retriever) expired(entry mirrorEntry, now time.Time) bool {
	if r.cfg.MirrorTTL <= 0 {
		return false
	}
	return now.Sub(entry.fetchedAt) > r.cfg.MirrorTTL
}

// mirrorKey derives a filesystem-safe name from the URL.
func mirrorKey(target string) string {
	sum := sha256.Sum256([]byte(target))
	return hex.EncodeToString(sum[:16])
}

// contentTypeOf prefers the upstream's type but never trusts it blindly.
func contentTypeOf(resp *http.Response) string {
	if ct := resp.Header.Get("Content-Type"); strings.HasPrefix(ct, "video/") || strings.HasPrefix(ct, "image/") {
		return ct
	}
	// The CDNs serve videos as mp4 and images as jpeg; this is the fallback for
	// a response that does not say.
	return "video/mp4"
}

// ValidMediaURL reports whether a URL is worth handing to a client. The
// generated URLs are https, and a scheme check keeps a malformed response from
// turning into an open redirect.
func ValidMediaURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return u.Scheme == "https" && u.Host != ""
}
