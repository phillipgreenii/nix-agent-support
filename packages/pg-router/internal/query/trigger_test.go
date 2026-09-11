package query

import (
	"testing"
	"time"
)

func TestTrigger_helpers(t *testing.T) {
	// nil defaults to period (an unconfigured query reproduces today's behavior).
	if !IsPeriod(nil) {
		t.Fatal("nil trigger must default to period")
	}
	if IsManual(nil) {
		t.Fatal("nil trigger must not be manual")
	}
	if !IsPeriod(PeriodTrigger{Every: time.Minute}) {
		t.Fatal("PeriodTrigger must be period")
	}
	if !IsManual(ManualTrigger{}) {
		t.Fatal("ManualTrigger must be manual")
	}
	if IsPeriod(ThresholdTrigger{Binds: []string{"x"}, Count: 1}) {
		t.Fatal("ThresholdTrigger must not be period")
	}
	tt, ok := Threshold(ThresholdTrigger{Binds: []string{"x"}, Count: 3})
	if !ok || tt.Count != 3 || len(tt.Binds) != 1 {
		t.Fatalf("Threshold() must extract the spec, got %+v ok=%v", tt, ok)
	}
	if _, ok := Threshold(PeriodTrigger{}); ok {
		t.Fatal("Threshold() must report false for a non-threshold trigger")
	}
}

func TestMeta_defaults(t *testing.T) {
	var m Meta
	if len(m.Emits()) != 0 {
		t.Fatal("zero Meta emits nothing")
	}
	if !IsPeriod(m.Trigger()) {
		t.Fatal("zero Meta trigger must default to period")
	}
}
