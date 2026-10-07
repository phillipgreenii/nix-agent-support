package internal

import (
	"context"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-thread-slack/internal/eventlog"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// fakeRunner is a minimal double for Runner, keyed by prompt substring so
// a single test can script different replies for different calls (e.g.
// List's per-modifier fan-out) — mirrors
// cmd/pg-connector-issue-jira/internal's own fakeRunner-by-args
// convention, adapted to a single prompt string.
type fakeRunner struct {
	handle func(prompt string) (string, error)
}

// Run implements Runner.Run. jsonSchema (backend.go's showReplySchema/
// listReplySchema, passed through by Backend) is deliberately ignored
// here — every test in this file exercises Backend's own reply-shape
// validation against a canned string, never the real `claude -p
// --json-schema` CLI behavior itself (that is runner_test.go's
// TestCLIRunner_Command_AppendsJSONSchemaWhenProvided's job).
func (f *fakeRunner) Run(_ context.Context, prompt string, _ string) (string, error) {
	return f.handle(prompt)
}

func (f *fakeRunner) Binary() string { return "claude" }

// envelope wraps a raw inner JSON string the way claude -p
// --output-format json's own stdout does — a small test helper so every
// case below does not hand-escape JSON-in-JSON itself.
func envelope(inner string) string {
	return `{"result":` + toJSONString(inner) + `,"is_error":false}`
}

// toJSONString renders s as a JSON string literal (Go's %q happens to
// produce valid JSON string escaping for the ASCII fixtures this file
// uses).
func toJSONString(s string) string {
	return `"` + strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), `"`, `\"`) + `"`
}

// ----------------------------------------------------------------------
// Show
// ----------------------------------------------------------------------

func TestBackend_Show_Success(t *testing.T) {
	b := New(&fakeRunner{handle: func(prompt string) (string, error) {
		if !strings.Contains(prompt, `"1726000000.000100"`) {
			t.Fatalf("prompt did not carry the requested id: %q", prompt)
		}
		return envelope(`{"found":true,"id":"1726000000.000100","channel":"C1","permalink":"https://x.invalid/p1","started_by":"U1","participants":["U1","U2"],"last_reply_at":"1726000100.000200","reply_count":2,"text":"root msg","mentions_me":true}`), nil
	}})
	got, err := b.Show(context.Background(), "1726000000.000100")
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if got.ID != "1726000000.000100" || got.Channel != "C1" || got.Permalink != "https://x.invalid/p1" ||
		got.StartedBy != "U1" || got.LastReplyAt != "1726000100.000200" || got.ReplyCount != 2 ||
		got.Text != "root msg" || !got.MentionsMe {
		t.Fatalf("got = %+v", got)
	}
	if len(got.Participants) != 2 || got.Participants[0] != "U1" || got.Participants[1] != "U2" {
		t.Fatalf("Participants = %+v", got.Participants)
	}
	if got.AsOf == "" {
		t.Fatal("AsOf must be populated for a live read")
	}
	if got.Stale {
		t.Fatal("Stale must be false: this backend performs a live call on every request")
	}
}

func TestBackend_Show_EmptyIDIsInvalidArgument(t *testing.T) {
	b := New(&fakeRunner{handle: func(prompt string) (string, error) {
		t.Fatal("runner must not be invoked when id is empty")
		return "", nil
	}})
	_, err := b.Show(context.Background(), "   ")
	if !errors.Is(err, scriptout.ErrInvalidArgument) {
		t.Fatalf("err = %v, want errors.Is(err, ErrInvalidArgument)", err)
	}
}

func TestBackend_Show_NotFound(t *testing.T) {
	b := New(&fakeRunner{handle: func(prompt string) (string, error) {
		return envelope(`{"found":false}`), nil
	}})
	_, err := b.Show(context.Background(), "missing-id")
	if !errors.Is(err, scriptout.ErrNotFound) {
		t.Fatalf("err = %v, want errors.Is(err, ErrNotFound)", err)
	}
}

func TestBackend_Show_MissingFoundKeyTreatedAsNotFound(t *testing.T) {
	// A model that forgets the "found" key entirely must not be treated
	// as a fabricated found-with-empty-fields result (backend.go's own
	// doc comment on slackShowReply.Found).
	b := New(&fakeRunner{handle: func(prompt string) (string, error) {
		return envelope(`{"id":"x","channel":"C1","permalink":"https://x.invalid/p1"}`), nil
	}})
	_, err := b.Show(context.Background(), "some-id")
	if !errors.Is(err, scriptout.ErrNotFound) {
		t.Fatalf("err = %v, want errors.Is(err, ErrNotFound)", err)
	}
}

func TestBackend_Show_MalformedInnerReplyIsUnavailable(t *testing.T) {
	b := New(&fakeRunner{handle: func(prompt string) (string, error) {
		return envelope(`this is not json`), nil
	}})
	_, err := b.Show(context.Background(), "some-id")
	if !errors.Is(err, scriptout.ErrUnavailable) {
		t.Fatalf("err = %v, want errors.Is(err, ErrUnavailable)", err)
	}
}

func TestBackend_Show_MalformedOuterEnvelopeIsUnavailable(t *testing.T) {
	b := New(&fakeRunner{handle: func(prompt string) (string, error) {
		return "not json at all", nil
	}})
	_, err := b.Show(context.Background(), "some-id")
	if !errors.Is(err, scriptout.ErrUnavailable) {
		t.Fatalf("err = %v, want errors.Is(err, ErrUnavailable)", err)
	}
}

func TestBackend_Show_IsErrorEnvelopeIsUnavailable(t *testing.T) {
	b := New(&fakeRunner{handle: func(prompt string) (string, error) {
		return `{"result":"max turns exceeded","is_error":true}`, nil
	}})
	_, err := b.Show(context.Background(), "some-id")
	if !errors.Is(err, scriptout.ErrUnavailable) {
		t.Fatalf("err = %v, want errors.Is(err, ErrUnavailable)", err)
	}
}

func TestBackend_Show_RunFailureIsUnavailable(t *testing.T) {
	b := New(&fakeRunner{handle: func(prompt string) (string, error) {
		return "", errors.New("claude: executable file not found in $PATH")
	}})
	_, err := b.Show(context.Background(), "some-id")
	if !errors.Is(err, scriptout.ErrUnavailable) {
		t.Fatalf("err = %v, want errors.Is(err, ErrUnavailable)", err)
	}
}

func TestBackend_Show_MissingRequiredFieldIsUnavailable(t *testing.T) {
	// found:true but no channel/permalink — schema-invalid, never a
	// panic, never a best-effort partial Thread (this bead's own
	// acceptance criteria).
	b := New(&fakeRunner{handle: func(prompt string) (string, error) {
		return envelope(`{"found":true,"id":"x"}`), nil
	}})
	_, err := b.Show(context.Background(), "some-id")
	if !errors.Is(err, scriptout.ErrUnavailable) {
		t.Fatalf("err = %v, want errors.Is(err, ErrUnavailable)", err)
	}
}

// ----------------------------------------------------------------------
// List
// ----------------------------------------------------------------------

func TestBackend_List_TruncatedAlwaysTrue_CursorAlwaysNil(t *testing.T) {
	b := New(&fakeRunner{handle: func(prompt string) (string, error) {
		return envelope(`{"items":[]}`), nil
	}})
	got, err := b.List(context.Background(), schema.QueryExpr{"in:#general"}, false)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if !got.Truncated {
		t.Fatal("Truncated must be unconditionally true for this backend (design D23)")
	}
	if got.Cursor != nil {
		t.Fatalf("Cursor = %v, want nil (no incremental-listing backend at this phase)", got.Cursor)
	}
}

func TestBackend_List_UnionsAndDedupesAcrossModifiers(t *testing.T) {
	calls := 0
	b := New(&fakeRunner{handle: func(prompt string) (string, error) {
		calls++
		switch {
		case strings.Contains(prompt, `"in:#general"`):
			return envelope(`{"items":[{"id":"t1","channel":"C1","permalink":"https://x.invalid/1","text":"a"},{"id":"t2","channel":"C1","permalink":"https://x.invalid/2","text":"b"}]}`), nil
		case strings.Contains(prompt, `"in:#random"`):
			// t1 repeats across both modifiers and must be deduplicated.
			return envelope(`{"items":[{"id":"t1","channel":"C1","permalink":"https://x.invalid/1","text":"a"},{"id":"t3","channel":"C2","permalink":"https://x.invalid/3","text":"c"}]}`), nil
		}
		t.Fatalf("unexpected prompt: %q", prompt)
		return "", nil
	}})
	got, err := b.List(context.Background(), schema.QueryExpr{"in:#general", "in:#random"}, false)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2 (one per query expression)", calls)
	}
	if len(got.Entities) != 3 {
		t.Fatalf("Entities = %+v, want exactly 3 deduplicated threads", got.Entities)
	}
	if len(got.PresentIDs) != 3 {
		t.Fatalf("PresentIDs = %v, want exactly 3", got.PresentIDs)
	}
}

func TestBackend_List_IDsOnlyOmitsEntities(t *testing.T) {
	b := New(&fakeRunner{handle: func(prompt string) (string, error) {
		return envelope(`{"items":[{"id":"t1","channel":"C1","permalink":"https://x.invalid/1","text":"a"}]}`), nil
	}})
	got, err := b.List(context.Background(), schema.QueryExpr{"in:#general"}, true)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got.Entities != nil {
		t.Fatalf("Entities = %+v, want nil when idsOnly is true", got.Entities)
	}
	if len(got.PresentIDs) != 1 || got.PresentIDs[0] != "t1" {
		t.Fatalf("PresentIDs = %v", got.PresentIDs)
	}
}

func TestBackend_List_EmptyModifierSkipped(t *testing.T) {
	b := New(&fakeRunner{handle: func(prompt string) (string, error) {
		t.Fatal("runner must not be invoked for a blank modifier expression")
		return "", nil
	}})
	got, err := b.List(context.Background(), schema.QueryExpr{"   ", ""}, false)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got.Entities) != 0 {
		t.Fatalf("Entities = %+v, want empty", got.Entities)
	}
}

func TestBackend_List_ItemWithEmptyIDSkippedSilently(t *testing.T) {
	b := New(&fakeRunner{handle: func(prompt string) (string, error) {
		return envelope(`{"items":[{"id":"","channel":"C1","permalink":"https://x.invalid/1","text":"a"},{"id":"t1","channel":"C1","permalink":"https://x.invalid/1","text":"b"}]}`), nil
	}})
	got, err := b.List(context.Background(), schema.QueryExpr{"in:#general"}, false)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got.Entities) != 1 || got.Entities[0].ID != "t1" {
		t.Fatalf("Entities = %+v, want exactly the one item with a non-empty id", got.Entities)
	}
}

func TestBackend_List_MalformedReplyIsUnavailable(t *testing.T) {
	b := New(&fakeRunner{handle: func(prompt string) (string, error) {
		return envelope(`not json`), nil
	}})
	_, err := b.List(context.Background(), schema.QueryExpr{"in:#general"}, false)
	if !errors.Is(err, scriptout.ErrUnavailable) {
		t.Fatalf("err = %v, want errors.Is(err, ErrUnavailable)", err)
	}
}

// ----------------------------------------------------------------------
// compile-time interface checks
// ----------------------------------------------------------------------

func TestBackend_ImplementsProvider(t *testing.T) {
	_ = New(&fakeRunner{})
}

// ----------------------------------------------------------------------
// Non-zero exit with a claude -p envelope on stdout (bead pg2-ezwut)
// ----------------------------------------------------------------------

// A non-zero `claude -p` exit still writes its JSON envelope to stdout;
// CLIRunner.Run returns that stdout alongside the exec error, so the
// backend can surface the envelope's own cause text.
const execFailMsg = "claude -p --output-format json: exit status 1: "

func TestBackend_RunFailure_SurfacesEnvelopeResultFromStdout(t *testing.T) {
	b := New(&fakeRunner{handle: func(string) (string, error) {
		return `{"type":"result","subtype":"error_during_execution","is_error":true,"result":"API Error: 529 overloaded"}`,
			errors.New(execFailMsg)
	}})
	_, err := b.Show(context.Background(), "some-id")
	if !errors.Is(err, scriptout.ErrUnavailable) {
		t.Fatalf("err = %v, want errors.Is(err, ErrUnavailable)", err)
	}
	for _, want := range []string{execFailMsg, "error_during_execution", "API Error: 529 overloaded"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %q, want it to contain %q", err.Error(), want)
		}
	}
}

func TestBackend_RunFailure_EmptyOrNonJSONStdoutIsUnchanged(t *testing.T) {
	for name, stdout := range map[string]string{
		"empty":      "",
		"whitespace": "  \n",
		"non-JSON":   "Segmentation fault",
		"JSON array": `["x"]`,
		"empty obj":  `{}`,
		"null":       `null`,
		"no cause":   `{"type":"result","is_error":true}`,
	} {
		t.Run(name, func(t *testing.T) {
			b := New(&fakeRunner{handle: func(string) (string, error) {
				return stdout, errors.New(execFailMsg)
			}})
			_, err := b.Show(context.Background(), "some-id")
			if !errors.Is(err, scriptout.ErrUnavailable) {
				t.Fatalf("err = %v, want errors.Is(err, ErrUnavailable)", err)
			}
			want := "claude -p: " + execFailMsg
			if !strings.Contains(err.Error(), want) || strings.Contains(err.Error(), "stdout") {
				t.Errorf("err = %q, want unchanged text containing %q and no stdout note", err.Error(), want)
			}
		})
	}
}

func TestBackend_RunFailure_EnvelopeTextIsBounded(t *testing.T) {
	long := strings.Repeat("é", 5000) // 10000 bytes of 2-byte runes
	b := New(&fakeRunner{handle: func(string) (string, error) {
		return `{"is_error":true,"result":"` + long + `"}`, errors.New(execFailMsg)
	}})
	_, err := b.Show(context.Background(), "some-id")
	if err == nil {
		t.Fatal("want error")
	}
	if len(err.Error()) > maxEnvelopeTextBytes+1024 {
		t.Errorf("error length %d is not bounded near %d", len(err.Error()), maxEnvelopeTextBytes)
	}
	if !strings.Contains(err.Error(), "truncated") {
		t.Errorf("err = %q, want a truncation marker", err.Error())
	}
	if !utf8.ValidString(err.Error()) {
		t.Error("truncation split a multi-byte rune")
	}
}

func TestBackend_RunFailure_EnvelopeTextIsRedacted(t *testing.T) {
	secrets := []string{
		"sk-ant-api03-AbCdEf0123456789_-xyz",
		"xoxb-1234-5678-abcdefGHIJ",
		"ghp_abcdefghijklmnopqrstuvwxyz0123456789",
		"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0In0.abcDEF123_-",
		"hunter2hunter2",
	}
	b := New(&fakeRunner{handle: func(string) (string, error) {
		return `{"is_error":true,"result":"401 for ` + secrets[0] + ` and ` + secrets[1] + ` via ` + secrets[2] +
			` Authorization: Bearer ` + secrets[3] + ` password=` + secrets[4] + ` end"}`, errors.New(execFailMsg)
	}})
	_, err := b.Show(context.Background(), "some-id")
	if err == nil {
		t.Fatal("want error")
	}
	for _, s := range secrets {
		if strings.Contains(err.Error(), s) {
			t.Errorf("err = %q leaks %q", err.Error(), s)
		}
	}
	if !strings.Contains(err.Error(), "401 for") || !strings.Contains(err.Error(), "end") {
		t.Errorf("err = %q, want the non-secret text kept", err.Error())
	}
}

func TestBackend_RunFailure_EventRowCarriesEnvelopeText(t *testing.T) {
	sink := &captureSink{}
	table := instrumented(New(&fakeRunner{handle: func(string) (string, error) {
		return `{"is_error":true,"result":"Invalid API key"}`, errors.New(execFailMsg)
	}}), sink)
	_, err := table["show"].Handle(context.Background(), nil)
	if !errors.Is(err, scriptout.ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
	ev := sink.last(t)
	if ev.ErrorCode != "unavailable" || ev.FailureStage != eventlog.StageExec {
		t.Errorf("event = %+v, want error_code=unavailable failure_stage=exec", ev)
	}
	if !strings.Contains(ev.Error, "Invalid API key") {
		t.Errorf("event error = %q, want the envelope text", ev.Error)
	}
	if ev.FailureClass != eventlog.ClassAuth {
		t.Errorf("failure_class = %q, want auth (envelope text reads as auth)", ev.FailureClass)
	}
}
