//go:build darwin

package host

import (
	"strconv"

	"golang.org/x/sys/unix"
)

// SystemNative is the production in-process reader: vm.loadavg, hw.ncpu and
// the memory-pressure level. vm.swapusage stays on the command seam.
func SystemNative(key string) (string, error) {
	switch key {
	case SysctlLoad:
		b, err := unix.SysctlRaw(key)
		if err != nil {
			return "", err
		}
		l, err := ParseKernLoadavg(b)
		if err != nil {
			return "", err
		}
		return loadavgText(l), nil
	case SysctlNCPU, SysctlPressure:
		v, err := unix.SysctlUint32(key)
		if err != nil {
			return "", err
		}
		return strconv.FormatUint(uint64(v), 10) + "\n", nil
	}
	return "", ErrNotNative
}
