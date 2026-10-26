package hosting

import (
	"net"
	"sync"
	"time"
)

type rateBudget struct {
	at     time.Time
	tokens float64
}

func (b *rateBudget) allow(now time.Time, rate, burst float64) bool {
	if b.at.IsZero() {
		b.at = now
		b.tokens = burst
	}
	elapsed := now.Sub(b.at).Seconds()
	if elapsed < 0 {
		elapsed = 0
	}
	b.tokens = min(burst, b.tokens+elapsed*rate)
	b.at = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

type limitedListener struct {
	net.Listener
	slots chan struct{}
	done  chan struct{}
	once  sync.Once
}

func limitConnections(listener net.Listener, max int) net.Listener {
	return &limitedListener{Listener: listener, slots: make(chan struct{}, max), done: make(chan struct{})}
}
func (l *limitedListener) Accept() (net.Conn, error) {
	select {
	case l.slots <- struct{}{}:
	case <-l.done:
		return nil, net.ErrClosed
	}
	c, err := l.Listener.Accept()
	if err != nil {
		<-l.slots
		return nil, err
	}
	return &limitedConn{Conn: c, release: func() { <-l.slots }}, nil
}
func (l *limitedListener) Close() error {
	l.once.Do(func() { close(l.done) })
	return l.Listener.Close()
}

type limitedConn struct {
	net.Conn
	once    sync.Once
	release func()
}

func (c *limitedConn) Close() error { err := c.Conn.Close(); c.once.Do(c.release); return err }
