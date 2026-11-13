package host

import (
	"encoding/binary"
	"math"
	"testing"
)

func kernLoadavgFixture(ld [3]uint32, fscale int64) []byte {
	b := make([]byte, kernLoadavgSize)
	for i, v := range ld {
		binary.NativeEndian.PutUint32(b[i*4:], v)
	}
	binary.NativeEndian.PutUint64(b[16:], uint64(fscale))
	return b
}

func TestParseKernLoadavgMatchesSysctlText(t *testing.T) {
	got, err := ParseKernLoadavg(kernLoadavgFixture([3]uint32{6984, 5939, 5632}, 2048))
	if err != nil {
		t.Fatal(err)
	}
	if want := 6984.0 / 2048.0; got != want {
		t.Errorf("got %v, want %v", got, want)
	}
	// The rendered text goes through the existing text parser.
	back, err := ParseLoadAvg(loadavgText(got))
	if err != nil || math.Abs(back-got) > 0.0001 {
		t.Errorf("ParseLoadAvg(loadavgText(%v)) = %v, %v", got, back, err)
	}
	want, err := ParseLoadAvg("{ 3.41 2.90 2.75 }\n")
	if err != nil || math.Abs(got-want) > 0.005 {
		t.Errorf("got %v, sysctl text gives %v (%v)", got, want, err)
	}
}

func TestParseKernLoadavgNegativeControls(t *testing.T) {
	if _, err := ParseKernLoadavg(kernLoadavgFixture([3]uint32{1, 1, 1}, 2048)[:kernLoadavgSize-1]); err == nil {
		t.Error("short buffer parsed")
	}
	if _, err := ParseKernLoadavg(kernLoadavgFixture([3]uint32{1, 1, 1}, 0)); err == nil {
		t.Error("fscale 0 parsed")
	}
}

func TestSensorNativeWinsOverCommandAndFallsBack(t *testing.T) {
	r := newRig(t)
	r.s.Native = func(key string) (string, error) {
		if key == SysctlNCPU {
			return "4\n", nil
		}
		return "", ErrNotNative
	}
	got, err := r.s.sysctl(t.Context(), SysctlNCPU)
	if err != nil || got != "4\n" {
		t.Fatalf("native ncpu = %q, %v", got, err)
	}
	got, err = r.s.sysctl(t.Context(), SysctlLoad)
	if err != nil || got != r.load {
		t.Fatalf("fallback load = %q, %v; want %q", got, err, r.load)
	}
}
