// Package romset models a Pinball 2000 update ROM set (bootdata, im_flsh0,
// game, symbols, sf, pubboot) and validates it the way the machine does.
package romset

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
)

// BootDataSize is the size of bootdata.rom in bytes.
const BootDataSize = 32768

// BootDataChecksumWords is the number of uint32 words covered by the
// bootdata checksum. boot_state follows them.
const BootDataChecksumWords = 8000

// BootStateOffset is the byte offset of BootData.boot_state.
const BootStateOffset = BootDataChecksumWords * 4

// MagicCookie identifies a valid bootdata structure.
const MagicCookie = "13Dec96"

// MaskBank is the size/checksum pair of one PRISM image mask ROM bank.
type MaskBank struct {
	Size     uint32 // in uint32 words
	Checksum uint32
}

// BootData is the decoded bootdata.rom. Field names follow the names the
// machine prints in its "bootdata" shell command. All sizes are in uint32 words.
type BootData struct {
	Timestamp        string
	VersionMajor     uint16
	VersionMinor     uint16
	Size             uint32
	Checksum         uint32
	ImSys0Size       uint32
	ImSys0Checksum   uint32
	SystemMajor      uint32
	SystemMinor      uint32
	GameID           uint32
	GameMajor        uint32
	GameMinor        uint32
	GameReloc        uint32
	GameCodeSize     uint32
	GameCodeChecksum uint32
	SymbolsSize      uint32
	SymbolsChecksum  uint32
	ImMask           [4]MaskBank
	MagicCookie      string
	BootState        uint32
}

// ParseBootData decodes a bootdata.rom image.
func ParseBootData(b []byte) (*BootData, error) {
	if len(b) < BootDataSize {
		return nil, fmt.Errorf("bootdata: %d bytes, want %d", len(b), BootDataSize)
	}
	le := binary.LittleEndian
	w := func(off int) uint32 { return le.Uint32(b[off:]) }
	bd := &BootData{
		Timestamp:        cString(b[0:32]),
		VersionMajor:     le.Uint16(b[0x20:]),
		VersionMinor:     le.Uint16(b[0x22:]),
		Size:             w(0x24),
		Checksum:         w(0x28),
		ImSys0Size:       w(0x2c),
		ImSys0Checksum:   w(0x30),
		SystemMajor:      w(0x34),
		SystemMinor:      w(0x38),
		GameID:           w(0x3c),
		GameMajor:        w(0x40),
		GameMinor:        w(0x44),
		GameReloc:        w(0x48),
		GameCodeSize:     w(0x4c),
		GameCodeChecksum: w(0x50),
		SymbolsSize:      w(0x54),
		SymbolsChecksum:  w(0x58),
		MagicCookie:      cString(b[0x7c:0x84]),
		BootState:        w(BootStateOffset),
	}
	for i := range bd.ImMask {
		bd.ImMask[i] = MaskBank{Size: w(0x5c + 8*i), Checksum: w(0x60 + 8*i)}
	}
	return bd, nil
}

// Validate replicates boot_data_is_valid() from the game ROM.
func ValidateBootData(b []byte) error {
	if len(b) < BootDataSize {
		return errors.New("bootdata: file too short")
	}
	le := binary.LittleEndian
	if le.Uint16(b[0x20:]) == 0 {
		return errors.New("bootdata: version is 0")
	}
	if le.Uint32(b[0x24:]) != BootDataSize/4 {
		return errors.New("bootdata: wrong structure size")
	}
	if cString(b[0x7c:0x84]) != MagicCookie {
		return errors.New("bootdata: magic cookie missing")
	}
	if Sum32(b, BootDataChecksumWords) != 0 {
		return errors.New("bootdata: checksum mismatch")
	}
	return nil
}

// Sum32 is the machine's checksum: the 32-bit sum of the first n
// little-endian uint32 words of b (missing bytes count as zero).
func Sum32(b []byte, n uint32) uint32 {
	var s uint32
	end := int(n) * 4
	full := min(end, len(b)&^3)
	for i := 0; i < full; i += 4 {
		s += binary.LittleEndian.Uint32(b[i:])
	}
	if full < end && full < len(b) {
		var tail [4]byte
		copy(tail[:], b[full:])
		s += binary.LittleEndian.Uint32(tail[:])
	}
	return s
}

// SystemVersion formats the XINA system version exactly as the machine's
// "bootdata" shell command does.
func (bd *BootData) SystemVersion() string {
	if bd.VersionMajor == 1 && bd.VersionMinor == 0 {
		return fmt.Sprintf("%d.%d", bd.SystemMajor, bd.SystemMinor)
	}
	return asciiDigits(bd.SystemMajor, true) + "." + asciiDigits(bd.SystemMinor, false)
}

// asciiDigits decodes the two-character version fields used by later
// bootdata versions ("1 " -> "1", "37" -> "37").
func asciiDigits(v uint32, major bool) string {
	hi, lo := byte(v>>8), byte(v)
	isDigit := func(c byte) bool { return c >= '0' && c <= '9' }
	var s []byte
	if major {
		if isDigit(hi) {
			s = append(s, hi)
		}
		if isDigit(lo) {
			s = append(s, lo)
		} else {
			s = append(s, '-')
		}
		return string(s)
	}
	if isDigit(hi) {
		s = append(s, hi)
	} else {
		s = append(s, '-')
	}
	if isDigit(lo) {
		s = append(s, lo)
	}
	return string(s)
}

// GameVersion formats game_major.game_minor as the machine reports it
// (e.g. "1.8" for the 1.80 release, "2.60" for 2.60).
func (bd *BootData) GameVersion() string {
	return fmt.Sprintf("%d.%d", bd.GameMajor, bd.GameMinor)
}

func cString(b []byte) string {
	if i := strings.IndexByte(string(b), 0); i >= 0 {
		b = b[:i]
	}
	return strings.TrimSpace(string(b))
}
