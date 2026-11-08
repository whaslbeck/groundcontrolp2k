package fupdate

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/whaslbeck/groundcontrolp2k/internal/romset"
)

// Timing scaled down 10x from a real machine: input timeout 5s -> 500ms,
// hang detector 3s -> 300ms. The sender uses its defaults scaled the
// same way and no resync pause.
func runRealTiming(t *testing.T, mf MachineFaults, lf LinkFaults) (*Machine, error) {
	t.Helper()
	s := synthSet(t, 21, 50070, 2, 60)
	link := NewSimLink(lf)
	m := NewMachine(MachineConfig{InputTimeout: 500 * time.Millisecond, HangTimeout: 300 * time.Millisecond,
		Faults: mf, Log: t.Logf})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	go m.Run(ctx, link.Machine)
	plan, err := BuildPlan(s, ModeGame)
	if err != nil {
		t.Fatal(err)
	}
	err = Send(ctx, link.Host, plan, Options{ReplyTimeout: 100 * time.Millisecond,
		FirstTimeout: 150 * time.Millisecond, StartDelay: 50 * time.Millisecond, Log: t.Logf})
	for i := 0; i < 50 && m.LastLoad == "" && err == nil; i++ {
		time.Sleep(20 * time.Millisecond)
	}
	if err == nil {
		checkResult(t, m, s, ModeGame)
	}
	return m, err
}

func TestRealTimingRecovers(t *testing.T) {
	for name, mf := range map[string]MachineFaults{
		"clean":           {},
		"lost replies":    {DropReplyTo: []int{0, 5, 20, 21}},
		"corrupt packets": {CorruptPacket: []int{0, 3, 50}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := runRealTiming(t, mf, LinkFaults{}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// A byte lost on the line leaves the machine's framing shifted. A real
// machine cannot recover before its hang detector fires; the sender must
// report the restart instead of retrying for a long time.
func TestRealTimingLostByteRestart(t *testing.T) {
	start := time.Now()
	_, err := runRealTiming(t, MachineFaults{}, LinkFaults{DropByteAt: []int{PacketSize*7 + 100}})
	if !errors.Is(err, ErrMachineRebooted) {
		t.Fatalf("want ErrMachineRebooted, got %v", err)
	}
	if time.Since(start) > 20*time.Second {
		t.Errorf("took %v", time.Since(start))
	}
}

// On 0.90 the first packet after the start arrives garbled if it contains
// 0x13. Without the warm-up packet the machine NAKs packet 0 until its
// hang detector restarts it; with it, the update goes through.
func TestWarmupAvoidsFirstPacketGarble(t *testing.T) {
	s := synthSet(t, 22, 50070, 2, 60)
	data := s.Parts[romset.PartBootData].Data
	data[200] = 0x13 // an XOFF byte in the first packet, as in 1.60 (reserved area)
	fixBootChecksum(s)
	run := func(noWarmup bool) error {
		link := NewSimLink(LinkFaults{})
		m := NewMachine(MachineConfig{InputTimeout: 500 * time.Millisecond, HangTimeout: 300 * time.Millisecond,
			Faults: MachineFaults{FirstXoffGarble: true}, Log: t.Logf})
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		go m.Run(ctx, link.Machine)
		plan, err := BuildPlan(s, ModeGame)
		if err != nil {
			t.Fatal(err)
		}
		err = Send(ctx, link.Host, plan, Options{ReplyTimeout: 100 * time.Millisecond, NoWarmup: noWarmup,
			FirstTimeout: 150 * time.Millisecond, StartDelay: 50 * time.Millisecond, Log: t.Logf})
		for i := 0; i < 50 && m.LastLoad == "" && err == nil; i++ {
			time.Sleep(20 * time.Millisecond)
		}
		if err == nil {
			checkResult(t, m, s, ModeGame)
		}
		return err
	}
	// Without the warm-up the quick repetition of packet 0 now usually
	// recovers as well; only the variant with warm-up is required to pass.
	if err := run(false); err != nil {
		t.Errorf("with warm-up: %v", err)
	}
}

// fixBootChecksum recomputes the bootdata checksum after a change.
func fixBootChecksum(s *romset.Set) {
	boot := s.Parts[romset.PartBootData].Data
	l := leU32{boot}
	l.put(0x28, 0)
	l.put(0x28, -romset.Sum32(boot, romset.BootDataChecksumWords))
	s.Boot, _ = romset.ParseBootData(boot)
}

// As seen on 0.90: some starts have one stray byte in the receive buffer,
// so the framing is off and the machine restarts. Nothing has been
// written then, and the sender starts again until a start is clean.
func TestStrayByteStartRetried(t *testing.T) {
	s := synthSet(t, 23, 50070, 2, 60)
	link := NewSimLink(LinkFaults{})
	m := NewMachine(MachineConfig{InputTimeout: 500 * time.Millisecond, HangTimeout: 300 * time.Millisecond,
		Faults: MachineFaults{StrayByteStarts: 2}, Log: t.Logf})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	go m.Run(ctx, link.Machine)
	plan, err := BuildPlan(s, ModeGame)
	if err != nil {
		t.Fatal(err)
	}
	err = Send(ctx, link.Host, plan, Options{ReplyTimeout: 100 * time.Millisecond,
		FirstTimeout: 150 * time.Millisecond, StartDelay: 50 * time.Millisecond, BootWait: 20 * time.Second, Log: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 50 && m.LastLoad != "ok"; i++ {
		time.Sleep(20 * time.Millisecond)
	}
	checkResult(t, m, s, ModeGame)
	if m.starts != 3 {
		t.Errorf("receiver started %d times, want 3", m.starts)
	}
}
