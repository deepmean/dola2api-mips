package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testAccountsConfig(t *testing.T) Config {
	t.Helper()
	return Config{
		AccountsFile: filepath.Join(t.TempDir(), "accounts.json"),
		DailyLimit:   2,
		CooldownSec:  60, // seconds, as the operator writes it
		ResetTZ:      time.FixedZone("CST-8", 8*3600),
	}
}

func writeAccounts(t *testing.T, cfg Config, body string) {
	t.Helper()
	if err := os.WriteFile(cfg.AccountsFile, []byte(body), 0o600); err != nil {
		t.Fatalf("write accounts: %v", err)
	}
}

func TestAccountStoreLoadsArrayForm(t *testing.T) {
	cfg := testAccountsConfig(t)
	writeAccounts(t, cfg, `[
	  {"label":"a","cookie":"sessionid=aaa; sid_tt=1"},
	  {"label":"b","cookie":"sessionid=bbb"}
	]`)

	store := NewAccountStore(cfg)
	if err := store.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := len(store.Cookies()); got != 2 {
		t.Fatalf("cookies = %d, want 2", got)
	}
	if !store.Available(time.Now()) {
		t.Error("store reports nothing available right after loading two accounts")
	}
}

func TestAccountStoreLoadsObjectForm(t *testing.T) {
	cfg := testAccountsConfig(t)
	writeAccounts(t, cfg, `{"accounts":[{"cookie":"sessionid=aaa"}]}`)

	store := NewAccountStore(cfg)
	if err := store.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := len(store.Cookies()); got != 1 {
		t.Fatalf("cookies = %d, want 1", got)
	}
}

func TestAccountStoreRejectsUnusableFiles(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"empty file", ""},
		{"no sessionid", `[{"cookie":"ttwid=only"}]`},
		{"not json", `nonsense`},
		{"empty list", `[]`},
	}
	for _, c := range cases {
		cfg := testAccountsConfig(t)
		writeAccounts(t, cfg, c.body)
		store := NewAccountStore(cfg)
		if err := store.Load(); err == nil {
			t.Errorf("%s: Load accepted an unusable file", c.name)
		}
		if store.LastError() == "" {
			t.Errorf("%s: LastError is empty after a failed load", c.name)
		}
	}

	cfg := testAccountsConfig(t)
	store := NewAccountStore(cfg) // file never written
	if err := store.Load(); err == nil {
		t.Error("Load accepted a missing file")
	}
}

func TestAccountStoreEnabledFalseIsRespected(t *testing.T) {
	cfg := testAccountsConfig(t)
	writeAccounts(t, cfg, `[
	  {"label":"off","cookie":"sessionid=off","enabled":false},
	  {"label":"on","cookie":"sessionid=on"}
	]`)
	store := NewAccountStore(cfg)
	if err := store.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	now := time.Now()
	picked, err := store.Pick(now)
	if err != nil {
		t.Fatalf("Pick: %v", err)
	}
	if picked.Label != "on" {
		t.Errorf("picked %q, want the enabled account", picked.Label)
	}

	// Health must say which one is unusable, without revealing either cookie.
	health := store.Health(now)
	raw, err := json.Marshal(health)
	if err != nil {
		t.Fatalf("marshal health: %v", err)
	}
	if strings.Contains(string(raw), "sessionid=") {
		t.Error("the health snapshot leaked a cookie")
	}
	var found bool
	for _, entry := range health {
		m := entry.(map[string]any)
		if m["label"] == "off" {
			found = true
			if m["enabled"] != false || m["available"] != false {
				t.Errorf("disabled account reported as usable: %v", m)
			}
		}
	}
	if !found {
		t.Error("the disabled account is missing from health")
	}
}

func TestAccountStorePickSpreadsLoad(t *testing.T) {
	cfg := testAccountsConfig(t)
	writeAccounts(t, cfg, `[
	  {"label":"a","cookie":"sessionid=aaa"},
	  {"label":"b","cookie":"sessionid=bbb"}
	]`)
	store := NewAccountStore(cfg)
	if err := store.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	now := time.Now()

	first, err := store.Pick(now)
	if err != nil {
		t.Fatalf("Pick: %v", err)
	}
	store.RecordSuccess(first, now)

	second, err := store.Pick(now)
	if err != nil {
		t.Fatalf("Pick after one use: %v", err)
	}
	if second.Label == first.Label {
		t.Errorf("Pick chose %q twice while %q was unused", second.Label, "the other")
	}
}

func TestAccountStoreDailyLimitAndRollover(t *testing.T) {
	cfg := testAccountsConfig(t)
	writeAccounts(t, cfg, `[{"label":"a","cookie":"sessionid=aaa"}]`)
	store := NewAccountStore(cfg)
	if err := store.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	now := time.Now()
	acct, err := store.Pick(now)
	if err != nil {
		t.Fatalf("Pick: %v", err)
	}

	store.RecordSuccess(acct, now)
	store.RecordSuccess(acct, now) // DailyLimit is 2
	if store.Available(now) {
		t.Error("the account is still available after reaching its daily limit")
	}
	if _, err := store.Pick(now); err != ErrNoAccount {
		t.Errorf("Pick error = %v, want ErrNoAccount", err)
	}

	// The next day in the reset zone clears the counter.
	tomorrow := now.Add(24 * time.Hour)
	if !store.Available(tomorrow) {
		t.Error("the account is still exhausted on the following day")
	}
	if _, err := store.Pick(tomorrow); err != nil {
		t.Errorf("Pick on the next day: %v", err)
	}
}

func TestAccountStorePerAccountLimitOverridesConfig(t *testing.T) {
	cfg := testAccountsConfig(t)
	writeAccounts(t, cfg, `[{"label":"a","cookie":"sessionid=aaa","daily_limit":1}]`)
	store := NewAccountStore(cfg)
	if err := store.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	now := time.Now()
	acct, _ := store.Pick(now)
	store.RecordSuccess(acct, now)
	if store.Available(now) {
		t.Error("daily_limit 1 did not stop the account after one use")
	}

	health := store.Health(now)[0].(map[string]any)
	if health["daily_limit"] != 1 {
		t.Errorf("health daily_limit = %v, want 1", health["daily_limit"])
	}
}

func TestAccountStoreCooldown(t *testing.T) {
	cfg := testAccountsConfig(t)
	writeAccounts(t, cfg, `[{"label":"a","cookie":"sessionid=aaa"}]`)
	store := NewAccountStore(cfg)
	if err := store.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	now := time.Now()
	acct, _ := store.Pick(now)

	store.RecordFailure(acct, now, errTest)
	if store.Available(now) {
		t.Error("a failed account is still available during its cooldown")
	}
	health := store.Health(now)[0].(map[string]any)
	if health["cooldown_remaining"].(int) <= 0 {
		t.Errorf("cooldown_remaining = %v, want a positive value", health["cooldown_remaining"])
	}
	if health["last_error"] != errTest.Error() {
		t.Errorf("last_error = %v, want the cause", health["last_error"])
	}
	if !store.Available(now.Add(2 * time.Minute)) {
		t.Error("the cooldown never expires")
	}
}

var errTest = testError("upstream said no")

type testError string

func (e testError) Error() string { return string(e) }

func TestAccountStoreReloadKeepsUsageForUnchangedCookie(t *testing.T) {
	cfg := testAccountsConfig(t)
	writeAccounts(t, cfg, `[{"label":"a","cookie":"sessionid=aaa"}]`)
	store := NewAccountStore(cfg)
	if err := store.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	now := time.Now()
	acct, _ := store.Pick(now)
	store.RecordSuccess(acct, now)
	store.RecordSuccess(acct, now)

	// Rewriting the file with the same cookie must not hand back a fresh quota.
	writeAccounts(t, cfg, `[{"label":"a renamed","cookie":"sessionid=aaa"}]`)
	if err := store.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if store.Available(now) {
		t.Error("a reload reset the daily usage of an unchanged account")
	}
	health := store.Health(now)[0].(map[string]any)
	if health["label"] != "a renamed" {
		t.Errorf("label = %v, want the reloaded one", health["label"])
	}
	if health["used_today"] != 2 {
		t.Errorf("used_today = %v, want it carried over", health["used_today"])
	}
}

func TestAccountStoreReloadAddsAndRemovesAccounts(t *testing.T) {
	cfg := testAccountsConfig(t)
	writeAccounts(t, cfg, `[{"label":"a","cookie":"sessionid=aaa"}]`)
	store := NewAccountStore(cfg)
	if err := store.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	writeAccounts(t, cfg, `[
	  {"label":"b","cookie":"sessionid=bbb"},
	  {"label":"c","cookie":"sessionid=ccc"}
	]`)
	if err := store.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	cookies := store.Cookies()
	if len(cookies) != 2 || strings.Contains(strings.Join(cookies, " "), "aaa") {
		t.Errorf("reload did not replace the set: %v", cookies)
	}
}

// A typo while editing must not take a running service down.
func TestAccountStoreReloadKeepsPreviousSetOnBadFile(t *testing.T) {
	cfg := testAccountsConfig(t)
	writeAccounts(t, cfg, `[{"label":"a","cookie":"sessionid=aaa"}]`)
	store := NewAccountStore(cfg)
	if err := store.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	writeAccounts(t, cfg, `{ this is not json`)
	if err := store.Reload(); err == nil {
		t.Fatal("Reload accepted a malformed file")
	}
	if got := len(store.Cookies()); got != 1 {
		t.Errorf("accounts after a failed reload = %d, want the previous 1", got)
	}
	if store.LastError() == "" {
		t.Error("LastError is empty after a failed reload")
	}
	if !store.Available(time.Now()) {
		t.Error("the previous account stopped being available after a failed reload")
	}
}

func TestAccountStoreSkipsDuplicatesAndBadEntries(t *testing.T) {
	cfg := testAccountsConfig(t)
	writeAccounts(t, cfg, `[
	  {"label":"a","cookie":"sessionid=aaa"},
	  {"label":"dup","cookie":"sessionid=aaa"},
	  {"label":"nosession","cookie":"ttwid=x"},
	  {"label":"neg","cookie":"sessionid=neg","daily_limit":-1},
	  {"label":"good","cookie":"sessionid=good"}
	]`)
	store := NewAccountStore(cfg)
	if err := store.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := len(store.Cookies()); got != 2 {
		t.Errorf("cookies = %d, want 2 usable entries", got)
	}
	if !strings.Contains(store.LastError(), "skipped") {
		t.Errorf("LastError = %q, want a note about the skipped entries", store.LastError())
	}
}

func TestAccountStoreCookiesAreSortedAndStable(t *testing.T) {
	cfg := testAccountsConfig(t)
	writeAccounts(t, cfg, `[
	  {"cookie":"sessionid=zzz"},
	  {"cookie":"sessionid=aaa"}
	]`)
	store := NewAccountStore(cfg)
	if err := store.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	first := store.Cookies()
	second := store.Cookies()
	if strings.Join(first, "|") != strings.Join(second, "|") {
		t.Errorf("Cookies() is not stable: %v then %v", first, second)
	}
}
