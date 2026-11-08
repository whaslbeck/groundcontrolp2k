package fupdate

import (
	"context"
	"errors"
	"testing"
	"time"
)

func runLineTest(t *testing.T, mf MachineFaults) []LineTestResult {
	t.Helper()
	m := NewMachine(MachineConfig{Faults: mf, Log: t.Logf, InputTimeout: 2 * time.Second})
	link := NewSimLink(LinkFaults{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx, link.Machine)
	port := Port(link.Host)
	before := m.Flash()
	rs, err := LineTest(context.Background(), port, LineTestCases(), LineTestOptions{
		Wait: 300 * time.Millisecond, RecoverWait: 2500 * time.Millisecond, Log: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	if string(m.Flash()) != string(before) {
		t.Fatal("the line test changed the FLASH")
	}
	return rs
}

func TestLineTestRaw(t *testing.T) {
	rs := runLineTest(t, MachineFaults{})
	if len(rs) != len(LineTestCases()) {
		t.Fatalf("%d results", len(rs))
	}
	for _, r := range rs {
		if !r.OK() {
			t.Errorf("%s: %s", r.Name, r.Describe())
		}
	}
}

func TestLineTestLineMode(t *testing.T) {
	rs := runLineTest(t, MachineFaults{LineMode: true})
	got := map[string]bool{}
	for _, r := range rs {
		got[r.Name] = r.OK()
	}
	if !got["plain text"] || !got["zero bytes"] {
		t.Errorf("control packets failed: %v", got)
	}
	if got["XOFF (0x13)"] || got["XON (0x11)"] {
		t.Errorf("flow control bytes not detected: %v", got)
	}
	for _, n := range []string{"CR (0x0d)", "LF (0x0a)"} {
		if !got[n] {
			t.Errorf("%s wrongly reported as lost: %v", n, got)
		}
	}
}

// A receiver that restarts the machine on one byte value (as seen on a
// machine with firmware 0.90): the test marks that case, waits for the
// shell, starts the receiver again and tests the remaining bytes.
func TestFlashDetectsReboot(t *testing.T) {
	s := synthSet(t, 14, 50070, 2, 60)
	opt := fastOpts()
	_, err := runUpdate(t, s, ModeGame, MachineFaults{RebootOn: 0x13}, LinkFaults{}, opt)
	if !errors.Is(err, ErrMachineRebooted) {
		t.Fatalf("want ErrMachineRebooted, got %v", err)
	}
}

// The behaviour seen on a machine with 0.90: the receiver restarts the
// machine on its third packet, whatever it contains. Retesting the case
// as the first packet of a fresh receiver shows that the byte is not the
// cause.
func TestLineTestRebootByPosition(t *testing.T) {
	rs := runLineTest(t, MachineFaults{RebootAtPacket: 3})
	byByte, byPos := RestartCause(rs)
	if len(byByte) != 0 {
		t.Errorf("bytes blamed: %v", byByte)
	}
	if len(byPos) == 0 {
		t.Fatal("no position restarts found")
	}
	for _, p := range byPos {
		if p != 3 {
			t.Errorf("restart at packet %d, want 3", p)
		}
	}
	// every case must end up with a clean result
	ok := map[string]bool{}
	for _, r := range rs {
		if r.OK() {
			ok[r.Name] = true
		}
	}
	for _, c := range LineTestCases() {
		if !ok[c.Name] {
			t.Errorf("%s never passed", c.Name)
		}
	}
}

func TestLineTestRebootByByteRetest(t *testing.T) {
	rs := runLineTest(t, MachineFaults{RebootOn: 0x13})
	byByte, byPos := RestartCause(rs)
	if len(byByte) != 1 || byByte[0] != "XOFF (0x13)" || len(byPos) != 0 {
		t.Errorf("byByte %v byPos %v", byByte, byPos)
	}
}
