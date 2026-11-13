//go:build darwin

package main

import (
	"math"
	"testing"
)

func TestSysctlLoad1Real(t *testing.T) {
	v, ok := sysctlLoad1()
	if !ok {
		t.Fatal("sysctlLoad1 not ok on darwin")
	}
	if v < 0 || math.IsNaN(v) || math.IsInf(v, 0) {
		t.Errorf("load1 = %v, want finite and non-negative", v)
	}
}
