package query

import (
	"context"
	"errors"
	"testing"
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
	items, err := q.Run(context.Background(), Env{Cmd: cmd})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].ID != "a" || items[0].Metadata["k"] != "v" || items[1].ID != "b" {
		t.Fatalf("items wrong: %+v", items)
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
