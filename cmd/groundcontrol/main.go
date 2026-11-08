// Command groundcontrol reads Pinball 2000 firmware updates in all known
// distribution formats and installs them over RS-232, replacing the
// Williams "Pinball 2000 Update Manager".
package main

import (
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

const version = "1.0.0"

const usageText = `GroundControl P2K %s - Pinball 2000 firmware updater

usage:
  groundcontrol [path...]                     interactive menu (TUI)
  groundcontrol list    [path...]             list the updates found
  groundcontrol info    [-set N] [path...]    show details and checks of an update
  groundcontrol extract [-set N] -o DIR [path...]
                                              write the ROM files of an update
  groundcontrol ports                         list serial ports
  groundcontrol query   -port P               read the installed version from the machine
  groundcontrol flash   -port P [-set N] [-baud B] [-mode M] [-yes] [path...]
                                              install an update
  groundcontrol dump    -port P -o DIR [-part LIST | -addr A -len N] [path...]
                                              read the installed game image, single files
                                              of it or raw memory back from the machine
                                              (slow: about 1.5 h per MB)
  groundcontrol report  -port P [-o FILE]     save the machine's service report: audits,
                                              adjustments, high scores, earnings, errors
  groundcontrol linetest -port P [-baud B]    check that the update receiver gets every
                                              byte value unchanged (writes nothing)
  groundcontrol romtest -port P [-n 10]       check the graphics ROMs repeatedly for
                                              unstable reads (hardware faults)
  groundcontrol shell   -port P "COMMAND"     run one command in the machine's shell
  groundcontrol emulate -port P [-rom FILE]   act as a Pinball 2000 (for testing)
  groundcontrol version                       print the program version

Updates are searched in the current directory. Use -dir PATH (repeatable) or
put paths at the end of the command line to search elsewhere. Paths can be
directories, ROM files, ZIPs, self-extracting EXEs or Update Manager / PUB
installers. Use -port sim for a built-in
emulated machine. Run "groundcontrol <command> -h" for the options.
`

func usage() { fmt.Fprintf(os.Stderr, usageText, version) }

func main() {
	args := os.Args[1:]
	if len(args) == 1 && (args[0] == "version" || args[0] == "-version" || args[0] == "--version") {
		fmt.Println("GroundControl P2K", version)
		return
	}
	cmd := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		switch args[0] {
		case "list", "info", "extract", "ports", "query", "flash", "dump", "report", "linetest", "romtest", "shell", "emulate", "help", "tui":
			cmd, args = args[0], args[1:]
		}
	}
	var err error
	switch cmd {
	case "", "tui":
		err = cmdTUI(args)
	case "list":
		err = cmdList(args)
	case "info":
		err = cmdInfo(args)
	case "extract":
		err = cmdExtract(args)
	case "ports":
		for _, p := range serial.List() {
			fmt.Println(p)
		}
	case "query":
		err = cmdQuery(args)
	case "flash":
		err = cmdFlash(args)
	case "dump":
		err = cmdDump(args)
	case "report":
		err = cmdReport(args)
	case "linetest":
		err = cmdLineTest(args)
	case "shell":
		err = cmdShell(args)
	case "romtest":
		err = cmdROMTest(args)
	case "emulate":
		err = cmdEmulate(args)
	case "help":
		usage()
	}
	if err != nil {
		if !errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(os.Stderr, "error:", err)
		}
		os.Exit(1)
	}
}

// portFlags are shared by commands that talk to a machine.
type portFlags struct {
	port     string
	dtr, rts bool
	simSpeed float64
	trace    string // file that receives a log of all serial traffic
}

func (pf *portFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&pf.port, "port", "", "serial port (e.g. /dev/ttyUSB0, COM1), \"sim\", or tcp:HOST:PORT (raw TCP, baud changes not forwarded)")
	fs.BoolVar(&pf.dtr, "dtr", false, "assert DTR (the original tool kept it off)")
	fs.BoolVar(&pf.rts, "rts", false, "assert RTS (the original tool kept it off)")
	fs.Float64Var(&pf.simSpeed, "sim-speed", 0, "line speed of -port sim: 0 = instant, 1 = real time")
	fs.StringVar(&pf.trace, "trace", "", "write a log of all serial traffic to this file (for bug reports)")
}

func (pf *portFlags) open(simBoot []byte) (*conn, error) {
	if pf.port == "" {
		return nil, errors.New("no serial port given (-port); see \"groundcontrol ports\"")
	}
	c, err := openConn(pf.port, serial.Config{DTR: pf.dtr, RTS: pf.rts}, simBoot, pf.simSpeed)
	if err != nil || pf.trace == "" {
		return c, err
	}
	f, err := os.OpenFile(pf.trace, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		c.close()
		return nil, err
	}
	fmt.Fprintf(f, "# command: %s\n", strings.Join(os.Args, " "))
	c.port = fupdate.NewTracePort(c.port, f)
	inner := c.close
	c.close = func() {
		inner()
		f.Close()
		fmt.Println("serial trace written to", pf.trace)
	}
	return c, nil
}

// dirFlag collects -dir options; it may be given several times.
type dirFlag []string

func (d *dirFlag) String() string { return strings.Join(*d, ",") }
func (d *dirFlag) Set(v string) error {
	*d = append(*d, v)
	return nil
}

// searchPaths registers -dir on fs. The returned function yields the
// paths to scan: every -dir, then the positional arguments, or the
// current directory when neither is given.
func searchPaths(fs *flag.FlagSet) func() []string {
	var d dirFlag
	fs.Var(&d, "dir", "directory, archive or ROM file with updates (default: current directory; may be repeated)")
	return func() []string {
		ps := append(append([]string{}, d...), fs.Args()...)
		if len(ps) == 0 {
			ps = []string{"."}
		}
		return ps
	}
}

func scan(paths []string) (*source.Result, error) {
	if len(paths) == 0 {
		paths = []string{"."}
	}
	r := source.Scan(paths...)
	for _, w := range r.Warnings {
		fmt.Fprintln(os.Stderr, "warning:", w)
	}
	if len(r.Sets) == 0 {
		where := strings.Join(paths, ", ")
		if where == "." {
			where = "the current directory; use -dir PATH to search elsewhere"
		}
		return r, fmt.Errorf("no Pinball 2000 update found in %s", where)
	}
	return r, nil
}

// pickSet selects a set by 1-based index or version string.
func pickSet(sets []*romset.Set, sel string) (*romset.Set, error) {
	if sel == "" {
		if len(sets) == 1 {
			return sets[0], nil
		}
		return nil, fmt.Errorf("%d updates found, choose one with -set (number or version)", len(sets))
	}
	if n, err := strconv.Atoi(sel); err == nil && n >= 1 && n <= len(sets) && !strings.Contains(sel, ".") {
		return sets[n-1], nil
	}
	var hit []*romset.Set
	for _, s := range sets {
		if s.Version() == sel || (s.Boot != nil && s.Boot.GameVersion() == sel) {
			hit = append(hit, s)
		}
	}
	if len(hit) == 1 {
		return hit[0], nil
	}
	return nil, fmt.Errorf("-set %s matches %d updates", sel, len(hit))
}

func cmdList(args []string) error {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	paths := searchPaths(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	r, err := scan(paths())
	if err != nil {
		return err
	}
	printSets(os.Stdout, r.Sets)
	return nil
}

func cmdInfo(args []string) error {
	fs := flag.NewFlagSet("info", flag.ContinueOnError)
	sel := fs.String("set", "", "update number (from list) or version; default: all")
	paths := searchPaths(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	r, err := scan(paths())
	if err != nil {
		return err
	}
	sets := r.Sets
	if *sel != "" {
		s, err := pickSet(sets, *sel)
		if err != nil {
			return err
		}
		sets = []*romset.Set{s}
	}
	for i, s := range sets {
		if i > 0 {
			fmt.Println()
		}
		printSetDetails(os.Stdout, s)
	}
	return nil
}

func cmdExtract(args []string) error {
	fs := flag.NewFlagSet("extract", flag.ContinueOnError)
	sel := fs.String("set", "", "update number (from list) or version")
	out := fs.String("o", "", "output directory")
	paths := searchPaths(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *out == "" {
		return errors.New("extract: -o DIR is required")
	}
	r, err := scan(paths())
	if err != nil {
		return err
	}
	s, err := pickSet(r.Sets, *sel)
	if err != nil {
		return err
	}
	return extractSet(s, *out)
}

func extractSet(s *romset.Set, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	prefix := s.Prefix
	if prefix == "" {
		prefix = fmt.Sprintf("pin2000_%d_%s_", s.GameID(), strings.ReplaceAll(s.Version(), ".", ""))
	}
	for _, p := range s.Parts {
		if p == nil {
			continue
		}
		fn := filepath.Join(dir, prefix+p.Kind.FileName())
		if err := os.WriteFile(fn, p.Data, 0o644); err != nil {
			return err
		}
		fmt.Println("wrote", fn)
	}
	return nil
}

func cmdQuery(args []string) error {
	fs := flag.NewFlagSet("query", flag.ContinueOnError)
	var pf portFlags
	pf.register(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	c, err := pf.open(nil)
	if err != nil {
		return err
	}
	defer c.close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	mi, err := fupdate.QueryMachine(ctx, c.port)
	if err != nil {
		return err
	}
	printMachine(os.Stdout, mi)
	return nil
}

// updateParams are the inputs of one update run.
type updateParams struct {
	set      *romset.Set
	mode     fupdate.Mode
	baud     int
	skipCmd  bool
	force    bool
	noQuery  bool
	confirm  func(question string) bool
	pf       portFlags
	simBoot  []byte
	resyncOK bool
}

// runUpdate checks the machine, asks for confirmation and transfers.
func runUpdate(p updateParams) error {
	plan, err := fupdate.BuildPlan(p.set, p.mode)
	if err != nil {
		return err
	}
	c, err := p.pf.open(p.simBoot)
	if err != nil {
		return err
	}
	defer c.close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	fmt.Printf("Update:   %s (%s)\n", p.set.Label(), p.mode)
	fmt.Printf("Port:     %s, %d baud, %d packets, %s, about %s\n", p.pf.port, p.baud,
		len(plan.Packets), mb(plan.Bytes), clock(plan.Estimate(p.baud)))

	if !p.noQuery && !p.skipCmd {
		fmt.Println("Asking the machine for its installed version ...")
		mi, err := fupdate.QueryMachine(ctx, c.port)
		switch {
		case err != nil:
			fmt.Println("  no answer:", err)
			if !p.confirm("The machine did not answer. Try the update anyway?") {
				return errors.New("cancelled")
			}
		default:
			printMachine(os.Stdout, mi)
			if warn, fatal := compare(p.set, mi); warn != "" {
				fmt.Println("WARNING:", warn)
				if fatal && !p.force {
					return errors.New("refusing to install an update for a different game (use -force to override)")
				}
			}
		}
	}
	fmt.Println()
	fmt.Println("The machine must be in attract mode (no game running). During the")
	fmt.Println("transfer the display shows \"FLASH Update In Progress\". Do not switch")
	fmt.Println("the machine off. If the transfer is interrupted the machine starts the")
	fmt.Println("game version stored in its PRISM ROMs until an update completes.")
	fmt.Println()
	fmt.Println("WARNING: Writing firmware is risky. A faulty or wrong image, an interrupted")
	fmt.Println("transfer, a power failure or a bad cable can leave the FLASH inconsistent")
	fmt.Println("and in the worst case make the PRISM board unusable. The previous contents")
	fmt.Println("of the FLASH are overwritten (reading them back with \"dump\" takes hours).")
	fmt.Println("You use this software at your own risk, without any warranty or liability.")
	if !p.confirm("Start the update now?") {
		return errors.New("cancelled")
	}

	pr := &printer{w: os.Stdout}
	start := time.Now()
	err = fupdate.Send(ctx, c.port, plan, fupdate.Options{
		Baud: p.baud, SkipCommand: p.skipCmd, Progress: pr.Progress, Log: pr.Logf,
	})
	pr.Done()
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return errors.New("aborted by user; the machine gives up after about 30 seconds")
		}
		if errors.Is(err, fupdate.ErrMachineRebooted) {
			return fmt.Errorf("%w\nThe machine restarts with its previous version. Older firmware (before\n"+
				"1.20) can fail like this; \"groundcontrol linetest\" checks it without writing anything", err)
		}
		return err
	}
	fmt.Printf("Transfer complete in %s. The machine now finishes the update and restarts.\n",
		clock(time.Since(start)))
	if c.sim != nil {
		time.Sleep(200 * time.Millisecond)
		fmt.Println("emulator result:", c.sim.LastLoad)
		if mi, err := fupdate.QueryMachine(ctx, c.port); err == nil {
			printMachine(os.Stdout, mi)
		}
	} else {
		fmt.Println("After the restart, \"groundcontrol query\" shows the installed version.")
	}
	return nil
}

func confirmStdin(yes bool) func(string) bool {
	return func(q string) bool {
		if yes {
			return true
		}
		if !stdinIsTerminal() {
			fmt.Println(q, "(not confirmed: no terminal, use -yes)")
			return false
		}
		return askYesNo(q)
	}
}

func cmdFlash(args []string) error {
	fs := flag.NewFlagSet("flash", flag.ContinueOnError)
	var pf portFlags
	pf.register(fs)
	sel := fs.String("set", "", "update number (from list) or version")
	baud := fs.Int("baud", 115200, "transfer baud rate: 9600, 19200, 38400, 57600, 115200")
	mode := fs.String("mode", "auto", "what to update: game, sound, both, auto (both if sf.rom is present)")
	yes := fs.Bool("yes", false, "do not ask for confirmation")
	force := fs.Bool("force", false, "install even if the machine runs a different game")
	noCmd := fs.Bool("no-cmd", false, "do not send \"fupdate load\"; the machine is already waiting")
	noQuery := fs.Bool("no-query", false, "do not ask the machine for its version first")
	paths := searchPaths(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	r, err := scan(paths())
	if err != nil {
		return err
	}
	s, err := pickSet(r.Sets, *sel)
	if err != nil {
		return err
	}
	m, err := resolveMode(s, *mode)
	if err != nil {
		return err
	}
	if !fupdate.ValidBaud(*baud) {
		return fmt.Errorf("baud rate %d not supported (9600, 19200, 38400, 57600, 115200)", *baud)
	}
	return runUpdate(updateParams{set: s, mode: m, baud: *baud, skipCmd: *noCmd, force: *force,
		noQuery: *noQuery, confirm: confirmStdin(*yes), pf: pf, simBoot: simBootFor(r.Sets, s)})
}

func resolveMode(s *romset.Set, mode string) (fupdate.Mode, error) {
	if mode == "auto" {
		if s.SoundUpdateReady() == nil {
			return fupdate.ModeBoth, nil
		}
		return fupdate.ModeGame, nil
	}
	return fupdate.ParseMode(mode)
}

// simBootFor gives the emulator an older bootdata of the same game, so the
// simulated machine looks like one that needs the update.
func simBootFor(sets []*romset.Set, s *romset.Set) []byte {
	for _, o := range sets {
		if o != s && o.GameID() == s.GameID() && o.Parts[romset.PartBootData] != nil {
			return o.Parts[romset.PartBootData].Data
		}
	}
	if p := s.Parts[romset.PartBootData]; p != nil {
		return p.Data
	}
	return nil
}

func cmdEmulate(args []string) error {
	fs := flag.NewFlagSet("emulate", flag.ContinueOnError)
	var pf portFlags
	pf.register(fs)
	rom := fs.String("rom", "", "bootdata.rom reported as the PRISM ROM version")
	save := fs.String("save", "", "write the received update FLASH and sound FLASH images to this directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if pf.port == "" || strings.EqualFold(pf.port, SimPortName) {
		return errors.New("emulate needs a real serial port (-port)")
	}
	var boot []byte
	if *rom != "" {
		var err error
		if boot, err = os.ReadFile(*rom); err != nil {
			return err
		}
	}
	p, err := serial.Open(pf.port, serial.Config{DTR: pf.dtr, RTS: pf.rts})
	if err != nil {
		return err
	}
	defer p.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	cfg := fupdate.MachineConfig{ROMBoot: boot, Log: func(f string, a ...any) {
		fmt.Printf(f+"\n", a...)
	}}
	if *save != "" {
		cfg.OnUpdate = func(m *fupdate.Machine) {
			os.MkdirAll(*save, 0o755)
			for name, data := range map[string][]byte{"flash.bin": m.Flash(), "sound.bin": m.Sound()} {
				fn := filepath.Join(*save, name)
				if err := os.WriteFile(fn, data, 0o644); err != nil {
					fmt.Println("save:", err)
				} else {
					fmt.Println("saved", fn)
				}
			}
		}
	}
	m := fupdate.NewMachine(cfg)
	fmt.Printf("Emulating a Pinball 2000 on %s (shell at 9600 baud). Ctrl-C to stop.\n", pf.port)
	err = m.Run(ctx, p)
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}
