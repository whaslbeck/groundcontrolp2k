//go:build linux && !(mips || mipsle || mips64 || mips64le || ppc64 || ppc64le)

package serial

// ioctl numbers and termios masks not exported by package syscall.
const (
	tcFLSH  = 0x540B
	tcSBRK  = 0x5409
	tcCBAUD = 0x100F
)
