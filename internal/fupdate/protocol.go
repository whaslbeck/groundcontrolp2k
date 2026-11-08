// Package fupdate implements the Pinball 2000 serial FLASH update protocol
// ("fupdate"), both the PC side (sender) and an emulation of the machine
// side (receiver) for testing.
//
// The protocol was analysed for interoperability with the machine and the
// original update files. See docs/PROTOCOL.md.
package fupdate

import (
	"encoding/binary"
	"fmt"
	"time"

	"github.com/whaslbeck/groundcontrolp2k/internal/romset"
)

// Protocol constants.
const (
	DataSize   = 512            // maximum payload per packet
	PacketSize = 6 + DataSize   // sizeof(FUpdateDataPacket)
	ReplySize  = 4              // sizeof(FUpdateReplyPacket) incl. padding
	BlockSF    = uint16(0xFFFE) // marker: switch to sound FLASH
	BlockEOT   = uint16(0xFFFF) // marker: end of transfer
	TypeACK    = 6
	TypeNAK    = 21
	MaxFlash   = 4 << 20 // update FLASH size checked by the receiver
	ShellBaud  = 9600    // the XINA shell on COM1
)

// Baud rates accepted by "fupdate load".
var BaudRates = []int{9600, 19200, 38400, 57600, 115200}

// ValidBaud reports whether b is accepted by the machine.
func ValidBaud(b int) bool {
	for _, x := range BaudRates {
		if x == b {
			return true
		}
	}
	return false
}

// Mode selects what is updated.
type Mode int

const (
	ModeGame  Mode = iota // game program only
	ModeSound             // sf.rom only
	ModeBoth              // game program, then sf.rom
)

func (m Mode) String() string {
	return [...]string{"game", "sound", "game+sound"}[m]
}

// ParseMode accepts game, sound, both (and a few aliases).
func ParseMode(s string) (Mode, error) {
	switch s {
	case "game", "gfonly", "program":
		return ModeGame, nil
	case "sound", "sfonly", "sf":
		return ModeSound, nil
	case "both", "all", "game+sound":
		return ModeBoth, nil
	}
	return 0, fmt.Errorf("unknown mode %q (game, sound, both)", s)
}

// ShellCommand returns the command that starts the receiver on the machine.
// The original tool always tells the machine to use com1.
func ShellCommand(baud int, m Mode) string {
	cmd := fmt.Sprintf("fupdate load com1 %d", baud)
	switch m {
	case ModeSound:
		cmd += " sfonly"
	case ModeBoth:
		cmd += " sf"
	}
	return cmd
}

// Packet is one data packet (PC to machine).
type Packet struct {
	Block uint16
	Data  []byte // at most DataSize bytes
}

// IsMarker reports whether the packet is a control packet whose block
// number is not checked against the sequence (SF start, EOT).
func (p Packet) IsMarker() bool { return p.Block == BlockSF || p.Block == BlockEOT }

// Checksum is the negated 16-bit sum of the data bytes.
func Checksum(data []byte) uint16 {
	var s uint16
	for _, b := range data {
		s += uint16(b)
	}
	return -s
}

// Encode serialises the packet: block, length, checksum (little endian),
// then 512 data bytes padded with zeros.
func (p Packet) Encode() []byte {
	b := make([]byte, PacketSize)
	binary.LittleEndian.PutUint16(b[0:], p.Block)
	binary.LittleEndian.PutUint16(b[2:], uint16(len(p.Data)))
	binary.LittleEndian.PutUint16(b[4:], Checksum(p.Data))
	copy(b[6:], p.Data)
	return b
}

// Reply is the 4 byte answer of the machine: type, one padding byte, block.
type Reply struct {
	Type  byte
	Block uint16
}

// DecodeReply parses a 4 byte reply.
func DecodeReply(b []byte) Reply {
	return Reply{Type: b[0], Block: binary.LittleEndian.Uint16(b[2:])}
}

// Encode serialises a reply.
func (r Reply) Encode() []byte {
	b := make([]byte, ReplySize)
	b[0] = r.Type
	binary.LittleEndian.PutUint16(b[2:], r.Block)
	return b
}

// Section is a contiguous range of packets belonging to one file.
type Section struct {
	Name  string
	First int // index of the first packet in Plan.Packets
	Count int
	Bytes int
}

// Plan is the complete packet sequence for one update (without EOT).
type Plan struct {
	Mode     Mode
	Packets  []Packet
	Sections []Section
	Bytes    int // payload bytes
}

// DataPackets returns the number of non-marker packets.
func (pl *Plan) DataPackets() int {
	n := 0
	for _, p := range pl.Packets {
		if !p.IsMarker() {
			n++
		}
	}
	return n
}

// Estimate returns the approximate transfer time at a baud rate
// (8N1 = 10 bits per byte, plus reply and turnaround).
func (pl *Plan) Estimate(baud int) time.Duration {
	bytes := len(pl.Packets) * (PacketSize + ReplySize)
	return time.Duration(float64(bytes*10)/float64(baud)*float64(time.Second)) +
		time.Duration(len(pl.Packets))*2*time.Millisecond
}

// BuildPlan creates the packet sequence exactly like the original
// fupdate.exe: bootdata (with boot_state cleared), im_flsh0, game, symbols
// (each split into 512 byte packets, the last packet of a file may be
// shorter), then the SF marker followed by sf.rom.
func BuildPlan(s *romset.Set, m Mode) (*Plan, error) {
	pl := &Plan{Mode: m}
	var block uint16
	add := func(name string, data []byte) {
		sec := Section{Name: name, First: len(pl.Packets), Bytes: len(data)}
		for off := 0; off < len(data); off += DataSize {
			end := min(off+DataSize, len(data))
			pl.Packets = append(pl.Packets, Packet{Block: block, Data: data[off:end]})
			block++
		}
		sec.Count = len(pl.Packets) - sec.First
		pl.Sections = append(pl.Sections, sec)
		pl.Bytes += len(data)
	}

	if m == ModeGame || m == ModeBoth {
		if err := s.GameUpdateReady(); err != nil {
			return nil, err
		}
		boot := append([]byte(nil), s.Parts[romset.PartBootData].Data[:romset.BootDataSize]...)
		// Send the image disabled; the machine enables it itself after a
		// successful transfer. boot_state is not covered by the bootdata
		// checksum.
		binary.LittleEndian.PutUint32(boot[romset.BootStateOffset:], 0)
		add(romset.PartBootData.FileName(), boot)
		for _, k := range []romset.Kind{romset.PartImFlsh0, romset.PartGame, romset.PartSymbols} {
			size, _, _ := s.Expected(k)
			add(k.FileName(), s.Parts[k].Data[:size])
		}
		if pl.Bytes > MaxFlash {
			return nil, fmt.Errorf("update image is %d bytes, the update FLASH holds %d", pl.Bytes, MaxFlash)
		}
	}
	if m == ModeSound || m == ModeBoth {
		if err := s.SoundUpdateReady(); err != nil {
			return nil, err
		}
		pl.Packets = append(pl.Packets, Packet{Block: BlockSF})
		add(romset.PartSoundFlash.FileName(), s.Parts[romset.PartSoundFlash].Data)
	}
	return pl, nil
}
