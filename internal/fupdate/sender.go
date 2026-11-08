package fupdate

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Port is the serial line as seen by the protocol code.
type Port interface {
	Write(p []byte) (int, error)
	// ReadTimeout returns as soon as at least one byte is available, or
	// (0, nil) when nothing arrived within d.
	ReadTimeout(p []byte, d time.Duration) (int, error)
	SetBaud(baud int) error
	FlushInput() error
	Drain() error // wait until all written data has left the UART
}

// Options control a transfer.
type Options struct {
	Baud        int  // transfer baud rate (default 115200)
	SkipCommand bool // the machine already waits (fupdate load typed manually)
	// NoWarmup disables the warm-up packet. Normally one packet that the
	// machine must reject is sent before the first real one: on 0.90 the
	// first packet after the start arrived garbled when it contained an
	// XOFF byte (0x13), while later packets were fine.
	NoWarmup bool
	// StartAttempts is how often a start that fails before the first
	// packet is accepted is repeated (default 5). Such a failure writes
	// nothing; the machine restarts and is ready again after ~30 s.
	StartAttempts int
	BootWait      time.Duration // longest wait for the shell after a restart (default 120s)
	// The machine allows about 3 seconds per packet: before each packet
	// the receiver resets the scheduler hang detector, and while it waits
	// it does not yield the processor. If no packet is accepted in that
	// time, the machine stops with "scheduler is hung" and restarts. So
	// all waiting and retrying for one packet must fit into that window.
	ReplyTimeout time.Duration // wait for an ACK/NAK (default: packet time + 1s)
	FirstTimeout time.Duration // wait for the first reply (default: packet time + 1.5s)
	MaxRetries   int           // per packet (default 10)
	// ResyncDelay, if set, pauses after every second consecutive failure
	// so that a machine with a short input timeout discards a partial
	// packet. Real machines restart before their input timeout, so it is
	// off by default.
	ResyncDelay time.Duration
	StartDelay  time.Duration // after the shell command, before the first packet (default 0.5s)
	Progress    func(Progress)
	Log         func(format string, args ...any)
}

func (o *Options) defaults() {
	if o.Baud == 0 {
		o.Baud = 115200
	}
	packet := time.Duration(float64(PacketSize*10) / float64(o.Baud) * float64(time.Second))
	if o.ReplyTimeout == 0 {
		o.ReplyTimeout = packet + time.Second
	}
	if o.FirstTimeout == 0 {
		o.FirstTimeout = packet + 1500*time.Millisecond
	}
	if o.MaxRetries == 0 {
		o.MaxRetries = 10
	}
	if o.StartAttempts == 0 {
		o.StartAttempts = 5
	}
	if o.BootWait == 0 {
		o.BootWait = 120 * time.Second
	}
	if o.StartDelay == 0 {
		o.StartDelay = 500 * time.Millisecond
	}
	if o.Log == nil {
		o.Log = func(string, ...any) {}
	}
	if o.Progress == nil {
		o.Progress = func(Progress) {}
	}
}

// Progress is reported after every acknowledged packet.
type Progress struct {
	Section   string
	Packet    int // packets done
	Packets   int // total packets
	Bytes     int // payload bytes done
	Total     int // payload bytes total
	Retries   int // total retransmissions so far
	Elapsed   time.Duration
	Remaining time.Duration
}

// ErrRefused means the machine shell rejected the fupdate command.
var ErrRefused = errors.New("machine refused the update command")

type outcome int

const (
	gotACK outcome = iota
	gotNAK
	gotNothing // timeout, short or garbage reply
	gotStale   // valid reply for a different block
)

type sender struct {
	port    Port
	opt     *Options
	plan    *Plan
	retries int
	start   time.Time
	lastRaw []byte // bytes of the last reply, valid or not
	// accepted is set once the machine acknowledged a data packet, i.e.
	// once it started writing to its FLASH.
	accepted bool
}

// Send performs a complete update: it starts the receiver through the
// machine's shell (unless SkipCommand), switches to the transfer baud
// rate, sends all packets and finally the EOT packet.
func Send(ctx context.Context, port Port, plan *Plan, opt Options) error {
	opt.defaults()
	if !ValidBaud(opt.Baud) {
		return fmt.Errorf("baud rate %d not supported by the machine", opt.Baud)
	}
	for attempt := 1; ; attempt++ {
		s := &sender{port: port, opt: &opt, plan: plan}
		if !opt.SkipCommand {
			if err := s.startReceiver(ctx); err != nil {
				return err
			}
		} else {
			if err := port.SetBaud(opt.Baud); err != nil {
				return err
			}
			port.FlushInput()
		}
		err := s.transfer(ctx)
		// A start that failed before the machine accepted any packet
		// changed nothing: the machine restarts with its old version.
		// Wait for it and start again.
		if !errors.Is(err, ErrMachineRebooted) || s.accepted || opt.SkipCommand ||
			attempt >= opt.StartAttempts || ctx.Err() != nil {
			return err
		}
		opt.Log("start attempt %d failed before anything was written; waiting for the machine (attempt %d of %d follows)",
			attempt, attempt+1, opt.StartAttempts)
		if err := port.SetBaud(ShellBaud); err != nil {
			return err
		}
		if err := waitPrompt(ctx, port, opt.BootWait); err != nil {
			return fmt.Errorf("the shell did not come back after the restart: %w", err)
		}
		readFor(ctx, port, 3*time.Second) // let the start-up messages finish
		port.FlushInput()
	}
}

func (s *sender) startReceiver(ctx context.Context) error {
	p, o := s.port, s.opt
	if err := p.SetBaud(ShellBaud); err != nil {
		return err
	}
	p.FlushInput()
	if err := s.checkUsage(ctx); err != nil {
		return err
	}
	cmd := ShellCommand(o.Baud, s.plan.Mode)
	o.Log("shell: %s", cmd)
	if _, err := p.Write([]byte(cmd + "\n")); err != nil {
		return err
	}
	p.Drain()
	// Listen to the shell at 9600 baud: the echo, and any error message.
	// If the command is accepted the machine changes its baud rate.
	resp := readFor(ctx, p, o.StartDelay)
	if r := strings.TrimSpace(sanitize(resp)); r != "" {
		o.Log("shell response: %q", r)
	} else {
		o.Log("warning: the shell did not echo the command; trying anyway")
	}
	low := strings.ToLower(resp)
	for _, bad := range []string{"failed", "usage:", "bad baud", "bad port", "not found"} {
		if strings.Contains(low, bad) {
			return fmt.Errorf("%w: %s", ErrRefused, strings.TrimSpace(sanitize(resp)))
		}
	}
	if err := p.SetBaud(o.Baud); err != nil {
		return err
	}
	return p.FlushInput()
}

// ErrMachineRebooted means the machine restarted during the transfer.
var ErrMachineRebooted = errors.New("the machine restarted during the transfer")

// rebootMarkers are texts the machine prints while it starts up.
var rebootMarkers = []string{"bootstrap loader", "Probing the PCI", "VALIDATING UPDATE", "[ START"}

// LooksLikeReboot reports whether b contains start-up messages.
func LooksLikeReboot(b []byte) bool {
	for _, m := range rebootMarkers {
		if strings.Contains(string(b), m) {
			return true
		}
	}
	return false
}

// rebootedForce listens at 9600 baud for start-up messages even when
// nothing unexpected arrived; used before giving up.
func (s *sender) rebootedForce(ctx context.Context) bool {
	if s.port.SetBaud(ShellBaud) != nil {
		return false
	}
	defer s.port.SetBaud(s.opt.Baud)
	return LooksLikeReboot([]byte(readFor(ctx, s.port, 2*time.Second)))
}

// rebooted checks whether the machine restarted. A live receiver answers
// with a valid reply or not at all, so unexpected bytes are the trigger:
// a restart prints start-up messages for many seconds, at 9600 baud.
// The check stays short, because a live receiver counts the time as idle.
func (s *sender) rebooted(ctx context.Context) bool {
	got := append([]byte(nil), s.lastRaw...)
	// short: a live receiver counts this as part of its ~3 s window
	got = append(got, readFor(ctx, s.port, min(300*time.Millisecond, s.opt.ReplyTimeout/4))...)
	if LooksLikeReboot(got) {
		return true
	}
	if len(got) == 0 {
		return false
	}
	if s.opt.Baud != ShellBaud {
		// at the transfer rate the messages arrive garbled
		if s.port.SetBaud(ShellBaud) != nil {
			return false
		}
		defer s.port.SetBaud(s.opt.Baud)
	}
	return LooksLikeReboot([]byte(readFor(ctx, s.port, 1500*time.Millisecond)))
}

// checkUsage asks the shell for the syntax of its fupdate command and
// checks that this firmware supports what the plan needs. Old firmware
// may lack the sound options. Without an answer it lets the update go on.
func (s *sender) checkUsage(ctx context.Context) error {
	p, o := s.port, s.opt
	if _, err := p.Write([]byte("fupdate\n")); err != nil {
		return err
	}
	p.Drain()
	usage := strings.TrimSpace(sanitize(readFor(ctx, p, 1500*time.Millisecond)))
	p.FlushInput()
	if usage == "" {
		o.Log("warning: no answer to \"fupdate\"; trying anyway")
		return nil
	}
	for _, l := range strings.Split(usage, "\n") {
		if l = strings.TrimSpace(l); l != "" && l != "fupdate" && l != "%" {
			o.Log("machine: %s", l)
		}
	}
	low := strings.ToLower(usage)
	switch {
	case strings.Contains(low, "not found"):
		return fmt.Errorf("%w: this firmware has no \"fupdate\" command, so it cannot be updated "+
			"over the serial port", ErrRefused)
	case !strings.Contains(low, "load"):
		o.Log("warning: unexpected answer to \"fupdate\"; trying anyway")
	case s.plan.Mode != ModeGame && !strings.Contains(low, "sf"):
		return fmt.Errorf("%w: this firmware's \"fupdate load\" has no sound option; "+
			"install the game only (-mode game, menu 4), then the sound with the new version", ErrRefused)
	}
	return nil
}

// readFor collects everything received during d.
func readFor(ctx context.Context, p Port, d time.Duration) string {
	var out []byte
	buf := make([]byte, 256)
	end := time.Now().Add(d)
	for {
		left := time.Until(end)
		if left <= 0 || ctx.Err() != nil {
			return string(out)
		}
		n, err := p.ReadTimeout(buf, min(left, 100*time.Millisecond))
		out = append(out, buf[:n]...)
		if err != nil {
			return string(out)
		}
	}
}

func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || (r >= 32 && r < 127) {
			return r
		}
		return -1
	}, strings.ReplaceAll(s, "\r", ""))
}

// exchange sends one packet and waits for its reply.
func (s *sender) exchange(ctx context.Context, p Packet, timeout time.Duration) (outcome, Reply, error) {
	if err := ctx.Err(); err != nil {
		return gotNothing, Reply{}, err
	}
	if _, err := s.port.Write(p.Encode()); err != nil {
		return gotNothing, Reply{}, err
	}
	buf := make([]byte, ReplySize)
	got := 0
	end := time.Now().Add(timeout)
	s.lastRaw = s.lastRaw[:0]
	for got < ReplySize {
		left := time.Until(end)
		if left <= 0 {
			s.lastRaw = append(s.lastRaw, buf[:got]...)
			return gotNothing, Reply{}, nil
		}
		n, err := s.port.ReadTimeout(buf[got:], left)
		if err != nil {
			return gotNothing, Reply{}, err
		}
		if got == 0 && n > 0 && n < ReplySize {
			// the rest of a reply follows within a few byte times; if it
			// does not, waiting longer only costs the machine's time limit
			rest := time.Duration(float64(ReplySize*10)/float64(s.opt.Baud)*float64(time.Second)) + 150*time.Millisecond
			if e := time.Now().Add(rest); e.Before(end) {
				end = e
			}
		}
		got += n
	}
	r := DecodeReply(buf)
	s.lastRaw = append(s.lastRaw, buf...)
	switch {
	case r.Type != TypeACK && r.Type != TypeNAK:
		return gotNothing, r, nil
	case !p.IsMarker() && r.Block != p.Block:
		return gotStale, r, nil
	case r.Type == TypeACK:
		return gotACK, r, nil
	default:
		return gotNAK, r, nil
	}
}

// nextData returns the index of the next sequence-checked packet after i.
func (s *sender) nextData(i int) int {
	if i+1 < len(s.plan.Packets) && !s.plan.Packets[i+1].IsMarker() {
		return i + 1
	}
	return -1
}

// WarmupPacket returns a packet every receiver rejects: it carries block
// number 0xFFFD (never expected, not a marker), a wrong checksum and only
// plain bytes.
func WarmupPacket() []byte {
	d := make([]byte, DataSize)
	for i := range d {
		d[i] = 'W'
	}
	b := Packet{Block: 0xFFFD, Data: d}.Encode()
	binary.LittleEndian.PutUint16(b[4:], binary.LittleEndian.Uint16(b[4:])+1)
	return b
}

// warmup sends the warm-up packet and waits for its NAK.
func (s *sender) warmup(ctx context.Context) {
	o := s.opt
	if _, err := s.port.Write(WarmupPacket()); err != nil {
		return
	}
	buf := make([]byte, ReplySize)
	got := 0
	end := time.Now().Add(o.FirstTimeout)
	for got < ReplySize && time.Now().Before(end) && ctx.Err() == nil {
		n, err := s.port.ReadTimeout(buf[got:], time.Until(end))
		if err != nil {
			return
		}
		got += n
	}
	if got == ReplySize && buf[0] == TypeNAK {
		o.Log("warm-up packet rejected as expected")
	} else {
		o.Log("warm-up packet: unexpected answer % x", buf[:got])
	}
	s.port.FlushInput()
}

func (s *sender) transfer(ctx context.Context) error {
	o, pk := s.opt, s.plan.Packets
	if !o.NoWarmup {
		s.warmup(ctx)
	}
	s.start = time.Now()
	done := 0
	section := func(i int) string {
		for _, sec := range s.plan.Sections {
			if i >= sec.First && i < sec.First+sec.Count {
				return sec.Name
			}
		}
		return "sound start"
	}
	report := func(i int) {
		el := time.Since(s.start)
		var rem time.Duration
		if done > 0 {
			rem = time.Duration(float64(el) / float64(done) * float64(s.plan.Bytes-done))
		}
		o.Progress(Progress{Section: section(i), Packet: i + 1, Packets: len(pk), Bytes: done,
			Total: s.plan.Bytes, Retries: s.retries, Elapsed: el, Remaining: rem})
	}
	advance := func(i int) { done += len(pk[i].Data); report(i) }

	i := 0
	fails := 0       // consecutive failures for packet i
	suspect := false // packet i may already have been accepted (reply lost)
	nakAfterSuspect := 0
	for i < len(pk) {
		if fails >= o.MaxRetries {
			if s.rebootedForce(ctx) {
				return fmt.Errorf("%w after packet %d (%s)", ErrMachineRebooted, i, section(i))
			}
			return fmt.Errorf("packet %d (%s): no acknowledgement after %d attempts", i, section(i), fails)
		}
		if o.ResyncDelay > 0 && fails > 0 && fails%2 == 0 {
			o.Log("block %d: resynchronising (pause %v)", pk[i].Block, o.ResyncDelay)
			if err := sleepCtx(ctx, o.ResyncDelay); err != nil {
				return err
			}
			s.port.FlushInput()
		}
		timeout := o.ReplyTimeout
		if i == 0 && fails == 0 {
			timeout = o.FirstTimeout // the machine is still setting up its display
		}
		if fails > 0 {
			s.retries++
		}
		res, r, err := s.exchange(ctx, pk[i], timeout)
		if err != nil {
			return err
		}
		switch res {
		case gotACK:
			s.accepted = true
			advance(i)
			i++
			fails, suspect, nakAfterSuspect = 0, false, 0
		case gotNothing, gotStale:
			if res == gotStale {
				o.Log("block %d: reply for block %d, ignored", pk[i].Block, r.Block)
			} else {
				o.Log("block %d: no valid reply", pk[i].Block)
			}
			fails++
			suspect = true
			if fails >= 2 && s.rebooted(ctx) {
				return fmt.Errorf("%w after packet %d (%s)", ErrMachineRebooted, i, section(i))
			}
			s.port.FlushInput()
		case gotNAK:
			if pk[i].IsMarker() || !suspect {
				o.Log("block %d: NAK, resending", pk[i].Block)
				fails++
				continue
			}
			// The machine NAKs a duplicate as well as a damaged packet.
			// Packet i may have arrived while its ACK got lost: probe with
			// the next packet, which the machine only ACKs in that case.
			n := s.nextData(i)
			if n < 0 {
				nakAfterSuspect++
				if nakAfterSuspect >= 3 {
					o.Log("block %d: repeated NAK after lost reply, assuming it was received", pk[i].Block)
					advance(i)
					i++
					fails, suspect, nakAfterSuspect = 0, false, 0
				} else {
					fails++
				}
				continue
			}
			o.Log("block %d: NAK after lost reply, probing with block %d", pk[i].Block, pk[n].Block)
			res2, _, err := s.exchange(ctx, pk[n], o.ReplyTimeout)
			if err != nil {
				return err
			}
			s.retries++
			switch res2 {
			case gotACK:
				s.accepted = true
				advance(i)
				advance(n)
				i = n + 1
				fails, suspect, nakAfterSuspect = 0, false, 0
			case gotNAK:
				suspect = false // the machine still wants packet i
				fails++
			default:
				fails++
				s.port.FlushInput()
			}
		}
	}
	return s.sendEOT(ctx)
}

// sendEOT ends the transfer. The original tool sends it once without
// waiting; we wait for the ACK and repeat a few times if it is lost.
func (s *sender) sendEOT(ctx context.Context) error {
	eot := Packet{Block: BlockEOT}
	for try := 0; try < 3; try++ {
		res, _, err := s.exchange(ctx, eot, s.opt.ReplyTimeout)
		if err != nil {
			return err
		}
		if res == gotACK {
			s.opt.Log("EOT acknowledged")
			return nil
		}
		s.opt.Log("EOT: no acknowledgement (try %d)", try+1)
	}
	// The machine may be busy finishing; this is not necessarily an error.
	s.opt.Log("EOT was not acknowledged; check the machine display")
	return nil
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
