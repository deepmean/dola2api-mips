package main

import (
	"os"
	"strings"
	"testing"
)

// The shipped deploy files are part of the artifact, and a typo in them would
// otherwise only surface on the router. These tests parse them the way the
// device does.
//
// TestDeployConfLoads runs the real dola2api.conf through the real config
// loader, so a misspelled variable or a value the loader rejects fails in CI
// rather than on first boot.
func TestDeployConfLoads(t *testing.T) {
	env := parseShellConf(t, "deploy/dola2api.conf")
	if len(env) == 0 {
		t.Fatal("the deploy config parsed to nothing")
	}
	// The init script supplies these defaults itself; the loader must accept
	// them because the router always runs with them set.
	env["TZ"] = "CST-8"

	cfg, err := LoadConfig(func(key string) string { return env[key] })
	if err != nil {
		t.Fatalf("the shipped config does not load: %v", err)
	}

	// Spot-check the values that matter for a router deployment.
	if cfg.Port != 8790 {
		t.Errorf("port = %d, want 8790", cfg.Port)
	}
	if cfg.EgressMode != "relay" {
		t.Errorf("egress = %q, want relay (direct is refused upstream)", cfg.EgressMode)
	}
	if !cfg.StrictProxy {
		t.Error("strict proxy is off in the shipped config; the real address would leak")
	}
	if cfg.RelayURL == "" {
		t.Error("no relay configured")
	}
	if cfg.MaxConcurrency != 1 {
		t.Errorf("max concurrency = %d, want 1 on this device", cfg.MaxConcurrency)
	}
	if cfg.StoreMode != "redirect" {
		t.Errorf("store mode = %q, want redirect by default", cfg.StoreMode)
	}
	if cfg.HeapSoftLimit <= 0 {
		t.Error("no heap soft limit: the router's OOM killer would choose the victim")
	}
	if cfg.ResetTZ == nil {
		t.Error("no reset timezone")
	}
	if cfg.AccountsFile == "" {
		t.Error("no accounts file")
	}
}

// The init script is executed by procd on the device, so a syntax error there
// is a service that will not start. Checking what can be checked here: it must
// be POSIX sh, must not carry carriage returns, and must reference the files
// the deploy instructions place.
func TestDeployInitScriptIsSane(t *testing.T) {
	raw, err := os.ReadFile("deploy/dola2api.init")
	if err != nil {
		t.Fatalf("read init script: %v", err)
	}
	text := string(raw)

	if strings.Contains(text, "\r") {
		t.Error("the init script contains carriage returns; procd would fail with $'\\r': not found")
	}
	if !strings.HasPrefix(text, "#!/bin/sh") {
		t.Error("the init script does not start with a /bin/sh shebang")
	}
	if !strings.Contains(text, "USE_PROCD=1") {
		t.Error("the init script does not opt into procd")
	}
	for _, want := range []string{
		"/opt/dola2api/dola2api",
		"/opt/dola2api/dola2api.conf",
		"procd_open_instance",
		"procd_close_instance",
		"reload_service",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the init script is missing %q", want)
		}
	}

	// Every variable the init script forwards must be one the loader knows,
	// otherwise a typo silently ships a default instead.
	for _, name := range forwardedEnvNames(text) {
		if !strings.HasPrefix(name, "DOLA_") && name != "TZ" && name != "GOMEMLIMIT" && name != "GOGC" {
			t.Errorf("the init script forwards %q, which is not a setting this build reads", name)
		}
	}
}

// parseShellConf reads KEY=VALUE lines, which is the shape of both deploy files.
func parseShellConf(t *testing.T, path string) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	out := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		value = strings.Trim(value, `"'`)
		if key != "" {
			out[key] = value
		}
	}
	return out
}

// forwardedEnvNames pulls the names out of the procd_set_param env block only.
// Scanning the whole script would also pick up the init script's own variables
// (START, PROG, ...), which are not settings this build reads.
func forwardedEnvNames(script string) []string {
	lines := strings.Split(script, "\n")
	start := -1
	for i, line := range lines {
		if strings.Contains(line, "procd_set_param env") {
			start = i
			break
		}
	}
	if start < 0 {
		return nil
	}

	var names []string
	for _, line := range lines[start+1:] {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "procd_") {
			break // the env block is over
		}
		if trimmed == "" {
			continue
		}
		key, _, ok := strings.Cut(trimmed, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if key == "" || strings.ContainsAny(key, " \t") {
			continue
		}
		names = append(names, key)
	}
	return names
}
