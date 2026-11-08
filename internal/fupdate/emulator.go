package fupdate

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/whaslbeck/groundcontrolp2k/internal/romset"
)

// MachineFaults injects receiver-side errors into the emulator.
type MachineFaults struct {
	CorruptPacket []int // corrupt the n-th received packet (0-based)
	DropReplyTo   []int // process the n-th packet but send no reply
	GameRunning   bool  // a game is in progress: fupdate load fails
	// DumpFatalOver makes a "dump" of more than this many bytes stop
	// halfway with a fatal error, as the display watchdog does on a real
	// game when the shell hogs the CPU. 0 = never.
	DumpFatalOver int
	GarbleDump    []int // drop a line from the n-th dump command (0-based)
	// NoSoundOption emulates firmware whose fupdate load has no sf/sfonly.
	NoSoundOption bool
	// LineMode emulates a receiver that leaves the port in line mode:
	// XON/XOFF are swallowed and XOFF stops the machine's transmitter.
	LineMode bool
	// RebootOn makes the receiver restart the machine when this byte
	// arrives (0 = off), printing start-up messages, as observed on 0.90.
	RebootOn byte
	// RebootAtPacket makes the receiver restart the machine on its n-th
	// packet since it was started (0 = off), whatever the packet contains.
	RebootAtPacket int
	// FirstXoffGarble emulates 0.90: the first packet after the start of
	// the receiver arrives garbled when it contains an XOFF byte (0x13).
	FirstXoffGarble bool
	// StrayByteStarts puts one extra byte into the receive buffer at the
	// start of the receiver for this many starts, as observed on 0.90:
	// the framing is then off by one until the machine restarts.
	StrayByteStarts int
	// FlakyMask makes "bootdata ... verify" report bank FlakyMask-1 as bad
	// in every second run, with an error in the upper bits of the sum,
	// as on a machine with a loose graphics ROM (0 = off).
	FlakyMask int
}

// MachineConfig configures the emulated machine.
type MachineConfig struct {
	// ROMBoot is the bootdata of the PRISM mask ROMs, reported when the
	// update FLASH holds no valid, enabled update.
	ROMBoot      []byte
	InputTimeout time.Duration // rcv_packet timeout (machine: ~5s)
	// HangTimeout is the machine's scheduler hang detector: rcv_packet
	// never yields the processor, so when one call takes longer than
	// this, the machine stops with "scheduler is hung" and restarts
	// (machine: 3000 ticks, observed ~3-4 s). Default 3s; < 0 disables.
	HangTimeout time.Duration
	Faults      MachineFaults
	Log         func(format string, args ...any)
	OnUpdate    func(m *Machine) // called after each completed transfer
}

// Machine emulates the serial side of a Pinball 2000: the XINA shell on
// COM1 and the fupdate receiver from the game ROM.
type Machine struct {
	cfg MachineConfig

	mu       sync.Mutex
	flash    []byte // 4 MB update FLASH (bank at 0x12000000)
	sound    []byte // 1 MB DCS sound FLASH
	packets  int    // packets received in total (for fault injection)
	Updates  int    // completed transfers
	LastLoad string // result of the last fupdate load
	dumps    int    // dump commands executed (for fault injection)
	starts   int    // receiver starts (for fault injection)
	verifies int    // bootdata verify runs (for fault injection)
	monitor  bool   // stopped in the fatal error monitor
}

// NewMachine creates an emulated machine with erased FLASH.
func NewMachine(cfg MachineConfig) *Machine {
	if cfg.HangTimeout == 0 {
		cfg.HangTimeout = 3 * time.Second
	}
	if cfg.InputTimeout == 0 {
		cfg.InputTimeout = 5 * time.Second
	}
	if cfg.Log == nil {
		cfg.Log = func(string, ...any) {}
	}
	return &Machine{cfg: cfg, flash: bytes.Repeat([]byte{0xff}, MaxFlash),
		sound: bytes.Repeat([]byte{0xff}, romset.SoundFlashSize)}
}

// Flash returns a copy of the update FLASH contents.
func (m *Machine) Flash() []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]byte(nil), m.flash...)
}

// LoadFlash puts an image into the update FLASH and enables it, as a
// completed update would.
func (m *Machine) LoadFlash(img []byte) {
	m.mu.Lock()
	copy(m.flash, img)
	m.mu.Unlock()
	m.setState(true)
}

// Sound returns a copy of the sound FLASH contents.
func (m *Machine) Sound() []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]byte(nil), m.sound...)
}

// Run serves the shell until ctx is cancelled.
func (m *Machine) Run(ctx context.Context, p Port) error {
	p.SetBaud(ShellBaud)
	var line []byte
	buf := make([]byte, 256)
	m.say(p, "\r\nXINA shell (GroundControl emulator)\r\n% ")
	for ctx.Err() == nil {
		n, err := p.ReadTimeout(buf, 100*time.Millisecond)
		if err != nil {
			return err
		}
		for _, c := range buf[:n] {
			switch {
			case c == '\r' || c == '\n':
				m.say(p, "\r\n")
				if m.monitor {
					m.execMonitor(p, strings.TrimSpace(string(line)))
				} else {
					m.exec(ctx, p, strings.TrimSpace(string(line)))
				}
				line = line[:0]
				if m.monitor {
					m.say(p, "monitor> ")
				} else {
					m.say(p, "% ")
				}
			case c == 8 || c == 127:
				if len(line) > 0 {
					line = line[:len(line)-1]
				}
			case c >= 32 && c < 127:
				line = append(line, c)
				p.Write([]byte{c}) // echo
			}
		}
	}
	return ctx.Err()
}

func (m *Machine) say(p Port, s string) { p.Write([]byte(s)) }

func (m *Machine) exec(ctx context.Context, p Port, line string) {
	f := strings.Fields(line)
	if len(f) == 0 {
		return
	}
	switch f[0] {
	case "bootdata":
		m.cmdBootdata(p, f)
	case "fupdate":
		m.cmdFupdate(ctx, p, f)
	case "dump":
		m.cmdDump(p, f)
	case "printout", "hstd", "switch", "pdb", "game", "time":
		m.cmdReport(p, f)
	case "reboot":
		m.say(p, "rebooting...\r\n")
	default:
		m.say(p, f[0]+": not found\r\n")
	}
}

// execMonitor handles the fatal error monitor.
func (m *Machine) execMonitor(p Port, line string) {
	switch line {
	case "continue", "reboot":
		m.monitor = false
	case "":
	default:
		m.say(p, "monitor commands: <?> <continue> <reboot>\r\n")
	}
}

// readMem returns machine memory: the update FLASH at FlashBase, all
// ones elsewhere.
func (m *Machine) readMem(addr uint32, n int) []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	b := make([]byte, n)
	for i := range b {
		a := uint64(addr) + uint64(i)
		if a >= FlashBase && a < FlashBase+uint64(len(m.flash)) {
			b[i] = m.flash[a-FlashBase]
		} else {
			b[i] = 0xff
		}
	}
	return b
}

// cmdDump mirrors the shell's "dump <address> [<count>]".
func (m *Machine) cmdDump(p Port, f []string) {
	if len(f) != 2 && len(f) != 3 {
		m.say(p, "usage: dump <address> [<count>]\r\n")
		return
	}
	addr, _ := strconv.ParseUint(f[1], 0, 32)
	n := 0x100
	if len(f) == 3 {
		v, _ := strconv.ParseInt(f[2], 0, 32)
		n = int(v)
	}
	idx := m.dumps
	m.dumps++
	data := m.readMem(uint32(addr), max(n, 0))
	fatalAt := -1
	if fo := m.cfg.Faults.DumpFatalOver; fo > 0 && n > fo {
		fatalAt = n / 2
	}
	garble := -1
	if contains(m.cfg.Faults.GarbleDump, idx) {
		garble = n / 32
	}
	var s strings.Builder
	s.WriteString("dump:\r\n")
	for off, line := 0, 0; off < n; off, line = off+16, line+1 {
		if fatalAt >= 0 && off >= fatalAt {
			s.WriteString("*** Fatal: Display Manager(HD): Render pass watchdog has expired\r\n\r\n")
			s.WriteString("monitor commands: <?> <continue> <reboot>\r\n")
			m.say(p, s.String())
			m.monitor = true
			return
		}
		row := data[off:min(off+16, n)]
		if line == garble {
			continue
		}
		fmt.Fprintf(&s, "0x%08x ", uint32(addr)+uint32(off))
		for _, c := range row {
			fmt.Fprintf(&s, " %02x", c)
		}
		s.WriteString("    ")
		for _, c := range row {
			if c < 0x20 || c > 0x7e {
				c = '.'
			}
			s.WriteByte(c)
		}
		s.WriteString("\r\n")
		if s.Len() > 4096 {
			m.say(p, s.String())
			s.Reset()
		}
	}
	m.say(p, s.String())
}

// currentBoot returns the bootdata the machine is running from.
func (m *Machine) currentBoot() (b []byte, where string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	fb := m.flash[:romset.BootDataSize]
	if romset.ValidateBootData(fb) == nil && binary.LittleEndian.Uint32(fb[romset.BootStateOffset:]) == 1 {
		return append([]byte(nil), fb...), "PRISM FLASH"
	}
	return m.cfg.ROMBoot, "PRISM ROM"
}

func (m *Machine) cmdBootdata(p Port, f []string) {
	if len(f) < 2 {
		m.say(p, "usage: bootdata <current | rom | flash> [verify]\r\n")
		return
	}
	var b []byte
	var where string
	switch f[1] {
	case "current":
		b, where = m.currentBoot()
	case "rom":
		b, where = m.cfg.ROMBoot, "PRISM ROM"
	case "flash":
		m.mu.Lock()
		b, where = append([]byte(nil), m.flash[:romset.BootDataSize]...), "PRISM FLASH"
		m.mu.Unlock()
	default:
		m.say(p, "usage: bootdata <current | rom | flash> [verify]\r\n")
		return
	}
	m.say(p, where+": ")
	if b == nil {
		m.say(p, "BootData structure is not valid!\r\n")
		return
	}
	m.say(p, DumpBootData(b))
	if romset.ValidateBootData(b) != nil {
		m.say(p, "BootData structure is not valid!\r\n")
		return
	}
	m.say(p, "  im_sys0 is:       OK\r\n  game code is:     OK\r\n  symbol table is:  OK\r\n")
	if len(f) < 3 || f[2] != "verify" {
		m.say(p, "  im_mask[0-3] are: not verified\r\n")
		return
	}
	// like releases from 1.20 on: one line per bank
	bd, _ := romset.ParseBootData(b)
	flaky := m.cfg.Faults.FlakyMask - 1
	bad := flaky >= 0 && m.verifies%2 == 1
	m.verifies++
	for i := 0; i < MaskBanks; i++ {
		if bad && i == flaky {
			want := bd.ImMask[i].Checksum
			m.say(p, fmt.Sprintf("  im_mask%d: 0x%x != 0x%x\r\n", i, want+(1<<29), want))
			m.say(p, fmt.Sprintf("  im_mask[%d] is:      BAD\r\n", i))
			continue
		}
		m.say(p, fmt.Sprintf("  im_mask[%d] is:      OK\r\n", i))
	}
}

func (m *Machine) cmdFupdate(ctx context.Context, p Port, f []string) {
	usage := "usage: fupdate load <com1 | com2> <baudrate> [sf | sfonly]\r\n" +
		"       fupdate enable\r\n       fupdate disable\r\n"
	if m.cfg.Faults.NoSoundOption {
		usage = "usage: fupdate load <com1 | com2> <baudrate>\r\n"
		if len(f) == 5 {
			m.say(p, usage)
			return
		}
	}
	if len(f) == 2 && (f[1] == "enable" || f[1] == "disable") {
		m.setState(f[1] == "enable")
		m.say(p, f[1]+" OK\r\n")
		return
	}
	if (len(f) != 4 && len(f) != 5) || f[1] != "load" {
		m.say(p, usage)
		return
	}
	if f[2] != "com1" && f[2] != "com2" {
		m.say(p, "port = <com1 | com2>\r\n"+usage)
		return
	}
	baud, _ := strconv.Atoi(f[3])
	if !ValidBaud(baud) {
		m.say(p, "baudrate = <9600 | 19200 | 38400 | 57600 | 115200>\r\n"+usage)
		return
	}
	mode := ModeGame
	if len(f) == 5 {
		switch f[4] {
		case "sf":
			mode = ModeBoth
		case "sfonly":
			mode = ModeSound
		default:
			m.say(p, usage)
			return
		}
	}
	if m.cfg.Faults.GameRunning {
		m.say(p, "load FAILED\r\n")
		return
	}
	// The shell is killed; the port switches to the update baud rate.
	p.SetBaud(baud)
	p.FlushInput()
	err := m.receive(ctx, p, mode)
	p.SetBaud(ShellBaud)
	if errors.Is(err, errReboot) {
		m.LastLoad = "rebooted"
		// the real machine spreads its start-up messages over ~25 seconds
		for _, l := range []string{"Pin2000 Option ROM bootstrap loader - emulator", "Probing the PCI bus...",
			"WMS PRISM PCI device # 8", "[ VALIDATING UPDATE BOOT DATA ]", "Boot data structure:",
			"[ VALIDATING UPDATE SYS IMAGE ]", "[ VALIDATING UPDATE GAME CODE ]", "Game(emulator)"} {
			time.Sleep(600 * time.Millisecond)
			m.say(p, "\n\r"+l)
		}
		m.say(p, "\n\r")
		p.FlushInput()
		return
	}
	if err != nil {
		m.LastLoad = err.Error()
		m.cfg.Log("emulator: fupdate_load failed: %v", err)
		m.say(p, "load FAILED\r\n")
		return
	}
	m.LastLoad = "ok"
	m.Updates++
	m.cfg.Log("emulator: update complete, rebooting")
	if m.cfg.OnUpdate != nil {
		m.cfg.OnUpdate(m)
	}
	m.say(p, "\r\nrebooting...\r\n")
}

func (m *Machine) setState(on bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v := uint32(0)
	if on {
		v = 1
	}
	binary.LittleEndian.PutUint32(m.flash[romset.BootStateOffset:], v)
}

// rx is the receive buffer of the machine's tty.
type rx struct {
	p       Port
	buf     []byte
	line    bool // line mode: XON/XOFF flow control active
	stopped bool // XOFF received, output held
	count   int  // packets begun since the receiver started
}

// errReboot ends the receiver with a simulated restart.
var errReboot = errors.New("machine restarted")

// add appends received bytes, applying flow control in line mode.
func (r *rx) add(b []byte) {
	if !r.line {
		r.buf = append(r.buf, b...)
		return
	}
	for _, c := range b {
		switch c {
		case 0x13:
			r.stopped = true
		case 0x11:
			r.stopped = false
		default:
			r.buf = append(r.buf, c)
		}
	}
}

func (r *rx) reply(b []byte) {
	if !r.stopped {
		r.p.Write(b)
	}
}

// rcvPacket mirrors rcv_packet(): wait (up to the input timeout) until a
// full packet is buffered, validate it, reply ACK or NAK. Returns only
// with an accepted packet; gives up after more than 5 timeouts.
func (m *Machine) rcvPacket(ctx context.Context, r *rx, expect uint16) ([]byte, error) {
	timeouts := 0
	tmp := make([]byte, 2048)
	hung := time.Time{}
	if m.cfg.HangTimeout > 0 {
		hung = time.Now().Add(m.cfg.HangTimeout) // DelayHangDetect() before each call
	}
	for {
		deadline := time.Now().Add(m.cfg.InputTimeout)
		if !hung.IsZero() && hung.Before(deadline) {
			deadline = hung
		}
		for len(r.buf) < PacketSize {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if !hung.IsZero() && !time.Now().Before(hung) {
				m.cfg.Log("emulator: interval_0_25ms: scheduler is hung")
				return nil, errReboot
			}
			left := time.Until(deadline)
			if left <= 0 {
				break
			}
			n, err := r.p.ReadTimeout(tmp, left)
			if err != nil {
				return nil, err
			}
			if rb := m.cfg.Faults.RebootOn; rb != 0 && bytes.IndexByte(tmp[:n], rb) >= 0 {
				return nil, errReboot
			}
			if n > 0 && r.count == 0 {
				r.count = 1 // the first packet of this receiver has begun
			}
			r.add(tmp[:n])
		}
		if len(r.buf) < PacketSize && !hung.IsZero() && !time.Now().Before(hung) {
			m.cfg.Log("emulator: interval_0_25ms: scheduler is hung")
			return nil, errReboot
		}
		if len(r.buf) < PacketSize {
			timeouts++
			if timeouts > 5 {
				return nil, fmt.Errorf("rcv_packet: maximum timeouts exceeded for block %d", expect)
			}
			m.cfg.Log("emulator: input timeout, flushing %d bytes", len(r.buf))
			r.buf = nil
			r.p.FlushInput()
			continue
		}
		pkt := append([]byte(nil), r.buf[:PacketSize]...)
		r.buf = r.buf[PacketSize:]
		if m.cfg.Faults.FirstXoffGarble && r.count <= 1 && bytes.IndexByte(pkt, 0x13) >= 0 {
			pkt[0], pkt[1] = 0xc1, 0x88 // as observed: NAK for block 0x88c1
		}
		if at := m.cfg.Faults.RebootAtPacket; at > 0 && r.count == at {
			return nil, errReboot
		}
		r.count++
		m.mu.Lock()
		idx := m.packets
		m.packets++
		m.mu.Unlock()
		if contains(m.cfg.Faults.CorruptPacket, idx) {
			pkt[100] ^= 0xff
		}
		le := binary.LittleEndian
		block, length, sum := le.Uint16(pkt), le.Uint16(pkt[2:]), le.Uint16(pkt[4:])
		ok := length <= DataSize && (block >= BlockSF || block == expect) &&
			sum == Checksum(pkt[6:6+min(int(length), DataSize)])
		reply := Reply{Type: TypeNAK, Block: block}
		if ok {
			reply.Type = TypeACK
		}
		if !contains(m.cfg.Faults.DropReplyTo, idx) {
			r.reply(reply.Encode())
		}
		if ok {
			return pkt, nil
		}
	}
}

// receive mirrors the packet loop of fupdate_load().
func (m *Machine) receive(ctx context.Context, p Port, mode Mode) error {
	r := &rx{p: p, line: m.cfg.Faults.LineMode}
	if m.starts < m.cfg.Faults.StrayByteStarts {
		r.buf = append(r.buf, 0xb5)
	}
	m.starts++
	var seq uint16
	offset := 0
	inSF := false
	odd := false
	for {
		if offset+DataSize > MaxFlash {
			return fmt.Errorf("update exceeds FLASH size")
		}
		pkt, err := m.rcvPacket(ctx, r, seq)
		if err != nil {
			return fmt.Errorf("rcv_packet() failed at offset 0x%x: %w", offset, err)
		}
		le := binary.LittleEndian
		block, length := le.Uint16(pkt), int(le.Uint16(pkt[2:]))
		if length != 0 {
			if length&3 != 0 {
				if odd {
					return fmt.Errorf("data length not a multiple of 4")
				}
				length = (length + 3) &^ 3
				odd = true
			}
			data := pkt[6 : 6+length]
			m.mu.Lock()
			if inSF {
				if offset+length > len(m.sound) {
					m.mu.Unlock()
					return fmt.Errorf("PRISM DCS2 write failed, offset 0x%x", offset)
				}
				copy(m.sound[offset:], data)
			} else {
				copy(m.flash[offset:], data)
			}
			m.mu.Unlock()
			offset += length
			seq++
			continue
		}
		switch block {
		case BlockEOT:
			if mode != ModeSound {
				m.setState(true) // fupdate_set_state(True)
			}
			return nil
		case BlockSF:
			if mode == ModeGame {
				return fmt.Errorf("unexpected sound flash load")
			}
			inSF, offset = true, 0
		default:
			return fmt.Errorf("zero length packet, block (%d)", block)
		}
	}
}

// DumpBootData renders a bootdata image like the machine's
// dump_boot_data_internal() (lines end with CR LF).
func DumpBootData(b []byte) string {
	bd, err := romset.ParseBootData(b)
	if err != nil {
		return ""
	}
	var s strings.Builder
	w := func(f string, a ...any) { fmt.Fprintf(&s, f+"\r\n", a...) }
	w("Boot data structure:")
	w("  boot_data_timestamp: \"%s\"", bd.Timestamp)
	w("  boot_data_checksum:  0x%x", bd.Checksum)
	w("  boot_data_version:   %d.%d", bd.VersionMajor, bd.VersionMinor)
	w("  boot_data_size:      %d", bd.Size)
	w("  im_sys0_size:        %d", bd.ImSys0Size)
	w("  im_sys0_checksum:    0x%x", bd.ImSys0Checksum)
	w("  system_version:      %s", bd.SystemVersion())
	w("  game_id:             %d", bd.GameID)
	w("  game_version:        %d.%d", bd.GameMajor, bd.GameMinor)
	w("  game_reloc:          0x%x", bd.GameReloc)
	w("  game_code_size:      %d", bd.GameCodeSize)
	w("  game_code_checksum:  0x%x", bd.GameCodeChecksum)
	w("  symbols_size:        %d", bd.SymbolsSize)
	w("  symbols_checksum:    0x%x", bd.SymbolsChecksum)
	for i, mk := range bd.ImMask {
		w("  im_mask%d_size:       %d", i, mk.Size)
		w("  im_mask%d_checksum:   0x%x", i, mk.Checksum)
	}
	w("  magic_cookie:        \"%s\"", bd.MagicCookie)
	w("  boot_state:          %d", bd.BootState)
	return s.String()
}

// cmdReport answers the read-only service commands with plausible,
// fixed data in the machine's formats.
func (m *Machine) cmdReport(p Port, f []string) {
	b, _ := m.currentBoot()
	game, ver := "50070", "?.?"
	if bd, err := romset.ParseBootData(b); err == nil {
		game, ver = strconv.Itoa(int(bd.GameID)), bd.GameVersion()
	}
	head := func(title string) string {
		return title + "\r\n" + strings.Repeat("-", len(title)) + "\r\n\r\n" +
			"Location ID  : \r\nGame #" + game + "  : Revenge From Mars\r\n" +
			"Game Version : " + ver + " - Sun Apr 23 09:01:08 2006\r\n" +
			"XINA Version : 1.21 - Sun Apr 23 09:01:21 2006\r\n" +
			"Current Time : Monday,  7 November 2026,  8:49:12 am\r\n\r\n\r\n"
	}
	audits := head("SYSTEM AUDITS") + "Main Audits\r\n------------\r\n\r\n" +
		"  Total Plays                  1234\r\n  Total Free Plays             56      (4%)\r\n" +
		"  Avg. Ball Time               42.00 seconds\r\n  Total Earnings               $617.00\r\n\r\n"
	adjust := head("SYSTEM ADJUSTMENTS") + "System Adjustments\r\n-------------------\r\n\r\n" +
		"  Balls per Game                 3\r\n  Family Mode                    No\r\n" +
		"  Tilt Warnings                  1\r\n\r\nFeature Adjustments\r\n--------------------\r\n\r\n" +
		"  Ball Saver                     Off\r\n  Martian Attack Timer           30\r\n\r\n"
	hstd := "\r\nHigh Scores\r\n-----------\r\n\r\n  GAG          325,000,000\r\n  LFS          275,000,000\r\n" +
		"  XAQ          250,000,000\r\n  KEF          225,000,000\r\n  GIW          200,000,000\r\n"
	switch f[0] {
	case "printout":
		if len(f) < 2 {
			m.say(p, "printout <audits | adjustments | hstd | hourly | daily |\r\n          pricing | fatal | nonfatal | everything>\r\n")
			return
		}
		switch f[1] {
		case "audits":
			m.say(p, audits)
		case "adjustments":
			m.say(p, adjust)
		case "hstd":
			m.say(p, hstd)
		case "hourly", "daily":
			m.say(p, head(strings.Title(f[1])+" Earnings")+"             Mon  7   Sun  6\r\n\r\n  8:00 am     $0.00\r\n")
		case "pricing":
			m.say(p, "\r\nGame Pricing\r\n------------\r\n\r\n   Amount           Credits\r\n       $0.50          1 Credit\r\n")
		case "fatal":
			m.say(p, head("Fatal Errors")+"\r\n")
		case "nonfatal":
			m.say(p, head("Non-Fatal Errors")+"  *** NonFatal: 7 Nov 2026 08:03:18\r\n"+
				"  *** NonFatal: POWER DRIVER BOARD NOT DETECTED\r\n\r\n")
		case "everything":
			m.say(p, audits+adjust+hstd)
		default:
			m.say(p, "printout: unknown report\r\n")
		}
	case "hstd":
		m.say(p, "High Score Tables Dump\r\n\r\nHighest Scores - Priority: 9000\r\n\r\nTable Dump:\r\n"+
			"GAG -- 325,000,000\r\nLFS -- 275,000,000\r\n\r\n")
	case "switch":
		if len(f) == 2 && f[1] == "counters" {
			m.say(p, "Switch Maintenance Counters:\r\n00:  060 060 060 000 060 060 060 060\r\n"+
				"08:  000 000 060 000 060 060 060 060\r\nNo switches are broken.\r\n")
			return
		}
		m.say(p, "usage: switch <debug | callbacks | timers | counters>\r\n")
	case "pdb":
		m.say(p, "Power Driver Board is present version 0x87 (0x80)\r\nPDB: all fuses are good\r\n")
	case "game":
		switch {
		case len(f) == 2 && f[1] == "name":
			m.say(p, "Game(Bally - Revenge From Mars - "+game+" - "+ver+"): Sun Apr 23 09:01:08 2006\r\n")
		case len(f) == 2 && f[1] == "info":
			over := "True"
			if m.cfg.Faults.GameRunning {
				over = "False"
			}
			m.say(p, "GAME: m_tilt False m_game_over "+over+" m_bonus_count False\r\n")
		case len(f) == 2 && f[1] == "over":
			m.cfg.Faults.GameRunning = false
		default:
			m.say(p, "usage: game <info | tilt | over | name | collect | start>\r\n")
		}
	case "time":
		m.say(p, "\r\nIt is Monday,  7 November 2026,  8:47:38 am\r\n")
	}
}
