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

func cmdLineTest(args []string) error {
	fs := flag.NewFlagSet("linetest", flag.ContinueOnError)
	var pf portFlags
	pf.register(fs)
	baud := fs.Int("baud", 9600, "transfer baud rate to test")
	yes := fs.Bool("yes", false, "do not ask for confirmation")
	sel := fs.String("set", "", "also send the real first packet of this update (rejected, so nothing is written)")
	paths := searchPaths(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	cases := fupdate.LineTestCases()
	if *sel != "" {
		r, err := scan(paths())
		if err != nil {
			return err
		}
		set, err := pickSet(r.Sets, *sel)
		if err != nil {
			return err
		}
		plan, err := fupdate.BuildPlan(set, fupdate.ModeGame)
		if err != nil {
			return err
		}
		// The real first packet as the first and as the second packet
		// after the start: on 0.90 only the first position failed.
		data := plan.Packets[0].Data
		cases = []fupdate.LineTestCase{
			{Name: "update packet 0, 1st", Special: 0xfe, Data: data},
			cases[0],
			{Name: "update packet 0, 2nd", Special: 0xff, Data: data},
		}
	}
	if !fupdate.ValidBaud(*baud) {
		return fmt.Errorf("baud rate %d not supported", *baud)
	}
	c, err := pf.open(nil)
	if err != nil {
		return err
	}
	defer c.close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	fmt.Println("Line test: starts the machine's update receiver and sends test packets that")
	fmt.Println("it must reject (wrong block number and checksum). Nothing is written to the")
	fmt.Println("FLASH. The display shows \"FLASH Update In Progress\" during the test.")
	fmt.Println("A receiver that gets no valid packet restarts the machine after a few")
	fmt.Println("seconds; that is harmless, the machine comes back with its installed version.")
	fmt.Println("The test then waits for the shell and goes on, so it can take a few minutes.")
	if !confirmStdin(*yes)("Run the line test?") {
		return errors.New("cancelled")
	}
	pr := &printer{w: os.Stdout}
	rs, err := fupdate.LineTest(ctx, c.port, cases, fupdate.LineTestOptions{
		Baud: *baud, Log: pr.Logf,
		OnCase: func(r fupdate.LineTestResult) {
			name := r.Name
			if r.Retest {
				name += ", again"
			}
			fmt.Printf("  #%d %-24s %s\n", r.Seq, name, r.Describe())
		},
	})
	if err != nil {
		return err
	}
	fmt.Println()
	var lost []string
	controlOK := true
	for _, r := range rs {
		switch {
		case r.Special == 0 && !r.OK() && !r.Rebooted:
			controlOK = false
		case !r.OK() && !r.Rebooted && r.Special != 0:
			lost = append(lost, r.Name)
		}
	}
	byByte, byPos := fupdate.RestartCause(rs)
	switch {
	case !controlOK:
		fmt.Println("Result: even plain packets got no proper answer. The receiver does not work")
		fmt.Println("as expected on this firmware, or the line is faulty. Please send the output")
		fmt.Println("of this test run with -trace FILE.")
	case len(byPos) > 0 && len(byByte) == 0 && len(lost) == 0:
		fmt.Println("Result: every byte value arrives unchanged, but the machine restarts on")
		fmt.Printf("packet number %v after the receiver was started, whatever it contains.\n", uniq(byPos))
		fmt.Println("The receiver of this firmware cannot handle repeated rejected packets, so a")
		fmt.Println("serial update with GroundControl will not work. The machine first needs a")
		fmt.Println("newer version by another route, for example the PRISM Update Board (PUB card).")
	case len(byByte) > 0 || len(lost) > 0:
		fmt.Println("Result: plain packets arrive and are answered correctly, but:")
		if len(byByte) > 0 {
			fmt.Println("  the machine RESTARTS on: " + strings.Join(byByte, ", "))
		}
		if len(lost) > 0 {
			fmt.Println("  no answer on:            " + strings.Join(lost, ", "))
		}
		if len(byPos) > 0 {
			fmt.Printf("  and also restarts on packet number %v, whatever it contains\n", uniq(byPos))
		}
		fmt.Println("Every update contains such bytes, so this firmware cannot be updated over")
		fmt.Println("the serial port. The machine first needs a newer version by another route,")
		fmt.Println("for example the PRISM Update Board (PUB card).")
	case len(rs) < len(cases):
		fmt.Println("Result: the test was stopped early.")
	default:
		fmt.Println("Result: all byte values arrive unchanged. The line is fine for an update.")
	}
	return nil
}

func uniq(v []int) []int {
	var out []int
	seen := map[int]bool{}
	for _, x := range v {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}
