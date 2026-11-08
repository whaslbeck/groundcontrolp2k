package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/whaslbeck/groundcontrolp2k/internal/fupdate"
	"github.com/whaslbeck/groundcontrolp2k/internal/romset"
	"github.com/whaslbeck/groundcontrolp2k/internal/serial"
	"github.com/whaslbeck/groundcontrolp2k/internal/source"
)

var in = bufio.NewReader(os.Stdin)

// prompt reads one line; io errors (EOF) end the program.
func prompt(q string) string {
	fmt.Print(q)
	line, err := in.ReadString('\n')
	if err != nil && line == "" {
		fmt.Println()
		os.Exit(0)
	}
	return strings.TrimSpace(line)
}

func askYesNo(q string) bool {
	a := strings.ToLower(prompt(q + " [y/N] "))
	return a == "y" || a == "yes" || a == "j" || a == "ja"
}

type tui struct {
	paths []string
	res   *source.Result
	set   *romset.Set
	pf    portFlags
	baud  int
	mode  string // auto, game, sound, both
}

func cmdTUI(args []string) error {
	fs := flag.NewFlagSet("groundcontrol", flag.ContinueOnError)
	fs.Usage = usage
	t := &tui{baud: 115200, mode: "auto"}
	t.pf.register(fs)
	fs.IntVar(&t.baud, "baud", 115200, "transfer baud rate")
	paths := searchPaths(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !stdinIsTerminal() {
		usage()
		return errors.New("the interactive menu needs a terminal; use one of the commands above")
	}
	t.paths = paths()
	if t.pf.port == "" {
		if ports := serial.List(); len(ports) == 1 {
			t.pf.port = ports[0]
		}
	}
	fmt.Printf("GroundControl P2K %s - Pinball 2000 firmware updater\n", version)
	t.rescan()
	for {
		t.header()
		fmt.Println("  1) choose update            5) read version from machine")
		fmt.Println("  2) update details           6) START UPDATE")
		fmt.Println("  3) serial port              7) extract ROM files")
		fmt.Println("  4) baud rate / what to send 8) search path / rescan")
		fmt.Println("  9) read firmware back from the machine (slow)")
		fmt.Println(" 10) save service report: audits, adjustments, high scores, errors")
		fmt.Println(" 11) test the graphics ROMs (repeated verify, finds hardware faults)")
		fmt.Println("  q) quit")
		switch strings.ToLower(prompt("> ")) {
		case "1":
			t.chooseSet()
		case "2":
			if t.needSet() {
				fmt.Println()
				printSetDetails(os.Stdout, t.set)
			}
		case "3":
			t.choosePort()
		case "4":
			t.chooseBaudMode()
		case "5":
			t.query()
		case "6":
			t.update()
		case "7":
			t.extract()
		case "8":
			t.changePath()
		case "9":
			t.dump()
		case "10":
			t.report()
		case "11":
			if t.needPort() {
				n := 10
				if a := prompt("number of runs (Enter = 10): "); a != "" {
					fmt.Sscanf(a, "%d", &n)
				}
				if err := runROMTest(romtestParams{pf: t.pf, n: n, which: "rom"}); err != nil {
					fmt.Println("error:", err)
				}
			}
		case "q", "quit", "exit":
			return nil
		}
	}
}

func (t *tui) header() {
	fmt.Println()
	fmt.Println(strings.Repeat("-", 72))
	n := 0
	if t.res != nil {
		n = len(t.res.Sets)
	}
	fmt.Printf("  search path: %s (%d updates)\n", strings.Join(t.paths, ", "), n)
	sel := "(none)"
	if t.set != nil {
		sel = t.set.Label() + "  [" + shortSource(t.set) + "]"
	}
	fmt.Printf("  update:      %s\n", sel)
	port := t.pf.port
	if port == "" {
		port = "(none)"
	}
	fmt.Printf("  port:        %s @ %d baud, send: %s\n", port, t.baud, t.modeText())
	fmt.Println(strings.Repeat("-", 72))
}

func (t *tui) modeText() string {
	if t.mode != "auto" {
		return t.mode
	}
	if t.set != nil {
		if m, err := resolveMode(t.set, "auto"); err == nil {
			return m.String() + " (auto)"
		}
	}
	return "auto"
}

func (t *tui) rescan() {
	fmt.Printf("Scanning %s ...\n", strings.Join(t.paths, ", "))
	t.res = source.Scan(t.paths...)
	for _, w := range t.res.Warnings {
		fmt.Println("  warning:", w)
	}
	t.set = nil
	if len(t.res.Sets) == 0 {
		fmt.Println("  no Pinball 2000 update found; use 8) to change the search path")
		return
	}
	// preselect the newest update
	t.set = t.res.Sets[len(t.res.Sets)-1]
}

func (t *tui) needSet() bool {
	if t.set == nil {
		fmt.Println("Choose an update first (1).")
		return false
	}
	return true
}

func (t *tui) chooseSet() {
	if t.res == nil || len(t.res.Sets) == 0 {
		fmt.Println("No updates found.")
		return
	}
	fmt.Println()
	printSets(os.Stdout, t.res.Sets)
	a := prompt("number (Enter = keep): ")
	if a == "" {
		return
	}
	s, err := pickSet(t.res.Sets, a)
	if err != nil {
		fmt.Println(err)
		return
	}
	t.set = s
	if t.mode != "auto" {
		t.mode = "auto"
		fmt.Println("send mode reset to auto")
	}
}

func (t *tui) choosePort() {
	ports := serial.List()
	fmt.Println()
	for i, p := range ports {
		fmt.Printf("  %d) %s\n", i+1, p)
	}
	fmt.Printf("  s) built-in emulated machine (\"%s\", for testing)\n", SimPortName)
	a := prompt("number or port name (Enter = keep): ")
	switch {
	case a == "":
	case strings.EqualFold(a, "s"):
		t.pf.port = SimPortName
	default:
		if n, err := strconv.Atoi(a); err == nil && n >= 1 && n <= len(ports) {
			t.pf.port = ports[n-1]
		} else {
			t.pf.port = a
		}
	}
}

func (t *tui) chooseBaudMode() {
	fmt.Println()
	for i, b := range fupdate.BaudRates {
		fmt.Printf("  %d) %d\n", i+1, b)
	}
	if a := prompt(fmt.Sprintf("baud rate (Enter = %d): ", t.baud)); a != "" {
		n, err := strconv.Atoi(a)
		switch {
		case err == nil && n >= 1 && n <= len(fupdate.BaudRates):
			t.baud = fupdate.BaudRates[n-1]
		case err == nil && fupdate.ValidBaud(n):
			t.baud = n
		default:
			fmt.Println("invalid baud rate")
		}
	}
	fmt.Println("  a) auto: game and sound if sf.rom is present")
	fmt.Println("  g) game program only")
	fmt.Println("  s) sound only (sf.rom)")
	fmt.Println("  b) both")
	switch strings.ToLower(prompt(fmt.Sprintf("send (Enter = %s): ", t.mode))) {
	case "a":
		t.mode = "auto"
	case "g":
		t.mode = "game"
	case "s":
		t.mode = "sound"
	case "b":
		t.mode = "both"
	}
}

func (t *tui) needPort() bool {
	if t.pf.port == "" {
		fmt.Println("Choose a serial port first (3).")
		return false
	}
	return true
}

func (t *tui) simBoot() []byte {
	if t.res == nil || t.set == nil {
		return nil
	}
	return simBootFor(t.res.Sets, t.set)
}

func (t *tui) query() {
	if !t.needPort() {
		return
	}
	c, err := t.pf.open(t.simBoot())
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	defer c.close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	fmt.Println("Asking the machine (shell on COM1, 9600 baud) ...")
	mi, err := fupdate.QueryMachine(ctx, c.port)
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	printMachine(os.Stdout, mi)
	if t.set != nil {
		if w, _ := compare(t.set, mi); w != "" {
			fmt.Println("note for the selected update:", w)
		}
	}
}

func (t *tui) update() {
	if !t.needSet() || !t.needPort() {
		return
	}
	m, err := resolveMode(t.set, t.mode)
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	fmt.Println()
	err = runUpdate(updateParams{set: t.set, mode: m, baud: t.baud, pf: t.pf, simBoot: t.simBoot(),
		confirm: func(q string) bool { return askYesNo(q) }})
	if err != nil {
		fmt.Println("error:", err)
	}
	time.Sleep(100 * time.Millisecond)
}

func (t *tui) extract() {
	if !t.needSet() {
		return
	}
	def := filepath.Join(".", strings.TrimSuffix(t.set.Prefix, "_"))
	if t.set.Prefix == "" {
		def = "romset"
	}
	dir := prompt(fmt.Sprintf("output directory (Enter = %s): ", def))
	if dir == "" {
		dir = def
	}
	if err := extractSet(t.set, dir); err != nil {
		fmt.Println("error:", err)
	}
}

func (t *tui) dump() {
	if !t.needPort() {
		return
	}
	def := "machine-dump"
	dir := prompt(fmt.Sprintf("output directory (Enter = %s): ", def))
	if dir == "" {
		dir = def
	}
	p := dumpParams{pf: t.pf, out: dir, chunk: 4096, pause: 300 * time.Millisecond, confirm: askYesNo}
	if t.res != nil {
		p.known = t.res.Sets
	}
	if strings.EqualFold(t.pf.port, SimPortName) {
		p.simSet = t.set // the emulator holds the selected update
	}
	if err := runDump(p); err != nil {
		fmt.Println("error:", err)
	}
}

func (t *tui) report() {
	if !t.needPort() {
		return
	}
	fn := prompt("output file (Enter = automatic name): ")
	if err := runReport(reportParams{pf: t.pf, out: fn, simBoot: t.simBoot()}); err != nil {
		fmt.Println("error:", err)
	}
}

func (t *tui) changePath() {
	a := prompt("directories or files, separated by ';' (Enter = rescan): ")
	if a != "" {
		var ps []string
		for _, p := range strings.Split(a, ";") {
			if p = strings.TrimSpace(p); p != "" {
				ps = append(ps, p)
			}
		}
		t.paths = ps
	}
	t.rescan()
}
