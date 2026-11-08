// Package serial is a minimal, dependency-free serial port driver for
// Linux, macOS and Windows: 8N1, no flow control, selectable baud rate,
// read with timeout, input flush and output drain.
package serial

import (
	"errors"
	"time"
)

// Config controls the modem lines. The original Williams tool disabled
// both DTR and RTS; that is the default here as well.
type Config struct {
	DTR bool // assert DTR
	RTS bool // assert RTS
}

// Port is an open serial port. It implements fupdate.Port.
type Port struct {
	name string
	sys  sysPort
	baud int // current line speed
}

type sysPort interface {
	write(p []byte) (int, error)
	read(p []byte, d time.Duration) (int, error)
	setBaud(baud int) error
	flushInput() error
	drain() error
	close() error
}

// ErrUnsupported is returned on platforms without an implementation.
var ErrUnsupported = errors.New("serial: not supported on this platform")

// Open opens a port by name (/dev/ttyUSB0, /dev/cu.usbserial-1, COM3)
// at 9600 baud.
func Open(name string, cfg Config) (*Port, error) {
	s, err := openPort(name, cfg)
	if err != nil {
		return nil, err
	}
	return &Port{name: name, sys: s, baud: 9600}, nil
}

// Name returns the port name.
func (p *Port) Name() string { return p.name }

// Write sends all of b.
func (p *Port) Write(b []byte) (int, error) {
	done := 0
	for done < len(b) {
		n, err := p.sys.write(b[done:])
		done += n
		if err != nil {
			return done, err
		}
	}
	return done, nil
}

// ReadTimeout returns as soon as data is available or after d.
func (p *Port) ReadTimeout(b []byte, d time.Duration) (int, error) {
	if len(b) == 0 {
		return 0, nil
	}
	return p.sys.read(b, d)
}

// SetBaud changes the line speed.
// It does nothing when the speed is already set: reprogramming the port
// can make some USB adapters toggle the modem lines, which disturbs the
// serial interrupt handling of early Pinball 2000 firmware.
func (p *Port) SetBaud(baud int) error {
	if baud == p.baud {
		return nil
	}
	if err := p.sys.setBaud(baud); err != nil {
		return err
	}
	p.baud = baud
	return nil
}

// FlushInput discards received but unread data.
func (p *Port) FlushInput() error { return p.sys.flushInput() }

// Drain waits until all output has been transmitted.
func (p *Port) Drain() error { return p.sys.drain() }

// Close closes the port.
func (p *Port) Close() error { return p.sys.close() }
