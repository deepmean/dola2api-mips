package main

import (
	"net"
	"runtime"
	"sync"
	"testing"
	"time"
)

func listenBounded(t *testing.T, n int) (net.Listener, string) {
	t.Helper()
	inner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	return LimitListener(inner, n), inner.Addr().String()
}

// acceptAsync accepts a single connection in the background and reports it. A
// nil connection means Accept failed.
func acceptAsync(l net.Listener) <-chan net.Conn {
	out := make(chan net.Conn, 1)
	go func() {
		c, err := l.Accept()
		if err != nil {
			out <- nil
			return
		}
		out <- c
	}()
	return out
}

func mustAccept(t *testing.T, l net.Listener, what string) net.Conn {
	t.Helper()
	select {
	case c := <-acceptAsync(l):
		if c == nil {
			t.Fatalf("%s: accept failed", what)
		}
		return c
	case <-time.After(2 * time.Second):
		t.Fatalf("%s: accept timed out", what)
		return nil
	}
}

func TestLimitListenerBoundsConcurrentAccepts(t *testing.T) {
	l, addr := listenBounded(t, 2)
	defer l.Close()

	var clients, servers []net.Conn
	defer func() {
		for _, c := range clients {
			c.Close()
		}
		for _, c := range servers {
			c.Close()
		}
	}()

	for i := 0; i < 2; i++ {
		c, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatalf("dial %d: %v", i, err)
		}
		clients = append(clients, c)
		servers = append(servers, mustAccept(t, l, "initial"))
	}

	// Third connection: with the bound reached, Accept must stay parked.
	third, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial third: %v", err)
	}
	clients = append(clients, third)

	parked := acceptAsync(l)
	select {
	case c := <-parked:
		if c != nil {
			c.Close()
		}
		t.Fatal("third connection was accepted while the bound was reached")
	case <-time.After(300 * time.Millisecond):
		// expected
	}

	// Releasing one server-side slot must let the parked Accept through.
	servers[0].Close()
	select {
	case c := <-parked:
		if c == nil {
			t.Fatal("parked accept failed after a slot was released")
		}
		c.Close()
	case <-time.After(2 * time.Second):
		t.Fatal("parked accept never completed after a slot was released")
	}
}

// Closing must wake parked Accept calls, otherwise shutdown hangs whenever the
// bound is saturated.
func TestLimitListenerCloseReleasesBlockedAccepts(t *testing.T) {
	l, addr := listenBounded(t, 1)
	defer l.Close()

	first, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial first: %v", err)
	}
	defer first.Close()
	server := mustAccept(t, l, "first")
	defer server.Close()

	second, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial second: %v", err)
	}
	defer second.Close()

	parked := acceptAsync(l)
	select {
	case <-parked:
		t.Fatal("second accept should be parked while the bound is reached")
	case <-time.After(300 * time.Millisecond):
	}

	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case c := <-parked:
		if c != nil {
			c.Close()
			t.Fatal("a closed listener handed out a connection")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not wake the parked Accept")
	}
}

// The done channel is closed once; a second Close must not panic.
func TestLimitListenerDoubleCloseDoesNotPanic(t *testing.T) {
	l, _ := listenBounded(t, 1)
	if err := l.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	l.Close() // second call: error is acceptable, panic is not

	if c, err := l.Accept(); err == nil {
		c.Close()
		t.Error("Accept after Close should fail")
	}
}

// Closing a server connection twice must release the slot exactly once, or the
// semaphore drifts and eventually permits unbounded concurrency.
func TestLimitListenerConnectionReleaseIsIdempotent(t *testing.T) {
	l, addr := listenBounded(t, 1)
	defer l.Close()

	client, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	server := mustAccept(t, l, "first")
	server.Close()
	server.Close()

	client2, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial after release: %v", err)
	}
	defer client2.Close()

	next := mustAccept(t, l, "after release")
	next.Close()
}

func TestLimitListenerNoGoroutineLeak(t *testing.T) {
	runtime.GC()
	time.Sleep(warmupDelay)
	before := runtime.NumGoroutine()

	l, addr := listenBounded(t, 4)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		c, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatalf("dial %d: %v", i, err)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			server, err := l.Accept()
			if err == nil {
				server.Close()
			}
		}()
		c.Close()
	}
	wg.Wait()
	l.Close()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		runtime.GC()
		if runtime.NumGoroutine() <= before+2 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Errorf("goroutines grew from %d to %d", before, runtime.NumGoroutine())
}

func TestLimitListenerWithNonPositiveBoundIsPassthrough(t *testing.T) {
	inner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer inner.Close()
	if got := LimitListener(inner, 0); got != net.Listener(inner) {
		t.Error("n<=0 should return the original listener unchanged")
	}
}

// warmupDelay lets the runtime settle before a goroutine-count baseline is
// taken; short enough not to matter, long enough to avoid a flaky assertion.
const warmupDelay = 100 * time.Millisecond
