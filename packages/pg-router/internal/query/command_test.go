package query

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/x/cmdrun"
	"github.com/phillipgreenii/x/cmdruntest"
)

type fakeCmd struct {
	out []byte
	err error
}

func (f fakeCmd) Run(_ context.Context, _ []string) ([]byte, error) { return f.out, f.err }

func TestCommandQuery_jsonl(t *testing.T) {
	q := CommandQuery{Argv: []string{"x"}, Format: FormatJSONL}
	cmd := fakeCmd{out: []byte(`{"id":"a","type":"task","title":"A","metadata":{"k":"v"}}` + "\n" +
		`{"id":"b","type":"bug","title":"B"}` + "\n")}
	evts, err := q.Run(context.Background(), Env{Cmd: cmd})
	if err != nil {
		t.Fatal(err)
	}
	// M2: events wrap the parsed items.
	if len(evts) != 2 || evts[0].Item.ID != "a" || evts[0].Item.Metadata["k"] != "v" || evts[1].Item.ID != "b" {
		t.Fatalf("events wrong: %+v", evts)
	}
}

func TestCommandQuery_nonZeroExitPropagates(t *testing.T) {
	q := CommandQuery{Argv: []string{"x"}, Format: FormatJSONL}
	_, err := q.Run(context.Background(), Env{Cmd: fakeCmd{err: errors.New("exit 1")}})
	if err == nil {
		t.Fatal("non-zero exit must propagate as error, not empty items")
	}
}

func TestCommandQuery_emptyStdoutIsZeroItems(t *testing.T) {
	q := CommandQuery{Argv: []string{"x"}, Format: FormatJSONL}
	items, err := q.Run(context.Background(), Env{Cmd: fakeCmd{out: []byte("")}})
	if err != nil || len(items) != 0 {
		t.Fatalf("empty stdout + exit 0 = zero items, no error; got items=%v err=%v", items, err)
	}
}

func TestCommandQuery_malformedIsError(t *testing.T) {
	q := CommandQuery{Argv: []string{"x"}, Format: FormatJSONL}
	_, err := q.Run(context.Background(), Env{Cmd: fakeCmd{out: []byte("{not json}\n")}})
	if err == nil {
		t.Fatal("malformed output must error")
	}
}

func TestCommandQuery_missingIDIsError(t *testing.T) {
	q := CommandQuery{Argv: []string{"x"}, Format: FormatJSONL}
	_, err := q.Run(context.Background(), Env{Cmd: fakeCmd{out: []byte(`{"type":"task"}` + "\n")}})
	if err == nil {
		t.Fatal("record missing id must error")
	}
}

// Task 1.4: command sources gain an optional per-record `expiresAt` (RFC3339,
// camelCase matching the event wire) that is carried onto the produced event's
// Attributes — the general "extra, type-specific fields" seam event.Event
// already declares, so this task widens the rawItem contract without touching
// the event/eventqueue message boundary (that stays Phase 5).
func TestCommandQuery_expiresAtCarried(t *testing.T) {
	q := CommandQuery{Argv: []string{"x"}, Format: FormatJSONL, Meta: Meta{EmitTypes: []string{"typeA"}}}
	cmd := fakeCmd{out: []byte(`{"id":"a","type":"task","expiresAt":"2026-09-01T12:00:00Z"}` + "\n")}
	evts, err := q.Run(context.Background(), Env{Cmd: cmd})
	if err != nil {
		t.Fatal(err)
	}
	if len(evts) != 1 {
		t.Fatalf("want 1 event, got %d: %+v", len(evts), evts)
	}
	want, perr := time.Parse(time.RFC3339, "2026-09-01T12:00:00Z")
	if perr != nil {
		t.Fatal(perr)
	}
	got, ok := evts[0].Attributes["expiresAt"].(time.Time)
	if !ok || !got.Equal(want) {
		t.Fatalf("expiresAt not carried onto the event: attributes=%+v", evts[0].Attributes)
	}
}

// emit is an event-type selector: when present it MUST be among the source's
// declared Emits() and, when valid, picks which declared type THIS record's
// event carries (multi-emit).
func TestCommandQuery_emitSelectsSecondDeclaredType(t *testing.T) {
	q := CommandQuery{Argv: []string{"x"}, Format: FormatJSONL, Meta: Meta{EmitTypes: []string{"typeA", "typeB"}}}
	cmd := fakeCmd{out: []byte(`{"id":"a","type":"task","emit":"typeB"}` + "\n")}
	evts, err := q.Run(context.Background(), Env{Cmd: cmd})
	if err != nil {
		t.Fatal(err)
	}
	if len(evts) != 1 || evts[0].Type != "typeB" {
		t.Fatalf("emit must select the declared type typeB: %+v", evts)
	}
}

// An emit value NOT among the declared emits is a per-record error — the
// record is dropped, but OTHER records in the same batch still emit.
func TestCommandQuery_undeclaredEmitIsPerRecordError(t *testing.T) {
	q := CommandQuery{Argv: []string{"x"}, Format: FormatJSONL, Meta: Meta{EmitTypes: []string{"typeA", "typeB"}}}
	cmd := fakeCmd{out: []byte(
		`{"id":"a","type":"task","emit":"nope"}` + "\n" +
			`{"id":"b","type":"task"}` + "\n",
	)}
	evts, err := q.Run(context.Background(), Env{Cmd: cmd})
	if err == nil {
		t.Fatal("an undeclared emit must produce a per-record error")
	}
	if len(evts) != 1 || evts[0].Item.ID != "b" {
		t.Fatalf("the other record must still be emitted: %+v", evts)
	}
}

// Absent emit falls back to firstEmit(q); `type` keeps its pre-existing
// meaning (item classification in the payload) untouched.
func TestCommandQuery_typeSetNoEmitUsesFirstDeclaredEmitType(t *testing.T) {
	q := CommandQuery{Argv: []string{"x"}, Format: FormatJSONL, Meta: Meta{EmitTypes: []string{"typeA", "typeB"}}}
	cmd := fakeCmd{out: []byte(`{"id":"a","type":"bug"}` + "\n")}
	evts, err := q.Run(context.Background(), Env{Cmd: cmd})
	if err != nil {
		t.Fatal(err)
	}
	if len(evts) != 1 || evts[0].Type != "typeA" || evts[0].Item.Type != "bug" {
		t.Fatalf("want event type typeA (first declared emit) and payload.item.type unchanged (bug): %+v", evts)
	}
}

// OSCommander.Run must fold a failed child's own captured stderr into its
// returned error's message: the bare exit status never names the cause, so
// without this CommandQuery.Run's "command query %v: %w" wrapping (and the
// producer-tick WARN log built from it) would lose the reason a source's
// backing command wrote on its own stderr.
func TestOSCommanderRun_FoldsChildStderrIntoError(t *testing.T) {
	fake := cmdruntest.New(t)
	fake.On(cmdruntest.Any).Return(cmdruntest.Fail(3, "boom-reason\n"))
	c := OSCommander{Exec: fake.Runner()}
	_, err := c.Run(context.Background(), []string{"src", "x"})
	if err == nil {
		t.Fatal("non-zero exit must produce an error")
	}
	if !strings.Contains(err.Error(), "boom-reason") {
		t.Fatalf("error must fold in the child's own stderr text; err = %v", err)
	}
	var exitErr *ExitCodeError
	if !errors.As(err, &exitErr) || exitErr.Code != 3 {
		t.Fatalf("errors.As must resolve the *ExitCodeError (code 3); err = %v", err)
	}
}

// A child that exits non-zero with NO stderr output keeps the plain
// "exit status N" message.
func TestOSCommanderRun_EmptyStderrLeavesPlainExitError(t *testing.T) {
	fake := cmdruntest.New(t)
	fake.On(cmdruntest.Any).Return(cmdruntest.Fail(1, ""))
	_, err := OSCommander{Exec: fake.Runner()}.Run(context.Background(), []string{"src"})
	if err == nil || err.Error() != "exit status 1" {
		t.Fatalf("with no child stderr the error must be the plain exit-status message; err = %v", err)
	}
}

// The call OSCommander builds: own process group, SIGTERM grace on cancel, a
// backstop timeout, an exact environment only when one is given.
func TestOSCommanderRunEnv_CallShape(t *testing.T) {
	fake := cmdruntest.New(t)
	fake.On(cmdruntest.Any).Return(cmdruntest.Out("ok"))
	c := OSCommander{Exec: fake.Runner()}
	out, err := c.RunEnv(context.Background(), []string{"src", "a"}, []string{"K=v"})
	if err != nil || string(out) != "ok" {
		t.Fatalf("out=%q err=%v", out, err)
	}
	if _, err := c.RunEnv(context.Background(), []string{"src"}, nil); err != nil {
		t.Fatal(err)
	}
	calls := fake.Calls()
	g := calls[0]
	if g.Name != "src" || len(g.Args) != 1 || !g.OwnProcGroup || g.TermGrace != cancelGrace || g.Timeout != commandTimeout ||
		g.EnvMode != cmdrun.EnvExact || len(g.Env) != 1 {
		t.Fatalf("call = %+v", g)
	}
	if calls[1].EnvMode != cmdrun.EnvAppend || calls[1].Env != nil {
		t.Fatalf("nil env must inherit: %+v", calls[1])
	}
}

func TestOSCommanderRunEnv_Failures(t *testing.T) {
	for name, tc := range map[string]struct {
		res  cmdrun.Result
		want string
	}{
		"start":   {cmdrun.Result{Code: -1, StartErr: errors.New("no such file")}, "no such file"},
		"timeout": {cmdrun.Result{Code: -1, Signal: "killed", CtxErr: context.DeadlineExceeded, TimedOut: true}, "timed out"},
		"cancel":  {cmdrun.Result{Code: -1, Signal: "terminated", CtxErr: context.Canceled}, "context canceled"},
		"signal":  {cmdrun.Result{Code: -1, Signal: "killed"}, "signal: killed"},
	} {
		t.Run(name, func(t *testing.T) {
			fake := cmdruntest.New(t)
			fake.On(cmdruntest.Any).Return(tc.res)
			_, err := OSCommander{Exec: fake.Runner()}.Run(context.Background(), []string{"src"})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.want)
			}
		})
	}
	if _, err := (OSCommander{}).Run(context.Background(), nil); err == nil {
		t.Fatal("empty argv must be an error")
	}
}

// End-to-end through CommandQuery.Run's default-Commander fallback with an
// injected runner: a failing backing command's own stderr reason must reach
// the error CommandQuery.Run returns (what the producer-tick WARN log shows).
func TestCommandQuery_FailingCommandFoldsStderrReasonIntoError(t *testing.T) {
	fake := cmdruntest.New(t)
	fake.On(cmdruntest.Any).Return(cmdruntest.Fail(3, "rate-limited-reason"))
	q := CommandQuery{Argv: []string{"src"}, Format: FormatJSONL}
	_, err := q.Run(context.Background(), Env{Cmd: OSCommander{Exec: fake.Runner()}})
	if err == nil || !strings.Contains(err.Error(), "rate-limited-reason") {
		t.Fatalf("error must carry the backing command's own stderr reason; err = %v", err)
	}
}

// ArgvOf names what a source actually runs, for the producer-tick failure log
// (bead pg2-zdowv); a query with no command yields nil and the result is a copy.
func TestArgvOf(t *testing.T) {
	cq := CommandQuery{Argv: []string{"a", "b"}}
	got := ArgvOf(cq)
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("ArgvOf(CommandQuery) = %v", got)
	}
	got[0] = "mutated"
	if cq.Argv[0] != "a" {
		t.Fatal("ArgvOf must return a copy, not the query's own slice")
	}
	if got := ArgvOf(ParticipantQuery{Command: []string{"p", "q"}}); len(got) != 2 || got[0] != "p" {
		t.Fatalf("ArgvOf(ParticipantQuery) = %v", got)
	}
	if got := ArgvOf(&CommandQuery{Argv: []string{"x"}}); len(got) != 1 {
		t.Fatalf("ArgvOf(*CommandQuery) = %v", got)
	}
	if got := ArgvOf(CommandQuery{}); got != nil {
		t.Fatalf("ArgvOf(empty) = %v, want nil", got)
	}
}
