//go:build !linux && !darwin && !windows

package serial

func openPort(name string, cfg Config) (sysPort, error) { return nil, ErrUnsupported }

// List returns no ports on unsupported platforms.
func List() []string { return nil }
