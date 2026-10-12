package main

import (
	"errors"
	"fmt"
	"runtime"
	"runtime/debug"
)

// ErrHeapSoftLimit is returned by Guard.Check when the Go heap has grown past
// the configured soft limit.
//
// The router has 250MB of RAM shared with the whole system. OpenWrt's OOM
// killer does not know which process caused the pressure, so letting it decide
// can take out sshd or a network helper. Failing the in-flight task with a
// readable error is strictly better than being killed: the process stays up,
// /health stays reachable, and the operator sees the reason in logread.
var ErrHeapSoftLimit = errors.New("heap soft limit exceeded")

// MemSnapshot is the memory view exposed on /health. It is read on demand only
// (never in a hot path) so it costs nothing while idle.
type MemSnapshot struct {
	HeapAlloc   uint64 `json:"heap_alloc"`
	HeapInuse   uint64 `json:"heap_inuse"`
	HeapSys     uint64 `json:"heap_sys"`
	Sys         uint64 `json:"sys"`
	TotalAlloc  uint64 `json:"total_alloc"`
	NumGC       uint32 `json:"num_gc"`
	Goroutines  int    `json:"goroutines"`
	SoftLimit   uint64 `json:"soft_limit"`
	FreeAfterGC bool   `json:"free_os_after_job"`
}

// Guard enforces the heap soft limit and, at task boundaries, returns freed
// memory to the kernel.
//
// Go only releases memory to the OS during a GC cycle; calling FreeOSMemory at
// the end of each job pins that release to the moment the spike is over, which
// keeps MemAvailable predictable on a 250MB device. It is deliberately NOT
// called on a hot path: forcing a GC per request would burn CPU for no gain.
type Guard struct {
	softLimit   int64
	freeOS      func()
	freeEnabled bool
}

// NewGuard builds a Guard. softLimit <= 0 disables the check; freeOS may be nil
// (tests inject a counter; production passes debug.FreeOSMemory).
func NewGuard(softLimit int64, freeOS func(), freeEnabled bool) *Guard {
	return &Guard{softLimit: softLimit, freeOS: freeOS, freeEnabled: freeEnabled}
}

// NewGuardFromConfig wires the guard to the configured soft limit and the
// production FreeOSMemory hook.
func NewGuardFromConfig(cfg Config) *Guard {
	return NewGuard(cfg.HeapSoftLimit, freeOSMemory, cfg.FreeOSAfterJob)
}

// Check reports whether the heap is still under the soft limit. Call it before
// a large allocation (decoding a reference image, parsing a big poll response).
func (g *Guard) Check() error {
	if g == nil || g.softLimit <= 0 {
		return nil
	}
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	if int64(ms.HeapAlloc) > g.softLimit {
		return fmt.Errorf("%w: heap_alloc=%d bytes > limit=%d bytes; failing this task instead of risking an OOM kill (raise DOLA_HEAP_SOFT_LIMIT to allow more)",
			ErrHeapSoftLimit, ms.HeapAlloc, g.softLimit)
	}
	return nil
}

// AfterJob releases memory back to the OS when enabled. Safe to call once per
// finished job (success, failure, or cancellation).
func (g *Guard) AfterJob() {
	if g == nil || !g.freeEnabled || g.freeOS == nil {
		return
	}
	g.freeOS()
}

// Snapshot returns the current memory view for /health.
func (g *Guard) Snapshot() MemSnapshot {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	snap := MemSnapshot{
		HeapAlloc:  ms.HeapAlloc,
		HeapInuse:  ms.HeapInuse,
		HeapSys:    ms.HeapSys,
		Sys:        ms.Sys,
		TotalAlloc: ms.TotalAlloc,
		NumGC:      ms.NumGC,
		Goroutines: runtime.NumGoroutine(),
	}
	if g != nil {
		if g.softLimit > 0 {
			snap.SoftLimit = uint64(g.softLimit)
		}
		snap.FreeAfterGC = g.freeEnabled
	}
	return snap
}

// freeOSMemory is the production hook. Kept behind a var so tests can prove the
// call happens exactly at task boundaries without triggering a real GC.
var freeOSMemory = debug.FreeOSMemory
