package main

import (
	"bytes"
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
)

// partialName is the raw image read so far; a later run continues it.
const partialName = "flash-dump.part"

type dumpParams struct {
	pf      portFlags
	out     string
	chunk   int
	pause   time.Duration
	restart bool
	confirm func(string) bool
	known   []*romset.Set // updates to identify the dumped release
	simSet  *romset.Set   // -port sim: preload the emulated FLASH
	parts   []romset.Kind // read only these files of the image
	addr    uint32        // read a raw memory range instead ...
	length  int           // ... of this many bytes (0 = not)
}

func cmdDump(args []string) error {
	fs := flag.NewFlagSet("dump", flag.ContinueOnError)
	var p dumpParams
	p.pf.register(fs)
	fs.StringVar(&p.out, "o", "", "output directory")
	fs.IntVar(&p.chunk, "chunk", 4096, "bytes per dump command")
	fs.DurationVar(&p.pause, "pause", 300*time.Millisecond, "pause between dump commands")
	fs.BoolVar(&p.restart, "restart", false, "discard an interrupted dump instead of continuing it")
	yes := fs.Bool("yes", false, "do not ask for confirmation")
	simSel := fs.String("sim-set", "", "with -port sim: update (from the paths) the emulated FLASH holds")
	partList := fs.String("part", "", "read only these files, comma separated: bootdata, im_flsh0, game, symbols")
	addr := fs.String("addr", "", "read raw memory from this address (e.g. 0x100000) instead of the image")
	length := fs.String("len", "", "with -addr: number of bytes (e.g. 4096 or 0x1000)")
	paths := searchPaths(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *partList != "" {
		for _, name := range strings.Split(*partList, ",") {
			k, ok := partKind(strings.TrimSpace(name))
			if !ok {
				return fmt.Errorf("-part: unknown file %q (bootdata, im_flsh0, game, symbols)", name)
			}
			p.parts = append(p.parts, k)
		}
	}
	if (*addr == "") != (*length == "") {
		return errors.New("dump: -addr and -len belong together")
	}
	if *addr != "" {
		if *partList != "" {
			return errors.New("dump: use either -part or -addr")
		}
		a, err := strconv.ParseUint(*addr, 0, 32)
		if err != nil {
			return fmt.Errorf("-addr: %v", err)
		}
		n, err := strconv.ParseUint(*length, 0, 32)
		if err != nil || n == 0 || n > 16<<20 {
			return fmt.Errorf("-len: a number between 1 and 16 MB, got %q", *length)
		}
		p.addr, p.length = uint32(a), int(n)
	}
	if p.out == "" {
		return errors.New("dump: -o DIR is required")
	}
	p.confirm = confirmStdin(*yes)
	// known updates are only used to name the result; none found is fine
	r, err := scan(paths())
	if err != nil && *simSel != "" {
		return err
	}
	if r != nil {
		p.known = r.Sets
		if *simSel != "" {
			if p.simSet, err = pickSet(r.Sets, *simSel); err != nil {
				return err
			}
		}
	}
	return runDump(p)
}

// runDump reads the game image from the machine's update FLASH into p.out.
func runDump(p dumpParams) error {
	c, err := p.pf.open(nil)
	if err != nil {
		return err
	}
	defer c.close()
	if c.sim != nil && p.simSet != nil {
		if err := p.simSet.GameUpdateReady(); err != nil {
			return err
		}
		var img []byte
		for k := romset.PartBootData; k <= romset.PartSymbols; k++ {
			img = append(img, p.simSet.Parts[k].Data...)
		}
		c.sim.LoadFlash(img)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if p.length > 0 {
		return dumpRaw(ctx, c, p)
	}

	fmt.Println("Reading the bootdata of the update FLASH ...")
	fupdate.RunShell(ctx, c.port, "", 300*time.Millisecond, 600*time.Millisecond)
	raw, err := fupdate.RunShell(ctx, c.port, "bootdata flash", 700*time.Millisecond, 8*time.Second)
	if err != nil {
		return err
	}
	mi := fupdate.ParseBootDataDump(raw)
	printMachine(os.Stdout, mi)
	if !mi.Valid {
		return errors.New("the update FLASH holds no valid update; nothing to dump " +
			"(a machine without a FLASH update runs from its PRISM ROMs)")
	}
	words := 0
	for _, k := range []string{"im_sys0_size", "game_code_size", "symbols_size"} {
		v, err := strconv.Atoi(mi.Fields[k])
		if err != nil {
			return fmt.Errorf("bootdata: field %s missing or unreadable", k)
		}
		words += v
	}
	total := romset.BootDataSize + 4*words
	if len(p.parts) > 0 {
		return dumpParts(ctx, c, p, mi)
	}

	if err := os.MkdirAll(p.out, 0o755); err != nil {
		return err
	}
	partial := filepath.Join(p.out, partialName)
	var have []byte
	if !p.restart {
		if b, err := os.ReadFile(partial); err == nil {
			have = b[:len(b)&^15]
			if len(have) >= romset.BootDataSize {
				bd, err := romset.ParseBootData(have)
				if err != nil || fmt.Sprintf("0x%x", bd.Checksum) != mi.Fields["boot_data_checksum"] {
					fmt.Println("The interrupted dump in", p.out, "is of a different image, starting over.")
					have = nil
				}
			}
			if have != nil {
				fmt.Printf("Continuing an interrupted dump at %s.\n", mb(len(have)))
			}
		}
	}
	fmt.Printf("\nImage: %s. At 9600 baud this takes about %s (h:mm).\n", mb(total),
		hours(fupdate.DumpEstimate(total-len(have))))
	fmt.Println("The machine must stay in attract mode. Reading does not change anything on")
	fmt.Println("the machine. Ctrl-C stops; running the same command again continues.")
	if !p.confirm("Start reading?") {
		return errors.New("cancelled")
	}

	f, err := os.OpenFile(partial, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	if err := f.Truncate(int64(len(have))); err != nil {
		f.Close()
		return err
	}
	pr := &printer{w: os.Stdout}
	start := time.Now()
	set, err := fupdate.DumpFlash(ctx, c.port, fupdate.DumpOptions{
		Chunk: p.chunk, Pause: p.pause, Have: have, Progress: pr.Progress, Log: pr.Logf,
		OnData: func(off int, data []byte) error {
			_, err := f.WriteAt(data, int64(off))
			return err
		},
	})
	pr.Done()
	f.Close()
	if err != nil {
		var de *fupdate.DumpError
		if errors.As(err, &de) {
			return fmt.Errorf("%v\n%s of the image are saved in %s; run the same command again to continue",
				err, mb(de.Offset), partial)
		}
		return err
	}
	fmt.Printf("Read %s in %s.\n\n", mb(total-len(have)), hours(time.Since(start)))

	set.Prefix = fmt.Sprintf("pin2000_%d_dump_", set.Boot.GameID)
	set.Source = p.out
	if k := identify(set, p.known); k != nil {
		fmt.Printf("The image is identical to update %s (%s).\n", k.Label(), shortSource(k))
		set.FileVersion = k.FileVersion
		if k.FileVersion != "" {
			set.Prefix = fmt.Sprintf("pin2000_%d_%s_dump_", set.Boot.GameID, k.FileVersion)
		}
	}
	printSetDetails(os.Stdout, set)
	for _, c := range set.Checks() {
		if c.Kind <= romset.PartSymbols && c.Status != romset.StatusOK {
			return fmt.Errorf("%s does not match its checksum in bootdata; the raw image stays in %s, "+
				"use -restart to read it again", c.Kind, partial)
		}
	}
	if err := extractSet(set, p.out); err != nil {
		return err
	}
	os.Remove(partial)
	fmt.Println("The sound FLASH (sf.rom) cannot be read through the shell and is not included.")
	return nil
}

// identify finds a known update with exactly the same game image.
func identify(s *romset.Set, known []*romset.Set) *romset.Set {
	for _, k := range known {
		same := true
		for kind := romset.PartImFlsh0; kind <= romset.PartSymbols; kind++ {
			a, b := s.Parts[kind], k.Parts[kind]
			if a == nil || b == nil || !bytes.Equal(a.Data, b.Data) {
				same = false
				break
			}
		}
		// bootdata differs only in the enable flag
		if same && k.Parts[romset.PartBootData] != nil &&
			bytes.Equal(s.Parts[romset.PartBootData].Data[:romset.BootStateOffset],
				k.Parts[romset.PartBootData].Data[:romset.BootStateOffset]) {
			return k
		}
	}
	return nil
}

func hours(d time.Duration) string {
	d = d.Round(time.Minute)
	return fmt.Sprintf("%d:%02d", int(d.Hours()), int(d.Minutes())%60)
}

func partKind(name string) (romset.Kind, bool) {
	for k := romset.PartBootData; k <= romset.PartSymbols; k++ {
		if k.String() == name {
			return k, true
		}
	}
	return 0, false
}

// readResumable reads a range into file name inside p.out, continuing a
// file name+".part" left by an interrupted run.
func readResumable(ctx context.Context, c *conn, p dumpParams, addr uint32, n int, label, name string) ([]byte, error) {
	if err := os.MkdirAll(p.out, 0o755); err != nil {
		return nil, err
	}
	partial := filepath.Join(p.out, name+".part")
	var have []byte
	if !p.restart {
		if b, err := os.ReadFile(partial); err == nil && len(b) <= n {
			have = b[:len(b)&^15]
			if len(have) > 0 {
				fmt.Printf("  %s: continuing at %d of %d bytes\n", label, len(have), n)
			}
		}
	}
	f, err := os.OpenFile(partial, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if err := f.Truncate(int64(len(have))); err != nil {
		return nil, err
	}
	pr := &printer{w: os.Stdout}
	data, err := fupdate.DumpRange(ctx, c.port, addr, n, label, fupdate.DumpOptions{
		Chunk: p.chunk, Pause: p.pause, Have: have, Progress: pr.Progress, Log: pr.Logf,
		OnData: func(off int, d []byte) error {
			_, err := f.WriteAt(d, int64(off))
			return err
		},
	})
	pr.Done()
	if err != nil {
		var de *fupdate.DumpError
		if errors.As(err, &de) {
			return nil, fmt.Errorf("%v\n%d bytes are saved in %s; run the same command again to continue",
				err, de.Offset, partial)
		}
		return nil, err
	}
	f.Close()
	os.Remove(partial)
	return data, nil
}

// dumpRaw reads a raw memory range, e.g. code in RAM.
func dumpRaw(ctx context.Context, c *conn, p dumpParams) error {
	fmt.Printf("Memory 0x%08x-0x%08x (%d bytes), about %s (h:mm) at 9600 baud.\n",
		p.addr, p.addr+uint32(p.length)-1, p.length, hours(fupdate.DumpEstimate(p.length)))
	fmt.Println("Reading changes nothing on the machine. Ctrl-C stops; the same command continues.")
	if !p.confirm("Start reading?") {
		return errors.New("cancelled")
	}
	name := fmt.Sprintf("mem-%08x-%x.bin", p.addr, p.length)
	data, err := readResumable(ctx, c, p, p.addr, p.length, "memory", name)
	if err != nil {
		return err
	}
	fn := filepath.Join(p.out, name)
	if err := os.WriteFile(fn, data, 0o644); err != nil {
		return err
	}
	fmt.Println("wrote", fn)
	return nil
}

// dumpParts reads single files of the FLASH image and checks each
// against the size and checksum the machine reports in its bootdata.
func dumpParts(ctx context.Context, c *conn, p dumpParams, mi *fupdate.MachineInfo) error {
	field := func(k string) (uint32, error) {
		v, err := strconv.ParseUint(mi.Fields[k], 0, 32)
		if err != nil {
			return 0, fmt.Errorf("bootdata: field %s missing or unreadable", k)
		}
		return uint32(v), nil
	}
	var words [3]uint32
	for i, k := range []string{"im_sys0_size", "game_code_size", "symbols_size"} {
		v, err := field(k)
		if err != nil {
			return err
		}
		words[i] = v
	}
	sumField := map[romset.Kind]string{romset.PartImFlsh0: "im_sys0_checksum",
		romset.PartGame: "game_code_checksum", romset.PartSymbols: "symbols_checksum"}
	total := 0
	for _, k := range p.parts {
		_, n, _ := fupdate.PartRange(k, words[0], words[1], words[2])
		total += n
	}
	fmt.Printf("\nFiles: %v, %s. At 9600 baud this takes about %s (h:mm).\n", p.parts, mb(total),
		hours(fupdate.DumpEstimate(total)))
	fmt.Println("Reading changes nothing on the machine. Ctrl-C stops; the same command continues.")
	if !p.confirm("Start reading?") {
		return errors.New("cancelled")
	}
	prefix := fmt.Sprintf("pin2000_%d_dump_", mi.GameID)
	if v := strings.ReplaceAll(mi.GameVersion, ".", ""); len(v) == 3 {
		prefix = fmt.Sprintf("pin2000_%d_0%s_dump_", mi.GameID, v)
	}
	for _, k := range p.parts {
		off, n, _ := fupdate.PartRange(k, words[0], words[1], words[2])
		name := prefix + k.FileName()
		data, err := readResumable(ctx, c, p, fupdate.FlashBase+uint32(off), n, k.String(), name)
		if err != nil {
			return err
		}
		if k == romset.PartBootData {
			if err := romset.ValidateBootData(data); err != nil {
				return fmt.Errorf("bootdata read from the machine is not valid: %v", err)
			}
		} else {
			want, err := field(sumField[k])
			if err != nil {
				return err
			}
			if got := romset.Sum32(data, uint32(n/4)); got != want {
				return fmt.Errorf("%s: checksum 0x%x, the machine expects 0x%x; use -restart to read it again",
					k, got, want)
			}
		}
		fn := filepath.Join(p.out, name)
		if err := os.WriteFile(fn, data, 0o644); err != nil {
			return err
		}
		fmt.Printf("wrote %s (checksum ok)\n", fn)
	}
	return nil
}
