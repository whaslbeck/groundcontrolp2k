package fupdate

import (
	"context"
	"fmt"
	"math/bits"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ROM test: repeated verification of the PRISM graphics mask ROMs.
//
// "bootdata rom verify" makes the machine add up the four graphics ROM
// banks (im_mask0..3) and compare the sums with bootdata. A bank that is
// sometimes OK and sometimes BAD points to a hardware fault: a loose ROM,
// a bad socket, a weak data line, or an unstable supply. When the machine
// prints the wrong sum, the bits in which it differs from the expected
// one show which data lines are affected: a 32-bit sum only changes in
// its upper bits when the errors are in the upper bits of the data words.

// MaskBanks is the number of graphics ROM banks.
const MaskBanks = 4

// VerifyRun is the result of one verification.
type VerifyRun struct {
	Banks    [MaskBanks]string // "OK", "BAD" or "" (not reported)
	Got      [MaskBanks]uint32 // wrong sum the machine printed (0 = none)
	Want     [MaskBanks]uint32 // expected sum (0 = none)
	Files    map[string]string // "im_sys0", "game code", "symbol table" -> OK/BAD
	Garbled  int               // characters that are not plain text: disturbed serial output
	Complete bool              // the answer ended with the shell prompt
	Raw      string
}

var (
	reBankIs  = regexp.MustCompile(`im_mask\[(\d)\] is:\s+(OK|BAD)`)
	reBanksAr = regexp.MustCompile(`im_mask\[0-3\] are:\s+(OK|BAD|not verified)`)
	reMismach = regexp.MustCompile(`im_mask(\d):\s*0x([0-9a-fA-F]{1,8})\s*!=\s*0x([0-9a-fA-F]{1,8})`)
	reFile    = regexp.MustCompile(`(im_sys0|game code|symbol table) is:\s+(OK|BAD)`)
)

// ParseVerify reads the answer of "bootdata <rom|flash|current> verify".
// Firmware before 1.20 prints one line for all banks ("im_mask[0-3] are")
// plus the first wrong sum; later firmware prints one line per bank.
func ParseVerify(raw string) VerifyRun {
	r := VerifyRun{Files: map[string]string{}, Raw: raw}
	for _, c := range raw {
		if c != '\r' && c != '\n' && c != '\t' && (c < 32 || c > 126) {
			r.Garbled++
		}
	}
	text := sanitize(raw)
	r.Complete = strings.HasSuffix(strings.TrimRight(text, " "), "%")
	for _, m := range reMismach.FindAllStringSubmatch(text, -1) {
		b, _ := strconv.Atoi(m[1])
		if b < MaskBanks {
			got, _ := strconv.ParseUint(m[2], 16, 32)
			want, _ := strconv.ParseUint(m[3], 16, 32)
			r.Got[b], r.Want[b] = uint32(got), uint32(want)
			r.Banks[b] = "BAD"
		}
	}
	for _, m := range reBankIs.FindAllStringSubmatch(text, -1) {
		b, _ := strconv.Atoi(m[1])
		if b < MaskBanks {
			r.Banks[b] = m[2]
		}
	}
	if m := reBanksAr.FindStringSubmatch(text); m != nil {
		for b := range r.Banks {
			switch {
			case m[1] == "OK":
				r.Banks[b] = "OK"
			case m[1] == "BAD" && r.Banks[b] == "":
				// older firmware stops at the first bad bank: the banks
				// before it are fine, the ones after it unchecked
				first := -1
				for i := range r.Got {
					if r.Got[i] != 0 {
						first = i
						break
					}
				}
				if first >= 0 && b < first {
					r.Banks[b] = "OK"
				}
			}
		}
	}
	for _, m := range reFile.FindAllStringSubmatch(text, -1) {
		r.Files[m[1]] = m[2]
	}
	return r
}

// BankStats summarises one bank over all runs.
type BankStats struct {
	OK, Bad int
	Wrong   map[uint32]int // wrong sums seen and how often
	Want    uint32
	Bits    [32]int // how often each bit of the sum differed
}

// ROMTestResult is the outcome of RunROMTest.
type ROMTestResult struct {
	Runs    []VerifyRun
	Banks   [MaskBanks]BankStats
	Garbled int // runs with disturbed serial output
}

// RunROMTest runs "bootdata <which> verify" n times. onRun, if set, is
// called after every run.
func RunROMTest(ctx context.Context, p Port, which string, n int, onRun func(int, VerifyRun)) (*ROMTestResult, error) {
	if err := p.SetBaud(ShellBaud); err != nil {
		return nil, err
	}
	RunShell(ctx, p, "", 300*time.Millisecond, 600*time.Millisecond)
	res := &ROMTestResult{}
	for b := range res.Banks {
		res.Banks[b].Wrong = map[uint32]int{}
	}
	cmd := "bootdata " + which + " verify"
	for i := 0; i < n; i++ {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		// adding up 16 MB of ROM takes a few seconds
		raw, err := RunShell(ctx, p, cmd, 2500*time.Millisecond, 90*time.Second)
		if err != nil && i == 0 {
			return nil, err
		}
		run := ParseVerify(raw)
		if i == 0 && strings.Contains(sanitize(raw), "usage") && run.Banks == [MaskBanks]string{} {
			return nil, fmt.Errorf("this firmware does not support %q", cmd)
		}
		res.Runs = append(res.Runs, run)
		if run.Garbled > 0 {
			res.Garbled++
		}
		for b := range res.Banks {
			st := &res.Banks[b]
			switch run.Banks[b] {
			case "OK":
				st.OK++
			case "BAD":
				st.Bad++
			}
			if run.Got[b] != 0 {
				st.Wrong[run.Got[b]]++
				st.Want = run.Want[b]
				diff := run.Got[b] ^ run.Want[b]
				for bit := 0; bit < 32; bit++ {
					if diff&(1<<bit) != 0 {
						st.Bits[bit]++
					}
				}
			}
		}
		if onRun != nil {
			onRun(i+1, run)
		}
	}
	return res, nil
}

// Diagnosis explains the result in plain words, one finding per line.
func (r *ROMTestResult) Diagnosis() []string {
	var out []string
	reported := 0
	for _, st := range r.Banks {
		reported += st.OK + st.Bad
	}
	if reported == 0 {
		return []string{`The machine reported no graphics ROM results. Look at its answer with: ` +
			`groundcontrol shell -port ... "bootdata rom verify"`}
	}
	unstable := false
	for b, st := range r.Banks {
		switch {
		case st.Bad == 0 && st.OK == 0:
			continue
		case st.Bad == 0:
			continue
		case st.OK > 0:
			unstable = true
			out = append(out, fmt.Sprintf("Bank %d is sometimes OK and sometimes BAD (%d of %d runs bad): "+
				"the ROM data is read unreliably, a hardware fault.", b, st.Bad, st.OK+st.Bad))
		default:
			out = append(out, fmt.Sprintf("Bank %d is BAD in every run: a ROM in this bank is defective, "+
				"missing or of the wrong version, or a data line is broken.", b))
		}
		if len(st.Wrong) > 0 {
			var lines []int
			for bit, c := range st.Bits {
				if c > 0 {
					lines = append(lines, bit)
				}
			}
			mask := uint32(0)
			for _, l := range lines {
				mask |= 1 << l
			}
			lowest := bits.TrailingZeros32(mask)
			var names []string
			for _, l := range lines {
				names = append(names, strconv.Itoa(l))
			}
			out = append(out, fmt.Sprintf("  The wrong sums differ from 0x%08x in bit(s) %s.", st.Want, strings.Join(names, ", ")))
			if lowest >= 8 {
				byteNo := lowest / 8
				out = append(out, fmt.Sprintf("  The lowest %d bits are always right, so the errors are in byte %d "+
					"(data bits %d-%d) of the 32-bit words: look at the ROM, socket and lines for that byte of bank %d.",
					lowest, byteNo, byteNo*8, byteNo*8+7, b))
			}
		}
	}
	if r.Garbled > 0 {
		out = append(out, fmt.Sprintf("The serial output was disturbed in %d of %d runs.", r.Garbled, len(r.Runs)))
	}
	if len(out) == 0 {
		out = append(out, fmt.Sprintf("All banks were OK in all %d runs.", len(r.Runs)))
	} else if unstable {
		out = append(out, "Check the PRISM card (contacts, ROM sockets), the 5 V supply and whether it gets worse when warm.")
	}
	return out
}

// WrongSums lists the wrong sums of a bank, most frequent first.
func (s BankStats) WrongSums() []string {
	type kv struct {
		v uint32
		n int
	}
	var l []kv
	for v, n := range s.Wrong {
		l = append(l, kv{v, n})
	}
	sort.Slice(l, func(i, j int) bool { return l[i].n > l[j].n || l[i].n == l[j].n && l[i].v < l[j].v })
	var out []string
	for _, e := range l {
		out = append(out, fmt.Sprintf("0x%08x (%dx)", e.v, e.n))
	}
	return out
}
