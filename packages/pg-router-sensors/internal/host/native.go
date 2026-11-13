package host

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
)

// ErrNotNative is returned by a Native reader for a key it does not serve; the
// sensor then falls back to launching sysctl through the command seam.
var ErrNotNative = errors.New("sysctl key not served in-process")

// Native reads one sysctl key in-process (no process launch: every launch is
// inspected by endpoint security software) and returns the same text
// `sysctl -n <key>` prints, so the existing parsers apply unchanged.
type Native func(key string) (string, error)

// kernLoadavgSize is the size of the kernel's struct loadavg: 3 x uint32
// ldavg, 4 bytes of padding, int64 fscale.
const kernLoadavgSize = 24

// ParseKernLoadavg decodes the raw vm.loadavg value (a native-endian struct
// loadavg) and returns load1 = ldavg[0] / fscale. A short buffer or a
// non-positive fscale is an error.
func ParseKernLoadavg(b []byte) (float64, error) {
	if len(b) < kernLoadavgSize {
		return 0, fmt.Errorf("loadavg: struct is %d bytes, want %d", len(b), kernLoadavgSize)
	}
	fscale := int64(binary.NativeEndian.Uint64(b[16:24]))
	if fscale <= 0 {
		return 0, fmt.Errorf("loadavg: bad fscale %d", fscale)
	}
	return float64(binary.NativeEndian.Uint32(b[0:4])) / float64(fscale), nil
}

// loadavgText renders load1 the way `sysctl -n vm.loadavg` prints it.
func loadavgText(load1 float64) string {
	return "{ " + strconv.FormatFloat(load1, 'f', 4, 64) + " }\n"
}
