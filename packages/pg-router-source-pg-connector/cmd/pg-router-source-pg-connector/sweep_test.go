package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestSweep_UnionsPresentIdsAcrossQueries_ReadingPresentIdsNeverEntities(t *testing.T) {
	// The wire double's --ids-only response carries an EMPTY entities
	// array and a populated present_ids array (the REAL landed shape) —
	// a double built the opposite way (entities populated, present_ids
	// empty) would catch the OPPOSITE bug and is deliberately not what
	// this test uses [design: section 6.1].
	withFactory(t, "sweep_ids_by_query")

	stdout, stderr, code := runCLI(t, "sweep", "issue", "q1", "q2")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr=%q)", code, stderr)
	}

	items := mustUnmarshalItems(t, stdout)
	if len(items) != 3 {
		t.Fatalf("len(items) = %d, want 3 (union of q1={a,b}, q2={b,c}, deduped) (items=%+v)", len(items), items)
	}
	gotIDs := make([]string, len(items))
	for i, it := range items {
		gotIDs[i] = it.ID
	}
	want := []string{"a", "b", "c"}
	for i, w := range want {
		if gotIDs[i] != w {
			t.Fatalf("items ids = %v, want %v (in this order, first-seen)", gotIDs, want)
		}
	}

	for _, it := range items {
		if it.Type != "issue" {
			t.Fatalf("item %+v: type = %q, want issue", it, it.Type)
		}
		if it.Title != it.ID {
			t.Fatalf("item %+v: title = %q, want equal to id (present_ids carries no title)", it, it.Title)
		}
		if len(it.Metadata) != 1 || it.Metadata["change"] != "sweep" {
			t.Fatalf("item %+v: metadata = %v, want exactly {\"change\":\"sweep\"}", it, it.Metadata)
		}
	}
}

func TestSweep_SinceFlag_IsPassedThroughToEveryListCall(t *testing.T) {
	// --since narrows the swept set to entities updated in the window: the
	// double returns only "recent" for --since 40m, and the output shape
	// (title = id, metadata exactly {"change":"sweep"}) is unchanged.
	withFactory(t, "sweep_since_echo")

	stdout, stderr, code := runCLI(t, "sweep", "pr", "--since", "40m", "mine", "team")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr=%q)", code, stderr)
	}
	items := mustUnmarshalItems(t, stdout)
	if len(items) != 1 || items[0].ID != "recent" {
		t.Fatalf("items = %+v, want exactly the one id \"recent\" (the --since 40m result, deduped across both queries)", items)
	}
	if items[0].Title != "recent" || len(items[0].Metadata) != 1 || items[0].Metadata["change"] != "sweep" {
		t.Fatalf("item %+v: title/metadata shape changed", items[0])
	}
}

func TestSweep_NoSinceFlag_ListsTheWholeSetAsBefore(t *testing.T) {
	withFactory(t, "sweep_since_echo")

	stdout, stderr, code := runCLI(t, "sweep", "pr", "mine")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr=%q)", code, stderr)
	}
	items := mustUnmarshalItems(t, stdout)
	if len(items) != 2 || items[0].ID != "recent" || items[1].ID != "old" {
		t.Fatalf("items = %+v, want [recent old] (no --since means the unfiltered set)", items)
	}
}

func TestSweep_ZeroQueryNames_IsUsageErrorWithNoSubprocessConsulted(t *testing.T) {
	// No withFactory: if this RunE ever tried to exec pg-connector for
	// this case, the (unset) execCmdFactory default would try to run a
	// real "pg-connector" binary — this test's whole point is that it
	// must never get that far [design: section 6.1, "there is no
	// pg-connector subprocess to consult in this specific case"].
	stdout, stderr, code := runCLI(t, "sweep", "issue")

	if code == 0 {
		t.Fatalf("exit code = 0, want non-zero")
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want empty", stdout)
	}
	if stderr == "" {
		t.Fatalf("stderr = %q, want a usage diagnostic", stderr)
	}
}

func TestSweep_SingleQuery_PropagatesTotalFailure(t *testing.T) {
	withFactory(t, "total_failure")
	stdout, stderr, code := runCLI(t, "sweep", "issue", "q1")

	if code == 0 {
		t.Fatalf("exit code = 0, want non-zero")
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want empty", stdout)
	}
	if stderr == "" {
		t.Fatalf("stderr empty, want pg-connector's own stderr copied through")
	}
}

// bead pg2-otfq2: --backend is forwarded to every list call only when given.
func TestSweep_BackendFlag_IsPassedThroughToEveryListCallOnlyWhenGiven(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"omitted", []string{"sweep", "issue", "q1"}, ""},
		{"given", []string{"sweep", "issue", "--backend", "pg-connector-issue-beads-pg2", "q1"}, "pg-connector-issue-beads-pg2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recordFile := filepath.Join(t.TempDir(), "recorded-args")
			withFactory(t, "sweep_ids_by_query", "GO_HELPER_ARGS_RECORD_FILE="+recordFile)
			if _, stderr, code := runCLI(t, tc.args...); code != 0 {
				t.Fatalf("exit code = %d, want 0 (stderr=%q)", code, stderr)
			}
			got := strings.Fields(readFile(t, recordFile))
			if v := flagValue(got, "--backend"); v != tc.want {
				t.Fatalf("child argv %v: --backend = %q, want %q", got, v, tc.want)
			}
		})
	}
}
