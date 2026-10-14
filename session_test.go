package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// A real sid_guard, URL-encoded exactly as the jar carries it.
const sampleGuard = "sid_guard=84c4d03cbf399b8ff5254436a1b471c4%7C1791885294%7C5184000%7CSat%2C+12-Dec-2026+09%3A54%3A54+GMT"

func TestSessionExpiryParsesTheEncodedGuard(t *testing.T) {
	when, ok := sessionExpiry("sessionid=x; " + sampleGuard + "; ttwid=y")
	if !ok {
		t.Fatal("no expiry was read; the value is URL-encoded, so %7C and + must be decoded")
	}
	want := time.Date(2026, 12, 12, 9, 54, 54, 0, time.UTC)
	if !when.Equal(want) {
		t.Errorf("expiry = %s, want %s", when.Format(time.RFC3339), want.Format(time.RFC3339))
	}
}

func TestSessionExpiryFallsBackToIssuedPlusTTL(t *testing.T) {
	// A guard whose date this build cannot parse must still yield an answer.
	when, ok := sessionExpiry("sid_guard=hash%7C1791885294%7C5184000%7Cnot-a-date")
	if !ok {
		t.Fatal("no expiry was derived from issued+ttl")
	}
	want := time.Unix(1791885294+5184000, 0).UTC()
	if !when.Equal(want) {
		t.Errorf("expiry = %s, want %s", when.Format(time.RFC3339), want.Format(time.RFC3339))
	}
}

func TestSessionExpiryHandlesAbsentGuard(t *testing.T) {
	if _, ok := sessionExpiry("sessionid=x; ttwid=y"); ok {
		t.Error("an expiry was invented for a cookie that carries none")
	}
}

func TestExpiredSessionIsUnavailableAndReported(t *testing.T) {
	cfg := testAccountsConfig(t)
	// Issued in the past with a short ttl, so it is already gone.
	expired := "sessionid=old; sid_guard=h%7C1000000000%7C3600%7CThu%2C+01+Jan+1970+00%3A00%3A00+GMT"
	writeAccounts(t, cfg, `[{"label":"old","cookie":"`+expired+`"}]`)

	store := NewAccountStore(cfg)
	if err := store.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	now := time.Now()
	if store.Available(now) {
		t.Error("an expired session is reported as available; the upstream would refuse it and it would look like a rate limit")
	}
	if _, err := store.Pick(now); err != ErrNoAccount {
		t.Errorf("Pick on an expired session = %v, want ErrNoAccount", err)
	}

	entry := store.Health(now)[0].(map[string]any)
	if entry["expired"] != true {
		t.Errorf("health does not say the session expired: %v", entry)
	}
	if entry["available"] != false {
		t.Errorf("health says an expired session is available: %v", entry)
	}
}

func TestExpiringSoonWarnsBeforeItHappens(t *testing.T) {
	cfg := testAccountsConfig(t)
	cfg.SessionWarnDays = 30
	writeAccounts(t, cfg, `[{"label":"soon","cookie":"sessionid=x; `+sampleGuard+`"}]`)

	store := NewAccountStore(cfg)
	if err := store.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Two months out: outside a 30-day window.
	if warnings := store.ExpiringSoon(time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)); len(warnings) != 0 {
		t.Errorf("warned too early: %v", warnings)
	}
	// A week before: inside it.
	warnings := store.ExpiringSoon(time.Date(2026, 12, 5, 0, 0, 0, 0, time.UTC))
	if len(warnings) != 1 || !strings.Contains(warnings[0], "soon") {
		t.Errorf("no warning inside the window: %v", warnings)
	}
	// After it: reported as expired rather than as expiring.
	after := store.ExpiringSoon(time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC))
	if len(after) != 1 || !strings.Contains(after[0], "expired") {
		t.Errorf("an expired session is not reported as expired: %v", after)
	}
}

func TestUpsertAddsReplacesAndPreserves(t *testing.T) {
	cfg := testAccountsConfig(t)
	writeAccounts(t, cfg, `[{"label":"keep","cookie":"sessionid=keep"}]`)
	store := NewAccountStore(cfg)
	if err := store.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	if err := store.Upsert("added", "sessionid=added"); err != nil {
		t.Fatalf("Upsert add: %v", err)
	}
	if got := len(store.Cookies()); got != 2 {
		t.Fatalf("sessions = %d, want 2", got)
	}

	// Replacing by label keeps the entry count rather than appending.
	if err := store.Upsert("added", "sessionid=replaced"); err != nil {
		t.Fatalf("Upsert replace: %v", err)
	}
	if got := len(store.Cookies()); got != 2 {
		t.Errorf("sessions = %d after a replace, want 2", got)
	}
	if !strings.Contains(strings.Join(store.Cookies(), " "), "replaced") {
		t.Error("the replacement was not stored")
	}

	// The other entry survives, because the file belongs to the operator.
	if !strings.Contains(strings.Join(store.Cookies(), " "), "keep") {
		t.Error("an unrelated session was dropped")
	}

	// And the file on disk is what the next start will read.
	raw, err := os.ReadFile(cfg.AccountsFile)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	var entries []accountFile
	if err := json.Unmarshal(raw, &entries); err != nil {
		t.Fatalf("the written file is not valid JSON: %v", err)
	}
	if len(entries) != 2 {
		t.Errorf("file has %d entries, want 2", len(entries))
	}
}

func TestUpsertRejectsAUsableLookingButEmptyCookie(t *testing.T) {
	cfg := testAccountsConfig(t)
	// No Load: at startup a missing file is an error, but Upsert creates one.
	store := NewAccountStore(cfg)
	if err := store.Upsert("x", "ttwid=only"); err == nil {
		t.Error("a cookie with no sessionid was accepted")
	}
	if err := store.Upsert("x", "   "); err == nil {
		t.Error("an empty cookie was accepted")
	}
	if _, err := os.Stat(cfg.AccountsFile); !os.IsNotExist(err) {
		t.Error("a rejected cookie still created the file")
	}
}

func TestUpsertWritesMode0600(t *testing.T) {
	cfg := testAccountsConfig(t)
	store := NewAccountStore(cfg)
	if err := store.Upsert("a", "sessionid=aaa"); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	info, err := os.Stat(cfg.AccountsFile)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	// Windows only toggles the read-only bit, so the mode reads back as 0666
	// there. The device is Linux and CI runs on Linux, which is where this
	// assertion is meaningful.
	if runtime.GOOS != "windows" {
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("file mode = %o, want 600: it holds live sessions", perm)
		}
	}

	// No temporary file is left behind by the atomic write.
	entries, err := os.ReadDir(filepath.Dir(cfg.AccountsFile))
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".accounts-") {
			t.Errorf("a temporary file was left behind: %s", e.Name())
		}
	}
}

func TestRemoveDropsOnlyTheNamedSession(t *testing.T) {
	cfg := testAccountsConfig(t)
	writeAccounts(t, cfg, `[{"label":"a","cookie":"sessionid=aaa"},{"label":"b","cookie":"sessionid=bbb"}]`)
	store := NewAccountStore(cfg)
	if err := store.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	if err := store.Remove("a"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	cookies := store.Cookies()
	if len(cookies) != 1 || !strings.Contains(cookies[0], "bbb") {
		t.Errorf("cookies = %v, want only bbb", cookies)
	}
	if err := store.Remove("nope"); err == nil {
		t.Error("removing an unknown label succeeded")
	}
}

// verificationServer answers the recent-conv call with the shapes the upstream
// actually returns, which is what makes the test meaningful: the failure signal
// is status_code, and a dead session answers 200 with an EMPTY downlink_body
// object rather than an error.
func verificationServer(t *testing.T, live bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if live {
			_, _ = w.Write([]byte(`{"cmd":3200,"downlink_body":{"pull_recent_conv_chain_downlink_body":` +
				`{"messages":[],"has_more":false,"cells":[{"id":"38416996481149201"}]}},"version":"1"}`))
			return
		}
		_, _ = w.Write([]byte(`{"cmd":3200,"downlink_body":{},"version":"1",` +
			`"status_code":712012001,"status_desc":"閻ц缍嶉弽锟犵崣娑撳秴鎮庡▔?,"ext":{"tt_stable":"0"}}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newVerifyUpstream(t *testing.T, srv *httptest.Server) *Upstream {
	t.Helper()
	egress, err := NewEgress(Config{EgressMode: "direct", FirstByteTimeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("NewEgress: %v", err)
	}
	t.Cleanup(egress.Close)
	up := NewUpstream(egress)
	up.base = srv.URL
	return up
}

func TestVerifySessionAcceptsALiveSession(t *testing.T) {
	up := newVerifyUpstream(t, verificationServer(t, true))
	if err := up.VerifySession(context.Background(), "sessionid=live; msToken=x; s_v_web_id=y"); err != nil {
		t.Fatalf("VerifySession: %v", err)
	}
}

// The device run is what found this: a bogus sessionid was reported as verified
// because the check looked at "code", which this endpoint does not use.
func TestVerifySessionRejectsADeadSession(t *testing.T) {
	up := newVerifyUpstream(t, verificationServer(t, false))
	err := up.VerifySession(context.Background(), "sessionid=deadbeefdeadbeefdeadbeefdeadbeef")
	if err == nil {
		t.Fatal("a session the upstream refused was reported as verified")
	}
	if !strings.Contains(err.Error(), "712012001") {
		t.Errorf("error %q does not carry the upstream's status code", err)
	}
}

func TestVerifySessionRejectsA200WithNoConversationList(t *testing.T) {
	// A response that says nothing either way must not be read as success.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"cmd":3200,"downlink_body":{},"version":"1"}`))
	}))
	defer srv.Close()

	up := newVerifyUpstream(t, srv)
	if err := up.VerifySession(context.Background(), "sessionid=x"); err == nil {
		t.Error("a response with no conversation list was accepted")
	}
}

func TestVerifySessionRejectsACookieWithoutASession(t *testing.T) {
	egress, err := NewEgress(Config{EgressMode: "direct"})
	if err != nil {
		t.Fatalf("NewEgress: %v", err)
	}
	defer egress.Close()
	up := NewUpstream(egress)
	if err := up.VerifySession(context.Background(), "ttwid=only"); err == nil {
		t.Error("a cookie with no sessionid was verified")
	}
}

// A refusal that is about the upstream parks every session in turn until the
// pool is empty, and the service then reports "no session available" 鈥?a symptom
// that hides its own cause. Two things follow: the operator needs a way to say
// "the cause is dealt with", and the error has to name the cause.
func TestClearCooldownsReleasesParkedSessions(t *testing.T) {
	cfg := testAccountsConfig(t)
	cfg.CooldownSec = 600
	writeAccounts(t, cfg, `[{"label":"a","cookie":"sessionid=aaa"},{"label":"b","cookie":"sessionid=bbb"}]`)
	store := NewAccountStore(cfg)
	if err := store.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	now := time.Now()
	for _, label := range []string{"a", "b"} {
		account, err := store.Pick(now)
		if err != nil {
			t.Fatalf("Pick: %v", err)
		}
		_ = label
		store.RecordFailure(account, now, errTest)
	}
	if store.Available(now) {
		t.Fatal("the sessions were not parked, so this test proves nothing")
	}

	cleared := store.ClearCooldowns()
	if cleared != 2 {
		t.Errorf("cleared = %d, want 2", cleared)
	}
	if !store.Available(now) {
		t.Error("nothing is available after clearing the cooldowns")
	}
	// The backoff must be reset too, or the next failure parks them for longer
	// than the first one did.
	account, err := store.Pick(now)
	if err != nil {
		t.Fatalf("Pick after reset: %v", err)
	}
	store.RecordFailure(account, now, errTest)
	entry := store.Health(now)[0].(map[string]any)
	if remaining := entry["cooldown_remaining"].(int); remaining > cfg.CooldownSec+1 {
		t.Errorf("cooldown = %ds after a reset, want about %ds: the backoff was not cleared",
			remaining, cfg.CooldownSec)
	}
}

func TestNoSessionErrorNamesTheLastRefusal(t *testing.T) {
	cfg := testAccountsConfig(t)
	cfg.CooldownSec = 600
	writeAccounts(t, cfg, `[{"label":"a","cookie":"sessionid=aaa"}]`)
	store := NewAccountStore(cfg)
	if err := store.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	now := time.Now()
	account, err := store.Pick(now)
	if err != nil {
		t.Fatalf("Pick: %v", err)
	}
	store.RecordFailure(account, now, errors.New("upstream refused: 710022004"))

	explained := store.NoSessionError()
	if !errors.Is(explained, ErrNoAccount) {
		t.Errorf("the error no longer matches ErrNoAccount: %v", explained)
	}
	if !strings.Contains(explained.Error(), "710022004") {
		t.Errorf("the error does not name the upstream refusal: %v", explained)
	}
	if !strings.Contains(explained.Error(), "parked") {
		t.Errorf("the error does not say what happened: %v", explained)
	}
}

func TestAdminResetReleasesCooldownsAndNeedsTheAdminKey(t *testing.T) {
	exec := &fakeExecutor{conversationID: "conv-1"}
	api, _, accounts := newTestAPI(t, exec, Config{})
	if err := accounts.Upsert("extra", "sessionid=extra"); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	now := time.Now()
	for {
		account, err := accounts.Pick(now)
		if err != nil {
			break
		}
		accounts.RecordFailure(account, now, errors.New("refused"))
	}
	if accounts.Available(now) {
		t.Fatal("the sessions were not parked, so this test proves nothing")
	}

	router := apiRouter(api)
	if rec := doRequest(t, router, http.MethodPost, "/admin/reset", "", "sk-test"); rec.Code != http.StatusUnauthorized {
		t.Errorf("an ordinary API key cleared the cooldowns: status %d", rec.Code)
	}

	rec := doRequest(t, router, http.MethodPost, "/admin/reset", "", "admin-test")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body["cleared"] == nil || body["cleared"].(float64) < 1 {
		t.Errorf("the response does not say what was cleared: %s", rec.Body.String())
	}
	if !accounts.Available(time.Now()) {
		t.Error("nothing is available after a reset")
	}
}
func TestPanelIsServedAndCarriesNoSecrets(t *testing.T) {
	exec := &fakeExecutor{conversationID: "conv-1"}
	api, _, _ := newTestAPI(t, exec, Config{})

	rec := doRequest(t, apiRouter(api), http.MethodGet, "/admin", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: a browser cannot send a bearer token when navigating", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q", ct)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "dola2api") {
		t.Error("the page does not look like the panel")
	}
	// The page is the one unauthenticated route, so it must carry nothing secret.
	// The word "sessionid" legitimately appears in the input placeholder; what
	// must not appear is any actual credential.
	for _, needle := range []string{"sk-test", "admin-test", "sessionid=aaa", "sid_guard=84c4"} {
		if strings.Contains(body, needle) {
			t.Errorf("the panel page contains the credential %q", needle)
		}
	}
}

func TestAddAccountEndpointVerifiesBeforeSaving(t *testing.T) {
	srv := verificationServer(t, true)
	egress, err := NewEgress(Config{EgressMode: "direct", FirstByteTimeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("NewEgress: %v", err)
	}
	defer egress.Close()
	up := NewUpstream(egress)
	up.base = srv.URL

	exec := &fakeExecutor{conversationID: "conv-1"}
	api, _, accounts := newTestAPI(t, exec, Config{})
	api.upstream = up

	rec := doRequest(t, apiRouter(api), http.MethodPost, "/admin/accounts",
		`{"label":"fresh","cookie":"sessionid=fresh; msToken=x"}`, "admin-test")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var result addAccountResult
	_ = json.Unmarshal(rec.Body.Bytes(), &result)
	if !result.OK || !result.Verified {
		t.Errorf("result = %+v, want ok and verified", result)
	}
	if len(accounts.Cookies()) != 2 {
		t.Errorf("sessions = %d, want the original plus the new one", len(accounts.Cookies()))
	}
}

func TestAddAccountEndpointRefusesADeadCookie(t *testing.T) {
	srv := verificationServer(t, false)
	egress, err := NewEgress(Config{EgressMode: "direct", FirstByteTimeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("NewEgress: %v", err)
	}
	defer egress.Close()
	up := NewUpstream(egress)
	up.base = srv.URL

	exec := &fakeExecutor{conversationID: "conv-1"}
	api, _, accounts := newTestAPI(t, exec, Config{})
	api.upstream = up
	before := len(accounts.Cookies())

	rec := doRequest(t, apiRouter(api), http.MethodPost, "/admin/accounts",
		`{"label":"dead","cookie":"sessionid=dead"}`, "admin-test")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 so the operator can paste a different one", rec.Code)
	}
	if len(accounts.Cookies()) != before {
		t.Error("a session that failed verification was written anyway")
	}
}

func TestRemoveAccountEndpoint(t *testing.T) {
	exec := &fakeExecutor{conversationID: "conv-1"}
	api, _, accounts := newTestAPI(t, exec, Config{})
	if err := accounts.Upsert("extra", "sessionid=extra"); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	rec := doRequest(t, apiRouter(api), http.MethodDelete, "/admin/accounts/extra", "", "admin-test")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if len(accounts.Cookies()) != 1 {
		t.Errorf("sessions = %d, want 1", len(accounts.Cookies()))
	}

	missing := doRequest(t, apiRouter(api), http.MethodDelete, "/admin/accounts/nope", "", "admin-test")
	if missing.Code != http.StatusNotFound {
		t.Errorf("removing an unknown label gave %d, want 404", missing.Code)
	}
}

func TestAccountEndpointsRequireTheAdminKey(t *testing.T) {
	exec := &fakeExecutor{conversationID: "conv-1"}
	api, _, _ := newTestAPI(t, exec, Config{})
	router := apiRouter(api)

	if rec := doRequest(t, router, http.MethodPost, "/admin/accounts",
		`{"cookie":"sessionid=x"}`, "sk-test"); rec.Code != http.StatusUnauthorized {
		t.Errorf("an ordinary API key added an account: status %d", rec.Code)
	}
	if rec := doRequest(t, router, http.MethodDelete, "/admin/accounts/primary", "", "sk-test"); rec.Code != http.StatusUnauthorized {
		t.Errorf("an ordinary API key removed an account: status %d", rec.Code)
	}
}
