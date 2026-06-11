package main

import (
	"testing"

	"github.com/phillipgreenii/ccpool/internal/store"
)

func TestAttendCandidates(t *testing.T) {
	rows := []store.Session{
		{Name: "wait1", State: store.NeedsInput, TmuxSession: "cc-wait1"},
		{Name: "wait2", State: store.NeedsInput, TmuxSession: "cc-wait2"}, // dead
		{Name: "busy", State: store.Working, TmuxSession: "cc-busy"},
		{Name: "done1", State: store.Done, TmuxSession: "cc-done1"},
	}
	live := map[string]bool{"cc-wait1": true, "cc-wait2": false, "cc-busy": true, "cc-done1": true}
	liveFn := func(_ , target string) bool { return live[target] }

	// needs_input only, dead one filtered out
	got := attendCandidates(rows, false, liveFn, "ccpool")
	if len(got) != 1 || got[0].Name != "wait1" {
		t.Fatalf("default: got %v, want [wait1]", names(got))
	}
	// --include-done adds live done rows
	got = attendCandidates(rows, true, liveFn, "ccpool")
	if len(got) != 2 || got[0].Name != "wait1" || got[1].Name != "done1" {
		t.Fatalf("include-done: got %v, want [wait1 done1]", names(got))
	}
}

func names(rows []store.Session) []string {
	var n []string
	for _, r := range rows {
		n = append(n, r.Name)
	}
	return n
}
