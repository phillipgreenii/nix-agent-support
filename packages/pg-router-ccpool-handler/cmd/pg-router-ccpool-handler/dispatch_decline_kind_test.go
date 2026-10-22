package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/executor"
	"github.com/phillipgreenii/pg-router/conformance"
	"github.com/phillipgreenii/pg-router/schemas"
)

// The decline body's kind and detail (INV-CCH-34, INV-FAIL-1; the decline
// branch of handler.dispatch-reply.schema.json): busy for routine
// backpressure, fault for a condition worth an operator's look, neutral for a
// decline that says nothing about the dispatch's health.

func TestBusyDeclineFor_kindPerReason(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		reason     string
		kind       string
		wantDetail bool
	}{
		{"at-capacity", fmt.Errorf("%w: counted=1", executor.ErrPoolAtCapacity), "at-capacity", "busy", false},
		{"session-in-flight", fmt.Errorf("%w: s", executor.ErrSessionInFlight), "session-in-flight", "busy", false},
		{"command-busy", fmt.Errorf("x: %w", executor.ErrBusy), "command-busy", "busy", false},
		{"capacity-unknown", fmt.Errorf("%w: boom", executor.ErrPoolCapacityUnknown), "capacity-unknown", "fault", false},
		{"pool-unreadable", fmt.Errorf("%w: boom", executor.ErrPoolUnreadable), "pool-unreadable", "fault", false},
		{"low-disk", fmt.Errorf("isolation: %w", executor.ErrLowDisk), "low-disk", "fault", false},
		{"launch-failed", &executor.LaunchFailedError{Tail: "ccpool new: did not reach ready", Err: errors.New("x")}, "launch-failed", "fault", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d, ok := busyDeclineFor(c.err)
			if !ok {
				t.Fatalf("%v must be a decline", c.err)
			}
			if d.Reason != c.reason || d.Kind != c.kind {
				t.Fatalf("decline = %+v, want reason %q kind %q", d, c.reason, c.kind)
			}
			if (d.Detail != "") != c.wantDetail {
				t.Fatalf("detail = %q, want present=%v", d.Detail, c.wantDetail)
			}
		})
	}
}

func TestNeutralDecline_originUnavailableAndSkipped(t *testing.T) {
	for _, reason := range []string{busyReasonOriginUnavailable, executor.SkipBeadClosed, executor.SkipPRMerged, executor.SkipPendingReview, executor.SkipBeadNotOpen, executor.SkipBeadClaimed, executor.SkipBeadHuman} {
		d := neutralDecline(reason, "")
		if d.Reason != reason || d.Kind != "neutral" || d.Detail != "" {
			t.Errorf("neutralDecline(%q) = %+v", reason, d)
		}
	}
}

// The launch-failed detail is the redacted ccpool new error text, cut by bytes at
// a UTF-8 boundary to the schema's bound.
func TestBusyDeclineFor_launchFailedDetailIsBoundedAndRedacted(t *testing.T) {
	secret := "Bearer abcdefghijklmnopqrstuvwxyz0123456789ABCDEF"
	long := "ccpool new: " + secret + " " + strings.Repeat("é", schemas.DeclineDetailMaxLen) + " the diagnosis" // 2-byte runes
	d, ok := busyDeclineFor(&executor.LaunchFailedError{Tail: long, Err: errors.New("x")})
	if !ok {
		t.Fatal("must decline")
	}
	if strings.Contains(d.Detail, "abcdefghijklmnopqrstuvwxyz") {
		t.Errorf("detail must be redacted: %q", d.Detail)
	}
	if len(d.Detail) > schemas.DeclineDetailMaxLen {
		t.Errorf("detail is %d bytes, bound is %d", len(d.Detail), schemas.DeclineDetailMaxLen)
	}
	if !utf8.ValidString(d.Detail) {
		t.Errorf("the cut must not split a UTF-8 sequence: %q", d.Detail)
	}
	if !strings.HasSuffix(d.Detail, " the diagnosis") {
		t.Errorf("the END of the error text is kept (the stderr diagnosis): %q", d.Detail)
	}
	if len(d.Detail) < schemas.DeclineDetailMaxLen-3 {
		t.Errorf("the detail should use the bound, got %d bytes", len(d.Detail))
	}
}

func TestBoundedDetail_edges(t *testing.T) {
	if got := boundedDetail("  short  "); got != "short" {
		t.Errorf("boundedDetail trims: %q", got)
	}
	if got := boundedDetail(""); got != "" {
		t.Errorf("empty stays empty: %q", got)
	}
	// Words, not one run, so the redactor's generic long-token pattern leaves it.
	exact := strings.Repeat("ab ", schemas.DeclineDetailMaxLen/3) + "ab"
	exact = exact[:schemas.DeclineDetailMaxLen]
	for name, f := range map[string]func(string) string{"head": boundedDetail, "tail": boundedTailDetail} {
		if got := f(exact); got != strings.TrimSpace(exact) {
			t.Errorf("%s: a detail at the bound is kept whole, got %d bytes", name, len(got))
		}
		if got := f(exact + " zz"); len(got) > schemas.DeclineDetailMaxLen {
			t.Errorf("%s: a detail over the bound is cut to it, got %d bytes", name, len(got))
		}
	}
	if got := boundedDetail(exact + " zz"); strings.HasSuffix(got, "zz") {
		t.Errorf("head cut keeps the start: %q", got)
	}
	if got := boundedTailDetail(exact + " zz"); !strings.HasSuffix(got, "zz") {
		t.Errorf("tail cut keeps the end: %q", got)
	}
}

func TestWriteBusyReply_bodyMatchesSchemaAndCarriesKind(t *testing.T) {
	for _, d := range []busyDecline{
		{Reason: "at-capacity", Kind: "busy"},
		{Reason: "launch-failed", Kind: "fault", Detail: "ccpool new: boom"},
		neutralDecline("skipped-bead-closed", "the review bead is already closed"),
	} {
		var sb strings.Builder
		writeBusyReply(&sb, d)
		if err := schemas.ValidateBytes("handler.dispatch-reply", []byte(sb.String())); err != nil {
			t.Fatalf("body %s does not match the schema: %v", sb.String(), err)
		}
		var body map[string]any
		if err := json.Unmarshal([]byte(sb.String()), &body); err != nil {
			t.Fatal(err)
		}
		if body["reason"] != d.Reason || body["kind"] != d.Kind {
			t.Errorf("body = %v, want reason %q kind %q", body, d.Reason, d.Kind)
		}
		if _, has := body["detail"]; has != (d.Detail != "") {
			t.Errorf("detail present = %v for %+v", has, d)
		}
	}
}

// TestRunDispatch_launchFailedExitsBusyFault drives the whole path with a stub
// ccpool whose `new` fails: the dispatch exits busy (9) with reason
// launch-failed, kind fault and a redacted detail, purges the abandoned session
// (`close`) and never writes a bead label (no bd is on PATH, so a mutation
// would only show as an error; the unit tests assert the bead is untouched).
func TestRunDispatch_launchFailedExitsBusyFault(t *testing.T) {
	bin := t.TempDir()
	log := filepath.Join(bin, "calls.log")
	stub := `#!/bin/sh
echo "$@" >> ` + log + `
case "$1" in
  list) echo '[]'; exit 0;;
  capacity) echo '{"max_sessions":4,"free":4,"counted":0,"live":0,"preserved":0}'; exit 0;;
  new) echo "did not reach ready (state=starting) Bearer abcdefghijklmnopqrstuvwxyz0123456789ABCDEF" >&2; exit 1;;
  close) exit 0;;
esac
echo "stub ccpool: $1 unavailable" >&2
exit 1
`
	if err := os.WriteFile(filepath.Join(bin, "ccpool"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":/usr/bin:/bin")

	dir := t.TempDir()
	rolePath := filepath.Join(dir, "role.json")
	roleJSON := `{"name":"worker","type":"ccpool","ccpool":{"actor":"test-actor","completion":"close-only","onFailure":"unclaim","onDispatchFail":"unclaim","promptBody":"hello","isolation":{"Type":"none"}}}`
	if err := os.WriteFile(rolePath, []byte(roleJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	restoreIn := redirectStdin(t, `{"schemaVersion":"1","id":"d-1","event":{"id":"e-1","type":"dispatch","payload":{"id":"zr-w"}}}`)
	defer restoreIn()

	var code int
	got := captureStdout(t, func() { code = runDispatch([]string{"--role-config", rolePath}) })

	if code != conformance.ExitBusy {
		t.Fatalf("exit = %d, want ExitBusy (%d); stdout=%q", code, conformance.ExitBusy, got)
	}
	if err := schemas.ValidateBytes("handler.dispatch-reply", []byte(got)); err != nil {
		t.Fatalf("body %q does not match the schema: %v", got, err)
	}
	var reply struct {
		Reason string `json:"reason"`
		Kind   string `json:"kind"`
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal([]byte(got), &reply); err != nil {
		t.Fatal(err)
	}
	if reply.Reason != "launch-failed" || reply.Kind != "fault" {
		t.Fatalf("reply = %+v, want launch-failed/fault", reply)
	}
	if !strings.Contains(reply.Detail, "did not reach ready") || strings.Contains(reply.Detail, "abcdefghijklmnopqrstuvwxyz") {
		t.Errorf("detail = %q, want the redacted ccpool new error", reply.Detail)
	}
	calls, _ := os.ReadFile(log)
	if !strings.Contains(string(calls), "close ") {
		t.Errorf("the abandoned session must be purge-closed; ccpool calls:\n%s", calls)
	}
}
