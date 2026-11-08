package fupdate

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"math/rand"
	"testing"
	"time"

	"github.com/whaslbeck/groundcontrolp2k/internal/romset"
)

// synthSet builds a small but structurally valid update set.
func synthSet(t testing.TB, seed int64, gameID, major, minor uint32) *romset.Set {
	t.Helper()
	r := rand.New(rand.NewSource(seed))
	blob := func(words int) []byte {
		b := make([]byte, words*4)
		r.Read(b)
		return b
	}
	im, game, sym := blob(1500+r.Intn(100)), blob(4001), blob(777) // odd packet tails
	sf := make([]byte, romset.SoundFlashSize)
	r.Read(sf)

	boot := make([]byte, romset.BootDataSize)
	copy(boot, "Thu Aug 08 12:44:50 2024")
	le := binary.LittleEndian
	le.PutUint16(boot[0x20:], 1)
	le.PutUint16(boot[0x22:], 1)
	le.PutUint32(boot[0x24:], romset.BootDataSize/4)
	le.PutUint32(boot[0x2c:], uint32(len(im)/4))
	le.PutUint32(boot[0x30:], romset.Sum32(im, uint32(len(im)/4)))
	le.PutUint32(boot[0x34:], 0x2031)
	le.PutUint32(boot[0x38:], 0x3337)
	le.PutUint32(boot[0x3c:], gameID)
	le.PutUint32(boot[0x40:], major)
	le.PutUint32(boot[0x44:], minor)
	le.PutUint32(boot[0x48:], 0x100000)
	le.PutUint32(boot[0x4c:], uint32(len(game)/4))
	le.PutUint32(boot[0x50:], romset.Sum32(game, uint32(len(game)/4)))
	le.PutUint32(boot[0x54:], uint32(len(sym)/4))
	le.PutUint32(boot[0x58:], romset.Sum32(sym, uint32(len(sym)/4)))
	copy(boot[0x7c:], romset.MagicCookie)
	le.PutUint32(boot[romset.BootStateOffset:], 1)
	// make the checksum of the first 8000 words zero
	le.PutUint32(boot[0x28:], 0)
	le.PutUint32(boot[0x28:], -romset.Sum32(boot, romset.BootDataChecksumWords))
	if err := romset.ValidateBootData(boot); err != nil {
		t.Fatal(err)
	}
	bd, _ := romset.ParseBootData(boot)
	s := &romset.Set{Boot: bd, FileVersion: "0260"}
	for k, d := range map[romset.Kind][]byte{romset.PartBootData: boot, romset.PartImFlsh0: im,
		romset.PartGame: game, romset.PartSymbols: sym, romset.PartSoundFlash: sf} {
		s.Parts[k] = &romset.Part{Kind: k, Data: d}
	}
	return s
}

func expectedFlash(s *romset.Set) []byte {
	var b []byte
	for _, k := range []romset.Kind{romset.PartBootData, romset.PartImFlsh0, romset.PartGame, romset.PartSymbols} {
		b = append(b, s.Parts[k].Data...)
	}
	return b
}

func fastOpts() Options {
	return Options{ReplyTimeout: 150 * time.Millisecond, FirstTimeout: 500 * time.Millisecond,
		ResyncDelay: 400 * time.Millisecond, StartDelay: 100 * time.Millisecond}
}

func runUpdate(t *testing.T, s *romset.Set, mode Mode, mf MachineFaults, lf LinkFaults, opt Options) (*Machine, error) {
	t.Helper()
	link := NewSimLink(lf)
	m := NewMachine(MachineConfig{InputTimeout: 300 * time.Millisecond, Faults: mf, Log: t.Logf})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	go m.Run(ctx, link.Machine)
	plan, err := BuildPlan(s, mode)
	if err != nil {
		t.Fatal(err)
	}
	opt.Log = t.Logf
	err = Send(ctx, link.Host, plan, opt)
	// let the emulator finish the EOT handling
	for i := 0; i < 50 && m.LastLoad == "" && err == nil; i++ {
		time.Sleep(20 * time.Millisecond)
	}
	return m, err
}

func checkResult(t *testing.T, m *Machine, s *romset.Set, mode Mode) {
	t.Helper()
	if m.LastLoad != "ok" {
		t.Fatalf("machine: %q", m.LastLoad)
	}
	if mode != ModeSound {
		want := expectedFlash(s)
		got := m.Flash()[:len(want)]
		if !bytes.Equal(got, want) {
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("flash differs at 0x%x", i)
				}
			}
		}
	}
	if mode != ModeGame && !bytes.Equal(m.Sound(), s.Parts[romset.PartSoundFlash].Data) {
		t.Fatal("sound flash differs")
	}
}

func TestPlan(t *testing.T) {
	s := synthSet(t, 1, 50070, 2, 60)
	pl, err := BuildPlan(s, ModeBoth)
	if err != nil {
		t.Fatal(err)
	}
	if len(pl.Sections) != 5 {
		t.Fatalf("sections %d", len(pl.Sections))
	}
	if st := binary.LittleEndian.Uint32(pl.Packets[romset.BootStateOffset/DataSize].Data[romset.BootStateOffset%DataSize:]); st != 0 {
		t.Fatal("boot_state not cleared in transmitted bootdata")
	}
	var seq uint16
	for _, p := range pl.Packets {
		if p.IsMarker() {
			continue
		}
		if p.Block != seq || len(p.Data) == 0 || len(p.Data) > DataSize || len(p.Data)%4 != 0 {
			t.Fatalf("bad packet %d len %d", p.Block, len(p.Data))
		}
		seq++
	}
	enc := Packet{Block: 7, Data: []byte{1, 2, 3, 250}}.Encode()
	if len(enc) != PacketSize || binary.LittleEndian.Uint16(enc[4:]) != Checksum([]byte{1, 2, 3, 250}) || Checksum([]byte{1, 2, 3, 250})+256 != 0 {
		t.Fatal("encoding")
	}
}

func TestTransferClean(t *testing.T) {
	s := synthSet(t, 2, 50070, 2, 60)
	for _, mode := range []Mode{ModeBoth, ModeGame, ModeSound} {
		t.Run(mode.String(), func(t *testing.T) {
			m, err := runUpdate(t, s, mode, MachineFaults{}, LinkFaults{}, fastOpts())
			if err != nil {
				t.Fatal(err)
			}
			checkResult(t, m, s, mode)
		})
	}
}

func TestTransferFaults(t *testing.T) {
	s := synthSet(t, 3, 50070, 2, 60)
	pl, _ := BuildPlan(s, ModeBoth)
	lastGame := pl.Sections[3].First + pl.Sections[3].Count - 1
	cases := map[string]struct {
		mf MachineFaults
		lf LinkFaults
	}{
		"corrupt packets":        {mf: MachineFaults{CorruptPacket: []int{0, 3, 50, 51}}},
		"lost replies":           {mf: MachineFaults{DropReplyTo: []int{0, 5, 20, 21, 90}}},
		"lost reply before SF":   {mf: MachineFaults{DropReplyTo: []int{lastGame}}},
		"lost reply on SF start": {mf: MachineFaults{DropReplyTo: []int{lastGame + 1}}},
		"dropped byte":           {lf: LinkFaults{DropByteAt: []int{PacketSize*7 + 100}}},
		"corrupted byte":         {lf: LinkFaults{CorruptByteAt: []int{PacketSize*9 + 3, PacketSize * 30}}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			m, err := runUpdate(t, s, ModeBoth, c.mf, c.lf, fastOpts())
			if err != nil {
				t.Fatal(err)
			}
			checkResult(t, m, s, ModeBoth)
		})
	}
}

func TestRefusedAndQuery(t *testing.T) {
	s := synthSet(t, 4, 50070, 2, 60)
	_, err := runUpdate(t, s, ModeGame, MachineFaults{GameRunning: true}, LinkFaults{}, fastOpts())
	if !errors.Is(err, ErrRefused) {
		t.Fatalf("want ErrRefused, got %v", err)
	}

	// after an update the machine reports the new version from FLASH
	link := NewSimLink(LinkFaults{})
	old := synthSet(t, 5, 50070, 1, 8)
	m := NewMachine(MachineConfig{ROMBoot: old.Parts[romset.PartBootData].Data, InputTimeout: 300 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx, link.Machine)
	mi, err := QueryMachine(ctx, link.Host)
	if err != nil {
		t.Fatal(err)
	}
	if mi.Location != "PRISM ROM" || mi.GameVersion != "1.8" || mi.GameID != 50070 || mi.System != "1.37" {
		t.Fatalf("before: %+v", mi)
	}
	plan, _ := BuildPlan(s, ModeGame)
	if err := Send(ctx, link.Host, plan, fastOpts()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	mi, err = QueryMachine(ctx, link.Host)
	if err != nil {
		t.Fatal(err)
	}
	if mi.Location != "PRISM FLASH" || mi.GameVersion != "2.60" || mi.BootState != "1" {
		t.Fatalf("after: %+v", mi)
	}
}
