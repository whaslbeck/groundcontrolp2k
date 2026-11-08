//go:build linux

package serial

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"unsafe"
)

func getTermios(fd int) (*syscall.Termios, error) {
	t := &syscall.Termios{}
	return t, ioctl(fd, syscall.TCGETS, uintptr(unsafe.Pointer(t)))
}

func setTermios(fd int, t *syscall.Termios) error {
	return ioctl(fd, syscall.TCSETS, uintptr(unsafe.Pointer(t)))
}

func makeRaw(t *syscall.Termios) {
	t.Iflag = syscall.IGNPAR
	t.Oflag = 0
	t.Lflag = 0
	t.Cflag = syscall.CS8 | syscall.CREAD | syscall.CLOCAL
}

var speeds = map[int]uint32{
	9600:   syscall.B9600,
	19200:  syscall.B19200,
	38400:  syscall.B38400,
	57600:  syscall.B57600,
	115200: syscall.B115200,
}

func setSpeed(t *syscall.Termios, baud int) error {
	s, ok := speeds[baud]
	if !ok {
		return fmt.Errorf("serial: unsupported baud rate %d", baud)
	}
	t.Cflag = t.Cflag&^tcCBAUD | s
	return nil
}

func (p *unixPort) flushInput() error { return ioctl(p.fd, tcFLSH, syscall.TCIFLUSH) }

func (p *unixPort) drain() error { return ioctl(p.fd, tcSBRK, 1) }

// List returns candidate serial ports.
func List() []string {
	var out []string
	seen := map[string]bool{}
	add := func(p string) {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	for _, pat := range []string{"/dev/ttyUSB*", "/dev/ttyACM*", "/dev/ttyAMA*", "/dev/ttyS*"} {
		m, _ := filepath.Glob(pat)
		sort.Strings(m)
		for _, p := range m {
			base := filepath.Base(p)
			if strings.HasPrefix(base, "ttyS") {
				// the 8250 driver creates ttyS0..31 even without hardware
				t, err := os.ReadFile("/sys/class/tty/" + base + "/type")
				if err != nil || strings.TrimSpace(string(t)) == "0" {
					continue
				}
			}
			add(p)
		}
	}
	return out
}
