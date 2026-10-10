package dependency

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// putIssue stores an issue row. deps are the DIRECT issue_show.deps edges
// ({id, type}); issueDeps is the raw issue_deps JSON ("" omits the key, as a
// hydration with the switch off does). active false stores the row inactive.
func putIssue(t *testing.T, st *store.Store, id, state string, active bool, issueDeps string, deps ...[2]string) {
	t.Helper()
	edges := ""
	for i, d := range deps {
		if i > 0 {
			edges += ","
		}
		edges += fmt.Sprintf(`{"id":%q,"type":%q}`, d[0], d[1])
	}
	facts := fmt.Sprintf(`{"issue_show":{"id":%q,"state":%q,"deps":[%s]}`, id, state, edges)
	if issueDeps != "" {
		facts += `,"issue_deps":` + issueDeps
	}
	facts += "}"
	e := store.Entity{Repo: testRepo, EntityType: "issue", EntityID: id, Facts: facts, AsOf: "2026-10-01T00:00:00Z"}
	if _, err := st.WriteEntityStateWithLog(e, 0, "2026-10-01T00:00:00Z", active, nil, "test", "2026-10-01T00:00:00Z"); err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}
}

func terminalByState(e store.Entity) bool {
	var f struct {
		IssueShow struct {
			State string `json:"state"`
		} `json:"issue_show"`
	}
	_ = json.Unmarshal([]byte(e.Facts), &f)
	return f.IssueShow.State == "closed"
}

func mustIndex(t *testing.T, st *store.Store, term func(store.Entity) bool) *IssueDependents {
	t.Helper()
	x, err := NewIssueDependents(st, term)
	if err != nil {
		t.Fatalf("NewIssueDependents: %v", err)
	}
	return x
}

// (a) A blocked-by edge stored on B naming A makes B a dependent of A.
func TestIssueDependents_BlockedByEdgeMakesDependent(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	putIssue(t, st, "bd-a", "open", true, "")
	putIssue(t, st, "bd-b", "open", true, "", [2]string{"bd-a", "blocks"})
	putIssue(t, st, "bd-c", "open", true, "", [2]string{"bd-a", "blocks"})
	x := mustIndex(t, st, terminalByState)
	if got, want := x.DependentsOf("bd-a"), []string{"bd-b", "bd-c"}; !reflect.DeepEqual(got, want) {
		t.Errorf("DependentsOf(bd-a) = %v, want %v (sorted)", got, want)
	}
	if got := x.DependentsOf("bd-b"); got != nil {
		t.Errorf("DependentsOf(bd-b) = %v, want none: the edge points at bd-a only", got)
	}
}

// (b) A terminal or inactive dependent is excluded.
func TestIssueDependents_ExcludesTerminalAndInactive(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	putIssue(t, st, "bd-a", "open", true, "")
	putIssue(t, st, "bd-open", "open", true, "", [2]string{"bd-a", "blocks"})
	putIssue(t, st, "bd-closed", "closed", true, "", [2]string{"bd-a", "blocks"})
	putIssue(t, st, "bd-inactive", "open", false, "", [2]string{"bd-a", "blocks"})
	x := mustIndex(t, st, terminalByState)
	if got, want := x.DependentsOf("bd-a"), []string{"bd-open"}; !reflect.DeepEqual(got, want) {
		t.Errorf("DependentsOf(bd-a) = %v, want %v", got, want)
	}
	// The predicate is the caller's: with none, a closed dependent counts.
	x = mustIndex(t, st, nil)
	if got, want := x.DependentsOf("bd-a"), []string{"bd-closed", "bd-open"}; !reflect.DeepEqual(got, want) {
		t.Errorf("DependentsOf(bd-a) with nil predicate = %v, want %v (inactive still excluded)", got, want)
	}
}

// (c) Transitive blockers do not count: A blocked by B, B blocked by C.
func TestIssueDependents_TransitiveChainIsOneHop(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	putIssue(t, st, "bd-c", "open", true, `{"ids":[],"entities":[]}`)
	putIssue(t, st, "bd-b", "open", true, `{"ids":["bd-c"],"entities":[]}`, [2]string{"bd-c", "blocks"})
	// A's recursive blocked-by set names both B and C; only B is a direct edge.
	putIssue(t, st, "bd-a", "open", true, `{"ids":["bd-b","bd-c"],"entities":[]}`, [2]string{"bd-b", "blocks"})
	x := mustIndex(t, st, terminalByState)
	if got, want := x.DependentsOf("bd-c"), []string{"bd-b"}; !reflect.DeepEqual(got, want) {
		t.Errorf("DependentsOf(bd-c) = %v, want %v only", got, want)
	}
	if got, want := x.DependentsOf("bd-b"), []string{"bd-a"}; !reflect.DeepEqual(got, want) {
		t.Errorf("DependentsOf(bd-b) = %v, want %v", got, want)
	}
	if got := x.DependentsOf("bd-a"); got != nil {
		t.Errorf("DependentsOf(bd-a) = %v, want none", got)
	}
}

// Only edges of type blocks count; self edges and an empty id are ignored.
func TestIssueDependents_OnlyBlocksEdges(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	putIssue(t, st, "bd-a", "open", true, "")
	putIssue(t, st, "bd-child", "open", true, "", [2]string{"bd-a", "parent-child"}, [2]string{"", "blocks"}, [2]string{"bd-child", "blocks"})
	putIssue(t, st, "bd-b", "open", true, "", [2]string{"bd-a", "Blocks"}, [2]string{"bd-a", "blocks"})
	x := mustIndex(t, st, nil)
	if got, want := x.DependentsOf("bd-a"), []string{"bd-b"}; !reflect.DeepEqual(got, want) {
		t.Errorf("DependentsOf(bd-a) = %v, want %v (deduplicated, blocks only)", got, want)
	}
	if got := x.DependentsOf("bd-child"); got != nil {
		t.Errorf("DependentsOf(bd-child) = %v, want none (self edge ignored)", got)
	}
}

// A blocker with no stored row of its own still has its dependents.
func TestIssueDependents_BlockerWithoutRow(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	putIssue(t, st, "bd-b", "open", true, "", [2]string{"bd-gone", "blocks"})
	x := mustIndex(t, st, nil)
	if got, want := x.DependentsOf("bd-gone"), []string{"bd-b"}; !reflect.DeepEqual(got, want) {
		t.Errorf("DependentsOf(bd-gone) = %v, want %v", got, want)
	}
}

// (d) An issue without stored issue_deps is unavailable, not zero.
func TestIssueDependents_Available(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	putIssue(t, st, "bd-on", "open", true, `{"ids":[],"entities":[]}`)
	putIssue(t, st, "bd-off", "open", true, "")
	putIssue(t, st, "bd-null", "open", true, "null")
	x := mustIndex(t, st, nil)
	for id, want := range map[string]bool{"bd-on": true, "bd-off": false, "bd-null": false, "bd-unknown": false} {
		if got := x.Available(id); got != want {
			t.Errorf("Available(%s) = %v, want %v", id, got, want)
		}
	}
}

// Only issue rows are indexed: a PR row with a deps-shaped payload is ignored.
func TestIssueDependents_IgnoresOtherEntityTypes(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	putIssue(t, st, "bd-a", "open", true, "")
	err := st.UpsertEntity(store.Entity{
		Repo: testRepo, EntityType: "pr", EntityID: prID(1),
		Facts: `{"issue_show":{"deps":[{"id":"bd-a","type":"blocks"}]},"issue_deps":{}}`, AsOf: "2026-10-01T00:00:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	x := mustIndex(t, st, nil)
	if got := x.DependentsOf("bd-a"); got != nil {
		t.Errorf("DependentsOf(bd-a) = %v, want none", got)
	}
	if x.Available(prID(1)) {
		t.Error("a PR row must not be Available as an issue")
	}
}

// The returned slice is a copy: mutating it cannot corrupt the index.
func TestIssueDependents_ReturnsCopy(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	putIssue(t, st, "bd-b", "open", true, "", [2]string{"bd-a", "blocks"})
	x := mustIndex(t, st, nil)
	x.DependentsOf("bd-a")[0] = "mutated"
	if got := x.DependentsOf("bd-a"); !reflect.DeepEqual(got, []string{"bd-b"}) {
		t.Errorf("DependentsOf(bd-a) = %v after caller mutation", got)
	}
}

// The unmigrated store records no activity, so the index refuses it.
func TestIssueDependents_RefusesOldSchema(t *testing.T) {
	st := store.OpenForTest(t)
	if _, err := NewIssueDependents(st, nil); !errors.Is(err, store.ErrOldSchema) {
		t.Errorf("err = %v, want store.ErrOldSchema", err)
	}
}
