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
// workspace/workspaceErr back Workspace(), defaulting to a fixed
// non-empty test value so toSchemaIssue's Tracker field has something to
// assert against.
type fakeRunner struct {
	calls        [][]string
	handle       func(args []string) (string, error)
	workspace    string
	workspaceErr error
}

func (f *fakeRunner) Run(_ context.Context, args ...string) (string, error) {
	f.calls = append(f.calls, args)
	if f.handle != nil {
		return f.handle(args)
	}
	return "", nil
}

func (f *fakeRunner) Workspace() (string, error) {
	if f.workspaceErr != nil {
		return "", f.workspaceErr
	}
	if f.workspace != "" {
		return f.workspace, nil
	}
	return "/fake/workspace", nil
}

func containsArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

// argsEndWith reports whether args ends with exactly the given tail, in
// order — used to assert that every caller-supplied id/body positional was
// placed after a literal "--" terminator [bead: pg2-usu5b], since bd's
// cobra/pflag layer stops treating anything past "--" as a flag.
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
	if got.Tracker != "/fake/workspace" {
		t.Fatalf("Tracker = %q, want /fake/workspace (bead pg2-1q9c0, AC2)", got.Tracker)
	}
	if !containsArg(fr.calls[0], "--readonly") {
		t.Fatalf("expected --readonly in Show's bd invocation (bead pg2-1q9c0, AC3), got %v", fr.calls[0])
	}
	if !argsEndWith(fr.calls[0], "--", "tp-1") {
		t.Fatalf("expected id as a literal positional after a \"--\" terminator (bead pg2-usu5b), got %v", fr.calls[0])
	}
}

// TestBackend_Show_IDLooksLikeBDFlag locks in the pg2-usu5b fix: an id that
// is itself a valid bd flag string (e.g. "--current", which really does
// mean "show the last-touched issue" per `bd show --help`) must not be
// interpretable as a flag by bd's own cobra/pflag layer — it must be sent
// as a literal positional, after a "--" terminator [review finding A-8]. Verified live
// against real bd v1.2.2 that the unescaped shape actually redirects to an
// unrelated issue and the escaped shape correctly reports not-found.
func TestBackend_Show_IDLooksLikeBDFlag(t *testing.T) {
	fr := &fakeRunner{handle: func(args []string) (string, error) {
		if !argsEndWith(args, "--", "--current") {
			t.Fatalf("expected \"--current\" escaped as a literal positional after \"--\", got %v", args)
		}
		out := `{"data":{"error":"no issues found matching the provided IDs"},"schema_version":1}`
		return out, errors.New(`bd show --readonly --json -- --current: exit status 1: Error fetching --current: no issue found matching "--current"`)
	}}
	b := New(fr)
	_, err := b.Show(context.Background(), "--current")
	if !errors.Is(err, scriptout.ErrNotFound) {
		t.Fatalf("err = %v, want wrapping ErrNotFound (bd treated \"--current\" as a literal, nonexistent id)", err)
	}
}

// TestBackend_Show_WorkspaceNotConfigured locks in bead pg2-1q9c0's AC1: a
// Runner that cannot resolve a workspace (CLIRunner.Run's own behavior when
// neither $PG_CONNECTOR_ISSUE_BEADS_DIR nor $BEADS_DIR is set) must surface
// as a well-formed scriptout.ErrUnavailable through Backend.Show, not a
// silent success against whatever tracker happened to be ambient.
func TestBackend_Show_WorkspaceNotConfigured(t *testing.T) {
	fr := &fakeRunner{handle: func(args []string) (string, error) {
		return "", ErrWorkspaceNotConfigured
	}}
	b := New(fr)
	_, err := b.Show(context.Background(), "tp-1")
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, scriptout.ErrUnavailable) {
		t.Fatalf("err = %v, want wrapping ErrUnavailable", err)
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

// TestBackend_Show_EmptyStdoutOnSuccess_FailsAtDecodeNotAvailability locks
// in finding 24's fix at the OTHER pole from
// TestBackend_Comment_SuccessWithEmptyStdout below: for an op that DOES
// need the payload (Show), an exit-0-with-empty-stdout call must still
// fail — but as a decode failure (ErrUnavailable via bdIssueFromArray),
// never silently succeeding with a zero-value issue, and never confused
// with ErrNotFound [review finding A-24].
func TestBackend_Show_EmptyStdoutOnSuccess_FailsAtDecodeNotAvailability(t *testing.T) {
	fr := &fakeRunner{handle: func(args []string) (string, error) {
		return "", nil
	}}
	b := New(fr)
	_, err := b.Show(context.Background(), "tp-1")
	if err == nil {
		t.Fatal("expected an error decoding a nil/empty payload")
	}
	if errors.Is(err, scriptout.ErrNotFound) {
		t.Fatalf("err = %v, must not be classified as ErrNotFound", err)
	}
	if !errors.Is(err, scriptout.ErrUnavailable) {
		t.Fatalf("err = %v, want wrapping ErrUnavailable", err)
	}
}

// TestBackend_Show_IncludesDescriptionAssigneeParentDeps locks in the fix
// for finding 33's "Show drops description/assignee/parent/deps from its
// response" [review finding A-33].
func TestBackend_Show_IncludesDescriptionAssigneeParentDeps(t *testing.T) {
	fr := &fakeRunner{handle: func(args []string) (string, error) {
		return `{"data":[{"id":"tp-1.1","title":"child","description":"a desc",` +
			`"status":"open","priority":1,"issue_type":"task",` +
			`"assignee":"someone@example.com","parent":"tp-1",` +
			`"dependencies":[{"id":"tp-1","title":"parent","status":"open",` +
			`"priority":1,"issue_type":"task","dependency_type":"parent-child"}]}],` +
			`"schema_version":1}`, nil
	}}
	b := New(fr)
	got, err := b.Show(context.Background(), "tp-1.1")
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if got.Description != "a desc" {
		t.Fatalf("Description = %q, want %q", got.Description, "a desc")
	}
	if got.Assignee != "someone@example.com" {
		t.Fatalf("Assignee = %q, want %q", got.Assignee, "someone@example.com")
	}
	if got.Parent != "tp-1" {
		t.Fatalf("Parent = %q, want %q", got.Parent, "tp-1")
	}
	if len(got.Deps) != 1 || got.Deps[0].ID != "tp-1" || got.Deps[0].Type != "parent-child" {
		t.Fatalf("Deps = %+v, want one dependency {ID:tp-1 Type:parent-child}", got.Deps)
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
		t.Fatalf("expected no bd invocation for an invalid call, got %v", fr.calls)
	}
	// An empty id is the CALLER's mistake, not this backend being
	// unhealthy (INV-ERR-2; bug pg2-r9iok) — it must not share
	// ErrUnavailable's "this backend cannot currently be used" meaning,
	// and must not be confused with ErrNotFound (a well-formed id that
	// genuinely doesn't exist) either — an empty id was never even a
	// candidate for lookup.
	if !errors.Is(err, scriptout.ErrInvalidArgument) {
		t.Fatalf("err = %v, want errors.Is(err, ErrInvalidArgument)", err)
	}
	if errors.Is(err, scriptout.ErrNotFound) || errors.Is(err, scriptout.ErrUnavailable) {
		t.Fatalf("err = %v, must not also be ErrNotFound/ErrUnavailable", err)
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
	if got.Tracker != "/fake/workspace" {
		t.Fatalf("Tracker = %q, want /fake/workspace (bead pg2-1q9c0, AC2)", got.Tracker)
	}
}

func TestBackend_Create_MissingTitle(t *testing.T) {
	fr := &fakeRunner{}
	b := New(fr)
	_, err := b.Create(context.Background(), issue.IssueInput{})
	if err == nil {
		t.Fatal("expected error for empty title")
	}
	if len(fr.calls) != 0 {
		t.Fatalf("expected no bd invocation for an invalid call, got %v", fr.calls)
	}
	if !errors.Is(err, scriptout.ErrInvalidArgument) {
		t.Fatalf("err = %v, want errors.Is(err, ErrInvalidArgument)", err)
	}
}

func TestBackend_Create_OmitsOptionalFlagsWhenUnset(t *testing.T) {
	fr := &fakeRunner{handle: func(args []string) (string, error) {
		for _, flag := range []string{"-p", "--type", "--labels", "--description"} {
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

// TestBackend_Create_CommaBearingLabelRoundTrips locks in finding 33's fix
// for "--labels values are joined with a bare ',' ... which splits any
// label that itself contains a comma" [review finding A-33]. The expected
// --labels value is CSV-quoted exactly the way bd's own pflag StringSlice
// flag decodes it (verified live against a real bd v1.2.2 in this bead's
// investigation).
func TestBackend_Create_CommaBearingLabelRoundTrips(t *testing.T) {
	fr := &fakeRunner{handle: func(args []string) (string, error) {
		idx := -1
		for i, a := range args {
			if a == "--labels" {
				idx = i
				break
			}
		}
		if idx == -1 || idx+1 >= len(args) {
			t.Fatalf("expected --labels <value> in args, got %v", args)
		}
		got := args[idx+1]
		want := `"foo,bar",baz`
		if got != want {
			t.Fatalf("--labels value = %q, want %q (CSV-quoted so bd decodes the embedded comma as part of one label)", got, want)
		}
		return `{"data":{"id":"tp-9","title":"t","status":"open","priority":2,"issue_type":"task","labels":["foo,bar","baz"]},"schema_version":1}`, nil
	}}
	b := New(fr)
	got, err := b.Create(context.Background(), issue.IssueInput{
		Title:  "t",
		Labels: []string{"foo,bar", "baz"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(got.Labels) != 2 || got.Labels[0] != "foo,bar" || got.Labels[1] != "baz" {
		t.Fatalf(`Labels = %v, want ["foo,bar" "baz"] (a comma-bearing label must round-trip intact)`, got.Labels)
	}
}

// TestBackend_Create_SetsDescription locks in finding 33's fix for
// "Create has no way to set a description at all" [review finding A-33].
func TestBackend_Create_SetsDescription(t *testing.T) {
	fr := &fakeRunner{handle: func(args []string) (string, error) {
		if !containsArg(args, "--description") || !containsArg(args, "a desc") {
			t.Fatalf(`expected --description "a desc" in args, got %v`, args)
		}
		return `{"data":{"id":"tp-9","title":"t","description":"a desc","status":"open","priority":2,"issue_type":"task"},"schema_version":1}`, nil
	}}
	b := New(fr)
	got, err := b.Create(context.Background(), issue.IssueInput{Title: "t", Description: "a desc"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got.Description != "a desc" {
		t.Fatalf("Description = %q, want %q", got.Description, "a desc")
	}
}

// ----------------------------------------------------------------------
// Comment
// ----------------------------------------------------------------------

func TestBackend_Comment_Success(t *testing.T) {
	fr := &fakeRunner{handle: func(args []string) (string, error) {
		if args[0] != "comment" {
			t.Fatalf("unexpected op: %v", args)
		}
		if !argsEndWith(args, "--", "tp-1", "hello there") {
			t.Fatalf("expected id/body positionals after a \"--\" terminator, got %v", args)
		}
		return `{"data":{"id":"c1","issue_id":"tp-1","text":"hello there"},"schema_version":1}`, nil
	}}
	b := New(fr)
	if err := b.Comment(context.Background(), "tp-1", "hello there"); err != nil {
		t.Fatalf("Comment: %v", err)
	}
}

// TestBackend_Comment_IDLooksLikeBDFlag locks in the pg2-usu5b fix: an id
// that is itself a valid bd flag string (e.g. "--claim") must not be
// interpretable as a flag by bd's own cobra/pflag layer — it must be sent
// as a literal positional, after a "--" terminator, exactly like any other
// id [review finding A-8].
func TestBackend_Comment_IDLooksLikeBDFlag(t *testing.T) {
	fr := &fakeRunner{handle: func(args []string) (string, error) {
		if !argsEndWith(args, "--", "--claim", "hijack attempt") {
			t.Fatalf("expected \"--claim\" escaped as a literal positional after \"--\", got %v", args)
		}
		return `{"data":{"error":"resolving --claim: no issue found matching \"--claim\""},"schema_version":1}`,
			errors.New("bd comment --json -- --claim \"hijack attempt\": exit status 1")
	}}
	b := New(fr)
	err := b.Comment(context.Background(), "--claim", "hijack attempt")
	if !errors.Is(err, scriptout.ErrNotFound) {
		t.Fatalf("err = %v, want wrapping ErrNotFound (bd treated \"--claim\" as a literal, nonexistent id)", err)
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

// TestBackend_Comment_SuccessWithEmptyStdout locks in finding 24's fix: an
// exit-0 bd invocation with genuinely empty stdout must be treated as a
// real, payload-less success, not backend ill-health [review finding A-24] — Comment already
// discards whatever payload b.run returns, so this exercises the
// hypothetical future bd version that stops echoing JSON on this op.
func TestBackend_Comment_SuccessWithEmptyStdout(t *testing.T) {
	fr := &fakeRunner{handle: func(args []string) (string, error) {
		return "", nil // exit 0, no stdout
	}}
	b := New(fr)
	if err := b.Comment(context.Background(), "tp-1", "hello"); err != nil {
		t.Fatalf("Comment: %v, want success (exit 0 + empty stdout is a real success, not ErrUnavailable)", err)
	}
}

func TestBackend_Comment_EmptyBody(t *testing.T) {
	fr := &fakeRunner{}
	b := New(fr)
	err := b.Comment(context.Background(), "tp-1", "")
	if err == nil {
		t.Fatal("expected error for empty body")
	}
	if len(fr.calls) != 0 {
		t.Fatalf("expected no bd invocation for an invalid call, got %v", fr.calls)
	}
	if !errors.Is(err, scriptout.ErrInvalidArgument) {
		t.Fatalf("err = %v, want errors.Is(err, ErrInvalidArgument)", err)
	}
}

func TestBackend_Comment_EmptyID(t *testing.T) {
	fr := &fakeRunner{}
	b := New(fr)
	err := b.Comment(context.Background(), "  ", "hello")
	if len(fr.calls) != 0 {
		t.Fatalf("expected no bd invocation for an invalid call, got %v", fr.calls)
	}
	if !errors.Is(err, scriptout.ErrInvalidArgument) {
		t.Fatalf("err = %v, want errors.Is(err, ErrInvalidArgument)", err)
	}
}

// ----------------------------------------------------------------------
// Transition
// ----------------------------------------------------------------------

func TestBackend_Transition_Success(t *testing.T) {
	fr := &fakeRunner{handle: func(args []string) (string, error) {
		if args[0] != "update" {
			t.Fatalf("unexpected op: %v", args)
		}
		if !containsArg(args, "--status") || !containsArg(args, "in_progress") {
			t.Fatalf("expected --status in_progress in args, got %v", args)
		}
		if !argsEndWith(args, "--", "tp-1") {
			t.Fatalf("expected id as a literal positional after a \"--\" terminator (bead pg2-usu5b), got %v", args)
		}
		return `{"data":[{"id":"tp-1","title":"probe","status":"in_progress","priority":2,"issue_type":"task"}],"schema_version":1}`, nil
	}}
	b := New(fr)
	if err := b.Transition(context.Background(), "tp-1", "in_progress"); err != nil {
		t.Fatalf("Transition: %v", err)
	}
}

// TestBackend_Transition_IDLooksLikeBDFlag locks in the pg2-usu5b fix: an
// id that is itself a valid bd flag string (e.g. "--claim") must not be
// interpretable as a flag by bd's own cobra/pflag layer — it must be sent
// as a literal positional, after a "--" terminator [review finding A-8]. Verified live
// against real bd v1.2.2 that the unescaped shape
// (`bd update --claim --status closed --json`) actually claims AND closes
// bd's workspace-wide "last touched" issue — an unrelated bead — while the
// escaped shape correctly reports not-found for the literal id.
func TestBackend_Transition_IDLooksLikeBDFlag(t *testing.T) {
	fr := &fakeRunner{handle: func(args []string) (string, error) {
		if !argsEndWith(args, "--", "--claim") {
			t.Fatalf("expected \"--claim\" escaped as a literal positional after \"--\", got %v", args)
		}
		return "", errors.New(`bd update --status closed --json -- --claim: exit status 1: Error resolving --claim: no issue found matching "--claim"`)
	}}
	b := New(fr)
	err := b.Transition(context.Background(), "--claim", "closed")
	if !errors.Is(err, scriptout.ErrNotFound) {
		t.Fatalf("err = %v, want wrapping ErrNotFound (bd treated \"--claim\" as a literal, nonexistent id)", err)
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

// TestBackend_Transition_SuccessWithEmptyStdout locks in finding 24's fix,
// mirroring TestBackend_Comment_SuccessWithEmptyStdout above: an exit-0
// `bd update ... --json` with empty stdout is a real success (Transition
// already discards the payload), not backend ill-health [review finding A-24].
func TestBackend_Transition_SuccessWithEmptyStdout(t *testing.T) {
	fr := &fakeRunner{handle: func(args []string) (string, error) {
		return "", nil
	}}
	b := New(fr)
	if err := b.Transition(context.Background(), "tp-1", "in_progress"); err != nil {
		t.Fatalf("Transition: %v, want success (exit 0 + empty stdout is a real success, not ErrUnavailable)", err)
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
	err := b.Transition(context.Background(), "tp-1", "")
	if err == nil {
		t.Fatal("expected error for empty target state")
	}
	if len(fr.calls) != 0 {
		t.Fatalf("expected no bd invocation for an invalid call, got %v", fr.calls)
	}
	if !errors.Is(err, scriptout.ErrInvalidArgument) {
		t.Fatalf("err = %v, want errors.Is(err, ErrInvalidArgument)", err)
	}
}

func TestBackend_Transition_EmptyID(t *testing.T) {
	fr := &fakeRunner{}
	b := New(fr)
	err := b.Transition(context.Background(), "  ", "closed")
	if len(fr.calls) != 0 {
		t.Fatalf("expected no bd invocation for an invalid call, got %v", fr.calls)
	}
	if !errors.Is(err, scriptout.ErrInvalidArgument) {
		t.Fatalf("err = %v, want errors.Is(err, ErrInvalidArgument)", err)
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

// TestPriorityVocabulary_NonEmptyAndMatchesRealBDPriorities locks in
// finding 33's fix: capabilities.vocabulary must declare a priority
// vocabulary matching what bd's own `-p`/`--priority` flag actually
// accepts ("0-4 or P0-P4"), not the invented "High"/"Medium"/"Low" scale
// schema.Issue.Priority's doc comment used to advertise [review finding A-33].
func TestPriorityVocabulary_NonEmptyAndMatchesRealBDPriorities(t *testing.T) {
	if len(PriorityVocabulary) == 0 {
		t.Fatal("PriorityVocabulary must be non-empty")
	}
	want := map[string]bool{"P0": true, "P1": true, "P2": true, "P3": true, "P4": true}
	got := map[string]bool{}
	for _, v := range PriorityVocabulary {
		got[v] = true
	}
	for w := range want {
		if !got[w] {
			t.Errorf("PriorityVocabulary missing bd priority %q", w)
		}
	}
	for g := range got {
		if !want[g] {
			t.Errorf("PriorityVocabulary has unexpected entry %q not among bd's real priority values", g)
		}
	}
}
