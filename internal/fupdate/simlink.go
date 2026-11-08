package fupdate

import (
	"sync"
	"time"
)

// LinkFaults injects transport errors into a SimLink (host -> machine).
type LinkFaults struct {
	DropByteAt    []int // drop the n-th byte (0-based) sent by the host
	CorruptByteAt []int // flip bits in the n-th byte sent by the host
}

// SimLink is an in-memory null-modem cable. Bytes sent while the two ends
// use different baud rates arrive garbled, like on a real line.
type SimLink struct {
	Host, Machine *SimPort
	// Speed scales the simulated line speed: 0 = instant, 1 = real time.
	Speed float64
	mu    sync.Mutex
	sent  int // bytes sent by the host so far
	f     LinkFaults
}

// SimPort is one end of a SimLink.
type SimPort struct {
	link   *SimLink
	peer   *SimPort
	mu     sync.Mutex
	rx     []byte
	notify chan struct{}
	baud   int
	isHost bool
}

// NewSimLink creates a connected pair of ports.
func NewSimLink(f LinkFaults) *SimLink {
	l := &SimLink{f: f}
	l.Host = &SimPort{link: l, notify: make(chan struct{}, 1), baud: ShellBaud, isHost: true}
	l.Machine = &SimPort{link: l, notify: make(chan struct{}, 1), baud: ShellBaud}
	l.Host.peer, l.Machine.peer = l.Machine, l.Host
	return l
}

func contains(xs []int, v int) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

func (p *SimPort) Write(b []byte) (int, error) {
	l := p.link
	if l.Speed > 0 {
		time.Sleep(time.Duration(float64(len(b)*10) / float64(p.baud) / l.Speed * float64(time.Second)))
	}
	out := make([]byte, 0, len(b))
	p.mu.Lock()
	myBaud := p.baud
	p.mu.Unlock()
	p.peer.mu.Lock()
	peerBaud := p.peer.baud
	p.peer.mu.Unlock()
	l.mu.Lock()
	for _, c := range b {
		if p.isHost {
			n := l.sent
			l.sent++
			if contains(l.f.DropByteAt, n) {
				continue
			}
			if contains(l.f.CorruptByteAt, n) {
				c ^= 0x5a
			}
		}
		if myBaud != peerBaud {
			c = c>>1 | 0x80 // framing garbage
		}
		out = append(out, c)
	}
	l.mu.Unlock()
	p.peer.mu.Lock()
	p.peer.rx = append(p.peer.rx, out...)
	p.peer.mu.Unlock()
	select {
	case p.peer.notify <- struct{}{}:
	default:
	}
	return len(b), nil
}

func (p *SimPort) ReadTimeout(b []byte, d time.Duration) (int, error) {
	deadline := time.Now().Add(d)
	for {
		p.mu.Lock()
		if len(p.rx) > 0 {
			n := copy(b, p.rx)
			p.rx = p.rx[n:]
			p.mu.Unlock()
			return n, nil
		}
		p.mu.Unlock()
		left := time.Until(deadline)
		if left <= 0 {
			return 0, nil
		}
		t := time.NewTimer(left)
		select {
		case <-p.notify:
		case <-t.C:
		}
		t.Stop()
	}
}

func (p *SimPort) SetBaud(b int) error {
	p.mu.Lock()
	p.baud = b
	p.mu.Unlock()
	return nil
}

func (p *SimPort) FlushInput() error {
	p.mu.Lock()
	p.rx = nil
	p.mu.Unlock()
	return nil
}

func (p *SimPort) Drain() error { return nil }

// Close is a no-op so SimPort can stand in for a serial port.
func (p *SimPort) Close() error { return nil }
