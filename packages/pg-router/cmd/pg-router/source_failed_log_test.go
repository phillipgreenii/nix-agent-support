package main

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router/internal/discover"
)

// The producer-tick "source failed" WARN carries elapsed, argv, attempts and
// load1 (bead pg2-zdowv); the load average parsers cover both host families.

func TestSourceFailedAttrs_ElapsedArgvAttemptsAndLoad(t *testing.T) {
	prev := load1
	load1 = func() (float64, bool) { return 7.25, true }
	t.Cleanup(func() { load1 = prev })

	var buf bytes.Buffer
	l := slog.New(slog.NewTextHandler(&buf, nil))
	l.Warn("producer tick: source failed; other sources still produced",
		sourceFailedAttrs("pr-mine", errors.New("signal: killed"), discover.FailureInfo{
			Count: 2, Elapsed: 30*time.Second + 12*time.Millisecond, Argv: []string{"/bin/x", "changes", "pr"},
		})...)
	out := buf.String()
	for _, want := range []string{"source=pr-mine", "elapsed=30.012s", `argv="/bin/x changes pr"`, "attempts=2", "load1=7.25", `err="signal: killed"`} {
		if !strings.Contains(out, want) {
			t.Errorf("WARN missing %q:\n%s", want, out)
		}
	}
}

func TestSourceFailedAttrs_NonGiveUpOmitsAttemptDetail(t *testing.T) {
	prev := load1
	load1 = func() (float64, bool) { return 0, false }
	t.Cleanup(func() { load1 = prev })

	attrs := sourceFailedAttrs("s", errors.New("e"), discover.FailureInfo{})
	if len(attrs) != 4 {
		t.Fatalf("attrs = %v, want only source and err when there is no attempt detail and no load", attrs)
	}
}

func TestParseLoadavg(t *testing.T) {
	if v, ok := parseProcLoadavg("0.52 0.58 0.59 1/467 12345\n"); !ok || v != 0.52 {
		t.Errorf("proc loadavg = %v %v", v, ok)
	}
	if v, ok := parseSysctlLoadavg("{ 3.41 2.90 2.75 }\n"); !ok || v != 3.41 {
		t.Errorf("sysctl loadavg = %v %v", v, ok)
	}
	for _, bad := range []string{"", "   ", "not-a-number x"} {
		if _, ok := parseProcLoadavg(bad); ok {
			t.Errorf("parseProcLoadavg(%q) ok, want false", bad)
		}
		if _, ok := parseSysctlLoadavg(bad); ok {
			t.Errorf("parseSysctlLoadavg(%q) ok, want false", bad)
		}
	}
}
