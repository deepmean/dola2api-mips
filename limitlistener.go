package main

import (
	"errors"
	"net"
	"sync"
)

// errListenerClosed is returned by Accept once the bounded listener is closed.
var errListenerClosed = errors.New("limit listener closed")

// limitListener bounds the number of connections served at once.
//
// This mirrors x/net/netutil.LimitListener, reimplemented because the module is
// stdlib-only by design. The bound matters on a 250MB device: every accepted
// connection holds buffers and a goroutine, and a slow client can hold them
// indefinitely. Excess connections wait in Accept instead of being served, so
// memory cannot grow with inbound socket count.
type limitListener struct {
	net.Listener
	sem       chan struct{}
	done      chan struct{}
	closeOnce sync.Once
}

// LimitListener wraps l so at most n connections are served simultaneously.
func LimitListener(l net.Listener, n int) net.Listener {
	if n <= 0 {
		return l
	}
	return &limitListener{
		Listener: l,
		sem:      make(chan struct{}, n),
		done:     make(chan struct{}),
	}
}

func (l *limitListener) acquire() bool {
	select {
	case <-l.done:
		return false
	case l.sem <- struct{}{}:
		return true
	}
}

func (l *limitListener) release() { <-l.sem }

func (l *limitListener) Accept() (net.Conn, error) {
	if !l.acquire() {
		return nil, errListenerClosed
	}
	c, err := l.Listener.Accept()
	if err != nil {
		l.release()
		return nil, err
	}
	return &limitListenerConn{Conn: c, release: l.release}, nil
}

func (l *limitListener) Close() error {
	err := l.Listener.Close()
	// Waking every blocked Accept is what makes shutdown deterministic: without
	// it, a full semaphore would leave goroutines parked until a client leaves.
	l.closeOnce.Do(func() { close(l.done) })
	return err
}

type limitListenerConn struct {
	net.Conn
	releaseOnce sync.Once
	release     func()
}

func (c *limitListenerConn) Close() error {
	err := c.Conn.Close()
	c.releaseOnce.Do(c.release)
	return err
}
