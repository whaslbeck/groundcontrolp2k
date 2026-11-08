package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"

	"github.com/whaslbeck/groundcontrolp2k/internal/fupdate"
)

type romtestParams struct {
	pf    portFlags
	n     int
	which string
}

func cmdROMTest(args []string) error {
	fs := flag.NewFlagSet("romtest", flag.ContinueOnError)
	var p romtestParams
	p.pf.register(fs)
	fs.IntVar(&p.n, "n", 10, "number of runs")
	fs.StringVar(&p.which, "which", "rom", "bootdata to verify: rom, flash or current")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return runROMTest(p)
}

// runROMTest checks the graphics mask ROMs repeatedly and explains the result.
func runROMTest(p romtestParams) error {
	switch p.which {
	case "rom", "flash", "current":
	default:
		return errors.New("-which must be rom, flash or current")
	}
	if p.n < 1 {
		return errors.New("-n must be at least 1")
	}
	c, err := p.pf.open(nil)
	if err != nil {
		return err
	}
	defer c.close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	fmt.Printf("ROM test: \"bootdata %s verify\" %d times. Each run adds up the four graphics\n", p.which, p.n)
	fmt.Println("ROM banks of the PRISM card and takes a few seconds. It only reads.")
	res, err := fupdate.RunROMTest(ctx, c.port, p.which, p.n, func(i int, r fupdate.VerifyRun) {
		var cols []string
		for b, s := range r.Banks {
			if s == "" {
				s = "-"
			}
			col := fmt.Sprintf("bank%d %-3s", b, s)
			if r.Got[b] != 0 {
				col += fmt.Sprintf(" (0x%08x)", r.Got[b])
			}
			cols = append(cols, col)
		}
		line := fmt.Sprintf("  run %2d: %s", i, strings.Join(cols, "  "))
		if r.Garbled > 0 {
			line += "  [serial output disturbed]"
		}
		if !r.Complete {
			line += "  [answer incomplete]"
		}
		fmt.Println(line)
	})
	if res == nil {
		return err
	}
	fmt.Println()
	fmt.Println("  bank    OK   BAD   wrong sums")
	for b, st := range res.Banks {
		fmt.Printf("  %d    %4d  %4d   %s\n", b, st.OK, st.Bad, strings.Join(st.WrongSums(), ", "))
	}
	fmt.Println()
	for _, l := range res.Diagnosis() {
		fmt.Println(l)
	}
	return err
}
