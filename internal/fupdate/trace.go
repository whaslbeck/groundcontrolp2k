package fupdate

import (
	"fmt"
	"io"
	"sync"
	"time"
)

// TracePort wraps a Port and logs every byte that goes over the line, for
// diagnosing a machine that behaves differently than expected.
type TracePort struct {
	Port
	mu    sync.Mutex
	w     io.Writer
	start time.Time
	baud  int
}

// NewTracePort logs the traffic of p to w.
func NewTracePort(p Port, w io.Writer) *TracePort {
	t := &TracePort{Port: p, w: w, start: time.Now(), baud: ShellBaud}
	fmt.Fprintf(w, "# GroundControl serial trace, started %s\n", t.start.Format(time.RFC3339))
	fmt.Fprintf(w, "# time[s] dir  bytes (hex) | text\n")
	return t
}

func (t *TracePort) line(dir string, b []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	el := time.Since(t.start).Seconds()
	// data packets are long: log their header and length only
	if dir == "TX" && len(b) == PacketSize {
		fmt.Fprintf(t.w, "%9.3f TX   packet block=%d len=%d sum=0x%04x (518 bytes)\n", el,
			uint16(b[0])|uint16(b[1])<<8, uint16(b[2])|uint16(b[3])<<8, uint16(b[4])|uint16(b[5])<<8)
		return
	}
	for len(b) > 0 {
		n := min(len(b), 16)
		var hex, txt []byte
		for _, c := range b[:n] {
			hex = fmt.Appendf(hex, "%02x ", c)
			if c >= 32 && c < 127 {
				txt = append(txt, c)
			} else {
				txt = append(txt, '.')
			}
		}
		fmt.Fprintf(t.w, "%9.3f %-4s %-48s| %s\n", el, dir, hex, txt)
		b = b[n:]
	}
}

func (t *TracePort) Write(b []byte) (int, error) {
	t.line("TX", b)
	return t.Port.Write(b)
}

func (t *TracePort) ReadTimeout(b []byte, d time.Duration) (int, error) {
	n, err := t.Port.ReadTimeout(b, d)
	if n > 0 {
		t.line("RX", b[:n])
	}
	if err != nil {
		t.note("read error: %v", err)
	}
	return n, err
}

func (t *TracePort) SetBaud(baud int) error {
	if baud != t.baud {
		t.note("baud %d -> %d", t.baud, baud)
		t.baud = baud
	}
	return t.Port.SetBaud(baud)
}

func (t *TracePort) FlushInput() error {
	t.note("flush input")
	return t.Port.FlushInput()
}

func (t *TracePort) note(f string, a ...any) {
	t.mu.Lock()
	defer t.mu.Unlock()
	fmt.Fprintf(t.w, "%9.3f ---- %s\n", time.Since(t.start).Seconds(), fmt.Sprintf(f, a...))
}
