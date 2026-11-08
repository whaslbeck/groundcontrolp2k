//go:build linux || darwin

package serial

import (
	"fmt"
	"syscall"
	"time"
	"unsafe"
)

type unixPort struct {
	fd    int
	vtime uint8 // currently configured VTIME (1/10 s)
}

func ioctl(fd int, req uintptr, arg uintptr) error {
	for {
		_, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), req, arg)
		if e == syscall.EINTR {
			continue
		}
		if e != 0 {
			return e
		}
		return nil
	}
}

func openPort(name string, cfg Config) (sysPort, error) {
	fd, err := syscall.Open(name, syscall.O_RDWR|syscall.O_NOCTTY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("serial: open %s: %w", name, err)
	}
	p := &unixPort{fd: fd}
	fail := func(err error) (sysPort, error) {
		syscall.Close(fd)
		return nil, fmt.Errorf("serial: %s: %w", name, err)
	}
	if err := ioctl(fd, syscall.TIOCEXCL, 0); err != nil {
		return fail(err)
	}
	if err := syscall.SetNonblock(fd, false); err != nil {
		return fail(err)
	}
	t, err := getTermios(fd)
	if err != nil {
		return fail(err)
	}
	makeRaw(t)
	if err := setSpeed(t, 9600); err != nil {
		return fail(err)
	}
	t.Cc[syscall.VMIN] = 0
	t.Cc[syscall.VTIME] = 1
	p.vtime = 1
	if err := setTermios(fd, t); err != nil {
		return fail(err)
	}
	if err := setModemLines(fd, cfg); err != nil {
		return fail(err)
	}
	return p, nil
}

func setModemLines(fd int, cfg Config) error {
	var clear, set int
	if cfg.DTR {
		set |= syscall.TIOCM_DTR
	} else {
		clear |= syscall.TIOCM_DTR
	}
	if cfg.RTS {
		set |= syscall.TIOCM_RTS
	} else {
		clear |= syscall.TIOCM_RTS
	}
	// Some USB adapters do not support modem control; ignore errors.
	if clear != 0 {
		ioctl(fd, syscall.TIOCMBIC, uintptr(unsafe.Pointer(&clear)))
	}
	if set != 0 {
		ioctl(fd, syscall.TIOCMBIS, uintptr(unsafe.Pointer(&set)))
	}
	return nil
}

func (p *unixPort) write(b []byte) (int, error) {
	for {
		n, err := syscall.Write(p.fd, b)
		if err == syscall.EINTR || err == syscall.EAGAIN {
			if n > 0 {
				return n, nil
			}
			continue
		}
		if n < 0 {
			n = 0
		}
		return n, err
	}
}

func (p *unixPort) read(b []byte, d time.Duration) (int, error) {
	deadline := time.Now().Add(d)
	for {
		left := time.Until(deadline)
		if left <= 0 {
			return 0, nil
		}
		vt := uint8(min(255, max(1, (left+99*time.Millisecond)/(100*time.Millisecond))))
		if vt != p.vtime {
			t, err := getTermios(p.fd)
			if err != nil {
				return 0, err
			}
			t.Cc[syscall.VMIN] = 0
			t.Cc[syscall.VTIME] = vt
			if err := setTermios(p.fd, t); err != nil {
				return 0, err
			}
			p.vtime = vt
		}
		n, err := syscall.Read(p.fd, b)
		if err == syscall.EINTR || err == syscall.EAGAIN {
			continue
		}
		if err != nil {
			return 0, err
		}
		if n > 0 {
			return n, nil
		}
	}
}

func (p *unixPort) setBaud(baud int) error {
	t, err := getTermios(p.fd)
	if err != nil {
		return err
	}
	if err := setSpeed(t, baud); err != nil {
		return err
	}
	return setTermios(p.fd, t)
}

func (p *unixPort) close() error {
	ioctl(p.fd, syscall.TIOCNXCL, 0) // the exclusive flag outlives close() while the peer is open
	return syscall.Close(p.fd)
}
