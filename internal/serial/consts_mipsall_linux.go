//go:build linux && (mips || mipsle || mips64 || mips64le)

package serial

const (
	tcFLSH  = 0x5407
	tcSBRK  = 0x5405
	tcCBAUD = 0x100F
)
