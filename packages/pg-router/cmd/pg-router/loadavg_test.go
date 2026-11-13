package main

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
	// ldavg {3.41, 2.90, 2.75} at fscale 2048 is "{ 3.41 2.90 2.75 }".
	b := kernLoadavgFixture([3]uint32{6984, 5939, 5632}, 2048)
	got, ok := parseKernLoadavg(b)
	if !ok {
		t.Fatal("parseKernLoadavg not ok")
	}
	want, _ := parseSysctlLoadavg("{ 3.41 2.90 2.75 }\n")
	if math.Abs(got-want) > 0.005 {
		t.Errorf("got %v, want about %v", got, want)
	}
	if exact := 6984.0 / 2048.0; got != exact {
		t.Errorf("got %v, want %v", got, exact)
	}
}

func TestParseKernLoadavgNegativeControls(t *testing.T) {
	if _, ok := parseKernLoadavg(kernLoadavgFixture([3]uint32{1, 1, 1}, 2048)[:kernLoadavgSize-1]); ok {
		t.Error("short buffer parsed")
	}
	if _, ok := parseKernLoadavg(nil); ok {
		t.Error("nil buffer parsed")
	}
	if _, ok := parseKernLoadavg(kernLoadavgFixture([3]uint32{1, 1, 1}, 0)); ok {
		t.Error("fscale 0 parsed")
	}
}
