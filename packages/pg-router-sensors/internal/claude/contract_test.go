//go:build contract

package claude

import (
	"bytes"
	"context"
	"os/exec"
	"testing"
	"time"

	"github.com/phillipgreenii/x/cmdrun"

	"github.com/phillipgreenii/pg-router-sensors/internal/sensorcli"
)

// TestContractClaudeRealPaMonitor runs the sensor against the real
// `pa-monitor status --rate-limits`. It needs pa-monitor on PATH (and its
// daemon); without the binary it skips. A call that fails exits non-zero from
// the sensor (the cause is the test's evidence); a success must parse.
func TestContractClaudeRealPaMonitor(t *testing.T) {
	if _, err := exec.LookPath(DefaultCommand); err != nil {
		t.Skipf("%s not on PATH: %v", DefaultCommand, err)
	}
	s := New()
	s.MaxAge = 1000 * time.Hour // the daemon's last capture may be old; this test checks the contract, not freshness
	var eb bytes.Buffer
	obs, err := s.Measure(context.Background(), sensorcli.Env{Now: time.Now(), Stderr: &eb})
	if err != nil {
		t.Fatalf("pa-monitor status --rate-limits failed: %v", err)
	}
	t.Logf("%d readings; stderr: %s", len(obs), eb.String())
	for _, o := range obs {
		if o.Reading == nil && o.Level == "" {
			t.Errorf("empty observation %+v", o)
		}
		if o.Reading != nil && o.Reading.Unit != Unit {
			t.Errorf("unit %q", o.Reading.Unit)
		}
	}
	// The real output parses as the documented document.
	res := cmdrun.Exec(context.Background(), cmdrun.Call{Name: DefaultCommand, Args: RateLimitsArgs})
	if !res.OK() {
		t.Fatalf("second call: %+v", res)
	}
	if _, err := Parse(res.Stdout); err != nil {
		t.Fatal(err)
	}
}

// TestContractClaudeFailedCall runs the sensor against a command that fails.
func TestContractClaudeFailedCall(t *testing.T) {
	s := New()
	s.Command = "false"
	if _, err := s.Measure(context.Background(), sensorcli.Env{Now: time.Now(), Stderr: &bytes.Buffer{}}); err == nil {
		t.Fatal("want an error")
	}
}
