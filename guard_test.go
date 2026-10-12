package main

import (
	"errors"
	"runtime"
	"strings"
	"testing"
)

func TestGuardCheckDisabledWhenLimitNonPositive(t *testing.T) {
	g := NewGuard(0, nil, false)
	if err := g.Check(); err != nil {
		t.Errorf("softLimit=0 should disable the check, got %v", err)
	}
	g = NewGuard(-1, nil, false)
	if err := g.Check(); err != nil {
		t.Errorf("negative softLimit should disable the check, got %v", err)
	}
	var nilGuard *Guard
	if err := nilGuard.Check(); err != nil {
		t.Errorf("nil guard should be a no-op, got %v", err)
	}
}

// A 1-byte limit is guaranteed to be exceeded by any running Go program, so it
// proves the failure path without allocating a large buffer.
func TestGuardCheckTriggersOnTinyLimit(t *testing.T) {
	g := NewGuard(1, nil, false)
	err := g.Check()
	if err == nil {
		t.Fatal("expected ErrHeapSoftLimit with a 1-byte soft limit")
	}
	if !errors.Is(err, ErrHeapSoftLimit) {
		t.Errorf("errors.Is(err, ErrHeapSoftLimit) = false for %v", err)
	}
	// The message must be actionable: it is what the operator reads in logread
	// and in the 507 response.
	if !strings.Contains(err.Error(), "DOLA_HEAP_SOFT_LIMIT") {
		t.Errorf("error should name the knob to raise, got %v", err)
	}
}

func TestGuardCheckPassesWithGenerousLimit(t *testing.T) {
	g := NewGuard(1<<40, nil, false) // 1TiB: never reached in a test
	if err := g.Check(); err != nil {
		t.Errorf("expected no error with a generous limit, got %v", err)
	}
}

func TestGuardSnapshot(t *testing.T) {
	g := NewGuard(32<<20, nil, true)
	snap := g.Snapshot()
	if snap.HeapAlloc == 0 {
		t.Error("HeapAlloc should be non-zero in a running program")
	}
	if snap.Sys == 0 {
		t.Error("Sys should be non-zero in a running program")
	}
	if snap.Goroutines < 1 {
		t.Errorf("Goroutines = %d, want >= 1", snap.Goroutines)
	}
	if snap.SoftLimit != 32<<20 {
		t.Errorf("SoftLimit = %d, want %d", snap.SoftLimit, 32<<20)
	}
	if !snap.FreeAfterGC {
		t.Error("FreeAfterGC should mirror the enabled flag")
	}

	var nilGuard *Guard
	if snap := nilGuard.Snapshot(); snap.Goroutines < 1 {
		t.Error("nil guard Snapshot should still report runtime numbers")
	}
}

// AfterJob must call the injected hook exactly when it is enabled, and must not
// call it when disabled -- forcing a GC on every job when the operator turned it
// off would be a surprise CPU cost.
func TestGuardAfterJobCallsHookOnlyWhenEnabled(t *testing.T) {
	calls := 0
	hook := func() { calls++ }

	g := NewGuard(32<<20, hook, true)
	g.AfterJob()
	g.AfterJob()
	if calls != 2 {
		t.Errorf("hook called %d times, want 2", calls)
	}

	calls = 0
	g = NewGuard(32<<20, hook, false)
	g.AfterJob()
	if calls != 0 {
		t.Errorf("hook called %d times with FreeOSAfterJob=false, want 0", calls)
	}

	// A nil hook must not panic even when enabled.
	g = NewGuard(32<<20, nil, true)
	g.AfterJob()

	var nilGuard *Guard
	nilGuard.AfterJob()
}

// freeOSMemory is what production wires in; the test only asserts it is a real
// function so a refactor cannot silently turn it into a no-op.
func TestFreeOSMemoryHookIsWired(t *testing.T) {
	if freeOSMemory == nil {
		t.Fatal("freeOSMemory must be wired to debug.FreeOSMemory")
	}
	before := runtime.NumGoroutine()
	freeOSMemory()
	if after := runtime.NumGoroutine(); after < before {
		t.Errorf("goroutines dropped from %d to %d across FreeOSMemory", before, after)
	}
}
