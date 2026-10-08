package internal

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/issue"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
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
	// An assignee who is not the operator: Show must stay a single pjira call.
	b.getenv = func(string) string { return "operator@example.com" }

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

// TestBackend_Show_AsOfAndStale locks in bead pg2-2j5ac.28.3's own
// additions: Stale is always false (a live pjira read, no local cache),
// and AsOf is populated with a valid, current RFC3339 timestamp.
func TestBackend_Show_AsOfAndStale(t *testing.T) {
	fr := &fakeRunner{handle: func(args []string) (string, error) {
		return `{"key":"PROJ-1","summary":"probe","status":"To Do"}`, nil
	}}
	b := New(fr)
	got, err := b.Show(context.Background(), "PROJ-1")
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if got.Stale {
		t.Fatal("Stale = true, want false (a live pjira read)")
	}
	asOf, parseErr := time.Parse(time.RFC3339, got.AsOf)
	if parseErr != nil || time.Since(asOf) > 10*time.Second || time.Since(asOf) < -10*time.Second {
		t.Fatalf("AsOf = %q, want a valid RFC3339 timestamp close to now", got.AsOf)
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
// Create
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

// TestBackend_Create_MissingIssueType locks in this backend's own
// additional required field: Jira's create endpoint requires an issue type
// per project with no safe cross-project default, unlike issue.IssueInput's
// own (omitempty) IssueType.
func TestBackend_Create_MissingIssueType(t *testing.T) {
	fr := &fakeRunner{}
	b := New(fr)
	_, err := b.Create(context.Background(), issue.IssueInput{Title: "valid title"})
	if len(fr.calls) != 0 {
		t.Fatalf("expected no pjira invocation for an invalid call, got %v", fr.calls)
	}
	if !errors.Is(err, scriptout.ErrInvalidArgument) {
		t.Fatalf("err = %v, want errors.Is(err, ErrInvalidArgument)", err)
	}
}

// TestBackend_Create_ProjectNotConfigured proves Create refuses outright,
// as a classifiable ErrUnavailable, when $PG_CONNECTOR_ISSUE_JIRA_PROJECT
// is unset — mirroring ErrWorkspaceNotConfigured's identical
// "refuse rather than silently fall back" discipline in the sibling beads
// backend.
func TestBackend_Create_ProjectNotConfigured(t *testing.T) {
	fr := &fakeRunner{}
	b := &Backend{runner: fr, getenv: fakeEnv(nil)}
	_, err := b.Create(context.Background(), issue.IssueInput{Title: "valid title", IssueType: "Task"})
	if len(fr.calls) != 0 {
		t.Fatalf("expected no pjira invocation when no project is configured, got %v", fr.calls)
	}
	if !errors.Is(err, scriptout.ErrUnavailable) {
		t.Fatalf("err = %v, want wrapping ErrUnavailable", err)
	}
}

// TestBackend_Create_Success proves Create execs `pjira create --project
// ... --type ... --summary ...`, decodes the {key,url} result, and then
// fetches the created issue's actual state via a follow-up Show call
// (pjira's own create endpoint does not itself return status/labels/etc).
func TestBackend_Create_Success(t *testing.T) {
	var calls [][]string
	fr := &fakeRunner{handle: func(args []string) (string, error) {
		calls = append(calls, args)
		if args[0] == "create" {
			return `{"key":"PROJ-2","url":"https://example.atlassian.net/browse/PROJ-2"}`, nil
		}
		if args[0] == "issue" {
			return `{"key":"PROJ-2","summary":"a new issue","status":"To Do","issuetype":"Task",` +
				`"url":"https://example.atlassian.net/browse/PROJ-2","project":"PROJ"}`, nil
		}
		t.Fatalf("unexpected op: %v", args)
		return "", nil
	}}
	b := &Backend{runner: fr, getenv: fakeEnv(map[string]string{EnvProject: "PROJ"})}

	got, err := b.Create(context.Background(), issue.IssueInput{
		Title:       "a new issue",
		IssueType:   "Task",
		Description: "a description",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got.ID != "PROJ-2" || got.Title != "a new issue" || got.State != "To Do" {
		t.Fatalf("got %+v", got)
	}
	if got.Tracker != "PROJ" {
		t.Fatalf("Tracker = %q, want PROJ", got.Tracker)
	}
	if len(calls) != 2 || calls[0][0] != "create" || calls[1][0] != "issue" {
		t.Fatalf("expected a create call followed by a Show(issue) call, got %v", calls)
	}
	if !argsEndWith(calls[0], "--project", "PROJ", "--type", "Task", "--summary", "a new issue", "--description", "a description") {
		t.Fatalf("unexpected create args: %v", calls[0])
	}
}

// TestBackend_Create_RunFailure_IsClassified proves a pjira create failure
// (e.g. an unauthenticated tenant) classifies through the same
// classifyPJIRAErrorMessage taxonomy as Show, rather than a bespoke path.
func TestBackend_Create_RunFailure_IsClassified(t *testing.T) {
	fr := &fakeRunner{handle: func(args []string) (string, error) {
		return "", errors.New("pjira create ...: exit status 1: pjira: create issue: unauthenticated")
	}}
	b := &Backend{runner: fr, getenv: fakeEnv(map[string]string{EnvProject: "PROJ"})}
	_, err := b.Create(context.Background(), issue.IssueInput{Title: "t", IssueType: "Task"})
	if !errors.Is(err, scriptout.ErrUnauthenticated) {
		t.Fatalf("err = %v, want wrapping ErrUnauthenticated", err)
	}
}

// TestBackend_Create_MalformedResult_IsUnavailable proves a well-formed
// exit-0 pjira create response missing its key never silently succeeds.
func TestBackend_Create_MalformedResult_IsUnavailable(t *testing.T) {
	fr := &fakeRunner{handle: func(args []string) (string, error) {
		return `{"url":"https://example.atlassian.net/browse/PROJ-2"}`, nil
	}}
	b := &Backend{runner: fr, getenv: fakeEnv(map[string]string{EnvProject: "PROJ"})}
	_, err := b.Create(context.Background(), issue.IssueInput{Title: "t", IssueType: "Task"})
	if !errors.Is(err, scriptout.ErrUnavailable) {
		t.Fatalf("err = %v, want wrapping ErrUnavailable", err)
	}
}

// ----------------------------------------------------------------------
// Comment
// ----------------------------------------------------------------------

func TestBackend_Comment_EmptyID(t *testing.T) {
	fr := &fakeRunner{}
	b := New(fr)
	err := b.Comment(context.Background(), "  ", "hello")
	if len(fr.calls) != 0 {
		t.Fatalf("expected no pjira invocation for an invalid call, got %v", fr.calls)
	}
	if !errors.Is(err, scriptout.ErrInvalidArgument) {
		t.Fatalf("err = %v, want errors.Is(err, ErrInvalidArgument)", err)
	}
}

func TestBackend_Comment_EmptyBody(t *testing.T) {
	fr := &fakeRunner{}
	b := New(fr)
	err := b.Comment(context.Background(), "PROJ-1", "  ")
	if len(fr.calls) != 0 {
		t.Fatalf("expected no pjira invocation for an invalid call, got %v", fr.calls)
	}
	if !errors.Is(err, scriptout.ErrInvalidArgument) {
		t.Fatalf("err = %v, want errors.Is(err, ErrInvalidArgument)", err)
	}
}

// TestBackend_Comment_Success proves Comment execs `pjira comment <KEY>
// <BODY>` with a defense-in-depth "--" terminator ahead of both
// positionals, mirroring Show's identical precedent.
func TestBackend_Comment_Success(t *testing.T) {
	fr := &fakeRunner{handle: func(args []string) (string, error) {
		if args[0] != "comment" {
			t.Fatalf("unexpected op: %v", args)
		}
		return `{"key":"PROJ-1","id":"10042"}`, nil
	}}
	b := New(fr)
	if err := b.Comment(context.Background(), "PROJ-1", "a comment"); err != nil {
		t.Fatalf("Comment: %v", err)
	}
	if !argsEndWith(fr.calls[0], "--", "PROJ-1", "a comment") {
		t.Fatalf("expected id/body as literal positionals after a \"--\" terminator, got %v", fr.calls[0])
	}
}

// TestBackend_Comment_NotFound_IsClassified proves Comment classifies a
// not-found issue key the same way Show does.
func TestBackend_Comment_NotFound_IsClassified(t *testing.T) {
	fr := &fakeRunner{handle: func(args []string) (string, error) {
		return "", errors.New("pjira comment -- PROJ-999 hi: exit status 1: pjira: issue PROJ-999 not found")
	}}
	b := New(fr)
	err := b.Comment(context.Background(), "PROJ-999", "hi")
	if !errors.Is(err, scriptout.ErrNotFound) {
		t.Fatalf("err = %v, want wrapping ErrNotFound", err)
	}
}

// ----------------------------------------------------------------------
// Transition
// ----------------------------------------------------------------------

func TestBackend_Transition_EmptyID(t *testing.T) {
	fr := &fakeRunner{}
	b := New(fr)
	err := b.Transition(context.Background(), "  ", "Done")
	if len(fr.calls) != 0 {
		t.Fatalf("expected no pjira invocation for an invalid call, got %v", fr.calls)
	}
	if !errors.Is(err, scriptout.ErrInvalidArgument) {
		t.Fatalf("err = %v, want errors.Is(err, ErrInvalidArgument)", err)
	}
}

func TestBackend_Transition_EmptyTargetState(t *testing.T) {
	fr := &fakeRunner{}
	b := New(fr)
	err := b.Transition(context.Background(), "PROJ-1", "  ")
	if len(fr.calls) != 0 {
		t.Fatalf("expected no pjira invocation for an invalid call, got %v", fr.calls)
	}
	if !errors.Is(err, scriptout.ErrInvalidArgument) {
		t.Fatalf("err = %v, want errors.Is(err, ErrInvalidArgument)", err)
	}
}

// TestBackend_Transition_Success proves Transition execs `pjira transition
// <KEY> <TO>` with the same "--" defense-in-depth as Comment/Show.
func TestBackend_Transition_Success(t *testing.T) {
	fr := &fakeRunner{handle: func(args []string) (string, error) {
		if args[0] != "transition" {
			t.Fatalf("unexpected op: %v", args)
		}
		return `{"key":"PROJ-1","to":"Done"}`, nil
	}}
	b := New(fr)
	if err := b.Transition(context.Background(), "PROJ-1", "Done"); err != nil {
		t.Fatalf("Transition: %v", err)
	}
	if !argsEndWith(fr.calls[0], "--", "PROJ-1", "Done") {
		t.Fatalf("expected id/target as literal positionals after a \"--\" terminator, got %v", fr.calls[0])
	}
}

// TestBackend_Transition_UnknownTargetState is the packet's design-required
// test proving an unrecognized targetState classifies as a DISTINCT,
// clearly-classified error (ErrInvalidArgument, a caller-input problem) —
// never folded into the issue's own not-found classification, mirroring
// Client.Transition's own real "no transition to state %q available"
// message — see classifyPJIRAErrorMessage's own doc comment for the full
// rationale.
func TestBackend_Transition_UnknownTargetState(t *testing.T) {
	fr := &fakeRunner{handle: func(args []string) (string, error) {
		return "", errors.New(`pjira transition -- PROJ-1 Bogus: exit status 1: pjira: transition PROJ-1: no transition to state "Bogus" available`)
	}}
	b := New(fr)
	err := b.Transition(context.Background(), "PROJ-1", "Bogus")
	if !errors.Is(err, scriptout.ErrInvalidArgument) {
		t.Fatalf("err = %v, want wrapping ErrInvalidArgument", err)
	}
	if errors.Is(err, scriptout.ErrNotFound) {
		t.Fatalf("err = %v, must not be classified as ErrNotFound (the issue exists; the target state doesn't)", err)
	}
}

// TestBackend_Transition_NotFound_IsClassified proves Transition classifies
// a not-found issue key the same way Show does.
func TestBackend_Transition_NotFound_IsClassified(t *testing.T) {
	fr := &fakeRunner{handle: func(args []string) (string, error) {
		return "", errors.New("pjira transition -- PROJ-999 Done: exit status 1: pjira: issue PROJ-999 not found")
	}}
	b := New(fr)
	err := b.Transition(context.Background(), "PROJ-999", "Done")
	if !errors.Is(err, scriptout.ErrNotFound) {
		t.Fatalf("err = %v, want wrapping ErrNotFound", err)
	}
}

// ----------------------------------------------------------------------
// Update / Close / Deps (bead pg2-2j5ac.28.3)
// ----------------------------------------------------------------------

// TestBackend_Update_NoOpAvailable_IsUnavailable locks in Update's own
// documented binding decision: pjira has no field-level update op at all
// today, so this method returns a well-formed ErrUnavailable stub rather
// than silently no-opping — for an id it actually recognizes as its own.
// Fixed by pg2-rf8k2: Update now verifies existence via a follow-up Show
// call first [mirrors Deps' own call-then-Show pattern], so pjira IS
// invoked (unlike the pre-fix version of this test, which asserted pjira
// was NEVER invoked).
func TestBackend_Update_NoOpAvailable_IsUnavailable(t *testing.T) {
	fr := &fakeRunner{handle: func(args []string) (string, error) {
		if args[0] != "issue" {
			t.Fatalf("unexpected op: %v", args)
		}
		return `{"key":"PROJ-1","summary":"probe","status":"To Do"}`, nil
	}}
	b := New(fr)
	_, err := b.Update(context.Background(), "PROJ-1", issue.IssueUpdateFields{Title: "new title"})
	if !errors.Is(err, scriptout.ErrUnavailable) {
		t.Fatalf("err = %v, want errors.Is(err, ErrUnavailable)", err)
	}
}

// TestBackend_Update_UnknownID_NotFound is pg2-rf8k2's own regression
// test (mirroring TestBackend_Deps_UnknownID_NotFound): an id Jira does
// not recognize at all (e.g. a beads-shaped id from a sibling
// issue-beads backend) must classify to scriptout.ErrNotFound, not the
// ErrUnavailable capability-gap stub — otherwise DispatchTargeted's
// multi-instance try-each resolution policy (design's section 4.13)
// never falls through to try the next registered backend, and a beads
// id's `update` call always dies on Jira's own stub instead of ever
// reaching the beads backend's own real update.
func TestBackend_Update_UnknownID_NotFound(t *testing.T) {
	fr := &fakeRunner{handle: func(args []string) (string, error) {
		return "", errors.New("pjira issue -- pg2-2j5ac.30: exit status 1: pjira: issue pg2-2j5ac.30 not found")
	}}
	b := New(fr)
	_, err := b.Update(context.Background(), "pg2-2j5ac.30", issue.IssueUpdateFields{Title: "new title"})
	if !errors.Is(err, scriptout.ErrNotFound) {
		t.Fatalf("err = %v, want errors.Is(err, ErrNotFound)", err)
	}
}

func TestBackend_Update_EmptyID(t *testing.T) {
	fr := &fakeRunner{}
	b := New(fr)
	_, err := b.Update(context.Background(), "  ", issue.IssueUpdateFields{})
	if len(fr.calls) != 0 {
		t.Fatalf("expected no pjira invocation for an invalid call, got %v", fr.calls)
	}
	if !errors.Is(err, scriptout.ErrInvalidArgument) {
		t.Fatalf("err = %v, want errors.Is(err, ErrInvalidArgument)", err)
	}
}

// TestBackend_Close_Success proves Close records reason as a comment
// BEFORE transitioning to resolvingState ("Done").
func TestBackend_Close_Success(t *testing.T) {
	var calls [][]string
	fr := &fakeRunner{handle: func(args []string) (string, error) {
		calls = append(calls, args)
		if args[0] == "comment" {
			return `{"key":"PROJ-1","id":"1"}`, nil
		}
		if args[0] == "transition" {
			return `{"key":"PROJ-1","to":"Done"}`, nil
		}
		t.Fatalf("unexpected op: %v", args)
		return "", nil
	}}
	b := New(fr)
	if err := b.Close(context.Background(), "PROJ-1", "fixed in prod"); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if len(calls) != 2 || calls[0][0] != "comment" || calls[1][0] != "transition" {
		t.Fatalf("expected a comment call followed by a transition call, got %v", calls)
	}
	if !argsEndWith(calls[0], "--", "PROJ-1", "fixed in prod") {
		t.Fatalf("comment args = %v", calls[0])
	}
	if !argsEndWith(calls[1], "--", "PROJ-1", "Done") {
		t.Fatalf("transition args = %v", calls[1])
	}
}

// TestBackend_Close_NoReason_SkipsComment proves Close does not call
// pjira comment at all when reason is empty.
func TestBackend_Close_NoReason_SkipsComment(t *testing.T) {
	var calls [][]string
	fr := &fakeRunner{handle: func(args []string) (string, error) {
		calls = append(calls, args)
		return `{"key":"PROJ-1","to":"Done"}`, nil
	}}
	b := New(fr)
	if err := b.Close(context.Background(), "PROJ-1", ""); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if len(calls) != 1 || calls[0][0] != "transition" {
		t.Fatalf("expected only a transition call, got %v", calls)
	}
}

// TestBackend_Close_CommentFailure_NeverTransitions proves a comment
// failure short-circuits before ever attempting the transition.
func TestBackend_Close_CommentFailure_NeverTransitions(t *testing.T) {
	fr := &fakeRunner{handle: func(args []string) (string, error) {
		if args[0] == "comment" {
			return "", errors.New("pjira comment -- PROJ-999 x: exit status 1: pjira: issue PROJ-999 not found")
		}
		t.Fatal("transition must not be attempted after a comment failure")
		return "", nil
	}}
	b := New(fr)
	err := b.Close(context.Background(), "PROJ-999", "x")
	if !errors.Is(err, scriptout.ErrNotFound) {
		t.Fatalf("err = %v, want errors.Is(err, ErrNotFound)", err)
	}
}

func TestBackend_Close_EmptyID(t *testing.T) {
	fr := &fakeRunner{}
	b := New(fr)
	err := b.Close(context.Background(), "  ", "reason")
	if len(fr.calls) != 0 {
		t.Fatalf("expected no pjira invocation for an invalid call, got %v", fr.calls)
	}
	if !errors.Is(err, scriptout.ErrInvalidArgument) {
		t.Fatalf("err = %v, want errors.Is(err, ErrInvalidArgument)", err)
	}
}

// TestBackend_Deps_NoDependencyConcept_EmptyResult locks in Deps' own
// binding decision: pjira has no dependency/link query op at all, so this
// backend answers an empty result, never an error — for an id it
// actually recognizes as its own. Fixed by pg2-ljk9k: Deps now verifies
// existence via a follow-up Show call first [mirrors Create's own
// call-then-Show pattern], so pjira IS invoked (unlike the pre-fix
// version of this test).
func TestBackend_Deps_NoDependencyConcept_EmptyResult(t *testing.T) {
	fr := &fakeRunner{handle: func(args []string) (string, error) {
		if args[0] != "issue" {
			t.Fatalf("unexpected op: %v", args)
		}
		return `{"key":"PROJ-1","summary":"probe","status":"To Do"}`, nil
	}}
	b := New(fr)
	got, err := b.Deps(context.Background(), "PROJ-1", true)
	if err != nil {
		t.Fatalf("Deps: %v, want nil (empty result, never an error)", err)
	}
	if len(got.IDs) != 0 || len(got.Entities) != 0 {
		t.Fatalf("got = %+v, want an empty result", got)
	}
}

// TestBackend_Deps_UnknownID_NotFound is pg2-ljk9k's own regression test:
// an id Jira does not recognize at all (e.g. a beads-shaped id from a
// sibling issue-beads backend) must classify to scriptout.ErrNotFound,
// not silently succeed with an empty result — otherwise
// DispatchTargeted's multi-instance try-each resolution policy
// (design's section 4.13) never falls through to try the next
// registered backend, and a beads id's `deps` call resolves to an
// empty set instead of the beads backend's own recursive answer.
func TestBackend_Deps_UnknownID_NotFound(t *testing.T) {
	fr := &fakeRunner{handle: func(args []string) (string, error) {
		return "", errors.New("pjira issue -- pg2-2j5ac.30: exit status 1: pjira: issue pg2-2j5ac.30 not found")
	}}
	b := New(fr)
	_, err := b.Deps(context.Background(), "pg2-2j5ac.30", false)
	if !errors.Is(err, scriptout.ErrNotFound) {
		t.Fatalf("err = %v, want errors.Is(err, ErrNotFound)", err)
	}
}

func TestBackend_Deps_EmptyID(t *testing.T) {
	fr := &fakeRunner{}
	b := New(fr)
	_, err := b.Deps(context.Background(), "  ", false)
	if len(fr.calls) != 0 {
		t.Fatalf("expected no pjira invocation for an invalid call, got %v", fr.calls)
	}
	if !errors.Is(err, scriptout.ErrInvalidArgument) {
		t.Fatalf("err = %v, want errors.Is(err, ErrInvalidArgument)", err)
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

// ----------------------------------------------------------------------
// List (bead pg2-2j5ac.28.1; cursor round trip + present_ids widening,
// the 2026-09-18 operator decision on this docket's Jira cursor packet)
// ----------------------------------------------------------------------

func TestBackend_List_SingleExpr_JQLPassedThrough(t *testing.T) {
	calls := 0
	fr := &fakeRunner{handle: func(args []string) (string, error) {
		calls++
		if args[0] != "search" {
			t.Fatalf("unexpected op: %v", args)
		}
		if !argsEndWith(args, "--jql", "assignee = currentUser()", "--all") {
			t.Fatalf("args = %v, want --jql <expr> --all (no bound on a first/cursorless call)", args)
		}
		return `{"items":[{"key":"PROJ-1","summary":"a","status":"To Do"}],"truncated":false}`, nil
	}}
	b := New(fr)

	got, err := b.List(context.Background(), []string{"assignee = currentUser()"}, false, nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2 (the bound-appended search plus the unconditional ids-only present-ids search)", calls)
	}
	if len(got.Entities) != 1 || got.Entities[0].ID != "PROJ-1" {
		t.Fatalf("Entities = %+v", got.Entities)
	}
	if len(got.PresentIDs) != 1 || got.PresentIDs[0] != "PROJ-1" {
		t.Fatalf("PresentIDs = %+v", got.PresentIDs)
	}
	if len(got.Cursor) == 0 {
		t.Fatalf("Cursor = %v, want a real, non-nil value (this backend now always returns one on success)", got.Cursor)
	}
}

func TestBackend_List_MultipleExpressions_UnionDeduplicated(t *testing.T) {
	calls := 0
	fr := &fakeRunner{handle: func(args []string) (string, error) {
		calls++
		if containsArg(args, "assignee = currentUser()") {
			return `{"items":[{"key":"PROJ-1","summary":"a","status":"To Do"},{"key":"PROJ-2","summary":"b","status":"To Do"}],"truncated":false}`, nil
		}
		return `{"items":[{"key":"PROJ-2","summary":"b","status":"To Do"}],"truncated":true}`, nil
	}}
	b := New(fr)

	got, err := b.List(context.Background(), []string{"assignee = currentUser()", "labels = focus"}, false, nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if calls != 4 {
		t.Fatalf("calls = %d, want 4 (two searches per expression: bound-appended + ids-only)", calls)
	}
	if len(got.Entities) != 2 {
		t.Fatalf("Entities = %+v, want exactly 2 after dedup by key (PROJ-2 appeared in both)", got.Entities)
	}
	if len(got.PresentIDs) != 2 {
		t.Fatalf("PresentIDs = %+v, want exactly 2 after dedup by key", got.PresentIDs)
	}
	if !got.Truncated {
		t.Fatal("Truncated = false, want true (the second expression's search reported truncated)")
	}
}

func TestBackend_List_IDsOnly_OmitsEntities(t *testing.T) {
	fr := &fakeRunner{handle: func(args []string) (string, error) {
		return `{"items":[{"key":"PROJ-1","summary":"a","status":"To Do"}],"truncated":false}`, nil
	}}
	b := New(fr)

	got, err := b.List(context.Background(), []string{"assignee = currentUser()"}, true, nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got.Entities) != 0 {
		t.Fatalf("Entities = %+v, want empty when ids_only is true", got.Entities)
	}
	if len(got.PresentIDs) != 1 {
		t.Fatalf("PresentIDs = %+v, want present_ids populated regardless of ids_only", got.PresentIDs)
	}
}

// TestBackend_List_IDsOnly_RunsOneUnboundedSearchPerExpression pins the
// membership-query cost the umbrella's refresh cache relies on (spec section
// 6.7, INV-CACHE-6): an ids_only call is ONE origin search per expression —
// the unbounded id search — never the entity search whose result would be
// discarded, and a prior cursor's updated >= bound never narrows it.
func TestBackend_List_IDsOnly_RunsOneUnboundedSearchPerExpression(t *testing.T) {
	cursor := json.RawMessage(`{"updated_since":"2026-09-18T00:00:00Z"}`)
	var jqls []string
	fr := &fakeRunner{handle: func(args []string) (string, error) {
		jqls = append(jqls, jqlArg(args))
		return `{"items":[{"key":"PROJ-1","summary":"a","status":"To Do"},{"key":"PROJ-2","summary":"b","status":"To Do"}],"truncated":false}`, nil
	}}

	got, err := New(fr).List(context.Background(), []string{"assignee = currentUser()", "labels = focus"}, true, cursor)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []string{"assignee = currentUser()", "labels = focus"}
	if !reflect.DeepEqual(jqls, want) {
		t.Fatalf("searches = %q, want exactly one unbounded search per expression %q", jqls, want)
	}
	if !reflect.DeepEqual(got.PresentIDs, []string{"PROJ-1", "PROJ-2"}) {
		t.Fatalf("PresentIDs = %v, want [PROJ-1 PROJ-2] deduplicated across expressions", got.PresentIDs)
	}
	if len(got.Entities) != 0 {
		t.Fatalf("Entities = %+v, want none for ids_only", got.Entities)
	}
	if len(got.Cursor) == 0 {
		t.Fatal("Cursor empty, want a real cursor on a successful call")
	}
}

// TestBackend_List_IDsOnly_TruncatedFromTheIDSearch proves a truncated id
// search still marks the membership truncated, so the umbrella reports no
// removal from it.
func TestBackend_List_IDsOnly_TruncatedFromTheIDSearch(t *testing.T) {
	fr := &fakeRunner{handle: func(args []string) (string, error) {
		return `{"items":[{"key":"PROJ-1","summary":"a","status":"To Do"}],"truncated":true}`, nil
	}}
	got, err := New(fr).List(context.Background(), []string{"assignee = currentUser()"}, true, nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if !got.Truncated {
		t.Fatal("Truncated = false, want true from the ids-only search")
	}
}

func TestBackend_List_RunFailure_ClassifiedError(t *testing.T) {
	fr := &fakeRunner{handle: func(args []string) (string, error) {
		return "", errors.New("exit status 1: pjira: search: status 401 Unauthorized")
	}}
	b := New(fr)

	_, err := b.List(context.Background(), []string{"assignee = currentUser()"}, false, nil)
	if !errors.Is(err, scriptout.ErrUnauthenticated) {
		t.Fatalf("err = %v, want errors.Is(err, ErrUnauthenticated)", err)
	}
}

// jqlArg extracts the --jql flag's own value from a `search --jql <jql>
// --all` invocation.
func jqlArg(args []string) string {
	for i, a := range args {
		if a == "--jql" && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// TestBackend_List_CursorRoundTrip_AppendsUpdatedBound is the packet's
// required test: a second List call, given the Cursor a prior call
// returned, must send a JQL string with an "updated >=" bound appended to
// the configured expression — while the unconditional ids-only
// present-ids search still runs the plain, unmodified expression.
func TestBackend_List_CursorRoundTrip_AppendsUpdatedBound(t *testing.T) {
	fr1 := &fakeRunner{handle: func(args []string) (string, error) {
		return `{"items":[{"key":"PROJ-1","summary":"a","status":"To Do"}],"truncated":false}`, nil
	}}
	first, err := New(fr1).List(context.Background(), []string{"assignee = currentUser()"}, false, nil)
	if err != nil {
		t.Fatalf("first List: %v", err)
	}
	if len(first.Cursor) == 0 {
		t.Fatal("first call returned no cursor")
	}

	var sawBounded, sawUnbounded bool
	fr2 := &fakeRunner{handle: func(args []string) (string, error) {
		jql := jqlArg(args)
		switch {
		case strings.HasPrefix(jql, "(assignee = currentUser())") && strings.Contains(jql, "updated >="):
			sawBounded = true
		case jql == "assignee = currentUser()":
			sawUnbounded = true
		default:
			t.Fatalf("unexpected jql: %q", jql)
		}
		return `{"items":[{"key":"PROJ-1","summary":"a","status":"To Do"}],"truncated":false}`, nil
	}}
	if _, err := New(fr2).List(context.Background(), []string{"assignee = currentUser()"}, false, first.Cursor); err != nil {
		t.Fatalf("second List: %v", err)
	}
	if !sawBounded {
		t.Fatal("expected the bound-appended search's own jql to carry an \"updated >=\" clause derived from the prior cursor")
	}
	if !sawUnbounded {
		t.Fatal("expected the unconditional ids-only search to still run the plain, unmodified expression (no implicit rewriting)")
	}
}

// TestBackend_List_PresentIDs_ReflectsFullSetIndependentOfBound is the
// packet's required test: "PresentIDs reflects the ids-only search's
// result even when idsOnly=false" — when a cursor narrows the
// bound-appended search's own Entities, PresentIDs must still reflect
// the query's CURRENT, unbounded full match set from the separate
// ids-only search, not the narrower bounded set.
func TestBackend_List_PresentIDs_ReflectsFullSetIndependentOfBound(t *testing.T) {
	cursor := json.RawMessage(`{"updated_since":"2026-09-18T00:00:00Z"}`)
	fr := &fakeRunner{handle: func(args []string) (string, error) {
		jql := jqlArg(args)
		if strings.Contains(jql, "updated >=") {
			// The bound-appended search: only what actually changed
			// since the cursor.
			return `{"items":[{"key":"PROJ-2","summary":"b","status":"To Do"}],"truncated":false}`, nil
		}
		// The unconditional ids-only search: the query's CURRENT full
		// match set.
		return `{"items":[{"key":"PROJ-1","summary":"a","status":"To Do"},{"key":"PROJ-2","summary":"b","status":"To Do"}],"truncated":false}`, nil
	}}
	b := New(fr)

	got, err := b.List(context.Background(), []string{"assignee = currentUser()"}, false, cursor)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got.Entities) != 1 || got.Entities[0].ID != "PROJ-2" {
		t.Fatalf("Entities = %+v, want only PROJ-2 (the bound-appended search's own narrower result)", got.Entities)
	}
	if len(got.PresentIDs) != 2 {
		t.Fatalf("PresentIDs = %+v, want both PROJ-1 and PROJ-2 (the ids-only search's own unbounded full match set)", got.PresentIDs)
	}
}

// TestBackend_List_Truncated_FromIDsOnlySearchAlone is the packet's
// required test: "A Truncated: true response from either the
// bound-appended search or the ids-only search yields an overall
// Truncated: true result" — this half specifically proves the ids-only
// search's own truncation is honored even when the bound-appended search
// reports none.
func TestBackend_List_Truncated_FromIDsOnlySearchAlone(t *testing.T) {
	cursor := json.RawMessage(`{"updated_since":"2026-09-18T00:00:00Z"}`)
	fr := &fakeRunner{handle: func(args []string) (string, error) {
		jql := jqlArg(args)
		if strings.Contains(jql, "updated >=") {
			return `{"items":[{"key":"PROJ-1","summary":"a","status":"To Do"}],"truncated":false}`, nil
		}
		return `{"items":[{"key":"PROJ-1","summary":"a","status":"To Do"}],"truncated":true}`, nil
	}}
	b := New(fr)

	got, err := b.List(context.Background(), []string{"assignee = currentUser()"}, false, cursor)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if !got.Truncated {
		t.Fatal("Truncated = false, want true (the ids-only search alone reported truncated)")
	}
}

// TestBackend_List_MalformedCursor_TreatedAsAbsent proves an undecodable
// cursor is tolerated as if absent (never surfaced as an error), mirroring
// decodeJiraCursor's own doc comment.
func TestBackend_List_MalformedCursor_TreatedAsAbsent(t *testing.T) {
	fr := &fakeRunner{handle: func(args []string) (string, error) {
		if strings.Contains(jqlArg(args), "updated >=") {
			t.Fatalf("expected no bound for a malformed/undecodable cursor, got jql %q", jqlArg(args))
		}
		return `{"items":[],"truncated":false}`, nil
	}}
	b := New(fr)
	if _, err := b.List(context.Background(), []string{"assignee = currentUser()"}, false, json.RawMessage(`not json`)); err != nil {
		t.Fatalf("List: %v", err)
	}
}

// TestBackend_List_NilOrNullCursor_RunsUnboundedSearch is a regression pin:
// the pg-desk list-fingerprint check calls list with NO cursor and relies on
// that running the unbounded search, so no `updated >=` lookback clause may
// appear in either search's JQL (the umbrella's list verb always passes a nil
// cursor, and the wire "cursor": null decodes to the literal JSON null).
func TestBackend_List_NilOrNullCursor_RunsUnboundedSearch(t *testing.T) {
	for name, cursor := range map[string]json.RawMessage{
		"nil":      nil,
		"json-nul": json.RawMessage(`null`),
	} {
		t.Run(name, func(t *testing.T) {
			var jqls []string
			fr := &fakeRunner{handle: func(args []string) (string, error) {
				jqls = append(jqls, jqlArg(args))
				return `{"items":[{"key":"PROJ-1","summary":"a","status":"To Do"}],"truncated":false}`, nil
			}}
			if _, err := New(fr).List(context.Background(), []string{"assignee = currentUser()"}, false, cursor); err != nil {
				t.Fatalf("List: %v", err)
			}
			if len(jqls) == 0 {
				t.Fatal("no search ran")
			}
			for _, jql := range jqls {
				if strings.Contains(jql, "updated >=") {
					t.Fatalf("jql %q carries an updated >= lookback clause; a cursorless list must be unbounded", jql)
				}
				if jql != "assignee = currentUser()" {
					t.Fatalf("jql = %q, want the configured JQL unchanged", jql)
				}
			}
		})
	}
}

// ----------------------------------------------------------------------
// Cursor helpers (jiraCursor / decodeJiraCursor / newJiraCursor / boundedJQL)
// ----------------------------------------------------------------------

func TestBoundedJQL(t *testing.T) {
	if got := boundedJQL("assignee = currentUser()", ""); got != "assignee = currentUser()" {
		t.Fatalf("boundedJQL with empty updatedSince = %q, want jql unchanged", got)
	}
	got := boundedJQL("assignee = currentUser()", "2026-09-18T00:00:00Z")
	want := `(assignee = currentUser()) AND updated >= "2026-09-18 00:00"`
	if got != want {
		t.Fatalf("boundedJQL = %q, want %q", got, want)
	}
}

func TestDecodeJiraCursor(t *testing.T) {
	if c := decodeJiraCursor(nil); c.UpdatedSince != "" {
		t.Fatalf("decodeJiraCursor(nil) = %+v, want zero value", c)
	}
	if c := decodeJiraCursor(json.RawMessage("null")); c.UpdatedSince != "" {
		t.Fatalf("decodeJiraCursor(null) = %+v, want zero value", c)
	}
	if c := decodeJiraCursor(json.RawMessage(`{"updated_since":"2026-09-18T00:00:00Z"}`)); c.UpdatedSince != "2026-09-18T00:00:00Z" {
		t.Fatalf("decodeJiraCursor = %+v", c)
	}
	if c := decodeJiraCursor(json.RawMessage(`not json`)); c.UpdatedSince != "" {
		t.Fatalf("decodeJiraCursor(malformed) = %+v, want zero value (tolerated as absent)", c)
	}
}

func TestNewJiraCursor_RoundTrips(t *testing.T) {
	asOf := time.Date(2026, 9, 18, 12, 30, 0, 0, time.UTC)
	raw := newJiraCursor(asOf)
	c := decodeJiraCursor(raw)
	if c.UpdatedSince != asOf.Format(time.RFC3339) {
		t.Fatalf("round trip = %+v, want %q", c, asOf.Format(time.RFC3339))
	}
}

// ----------------------------------------------------------------------
// Search (bead pg2-8hcnx: wire the search capability's Search op onto the
// same `pjira search --jql` call List already uses)
// ----------------------------------------------------------------------

func TestBackend_Search_JQLPassedThrough(t *testing.T) {
	fr := &fakeRunner{handle: func(args []string) (string, error) {
		if args[0] != "search" {
			t.Fatalf("unexpected op: %v", args)
		}
		if !argsEndWith(args, "--jql", "assignee = currentUser()", "--all") {
			t.Fatalf("args = %v, want --jql <expr> --all", args)
		}
		return `{"items":[{"key":"PROJ-1","summary":"a","status":"To Do","url":"https://example.atlassian.net/browse/PROJ-1"}],"truncated":false}`, nil
	}}
	b := New(fr)

	got, err := b.Search(context.Background(), "assignee = currentUser()", nil)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Search results = %+v, want exactly 1", got)
	}
	want := schema.SearchResult{Type: "issue", ID: "PROJ-1", Title: "a", URL: "https://example.atlassian.net/browse/PROJ-1", Source: "pg-connector-issue-jira"}
	if got[0].Type != want.Type || got[0].ID != want.ID || got[0].Title != want.Title || got[0].URL != want.URL || got[0].Source != want.Source || len(got[0].Attributes) != 0 {
		t.Fatalf("Search()[0] = %+v, want %+v", got[0], want)
	}
}

func TestBackend_Search_EmptyQuery_IsInvalidArgument(t *testing.T) {
	b := New(&fakeRunner{})

	_, err := b.Search(context.Background(), "   ", nil)
	if !errors.Is(err, scriptout.ErrInvalidArgument) {
		t.Fatalf("err = %v, want errors.Is(err, ErrInvalidArgument)", err)
	}
}

func TestBackend_Search_RunFailure_ClassifiedError(t *testing.T) {
	fr := &fakeRunner{handle: func(args []string) (string, error) {
		return "", errors.New("exit status 1: pjira: search: status 401 Unauthorized")
	}}
	b := New(fr)

	_, err := b.Search(context.Background(), "assignee = currentUser()", nil)
	if !errors.Is(err, scriptout.ErrUnauthenticated) {
		t.Fatalf("err = %v, want errors.Is(err, ErrUnauthenticated)", err)
	}
}

func containsArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

// TestClassifyPJIRAErrorMessage_AuthStatusNotKeyDigits pins the
// anchored-match fix for bead pg2-uzsfj: the classifier recognises auth only
// by pjira's real shapes ("status 401 Unauthorized", "status 403 Forbidden",
// and the write paths' ": unauthenticated"), never by bare digits or words,
// so an issue key like PROJ-401 in an otherwise-unrelated failure is not an
// auth failure. Messages use pjira's real format (client.go:
// "pjira: <op> <KEY>: status <resp.Status>", wrapped by the runner as
// "exit status 1: ...").
func TestClassifyPJIRAErrorMessage_AuthStatusNotKeyDigits(t *testing.T) {
	tests := []struct {
		name string
		msg  string
		want error
	}{
		// Real auth failures stay unauthenticated.
		{"real 401 get issue", "exit status 1: pjira: get issue PROJ-1: status 401 Unauthorized", scriptout.ErrUnauthenticated},
		{"real 403 get issue", "exit status 1: pjira: get issue PROJ-1: status 403 Forbidden", scriptout.ErrUnauthenticated},
		{"real 401 search", "exit status 1: pjira: search: status 401 Unauthorized", scriptout.ErrUnauthenticated},
		{"real 401 with key containing 401", "exit status 1: pjira: get issue PROJ-401: status 401 Unauthorized", scriptout.ErrUnauthenticated},
		{"real 403 with key containing 403", "exit status 1: pjira: get issue ABC-403: status 403 Forbidden", scriptout.ErrUnauthenticated},
		{"create issue unauthenticated", "exit status 1: pjira: create issue: unauthenticated", scriptout.ErrUnauthenticated},
		{"transition unauthenticated", "exit status 1: pjira: transition PROJ-7: unauthenticated", scriptout.ErrUnauthenticated},
		{"add comment unauthenticated", "exit status 1: pjira: add comment PROJ-7: unauthenticated", scriptout.ErrUnauthenticated},

		// An issue key that merely contains 401/403 is NOT an auth failure.
		{"key PROJ-401 with 500", "exit status 1: pjira: get issue PROJ-401: status 500 Internal Server Error", scriptout.ErrUnavailable},
		{"key ABC-403 with 502", "exit status 1: pjira: get issue ABC-403: status 502 Bad Gateway", scriptout.ErrUnavailable},
		{"key PROJ-4010 with 500", "exit status 1: pjira: get issue PROJ-4010: status 500 Internal Server Error", scriptout.ErrUnavailable},
		{"transition key PROJ-401 list failure", "exit status 1: pjira: transition PROJ-401: list transitions: status 503 Service Unavailable", scriptout.ErrUnavailable},
		{"key PROJ-403 not found stays not_found", "exit status 1: pjira: issue PROJ-403 not found", scriptout.ErrNotFound},
		{"key PROJ-401 no transition stays invalid_argument", `exit status 1: pjira: transition PROJ-401: no transition to state "Done" available`, scriptout.ErrInvalidArgument},
		{"key PROJ-401 with a 429", "exit status 1: pjira: get issue PROJ-401: status 429 Too Many Requests", scriptout.ErrUnavailable},

		// The words alone (an issue key or summary fragment) are not auth either.
		{"key spelled FORBIDDEN", "exit status 1: pjira: get issue FORBIDDEN-12: status 500 Internal Server Error", scriptout.ErrUnavailable},
		{"key spelled UNAUTHORIZED", "exit status 1: pjira: get issue UNAUTHORIZED-3: status 500 Internal Server Error", scriptout.ErrUnavailable},
		{"key spelled UNAUTHENTICATED", "exit status 1: pjira: get issue UNAUTHENTICATED-5: status 500 Internal Server Error", scriptout.ErrUnavailable},
		{"exec failure stays unavailable", `exec: "pjira": executable file not found in $PATH`, scriptout.ErrUnavailable},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := classifyPJIRAErrorMessage(tc.msg)
			if !errors.Is(err, tc.want) {
				t.Fatalf("classifyPJIRAErrorMessage(%q) = %v, want errors.Is(err, %v)", tc.msg, err, tc.want)
			}
			if tc.want != scriptout.ErrUnauthenticated && errors.Is(err, scriptout.ErrUnauthenticated) {
				t.Fatalf("classifyPJIRAErrorMessage(%q) = %v, must NOT be unauthenticated", tc.msg, err)
			}
		})
	}
}

// ----------------------------------------------------------------------
// pjira activity decode fields (created, reporter, changelog, comments)
// ----------------------------------------------------------------------

// pjiraActivityIssueJSON is a pjira-shaped issue carrying the activity fields,
// with Jira's raw non-RFC3339 "+0000" timestamp form that pjira forwards
// unchanged.
const pjiraActivityIssueJSON = `{"key":"PROJ-7","summary":"s","status":"Done","issuetype":"Task","labels":[],` +
	`"url":"https://example.atlassian.net/browse/PROJ-7","created":"2026-01-01T00:00:00.000+0000",` +
	`"reporter":{"email":"rep@example.com","account_id":"acc-r","display_name":"Rep"},` +
	`"changelog":[{"id":"h-900","field":"status","from":"Open","to":"Done",` +
	`"author":{"email":"h@example.com","account_id":"acc-h","display_name":"H"},"at":"2026-01-02T00:00:00.000+0000"}],` +
	`"comments":[{"id":"c-501","author":{"email":"c@example.com","account_id":"acc-c","display_name":"C"},` +
	`"body":"a note","created":"2026-01-03T00:00:00.000+0000"}]}`

func assertActivityFields(t *testing.T, iss *pjiraIssue) {
	t.Helper()
	if iss.Created != "2026-01-01T00:00:00.000+0000" {
		t.Fatalf("Created = %q, want raw +0000 text byte-for-byte", iss.Created)
	}
	if iss.Reporter == nil || *iss.Reporter != (pjiraUser{Email: "rep@example.com", AccountID: "acc-r", DisplayName: "Rep"}) {
		t.Fatalf("Reporter = %+v", iss.Reporter)
	}
	if len(iss.Changelog) != 1 {
		t.Fatalf("Changelog = %+v, want 1 entry", iss.Changelog)
	}
	wantCL := pjiraChangelogEntry{
		ID: "h-900", Field: "status", From: "Open", To: "Done",
		Author: pjiraUser{Email: "h@example.com", AccountID: "acc-h", DisplayName: "H"},
		At:     "2026-01-02T00:00:00.000+0000",
	}
	if iss.Changelog[0] != wantCL {
		t.Fatalf("Changelog[0] = %+v, want %+v", iss.Changelog[0], wantCL)
	}
	if len(iss.Comments) != 1 {
		t.Fatalf("Comments = %+v, want 1 entry", iss.Comments)
	}
	wantC := pjiraComment{
		ID:      "c-501",
		Author:  pjiraUser{Email: "c@example.com", AccountID: "acc-c", DisplayName: "C"},
		Body:    "a note",
		Created: "2026-01-03T00:00:00.000+0000",
	}
	if iss.Comments[0] != wantC {
		t.Fatalf("Comments[0] = %+v, want %+v", iss.Comments[0], wantC)
	}
}

func TestDecodePJIRAIssue_ActivityFields(t *testing.T) {
	iss, err := decodePJIRAIssue(pjiraActivityIssueJSON)
	if err != nil {
		t.Fatalf("decodePJIRAIssue: %v", err)
	}
	assertActivityFields(t, iss)
}

func TestDecodePJIRASearchResult_ActivityFields(t *testing.T) {
	res, err := decodePJIRASearchResult(`{"items":[` + pjiraActivityIssueJSON + `],"truncated":false}`)
	if err != nil {
		t.Fatalf("decodePJIRASearchResult: %v", err)
	}
	if len(res.Items) != 1 {
		t.Fatalf("Items = %d, want 1", len(res.Items))
	}
	assertActivityFields(t, &res.Items[0])
}

func TestDecodePJIRASearchResult_WithoutActivityFields(t *testing.T) {
	res, err := decodePJIRASearchResult(`{"items":[{"key":"PROJ-1","summary":"a","status":"To Do"}],"truncated":false}`)
	if err != nil {
		t.Fatalf("decodePJIRASearchResult: %v", err)
	}
	it := res.Items[0]
	if it.Key != "PROJ-1" || it.Created != "" || it.Reporter != nil || it.Changelog != nil || it.Comments != nil {
		t.Fatalf("absent keys must decode to zero values, got %+v", it)
	}
}

// TestBackend_Show_ActivityFieldsDoNotChangeOutput proves the extra decode
// fields are invisible to the schema.Issue Show returns: the same issue with
// and without them maps to an identical result.
func TestBackend_Show_ActivityFieldsDoNotChangeOutput(t *testing.T) {
	base := `{"key":"PROJ-7","summary":"s","status":"Done","issuetype":"Task","labels":[],` +
		`"url":"https://example.atlassian.net/browse/PROJ-7"}`
	show := func(raw string) *schema.Issue {
		b := New(&fakeRunner{handle: func([]string) (string, error) { return raw, nil }})
		got, err := b.Show(context.Background(), "PROJ-7")
		if err != nil {
			t.Fatalf("Show: %v", err)
		}
		return got
	}
	with, without := show(pjiraActivityIssueJSON), show(base)
	// AsOf is stamped per call; compare with it cleared.
	with.AsOf, without.AsOf = "", ""
	jw, _ := json.Marshal(with)
	jo, _ := json.Marshal(without)
	if string(jw) != string(jo) {
		t.Fatalf("Show output changed by activity fields:\n with:    %s\n without: %s", jw, jo)
	}
}

// TestJiraBackendCarriesStatusCategory (bead pg2-mj0jv, daily-focus design
// item (r)): pjira's status_category rides onto schema.Issue.StatusCategory
// unchanged for each of its closed values, and an absent key (pjira omits it
// for Jira's legacy "No Category", or predates the field) stays empty — on
// both the show path and the list path.
func TestJiraBackendCarriesStatusCategory(t *testing.T) {
	cases := []struct {
		name, field, want string
	}{
		{"new", `"status_category":"new",`, "new"},
		{"indeterminate", `"status_category":"indeterminate",`, "indeterminate"},
		{"done", `"status_category":"done",`, "done"},
		{"absent", ``, ""},
	}
	for _, c := range cases {
		t.Run("show/"+c.name, func(t *testing.T) {
			raw := `{"key":"PROJ-1","summary":"s","status":"Complete",` + c.field +
				`"issuetype":"Task","labels":[],"url":"https://example.atlassian.net/browse/PROJ-1"}`
			b := New(&fakeRunner{handle: func([]string) (string, error) { return raw, nil }})
			got, err := b.Show(context.Background(), "PROJ-1")
			if err != nil {
				t.Fatalf("Show: %v", err)
			}
			if got.StatusCategory != c.want {
				t.Fatalf("StatusCategory = %q, want %q", got.StatusCategory, c.want)
			}
			// The status NAME is untouched by the category.
			if got.State != "Complete" {
				t.Fatalf("State = %q, want Complete", got.State)
			}
		})
		t.Run("list/"+c.name, func(t *testing.T) {
			raw := `{"items":[{"key":"PROJ-1","summary":"s","status":"Complete",` + c.field +
				`"issuetype":"Task","labels":[]}],"truncated":false}`
			b := New(&fakeRunner{handle: func([]string) (string, error) { return raw, nil }})
			got, err := b.List(context.Background(), []string{"project = PROJ"}, false, nil)
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if len(got.Entities) != 1 || got.Entities[0].StatusCategory != c.want {
				t.Fatalf("Entities = %+v, want one entity with StatusCategory %q", got.Entities, c.want)
			}
		})
	}
}

// ----------------------------------------------------------------------
// Parent mapping (bead pg2-upb9j, design item (u) of the daily-focus
// store-first design: pjira's `parent` key becomes schema.Issue.Parent)
// ----------------------------------------------------------------------

// showJSON runs Show against a fake pjira that answers the given
// `pjira issue` JSON, with an operator identity that matches no assignee so
// the call stays a single pjira invocation.
func showJSON(t *testing.T, out string) *schema.Issue {
	t.Helper()
	fr := &fakeRunner{handle: func(args []string) (string, error) {
		if args[0] != "issue" {
			t.Fatalf("unexpected op: %v", args)
		}
		return out, nil
	}}
	b := New(fr)
	b.getenv = func(string) string { return "operator@example.com" }
	got, err := b.Show(context.Background(), "PROJ-2")
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	return got
}

// TestJiraBackendMapsParent: a child of an Epic carries the Epic's key as
// Parent, on both Show and List (the list summary feeds the fingerprint).
func TestJiraBackendMapsParent(t *testing.T) {
	got := showJSON(t, `{"key":"PROJ-2","summary":"child","status":"To Do","issuetype":"Story",`+
		`"url":"https://example.atlassian.net/browse/PROJ-2","project":"PROJ","parent":"PROJ-1"}`)
	if got.Parent != "PROJ-1" {
		t.Fatalf("Show Parent = %q, want PROJ-1", got.Parent)
	}

	fr := &fakeRunner{handle: func(args []string) (string, error) {
		return `{"items":[{"key":"PROJ-2","summary":"child","status":"To Do","parent":"PROJ-1"},` +
			`{"key":"PROJ-3","summary":"orphan","status":"To Do"}],"truncated":false}`, nil
	}}
	res, err := New(fr).List(context.Background(), []string{"project = PROJ"}, false, nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	parents := map[string]string{}
	for _, e := range res.Entities {
		parents[e.ID] = e.Parent
	}
	if parents["PROJ-2"] != "PROJ-1" || parents["PROJ-3"] != "" {
		t.Fatalf("List parents = %v, want PROJ-2 -> PROJ-1 and PROJ-3 -> empty", parents)
	}
}

// TestJiraBackendNoParentLeavesParentEmpty: pjira omits `parent` for an issue
// with none (an Epic itself, or an unparented issue); Parent stays empty and
// is omitted from the wire JSON.
func TestJiraBackendNoParentLeavesParentEmpty(t *testing.T) {
	got := showJSON(t, `{"key":"PROJ-2","summary":"epic","status":"To Do","issuetype":"Epic"}`)
	if got.Parent != "" {
		t.Fatalf("Parent = %q, want empty", got.Parent)
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"parent"`) {
		t.Fatalf("wire JSON carries a parent key for a parentless issue: %s", raw)
	}
}

// TestJiraBackendSubtaskParentIsItsParentIssue: a sub-task's Parent is its
// parent issue's key (a story, not an epic); the backend maps the key as
// pjira reports it and does not walk the hierarchy.
func TestJiraBackendSubtaskParentIsItsParentIssue(t *testing.T) {
	got := showJSON(t, `{"key":"PROJ-2","summary":"sub","status":"To Do","issuetype":"Sub-task","parent":"PROJ-7"}`)
	if got.Parent != "PROJ-7" || got.IssueType != "Sub-task" {
		t.Fatalf("got Parent=%q IssueType=%q, want PROJ-7 / Sub-task", got.Parent, got.IssueType)
	}
}

// ----------------------------------------------------------------------
// Children (bead pg2-sii5c, the Jira half of design item (t))
// ----------------------------------------------------------------------

// childrenFake answers `pjira issue -- <key>` for the keys in known and
// `pjira search` with searchOut, recording the JQL of each search.
func childrenFake(known map[string]string, searchOut string, jqls *[]string) *fakeRunner {
	return &fakeRunner{handle: func(args []string) (string, error) {
		switch args[0] {
		case "issue":
			key := args[len(args)-1]
			if _, ok := known[key]; !ok {
				return "", errors.New("pjira issue -- " + key + ": exit status 1: pjira: issue " + key + " not found")
			}
			return `{"key":"` + key + `","summary":"epic","status":"To Do","issuetype":"Epic"}`, nil
		case "search":
			*jqls = append(*jqls, args[2])
			return searchOut, nil
		}
		return "", errors.New("unexpected op: " + args[0])
	}}
}

// TestJiraChildren covers the three outcomes of the children op: the
// non-closed direct children of a Jira key (carrying Parent), an empty list
// for a key with none, and not_found for a key Jira does not own.
func TestJiraChildren(t *testing.T) {
	known := map[string]string{"PROJ-1": "", "PROJ-9": ""}

	t.Run("children", func(t *testing.T) {
		var jqls []string
		fr := childrenFake(known, `{"truncated":false,"items":[`+
			`{"key":"PROJ-2","summary":"one","status":"To Do","status_category":"new","parent":"PROJ-1"},`+
			`{"key":"PROJ-3","summary":"two","status":"In Progress","status_category":"indeterminate","parent":"PROJ-1"}]}`, &jqls)
		got, err := New(fr).Children(context.Background(), "PROJ-1")
		if err != nil {
			t.Fatalf("Children: %v", err)
		}
		if len(got.Children) != 2 || got.Children[0].ID != "PROJ-2" || got.Children[1].ID != "PROJ-3" ||
			got.Children[0].Parent != "PROJ-1" || got.Children[1].State != "In Progress" {
			t.Fatalf("children = %+v", got.Children)
		}
		if len(jqls) != 1 || jqls[0] != `parent = "PROJ-1" AND statusCategory != Done ORDER BY key ASC` {
			t.Fatalf("search JQL = %q, want the parent clause excluding the Done category", jqls)
		}
	})

	t.Run("none", func(t *testing.T) {
		var jqls []string
		fr := childrenFake(known, `{"truncated":false,"items":[]}`, &jqls)
		got, err := New(fr).Children(context.Background(), "PROJ-9")
		if err != nil {
			t.Fatalf("Children: %v", err)
		}
		if got.Children == nil || len(got.Children) != 0 {
			t.Fatalf("children = %#v, want a non-nil empty list", got.Children)
		}
	})

	t.Run("non-Jira key", func(t *testing.T) {
		var jqls []string
		fr := childrenFake(known, `{"truncated":false,"items":[]}`, &jqls)
		_, err := New(fr).Children(context.Background(), "pg2-2j5ac.52")
		if !errors.Is(err, scriptout.ErrNotFound) {
			t.Fatalf("err = %v, want errors.Is(err, ErrNotFound)", err)
		}
		if len(fr.calls) != 0 {
			t.Fatalf("a non-Jira key must not reach pjira, got %v", fr.calls)
		}
	})

	t.Run("unknown Jira key", func(t *testing.T) {
		var jqls []string
		fr := childrenFake(known, `{"truncated":false,"items":[]}`, &jqls)
		_, err := New(fr).Children(context.Background(), "PROJ-404")
		if !errors.Is(err, scriptout.ErrNotFound) {
			t.Fatalf("err = %v, want errors.Is(err, ErrNotFound)", err)
		}
		if len(jqls) != 0 {
			t.Fatalf("an unknown key must not be searched, got %v", jqls)
		}
	})

	t.Run("empty id", func(t *testing.T) {
		fr := &fakeRunner{}
		_, err := New(fr).Children(context.Background(), "  ")
		if !errors.Is(err, scriptout.ErrInvalidArgument) || len(fr.calls) != 0 {
			t.Fatalf("err = %v calls = %v, want invalid_argument and no pjira call", err, fr.calls)
		}
	})
}

// TestJiraChildrenFailsClosed: a pjira failure, an undecodable search and a
// truncated search are errors, never a (possibly short) list.
func TestJiraChildrenFailsClosed(t *testing.T) {
	known := map[string]string{"PROJ-1": ""}
	var jqls []string

	truncated := childrenFake(known, `{"truncated":true,"items":[{"key":"PROJ-2","summary":"one","status":"To Do"}]}`, &jqls)
	if _, err := New(truncated).Children(context.Background(), "PROJ-1"); !errors.Is(err, scriptout.ErrUnavailable) {
		t.Fatalf("truncated: err = %v, want ErrUnavailable", err)
	}

	garbled := childrenFake(known, `not json`, &jqls)
	if _, err := New(garbled).Children(context.Background(), "PROJ-1"); !errors.Is(err, scriptout.ErrUnavailable) {
		t.Fatalf("garbled: err = %v, want ErrUnavailable", err)
	}

	down := &fakeRunner{handle: func(args []string) (string, error) {
		if args[0] == "issue" {
			return `{"key":"PROJ-1","summary":"epic","status":"To Do"}`, nil
		}
		return "", errors.New("pjira: network down")
	}}
	if _, err := New(down).Children(context.Background(), "PROJ-1"); !errors.Is(err, scriptout.ErrUnavailable) {
		t.Fatalf("search failure: err = %v, want ErrUnavailable", err)
	}
}
