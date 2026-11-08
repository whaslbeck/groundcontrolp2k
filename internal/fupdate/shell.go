package fupdate

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// MachineInfo is what the machine reports via "bootdata current".
type MachineInfo struct {
	Location     string            // "PRISM ROM" or "PRISM FLASH"
	Fields       map[string]string // all "key: value" lines
	GameID       uint32
	GameVersion  string // as printed by the machine, e.g. "1.8"
	System       string
	Timestamp    string
	BootState    string
	Valid        bool
	ValidityText []string // "game code is: OK" lines
	Raw          string
}

// ErrNoResponse means nothing came back from the shell.
var ErrNoResponse = errors.New("no response from the machine (check cable, port and that COM1 is in Shell mode)")

// RunShell sends one command line at 9600 baud and returns what came back
// until the line stays quiet for `quiet` (or `max` elapsed).
func RunShell(ctx context.Context, p Port, cmd string, quiet, max time.Duration) (string, error) {
	if err := p.SetBaud(ShellBaud); err != nil {
		return "", err
	}
	p.FlushInput()
	if _, err := p.Write([]byte(cmd + "\n")); err != nil {
		return "", err
	}
	p.Drain()
	var out []byte
	buf := make([]byte, 512)
	end := time.Now().Add(max)
	last := time.Now()
	for time.Now().Before(end) && ctx.Err() == nil {
		n, err := p.ReadTimeout(buf, 100*time.Millisecond)
		if err != nil {
			return string(out), err
		}
		if n > 0 {
			out = append(out, buf[:n]...)
			last = time.Now()
		} else if len(out) > 0 && time.Since(last) >= quiet {
			break
		}
	}
	if len(out) == 0 {
		return "", ErrNoResponse
	}
	return string(out), ctx.Err()
}

var fieldRe = regexp.MustCompile(`^\s*([a-z_0-9\[\]\- ]+?):\s*(.*?)\s*$`)

// QueryMachine runs "bootdata current" and parses the result.
func QueryMachine(ctx context.Context, p Port) (*MachineInfo, error) {
	// wake the shell / clear a half typed line
	RunShell(ctx, p, "", 300*time.Millisecond, 600*time.Millisecond)
	var mi *MachineInfo
	for try := 0; try < 2; try++ {
		raw, err := RunShell(ctx, p, "bootdata current", 700*time.Millisecond, 8*time.Second)
		if err != nil {
			return nil, err
		}
		// The first answer after opening the port can be incomplete (a
		// line still being typed, noise from the adapter): ask again.
		if mi = ParseBootDataDump(raw); mi.GameID != 0 || strings.Contains(raw, "not valid") {
			break
		}
	}
	return mi, nil
}

// ParseBootDataDump parses the output of the "bootdata" shell command.
func ParseBootDataDump(raw string) *MachineInfo {
	mi := &MachineInfo{Fields: map[string]string{}, Raw: raw}
	text := sanitize(raw)
	if strings.Contains(text, "PRISM FLASH") {
		mi.Location = "PRISM FLASH"
	} else if strings.Contains(text, "PRISM ROM") {
		mi.Location = "PRISM ROM"
	}
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, " is:") || strings.Contains(line, " are:") {
			mi.ValidityText = append(mi.ValidityText, strings.TrimSpace(line))
		}
		m := fieldRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		k := strings.TrimSpace(m[1])
		if i := strings.LastIndex(k, " "); i >= 0 && !strings.Contains(k, " is") && !strings.Contains(k, " are") {
			k = k[i+1:] // "PRISM FLASH: Boot data structure" etc.
		}
		mi.Fields[k] = strings.Trim(m[2], `"`)
	}
	if v, err := strconv.Atoi(mi.Fields["game_id"]); err == nil {
		mi.GameID = uint32(v)
	}
	mi.GameVersion = mi.Fields["game_version"]
	mi.System = mi.Fields["system_version"]
	mi.Timestamp = mi.Fields["boot_data_timestamp"]
	mi.BootState = mi.Fields["boot_state"]
	mi.Valid = mi.GameID != 0 && !strings.Contains(text, "not valid")
	return mi
}
