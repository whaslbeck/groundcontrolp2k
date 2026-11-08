package fupdate

import (
	"bytes"
	"context"
	"testing"

	"github.com/whaslbeck/groundcontrolp2k/internal/romset"
)

func TestDumpRangePart(t *testing.T) {
	s := synthSet(t, 15, 50070, 2, 60)
	_, port, stop := dumpMachine(t, s, MachineFaults{GarbleDump: []int{2}})
	defer stop()
	bd := s.Boot
	off, n, ok := PartRange(romset.PartSymbols, bd.ImSys0Size, bd.GameCodeSize, bd.SymbolsSize)
	if !ok {
		t.Fatal("no range")
	}
	opt := fastDump()
	opt.Have = s.Parts[romset.PartSymbols].Data[:1000] // continue an interrupted read
	got, err := DumpRange(context.Background(), port, FlashBase+uint32(off), n, "symbols", opt)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, s.Parts[romset.PartSymbols].Data) {
		t.Fatal("symbols differ")
	}
	if romset.Sum32(got, bd.SymbolsSize) != bd.SymbolsChecksum {
		t.Fatal("checksum")
	}
}
