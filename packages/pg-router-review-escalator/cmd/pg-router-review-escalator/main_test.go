package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router-review-escalator/internal/adapters"
)

// script is a fake Runner that answers by command shape and records every call.
type script struct {
	calls []string
	// submit is the answer to `pr review submit`.
	submit    adapters.Result
	submitErr error
	// openBeads is the answer to `issue list`.
	openBeads string
	// failNotify makes the push command exit non-zero.
	failNotify bool
	// failCreate makes `issue create` exit non-zero.
	failCreate bool
	// stdin of the submit call.
	submitStdin string
}

func (s *script) Run(_ context.Context, name string, args []string, stdin []byte, _ []string) (adapters.Result, error) {
	s.calls = append(s.calls, name+" "+strings.Join(args, " "))
	switch {
	case name == "push" || name == "notify-cmd":
		if s.failNotify {
			return adapters.Result{ExitCode: 1, Stderr: []byte("channel down")}, nil
		}
		return adapters.Result{}, nil
	case len(args) >= 3 && args[0] == "pr" && args[1] == "review" && args[2] == "submit":
		s.submitStdin = string(stdin)
		return s.submit, s.submitErr
	case len(args) >= 2 && args[0] == "issue" && args[1] == "list":
		out := s.openBeads
		if out == "" {
			out = `{"entities":[]}`
		}
		return adapters.Result{Stdout: []byte(out)}, nil
	case len(args) >= 2 && args[0] == "issue" && args[1] == "create":
		if s.failCreate {
			return adapters.Result{ExitCode: 1, Stderr: []byte("tracker down")}, nil
		}
		return adapters.Result{Stdout: []byte(`{"result":{"id":"bd-7"}}`)}, nil
	default:
		return adapters.Result{Stdout: []byte(`{"result":{}}`)}, nil
	}
}

func (s *script) did(sub string) bool {
	for _, c := range s.calls {
		if strings.Contains(c, sub) {
			return true
		}
	}
	return false
}

type harness struct {
	s              *script
	stdin          string
	stdout, stderr bytes.Buffer
}

func (h *harness) run(args ...string) int {
	d := deps{
		runner: func(time.Duration) adapters.Runner { return h.s },
		now:    func() time.Time { return time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC) },
		stdin:  strings.NewReader(h.stdin),
		stdout: &h.stdout,
		stderr: &h.stderr,
	}
	return run(context.Background(), args, d)
}

const (
	blockedOut = `{"result":{"status":"blocked_human_pending","reason":"human_edited","message":"left untouched","head_sha":"abc1234","pending_review":{"url":"https://example.test/r/1"}}}`
	postedOut  = `{"result":{"status":"posted","review_id":"R1","state":"pending","head_sha":"abc1234"}}`
)

var notifyArgs = []string{"--notify-arg", "push", "--notify-arg", "{title}", "--notify-arg", "{body}"}

func with(args ...string) []string { return append(append([]string{}, args...), notifyArgs...) }

func TestSubmitBlockedEscalatesAndPassesOutputThrough(t *testing.T) {
	h := &harness{s: &script{submit: adapters.Result{Stdout: []byte(blockedOut)}}, stdin: `{"head_sha":"abc1234"}`}
	code := h.run(with("submit", "--label", "repo-x", "--priority", "P1", "--submit-backend", "gh", "acme/api#5")...)
	if code != exitOK {
		t.Fatalf("exit = %d, stderr:\n%s", code, h.stderr.String())
	}
	if h.stdout.String() != blockedOut {
		t.Errorf("stdout changed: %q", h.stdout.String())
	}
	if h.s.submitStdin != `{"head_sha":"abc1234"}` {
		t.Errorf("request not forwarded: %q", h.s.submitStdin)
	}
	if !h.s.did("pr review submit acme/api#5 --backend gh") {
		t.Errorf("calls = %q", h.s.calls)
	}
	if !h.s.did("issue create") || !h.s.did("--priority P1") || !h.s.did("--labels repo-x") || !h.s.did("--labels human-focus-required") {
		t.Errorf("bead not created as configured: %q", h.s.calls)
	}
	if !h.s.did("push Pending review stuck: acme/api#5") {
		t.Errorf("no notification: %q", h.s.calls)
	}
	if !strings.Contains(h.stderr.String(), "created bd-7") {
		t.Errorf("stderr lacks the action log: %q", h.stderr.String())
	}
}

func TestSubmitPostedClosesOpenEscalation(t *testing.T) {
	open := `{"entities":[{"id":"bd-3","metadata":{"review_escalation_kind":"pr","review_escalation_pr":"acme/api#5","review_escalation_key":"pr:acme/api#5"}}]}`
	h := &harness{s: &script{submit: adapters.Result{Stdout: []byte(postedOut)}, openBeads: open}}
	if code := h.run("submit", "acme/api#5"); code != exitOK {
		t.Fatalf("exit = %d, stderr:\n%s", code, h.stderr.String())
	}
	if !h.s.did("issue close bd-3") {
		t.Errorf("escalation not closed: %q", h.s.calls)
	}
	if h.s.did("push") {
		t.Errorf("a resolve must not notify")
	}
}

func TestSubmitAppendAndNoChangeCloseOpenEscalation(t *testing.T) {
	open := `{"entities":[{"id":"bd-3","metadata":{"review_escalation_kind":"pr","review_escalation_pr":"acme/api#5","review_escalation_key":"pr:acme/api#5"}}]}`
	for _, status := range []string{"append", "no_change"} {
		t.Run(status, func(t *testing.T) {
			out := `{"result":{"status":"` + status + `","head_sha":"abc","review_id":"r1","state":"pending"}}`
			h := &harness{s: &script{submit: adapters.Result{Stdout: []byte(out)}, openBeads: open}}
			if code := h.run("submit", "acme/api#5"); code != exitOK {
				t.Fatalf("exit = %d, stderr:\n%s", code, h.stderr.String())
			}
			if strings.Contains(h.stderr.String(), "unknown status") {
				t.Errorf("status %s rejected: %q", status, h.stderr.String())
			}
			if !h.s.did("issue close bd-3") {
				t.Errorf("escalation not closed: %q", h.s.calls)
			}
			if h.s.did("push") {
				t.Errorf("a resolve must not notify")
			}
		})
	}
}

func TestSubmitFailurePassesConnectorCodeThroughWithoutEscalating(t *testing.T) {
	for _, tc := range []struct{ in, want int }{{4, 4}, {1, 1}, {9, 1}} {
		h := &harness{s: &script{submit: adapters.Result{ExitCode: tc.in, Stdout: []byte(`{"error":{"code":"x","message":"y"}}`)}}}
		if code := h.run("submit", "acme/api#5"); code != tc.want {
			t.Errorf("connector exit %d -> %d, want %d", tc.in, code, tc.want)
		}
		if h.s.did("issue") {
			t.Errorf("a failed submit touched the tracker: %q", h.s.calls)
		}
		if !strings.Contains(h.stdout.String(), `"error"`) {
			t.Errorf("stdout not forwarded")
		}
	}
}

func TestSubmitUnrecognizableOutputIsLoud(t *testing.T) {
	h := &harness{s: &script{submit: adapters.Result{Stdout: []byte(`{"result":{"review_id":"R"}}`)}}}
	if code := h.run("submit", "acme/api#5"); code != exitError {
		t.Fatalf("exit = %d, want %d", code, exitError)
	}
	if !strings.Contains(h.stderr.String(), "no status") {
		t.Errorf("stderr = %q", h.stderr.String())
	}
	if h.stdout.String() == "" {
		t.Errorf("the connector output must still be forwarded")
	}
}

func TestSubmitStartFailure(t *testing.T) {
	h := &harness{s: &script{submitErr: errors.New("cannot start")}}
	if code := h.run("submit", "acme/api#5"); code != exitError {
		t.Fatalf("exit = %d", code)
	}
}

func TestDeliveryFailureExitsNonZeroAndLogsEachPath(t *testing.T) {
	h := &harness{s: &script{submit: adapters.Result{Stdout: []byte(blockedOut)}, failCreate: true, failNotify: true}}
	code := h.run(with("submit", "acme/api#5")...)
	if code != exitDelivery {
		t.Fatalf("exit = %d, want %d", code, exitDelivery)
	}
	for _, want := range []string{"ESCALATION FAILED", "tracker: create", "notify:", "tracker down", "channel down"} {
		if !strings.Contains(h.stderr.String(), want) {
			t.Errorf("stderr lacks %q:\n%s", want, h.stderr.String())
		}
	}
	if h.stdout.String() != blockedOut {
		t.Errorf("the submit output must still reach the caller")
	}
}

func TestNoNotifyCommandConfiguredFailsLoudly(t *testing.T) {
	h := &harness{s: &script{submit: adapters.Result{Stdout: []byte(blockedOut)}}}
	if code := h.run("submit", "acme/api#5"); code != exitDelivery {
		t.Fatalf("exit = %d, want %d", code, exitDelivery)
	}
	if !strings.Contains(h.stderr.String(), "no notify command") {
		t.Errorf("stderr = %q", h.stderr.String())
	}
}

func TestReport(t *testing.T) {
	h := &harness{s: &script{}, stdin: blockedOut}
	if code := h.run(with("report", "acme/api#5")...); code != exitOK {
		t.Fatalf("exit = %d, stderr:\n%s", code, h.stderr.String())
	}
	if h.s.did("pr review submit") {
		t.Errorf("report must not run submit")
	}
	if !h.s.did("issue create") || !h.s.did("push") {
		t.Errorf("calls = %q", h.s.calls)
	}

	h = &harness{s: &script{}, stdin: `{"error":{"code":"unavailable","message":"down"}}`}
	if code := h.run("report", "acme/api#5"); code != exitError {
		t.Errorf("an error envelope is not an outcome: exit = %d", code)
	}
}

func TestInterleavedFlagsAndPositional(t *testing.T) {
	h := &harness{s: &script{}, stdin: postedOut}
	if code := h.run("report", "acme/api#5", "--label", "x", "--rollup-threshold", "5"); code != exitOK {
		t.Fatalf("exit = %d, stderr:\n%s", code, h.stderr.String())
	}
}

func TestUsageErrors(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"no args", nil},
		{"unknown subcommand", []string{"frobnicate"}},
		{"missing pr id", []string{"submit"}},
		{"two pr ids", []string{"report", "a", "b"}},
		{"bad flag", []string{"report", "--nope", "a"}},
		{"bad duration", []string{"report", "--renotify-interval", "soon", "a"}},
		{"non-positive interval", []string{"report", "--renotify-interval", "0s", "a"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := &harness{s: &script{}}
			if code := h.run(tc.args...); code != exitUsage {
				t.Errorf("exit = %d, want %d (stderr %q)", code, exitUsage, h.stderr.String())
			}
			if len(h.s.calls) != 0 {
				t.Errorf("a usage error ran something: %q", h.s.calls)
			}
		})
	}
}

func TestVersionAndHelp(t *testing.T) {
	h := &harness{s: &script{}}
	if code := h.run("--version"); code != exitOK || strings.TrimSpace(h.stdout.String()) != Version {
		t.Errorf("version: exit %d out %q", code, h.stdout.String())
	}
	h = &harness{s: &script{}}
	if code := h.run("--help"); code != exitOK || !strings.Contains(h.stdout.String(), "usage:") {
		t.Errorf("help: exit %d", code)
	}
}

func TestSplitErrors(t *testing.T) {
	err := errors.Join(fmt.Errorf("a"), errors.Join(fmt.Errorf("b"), fmt.Errorf("c")))
	if got := splitErrors(err); strings.Join(got, ",") != "a,b,c" {
		t.Errorf("got %v", got)
	}
}
