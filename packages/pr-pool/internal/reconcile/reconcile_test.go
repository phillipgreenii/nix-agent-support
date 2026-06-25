package reconcile

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/phillipgreenii/pr-pool/internal/beads"
)

// fakeBR is a fake beads.Runner: it returns canned JSON keyed by the joined args,
// or a single error for every call. It mirrors cmd/pr-pool's fakeBR so the
// reconcile guard is testable without touching a real bd.
type fakeBR struct {
	out map[string]string
	err error
}

func (f fakeBR) Run(_ context.Context, args ...string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return f.out[strings.Join(args, " ")], nil
}

// envelope wraps issues in bd's {"data":[...]} response shape.
func envelope(issues ...beads.Issue) string {
	b, _ := json.Marshal(struct {
		Data []beads.Issue `json:"data"`
	}{Data: issues})
	return string(b)
}

// listOpenFeedbackKey is the exact bd argv reconcile uses to enumerate open
// process-feedback cycles (beads.List appends --json --limit 0).
const listOpenFeedbackKey = "list --status open --json --limit 0"

func showKey(id string) string { return "show " + id + " --json" }

// captureWarnings installs a slog handler that records WARN-level messages for
// the duration of fn, then restores the previous default logger.
func captureWarnings(t *testing.T, fn func()) []string {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	defer slog.SetDefault(prev)
	fn()
	var warns []string
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		if strings.Contains(line, "level=WARN") {
			warns = append(warns, line)
		}
	}
	return warns
}

// TestStrandedSelfCycles_detectsUnstampedSelfCycle is the core positive case:
// an open process-feedback cycle whose parent author == self but which LACKS the
// `mine` label is STRANDED — discovery (bd ready --label mine) silently skips it.
// The guard must detect it, return it, and emit a WARN (loud, observable signal).
func TestStrandedSelfCycles_detectsUnstampedSelfCycle(t *testing.T) {
	self := "phillipg"
	cycle := beads.Issue{
		ID: "zr-100", Title: "process-feedback: PR 42", Type: "task",
		Status: "open", Parent: "zr-10", Labels: nil, // no `mine`
	}
	parent := beads.Issue{
		ID: "zr-10", Title: "merge-request: PR 42", Type: "merge-request",
		Status: "open", Metadata: map[string]any{"author": self},
	}
	br := fakeBR{out: map[string]string{
		listOpenFeedbackKey: envelope(cycle),
		showKey("zr-10"):    envelope(parent),
	}}

	var got []string
	warns := captureWarnings(t, func() {
		var err error
		got, err = StrandedSelfCycles(context.Background(), br, self)
		if err != nil {
			t.Fatalf("StrandedSelfCycles error: %v", err)
		}
	})

	if len(got) != 1 || got[0] != "zr-100" {
		t.Fatalf("expected stranded=[zr-100], got %v", got)
	}
	if len(warns) == 0 {
		t.Fatalf("expected a WARN for the stranded self-cycle, got none")
	}
	joined := strings.Join(warns, "\n")
	if !strings.Contains(joined, "zr-100") {
		t.Errorf("WARN should name the stranded cycle zr-100; got %q", joined)
	}
}

// TestStrandedSelfCycles_noFalsePositiveForTeamCycle asserts NO false positive
// for a genuine team cycle: parent author != self. It is correctly unstamped and
// must NOT be flagged (it is not the pool's work).
func TestStrandedSelfCycles_noFalsePositiveForTeamCycle(t *testing.T) {
	self := "phillipg"
	cycle := beads.Issue{
		ID: "zr-200", Title: "process-feedback: PR 99", Type: "task",
		Status: "open", Parent: "zr-20", Labels: nil,
	}
	parent := beads.Issue{
		ID: "zr-20", Title: "merge-request: PR 99", Type: "merge-request",
		Status: "open", Metadata: map[string]any{"author": "someone-else"},
	}
	br := fakeBR{out: map[string]string{
		listOpenFeedbackKey: envelope(cycle),
		showKey("zr-20"):    envelope(parent),
	}}

	var got []string
	warns := captureWarnings(t, func() {
		var err error
		got, err = StrandedSelfCycles(context.Background(), br, self)
		if err != nil {
			t.Fatalf("StrandedSelfCycles error: %v", err)
		}
	})

	if len(got) != 0 {
		t.Fatalf("team cycle (parent author != self) must NOT be flagged; got %v", got)
	}
	if len(warns) != 0 {
		t.Errorf("team cycle must not produce a WARN; got %v", warns)
	}
}

// TestStrandedSelfCycles_noFalsePositiveForStampedSelfCycle asserts NO false
// positive for an already-stamped self cycle: parent author == self AND it
// carries `mine`. Discovery already picks it up; it is not stranded.
func TestStrandedSelfCycles_noFalsePositiveForStampedSelfCycle(t *testing.T) {
	self := "phillipg"
	cycle := beads.Issue{
		ID: "zr-300", Title: "process-feedback: PR 7", Type: "task",
		Status: "open", Parent: "zr-30", Labels: []string{"mine"},
	}
	parent := beads.Issue{
		ID: "zr-30", Title: "merge-request: PR 7", Type: "merge-request",
		Status: "open", Metadata: map[string]any{"author": self},
	}
	br := fakeBR{out: map[string]string{
		listOpenFeedbackKey: envelope(cycle),
		showKey("zr-30"):    envelope(parent),
	}}

	var got []string
	warns := captureWarnings(t, func() {
		var err error
		got, err = StrandedSelfCycles(context.Background(), br, self)
		if err != nil {
			t.Fatalf("StrandedSelfCycles error: %v", err)
		}
	})

	if len(got) != 0 {
		t.Fatalf("already-stamped self cycle must NOT be flagged; got %v", got)
	}
	if len(warns) != 0 {
		t.Errorf("stamped self cycle must not produce a WARN; got %v", warns)
	}
}

// TestStrandedSelfCycles_ignoresNonFeedbackBeads asserts beads that are not
// `process-feedback:` cycles (by title prefix) are ignored even if self-authored
// and unstamped — the guard scopes strictly to feedback cycles.
func TestStrandedSelfCycles_ignoresNonFeedbackBeads(t *testing.T) {
	self := "phillipg"
	work := beads.Issue{
		ID: "zr-400", Title: "implement the thing", Type: "task",
		Status: "open", Parent: "zr-40", Labels: nil,
	}
	br := fakeBR{out: map[string]string{
		listOpenFeedbackKey: envelope(work),
		// no show for zr-40: it must never be consulted (not a feedback cycle)
	}}

	got, err := StrandedSelfCycles(context.Background(), br, self)
	if err != nil {
		t.Fatalf("StrandedSelfCycles error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("non-feedback bead must be ignored; got %v", got)
	}
}

// TestStrandedSelfCycles_listErrorPropagates asserts a bd failure does NOT
// masquerade as "nothing stranded" — it must propagate (same contract as
// discovery, pg2-qq9v).
func TestStrandedSelfCycles_listErrorPropagates(t *testing.T) {
	sentinel := errors.New("bd down")
	_, err := StrandedSelfCycles(context.Background(), fakeBR{err: sentinel}, "phillipg")
	if err == nil {
		t.Fatal("a bd list failure must propagate, not be swallowed as no-strays")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("error should wrap the bd error; got %v", err)
	}
}

// TestStrandedSelfCycles_emptySelfSkips asserts that when self login is unknown
// the guard cannot decide authorship, so it returns nothing rather than guessing
// (no false positives, no bd show calls).
func TestStrandedSelfCycles_emptySelfSkips(t *testing.T) {
	br := fakeBR{out: map[string]string{
		listOpenFeedbackKey: envelope(beads.Issue{
			ID: "zr-500", Title: "process-feedback: PR 1", Type: "task",
			Status: "open", Parent: "zr-50",
		}),
	}}
	got, err := StrandedSelfCycles(context.Background(), br, "")
	if err != nil {
		t.Fatalf("StrandedSelfCycles error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("with unknown self, nothing can be classified as stranded; got %v", got)
	}
}
