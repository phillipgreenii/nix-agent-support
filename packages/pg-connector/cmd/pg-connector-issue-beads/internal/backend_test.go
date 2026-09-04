package internal

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/issue"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// fakeRunner is a minimal double for Runner, so this file's unit tests
// never spawn a real `bd` subprocess. handle computes (stdout, err) for a
// given invocation; calls records every invocation for assertions.
type fakeRunner struct {
	calls  [][]string
	handle func(args []string) (string, error)
}

func (f *fakeRunner) Run(_ context.Context, args ...string) (string, error) {
	f.calls = append(f.calls, args)
	if f.handle != nil {
		return f.handle(args)
	}
	return "", nil
}

func containsArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

// ----------------------------------------------------------------------
// Show
// ----------------------------------------------------------------------

func TestBackend_Show_Success(t *testing.T) {
	fr := &fakeRunner{handle: func(args []string) (string, error) {
		if args[0] != "show" {
			t.Fatalf("unexpected op: %v", args)
		}
		return `{"data":[{"id":"tp-1","title":"probe","status":"open","priority":1,"issue_type":"bug","labels":["a","b"]}],"schema_version":1}`, nil
	}}
	b := New(fr)

	got, err := b.Show(context.Background(), "tp-1")
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if got.ID != "tp-1" || got.Title != "probe" || got.State != "open" {
		t.Fatalf("got %+v", got)
	}
	if got.Priority != "P1" {
		t.Fatalf("Priority = %q, want P1", got.Priority)
	}
	if got.IssueType != "bug" {
		t.Fatalf("IssueType = %q, want bug", got.IssueType)
	}
	if len(got.Labels) != 2 || got.Labels[0] != "a" || got.Labels[1] != "b" {
		t.Fatalf("Labels = %v", got.Labels)
	}
	if got.URL != "" {
		t.Fatalf("URL = %q, want empty (bd has no hosted URL convention)", got.URL)
	}
}

func TestBackend_Show_NotFound_ViaJSONErrorEnvelope(t *testing.T) {
	// Mirrors bd's observed behavior for `bd show <missing-id> --json`: exit
	// 1 AND a well-formed {"data":{"error":...}} envelope on stdout.
	fr := &fakeRunner{handle: func(args []string) (string, error) {
		out := `{"data":{"error":"no issues found matching the provided IDs"},"schema_version":1}`
		return out, fmt.Errorf("bd %s: exit status 1: Error fetching tp-zzz: no issue found matching \"tp-zzz\"", strings.Join(args, " "))
	}}
	b := New(fr)

	_, err := b.Show(context.Background(), "tp-zzz")
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, scriptout.ErrNotFound) {
		t.Fatalf("err = %v, want wrapping ErrNotFound", err)
	}
}

func TestBackend_Show_EmptyID(t *testing.T) {
	fr := &fakeRunner{}
	b := New(fr)
	if _, err := b.Show(context.Background(), "  "); err == nil {
		t.Fatal("expected error for empty id")
	}
	if len(fr.calls) != 0 {
		t.Fatalf("expected no bd invocation for an invalid call, got %v", fr.calls)
	}
}

// ----------------------------------------------------------------------
// Create
// ----------------------------------------------------------------------

func TestBackend_Create_Success(t *testing.T) {
	fr := &fakeRunner{handle: func(args []string) (string, error) {
		if args[0] != "create" {
			t.Fatalf("unexpected op: %v", args)
		}
		if !containsArg(args, "--title") || !containsArg(args, "new issue") {
			t.Fatalf("expected --title \"new issue\" in args, got %v", args)
		}
		if !containsArg(args, "-p") || !containsArg(args, "P2") {
			t.Fatalf("expected -p P2 in args, got %v", args)
		}
		if !containsArg(args, "--type") || !containsArg(args, "bug") {
			t.Fatalf("expected --type bug in args, got %v", args)
		}
		if !containsArg(args, "--labels") || !containsArg(args, "x,y") {
			t.Fatalf("expected --labels x,y in args, got %v", args)
		}
		return `{"data":{"id":"tp-2","title":"new issue","status":"open","priority":2,"issue_type":"bug","labels":["x","y"]},"schema_version":1}`, nil
	}}
	b := New(fr)

	got, err := b.Create(context.Background(), issue.IssueInput{
		Title:     "new issue",
		Priority:  "P2",
		Labels:    []string{"x", "y"},
		IssueType: "bug",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got.ID != "tp-2" || got.State != "open" || got.Priority != "P2" {
		t.Fatalf("got %+v", got)
	}
}

func TestBackend_Create_MissingTitle(t *testing.T) {
	fr := &fakeRunner{}
	b := New(fr)
	if _, err := b.Create(context.Background(), issue.IssueInput{}); err == nil {
		t.Fatal("expected error for empty title")
	}
	if len(fr.calls) != 0 {
		t.Fatalf("expected no bd invocation for an invalid call, got %v", fr.calls)
	}
}

func TestBackend_Create_OmitsOptionalFlagsWhenUnset(t *testing.T) {
	fr := &fakeRunner{handle: func(args []string) (string, error) {
		for _, flag := range []string{"-p", "--type", "--labels"} {
			if containsArg(args, flag) {
				t.Fatalf("did not expect %s in args when unset, got %v", flag, args)
			}
		}
		return `{"data":{"id":"tp-3","title":"bare","status":"open","priority":2,"issue_type":"task"},"schema_version":1}`, nil
	}}
	b := New(fr)
	if _, err := b.Create(context.Background(), issue.IssueInput{Title: "bare"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
}

// ----------------------------------------------------------------------
// Comment
// ----------------------------------------------------------------------

func TestBackend_Comment_Success(t *testing.T) {
	fr := &fakeRunner{handle: func(args []string) (string, error) {
		if args[0] != "comment" || args[1] != "tp-1" || args[2] != "hello there" {
			t.Fatalf("unexpected args: %v", args)
		}
		return `{"data":{"id":"c1","issue_id":"tp-1","text":"hello there"},"schema_version":1}`, nil
	}}
	b := New(fr)
	if err := b.Comment(context.Background(), "tp-1", "hello there"); err != nil {
		t.Fatalf("Comment: %v", err)
	}
}

func TestBackend_Comment_NotFound_ViaStderrOnlyFailure(t *testing.T) {
	// Mirrors bd's observed behavior for `bd comment <missing-id> ... --json`:
	// a well-formed JSON error envelope IS written for comment's not-found
	// path (unlike update's stderr-only path exercised below).
	fr := &fakeRunner{handle: func(args []string) (string, error) {
		out := `{"data":{"error":"resolving tp-zzz: no issue found matching \"tp-zzz\""},"schema_version":1}`
		return out, errors.New("bd comment tp-zzz hi --json: exit status 1")
	}}
	b := New(fr)
	err := b.Comment(context.Background(), "tp-zzz", "hi")
	if !errors.Is(err, scriptout.ErrNotFound) {
		t.Fatalf("err = %v, want wrapping ErrNotFound", err)
	}
}

func TestBackend_Comment_EmptyBody(t *testing.T) {
	fr := &fakeRunner{}
	b := New(fr)
	if err := b.Comment(context.Background(), "tp-1", ""); err == nil {
		t.Fatal("expected error for empty body")
	}
	if len(fr.calls) != 0 {
		t.Fatalf("expected no bd invocation for an invalid call, got %v", fr.calls)
	}
}

// ----------------------------------------------------------------------
// Transition
// ----------------------------------------------------------------------

func TestBackend_Transition_Success(t *testing.T) {
	fr := &fakeRunner{handle: func(args []string) (string, error) {
		if args[0] != "update" || args[1] != "tp-1" {
			t.Fatalf("unexpected args: %v", args)
		}
		if !containsArg(args, "--status") || !containsArg(args, "in_progress") {
			t.Fatalf("expected --status in_progress in args, got %v", args)
		}
		return `{"data":[{"id":"tp-1","title":"probe","status":"in_progress","priority":2,"issue_type":"task"}],"schema_version":1}`, nil
	}}
	b := New(fr)
	if err := b.Transition(context.Background(), "tp-1", "in_progress"); err != nil {
		t.Fatalf("Transition: %v", err)
	}
}

// TestBackend_Transition_NotFound_ViaStderrOnlyFailure mirrors bd's
// observed behavior for `bd update <missing-id> --status ... --json`:
// unlike show/comment, update writes NOTHING to stdout on this failure —
// only a stderr message, surfaced here via the wrapped exec error. The
// classifier must still recognize it as not_found from that message alone.
func TestBackend_Transition_NotFound_ViaStderrOnlyFailure(t *testing.T) {
	fr := &fakeRunner{handle: func(args []string) (string, error) {
		return "", errors.New(`bd update tp-zzz --status closed --json: exit status 1: Error resolving tp-zzz: no issue found matching "tp-zzz"`)
	}}
	b := New(fr)
	err := b.Transition(context.Background(), "tp-zzz", "closed")
	if !errors.Is(err, scriptout.ErrNotFound) {
		t.Fatalf("err = %v, want wrapping ErrNotFound", err)
	}
}

// TestBackend_Transition_InvalidTargetState_IsNotMisclassifiedAsNotFound
// locks in the Freedom boundary choice documented on Transition: an
// unrecognized target state is bd's own rejection to report, passed
// through as the taxonomy's generic ErrUnavailable — never confused with
// ErrNotFound just because both are failures.
func TestBackend_Transition_InvalidTargetState_IsNotMisclassifiedAsNotFound(t *testing.T) {
	fr := &fakeRunner{handle: func(args []string) (string, error) {
		out := `{"data":{"error":"invalid status \"bogus\" (built-in: open, in_progress, blocked, deferred, closed, pinned, hooked; or configure custom statuses via 'bd config set status.custom')"},"schema_version":1}`
		return out, errors.New("bd update tp-1 --status bogus --json: exit status 1")
	}}
	b := New(fr)
	err := b.Transition(context.Background(), "tp-1", "bogus")
	if err == nil {
		t.Fatal("expected error")
	}
	if errors.Is(err, scriptout.ErrNotFound) {
		t.Fatalf("err = %v, must not be classified as ErrNotFound", err)
	}
	if !errors.Is(err, scriptout.ErrUnavailable) {
		t.Fatalf("err = %v, want wrapping ErrUnavailable", err)
	}
}

// TestBackend_Run_PathMissing_IsNotMisclassifiedAsNotFound guards
// classifyBDErrorMessage's deliberately narrow "no issue(s) found" match:
// exec's own "executable file not found in $PATH" message also contains
// the substring "not found", but must never be classified as ErrNotFound —
// that would misreport "bd is entirely unavailable" as "this one issue
// doesn't exist".
func TestBackend_Run_PathMissing_IsNotMisclassifiedAsNotFound(t *testing.T) {
	fr := &fakeRunner{handle: func(args []string) (string, error) {
		return "", errors.New(`bd show tp-1 --json: exec: "bd": executable file not found in $PATH (is bd on PATH?)`)
	}}
	b := New(fr)
	_, err := b.Show(context.Background(), "tp-1")
	if errors.Is(err, scriptout.ErrNotFound) {
		t.Fatalf("err = %v, must not be classified as ErrNotFound", err)
	}
	if !errors.Is(err, scriptout.ErrUnavailable) {
		t.Fatalf("err = %v, want wrapping ErrUnavailable", err)
	}
}

func TestBackend_Transition_EmptyTargetState(t *testing.T) {
	fr := &fakeRunner{}
	b := New(fr)
	if err := b.Transition(context.Background(), "tp-1", ""); err == nil {
		t.Fatal("expected error for empty target state")
	}
	if len(fr.calls) != 0 {
		t.Fatalf("expected no bd invocation for an invalid call, got %v", fr.calls)
	}
}

// ----------------------------------------------------------------------
// Binding decisions
// ----------------------------------------------------------------------

// TestBackend_DoesNotImplementAuthChecker locks in backend.go's documented
// binding decision: this workspace's bd/dolt setup has no per-caller auth
// concept, so Backend deliberately does not implement
// pkg/provider.AuthChecker.
func TestBackend_DoesNotImplementAuthChecker(t *testing.T) {
	var p issue.Provider = New(&fakeRunner{})
	if _, ok := p.(provider.AuthChecker); ok {
		t.Fatal("Backend must not implement provider.AuthChecker (bd has no per-caller auth concept)")
	}
}

func TestVocabulary_NonEmptyAndMatchesRealBDStatuses(t *testing.T) {
	if len(Vocabulary) == 0 {
		t.Fatal("Vocabulary must be non-empty")
	}
	want := map[string]bool{
		"open": true, "in_progress": true, "blocked": true,
		"deferred": true, "closed": true, "pinned": true, "hooked": true,
	}
	got := map[string]bool{}
	for _, v := range Vocabulary {
		got[v] = true
	}
	for w := range want {
		if !got[w] {
			t.Errorf("Vocabulary missing bd built-in status %q", w)
		}
	}
	for g := range got {
		if !want[g] {
			t.Errorf("Vocabulary has unexpected entry %q not among bd's built-in statuses", g)
		}
	}
}
