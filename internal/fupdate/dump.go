package fupdate

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"time"

	"github.com/whaslbeck/groundcontrolp2k/internal/romset"
)

// Reading the update FLASH back from a machine.
//
// The machine's shell has a "dump <address> [<count>]" command that prints
// memory as a hex dump, 16 bytes per line. The 4 MB update FLASH is mapped
// at FlashBase, so the installed image can be read with it. The shell runs
// at 9600 baud and every byte becomes about five characters, so this is
// slow: roughly 1.5 hours per MB.
//
// One large dump can stall the game long enough for its display watchdog
// to fire (observed in an emulator), so the image is read in small chunks
// with a pause between them.

// FlashBase is the address of the update FLASH in the machine's memory map.
const FlashBase = 0x12000000

// dumpLineChars is the length of one 16-byte dump line including CR LF.
const dumpLineChars = 82

// ErrMachineFatal means the machine stopped with a fatal error during a dump.
var ErrMachineFatal = errors.New("the machine reported a fatal error")

var errDumpGarbled = errors.New("dump output garbled")

// DumpEstimate is the time needed to dump n bytes at 9600 baud.
func DumpEstimate(n int) time.Duration {
	chars := float64(n) * dumpLineChars / 16
	return time.Duration(chars * 10 / ShellBaud * float64(time.Second))
}

var dumpLineRe = regexp.MustCompile(`^0x([0-9a-fA-F]{8}) ((?: [0-9a-fA-F]{2}){1,16})(?:\s|$)`)

// ReadMemory reads n bytes at addr with one "dump" command. idle is the
// longest silence tolerated while the output is still incomplete. The port
// must already be at the shell baud rate.
func ReadMemory(ctx context.Context, p Port, addr uint32, n int, idle time.Duration) ([]byte, error) {
	if n <= 0 {
		return nil, nil
	}
	p.FlushInput()
	if _, err := fmt.Fprintf(writerFunc(p.Write), "dump 0x%x %d\n", addr, n); err != nil {
		return nil, err
	}
	data := make([]byte, 0, n)
	var text []byte // unparsed output
	buf := make([]byte, 4096)
	last := time.Now()
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		k, err := p.ReadTimeout(buf, 100*time.Millisecond)
		if err != nil {
			return nil, err
		}
		if k == 0 {
			if time.Since(last) > idle {
				if len(data) == 0 {
					return nil, fmt.Errorf("no dump output: %w", ErrNoResponse)
				}
				return nil, fmt.Errorf("dump stopped after %d of %d bytes", len(data), n)
			}
			continue
		}
		last = time.Now()
		text = append(text, buf[:k]...)
		for {
			i := bytes.IndexByte(text, '\n')
			if i < 0 {
				break
			}
			line := bytes.TrimRight(text[:i], "\r")
			text = text[i+1:]
			m := dumpLineRe.FindSubmatch(line)
			if m == nil {
				// Only lines that are not hex dump lines are examined, as
				// the ASCII column of the data may contain any text.
				switch {
				case isFatal(line):
					return nil, ErrMachineFatal
				case bytes.Contains(line, []byte("not found")) || bytes.HasPrefix(line, []byte("usage")):
					return nil, fmt.Errorf("the machine does not accept \"dump\": %s", sanitize(string(line)))
				}
				continue // echo, "dump:" title, noise
			}
			if len(data) == n {
				continue
			}
			a, _ := strconv.ParseUint(string(m[1]), 16, 32)
			b, err := hex.DecodeString(string(bytes.ReplaceAll(m[2], []byte(" "), nil)))
			if err != nil || uint32(a) != addr+uint32(len(data)) || len(b) != min(16, n-len(data)) {
				return nil, fmt.Errorf("%w at 0x%08x", errDumpGarbled, addr+uint32(len(data)))
			}
			data = append(data, b...)
		}
		if isFatal(text) {
			return nil, ErrMachineFatal // "monitor> " prompt without line end
		}
		if len(data) == n && bytes.Contains(text, []byte("% ")) {
			return data, nil
		}
	}
}

// isFatal recognises the messages of the machine's fatal error handler and
// its monitor prompt.
func isFatal(line []byte) bool {
	l := bytes.TrimSpace(line)
	return bytes.HasPrefix(l, []byte("*** Fatal")) || bytes.HasPrefix(l, []byte("monitor"))
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(b []byte) (int, error) { return f(b) }

// waitPrompt sends a line feed until the shell answers with its prompt.
func waitPrompt(ctx context.Context, p Port, timeout time.Duration) error {
	end := time.Now().Add(timeout)
	buf := make([]byte, 512)
	for time.Now().Before(end) {
		p.FlushInput()
		p.Write([]byte("\n"))
		var got []byte
		t := time.Now().Add(1500 * time.Millisecond)
		for time.Now().Before(t) {
			if err := ctx.Err(); err != nil {
				return err
			}
			k, err := p.ReadTimeout(buf, 100*time.Millisecond)
			if err != nil {
				return err
			}
			got = append(got, buf[:k]...)
			if bytes.HasSuffix(bytes.TrimRight(got, " "), []byte("%")) {
				time.Sleep(100 * time.Millisecond)
				p.FlushInput()
				return nil
			}
		}
	}
	return ErrNoResponse
}

// DumpOptions control DumpFlash.
type DumpOptions struct {
	Chunk      int           // bytes per dump command (default 4096, multiple of 16)
	Pause      time.Duration // between commands (default 300ms)
	Idle       time.Duration // longest silence within one command (default 10s)
	MaxRetries int           // per chunk (default 5)
	// RecoverDelay is the wait after an error before the shell is used
	// again (default 2s).
	RecoverDelay time.Duration
	// Have is image data already read in an earlier run, starting at
	// offset 0. Reading continues behind it.
	Have []byte
	// OnData is called with every chunk read, e.g. to append it to a file.
	OnData   func(off int, data []byte) error
	Progress func(Progress)
	Log      func(format string, args ...any)
}

func (o *DumpOptions) defaults() {
	if o.Chunk <= 0 {
		o.Chunk = 4096
	}
	o.Chunk = max(16, o.Chunk&^15)
	if o.Pause == 0 {
		o.Pause = 300 * time.Millisecond
	}
	if o.Idle == 0 {
		o.Idle = 10 * time.Second
	}
	if o.MaxRetries == 0 {
		o.MaxRetries = 5
	}
	if o.RecoverDelay == 0 {
		o.RecoverDelay = 2 * time.Second
	}
	if o.Log == nil {
		o.Log = func(string, ...any) {}
	}
	if o.Progress == nil {
		o.Progress = func(Progress) {}
	}
}

// DumpError is returned when a dump stops before the image is complete.
// Offset bytes were read successfully and can be passed as Have later.
type DumpError struct {
	Offset int
	Err    error
}

func (e *DumpError) Error() string {
	return fmt.Sprintf("dump stopped at offset 0x%x: %v", e.Offset, e.Err)
}
func (e *DumpError) Unwrap() error { return e.Err }

// ImageSize returns the length of the game image described by bootdata:
// bootdata, im_flsh0, game and symbols back to back.
func ImageSize(bd *romset.BootData) int {
	return romset.BootDataSize + 4*int(bd.ImSys0Size+bd.GameCodeSize+bd.SymbolsSize)
}

// DumpFlash reads the game image from the machine's update FLASH. It
// first reads bootdata to learn the file sizes, then the rest. The
// result has its parts filled in; check them with Set.Checks.
func DumpFlash(ctx context.Context, p Port, opt DumpOptions) (*romset.Set, error) {
	opt.defaults()
	if err := p.SetBaud(ShellBaud); err != nil {
		return nil, err
	}
	if err := waitPrompt(ctx, p, 10*time.Second); err != nil {
		return nil, err
	}
	img := append([]byte(nil), opt.Have...)
	start, startLen := time.Now(), len(img)
	total := romset.BootDataSize
	var bd *romset.BootData
	chunk := opt.Chunk
	section := func(off int) string {
		if bd == nil || off < romset.BootDataSize {
			return "bootdata"
		}
		off -= romset.BootDataSize
		for _, s := range []struct {
			name  string
			words uint32
		}{{"im_flsh0", bd.ImSys0Size}, {"game", bd.GameCodeSize}, {"symbols", bd.SymbolsSize}} {
			if off < 4*int(s.words) {
				return s.name
			}
			off -= 4 * int(s.words)
		}
		return "symbols"
	}
	progress := func() {
		pr := Progress{Section: section(len(img)), Bytes: len(img), Total: total, Elapsed: time.Since(start),
			Packet: len(img), Packets: total}
		if done := len(img) - startLen; done > 0 {
			pr.Remaining = time.Duration(float64(pr.Elapsed) * float64(total-len(img)) / float64(done))
		} else {
			pr.Remaining = DumpEstimate(total - len(img))
		}
		opt.Progress(pr)
	}
	for {
		if bd == nil && len(img) >= romset.BootDataSize {
			b := img[:romset.BootDataSize]
			if err := romset.ValidateBootData(b); err != nil {
				return nil, fmt.Errorf("the update FLASH holds no valid image (%v); "+
					"a machine that runs from its PRISM ROMs has nothing to dump here", err)
			}
			bd, _ = romset.ParseBootData(b)
			total = ImageSize(bd)
			if total > MaxFlash {
				return nil, fmt.Errorf("bootdata describes %d bytes, more than the FLASH holds", total)
			}
			left := DumpEstimate(total - len(img)).Round(time.Minute)
			opt.Log("bootdata: game %d version %s, image %d bytes, about %dh%02dm at 9600 baud",
				bd.GameID, bd.GameVersion(), total, int(left.Hours()), int(left.Minutes())%60)
		}
		if len(img) > total {
			img = img[:total]
		}
		progress()
		if len(img) == total {
			break
		}
		off := len(img)
		n := min(chunk, total-off)
		var data []byte
		var err error
		for try := 0; ; try++ {
			data, err = ReadMemory(ctx, p, FlashBase+uint32(off), n, opt.Idle)
			if err == nil || ctx.Err() != nil {
				break
			}
			if try >= opt.MaxRetries {
				return nil, &DumpError{Offset: off, Err: err}
			}
			opt.Log("offset 0x%x: %v, retrying", off, err)
			if errors.Is(err, ErrMachineFatal) {
				// leave the monitor and use smaller chunks from now on
				time.Sleep(opt.RecoverDelay)
				p.Write([]byte("continue\n"))
				time.Sleep(opt.RecoverDelay)
				chunk = max(512, chunk/2)
				n = min(chunk, total-off)
			}
			time.Sleep(opt.RecoverDelay / 2)
			if err := waitPrompt(ctx, p, 30*time.Second); err != nil && ctx.Err() == nil {
				return nil, &DumpError{Offset: off, Err: fmt.Errorf("shell does not answer after error: %w", err)}
			}
		}
		if err != nil {
			return nil, &DumpError{Offset: off, Err: err}
		}
		if opt.OnData != nil {
			if err := opt.OnData(off, data); err != nil {
				return nil, &DumpError{Offset: off, Err: err}
			}
		}
		img = append(img, data...)
		select {
		case <-ctx.Done():
			return nil, &DumpError{Offset: len(img), Err: ctx.Err()}
		case <-time.After(opt.Pause):
		}
	}
	return SplitImage(img)
}

// SplitImage cuts a FLASH image into its files.
func SplitImage(img []byte) (*romset.Set, error) {
	if len(img) < romset.BootDataSize {
		return nil, errors.New("image shorter than bootdata")
	}
	bd, err := romset.ParseBootData(img)
	if err != nil {
		return nil, err
	}
	if len(img) < ImageSize(bd) {
		return nil, fmt.Errorf("image has %d bytes, bootdata describes %d", len(img), ImageSize(bd))
	}
	s := &romset.Set{Boot: bd}
	off := 0
	for _, part := range []struct {
		k    romset.Kind
		size int
	}{
		{romset.PartBootData, romset.BootDataSize},
		{romset.PartImFlsh0, 4 * int(bd.ImSys0Size)},
		{romset.PartGame, 4 * int(bd.GameCodeSize)},
		{romset.PartSymbols, 4 * int(bd.SymbolsSize)},
	} {
		s.Parts[part.k] = &romset.Part{Kind: part.k, Name: part.k.FileName(),
			Data: append([]byte(nil), img[off:off+part.size]...)}
		off += part.size
	}
	return s, nil
}

// DumpRange reads n bytes of machine memory starting at addr in chunks,
// with the same retries and recovery as DumpFlash. opt.Have holds data
// already read from addr on (to continue an interrupted read), and
// opt.OnData receives offsets relative to addr. The port must be at the
// shell and the shell must answer; section names the range in progress
// reports.
func DumpRange(ctx context.Context, p Port, addr uint32, n int, section string, opt DumpOptions) ([]byte, error) {
	opt.defaults()
	if err := p.SetBaud(ShellBaud); err != nil {
		return nil, err
	}
	if err := waitPrompt(ctx, p, 10*time.Second); err != nil {
		return nil, err
	}
	buf := append([]byte(nil), opt.Have...)
	if len(buf) > n {
		buf = buf[:n]
	}
	start, startLen := time.Now(), len(buf)
	chunk := opt.Chunk
	for {
		pr := Progress{Section: section, Bytes: len(buf), Total: n, Elapsed: time.Since(start),
			Packet: len(buf), Packets: n}
		if done := len(buf) - startLen; done > 0 {
			pr.Remaining = time.Duration(float64(pr.Elapsed) * float64(n-len(buf)) / float64(done))
		} else {
			pr.Remaining = DumpEstimate(n - len(buf))
		}
		opt.Progress(pr)
		if len(buf) >= n {
			return buf, nil
		}
		off := len(buf)
		size := min(chunk, n-off)
		var data []byte
		var err error
		for try := 0; ; try++ {
			data, err = ReadMemory(ctx, p, addr+uint32(off), size, opt.Idle)
			if err == nil || ctx.Err() != nil {
				break
			}
			if try >= opt.MaxRetries {
				return nil, &DumpError{Offset: off, Err: err}
			}
			opt.Log("0x%08x: %v, retrying", addr+uint32(off), err)
			if errors.Is(err, ErrMachineFatal) {
				time.Sleep(opt.RecoverDelay)
				p.Write([]byte("continue\n"))
				time.Sleep(opt.RecoverDelay)
				chunk = max(512, chunk/2)
				size = min(chunk, n-off)
			}
			time.Sleep(opt.RecoverDelay / 2)
			if err := waitPrompt(ctx, p, 30*time.Second); err != nil && ctx.Err() == nil {
				return nil, &DumpError{Offset: off, Err: fmt.Errorf("shell does not answer after error: %w", err)}
			}
		}
		if err != nil {
			return nil, &DumpError{Offset: off, Err: err}
		}
		if opt.OnData != nil {
			if err := opt.OnData(off, data); err != nil {
				return nil, &DumpError{Offset: off, Err: err}
			}
		}
		buf = append(buf, data...)
		select {
		case <-ctx.Done():
			return nil, &DumpError{Offset: len(buf), Err: ctx.Err()}
		case <-time.After(opt.Pause):
		}
	}
}

// PartRange returns where a part of the game image lies in the update
// FLASH (offset from FlashBase) and its length, from the sizes recorded
// in bootdata (in 32-bit words).
func PartRange(k romset.Kind, imSys0Words, gameWords, symbolWords uint32) (off, n int, ok bool) {
	b := romset.BootDataSize
	switch k {
	case romset.PartBootData:
		return 0, b, true
	case romset.PartImFlsh0:
		return b, 4 * int(imSys0Words), true
	case romset.PartGame:
		return b + 4*int(imSys0Words), 4 * int(gameWords), true
	case romset.PartSymbols:
		return b + 4*int(imSys0Words+gameWords), 4 * int(symbolWords), true
	}
	return 0, 0, false
}
