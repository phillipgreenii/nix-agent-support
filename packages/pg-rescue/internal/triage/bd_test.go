package triage

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func fakeRun(reply string, code int, err error, got *[][]string) Runner {
	return func(_ context.Context, argv []string) ([]byte, int, error) {
		*got = append(*got, argv)
		return []byte(reply), code, err
	}
}

func TestBDArgv(t *testing.T) {
	var got [][]string
	b := BD{Run: fakeRun("", 0, nil, &got)}
	_ = b.Comment("i", "t")
	_ = b.Close("i", "r")
	_ = b.Release("i")
	_ = b.HandOver("i")
	_ = b.Claim("i", "act")
	want := [][]string{
		{"bd", "comments", "add", "i", "--actor", "pg-rescue-triage", "t"},
		{"bd", "close", "i", "--reason", "r"},
		{"bd", "update", "i", "--status", "open", "--assignee", ""},
		{"bd", "update", "i", "--status", "open", "--assignee", "", "--add-label", "human"},
		{"bd", "update", "i", "--claim", "--actor", "act"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %q", got)
	}
}

func TestBDShowAndCommentsDecodeEnvelope(t *testing.T) {
	var got [][]string
	b := BD{Run: fakeRun(`{"data":[{"id":"i","status":"open","labels":["pg-rescue"],"metadata":{"pg_rescue_cwd":"/c"}}]}`, 0, nil, &got)}
	it, err := b.Show("i")
	if err != nil || it.Status != "open" || it.Metadata[MetaCwd] != "/c" || !hasLabel(it.Labels, RescueLabel) {
		t.Fatalf("%+v %v", it, err)
	}
	b = BD{Run: fakeRun(`{"data":[{"text":"a"},{"text":"b"}]}`, 0, nil, &got)}
	cs, err := b.Comments("i")
	if err != nil || !reflect.DeepEqual(cs, []string{"a", "b"}) {
		t.Fatalf("%v %v", cs, err)
	}
	if _, err := (BD{Run: fakeRun(`{"data":[]}`, 0, nil, &got)}).Show("i"); err == nil {
		t.Fatal("want error on empty")
	}
}

func TestBDClaimRefusedIsPeerHeld(t *testing.T) {
	var got [][]string
	err := BD{Run: fakeRun("", 1, errors.New("held"), &got)}.Claim("i", "a")
	if !errors.Is(err, ErrPeerHeld) || !strings.Contains(err.Error(), "held") {
		t.Fatal(err)
	}
	// a runner failure with no exit code is a real error, not a peer hold
	err = BD{Run: fakeRun("", 0, errors.New("no bd"), &got)}.Claim("i", "a")
	if errors.Is(err, ErrPeerHeld) || err == nil {
		t.Fatal(err)
	}
}
