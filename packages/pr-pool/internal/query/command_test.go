package query

import (
	"context"
	"errors"
	"testing"
	"time"
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
