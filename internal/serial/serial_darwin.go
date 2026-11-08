//go:build darwin

package serial

import (
	"fmt"
	"path/filepath"
	"sort"
	"syscall"
	"unsafe"
)

func getTermios(fd int) (*syscall.Termios, error) {
	t := &syscall.Termios{}
	return t, ioctl(fd, syscall.TIOCGETA, uintptr(unsafe.Pointer(t)))
}

func setTermios(fd int, t *syscall.Termios) error {
	return ioctl(fd, syscall.TIOCSETA, uintptr(unsafe.Pointer(t)))
}

func makeRaw(t *syscall.Termios) {
	t.Iflag = syscall.IGNPAR
	t.Oflag = 0
	t.Lflag = 0
	t.Cflag = syscall.CS8 | syscall.CREAD | syscall.CLOCAL
}

func setSpeed(t *syscall.Termios, baud int) error {
	switch baud {
	case 9600, 19200, 38400, 57600, 115200:
	default:
		return fmt.Errorf("serial: unsupported baud rate %d", baud)
	}
	// BSD termios stores the numeric speed directly.
	t.Ispeed, t.Ospeed = uint64(baud), uint64(baud)
	return nil
}

func (p *unixPort) flushInput() error {
	what := 1 // FREAD
	return ioctl(p.fd, syscall.TIOCFLUSH, uintptr(unsafe.Pointer(&what)))
}

func (p *unixPort) drain() error { return ioctl(p.fd, syscall.TIOCDRAIN, 0) }

// List returns candidate serial ports (callout devices).
func List() []string {
	m, _ := filepath.Glob("/dev/cu.*")
	sort.Strings(m)
	var out []string
	for _, p := range m {
		if p == "/dev/cu.Bluetooth-Incoming-Port" {
			continue
		}
		out = append(out, p)
	}
	return out
}
