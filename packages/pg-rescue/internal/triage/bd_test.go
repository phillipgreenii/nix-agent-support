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
	b := BD{Run: fakeRun("", 0, nil, &got)}.WithActor("act")
	bd := b.(BD)
	_ = bd.Comment("i", "t")
	_ = bd.Close("i", "r")
	_ = bd.Release("i")
	_ = bd.HandOver("i")
	_ = bd.Claim("i", "act")
	want := [][]string{
		{"bd", "comments", "add", "i", "--actor", "act", "t"},
		{"bd", "close", "i", "--reason", "r", "--actor", "act"},
		{"bd", "update", "i", "--status", "open", "--assignee", "", "--actor", "act"},
		{"bd", "update", "i", "--status", "open", "--assignee", "", "--add-label", "human", "--actor", "act"},
		{"bd", "update", "i", "--claim", "--actor", "act"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %q", got)
	}
}

// With no actor resolved (BEADS_ACTOR unset) every write still carries one
// and the same one.
func TestBDWritesShareDefaultActorWhenUnset(t *testing.T) {
	t.Setenv("BEADS_ACTOR", "")
	var got [][]string
	bd := BD{Run: fakeRun("", 0, nil, &got)}
	_ = bd.Claim("i", DefaultActor)
	_ = bd.Comment("i", "t")
	_ = bd.Close("i", "r")
	_ = bd.Release("i")
	_ = bd.HandOver("i")
	if len(got) != 5 {
		t.Fatalf("calls = %q", got)
	}
	for _, argv := range got {
		i := indexOf(argv, "--actor")
		if i < 0 || i+1 >= len(argv) || argv[i+1] != DefaultActor {
			t.Fatalf("write without the shared actor: %q", argv)
		}
	}
}

func indexOf(s []string, v string) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return -1
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

// TestRunStripsChangeSuffixBeforeTracker pins the event id shape pg-router
// hands the role ("<bead id>@<change id>"): bd knows only the bare bead id.
func TestRunStripsChangeSuffixBeforeTracker(t *testing.T) {
	var argvs [][]string
	run := func(_ context.Context, argv []string) ([]byte, int, error) {
		argvs = append(argvs, argv)
		return []byte(`[{"id":"pg2-s5mjbx","status":"closed"}]`), 0, nil
	}
	id := "pg2-s5mjbx@c764162a3b51c764162a3b51c764162a3b51abcd"
	if _, err := Run(BD{Run: run}, Options{ItemID: id, Actor: "a"}); err != nil {
		t.Fatal(err)
	}
	want := []string{"bd", "show", "pg2-s5mjbx", "--json"}
	if len(argvs) != 1 || !reflect.DeepEqual(argvs[0], want) {
		t.Fatalf("argv = %v, want [%v]", argvs, want)
	}
	if BeadID("x-1") != "x-1" {
		t.Fatal("bare id must pass through")
	}
}
