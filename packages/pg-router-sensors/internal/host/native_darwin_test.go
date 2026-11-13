//go:build darwin

package host

import (
	"math"
	"strconv"
	"strings"
	"testing"
)

func TestSystemNativeReal(t *testing.T) {
	out, err := SystemNative(SysctlLoad)
	if err != nil {
		t.Fatal(err)
	}
	load, err := ParseLoadAvg(out)
	if err != nil || load < 0 || math.IsNaN(load) || math.IsInf(load, 0) {
		t.Errorf("load = %v, %v (text %q)", load, err, out)
	}
	out, err = SystemNative(SysctlNCPU)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := strconv.Atoi(strings.TrimSpace(out)); err != nil || n < 1 {
		t.Errorf("ncpu = %q, %v", out, err)
	}
	out, err = SystemNative(SysctlPressure)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParsePressure(out); err != nil {
		t.Errorf("pressure text %q: %v", out, err)
	}
	if _, err := SystemNative(SysctlSwap); err != ErrNotNative {
		t.Errorf("swap native err = %v, want ErrNotNative", err)
	}
}
