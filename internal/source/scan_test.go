package source

import (
	"os"
	"testing"

	"github.com/whaslbeck/groundcontrolp2k/internal/romset"
)

// TestScanRealUpdates runs against the original update files if present
// (P2K_UPDATES or ../FirmwareUpdates next to the project).
func TestScanRealUpdates(t *testing.T) {
	dir := os.Getenv("P2K_UPDATES")
	if dir == "" {
		dir = "../../../FirmwareUpdates"
	}
	if _, err := os.Stat(dir); err != nil {
		t.Skip("no update files:", err)
	}
	r := Scan(dir)
	for _, w := range r.Warnings {
		t.Error(w)
	}
	want := []string{"1.20", "1.40", "1.50", "1.60", "1.80", "1.90", "1.91", "1.95", "2.00", "2.10", "2.50", "2.60"}
	if len(r.Sets) != len(want) {
		t.Fatalf("found %d sets, want %d", len(r.Sets), len(want))
	}
	for i, s := range r.Sets {
		if s.Version() != want[i] || s.GameID() != 50070 {
			t.Errorf("set %d: %s %d", i, s.Version(), s.GameID())
		}
		if err := s.GameUpdateReady(); err != nil {
			t.Errorf("%s: %v", s.Label(), err)
		}
		if s.Version() != "1.80" && s.SoundUpdateReady() != nil {
			t.Errorf("%s: no sound", s.Label())
		}
		if s.XinaVersion() == "" {
			t.Errorf("%s: no XINA version", s.Label())
		}
		if p := s.Parts[romset.PartImFlsh0]; s.Version() == "1.80" && p.Note == "" {
			t.Error("1.80 im_flsh0 should be borrowed")
		}
	}
}
