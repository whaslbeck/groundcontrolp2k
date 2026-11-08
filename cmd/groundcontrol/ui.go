package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/whaslbeck/groundcontrolp2k/internal/fupdate"
	"github.com/whaslbeck/groundcontrolp2k/internal/romset"
	"github.com/whaslbeck/groundcontrolp2k/internal/serial"
)

// SimPortName selects the built-in machine emulator instead of a real port.
const SimPortName = "sim"

// conn is an open connection to a machine (real or emulated).
type conn struct {
	port  fupdate.Port
	close func()
	sim   *fupdate.Machine
}

// openConn opens a serial port, or starts the emulator for "sim".
func openConn(name string, cfg serial.Config, simBoot []byte, simSpeed float64) (*conn, error) {
	if strings.EqualFold(name, SimPortName) {
		link := fupdate.NewSimLink(fupdate.LinkFaults{})
		link.Speed = simSpeed
		m := fupdate.NewMachine(fupdate.MachineConfig{ROMBoot: simBoot})
		ctx, cancel := context.WithCancel(context.Background())
		go m.Run(ctx, link.Machine)
		return &conn{port: link.Host, close: cancel, sim: m}, nil
	}
	if addr, ok := strings.CutPrefix(name, "tcp:"); ok {
		t, err := serial.OpenTCP(addr)
		if err != nil {
			return nil, err
		}
		return &conn{port: t, close: func() { t.Close() }}, nil
	}
	p, err := serial.Open(name, cfg)
	if err != nil {
		return nil, err
	}
	return &conn{port: p, close: func() { p.Close() }}, nil
}

// printer serialises output of the progress line and log messages.
type printer struct {
	mu       sync.Mutex
	w        io.Writer
	progress bool // a progress line is currently shown
	lastLen  int
	lastAt   time.Time
}

func (p *printer) clearLine() {
	if p.progress {
		fmt.Fprintf(p.w, "\r%s\r", strings.Repeat(" ", p.lastLen))
		p.progress = false
	}
}

func (p *printer) Logf(format string, a ...any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.clearLine()
	fmt.Fprintf(p.w, "  "+format+"\n", a...)
}

func (p *printer) Progress(pr fupdate.Progress) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if time.Since(p.lastAt) < 150*time.Millisecond && pr.Packet < pr.Packets {
		return
	}
	p.lastAt = time.Now()
	frac := 0.0
	if pr.Total > 0 {
		frac = float64(pr.Bytes) / float64(pr.Total)
	}
	const width = 28
	n := int(frac * width)
	bar := strings.Repeat("#", n) + strings.Repeat("-", width-n)
	rate := 0.0
	if pr.Elapsed > 0 {
		rate = float64(pr.Bytes) / pr.Elapsed.Seconds() / 1024
	}
	line := fmt.Sprintf("  %-12s [%s] %5.1f%%  %s/%s  %4.1f KB/s  ETA %s",
		pr.Section, bar, frac*100, mb(pr.Bytes), mb(pr.Total), rate, clock(pr.Remaining))
	if pr.Retries > 0 {
		line += fmt.Sprintf("  retries %d", pr.Retries)
	}
	pad := ""
	if len(line) < p.lastLen {
		pad = strings.Repeat(" ", p.lastLen-len(line))
	}
	fmt.Fprintf(p.w, "\r%s%s", line, pad)
	p.lastLen = len(line)
	p.progress = true
}

func (p *printer) Done() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.progress {
		fmt.Fprintln(p.w)
		p.progress = false
	}
}

func mb(n int) string { return fmt.Sprintf("%.2f MB", float64(n)/(1<<20)) }

func clock(d time.Duration) string {
	d = d.Round(time.Second)
	if d >= time.Hour {
		return fmt.Sprintf("%d:%02d:%02d", int(d.Hours()), int(d.Minutes())%60, int(d.Seconds())%60)
	}
	return fmt.Sprintf("%d:%02d", int(d.Minutes()), int(d.Seconds())%60)
}

// buildDate turns "Thu Aug 08 12:44:50 2024" into "2024-08-08".
func buildDate(ts string) string {
	for _, layout := range []string{"Mon Jan 02 15:04:05 2006", "Mon Jan 2 15:04:05 2006"} {
		if t, err := time.Parse(layout, ts); err == nil {
			return t.Format("2006-01-02")
		}
	}
	return ts
}

func shortSource(s *romset.Set) string { return filepath.Base(s.Source) }

// setRow is a compact table row for a set.
func setRow(i int, s *romset.Set) string {
	game := "ok"
	if err := s.GameUpdateReady(); err != nil {
		game = "INCOMPLETE"
	}
	sound := "yes"
	if s.SoundUpdateReady() != nil {
		sound = "-"
	}
	date, sys := "?", "?"
	if s.Boot != nil {
		date, sys = buildDate(s.Boot.Timestamp), s.Boot.SystemVersion()
	}
	note := ""
	for _, p := range s.Parts {
		if p != nil && p.Note != "" && strings.HasPrefix(p.Note, "taken") {
			note = " *"
		}
	}
	return fmt.Sprintf("%3d  %-20s %-7s %-10s %-6s %-11s %-5s  %s%s",
		i+1, s.GameName(), s.Version(), date, sys, game, sound, shortSource(s), note)
}

const setHeader = "  #  Game                 Version Built      XINA   Game image  Sound  Source"

func printSets(w io.Writer, sets []*romset.Set) {
	fmt.Fprintln(w, setHeader)
	star := false
	for i, s := range sets {
		r := setRow(i, s)
		star = star || strings.HasSuffix(r, " *")
		fmt.Fprintln(w, r)
	}
	if star {
		fmt.Fprintln(w, "  * a missing file was completed from another update with identical size and checksum")
	}
}

func printSetDetails(w io.Writer, s *romset.Set) {
	fmt.Fprintf(w, "%s %s\n", s.GameName(), s.Version())
	fmt.Fprintf(w, "  source:          %s\n", s.Source)
	if bd := s.Boot; bd != nil {
		fmt.Fprintf(w, "  game id:         %d\n", bd.GameID)
		fmt.Fprintf(w, "  game version:    %s (as shown by the machine)\n", bd.GameVersion())
		fmt.Fprintf(w, "  system version:  %s\n", bd.SystemVersion())
		if x := s.XinaVersion(); x != "" {
			fmt.Fprintf(w, "  XINA build:      %s\n", x)
		}
		fmt.Fprintf(w, "  built:           %s\n", bd.Timestamp)
	}
	fmt.Fprintln(w, "  files:")
	for _, c := range s.Checks() {
		p := s.Parts[c.Kind]
		name, size := "", ""
		if p != nil {
			name = p.Name
			size = fmt.Sprintf("%9d", len(p.Data))
		}
		role := ""
		switch c.Kind {
		case romset.PartPubBoot:
			role = "(PUB card only, not sent)"
		case romset.PartSoundFlash:
			role = "(sound FLASH)"
		}
		fmt.Fprintf(w, "    %-9s %-8s %9s  %-34s %s %s\n", c.Kind, c.Status, size, name, c.Detail, role)
		if p != nil && p.Note != "" {
			fmt.Fprintf(w, "              note: %s\n", p.Note)
		}
	}
	if err := s.GameUpdateReady(); err != nil {
		fmt.Fprintf(w, "  game update:     NOT possible: %v\n", err)
	} else {
		fmt.Fprintln(w, "  game update:     possible")
	}
	if err := s.SoundUpdateReady(); err != nil {
		fmt.Fprintf(w, "  sound update:    not possible: %v\n", err)
	} else {
		fmt.Fprintln(w, "  sound update:    possible")
	}
}

func printMachine(w io.Writer, mi *fupdate.MachineInfo) {
	fmt.Fprintf(w, "  running from:    %s\n", mi.Location)
	fmt.Fprintf(w, "  game:            %s (id %d)\n", romset.GameName(mi.GameID, nil), mi.GameID)
	fmt.Fprintf(w, "  game version:    %s\n", mi.GameVersion)
	fmt.Fprintf(w, "  system version:  %s\n", mi.System)
	fmt.Fprintf(w, "  built:           %s\n", mi.Timestamp)
	if mi.BootState != "" && mi.Location == "PRISM FLASH" {
		fmt.Fprintf(w, "  update state:    %s\n", map[string]string{"0": "disabled", "1": "enabled"}[mi.BootState])
	}
	for _, v := range mi.ValidityText {
		fmt.Fprintf(w, "  %s\n", v)
	}
}

// compare describes how an update relates to what the machine runs.
func compare(s *romset.Set, mi *fupdate.MachineInfo) (warn string, fatal bool) {
	if s.Boot == nil || mi == nil || mi.GameID == 0 {
		return "", false
	}
	if mi.GameID != s.Boot.GameID {
		return fmt.Sprintf("the machine runs %s (id %d) but the update is for %s (id %d)",
			romset.GameName(mi.GameID, nil), mi.GameID, s.GameName(), s.Boot.GameID), true
	}
	var mm, mn int
	fmt.Sscanf(mi.GameVersion, "%d.%d", &mm, &mn)
	um, un := int(s.Boot.GameMajor), int(s.Boot.GameMinor)
	switch {
	case mm == um && mn == un:
		return "the machine already runs this game version (" + mi.GameVersion + ")", false
	case mm > um || (mm == um && mn > un):
		return "this is a DOWNGRADE (machine: " + mi.GameVersion + ", update: " + s.Boot.GameVersion() + ")", false
	}
	return "", false
}

func stdinIsTerminal() bool {
	st, err := os.Stdin.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}
