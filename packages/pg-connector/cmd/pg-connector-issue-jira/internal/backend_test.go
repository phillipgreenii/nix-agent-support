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
// List (bead pg2-2j5ac.28.1)
// ----------------------------------------------------------------------

func TestBackend_List_SingleExpr_JQLPassedThrough(t *testing.T) {
	fr := &fakeRunner{handle: func(args []string) (string, error) {
		if args[0] != "search" {
			t.Fatalf("unexpected op: %v", args)
		}
		if !argsEndWith(args, "--jql", "assignee = currentUser()", "--all") {
			t.Fatalf("args = %v, want --jql <expr> --all", args)
		}
		return `{"items":[{"key":"PROJ-1","summary":"a","status":"To Do"}],"truncated":false}`, nil
	}}
	b := New(fr)

	got, err := b.List(context.Background(), []string{"assignee = currentUser()"}, false)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got.Entities) != 1 || got.Entities[0].ID != "PROJ-1" {
		t.Fatalf("Entities = %+v", got.Entities)
	}
	if len(got.PresentIDs) != 1 || got.PresentIDs[0] != "PROJ-1" {
		t.Fatalf("PresentIDs = %+v", got.PresentIDs)
	}
	if got.Cursor != nil {
		t.Fatalf("Cursor = %v, want nil (always null)", got.Cursor)
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

	got, err := b.List(context.Background(), []string{"assignee = currentUser()", "labels = focus"}, false)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2 (one per expression)", calls)
	}
	if len(got.Entities) != 2 {
		t.Fatalf("Entities = %+v, want exactly 2 after dedup by key (PROJ-2 appeared in both)", got.Entities)
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

	got, err := b.List(context.Background(), []string{"assignee = currentUser()"}, true)
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

func TestBackend_List_RunFailure_ClassifiedError(t *testing.T) {
	fr := &fakeRunner{handle: func(args []string) (string, error) {
		return "", errors.New("pjira: 401 unauthorized")
	}}
	b := New(fr)

	_, err := b.List(context.Background(), []string{"assignee = currentUser()"}, false)
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
