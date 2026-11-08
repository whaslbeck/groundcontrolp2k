package fupdate

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestCleanShellOutput(t *testing.T) {
	raw := "printout hstd\r\n\r\nHigh Scores\r\n-----------\r\n\r\n  GAG   325,000,000  \r\n% "
	got := CleanShellOutput(raw, "printout hstd")
	want := "\nHigh Scores\n-----------\n\n  GAG   325,000,000"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if CleanShellOutput("% game name\r\nGame(x)\r\n% ", "game name") != "Game(x)" {
		t.Error("prompt before echo not handled")
	}
}

func TestReadReport(t *testing.T) {
	s := synthSet(t, 11, 50070, 1, 80)
	_, port, stop := dumpMachine(t, s, MachineFaults{})
	defer stop()
	sections := append([]ReportSection{}, DefaultReport...)
	sections = append(sections, ReportSection{"Bogus", "nosuchcommand", DefaultReport[0].Max})
	var seen int
	rs, err := ReadReport(context.Background(), port, sections, 100*time.Millisecond, func(ReportResult) { seen++ })
	if err != nil {
		t.Fatal(err)
	}
	if seen != len(sections) || len(rs) != len(sections) {
		t.Fatalf("got %d results, want %d", len(rs), len(sections))
	}
	for _, r := range rs[:len(rs)-1] {
		if r.Err != nil {
			t.Errorf("%s: %v", r.Title, r.Err)
		}
		if strings.Contains(r.Text, "\r") || strings.HasSuffix(r.Text, "%") {
			t.Errorf("%s: output not cleaned: %q", r.Title, r.Text)
		}
	}
	if rs[len(rs)-1].Err == nil {
		t.Error("unknown command not reported")
	}
	if !strings.Contains(rs[3].Text, "Total Plays") || !strings.Contains(rs[5].Text, "325,000,000") {
		t.Error("audits or high scores missing")
	}
	doc := FormatReport("head\n", rs)
	if !strings.Contains(doc, "Adjustments\n(printout adjustments)") || !strings.Contains(doc, "** not available") {
		t.Error("formatted report incomplete")
	}
}
