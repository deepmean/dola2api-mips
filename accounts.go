package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Account is one upstream session plus the state the pool tracks for it.
//
// The cookie is never included in any health or API response: /health reports
// labels and counters only.
type Account struct {
	Label      string
	Cookie     string
	Enabled    bool
	DailyLimit int // 0 means "use the configured default"

	usedToday     int
	usedOn        time.Time // midnight of the day usedToday belongs to
	cooldownUntil time.Time
	lastError     string
	lastOK        time.Time
	// expiresAt is when the session itself dies, read from sid_guard. The zero
	// value means the cookie did not say.
	expiresAt time.Time
	// consecutiveFailures drives the backoff: a session that keeps failing is
	// parked for longer each time rather than being retried at the same rate.
	consecutiveFailures int
}

// maxCooldownMultiplier caps the exponential backoff at 8x the base cooldown,
// so a persistently broken session is not parked for days by a single counter.
const maxCooldownMultiplier = 8

// accountFile is one entry as written in accounts.json.
//
// Enabled is a pointer so an absent key means "enabled" while an explicit false
// disables the entry; a plain bool could not tell those apart.
type accountFile struct {
	Label      string `json:"label"`
	Cookie     string `json:"cookie"`
	Enabled    *bool  `json:"enabled"`
	DailyLimit int    `json:"daily_limit"`
}

// accountDocument is the file shape. A bare array is also accepted, because
// that is the most natural thing for an operator to write by hand.
type accountDocument struct {
	Accounts []accountFile `json:"accounts"`
}

// ErrNoAccount means no session can take work right now: none enabled, all
// cooling down, or all at their daily limit.
var ErrNoAccount = errors.New("accounts: none available")

// AccountStore holds the sessions and their daily state, and re-reads the file
// on demand so a session can be replaced without a restart.
type AccountStore struct {
	cfg Config

	// A read-write lock, because every read path is on a request: only Pick and
	// the recorders mutate.
	mu       sync.RWMutex
	accounts []*Account
	lastErr  string
	// cursor is the round-robin position, and rnd backs the random strategy.
	cursor uint64
	rnd    *rand.Rand
}

// NewAccountStore returns an empty store for cfg.
func NewAccountStore(cfg Config) *AccountStore {
	return &AccountStore{cfg: cfg, rnd: rand.New(rand.NewSource(time.Now().UnixNano()))}
}

// Load reads the accounts file. It is the startup path: a missing or unusable
// file is an error, because the service cannot do anything without a session.
func (s *AccountStore) Load() error {
	accounts, note, err := s.readFile()
	if err != nil {
		s.mu.Lock()
		s.lastErr = err.Error()
		s.mu.Unlock()
		return err
	}
	if len(accounts) == 0 {
		err := fmt.Errorf("accounts: %s has no usable entries", s.cfg.AccountsFile)
		s.mu.Lock()
		s.lastErr = err.Error()
		s.mu.Unlock()
		return err
	}
	s.mu.Lock()
	s.accounts = accounts
	// A note about skipped entries survives a successful load; only a clean
	// parse clears the field.
	s.lastErr = note
	s.mu.Unlock()
	return nil
}

// Reload re-reads the file for SIGHUP.
//
// Runtime state is carried over for accounts whose cookie is unchanged, so a
// reload does not hand an exhausted account a fresh daily quota. A file that
// cannot be read or parsed leaves the running set untouched and only records
// the error: a typo while editing must not take the service down.
func (s *AccountStore) Reload() error {
	accounts, note, err := s.readFile()
	if err != nil {
		s.mu.Lock()
		s.lastErr = err.Error()
		s.mu.Unlock()
		return err
	}
	if len(accounts) == 0 {
		err := fmt.Errorf("accounts: %s has no usable entries; keeping the previous set", s.cfg.AccountsFile)
		s.mu.Lock()
		s.lastErr = err.Error()
		s.mu.Unlock()
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	previous := make(map[string]*Account, len(s.accounts))
	for _, a := range s.accounts {
		previous[a.Cookie] = a
	}
	for _, a := range accounts {
		if old, ok := previous[a.Cookie]; ok {
			a.usedToday = old.usedToday
			a.usedOn = old.usedOn
			a.cooldownUntil = old.cooldownUntil
			a.lastError = old.lastError
			a.lastOK = old.lastOK
		}
	}
	s.accounts = accounts
	s.lastErr = note
	return nil
}

// readFile parses accounts.json. The returned note describes entries that were
// skipped, so a caller can surface it without treating it as a failure.
func (s *AccountStore) readFile() ([]*Account, string, error) {
	raw, err := os.ReadFile(s.cfg.AccountsFile)
	if err != nil {
		return nil, "", fmt.Errorf("accounts: read %s: %w", s.cfg.AccountsFile, err)
	}

	trimmed := strings.TrimSpace(string(raw))
	var entries []accountFile
	switch {
	case trimmed == "":
		return nil, "", fmt.Errorf("accounts: %s is empty", s.cfg.AccountsFile)
	case strings.HasPrefix(trimmed, "["):
		if err := json.Unmarshal([]byte(trimmed), &entries); err != nil {
			return nil, "", fmt.Errorf("accounts: parse %s: %w", s.cfg.AccountsFile, err)
		}
	default:
		var doc accountDocument
		if err := json.Unmarshal([]byte(trimmed), &doc); err != nil {
			return nil, "", fmt.Errorf("accounts: parse %s: %w", s.cfg.AccountsFile, err)
		}
		entries = doc.Accounts
	}

	var (
		out      []*Account
		problems []string
		seen     = map[string]bool{}
	)
	for i, entry := range entries {
		cookie := strings.TrimSpace(entry.Cookie)
		switch {
		case cookie == "":
			problems = append(problems, fmt.Sprintf("entry %d: empty cookie", i))
			continue
		case !strings.Contains(cookie, "sessionid="):
			problems = append(problems, fmt.Sprintf("entry %d (%s): cookie has no sessionid", i, labelOr(entry.Label, i)))
			continue
		case seen[cookie]:
			problems = append(problems, fmt.Sprintf("entry %d (%s): duplicate cookie", i, labelOr(entry.Label, i)))
			continue
		}
		seen[cookie] = true

		enabled := true
		if entry.Enabled != nil {
			enabled = *entry.Enabled
		}
		limit := entry.DailyLimit
		if limit < 0 {
			problems = append(problems, fmt.Sprintf("entry %d (%s): negative daily_limit", i, labelOr(entry.Label, i)))
			continue
		}
		expiry, _ := sessionExpiry(cookie)
		out = append(out, &Account{
			Label:      labelOr(entry.Label, i),
			Cookie:     cookie,
			Enabled:    enabled,
			DailyLimit: limit,
			expiresAt:  expiry,
		})
	}

	note := ""
	if len(problems) > 0 {
		note = fmt.Sprintf("accounts: skipped %d entry(ies): %s", len(problems), strings.Join(problems, "; "))
		if len(out) == 0 {
			return nil, note, fmt.Errorf("accounts: %s has no usable entries: %s",
				s.cfg.AccountsFile, strings.Join(problems, "; "))
		}
	}
	return out, note, nil
}

func labelOr(label string, index int) string {
	if label = strings.TrimSpace(label); label != "" {
		return label
	}
	return fmt.Sprintf("#%d", index+1)
}

// limit is the daily allowance for one account.
func (s *AccountStore) limit(a *Account) int {
	if a.DailyLimit > 0 {
		return a.DailyLimit
	}
	return s.cfg.DailyLimit
}

// dayOf returns midnight of t in the configured reset zone, which is how a
// "day" is defined for the daily allowance.
func (s *AccountStore) dayOf(t time.Time) time.Time {
	loc := s.cfg.ResetTZ
	if loc == nil {
		// A zero Config is reachable from tests; UTC is the sane default.
		loc = time.UTC
	}
	local := t.In(loc)
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
}

// rollIfNeeded resets a counter that belongs to an earlier day. Callers hold
// the lock.
func (s *AccountStore) rollIfNeeded(a *Account, now time.Time) {
	today := s.dayOf(now)
	if !a.usedOn.Equal(today) {
		a.usedToday = 0
		a.usedOn = today
	}
}

// available reports whether an account can take work now.
func (s *AccountStore) available(a *Account, now time.Time) bool {
	if !a.Enabled {
		return false
	}
	// An expired session is not worth a request: the upstream would refuse it
	// and the account would be parked, which looks like a rate limit rather
	// than a login that needs replacing.
	if !a.expiresAt.IsZero() && now.After(a.expiresAt) {
		return false
	}
	if now.Before(a.cooldownUntil) {
		return false
	}
	used := a.usedToday
	if !a.usedOn.Equal(s.dayOf(now)) {
		used = 0
	}
	return used < s.limit(a)
}

// Upsert adds or replaces a session and writes the file back.
//
// The write is atomic: a temporary file in the same directory is renamed over
// the target, so a failure part-way cannot leave a truncated accounts.json that
// the next reload would reject. The existing entries are preserved, because the
// file is the operator's, not this service's.
func (s *AccountStore) Upsert(label, cookie string) error {
	cookie = strings.TrimSpace(cookie)
	if cookie == "" {
		return errors.New("accounts: empty cookie")
	}
	if !strings.Contains(cookie, "sessionid=") {
		return errors.New("accounts: the cookie has no sessionid, so it cannot be a login")
	}
	label = strings.TrimSpace(label)
	if label == "" {
		label = fmt.Sprintf("session-%d", time.Now().Unix())
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := s.readEntriesLocked()
	if err != nil {
		return err
	}

	replaced := false
	for i := range entries {
		// Match on label first so an operator replacing a session keeps its
		// position; fall back to the session id so a rename does not duplicate.
		if entries[i].Label == label || sameSession(entries[i].Cookie, cookie) {
			entries[i] = accountFile{Label: label, Cookie: cookie, Enabled: boolPtr(true)}
			replaced = true
			break
		}
	}
	if !replaced {
		entries = append(entries, accountFile{Label: label, Cookie: cookie, Enabled: boolPtr(true)})
	}

	if err := s.writeEntriesLocked(entries); err != nil {
		return err
	}
	return s.reloadLocked()
}

// Remove drops a session by label and writes the file back.
func (s *AccountStore) Remove(label string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := s.readEntriesLocked()
	if err != nil {
		return err
	}
	kept := make([]accountFile, 0, len(entries))
	removed := false
	for _, entry := range entries {
		if entry.Label == label {
			removed = true
			continue
		}
		kept = append(kept, entry)
	}
	if !removed {
		return fmt.Errorf("accounts: no session labelled %q", label)
	}
	if err := s.writeEntriesLocked(kept); err != nil {
		return err
	}
	return s.reloadLocked()
}

func boolPtr(v bool) *bool { return &v }

// sameSession compares the sessionid, which identifies the login.
func sameSession(a, b string) bool {
	idA, idB := cookieField(a, "sessionid"), cookieField(b, "sessionid")
	return idA != "" && idA == idB
}

// readEntriesLocked reads the file as entries, for editing. Callers hold the lock.
func (s *AccountStore) readEntriesLocked() ([]accountFile, error) {
	raw, err := os.ReadFile(s.cfg.AccountsFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // an empty file is a valid starting point
		}
		return nil, fmt.Errorf("accounts: read %s: %w", s.cfg.AccountsFile, err)
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return nil, nil
	}
	if strings.HasPrefix(trimmed, "[") {
		var entries []accountFile
		if err := json.Unmarshal([]byte(trimmed), &entries); err != nil {
			return nil, fmt.Errorf("accounts: parse %s: %w", s.cfg.AccountsFile, err)
		}
		return entries, nil
	}
	var doc accountDocument
	if err := json.Unmarshal([]byte(trimmed), &doc); err != nil {
		return nil, fmt.Errorf("accounts: parse %s: %w", s.cfg.AccountsFile, err)
	}
	return doc.Accounts, nil
}

// writeEntriesLocked writes the file atomically. Callers hold the lock.
func (s *AccountStore) writeEntriesLocked(entries []accountFile) error {
	raw, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return fmt.Errorf("accounts: encode: %w", err)
	}
	raw = append(raw, '\n')

	dir := filepath.Dir(s.cfg.AccountsFile)
	temp, err := os.CreateTemp(dir, ".accounts-*.json")
	if err != nil {
		return fmt.Errorf("accounts: temp file: %w", err)
	}
	name := temp.Name()
	// The file holds live sessions, so it must never be group or world readable.
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		_ = os.Remove(name)
		return fmt.Errorf("accounts: chmod: %w", err)
	}
	if _, err := temp.Write(raw); err != nil {
		_ = temp.Close()
		_ = os.Remove(name)
		return fmt.Errorf("accounts: write: %w", err)
	}
	if err := temp.Close(); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("accounts: close: %w", err)
	}
	if err := os.Rename(name, s.cfg.AccountsFile); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("accounts: rename: %w", err)
	}
	return nil
}

// reloadLocked re-reads the file, carrying over state for unchanged cookies.
// Callers hold the lock, so this does the work of Reload without re-locking.
func (s *AccountStore) reloadLocked() error {
	accounts, note, err := s.readFile()
	if err != nil {
		s.lastErr = err.Error()
		return err
	}
	if len(accounts) == 0 {
		err := fmt.Errorf("accounts: %s has no usable entries", s.cfg.AccountsFile)
		s.lastErr = err.Error()
		return err
	}
	previous := make(map[string]*Account, len(s.accounts))
	for _, a := range s.accounts {
		previous[a.Cookie] = a
	}
	for _, a := range accounts {
		if old, ok := previous[a.Cookie]; ok {
			a.usedToday = old.usedToday
			a.usedOn = old.usedOn
			a.cooldownUntil = old.cooldownUntil
			a.lastError = old.lastError
			a.lastOK = old.lastOK
		}
	}
	s.accounts = accounts
	s.lastErr = note
	return nil
}

// ExpiringSoon reports the sessions that expire within the warning window, so a
// caller can say so before they stop working.
//
// Nothing here can refresh a session: this account type logs in through a
// browser, so replacing the cookie is a human step. Saying when it is due is
// the useful half.
func (s *AccountStore) ExpiringSoon(now time.Time) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	window := time.Duration(s.cfg.SessionWarnDays) * 24 * time.Hour
	var out []string
	for _, a := range s.accounts {
		if a.expiresAt.IsZero() {
			continue
		}
		remaining := a.expiresAt.Sub(now)
		if remaining > window {
			continue
		}
		if remaining < 0 {
			out = append(out, fmt.Sprintf("%s expired on %s", a.Label, a.expiresAt.Format(time.RFC3339)))
			continue
		}
		out = append(out, fmt.Sprintf("%s expires in %d day(s) (%s)",
			a.Label, int(remaining.Hours()/24), a.expiresAt.Format("2006-01-02")))
	}
	return out
}

// Available reports whether any account can take work now.
func (s *AccountStore) Available(now time.Time) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, a := range s.accounts {
		if s.available(a, now) {
			return true
		}
	}
	return false
}

// Pick returns the account that should take the next job.
//
// The strategy decides among the sessions that can work right now, so a parked
// or exhausted session is never chosen regardless of strategy. It takes the
// write lock because round-robin advances a cursor.
func (s *AccountStore) Pick(now time.Time) (*Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	usable := make([]*Account, 0, len(s.accounts))
	for _, a := range s.accounts {
		if s.available(a, now) {
			usable = append(usable, a)
		}
	}
	if len(usable) == 0 {
		return nil, ErrNoAccount
	}

	switch s.cfg.AccountStrategy {
	case "round-robin":
		s.cursor++
		return usable[int(s.cursor)%len(usable)], nil
	case "random":
		return usable[s.rnd.Intn(len(usable))], nil
	default: // least-used
		best, bestUsed := usable[0], s.usedToday(usable[0], now)
		for _, a := range usable[1:] {
			if used := s.usedToday(a, now); used < bestUsed {
				best, bestUsed = a, used
			}
		}
		return best, nil
	}
}

// sessionExpiry reads the expiry out of sid_guard, which is the cookie that
// carries it: hash|issued|ttl|expiry.
//
// The value is URL-encoded in the jar, so %7C stands in for the separator and
// the date's spaces arrive as '+'. Getting this wrong silently reports "no
// expiry", which is worse than not looking at all.
func sessionExpiry(cookie string) (time.Time, bool) {
	raw := cookieField(cookie, "sid_guard")
	if raw == "" {
		return time.Time{}, false
	}
	decoded, err := url.QueryUnescape(raw)
	if err != nil {
		decoded = raw
	}
	parts := strings.Split(decoded, "|")
	if len(parts) < 4 {
		return time.Time{}, false
	}

	// Prefer the stated expiry; fall back to issued+ttl so a date this build
	// cannot parse still yields an answer.
	if when, err := http.ParseTime(strings.TrimSpace(parts[3])); err == nil {
		return when, true
	}
	issued, err1 := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
	ttl, err2 := strconv.ParseInt(strings.TrimSpace(parts[2]), 10, 64)
	if err1 != nil || err2 != nil {
		return time.Time{}, false
	}
	return time.Unix(issued+ttl, 0).UTC(), true
}

// cookieField returns one cookie's value from a Cookie header line.
func cookieField(cookie, name string) string {
	for _, part := range strings.Split(cookie, ";") {
		part = strings.TrimSpace(part)
		if !strings.HasPrefix(part, name+"=") {
			continue
		}
		return strings.TrimPrefix(part, name+"=")
	}
	return ""
}

// usedToday reports an account's usage for the current day, without mutating it.
func (s *AccountStore) usedToday(a *Account, now time.Time) int {
	if !a.usedOn.Equal(s.dayOf(now)) {
		return 0
	}
	return a.usedToday
}

// RecordSuccess books one generated video against the account and clears any
// backoff: a session that has just worked is not a failing one.
func (s *AccountStore) RecordSuccess(a *Account, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rollIfNeeded(a, now)
	a.usedToday++
	a.cooldownUntil = time.Time{}
	a.lastError = ""
	a.lastOK = now
	a.consecutiveFailures = 0
}

// RecordFailure parks the account, for longer each time it fails in a row.
//
// The backoff matters when several sessions share a limit: without it, a pool
// whose sessions are all rate-limited is retried at the same rate forever, and
// every retry is another request against an upstream that just said no. The
// multiplier is capped so a long outage does not park a session for days.
func (s *AccountStore) RecordFailure(a *Account, now time.Time, cause error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	a.consecutiveFailures++
	shift := a.consecutiveFailures - 1
	if shift > 3 { // 1x, 2x, 4x, then 8x
		shift = 3
	}
	multiplier := time.Duration(1 << shift)
	if multiplier > maxCooldownMultiplier {
		multiplier = maxCooldownMultiplier
	}

	base := time.Duration(s.cfg.CooldownSec) * time.Second
	a.cooldownUntil = now.Add(base * multiplier)
	if cause != nil {
		a.lastError = cause.Error()
	}
}

// LastError reports the most recent load problem, for /health.
func (s *AccountStore) LastError() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastErr
}

// Health returns the per-account view for /health. The cookie is deliberately
// absent: this response is reachable without the admin key.
func (s *AccountStore) Health(now time.Time) []any {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]any, 0, len(s.accounts))
	for _, a := range s.accounts {
		used := a.usedToday
		if !a.usedOn.Equal(s.dayOf(now)) {
			used = 0
		}
		cooldown := 0
		if remaining := a.cooldownUntil.Sub(now); remaining > 0 {
			cooldown = int(remaining.Seconds())
		}
		entry := map[string]any{
			"label":              a.Label,
			"enabled":            a.Enabled,
			"used_today":         used,
			"daily_limit":        s.limit(a),
			"available":          s.available(a, now),
			"cooldown_remaining": cooldown,
		}
		if !a.expiresAt.IsZero() {
			entry["expires_at"] = a.expiresAt.Format(time.RFC3339)
			days := int(a.expiresAt.Sub(now).Hours() / 24)
			entry["expires_in_days"] = days
			entry["expired"] = now.After(a.expiresAt)
		}
		if a.lastError != "" {
			entry["last_error"] = a.lastError
		}
		out = append(out, entry)
	}
	return out
}

// Cookies returns the session cookies, for the pool to use.
//
// It is separate from Health on purpose so that the only caller that touches
// cookie material is the one that sends it upstream.
func (s *AccountStore) Cookies() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.accounts))
	for _, a := range s.accounts {
		out = append(out, a.Cookie)
	}
	sort.Strings(out)
	return out
}
