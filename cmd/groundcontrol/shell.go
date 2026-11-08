package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/whaslbeck/groundcontrolp2k/internal/fupdate"
)

// cmdShell runs one command in the machine's shell and prints the answer.
func cmdShell(args []string) error {
	fs := flag.NewFlagSet("shell", flag.ContinueOnError)
	var pf portFlags
	pf.register(fs)
	max := fs.Duration("max", 30*time.Second, "longest time to wait for the complete answer")
	quiet := fs.Duration("quiet", 1500*time.Millisecond, "the answer is complete after this much silence")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cmd := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if cmd == "" {
		return errors.New(`shell: give the command, e.g. groundcontrol shell -port P "game name"`)
	}
	c, err := pf.open(nil)
	if err != nil {
		return err
	}
	defer c.close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	fupdate.RunShell(ctx, c.port, "", 300*time.Millisecond, 600*time.Millisecond)
	raw, err := fupdate.RunShell(ctx, c.port, cmd, *quiet, *max)
	if out := fupdate.CleanShellOutput(raw, cmd); out != "" {
		fmt.Println(out)
	}
	return err
}
