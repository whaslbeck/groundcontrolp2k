package romset

import (
	"bytes"
	"crypto/md5"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Kind identifies one file of an update ROM set.
type Kind int

// The parts of an update. The first four form the update FLASH image,
// SoundFlash goes to the DCS sound FLASH, PubBoot is only used by the
// PRISM Update Board (ISA card) and is never sent over the serial line.
const (
	PartBootData Kind = iota
	PartImFlsh0
	PartGame
	PartSymbols
	PartSoundFlash
	PartPubBoot
	numKinds
)

var kindNames = [numKinds]string{"bootdata", "im_flsh0", "game", "symbols", "sf", "pubboot"}

func (k Kind) String() string { return kindNames[k] }

// FileName is the canonical file name for a part ("game.rom").
func (k Kind) FileName() string { return kindNames[k] + ".rom" }

// SoundFlashSize is the fixed size of sf.rom.
const SoundFlashSize = 1 << 20

// Part is one ROM file of a set.
type Part struct {
	Kind   Kind
	Name   string // original file name
	Origin string // where it came from (container and path)
	Data   []byte
	// Note explains a part that did not come with the set itself.
	Note string
}

// MD5 returns the hex MD5 of the part data.
func (p *Part) MD5() string {
	h := md5.Sum(p.Data)
	return hex.EncodeToString(h[:])
}

// Set is one complete or partial update.
type Set struct {
	Source      string // container (file or directory) the set was found in
	Prefix      string // file name prefix, e.g. "pin2000_50070_0260_"
	FileGameID  string // game id from the file name ("50070"), may be empty
	FileVersion string // version from the file name ("0260"), may be empty
	Boot        *BootData
	Parts       [numKinds]*Part
	GameNames   map[uint32]string // from gamelist.txt, may be nil
}

// Part returns the part of kind k or nil.
func (s *Set) Part(k Kind) *Part { return s.Parts[k] }

// knownGames is the gamelist.txt shipped with the later updates.
var knownGames = map[uint32]string{
	50000: "XINA Game",
	50069: "Star Wars Episode I",
	50070: "Revenge From Mars",
	50071: "Playboy",
	50072: "Wizard Blocks",
	60077: "Test Fixture",
}

// GameName returns a human readable name for a game id.
func GameName(id uint32, extra map[uint32]string) string {
	if n, ok := extra[id]; ok {
		return n
	}
	if n, ok := knownGames[id]; ok {
		return n
	}
	return fmt.Sprintf("Game %d", id)
}

// GameID returns the game id from bootdata, or from the file name.
func (s *Set) GameID() uint32 {
	if s.Boot != nil {
		return s.Boot.GameID
	}
	v, _ := strconv.Atoi(s.FileGameID)
	return uint32(v)
}

// GameName returns the game title.
func (s *Set) GameName() string { return GameName(s.GameID(), s.GameNames) }

// Version returns the release version, preferring the file name ("0191"
// -> "1.91") because bootdata only stores major/minor as integers and
// several releases share the same values (1.90, 1.91, 1.95 all say 1.9).
func (s *Set) Version() string {
	if len(s.FileVersion) == 4 {
		return strings.TrimLeft(s.FileVersion[:2], "0") + "." + s.FileVersion[2:]
	}
	if s.Boot != nil {
		return s.Boot.GameVersion()
	}
	return "?"
}

// Label is a short one-line description.
func (s *Set) Label() string {
	return fmt.Sprintf("%s %s", s.GameName(), s.Version())
}

// Status of a part after validation.
type Status int

const (
	StatusOK Status = iota
	StatusMissing
	StatusBad
	StatusUnchecked // present but there is no checksum to verify against
)

func (st Status) String() string {
	return [...]string{"OK", "missing", "BAD", "present"}[st]
}

// Check is the validation result of one part.
type Check struct {
	Kind   Kind
	Status Status
	Detail string
}

// wordsOf returns the size (in words) and checksum recorded in bootdata.
func (s *Set) wordsOf(k Kind) (uint32, uint32, bool) {
	if s.Boot == nil {
		return 0, 0, false
	}
	switch k {
	case PartImFlsh0:
		return s.Boot.ImSys0Size, s.Boot.ImSys0Checksum, true
	case PartGame:
		return s.Boot.GameCodeSize, s.Boot.GameCodeChecksum, true
	case PartSymbols:
		return s.Boot.SymbolsSize, s.Boot.SymbolsChecksum, true
	}
	return 0, 0, false
}

// Expected returns the byte length and checksum bootdata expects for k.
func (s *Set) Expected(k Kind) (size int, sum uint32, ok bool) {
	w, c, ok := s.wordsOf(k)
	return int(w) * 4, c, ok
}

// CheckPart validates one part against bootdata.
func (s *Set) CheckPart(k Kind) Check {
	p := s.Parts[k]
	if p == nil {
		return Check{Kind: k, Status: StatusMissing}
	}
	switch k {
	case PartBootData:
		if err := ValidateBootData(p.Data); err != nil {
			return Check{k, StatusBad, err.Error()}
		}
		return Check{k, StatusOK, "checksum ok"}
	case PartImFlsh0, PartGame, PartSymbols:
		w, sum, ok := s.wordsOf(k)
		if !ok {
			return Check{k, StatusUnchecked, "no bootdata"}
		}
		if len(p.Data) < int(w)*4 {
			return Check{k, StatusBad, fmt.Sprintf("too short: %d of %d bytes", len(p.Data), w*4)}
		}
		if got := Sum32(p.Data, w); got != sum {
			return Check{k, StatusBad, fmt.Sprintf("checksum 0x%08x, want 0x%08x", got, sum)}
		}
		return Check{k, StatusOK, "checksum ok"}
	case PartSoundFlash:
		if len(p.Data) != SoundFlashSize {
			return Check{k, StatusBad, fmt.Sprintf("size %d, must be %d", len(p.Data), SoundFlashSize)}
		}
		return Check{k, StatusUnchecked, "md5 " + p.MD5()[:8]}
	default:
		return Check{k, StatusUnchecked, ""}
	}
}

// Checks validates all parts.
func (s *Set) Checks() []Check {
	var out []Check
	for k := PartBootData; k < numKinds; k++ {
		out = append(out, s.CheckPart(k))
	}
	return out
}

// GameUpdateReady reports whether the game (FLASH image) part can be sent.
func (s *Set) GameUpdateReady() error {
	for _, k := range []Kind{PartBootData, PartImFlsh0, PartGame, PartSymbols} {
		if c := s.CheckPart(k); c.Status != StatusOK {
			return fmt.Errorf("%s: %s %s", k.FileName(), c.Status, c.Detail)
		}
	}
	return nil
}

// SoundUpdateReady reports whether sf.rom can be sent.
func (s *Set) SoundUpdateReady() error {
	c := s.CheckPart(PartSoundFlash)
	if c.Status == StatusMissing || c.Status == StatusBad {
		return fmt.Errorf("sf.rom: %s %s", c.Status, c.Detail)
	}
	return nil
}

// Symbol table support ------------------------------------------------------

// LookupSymbol finds an address in a *_symbols.rom table.
func LookupSymbol(sym []byte, name string) (uint32, bool) {
	if len(sym) < 0x18 || !bytes.HasPrefix(sym, []byte("SYMBOL TABLE")) {
		return 0, false
	}
	le := binary.LittleEndian
	count := le.Uint32(sym[0x10:])
	names := 0x18 + int(count)*8
	if names > len(sym) {
		return 0, false
	}
	for i := 0; i < int(count); i++ {
		addr := le.Uint32(sym[0x18+i*8:])
		p := names + int(le.Uint32(sym[0x1c+i*8:]))
		if p >= len(sym) {
			continue
		}
		e := bytes.IndexByte(sym[p:], 0)
		if e < 0 {
			continue
		}
		if string(sym[p:p+e]) == name {
			return addr, true
		}
	}
	return 0, false
}

// XinaVersion returns the operating system version string compiled into
// game.rom ("1.36 - Thu Aug 08 10:23:00 2024"), found via the symbol table.
func (s *Set) XinaVersion() string {
	g, sy := s.Parts[PartGame], s.Parts[PartSymbols]
	if g == nil || sy == nil || s.Boot == nil {
		return ""
	}
	addr, ok := LookupSymbol(sy.Data, "xina_version")
	if !ok {
		return ""
	}
	reloc := s.Boot.GameReloc
	ptrOff := int(addr) - int(reloc)
	if ptrOff < 0 || ptrOff+4 > len(g.Data) {
		return ""
	}
	strOff := int(binary.LittleEndian.Uint32(g.Data[ptrOff:])) - int(reloc)
	if strOff < 0 || strOff >= len(g.Data) {
		return ""
	}
	e := bytes.IndexByte(g.Data[strOff:], 0)
	if e < 0 || e > 80 {
		return ""
	}
	return string(g.Data[strOff : strOff+e])
}

// File name parsing ---------------------------------------------------------

var romName = regexp.MustCompile(`(?i)^(.*?)(bootdata|im_flsh0|im_sys0|game|symbols|sf|pubboot)\.rom$`)
var prefixInfo = regexp.MustCompile(`(?i)pin2000_(\d{5})_(\d{4})_(?:dump_)?$`)

// ParseRomName splits "pin2000_50070_0260_game.rom" into prefix and kind.
func ParseRomName(name string) (prefix string, k Kind, ok bool) {
	m := romName.FindStringSubmatch(name)
	if m == nil {
		return "", 0, false
	}
	switch strings.ToLower(m[2]) {
	case "bootdata":
		k = PartBootData
	case "im_flsh0", "im_sys0":
		k = PartImFlsh0
	case "game":
		k = PartGame
	case "symbols":
		k = PartSymbols
	case "sf":
		k = PartSoundFlash
	case "pubboot":
		k = PartPubBoot
	}
	return m[1], k, true
}

// PrefixInfo extracts game id and version from a prefix.
func PrefixInfo(prefix string) (game, version string) {
	if m := prefixInfo.FindStringSubmatch(prefix); m != nil {
		return m[1], m[2]
	}
	return "", ""
}

// ParseGameList reads a gamelist.txt (first line is a date).
func ParseGameList(b []byte) map[uint32]string {
	out := map[uint32]string{}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		f := strings.SplitN(line, " ", 2)
		if len(f) != 2 {
			continue
		}
		id, err := strconv.Atoi(f[0])
		if err != nil || id < 10000 {
			continue
		}
		out[uint32(id)] = strings.TrimSpace(f[1])
	}
	return out
}
