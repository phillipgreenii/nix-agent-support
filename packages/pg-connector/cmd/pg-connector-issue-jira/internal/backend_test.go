package internal

import (
	"context"
	"errors"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/issue"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// fakeRunner is a minimal double for Runner, so this file's unit tests
// never spawn a real pjira subprocess. handle computes (stdout, err) for a
// given invocation; calls records every invocation for assertions.
type fakeRunner struct {
	calls  [][]string
	handle func(args []string) (string, error)
	binary string
}

func (f *fakeRunner) Run(_ context.Context, args ...string) (string, error) {
	f.calls = append(f.calls, args)
	if f.handle != nil {
		return f.handle(args)
	}
	return "", nil
}

func (f *fakeRunner) Binary() string {
	if f.binary != "" {
		return f.binary
	}
	return "pjira"
}

func argsEndWith(args []string, tail ...string) bool {
	if len(tail) > len(args) {
		return false
	}
	start := len(args) - len(tail)
	for i, want := range tail {
		if args[start+i] != want {
			return false
		}
	}
	return true
}

// ----------------------------------------------------------------------
// Show
// ----------------------------------------------------------------------

func TestBackend_Show_Success(t *testing.T) {
	fr := &fakeRunner{handle: func(args []string) (string, error) {
		if args[0] != "issue" {
			t.Fatalf("unexpected op: %v", args)
		}
		return `{"key":"PROJ-1","summary":"probe","status":"To Do","issuetype":"Bug",` +
			`"labels":["a","b"],"url":"https://example.atlassian.net/browse/PROJ-1",` +
			`"priority":"High","project":"PROJ",` +
			`"assignee":{"display_name":"Someone","email":"someone@example.com"}}`, nil
	}}
	b := New(fr)

	got, err := b.Show(context.Background(), "PROJ-1")
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if got.ID != "PROJ-1" || got.Title != "probe" || got.State != "To Do" {
		t.Fatalf("got %+v", got)
	}
	if got.Priority != "High" {
		t.Fatalf("Priority = %q, want High", got.Priority)
	}
	if got.IssueType != "Bug" {
		t.Fatalf("IssueType = %q, want Bug", got.IssueType)
	}
	if len(got.Labels) != 2 || got.Labels[0] != "a" || got.Labels[1] != "b" {
		t.Fatalf("Labels = %v", got.Labels)
	}
	if got.URL != "https://example.atlassian.net/browse/PROJ-1" {
		t.Fatalf("URL = %q", got.URL)
	}
	if got.Tracker != "PROJ" {
		t.Fatalf("Tracker = %q, want PROJ (the issue's own Jira project key)", got.Tracker)
	}
	if got.Assignee != "Someone" {
		t.Fatalf("Assignee = %q, want Someone (display_name preferred over email)", got.Assignee)
	}
	if !argsEndWith(fr.calls[0], "--", "PROJ-1") {
		t.Fatalf("expected id as a literal positional after a \"--\" terminator, got %v", fr.calls[0])
	}
}

// TestBackend_Show_AssigneeFallsBackToEmail locks in toSchemaIssue's
// fallback: when a Jira instance's API returns an assignee with no
// display_name (only email), Assignee must still be populated rather than
// left empty.
func TestBackend_Show_AssigneeFallsBackToEmail(t *testing.T) {
	fr := &fakeRunner{handle: func(args []string) (string, error) {
		return `{"key":"PROJ-1","summary":"probe","status":"To Do","issuetype":"Bug",` +
			`"assignee":{"email":"someone@example.com"}}`, nil
	}}
	b := New(fr)
	got, err := b.Show(context.Background(), "PROJ-1")
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if got.Assignee != "someone@example.com" {
		t.Fatalf("Assignee = %q, want someone@example.com", got.Assignee)
	}
}

func TestBackend_Show_EmptyID(t *testing.T) {
	fr := &fakeRunner{}
	b := New(fr)
	_, err := b.Show(context.Background(), "  ")
	if err == nil {
		t.Fatal("expected error for empty id")
	}
	if len(fr.calls) != 0 {
		t.Fatalf("expected no pjira invocation for an invalid call, got %v", fr.calls)
	}
	if !errors.Is(err, scriptout.ErrInvalidArgument) {
		t.Fatalf("err = %v, want errors.Is(err, ErrInvalidArgument)", err)
	}
	if errors.Is(err, scriptout.ErrNotFound) || errors.Is(err, scriptout.ErrUnavailable) {
		t.Fatalf("err = %v, must not also be ErrNotFound/ErrUnavailable", err)
	}
}

// TestBackend_Show_NotFound is the packet's required test proving Show's
// not-found case classifies to scriptout.ErrNotFound — mirroring pjira's
// own verified client.go behavior: `fmt.Errorf("pjira: issue %s not
// found", key)` on a 404, printed to stderr by main.go and exiting 1.
func TestBackend_Show_NotFound(t *testing.T) {
	fr := &fakeRunner{handle: func(args []string) (string, error) {
		return "", errors.New("pjira issue -- PROJ-999: exit status 1: pjira: issue PROJ-999 not found")
	}}
	b := New(fr)
	_, err := b.Show(context.Background(), "PROJ-999")
	if !errors.Is(err, scriptout.ErrNotFound) {
		t.Fatalf("err = %v, want wrapping ErrNotFound", err)
	}
}

// TestBackend_Show_PathMissing_IsNotMisclassifiedAsNotFound guards
// classifyPJIRAErrorMessage's deliberately narrow "not found" match: exec's
// own "executable file not found in $PATH" message also contains the
// substring "not found", but must never be classified as ErrNotFound —
// that would misreport "pjira is entirely unavailable" as "this one issue
// doesn't exist" [mirrors
// cmd/pg-connector-issue-beads/internal/bd_test.go's identical guard].
func TestBackend_Show_PathMissing_IsNotMisclassifiedAsNotFound(t *testing.T) {
	fr := &fakeRunner{handle: func(args []string) (string, error) {
		return "", errors.New(`pjira issue -- PROJ-1: exec: "pjira": executable file not found in $PATH (is pjira on PATH?)`)
	}}
	b := New(fr)
	_, err := b.Show(context.Background(), "PROJ-1")
	if errors.Is(err, scriptout.ErrNotFound) {
		t.Fatalf("err = %v, must not be classified as ErrNotFound", err)
	}
	if !errors.Is(err, scriptout.ErrUnavailable) {
		t.Fatalf("err = %v, want wrapping ErrUnavailable", err)
	}
}

// TestBackend_Show_Unauthorized proves a 401/403-shaped failure classifies
// to ErrUnauthenticated, the taxonomy's closest fit — pjira's own GetIssue
// folds both into a generic "status <code>" message rather than a
// dedicated sentinel.
func TestBackend_Show_Unauthorized(t *testing.T) {
	fr := &fakeRunner{handle: func(args []string) (string, error) {
		return "", errors.New("pjira issue -- PROJ-1: exit status 1: pjira: get issue PROJ-1: status 401 Unauthorized")
	}}
	b := New(fr)
	_, err := b.Show(context.Background(), "PROJ-1")
	if !errors.Is(err, scriptout.ErrUnauthenticated) {
		t.Fatalf("err = %v, want wrapping ErrUnauthenticated", err)
	}
}

// TestBackend_Show_MalformedOutput_IsUnavailable is the packet's required
// test proving an unrecognized/malformed underlying-tool failure (here, a
// decode failure on the exit-0 success path) classifies to
// scriptout.ErrUnavailable.
func TestBackend_Show_MalformedOutput_IsUnavailable(t *testing.T) {
	fr := &fakeRunner{handle: func(args []string) (string, error) {
		return "not json at all", nil
	}}
	b := New(fr)
	_, err := b.Show(context.Background(), "PROJ-1")
	if err == nil {
		t.Fatal("expected a decode error")
	}
	if errors.Is(err, scriptout.ErrNotFound) {
		t.Fatalf("err = %v, must not be classified as ErrNotFound", err)
	}
	if !errors.Is(err, scriptout.ErrUnavailable) {
		t.Fatalf("err = %v, want wrapping ErrUnavailable", err)
	}
}

// TestBackend_Show_EmptyKey_IsUnavailable locks in decodePJIRAIssue's
// guard: a well-formed JSON object with an empty/missing "key" field must
// fail as a decode problem, never silently succeed with a zero-value
// issue.
func TestBackend_Show_EmptyKey_IsUnavailable(t *testing.T) {
	fr := &fakeRunner{handle: func(args []string) (string, error) {
		return `{"summary":"no key here"}`, nil
	}}
	b := New(fr)
	_, err := b.Show(context.Background(), "PROJ-1")
	if !errors.Is(err, scriptout.ErrUnavailable) {
		t.Fatalf("err = %v, want wrapping ErrUnavailable", err)
	}
}

// ----------------------------------------------------------------------
// Create / Comment / Transition — documented gap (writeNotSupportedErr)
// ----------------------------------------------------------------------

func TestBackend_Create_MissingTitle(t *testing.T) {
	fr := &fakeRunner{}
	b := New(fr)
	_, err := b.Create(context.Background(), issue.IssueInput{})
	if len(fr.calls) != 0 {
		t.Fatalf("expected no pjira invocation for an invalid call, got %v", fr.calls)
	}
	if !errors.Is(err, scriptout.ErrInvalidArgument) {
		t.Fatalf("err = %v, want errors.Is(err, ErrInvalidArgument)", err)
	}
}

// TestBackend_Create_ReportsNotSupported locks in the documented gap: with
// a valid input, Create must still fail (pjira has no write op), but as a
// well-formed, classifiable ErrUnavailable — never a silent/fabricated
// success and never an unwrapped plain error.
func TestBackend_Create_ReportsNotSupported(t *testing.T) {
	fr := &fakeRunner{}
	b := New(fr)
	_, err := b.Create(context.Background(), issue.IssueInput{Title: "valid title"})
	if len(fr.calls) != 0 {
		t.Fatalf("expected no pjira invocation (no write op to call), got %v", fr.calls)
	}
	if !errors.Is(err, scriptout.ErrUnavailable) {
		t.Fatalf("err = %v, want wrapping ErrUnavailable", err)
	}
}

func TestBackend_Comment_EmptyID(t *testing.T) {
	fr := &fakeRunner{}
	b := New(fr)
	err := b.Comment(context.Background(), "  ", "hello")
	if !errors.Is(err, scriptout.ErrInvalidArgument) {
		t.Fatalf("err = %v, want errors.Is(err, ErrInvalidArgument)", err)
	}
}

func TestBackend_Comment_EmptyBody(t *testing.T) {
	fr := &fakeRunner{}
	b := New(fr)
	err := b.Comment(context.Background(), "PROJ-1", "  ")
	if !errors.Is(err, scriptout.ErrInvalidArgument) {
		t.Fatalf("err = %v, want errors.Is(err, ErrInvalidArgument)", err)
	}
}

func TestBackend_Comment_ReportsNotSupported(t *testing.T) {
	fr := &fakeRunner{}
	b := New(fr)
	err := b.Comment(context.Background(), "PROJ-1", "a comment")
	if len(fr.calls) != 0 {
		t.Fatalf("expected no pjira invocation (no write op to call), got %v", fr.calls)
	}
	if !errors.Is(err, scriptout.ErrUnavailable) {
		t.Fatalf("err = %v, want wrapping ErrUnavailable", err)
	}
}

func TestBackend_Transition_EmptyID(t *testing.T) {
	fr := &fakeRunner{}
	b := New(fr)
	err := b.Transition(context.Background(), "  ", "Done")
	if !errors.Is(err, scriptout.ErrInvalidArgument) {
		t.Fatalf("err = %v, want errors.Is(err, ErrInvalidArgument)", err)
	}
}

func TestBackend_Transition_EmptyTargetState(t *testing.T) {
	fr := &fakeRunner{}
	b := New(fr)
	err := b.Transition(context.Background(), "PROJ-1", "  ")
	if !errors.Is(err, scriptout.ErrInvalidArgument) {
		t.Fatalf("err = %v, want errors.Is(err, ErrInvalidArgument)", err)
	}
}

func TestBackend_Transition_ReportsNotSupported(t *testing.T) {
	fr := &fakeRunner{}
	b := New(fr)
	err := b.Transition(context.Background(), "PROJ-1", "Done")
	if len(fr.calls) != 0 {
		t.Fatalf("expected no pjira invocation (no write op to call), got %v", fr.calls)
	}
	if !errors.Is(err, scriptout.ErrUnavailable) {
		t.Fatalf("err = %v, want wrapping ErrUnavailable", err)
	}
}

// ----------------------------------------------------------------------
// CheckAuth / AuthChecker
// ----------------------------------------------------------------------

// TestBackend_ImplementsAuthChecker locks in this backend's binding
// decision (the opposite of pg-connector-issue-beads, which deliberately
// does NOT implement AuthChecker): pjira's own auth-status op lets
// credential resolution be health-checked independently of a real op.
func TestBackend_ImplementsAuthChecker(t *testing.T) {
	var p issue.Provider = New(&fakeRunner{})
	if _, ok := p.(provider.AuthChecker); !ok {
		t.Fatal("Backend must implement provider.AuthChecker via pjira auth-status")
	}
}

func TestBackend_CheckAuth_OK(t *testing.T) {
	fr := &fakeRunner{handle: func(args []string) (string, error) {
		if args[0] != "auth-status" {
			t.Fatalf("unexpected op: %v", args)
		}
		return "OK\n", nil
	}}
	b := New(fr)
	if err := b.CheckAuth(context.Background()); err != nil {
		t.Fatalf("CheckAuth: %v, want nil", err)
	}
}

func TestBackend_CheckAuth_NotOK(t *testing.T) {
	fr := &fakeRunner{handle: func(args []string) (string, error) {
		return "MISSING\n", nil
	}}
	b := New(fr)
	if err := b.CheckAuth(context.Background()); err == nil {
		t.Fatal("expected an error for a non-OK auth-status state")
	}
}

func TestBackend_CheckAuth_TransportFailure(t *testing.T) {
	fr := &fakeRunner{handle: func(args []string) (string, error) {
		return "", errors.New("pjira auth-status: exit status 1: pjira auth-status: dial tcp: no route to host")
	}}
	b := New(fr)
	if err := b.CheckAuth(context.Background()); err == nil {
		t.Fatal("expected an error for a transport failure")
	}
}

// ----------------------------------------------------------------------
// Vocabulary
// ----------------------------------------------------------------------

func TestVocabulary_NonEmpty(t *testing.T) {
	if len(Vocabulary) == 0 {
		t.Fatal("Vocabulary must be non-empty")
	}
}

func TestPriorityVocabulary_NonEmpty(t *testing.T) {
	if len(PriorityVocabulary) == 0 {
		t.Fatal("PriorityVocabulary must be non-empty")
	}
}
