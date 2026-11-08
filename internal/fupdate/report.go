package fupdate

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Reading the machine's service report over the shell.
//
// The shell command "printout" produces the text the machine would send
// to a service printer: audits, adjustments, high scores, earnings,
// pricing and the error logs. A few more commands add what the printout
// lacks. Everything is read-only.

// ReportSection is one shell command of a report.
type ReportSection struct {
	Title string
	Cmd   string
	Max   time.Duration // longest time to wait for the complete output
}

// DefaultReport lists the sections of a full report, in order.
var DefaultReport = []ReportSection{
	{"Machine", "game name", 10 * time.Second},
	{"Installed version", "bootdata current", 10 * time.Second},
	{"Clock", "time", 10 * time.Second},
	{"Audits", "printout audits", 60 * time.Second},
	{"Adjustments", "printout adjustments", 60 * time.Second},
	{"High scores", "hstd", 30 * time.Second},
	{"Hourly earnings", "printout hourly", 60 * time.Second},
	{"Daily earnings", "printout daily", 60 * time.Second},
	{"Pricing", "printout pricing", 30 * time.Second},
	{"Switch counters", "switch counters", 30 * time.Second},
	{"Power driver board", "pdb", 10 * time.Second},
	{"Fatal errors", "printout fatal", 60 * time.Second},
	{"Non-fatal errors", "printout nonfatal", 60 * time.Second},
}

// ReportResult is the output of one section.
type ReportResult struct {
	ReportSection
	Text string // cleaned output without the echoed command and the prompt
	Err  error
}

// ReadReport runs the sections on the machine's shell. A section's
// output is complete when the line has been quiet for `quiet` (default
// 1.5 s, enough for the pauses between the machine's print buffers).
// onSection, if set, is called after every section. It stops early only
// when the context is cancelled or the first section gets no answer.
func ReadReport(ctx context.Context, p Port, sections []ReportSection, quiet time.Duration,
	onSection func(ReportResult)) ([]ReportResult, error) {
	if quiet == 0 {
		quiet = 1500 * time.Millisecond
	}
	if err := p.SetBaud(ShellBaud); err != nil {
		return nil, err
	}
	// wake the shell / clear a half typed line
	RunShell(ctx, p, "", 300*time.Millisecond, 600*time.Millisecond)
	var out []ReportResult
	for i, s := range sections {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		raw, err := RunShell(ctx, p, s.Cmd, quiet, s.Max)
		r := ReportResult{ReportSection: s, Text: CleanShellOutput(raw, s.Cmd), Err: err}
		if err == nil && isUsageAnswer(r.Text, s.Cmd) {
			r.Err = fmt.Errorf("this firmware does not support %q", s.Cmd)
		}
		out = append(out, r)
		if onSection != nil {
			onSection(r)
		}
		if i == 0 && err == ErrNoResponse {
			return out, err
		}
	}
	return out, nil
}

// isUsageAnswer recognises "not found" and usage texts, with which the
// shell answers a command or argument it does not know.
func isUsageAnswer(text, cmd string) bool {
	t := strings.TrimSpace(text)
	if len(t) > 300 {
		return false
	}
	low := strings.ToLower(t)
	word := strings.Fields(cmd)[0]
	return strings.Contains(low, ": not found") || strings.HasPrefix(low, "usage") ||
		strings.HasPrefix(low, word+" <")
}

// CleanShellOutput strips the echoed command line and the trailing
// prompt from shell output and normalises line ends.
func CleanShellOutput(raw, cmd string) string {
	t := sanitize(raw)
	lines := strings.Split(t, "\n")
	// echo of the command, possibly preceded by a prompt
	if len(lines) > 0 && strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(lines[0]), "%")) == cmd {
		lines = lines[1:]
	}
	// trailing prompt
	for len(lines) > 0 {
		l := strings.TrimSpace(lines[len(lines)-1])
		if l == "" || l == "%" {
			lines = lines[:len(lines)-1]
			continue
		}
		break
	}
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " \t")
	}
	return strings.Join(lines, "\n")
}

// FormatReport renders the results as one text document.
func FormatReport(header string, rs []ReportResult) string {
	var b strings.Builder
	b.WriteString(header)
	b.WriteString("\n")
	for _, r := range rs {
		fmt.Fprintf(&b, "\n%s\n%s\n", strings.Repeat("=", 72), r.Title)
		fmt.Fprintf(&b, "(%s)\n%s\n\n", r.Cmd, strings.Repeat("=", 72))
		if r.Err != nil {
			fmt.Fprintf(&b, "** not available: %v\n", r.Err)
			if r.Text != "" {
				b.WriteString(r.Text)
				b.WriteString("\n")
			}
			continue
		}
		b.WriteString(r.Text)
		b.WriteString("\n")
	}
	return b.String()
}
