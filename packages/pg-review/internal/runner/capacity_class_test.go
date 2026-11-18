package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/ccpool"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/ledger"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/metrics"
)

func TestIsTransientClassification(t *testing.T) {
	cases := map[string]struct {
		err  error
		want bool
	}{
		"non-zero exit":     {&ccpool.ExitError{Cmd: "capacity", Code: 1}, true},
		"busy exit":         {&ccpool.ExitError{Cmd: "capacity", Code: ccpool.ExitBusy}, true},
		"killed call":       {&ccpool.ExitError{Cmd: "capacity", Code: -1}, true},
		"wrapped exit":      {fmt.Errorf("wrap: %w", &ccpool.ExitError{Cmd: "capacity", Code: 1}), true},
		"call timeout":      {fmt.Errorf("ccpool capacity: %w", context.DeadlineExceeded), true},
		"missing binary":    {&exec.Error{Name: "ccpool", Err: exec.ErrNotFound}, false},
		"decode error":      {fmt.Errorf("ccpool capacity: decode: %w", &json.SyntaxError{}), false},
		"unclassified fail": {errors.New("boom"), false},
	}
	for name, c := range cases {
		if got := ccpool.IsTransient(c.err); got != c.want {
			t.Errorf("%s: IsTransient = %v, want %v", name, got, c.want)
		}
	}
}

// A transient capacity failure retries and ends Skipped with the exact
// accurate reason, a log line and a counted infrastructure skip.
func TestTransientCapacityFailureReasonLogAndMetric(t *testing.T) {
	e := setupCcpool(t, ccCfg, 0)
	var log bytes.Buffer
	e.d.Log = &log
	boom := &ccpool.ExitError{Cmd: "capacity", Code: 1, Stderr: "recorder unreachable"}
	e.fake.capErrs = []error{boom, boom, boom}
	_, err := e.runLens(t)
	var sk *SkippedError
	if !errors.As(err, &sk) {
		t.Fatalf("want a skipped run, got %v", err)
	}
	want := "ccpool capacity check failed 3 times (ccpool or its recorder was unreachable or timed out; the next scheduled run retries): " + boom.Error()
	if sk.Reason != want {
		t.Errorf("reason %q, want %q", sk.Reason, want)
	}
	if strings.Contains(sk.Reason, "likely transient") {
		t.Errorf("reason carries the retired wording: %q", sk.Reason)
	}
	if e.fake.capCalls != 3 {
		t.Errorf("capacity tried %d times, want 3", e.fake.capCalls)
	}
	wantLog := fmt.Sprintf("pg-review: infrastructure skip run=%d lens=agent-workflow scope=demo-scope reason_class=capacity\n", sk.RunID)
	if log.String() != wantLog {
		t.Errorf("log %q, want %q", log.String(), wantLog)
	}
	md, err := e.l.MetricsData(bg)
	if err != nil {
		t.Fatal(err)
	}
	gs := metrics.Collect(md, time.Now())
	found := false
	for _, g := range gs {
		if g.Name == "pg_review_infrastructure_skipped_runs" && g.Labels["reason"] == "capacity" &&
			g.Labels["lens"] == "agent-workflow" && g.Labels["scope"] == "demo-scope" && g.Value == 1 {
			found = true
		}
	}
	if !found {
		t.Errorf("no infrastructure skip gauge of 1 for the capacity class in %+v", gs)
	}
}

// A permanent fault is not retried and ends Failed with an accurate message.
func TestPermanentCapacityFaultFailsWithoutRetry(t *testing.T) {
	cases := map[string]error{
		"missing binary": &exec.Error{Name: "ccpool", Err: exec.ErrNotFound},
		"decode error":   fmt.Errorf("ccpool capacity: decode: %w", &json.SyntaxError{}),
	}
	for name, cause := range cases {
		t.Run(name, func(t *testing.T) {
			e := setupCcpool(t, ccCfg, 0)
			var log bytes.Buffer
			e.d.Log = &log
			e.fake.capErrs = []error{cause, cause, cause}
			_, err := e.runLens(t)
			var sk *SkippedError
			if err == nil || errors.As(err, &sk) {
				t.Fatalf("want a failed run, got %v", err)
			}
			wantMsg := "ccpool capacity check failed with a permanent fault (not retried; check the ccpool install): " + cause.Error()
			if err.Error() != wantMsg {
				t.Errorf("message %q, want %q", err.Error(), wantMsg)
			}
			if strings.Contains(err.Error(), "likely transient") {
				t.Errorf("message carries the retired wording: %q", err.Error())
			}
			if e.fake.capCalls != 1 {
				t.Errorf("capacity tried %d times, want 1", e.fake.capCalls)
			}
			r := lastRun(t, e)
			if r.State != ledger.RunFailed {
				t.Errorf("state %s, want Failed", r.State)
			}
			if log.Len() != 0 {
				t.Errorf("a failed run logged an infrastructure skip: %q", log.String())
			}
		})
	}
}

// A Skipped infrastructure run leaves the lens pause state unchanged.
func TestInfrastructureSkipLeavesPauseStateAlone(t *testing.T) {
	e := setupCcpool(t, ccCfg, 0)
	before, err := e.l.IsLensPaused(bg, "agent-workflow")
	if err != nil {
		t.Fatal(err)
	}
	boom := &ccpool.ExitError{Cmd: "capacity", Code: 1}
	e.fake.capErrs = []error{boom, boom, boom}
	var sk *SkippedError
	if _, err := e.runLens(t); !errors.As(err, &sk) {
		t.Fatalf("want a skipped run, got %v", err)
	}
	after, err := e.l.IsLensPaused(bg, "agent-workflow")
	if err != nil {
		t.Fatal(err)
	}
	if before || after {
		t.Errorf("paused before %v, after %v; want both false", before, after)
	}
}
