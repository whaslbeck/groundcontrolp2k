package fupdate

import (
	"context"
	"encoding/binary"
	"fmt"
	"time"
)

// Line test: does the machine's receiver see every byte value unchanged?
//
// The receiver of releases 1.20 to 2.60 switches its serial port to raw
// mode before it receives. A receiver that left the port in the shell's
// line mode would act on special characters: XOFF (0x13) stops the
// machine's transmitter, XON (0x11) is swallowed, CR may become LF, and
// so on. Every update contains such bytes, so the transfer would fail.
//
// The test starts the receiver and sends packets that the machine must
// reject: they carry a block number other than the expected one and a
// wrong checksum. Nothing is written to the FLASH. A NAK proves the
// packet arrived complete; no answer means bytes were lost on the way.

// LineTestCase is one test packet.
type LineTestCase struct {
	Name    string
	Special byte // the byte under test (0 for the control tests)
	Data    []byte
}

// LineTestResult is the machine's answer to one test packet.
type LineTestResult struct {
	LineTestCase
	Raw   []byte        // everything received after the packet
	Reply *Reply        // a 4 byte ACK/NAK, if one was found
	After time.Duration // time from the end of sending to the first byte
	// Rebooted is set when the machine restarted after this packet.
	Rebooted bool
	// Seq is the number of the packet since the receiver was started (1 = first).
	Seq int
	// Retest is set on the repetition of a case that was followed by a
	// restart: it is sent again as the first packet of a fresh receiver,
	// to tell a byte that causes the restart from a packet position.
	Retest bool
}

// OK reports whether the packet arrived complete (the machine answered
// with a NAK for block 1).
func (r LineTestResult) OK() bool {
	return r.Reply != nil && r.Reply.Type == TypeNAK && r.Reply.Block == 1
}

// LineTestCases returns the packets of the test: two control packets of
// plain bytes, then one packet per special character.
func LineTestCases() []LineTestCase {
	fill := func(b byte) []byte {
		d := make([]byte, DataSize)
		for i := range d {
			d[i] = b
		}
		return d
	}
	cases := []LineTestCase{
		{Name: "plain text", Data: fill('A')},
		{Name: "zero bytes", Data: fill(0)},
	}
	for _, c := range []struct {
		b    byte
		name string
	}{
		{0x13, "XOFF (0x13)"}, {0x11, "XON (0x11)"}, {0x0d, "CR (0x0d)"}, {0x0a, "LF (0x0a)"},
		{0x03, "Ctrl-C (0x03)"}, {0x08, "backspace (0x08)"}, {0x7f, "DEL (0x7f)"},
		{0x15, "Ctrl-U (0x15)"}, {0x04, "Ctrl-D (0x04)"}, {0x1a, "Ctrl-Z (0x1a)"},
	} {
		d := fill('A')
		d[100] = c.b
		cases = append(cases, LineTestCase{Name: c.name, Special: c.b, Data: d})
	}
	return cases
}

// encodeRejected builds a packet the receiver must NAK: block 1 while it
// expects 0, and a checksum that is off by one.
func encodeRejected(data []byte) []byte {
	b := Packet{Block: 1, Data: data}.Encode()
	sum := binary.LittleEndian.Uint16(b[4:]) + 1
	binary.LittleEndian.PutUint16(b[4:], sum)
	return b
}

// LineTestOptions control LineTest.
type LineTestOptions struct {
	Baud        int           // transfer baud rate (default 9600)
	Wait        time.Duration // wait for an answer after a packet (default 2.5s)
	RecoverWait time.Duration // pause after a lost packet (default 6s, longer than the receiver's input timeout)
	// MaxLost stops the test after this many unanswered packets, before
	// the receiver gives up by itself (default 4; the receiver allows 5).
	MaxLost int
	// BootWait is the longest wait for the shell after a restart (default 120s).
	BootWait time.Duration
	Log      func(format string, args ...any)
	OnCase   func(LineTestResult)
}

// LineTest starts the receiver and sends the test packets. It never
// sends a packet the machine could accept. At the end the receiver is
// left waiting and gives up by itself after about half a minute.
func LineTest(ctx context.Context, p Port, cases []LineTestCase, opt LineTestOptions) ([]LineTestResult, error) {
	if opt.Baud == 0 {
		opt.Baud = 9600
	}
	if opt.Wait == 0 {
		opt.Wait = 2500 * time.Millisecond
	}
	if opt.RecoverWait == 0 {
		opt.RecoverWait = 6 * time.Second
	}
	if opt.MaxLost == 0 {
		opt.MaxLost = 4
	}
	if opt.BootWait == 0 {
		opt.BootWait = 120 * time.Second
	}
	o := Options{Baud: opt.Baud, Log: opt.Log}
	o.defaults()
	s := &sender{port: p, opt: &o, plan: &Plan{Mode: ModeGame}}
	if err := s.startReceiver(ctx); err != nil {
		return nil, err
	}
	var out []LineTestResult
	lost := 0
	buf := make([]byte, 64)
	seq := 0
	retest := false
	for ci := 0; ci < len(cases); ci++ {
		c := cases[ci]
		if err := ctx.Err(); err != nil {
			return out, err
		}
		if lost >= opt.MaxLost {
			o.Log("stopping: %d packets without answer", lost)
			break
		}
		p.FlushInput()
		if _, err := p.Write(encodeRejected(c.Data)); err != nil {
			return out, err
		}
		p.Drain()
		sent := time.Now()
		seq++
		r := LineTestResult{LineTestCase: c, Seq: seq, Retest: retest}
		retest = false
		// The receiver allows only about five input timeouts in total, and
		// every lost packet costs one. So go on as soon as a reply is
		// complete instead of waiting the full time.
		end := sent.Add(opt.Wait)
		for time.Now().Before(end) {
			n, err := p.ReadTimeout(buf, 50*time.Millisecond)
			if err != nil {
				return out, err
			}
			if n > 0 {
				if len(r.Raw) == 0 {
					r.After = time.Since(sent)
				}
				r.Raw = append(r.Raw, buf[:n]...)
				if r.Reply = findReply(r.Raw); r.Reply != nil {
					// collect anything that follows right away
					extra, _ := p.ReadTimeout(buf, 100*time.Millisecond)
					r.Raw = append(r.Raw, buf[:extra]...)
					break
				}
				end = time.Now().Add(300 * time.Millisecond)
			}
		}
		if !r.OK() {
			// Let the receiver discard the partial packet: wait longer than
			// its input timeout. XON first, in case an XOFF stopped the
			// machine's transmitter. Meanwhile watch for a restart, whose
			// messages begin within a few seconds.
			p.Write([]byte{0x11})
			wait := time.Now().Add(opt.RecoverWait)
			for time.Now().Before(wait) && !r.Rebooted {
				r.Raw = append(r.Raw, readFor(ctx, p, 250*time.Millisecond)...)
				r.Rebooted = LooksLikeReboot(r.Raw)
				if ctx.Err() != nil {
					return out, ctx.Err()
				}
			}
		}
		out = append(out, r)
		if opt.OnCase != nil {
			opt.OnCase(r)
		}
		if r.Rebooted {
			// The receiver is gone. Wait for the shell and start it again,
			// so that every case is tested on a fresh receiver.
			o.Log("the machine restarted; waiting for the shell ...")
			if err := waitPrompt(ctx, p, opt.BootWait); err != nil {
				return out, fmt.Errorf("the shell did not come back after the restart: %w", err)
			}
			readFor(ctx, p, 3*time.Second) // let the start-up messages finish
			p.FlushInput()
			if err := s.startReceiver(ctx); err != nil {
				return out, err
			}
			lost, seq = 0, 0
			if !r.Retest {
				ci-- // the same case again, now as the first packet
				retest = true
			}
			continue
		}
		if !r.OK() {
			lost++
		}
	}
	return out, nil
}

// findReply returns the first ACK/NAK reply in b.
func findReply(b []byte) *Reply {
	for i := 0; i+ReplySize <= len(b); i++ {
		if t := b[i]; t == TypeACK || t == TypeNAK {
			r := DecodeReply(b[i:])
			return &r
		}
	}
	return nil
}

// Describe explains a result in one line.
func (r LineTestResult) Describe() string {
	switch {
	case r.OK():
		return fmt.Sprintf("ok (NAK after %s)", r.After.Round(time.Millisecond))
	case r.Rebooted && r.Reply == nil:
		return "no answer, the MACHINE RESTARTED"
	case r.Rebooted:
		return fmt.Sprintf("reply type %d block %d, then the MACHINE RESTARTED", r.Reply.Type, r.Reply.Block)
	case r.Reply != nil:
		return fmt.Sprintf("unexpected reply type %d block %d, raw % x", r.Reply.Type, r.Reply.Block, r.Raw)
	case len(r.Raw) > 0:
		return fmt.Sprintf("no valid reply, received % x", r.Raw)
	}
	return "no answer: bytes were lost or the machine stopped sending"
}

// RestartCause tells from the results whether restarts were caused by
// particular byte values or by the position of a packet after the start
// of the receiver. byPosition holds the packet numbers at which restarts
// happened although the byte passed as a first packet; byByte lists the
// cases that restarted the machine even as a first packet.
func RestartCause(rs []LineTestResult) (byByte []string, byPosition []int) {
	for i, r := range rs {
		if !r.Rebooted || r.Retest {
			continue
		}
		if i+1 < len(rs) && rs[i+1].Retest && rs[i+1].Name == r.Name {
			if rs[i+1].Rebooted {
				byByte = append(byByte, r.Name)
			} else {
				byPosition = append(byPosition, r.Seq)
			}
		}
	}
	return
}
