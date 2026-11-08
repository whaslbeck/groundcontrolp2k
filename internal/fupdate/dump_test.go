package fupdate

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/whaslbeck/groundcontrolp2k/internal/romset"
)

// dumpMachine returns an emulated machine whose update FLASH holds s.
func dumpMachine(t *testing.T, s *romset.Set, mf MachineFaults) (*Machine, Port, func()) {
	t.Helper()
	m := NewMachine(MachineConfig{Faults: mf, Log: t.Logf})
	if s != nil {
		copy(m.flash, expectedFlash(s))
	}
	link := NewSimLink(LinkFaults{})
	ctx, cancel := context.WithCancel(context.Background())
	go m.Run(ctx, link.Machine)
	return m, link.Host, cancel
}

func fastDump() DumpOptions {
	return DumpOptions{Chunk: 4096, Pause: time.Millisecond, Idle: 500 * time.Millisecond,
		RecoverDelay: 50 * time.Millisecond}
}

func checkDump(t *testing.T, s, got *romset.Set) {
	t.Helper()
	for _, c := range got.Checks() {
		if c.Kind <= romset.PartSymbols && c.Status != romset.StatusOK {
			t.Errorf("%s: %s %s", c.Kind, c.Status, c.Detail)
		}
	}
	for k := romset.PartBootData; k <= romset.PartSymbols; k++ {
		if !bytes.Equal(got.Parts[k].Data, s.Parts[k].Data) {
			t.Errorf("%s differs", k)
		}
	}
}

func TestDumpClean(t *testing.T) {
	s := synthSet(t, 7, 50070, 1, 80)
	// text in the ASCII column must not be mistaken for messages
	copy(s.Parts[romset.PartGame].Data[32:], "*** Fatal: x    ")
	copy(s.Parts[romset.PartGame].Data[48:], "monitor> not found  % usage")
	fixGameChecksum(s)
	_, port, stop := dumpMachine(t, s, MachineFaults{})
	defer stop()
	got, err := DumpFlash(context.Background(), port, fastDump())
	if err != nil {
		t.Fatal(err)
	}
	checkDump(t, s, got)
}

func TestDumpFaults(t *testing.T) {
	s := synthSet(t, 8, 50070, 2, 60)
	_, port, stop := dumpMachine(t, s, MachineFaults{DumpFatalOver: 2048, GarbleDump: []int{1, 5}})
	defer stop()
	opt := fastDump()
	opt.Log = t.Logf
	got, err := DumpFlash(context.Background(), port, opt)
	if err != nil {
		t.Fatal(err)
	}
	checkDump(t, s, got)
}

func TestDumpResume(t *testing.T) {
	s := synthSet(t, 9, 50070, 2, 60)
	img := expectedFlash(s)
	_, port, stop := dumpMachine(t, s, MachineFaults{})
	defer stop()
	opt := fastDump()
	opt.Have = img[:40000]
	first := -1
	opt.OnData = func(off int, _ []byte) error {
		if first < 0 {
			first = off
		}
		return nil
	}
	got, err := DumpFlash(context.Background(), port, opt)
	if err != nil {
		t.Fatal(err)
	}
	if first != 40000 {
		t.Errorf("first read at %d, want 40000", first)
	}
	checkDump(t, s, got)
}

func TestDumpErasedFlash(t *testing.T) {
	_, port, stop := dumpMachine(t, nil, MachineFaults{})
	defer stop()
	_, err := DumpFlash(context.Background(), port, fastDump())
	if err == nil {
		t.Fatal("dump of an erased FLASH succeeded")
	}
	var de *DumpError
	if errors.As(err, &de) {
		t.Fatalf("want a bootdata error, got %v", err)
	}
}

func fixGameChecksum(s *romset.Set) {
	g := s.Parts[romset.PartGame].Data
	boot := s.Parts[romset.PartBootData].Data
	le := leU32{boot}
	le.put(0x50, romset.Sum32(g, uint32(len(g)/4)))
	le.put(0x28, 0)
	le.put(0x28, -romset.Sum32(boot, romset.BootDataChecksumWords))
	s.Boot, _ = romset.ParseBootData(boot)
}

type leU32 struct{ b []byte }

func (l leU32) put(off int, v uint32) {
	l.b[off], l.b[off+1], l.b[off+2], l.b[off+3] = byte(v), byte(v>>8), byte(v>>16), byte(v>>24)
}
