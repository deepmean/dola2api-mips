package browserenv

import (
	"sort"
	"time"

	"github.com/dop251/goja"
)

// Timers implements setTimeout/setInterval for a goja runtime.
//
// A stub that returns an id without ever calling back looks harmless, but it
// silently disables every deferred step in an SDK. The dola signing SDK defers
// its token exchange, so with a no-op setTimeout it never fetched a token and
// never signed anything, which looked exactly like "the interceptor is broken".
//
// goja is single-threaded, so callbacks run on the caller's goroutine: the host
// drives them with RunPending between scripts rather than letting a background
// goroutine touch the runtime.
type Timers struct {
	vm      *goja.Runtime
	pending []*scheduled
	nextID  int64
}

type scheduled struct {
	id        int64
	callback  goja.Callable
	due       time.Time
	interval  time.Duration // zero for a one-shot timer
	cancelled bool
}

// NewTimers returns an uninstalled timer registry.
func NewTimers() *Timers { return &Timers{} }

// Install replaces window.setTimeout, clearTimeout, setInterval and
// clearInterval in vm.
func (t *Timers) Install(vm *goja.Runtime) error {
	t.vm = vm
	window, ok := vm.Get("window").(*goja.Object)
	if !ok {
		return errNoPrelude
	}
	window.Set("setTimeout", t.schedule(false))
	window.Set("setInterval", t.schedule(true))
	window.Set("clearTimeout", t.cancel)
	window.Set("clearInterval", t.cancel)
	return nil
}

func (t *Timers) schedule(repeating bool) func(goja.FunctionCall) goja.Value {
	return func(call goja.FunctionCall) goja.Value {
		fn, ok := goja.AssertFunction(call.Argument(0))
		if !ok {
			return goja.Undefined()
		}
		ms := int64(call.Argument(1).ToInteger())
		if ms < 0 {
			ms = 0
		}
		t.nextID++
		entry := &scheduled{id: t.nextID, callback: fn, due: time.Now().Add(time.Duration(ms) * time.Millisecond)}
		if repeating {
			entry.interval = time.Duration(ms) * time.Millisecond
			if entry.interval <= 0 {
				entry.interval = time.Millisecond
			}
		}
		t.pending = append(t.pending, entry)
		return t.vm.ToValue(entry.id)
	}
}

func (t *Timers) cancel(call goja.FunctionCall) goja.Value {
	id := call.Argument(0).ToInteger()
	for _, e := range t.pending {
		if e.id == id {
			e.cancelled = true
		}
	}
	return goja.Undefined()
}

// RunPending runs every timer that is due, waiting for the earliest one when
// none is yet. It returns the number of callbacks it ran, so a host can pump
// until the runtime stops scheduling work.
func (t *Timers) RunPending(wait time.Duration) int {
	ran := 0
	deadline := time.Now().Add(wait)
	for {
		now := time.Now()
		var due []*scheduled
		for _, e := range t.pending {
			if !e.cancelled && !e.due.After(now) {
				due = append(due, e)
			}
		}
		if len(due) == 0 {
			// Nothing is due: if something is scheduled before the deadline,
			// wait for it rather than spinning.
			next := t.earliest(now)
			if next.IsZero() || next.After(deadline) {
				return ran
			}
			time.Sleep(time.Until(next))
			continue
		}
		sort.Slice(due, func(i, j int) bool { return due[i].due.Before(due[j].due) })
		for _, e := range due {
			if e.cancelled {
				continue
			}
			if e.interval > 0 {
				e.due = time.Now().Add(e.interval)
			} else {
				e.cancelled = true
			}
			_, _ = e.callback(goja.Undefined())
			ran++
		}
		t.compact()
		if time.Now().After(deadline) {
			return ran
		}
	}
}

func (t *Timers) earliest(now time.Time) time.Time {
	var best time.Time
	for _, e := range t.pending {
		if e.cancelled {
			continue
		}
		if best.IsZero() || e.due.Before(best) {
			best = e.due
		}
	}
	return best
}

func (t *Timers) compact() {
	kept := t.pending[:0]
	for _, e := range t.pending {
		if !e.cancelled {
			kept = append(kept, e)
		}
	}
	t.pending = kept
}

// Pending reports how many timers are still armed.
func (t *Timers) Pending() int { return len(t.pending) }
