// Package dcl decompresses data in the PKWARE Data Compression Library
// "implode" format (also called DCL or "blast"), as used by InstallShield 3.
//
// It is a Go port of the algorithm in zlib's contrib/blast/blast.c by Mark Adler.
package dcl

import (
	"errors"
	"io"
)

const maxBits = 13

// Errors returned by Explode.
var (
	ErrLiteralFlag = errors.New("dcl: invalid literal mode byte")
	ErrDictSize    = errors.New("dcl: invalid dictionary size")
	ErrDistance    = errors.New("dcl: distance too far back")
	ErrCode        = errors.New("dcl: ran out of codes")
	ErrTruncated   = errors.New("dcl: unexpected end of input")
)

type huffman struct {
	count  [maxBits + 1]int
	symbol []int
}

// construct builds a canonical Huffman table from the compact
// run-length representation used by blast.c.
func construct(rep []byte, nsym int) *huffman {
	h := &huffman{symbol: make([]int, nsym)}
	length := make([]int, 0, nsym)
	for _, r := range rep {
		l := int(r & 15)
		for n := int(r>>4) + 1; n > 0; n-- {
			length = append(length, l)
		}
	}
	for _, l := range length {
		h.count[l]++
	}
	var offs [maxBits + 1]int
	for l := 1; l < maxBits; l++ {
		offs[l+1] = offs[l] + h.count[l]
	}
	for s, l := range length {
		if l != 0 {
			h.symbol[offs[l]] = s
			offs[l]++
		}
	}
	return h
}

var (
	litCode = construct([]byte{
		11, 124, 8, 7, 28, 7, 188, 13, 76, 4, 10, 8, 12, 10, 12, 10, 8, 23, 8,
		9, 7, 6, 7, 8, 7, 6, 55, 8, 23, 24, 12, 11, 7, 9, 11, 12, 6, 7, 22, 5,
		7, 24, 6, 11, 9, 6, 7, 22, 7, 11, 38, 7, 9, 8, 25, 11, 8, 11, 9, 12,
		8, 12, 5, 38, 5, 38, 5, 11, 7, 5, 6, 21, 6, 10, 53, 8, 7, 24, 10, 27,
		44, 253, 253, 253, 252, 252, 252, 13, 12, 45, 12, 45, 12, 61, 12, 45,
		44, 173}, 256)
	lenCode  = construct([]byte{2, 35, 36, 53, 38, 23}, 16)
	distCode = construct([]byte{2, 20, 53, 230, 247, 151, 248}, 64)

	lenBase  = [16]int{3, 2, 4, 5, 6, 7, 8, 9, 10, 12, 16, 24, 40, 72, 136, 264}
	lenExtra = [16]uint{0, 0, 0, 0, 0, 0, 0, 0, 1, 2, 3, 4, 5, 6, 7, 8}
)

type state struct {
	in     []byte
	pos    int
	bitbuf uint32
	bitcnt uint
}

func (s *state) bits(need uint) (int, error) {
	for s.bitcnt < need {
		if s.pos >= len(s.in) {
			return 0, ErrTruncated
		}
		s.bitbuf |= uint32(s.in[s.pos]) << s.bitcnt
		s.pos++
		s.bitcnt += 8
	}
	v := s.bitbuf & (1<<need - 1)
	s.bitbuf >>= need
	s.bitcnt -= need
	return int(v), nil
}

// decode reads one symbol. PKWARE stores Huffman codes bit-inverted.
func (s *state) decode(h *huffman) (int, error) {
	code, first, index := 0, 0, 0
	for l := 1; l <= maxBits; l++ {
		b, err := s.bits(1)
		if err != nil {
			return 0, err
		}
		code |= b ^ 1
		count := h.count[l]
		if code < first+count {
			return h.symbol[index+code-first], nil
		}
		index += count
		first = (first + count) << 1
		code <<= 1
	}
	return 0, ErrCode
}

// Explode decompresses a complete DCL stream. sizeHint (may be 0)
// pre-sizes the output buffer.
func Explode(in []byte, sizeHint int) ([]byte, error) {
	s := &state{in: in}
	out := make([]byte, 0, sizeHint)
	lit, err := s.bits(8)
	if err != nil {
		return nil, err
	}
	if lit > 1 {
		return nil, ErrLiteralFlag
	}
	dict, err := s.bits(8)
	if err != nil {
		return nil, err
	}
	if dict < 4 || dict > 6 {
		return nil, ErrDictSize
	}
	for {
		flag, err := s.bits(1)
		if err != nil {
			return out, err
		}
		if flag == 1 {
			sym, err := s.decode(lenCode)
			if err != nil {
				return out, err
			}
			ext, err := s.bits(lenExtra[sym])
			if err != nil {
				return out, err
			}
			length := lenBase[sym] + ext
			if length == 519 { // end code
				return out, nil
			}
			shift := uint(dict)
			if length == 2 {
				shift = 2
			}
			d, err := s.decode(distCode)
			if err != nil {
				return out, err
			}
			low, err := s.bits(shift)
			if err != nil {
				return out, err
			}
			dist := d<<shift + low + 1
			if dist > len(out) {
				return out, ErrDistance
			}
			from := len(out) - dist
			for i := range length {
				out = append(out, out[from+i])
			}
		} else {
			var sym int
			if lit == 1 {
				sym, err = s.decode(litCode)
			} else {
				sym, err = s.bits(8)
			}
			if err != nil {
				return out, err
			}
			out = append(out, byte(sym))
		}
	}
}

// ExplodeReader is a convenience wrapper reading all of r.
func ExplodeReader(r io.Reader, sizeHint int) ([]byte, error) {
	in, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	return Explode(in, sizeHint)
}
