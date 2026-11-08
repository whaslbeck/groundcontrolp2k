package serial

import (
	"errors"
	"net"
	"os"
	"time"
)

// TCPPort is a raw TCP connection used like a serial line, e.g. a
// network serial server (ser2net, ESP-Link) or an emulator's COM1.
// Baud rate changes are ignored: the remote end owns the real UART.
type TCPPort struct {
	conn net.Conn
}

// OpenTCP connects to host:port.
func OpenTCP(addr string) (*TCPPort, error) {
	c, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return nil, err
	}
	if tc, ok := c.(*net.TCPConn); ok {
		tc.SetNoDelay(true)
	}
	return &TCPPort{conn: c}, nil
}

func (p *TCPPort) Write(b []byte) (int, error) {
	p.conn.SetWriteDeadline(time.Time{})
	return p.conn.Write(b)
}

// ReadTimeout returns as soon as data is available or after d.
func (p *TCPPort) ReadTimeout(b []byte, d time.Duration) (int, error) {
	p.conn.SetReadDeadline(time.Now().Add(d))
	n, err := p.conn.Read(b)
	if errors.Is(err, os.ErrDeadlineExceeded) {
		return n, nil
	}
	return n, err
}

// SetBaud is a no-op for TCP.
func (p *TCPPort) SetBaud(int) error { return nil }

// FlushInput discards data that has already arrived.
func (p *TCPPort) FlushInput() error {
	buf := make([]byte, 4096)
	for {
		p.conn.SetReadDeadline(time.Now().Add(time.Millisecond))
		n, err := p.conn.Read(buf)
		if n == 0 || err != nil {
			return nil
		}
	}
}

// Drain is a no-op: TCP writes are handed to the kernel immediately.
func (p *TCPPort) Drain() error { return nil }

// Close closes the connection.
func (p *TCPPort) Close() error { return p.conn.Close() }
