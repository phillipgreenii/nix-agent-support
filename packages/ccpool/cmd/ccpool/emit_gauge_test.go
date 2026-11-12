package main

import (
	"errors"
	"os"
	"reflect"
	"testing"

	"github.com/phillipgreenii/ccpool/internal/telemetry"
)

type gaugeSpy struct {
	names  []string
	values []float64
	labels []map[string]string
	err    error
}

func (s *gaugeSpy) record(n string, v float64, l map[string]string) error {
	s.names = append(s.names, n)
	s.values = append(s.values, v)
	s.labels = append(s.labels, l)
	return s.err
}

func devNull(t *testing.T) *os.File {
	t.Helper()
	f, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

func TestEmitGauge_recordsNameValueAndLabels(t *testing.T) {
	spy := &gaugeSpy{}
	code := emitGauge([]string{"--name", "x_last_success_timestamp_seconds", "--label", "pass=a", "--label", "role=r=s", "--value", "1.7e9"}, spy.record, devNull(t))
	if code != 0 || len(spy.names) != 1 {
		t.Fatalf("code=%d calls=%d", code, len(spy.names))
	}
	if spy.names[0] != "x_last_success_timestamp_seconds" || spy.values[0] != 1.7e9 {
		t.Errorf("got %q %v", spy.names[0], spy.values[0])
	}
	if want := map[string]string{"pass": "a", "role": "r=s"}; !reflect.DeepEqual(spy.labels[0], want) {
		t.Errorf("labels = %v, want %v", spy.labels[0], want)
	}
}

func TestEmitGauge_usageErrorsExit2AndRecordNothing(t *testing.T) {
	for _, args := range [][]string{
		nil,
		{"--name", "m"},
		{"--value", "1"},
		{"--name", "m", "--value", "abc"},
		{"--name", "m", "--value", "NaN"},
		{"--name", "m", "--value", "1", "--label", "nokv"},
		{"--name", "m", "--value", "1", "--label", "=v"},
		{"--bogus"},
	} {
		spy := &gaugeSpy{}
		if code := emitGauge(args, spy.record, devNull(t)); code != 2 || len(spy.names) != 0 {
			t.Errorf("args %q: code=%d calls=%d, want 2/0", args, code, len(spy.names))
		}
	}
}

func TestEmitGauge_recorderErrorIsNonZero(t *testing.T) {
	spy := &gaugeSpy{err: errors.New("boom")}
	if code := emitGauge([]string{"--name", "m", "--value", "1"}, spy.record, devNull(t)); code == 0 {
		t.Fatal("recorder error must not exit 0")
	}
}

// TestRunEmitGauge_noEndpointIsSilentNoop: with no OTLP endpoint Init
// installs no-op providers, so the real recorder succeeds and exits 0.
func TestRunEmitGauge_noEndpointIsSilentNoop(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	shutdown := telemetry.Init(t.Context())
	defer func() { _ = shutdown(t.Context()) }()
	if code := runEmitGauge([]string{"--name", "ccpool_test_gauge", "--label", "k=v", "--value", "3"}); code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
}

func TestEmitGauge_pointsRecordEveryPointInOrder(t *testing.T) {
	spy := &gaugeSpy{}
	code := emitGauge([]string{
		"--point", `{"name":"a","value":1}`,
		"--point", `{"name":"b","value":2.5,"labels":{"role":"r"}}`,
	}, spy.record, devNull(t))
	if code != 0 || !reflect.DeepEqual(spy.names, []string{"a", "b"}) || !reflect.DeepEqual(spy.values, []float64{1, 2.5}) {
		t.Fatalf("code=%d names=%v values=%v", code, spy.names, spy.values)
	}
	if want := map[string]string{"role": "r"}; !reflect.DeepEqual(spy.labels[1], want) {
		t.Errorf("labels = %v, want %v", spy.labels[1], want)
	}
}

func TestEmitGauge_badPointsExit2AndRecordNothing(t *testing.T) {
	for _, args := range [][]string{
		{"--point", "notjson"},
		{"--point", `{"value":1}`},
		{"--point", `{"name":"a","value":1}`, "--name", "m", "--value", "1"},
		{"--point", `{"name":"a","value":1}`, "--point", "{"},
	} {
		spy := &gaugeSpy{}
		if code := emitGauge(args, spy.record, devNull(t)); code != 2 || len(spy.names) != 0 {
			t.Errorf("args %q: code=%d calls=%d, want 2/0", args, code, len(spy.names))
		}
	}
}
