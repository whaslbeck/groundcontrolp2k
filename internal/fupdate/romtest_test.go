package fupdate

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/whaslbeck/groundcontrolp2k/internal/romset"
)

// Output of firmware 0.1/0.90 (one line for all banks) and 1.60 (one line
// per bank), as seen on a machine with an unreliable graphics bank 3.
func TestParseVerify(t *testing.T) {
	old := ParseVerify("bootdata current\r\n  symbol table is:  OK\r\n  im_mask3: 0x7745869f != 0x5745869f\r\n  im_mask[0-3] are: BAD\r\n% ")
	if old.Banks != [MaskBanks]string{"OK", "OK", "OK", "BAD"} || old.Got[3] != 0x7745869f || old.Want[3] != 0x5745869f {
		t.Errorf("old format: %+v", old)
	}
	if old.Files["symbol table"] != "OK" || !old.Complete {
		t.Errorf("old format files/complete: %+v", old)
	}
	nw := ParseVerify("  im_mask[0] is:      OK\r\n  im_mask[1] is:      OK\r\n  im_mask[2] is:      OK\r\n  im_mask[3] is:      BAD\r\n% ")
	if nw.Banks != [MaskBanks]string{"OK", "OK", "OK", "BAD"} {
		t.Errorf("new format: %+v", nw.Banks)
	}
	garbled := ParseVerify("im_mask0_si\xef\xbf\xbdY'$$$\x01\x02 im_mask[0-3] are: OK\r\n% ")
	if garbled.Garbled == 0 || garbled.Banks[2] != "OK" {
		t.Errorf("garbled: %+v", garbled)
	}
}

func TestROMTestFindsUpperByte(t *testing.T) {
	s := synthSet(t, 31, 50070, 1, 80)
	boot := s.Parts[romset.PartBootData].Data
	link := NewSimLink(LinkFaults{})
	m := NewMachine(MachineConfig{ROMBoot: boot, Faults: MachineFaults{FlakyMask: 4}, Log: t.Logf})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	go m.Run(ctx, link.Machine)
	res, err := RunROMTest(ctx, link.Host, "rom", 6, nil)
	if err != nil {
		t.Fatal(err)
	}
	st := res.Banks[3]
	if st.OK != 3 || st.Bad != 3 || len(st.Wrong) != 1 || st.Bits[29] != 3 {
		t.Fatalf("bank 3: %+v", st)
	}
	if res.Banks[0].OK != 6 || res.Banks[0].Bad != 0 {
		t.Errorf("bank 0: %+v", res.Banks[0])
	}
	d := strings.Join(res.Diagnosis(), "\n")
	for _, want := range []string{"Bank 3 is sometimes OK and sometimes BAD", "bit(s) 29", "byte 3 (data bits 24-31)"} {
		if !strings.Contains(d, want) {
			t.Errorf("diagnosis lacks %q:\n%s", want, d)
		}
	}
}

func TestROMTestNoResults(t *testing.T) {
	r := &ROMTestResult{Runs: []VerifyRun{{}}}
	if d := r.Diagnosis(); len(d) != 1 || !strings.Contains(d[0], "no graphics ROM results") {
		t.Errorf("diagnosis: %v", d)
	}
}
