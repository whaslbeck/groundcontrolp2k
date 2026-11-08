// Package is3 reads InstallShield 3 ".Z" archives (signature 0x8C655D13),
// including the multi-volume sets (_SETUP.1, _SETUP.2, ...) that the
// "InstallShield Launcher SE" appends to a single self-extracting EXE.
//
// File data is compressed with PKWARE DCL implode (see package dcl).
package is3

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"path"
	"time"

	"github.com/whaslbeck/groundcontrolp2k/internal/dcl"
)

var signature = []byte{0x13, 0x5d, 0x65, 0x8c, 0x3a, 0x01}

const dataStart = 0xff // file data in every volume starts after the 255-byte header

// File is one entry of an archive set.
type File struct {
	Name     string // path inside the archive, directories joined with "/"
	Size     uint32 // uncompressed size
	CSize    uint32 // compressed size
	Modified time.Time

	set       *Set
	startVol  int
	endVol    int
	offset    uint32 // offset in the start volume
	dirIndex  int
	rawName   string
	dirPrefix string
}

type volume struct {
	data    []byte // archive bytes, starting at the signature
	number  int    // 1-based volume number, 0 for a single archive
	dataEnd uint32 // end of the file data area (= directory table offset)
}

// Set is one archive or one multi-volume archive set.
type Set struct {
	Files []*File
	vols  []*volume
}

type header struct {
	count     uint16
	dosTime   uint32
	totalVols byte
	volNumber byte
	dirOff    uint32
	dirSize   uint32
	dirCount  uint16
	fileOff   uint32
	fileSize  uint32
}

func parseHeader(b []byte) (header, error) {
	if len(b) < dataStart || !bytes.HasPrefix(b, signature) {
		return header{}, errors.New("is3: bad signature")
	}
	le := binary.LittleEndian
	return header{
		count:     le.Uint16(b[0x0c:]),
		dosTime:   le.Uint32(b[0x0e:]),
		totalVols: b[0x1e],
		volNumber: b[0x1f],
		dirOff:    le.Uint32(b[0x29:]),
		dirSize:   le.Uint32(b[0x2d:]),
		dirCount:  le.Uint16(b[0x31:]),
		fileOff:   le.Uint32(b[0x33:]),
		fileSize:  le.Uint32(b[0x37:]),
	}, nil
}

func dosDateTime(v uint32) time.Time {
	d, t := uint16(v), uint16(v>>16)
	return time.Date(1980+int(d>>9), time.Month(d>>5&15), int(d&31),
		int(t>>11), int(t>>5&63), int(t&31)*2, 0, time.Local)
}

// Find scans b (typically a whole Setup EXE) for all embedded IS3 archives
// and groups multi-volume archives into sets.
func Find(b []byte) ([]*Set, error) {
	var sets []*Set
	var cur *Set
	var curTime uint32
	for i := 0; ; {
		j := bytes.Index(b[i:], signature)
		if j < 0 {
			break
		}
		start := i + j
		h, err := parseHeader(b[start:])
		if err != nil || h.fileOff == 0 || int(h.fileOff+h.fileSize) > len(b)-start {
			i = start + 1
			continue
		}
		end := start + int(h.fileOff+h.fileSize)
		v := &volume{data: b[start:end], number: int(h.volNumber), dataEnd: h.dirOff}
		if h.volNumber <= 1 || cur == nil || h.dosTime != curTime {
			cur = &Set{}
			curTime = h.dosTime
			sets = append(sets, cur)
			cur.vols = append(cur.vols, v)
			if err := cur.parseTables(v.data, h); err != nil {
				return nil, err
			}
		} else {
			cur.vols = append(cur.vols, v)
		}
		i = end
	}
	if len(sets) == 0 {
		return nil, errors.New("is3: no InstallShield 3 archive found")
	}
	return sets, nil
}

func (s *Set) parseTables(b []byte, h header) error {
	le := binary.LittleEndian
	// directory table: u16 fileCount, u16 entrySize, u16 nameLen, name
	var dirs []string
	p := int(h.dirOff)
	for i := 0; i < int(h.dirCount); i++ {
		if p+6 > len(b) {
			return errors.New("is3: truncated directory table")
		}
		esz := int(le.Uint16(b[p+2:]))
		nl := int(le.Uint16(b[p+4:]))
		if esz == 0 || p+6+nl > len(b) {
			return errors.New("is3: bad directory entry")
		}
		dirs = append(dirs, string(b[p+6:p+6+nl]))
		p += esz
	}
	// file table
	p = int(h.fileOff)
	for i := 0; i < int(h.count); i++ {
		if p+30 > len(b) {
			return errors.New("is3: truncated file table")
		}
		e := b[p:]
		esz := int(le.Uint16(e[23:]))
		nl := int(e[29])
		if esz < 30+nl || p+esz > len(b) {
			return errors.New("is3: bad file entry")
		}
		f := &File{
			set:      s,
			endVol:   int(e[0]),
			dirIndex: int(le.Uint16(e[1:])),
			Size:     le.Uint32(e[3:]),
			CSize:    le.Uint32(e[7:]),
			offset:   le.Uint32(e[11:]),
			Modified: dosDateTime(le.Uint32(e[15:])),
			startVol: int(e[28]),
			rawName:  string(e[30 : 30+nl]),
		}
		if f.dirIndex < len(dirs) {
			f.dirPrefix = dirs[f.dirIndex]
		}
		f.Name = f.rawName
		if f.dirPrefix != "" {
			f.Name = path.Join(f.dirPrefix, f.rawName)
		}
		s.Files = append(s.Files, f)
		p += esz
	}
	return nil
}

func (s *Set) volume(n int) *volume {
	for _, v := range s.vols {
		if v.number == n {
			return v
		}
	}
	if n <= 1 && len(s.vols) == 1 {
		return s.vols[0]
	}
	return nil
}

// Compressed returns the raw (DCL-compressed) bytes of f, joining the parts
// of a file that spans several volumes.
func (f *File) Compressed() ([]byte, error) {
	sv, ev := f.startVol, f.endVol
	if sv == 0 {
		sv = ev
	}
	v := f.set.volume(sv)
	if v == nil {
		return nil, fmt.Errorf("is3: %s: volume %d missing", f.Name, sv)
	}
	need := int(f.CSize)
	if sv == ev || ev == 0 {
		end := int(f.offset) + need
		if end > len(v.data) {
			return nil, fmt.Errorf("is3: %s: data out of range", f.Name)
		}
		return v.data[f.offset:end], nil
	}
	out := make([]byte, 0, need)
	end := int(v.dataEnd)
	if end > len(v.data) || int(f.offset) > end {
		return nil, fmt.Errorf("is3: %s: bad split offset", f.Name)
	}
	out = append(out, v.data[f.offset:end]...)
	for n := sv + 1; n <= ev && len(out) < need; n++ {
		nv := f.set.volume(n)
		if nv == nil {
			return nil, fmt.Errorf("is3: %s: volume %d missing", f.Name, n)
		}
		take := need - len(out)
		if avail := int(nv.dataEnd) - dataStart; take > avail {
			take = avail
		}
		out = append(out, nv.data[dataStart:dataStart+take]...)
	}
	if len(out) != need {
		return nil, fmt.Errorf("is3: %s: short data (%d of %d)", f.Name, len(out), need)
	}
	return out, nil
}

// Data returns the decompressed content of f.
func (f *File) Data() ([]byte, error) {
	c, err := f.Compressed()
	if err != nil {
		return nil, err
	}
	d, err := dcl.Explode(c, int(f.Size))
	if err != nil {
		return nil, fmt.Errorf("is3: %s: %w", f.Name, err)
	}
	if len(d) != int(f.Size) {
		return nil, fmt.Errorf("is3: %s: size mismatch (%d, want %d)", f.Name, len(d), f.Size)
	}
	return d, nil
}
