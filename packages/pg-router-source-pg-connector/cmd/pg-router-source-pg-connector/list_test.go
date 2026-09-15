package main

import "testing"

func TestList_NoFilters_PrintsEveryEntity(t *testing.T) {
	withFactory(t, "list_full_ok")
	stdout, stderr, code := runCLI(t, "list", "issue", "feedback-ready", "--backend", "pg-connector-issue-beads")

	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr=%q)", code, stderr)
	}
	items := mustUnmarshalItems(t, stdout)
	if len(items) != 3 {
		t.Fatalf("len(items) = %d, want 3 (no filtering) (items=%+v)", len(items), items)
	}
	// Reads the entity's own metadata map as-is, and its own issue_type
	// as the rawItem's "type" (never the CLI <type> positional).
	got := items[0]
	if got.ID != "i1" || got.Type != "task" || got.Title != "Alpha task one" {
		t.Fatalf("items[0] = %+v", got)
	}
	if got.Metadata["k"] != "v1" {
		t.Fatalf("items[0].metadata = %v, want copied as-is", got.Metadata)
	}
}

func TestList_TitlePrefixFilter_CaseSensitiveExactPrefix(t *testing.T) {
	withFactory(t, "list_full_ok")
	stdout, stderr, code := runCLI(t, "list", "issue", "feedback-ready", "--backend", "b", "--title-prefix", "Alpha")

	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr=%q)", code, stderr)
	}
	items := mustUnmarshalItems(t, stdout)
	wantIDs := map[string]bool{"i1": true, "i3": true}
	if len(items) != len(wantIDs) {
		t.Fatalf("len(items) = %d, want %d (items=%+v)", len(items), len(wantIDs), items)
	}
	for _, it := range items {
		if !wantIDs[it.ID] {
			t.Fatalf("unexpected item %+v matched --title-prefix Alpha", it)
		}
	}
}

func TestList_IssueTypeFilter_ExactEquality(t *testing.T) {
	withFactory(t, "list_full_ok")
	stdout, stderr, code := runCLI(t, "list", "issue", "feedback-ready", "--backend", "b", "--issue-type", "task")

	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr=%q)", code, stderr)
	}
	items := mustUnmarshalItems(t, stdout)
	wantIDs := map[string]bool{"i1": true, "i2": true}
	if len(items) != len(wantIDs) {
		t.Fatalf("len(items) = %d, want %d (items=%+v)", len(items), len(wantIDs), items)
	}
	for _, it := range items {
		if !wantIDs[it.ID] {
			t.Fatalf("unexpected item %+v matched --issue-type task", it)
		}
	}
}

func TestList_BothFilters_AreANDed(t *testing.T) {
	withFactory(t, "list_full_ok")
	stdout, stderr, code := runCLI(t, "list", "issue", "feedback-ready", "--backend", "b", "--title-prefix", "Alpha", "--issue-type", "task")

	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr=%q)", code, stderr)
	}
	items := mustUnmarshalItems(t, stdout)
	if len(items) != 1 || items[0].ID != "i1" {
		t.Fatalf("items = %+v, want exactly [i1] (Alpha-prefixed AND issue_type=task)", items)
	}
}

func TestList_EmptyEntityTitle_FallsBackToID(t *testing.T) {
	withFactory(t, "list_full_title_fallback")
	stdout, stderr, code := runCLI(t, "list", "issue", "q", "--backend", "b")

	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr=%q)", code, stderr)
	}
	items := mustUnmarshalItems(t, stdout)
	if len(items) != 1 || items[0].Title != "i9" {
		t.Fatalf("items = %+v, want title fallback to id i9", items)
	}
}

func TestList_BackendNotRegistered_ErrorsRatherThanNoOp(t *testing.T) {
	withFactory(t, "backend_not_registered")
	stdout, stderr, code := runCLI(t, "list", "issue", "q", "--backend", "not-a-backend")

	if code == 0 {
		t.Fatalf("exit code = 0, want non-zero: --backend must be a real, required pass-through, not an ignored flag")
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want empty", stdout)
	}
	if stderr == "" {
		t.Fatalf("stderr empty, want pg-connector's own \"not registered\" diagnostic copied through")
	}
}
