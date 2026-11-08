//go:build linux && (ppc64 || ppc64le)

package serial

const (
	tcFLSH  = 0x2000741F
	tcSBRK  = 0x2000741D
	tcCBAUD = 0xFF
)
