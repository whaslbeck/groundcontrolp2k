package fupdate

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// Firmware without the sound options refuses a game+sound update before
// any packet is sent, and still accepts a game update.
func TestNoSoundOption(t *testing.T) {
	s := synthSet(t, 12, 50070, 2, 60)
	_, err := runUpdate(t, s, ModeBoth, MachineFaults{NoSoundOption: true}, LinkFaults{}, fastOpts())
	if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "-mode game") {
		t.Fatalf("want a refusal that suggests -mode game, got %v", err)
	}
	m, err := runUpdate(t, s, ModeGame, MachineFaults{NoSoundOption: true}, LinkFaults{}, fastOpts())
	if err != nil {
		t.Fatal(err)
	}
	checkResult(t, m, s, ModeGame)
}

func TestIsUsageAnswer(t *testing.T) {
	for _, c := range []struct {
		text, cmd string
		want      bool
	}{
		{"printout <audits | adjustments | hourly | daily | everything>", "printout pricing", true},
		{"hstd_dump: not found", "hstd_dump", true},
		{"usage: switch <debug | callbacks | timers | counters>", "switch x", true},
		{"PDB: all fuses are good", "pdb", false},
		{"\nHigh Scores\n-----------\n  GAG 325,000,000", "printout hstd", false},
	} {
		if got := isUsageAnswer(c.text, c.cmd); got != c.want {
			t.Errorf("%q: got %v", c.text, got)
		}
	}
}

func TestTracePort(t *testing.T) {
	link := NewSimLink(LinkFaults{})
	var log bytes.Buffer
	tp := NewTracePort(link.Host, &log)
	tp.Write([]byte("help\n"))
	tp.Write(Packet{Block: 7, Data: []byte{1, 2, 3}}.Encode())
	tp.SetBaud(115200)
	link.Machine.SetBaud(115200)
	link.Machine.Write([]byte{6, 0, 7, 0})
	buf := make([]byte, 8)
	tp.ReadTimeout(buf, 100e6)
	out := log.String()
	for _, want := range []string{"TX   68 65 6c 70 0a", "| help.", "packet block=7 len=3",
		"baud 9600 -> 115200", "RX   06 00 07 00"} {
		if !strings.Contains(out, want) {
			t.Errorf("trace lacks %q:\n%s", want, out)
		}
	}
}
