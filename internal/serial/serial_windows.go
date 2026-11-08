//go:build windows

package serial

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

var (
	kernel32            = syscall.NewLazyDLL("kernel32.dll")
	procGetCommState    = kernel32.NewProc("GetCommState")
	procSetCommState    = kernel32.NewProc("SetCommState")
	procSetCommTimeouts = kernel32.NewProc("SetCommTimeouts")
	procSetupComm       = kernel32.NewProc("SetupComm")
	procPurgeComm       = kernel32.NewProc("PurgeComm")
	procQueryDosDevice  = kernel32.NewProc("QueryDosDeviceW")
)

// dcb is the Win32 DCB structure (28 bytes).
type dcb struct {
	DCBlength  uint32
	BaudRate   uint32
	Flags      uint32
	wReserved  uint16
	XonLim     uint16
	XoffLim    uint16
	ByteSize   byte
	Parity     byte
	StopBits   byte
	XonChar    byte
	XoffChar   byte
	ErrorChar  byte
	EofChar    byte
	EvtChar    byte
	wReserved1 uint16
}

type commTimeouts struct {
	ReadIntervalTimeout         uint32
	ReadTotalTimeoutMultiplier  uint32
	ReadTotalTimeoutConstant    uint32
	WriteTotalTimeoutMultiplier uint32
	WriteTotalTimeoutConstant   uint32
}

const (
	dcbBinary        = 0x0001
	dtrControlEnable = 0x0010 // fDtrControl = DTR_CONTROL_ENABLE
	rtsControlEnable = 0x1000 // fRtsControl = RTS_CONTROL_ENABLE
	purgeRxAbort     = 0x0002
	purgeRxClear     = 0x0008
	maxDword         = 0xFFFFFFFF
)

type winPort struct {
	h       syscall.Handle
	cfg     Config
	timeout time.Duration
}

func call(p *syscall.LazyProc, args ...uintptr) error {
	r, _, err := p.Call(args...)
	if r == 0 {
		return err
	}
	return nil
}

func openPort(name string, cfg Config) (sysPort, error) {
	path := name
	if !strings.HasPrefix(path, `\\.\`) {
		path = `\\.\` + path
	}
	p16, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := syscall.CreateFile(p16, syscall.GENERIC_READ|syscall.GENERIC_WRITE, 0, nil,
		syscall.OPEN_EXISTING, 0, 0)
	if err != nil {
		return nil, fmt.Errorf("serial: open %s: %w", name, err)
	}
	p := &winPort{h: h, cfg: cfg}
	call(procSetupComm, uintptr(h), 4096, 4096)
	if err := p.setBaud(9600); err != nil {
		syscall.CloseHandle(h)
		return nil, err
	}
	p.timeout = -1
	return p, nil
}

func (p *winPort) setBaud(baud int) error {
	var d dcb
	d.DCBlength = uint32(unsafe.Sizeof(d))
	if err := call(procGetCommState, uintptr(p.h), uintptr(unsafe.Pointer(&d))); err != nil {
		return fmt.Errorf("serial: GetCommState: %w", err)
	}
	d.BaudRate = uint32(baud)
	d.Flags = dcbBinary // no parity check, no flow control, DTR/RTS off
	if p.cfg.DTR {
		d.Flags |= dtrControlEnable
	}
	if p.cfg.RTS {
		d.Flags |= rtsControlEnable
	}
	d.ByteSize, d.Parity, d.StopBits = 8, 0, 0 // 8N1
	if err := call(procSetCommState, uintptr(p.h), uintptr(unsafe.Pointer(&d))); err != nil {
		return fmt.Errorf("serial: SetCommState(%d): %w", baud, err)
	}
	return nil
}

func (p *winPort) setTimeout(d time.Duration) error {
	if d == p.timeout {
		return nil
	}
	ms := uint32(max(1, d/time.Millisecond))
	// Return immediately when data is buffered, else wait up to ms.
	t := commTimeouts{ReadIntervalTimeout: maxDword, ReadTotalTimeoutMultiplier: maxDword,
		ReadTotalTimeoutConstant: ms}
	if err := call(procSetCommTimeouts, uintptr(p.h), uintptr(unsafe.Pointer(&t))); err != nil {
		return fmt.Errorf("serial: SetCommTimeouts: %w", err)
	}
	p.timeout = d
	return nil
}

func (p *winPort) read(b []byte, d time.Duration) (int, error) {
	if err := p.setTimeout(d); err != nil {
		return 0, err
	}
	var n uint32
	err := syscall.ReadFile(p.h, b, &n, nil)
	return int(n), err
}

func (p *winPort) write(b []byte) (int, error) {
	var n uint32
	err := syscall.WriteFile(p.h, b, &n, nil)
	return int(n), err
}

func (p *winPort) flushInput() error {
	return call(procPurgeComm, uintptr(p.h), purgeRxAbort|purgeRxClear)
}

func (p *winPort) drain() error { return syscall.FlushFileBuffers(p.h) }

func (p *winPort) close() error { return syscall.CloseHandle(p.h) }

// List returns the COM ports known to Windows.
func List() []string {
	buf := make([]uint16, 65536)
	r, _, _ := procQueryDosDevice.Call(0, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if r == 0 {
		return nil
	}
	var out []string
	start := 0
	for i := 0; i < int(r); i++ {
		if buf[i] != 0 {
			continue
		}
		if i > start {
			s := syscall.UTF16ToString(buf[start:i])
			if strings.HasPrefix(s, "COM") {
				if _, err := strconv.Atoi(s[3:]); err == nil {
					out = append(out, s)
				}
			}
		}
		start = i + 1
	}
	sort.Slice(out, func(i, j int) bool {
		a, _ := strconv.Atoi(out[i][3:])
		b, _ := strconv.Atoi(out[j][3:])
		return a < b
	})
	return out
}
