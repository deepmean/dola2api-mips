package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
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
}

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

	mu       sync.RWMutex
	accounts []*Account
	lastErr  string
}

// NewAccountStore returns an empty store for cfg.
func NewAccountStore(cfg Config) *AccountStore {
	return &AccountStore{cfg: cfg}
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
		out = append(out, &Account{
			Label:      labelOr(entry.Label, i),
			Cookie:     cookie,
			Enabled:    enabled,
			DailyLimit: limit,
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
	if now.Before(a.cooldownUntil) {
		return false
	}
	used := a.usedToday
	if !a.usedOn.Equal(s.dayOf(now)) {
		used = 0
	}
	return used < s.limit(a)
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
// It prefers the least-used account so a pool of sessions is spread evenly
// rather than draining one and then the next.
func (s *AccountStore) Pick(now time.Time) (*Account, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var best *Account
	bestUsed := 0
	for _, a := range s.accounts {
		if !s.available(a, now) {
			continue
		}
		used := a.usedToday
		if !a.usedOn.Equal(s.dayOf(now)) {
			used = 0
		}
		if best == nil || used < bestUsed {
			best, bestUsed = a, used
		}
	}
	if best == nil {
		return nil, ErrNoAccount
	}
	return best, nil
}

// RecordSuccess books one generated video against the account.
func (s *AccountStore) RecordSuccess(a *Account, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rollIfNeeded(a, now)
	a.usedToday++
	a.cooldownUntil = time.Time{}
	a.lastError = ""
	a.lastOK = now
}

// RecordFailure parks the account for the configured cooldown.
func (s *AccountStore) RecordFailure(a *Account, now time.Time, cause error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// CooldownSec is a whole number of seconds, as the operator writes it.
	a.cooldownUntil = now.Add(time.Duration(s.cfg.CooldownSec) * time.Second)
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
