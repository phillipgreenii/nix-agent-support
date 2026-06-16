package beads

import (
	"context"
	"testing"
)

func TestShowObj_object(t *testing.T) {
	fr := &fakeRunner{out: `{"id":"zr-1","status":"open","parent":"zr-pr","metadata":{"author":"phillipg"}}`}
	iss, err := ShowObj(context.Background(), fr, "zr-1")
	if err != nil {
		t.Fatal(err)
	}
	if iss.ID != "zr-1" || iss.Status != "open" || iss.Parent != "zr-pr" {
		t.Errorf("got %+v", iss)
	}
	if iss.Metadata["author"] != "phillipg" {
		t.Errorf("author = %v", iss.Metadata["author"])
	}
}

func TestShowObj_array(t *testing.T) {
	fr := &fakeRunner{out: `[{"id":"zr-2","status":"closed"}]`}
	iss, err := ShowObj(context.Background(), fr, "zr-2")
	if err != nil {
		t.Fatal(err)
	}
	if iss.ID != "zr-2" || iss.Status != "closed" {
		t.Errorf("got %+v", iss)
	}
}

func TestStatus(t *testing.T) {
	fr := &fakeRunner{out: `{"id":"zr-1","status":"in_progress"}`}
	s, err := Status(context.Background(), fr, "zr-1")
	if err != nil || s != "in_progress" {
		t.Fatalf("status=%q err=%v", s, err)
	}
}

func TestReady_emptyAndArray(t *testing.T) {
	fr := &fakeRunner{out: `[{"id":"zr-1","issue_type":"task","title":"process-feedback: x"}]`}
	got, err := Ready(context.Background(), fr, "--label", "worker-ready")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "zr-1" {
		t.Fatalf("got %+v", got)
	}
	// argv assertion: Ready appends --json --limit 0
	last := fr.args[len(fr.args)-1]
	wantTail := []string{"ready", "--label", "worker-ready", "--json", "--limit", "0"}
	if joinArgs(last) != joinArgs(wantTail) {
		t.Errorf("argv = %v, want %v", last, wantTail)
	}
}

func TestReady_handlesNonArray(t *testing.T) {
	fr := &fakeRunner{out: `null`}
	got, err := Ready(context.Background(), fr)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("null should yield empty slice, got %v", got)
	}
}

func TestUnclaim_argv(t *testing.T) {
	fr := &fakeRunner{}
	if err := Unclaim(context.Background(), fr, "zr-1"); err != nil {
		t.Fatal(err)
	}
	want := []string{"update", "zr-1", "--status=open", "--assignee="}
	if joinArgs(fr.args[0]) != joinArgs(want) {
		t.Errorf("argv = %v, want %v", fr.args[0], want)
	}
}

func TestAddHuman_argv(t *testing.T) {
	fr := &fakeRunner{}
	if err := AddHuman(context.Background(), fr, "zr-1"); err != nil {
		t.Fatal(err)
	}
	want := []string{"update", "zr-1", "--add-label", "human"}
	if joinArgs(fr.args[0]) != joinArgs(want) {
		t.Errorf("argv = %v, want %v", fr.args[0], want)
	}
}

func TestShowObj_parsesLabels(t *testing.T) {
	fr := &fakeRunner{out: `{"id":"zr-1","status":"open","labels":["worker-ready","pool-launch-fail"]}`}
	iss, err := ShowObj(context.Background(), fr, "zr-1")
	if err != nil {
		t.Fatal(err)
	}
	if !iss.HasLabel("pool-launch-fail") {
		t.Errorf("HasLabel(pool-launch-fail) = false; labels=%v", iss.Labels)
	}
	if iss.HasLabel("human") {
		t.Errorf("HasLabel(human) should be false; labels=%v", iss.Labels)
	}
}

func TestAddLabel_argv(t *testing.T) {
	fr := &fakeRunner{}
	if err := AddLabel(context.Background(), fr, "zr-1", "pool-launch-fail"); err != nil {
		t.Fatal(err)
	}
	want := []string{"update", "zr-1", "--add-label", "pool-launch-fail"}
	if joinArgs(fr.args[0]) != joinArgs(want) {
		t.Errorf("argv = %v, want %v", fr.args[0], want)
	}
}

func TestHasLabel_fromShow(t *testing.T) {
	fr := &fakeRunner{out: `{"id":"zr-1","labels":["pool-launch-fail"]}`}
	got, err := HasLabel(context.Background(), fr, "zr-1", "pool-launch-fail")
	if err != nil || !got {
		t.Fatalf("HasLabel = %v, err=%v; want true,nil", got, err)
	}
	last := fr.args[len(fr.args)-1]
	want := []string{"show", "zr-1", "--json"}
	if joinArgs(last) != joinArgs(want) {
		t.Errorf("HasLabel must read via show --json; argv=%v", last)
	}
}

func joinArgs(a []string) string {
	s := ""
	for _, x := range a {
		s += "\x00" + x
	}
	return s
}

func TestComment_argv(t *testing.T) {
	fr := &fakeRunner{}
	if err := Comment(context.Background(), fr, "zr-1", "interrupted — budget"); err != nil {
		t.Fatal(err)
	}
	want := []string{"comment", "zr-1", "interrupted — budget"}
	if joinArgs(fr.args[0]) != joinArgs(want) {
		t.Errorf("argv = %v, want %v", fr.args[0], want)
	}
}
