package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/whaslbeck/groundcontrolp2k/internal/fupdate"
)

type reportParams struct {
	pf      portFlags
	out     string // file name, "" = automatic, "-" = stdout only
	quiet   bool
	simBoot []byte // -port sim: bootdata the emulated machine runs
}

func cmdReport(args []string) error {
	fs := flag.NewFlagSet("report", flag.ContinueOnError)
	var p reportParams
	p.pf.register(fs)
	fs.StringVar(&p.out, "o", "", "output file (default: p2k-report-<game>-<date>.txt in the current directory, \"-\" = print only)")
	fs.BoolVar(&p.quiet, "quiet", false, "do not print the report, only save it")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return runReport(p)
}

var gameNameRe = regexp.MustCompile(`Game\((.*?) - (.*?) - (\d+) - ([^)]*)\)`)

// runReport reads the service report from the machine and saves it.
func runReport(p reportParams) error {
	c, err := p.pf.open(p.simBoot)
	if err != nil {
		return err
	}
	defer c.close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	fmt.Printf("Reading the service report from the machine (%d sections, about a minute) ...\n",
		len(fupdate.DefaultReport))
	rs, err := fupdate.ReadReport(ctx, c.port, fupdate.DefaultReport, 0, func(r fupdate.ReportResult) {
		if r.Err != nil {
			fmt.Printf("  %-20s %v\n", r.Title, r.Err)
			return
		}
		fmt.Printf("  %-20s %d lines\n", r.Title, strings.Count(r.Text, "\n")+1)
	})
	if err != nil {
		return err
	}

	game, gameVersion := "unknown", ""
	for _, r := range rs {
		if m := gameNameRe.FindStringSubmatch(r.Text); m != nil && r.Cmd == "game name" {
			game, gameVersion = m[2], m[4]
			break
		}
	}
	now := time.Now()
	header := fmt.Sprintf("Pinball 2000 service report\n%s\n\nMachine:  %s %s\nRead:     %s\nPort:     %s\nTool:     GroundControl P2K %s\n",
		strings.Repeat("=", 72), game, gameVersion, now.Format("2006-01-02 15:04:05"), p.pf.port, version)
	text := fupdate.FormatReport(header, rs)

	if !p.quiet {
		fmt.Println()
		fmt.Println(text)
	}
	if p.out == "-" {
		return nil
	}
	fn := p.out
	if fn == "" {
		slug := strings.ToLower(strings.Join(strings.Fields(game), "-"))
		fn = fmt.Sprintf("p2k-report-%s-%s.txt", slug, now.Format("2006-01-02-150405"))
	}
	if err := os.WriteFile(fn, []byte(text), 0o644); err != nil {
		return err
	}
	abs, _ := filepath.Abs(fn)
	fmt.Println("saved", abs)
	failed := 0
	for _, r := range rs {
		if r.Err != nil {
			failed++
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d sections could not be read", failed, len(rs))
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return errors.New("interrupted")
	}
	return nil
}
