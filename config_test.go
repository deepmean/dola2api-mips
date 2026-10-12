package main

import (
	"strings"
	"testing"
	"time"
)

// envMap turns a map into a getenv func so tests never touch the real process
// environment.
func envMap(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadConfigDefaults(t *testing.T) {
	cfg, err := LoadConfig(envMap(nil))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	checks := []struct {
		name string
		got  any
		want any
	}{
		{"Port", cfg.Port, 8790},
		{"MaxConcurrency", cfg.MaxConcurrency, 1},
		{"MaxPendingTasks", cfg.MaxPendingTasks, 20},
		{"DailyLimit", cfg.DailyLimit, 2},
		{"CooldownSec", cfg.CooldownSec, 1800},
		{"StoreMode", cfg.StoreMode, "redirect"},
		{"EgressMode", cfg.EgressMode, "relay"},
		{"RelayURL", cfg.RelayURL, "https://vcrelay.dmean.dpdns.org/"},
		{"StrictProxy", cfg.StrictProxy, true},
		{"EgressRotate", cfg.EgressRotate, "none"},
		{"RelayMaxBody", cfg.RelayMaxBody, int64(4194304)},
		{"FirstByteTimeout", cfg.FirstByteTimeout, 25 * time.Second},
		{"SubmitDeadline", cfg.SubmitDeadline, 30 * time.Second},
		{"HeapSoftLimit", cfg.HeapSoftLimit, int64(32 << 20)},
		{"MaxConnections", cfg.MaxConnections, 32},
		{"MaxHeaderBytes", cfg.MaxHeaderBytes, 32768},
		{"FreeOSAfterJob", cfg.FreeOSAfterJob, true},
		{"Executor", cfg.Executor, "protocol"},
		{"VideoTimeout", cfg.VideoTimeout, 300 * time.Second},
		{"MirrorTTL", cfg.MirrorTTL, 24 * time.Hour},
		{"ReferenceImageMaxBytes", cfg.ReferenceImageMaxBytes, int64(4 << 20)},
		{"ReferenceImageMaxCount", cfg.ReferenceImageMaxCount, 30},
		{"Addr", cfg.Addr(), ":8790"},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
}

// OpenWrt has no tzdata: the reset zone must be a fixed offset, and the default
// must not depend on /usr/share/zoneinfo existing.
//
// "Does not depend on tzdata" is asserted behaviourally rather than by string
// comparison: a FixedZone reports the same offset in January and July and
// IsDST() is always false. A location loaded from zoneinfo would fail one of
// those for any zone that observes DST.
func TestLoadConfigResetTZIsFixedOffset(t *testing.T) {
	cfg, err := LoadConfig(envMap(nil))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if got := cfg.ResetTZ.String(); got != "Asia/Tokyo" {
		t.Errorf("default ResetTZ = %q, want Asia/Tokyo", got)
	}
	assertFixedOffset(t, cfg.ResetTZ, 9*3600)

	for _, tc := range []struct {
		raw      string
		wantName string
		wantOff  int
	}{
		{"Asia/Shanghai", "Asia/Shanghai", 8 * 3600},
		{"UTC", "UTC", 0},
		{"+08:00", "+08:00", 8 * 3600},
		{"-05:00", "-05:00", -5 * 3600},
		{"+0530", "+0530", 5*3600 + 30*60},
	} {
		cfg, err := LoadConfig(envMap(map[string]string{"DOLA_LIMIT_RESET_TZ": tc.raw}))
		if err != nil {
			t.Fatalf("LoadConfig(%q): %v", tc.raw, err)
		}
		if cfg.ResetTZ.String() != tc.wantName {
			t.Errorf("ResetTZ(%q).String() = %q, want %q", tc.raw, cfg.ResetTZ.String(), tc.wantName)
		}
		assertFixedOffset(t, cfg.ResetTZ, tc.wantOff)
	}
}

// assertFixedOffset proves loc behaves as a fixed offset: identical offset in
// mid-winter and mid-summer, and no DST in either.
func assertFixedOffset(t *testing.T, loc *time.Location, wantOffset int) {
	t.Helper()
	winter := time.Date(2026, time.January, 15, 12, 0, 0, 0, time.UTC).In(loc)
	summer := time.Date(2026, time.July, 15, 12, 0, 0, 0, time.UTC).In(loc)

	_, wOff := winter.Zone()
	_, sOff := summer.Zone()
	if wOff != wantOffset || sOff != wantOffset {
		t.Errorf("offset = %d (Jan) / %d (Jul), want %d in both", wOff, sOff, wantOffset)
	}
	if winter.IsDST() || summer.IsDST() {
		t.Errorf("location %q reports DST, which means it came from tzdata", loc)
	}
}

// A named zone outside the table must fail loudly instead of falling back to
// UTC (that silent fallback shifted "today" by 8 hours in wb-proxy).
func TestLoadConfigRejectsUnknownZoneName(t *testing.T) {
	_, err := LoadConfig(envMap(map[string]string{"DOLA_LIMIT_RESET_TZ": "Europe/Berlin"}))
	if err == nil {
		t.Fatal("expected error for a zone name with no fixed-offset entry")
	}
	if !strings.Contains(err.Error(), "Asia/Tokyo") {
		t.Errorf("error should list supported zones, got %v", err)
	}
}

func TestLoadConfigStrictProxyFlag(t *testing.T) {
	cfg, err := LoadConfig(envMap(map[string]string{"DOLA_STRICT_PROXY": "0"}))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.StrictProxy {
		t.Error("DOLA_STRICT_PROXY=0 should disable strict mode")
	}

	if _, err := LoadConfig(envMap(map[string]string{"DOLA_STRICT_PROXY": "yes"})); err == nil {
		t.Error("expected error for DOLA_STRICT_PROXY=yes (only 0/1 allowed)")
	}
}

func TestLoadConfigEgressValidation(t *testing.T) {
	if _, err := LoadConfig(envMap(map[string]string{"DOLA_RELAY_URL": "ftp://relay.example/"})); err == nil {
		t.Error("expected error for a non-http(s) relay URL")
	}
	if _, err := LoadConfig(envMap(map[string]string{"DOLA_RELAY_URL": "https://relay.example"})); err != nil {
		t.Errorf("https relay URL should be accepted, got %v", err)
	}

	// Both paths set with no explicit mode: refuse instead of guessing.
	both := map[string]string{
		"DOLA_RELAY_URL": "https://relay.example/",
		"DOLA_PROXY_URL": "http://127.0.0.1:10808",
	}
	if _, err := LoadConfig(envMap(both)); err == nil {
		t.Error("expected error when both relay and proxy are set without DOLA_EGRESS")
	}
	both["DOLA_EGRESS"] = "relay"
	if _, err := LoadConfig(envMap(both)); err != nil {
		t.Errorf("explicit DOLA_EGRESS=relay should resolve the ambiguity, got %v", err)
	}
}

func TestLoadConfigNoProxyList(t *testing.T) {
	cfg, err := LoadConfig(envMap(nil))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if len(cfg.NoProxy) != 4 {
		t.Fatalf("default NoProxy = %v, want 4 entries", cfg.NoProxy)
	}

	cfg, err = LoadConfig(envMap(map[string]string{"DOLA_NO_PROXY": " a.example , ,b.example, "}))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if len(cfg.NoProxy) != 2 || cfg.NoProxy[0] != "a.example" || cfg.NoProxy[1] != "b.example" {
		t.Errorf("NoProxy = %v, want [a.example b.example]", cfg.NoProxy)
	}
}

func TestLoadConfigPortValidation(t *testing.T) {
	for _, raw := range []string{"abc", "0", "70000", "-1"} {
		if _, err := LoadConfig(envMap(map[string]string{"DOLA_PORT": raw})); err == nil {
			t.Errorf("DOLA_PORT=%q should be rejected", raw)
		}
	}
	cfg, err := LoadConfig(envMap(map[string]string{"DOLA_PORT": "9090"}))
	if err != nil || cfg.Port != 9090 {
		t.Errorf("DOLA_PORT=9090 -> %v, %v", cfg.Port, err)
	}
}

func TestLoadConfigAPIKeys(t *testing.T) {
	cfg, err := LoadConfig(envMap(map[string]string{"DOLA_API_KEYS": "sk-a, sk-b ,"}))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if len(cfg.APIKeys) != 2 || cfg.APIKeys[0] != "sk-a" || cfg.APIKeys[1] != "sk-b" {
		t.Errorf("APIKeys = %v, want [sk-a sk-b]", cfg.APIKeys)
	}

	cfg, err = LoadConfig(envMap(map[string]string{"DOLA_API_KEYS": "   "}))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if len(cfg.APIKeys) != 0 {
		t.Errorf("blank API keys should yield an empty list, got %v", cfg.APIKeys)
	}
}

func TestLoadConfigExecutorChoice(t *testing.T) {
	if _, err := LoadConfig(envMap(map[string]string{"DOLA_EXECUTOR": "browser"})); err == nil {
		t.Error("expected error for an unsupported executor")
	}
	// sidecar without a URL would fail at request time; fail at load time instead.
	if _, err := LoadConfig(envMap(map[string]string{"DOLA_EXECUTOR": "sidecar"})); err == nil {
		t.Error("expected error for DOLA_EXECUTOR=sidecar without DOLA_SIDECAR_URL")
	}
	cfg, err := LoadConfig(envMap(map[string]string{
		"DOLA_EXECUTOR":    "sidecar",
		"DOLA_SIDECAR_URL": "http://192.168.26.10:8781",
	}))
	if err != nil || cfg.Executor != "sidecar" {
		t.Errorf("sidecar config -> %v, %v", cfg.Executor, err)
	}
}

func TestLoadConfigDurationsAcceptSecondsAndGoSyntax(t *testing.T) {
	cfg, err := LoadConfig(envMap(map[string]string{"DOLA_EGRESS_FIRST_BYTE_TIMEOUT": "20"}))
	if err != nil || cfg.FirstByteTimeout != 20*time.Second {
		t.Errorf("bare seconds -> %v, %v", cfg.FirstByteTimeout, err)
	}
	cfg, err = LoadConfig(envMap(map[string]string{"DOLA_SUBMIT_DEADLINE": "1m30s"}))
	if err != nil || cfg.SubmitDeadline != 90*time.Second {
		t.Errorf("go duration -> %v, %v", cfg.SubmitDeadline, err)
	}
	if _, err := LoadConfig(envMap(map[string]string{"DOLA_SUBMIT_DEADLINE": "0"})); err == nil {
		t.Error("expected error for a non-positive duration")
	}
	if _, err := LoadConfig(envMap(map[string]string{"DOLA_SUBMIT_DEADLINE": "soon"})); err == nil {
		t.Error("expected error for a non-duration value")
	}
}

func TestLoadConfigBoundsValidation(t *testing.T) {
	for _, tc := range []map[string]string{
		{"DOLA_MAX_CONCURRENCY": "0"},
		{"DOLA_MAX_PENDING_TASKS": "0"},
		{"DOLA_MAX_CONNECTIONS": "0"},
	} {
		if _, err := LoadConfig(envMap(tc)); err == nil {
			t.Errorf("%v should be rejected", tc)
		}
	}
}
