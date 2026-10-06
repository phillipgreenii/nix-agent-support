package attention

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/interpret"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// teamPR seeds a team PR that raises pr.review-requested (medium).
func teamPR(t *testing.T, st *store.Store, n int) {
	t.Helper()
	put(t, st, prSpec{number: n, ownership: "team", panel: interpret.PanelTeamAwaitingMe})
}

// failingOwnPR seeds an own PR that raises pr.own-ci-failing (high).
func failingOwnPR(t *testing.T, st *store.Store, n int) {
	t.Helper()
	put(t, st, prSpec{number: n, ownership: "mine", panel: interpret.PanelMineAwaitingTeam, ci: "failure"})
}

// link seeds one derived edge on a migrated store.
func link(t *testing.T, st *store.Store, fromType, fromID, toType, toID, relation string) {
	t.Helper()
	// ReplaceDerivedXrefs replaces ALL derived links leaving the entity, so
	// keep the ones already seeded.
	existing, err := st.ListXrefLinksFrom(testRepo, fromType, fromID)
	if err != nil {
		t.Fatal(err)
	}
	var keep []store.XrefLink
	for _, l := range existing {
		if strings.HasPrefix(l.Origin, "derived:") && l.Origin != "derived:legacy" {
			keep = append(keep, l)
		}
	}
	keep = append(keep, store.XrefLink{
		Repo: testRepo, FromType: fromType, FromID: fromID, ToType: toType, ToID: toID,
		Relation: relation, Origin: "derived:test", Evidence: "test",
		FirstSeen: "2026-10-01T00:00:00Z", LastConfirmed: "2026-10-01T00:00:00Z",
	})
	if err := st.ReplaceDerivedXrefs(testRepo, fromType, fromID, keep); err != nil {
		t.Fatal(err)
	}
}

// fakeStacks is a StackSource over a fixed PR-to-root map.
type fakeStacks map[string]string

func (f fakeStacks) StackRoot(_, id string) (string, bool, error) {
	r, ok := f[id]
	return r, ok, nil
}

type failingStacks struct{ err error }

func (f failingStacks) StackRoot(string, string) (string, bool, error) { return "", false, f.err }

func evaluateWith(t *testing.T, r Reader, stacks StackSource) Result {
	t.Helper()
	res, err := Evaluate(Inputs{Store: r, Repo: testRepo, Config: baseConfig(), Clock: testClock(), Stacks: stacks})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	return res
}

func groupKeys(res Result) []string {
	var out []string
	for _, g := range res.Groups {
		out = append(out, g.Key)
	}
	return out
}

func itemIDs(items []Item) string {
	var out []string
	for _, it := range items {
		out = append(out, it.ID[strings.Index(it.ID, "#"):])
	}
	return strings.Join(out, ",")
}

func TestJiraIssueIsTheGroupAndTheSmallestKeyWins(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	teamPR(t, st, 1)
	teamPR(t, st, 2)
	teamPR(t, st, 3)
	link(t, st, "pr", pid(1), "issue", "ABC-9", "jira")
	link(t, st, "pr", pid(2), "issue", "ABC-9", "jira")
	// PR 3 references two issues: the lexicographically smallest is its group.
	link(t, st, "pr", pid(3), "issue", "ABC-9", "jira")
	link(t, st, "pr", pid(3), "issue", "ABC-10", "jira")

	res := evaluate(t, st, baseConfig())

	if got := groupKeys(res); len(got) != 2 {
		t.Fatalf("groups = %v, want ABC-10 and ABC-9", got)
	}
	// Two groups of equal severity: the larger group (ABC-9, 2 items) first.
	if res.Groups[0].Key != "issue:ABC-9" || len(res.Groups[0].Items) != 2 {
		t.Errorf("first group = %s with %d items, want issue:ABC-9 with 2", res.Groups[0].Key, len(res.Groups[0].Items))
	}
	if res.Groups[1].Key != "issue:ABC-10" || itemIDs(res.Groups[1].Items) != "#3" {
		t.Errorf("second group = %s %s, want issue:ABC-10 holding #3 (smallest key by string compare)", res.Groups[1].Key, itemIDs(res.Groups[1].Items))
	}
	if res.Groups[0].Label != "ABC-9" {
		t.Errorf("label = %q, want the issue key", res.Groups[0].Label)
	}
	for _, it := range res.Items {
		if it.Group == "" || res.Traces[Ref(it.Type, it.ID)].Group != it.Group {
			t.Errorf("item %s group %q must match its trace %q", it.ID, it.Group, res.Traces[Ref(it.Type, it.ID)].Group)
		}
	}
}

func TestGroupKeyIsALinksRef(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	teamPR(t, st, 1)
	link(t, st, "pr", pid(1), "issue", "ABC-1", "jira")
	res := evaluate(t, st, baseConfig())
	if res.Groups[0].Key != "issue:ABC-1" {
		t.Fatalf("key = %q, want a <type>:<id> ref", res.Groups[0].Key)
	}
}

func TestGroupLevelPrecedence(t *testing.T) {
	// PR 1: jira + stack + work -> jira. PR 2: stack + work -> stack.
	// PR 3: work only -> work item. PR 4: nothing -> singleton.
	st := store.OpenNewSchemaForTest(t)
	for n := 1; n <= 4; n++ {
		teamPR(t, st, n)
	}
	link(t, st, "pr", pid(1), "issue", "ABC-1", "jira")
	link(t, st, "issue", "bd-1", "pr", pid(1), "work")
	link(t, st, "issue", "bd-1", "pr", pid(2), "work")
	link(t, st, "issue", "bd-1", "pr", pid(3), "work")
	stacks := fakeStacks{pid(1): pid(9), pid(2): pid(9)}

	res := evaluateWith(t, st, stacks)

	want := map[int]string{
		1: "issue:ABC-1",
		2: "pr:" + pid(9),
		3: "issue:bd-1",
		4: "pr:" + pid(4),
	}
	for n, key := range want {
		if it := mustItem(t, res, n); it.Group != key {
			t.Errorf("PR %d group = %q, want %q", n, it.Group, key)
		}
	}
}

func TestStackLevelIsOffWithoutAStackSource(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	teamPR(t, st, 1)
	res := evaluateWith(t, st, nil)
	if it := mustItem(t, res, 1); it.Group != "pr:"+pid(1) {
		t.Errorf("group = %q, want the singleton: no stack source is wired", it.Group)
	}
}

func TestWorkItemDoesNotGroupViaItsOwnOutEdges(t *testing.T) {
	// A PR that is the SOURCE of a "work" edge (not the target) is not tracked
	// by that issue; and a "parent" edge is not a work-context link.
	st := store.OpenNewSchemaForTest(t)
	teamPR(t, st, 1)
	link(t, st, "pr", pid(1), "issue", "bd-7", "work")
	link(t, st, "issue", "bd-8", "pr", pid(1), "parent")
	res := evaluate(t, st, baseConfig())
	if it := mustItem(t, res, 1); it.Group != "pr:"+pid(1) {
		t.Errorf("group = %q, want the singleton", it.Group)
	}
}

func TestWorkItemSmallestIDWins(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	teamPR(t, st, 1)
	link(t, st, "issue", "bd-2", "pr", pid(1), "work")
	link(t, st, "issue", "bd-1", "pr", pid(1), "work")
	res := evaluate(t, st, baseConfig())
	if it := mustItem(t, res, 1); it.Group != "issue:bd-1" {
		t.Errorf("group = %q, want issue:bd-1", it.Group)
	}
}

func TestInGroupAndBetweenGroupOrder(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	// Group G1 (ABC-1): #31 medium, #30 medium, #33 high  -> in-group: #33, #30, #31.
	teamPR(t, st, 31)
	teamPR(t, st, 30)
	failingOwnPR(t, st, 33)
	for _, n := range []int{31, 30, 33} {
		link(t, st, "pr", pid(n), "issue", "ABC-1", "jira")
	}
	// Group G2 (ABC-2): #40 high alone.
	failingOwnPR(t, st, 40)
	link(t, st, "pr", pid(40), "issue", "ABC-2", "jira")
	// Group G3 (ABC-3): #50 high, #51 low -> top is high, size 2.
	failingOwnPR(t, st, 50)
	put(t, st, prSpec{number: 51, ownership: "mine", panel: interpret.PanelMineAwaitingMe, ci: "success", approvals: interpret.Approvals{HumanApproved: true}})
	link(t, st, "pr", pid(50), "issue", "ABC-3", "jira")
	link(t, st, "pr", pid(51), "issue", "ABC-3", "jira")
	// Singletons: #20 medium, #10 medium.
	teamPR(t, st, 20)
	teamPR(t, st, 10)

	res := evaluate(t, st, baseConfig())

	// Between groups: top severity desc (high before medium), size desc
	// (G1 has 3 and G3 has 2 and G2 has 1), then top entity id.
	wantKeys := []string{"issue:ABC-1", "issue:ABC-3", "issue:ABC-2", "pr:" + pid(10), "pr:" + pid(20)}
	if got := groupKeys(res); strings.Join(got, ",") != strings.Join(wantKeys, ",") {
		t.Fatalf("group order = %v, want %v", got, wantKeys)
	}
	if got := itemIDs(res.Groups[0].Items); got != "#33,#30,#31" {
		t.Errorf("in-group order = %s, want severity desc then id: #33,#30,#31", got)
	}
	if got := itemIDs(res.Items); got != "#33,#30,#31,#50,#51,#40,#10,#20" {
		t.Errorf("flattened feed order = %s", got)
	}
	// Result.Items is exactly Result.Groups flattened (feed order round-trips).
	var flat []string
	for _, g := range res.Groups {
		for _, it := range g.Items {
			flat = append(flat, it.ID)
		}
	}
	var feed []string
	for _, it := range res.Items {
		feed = append(feed, it.ID)
	}
	if strings.Join(flat, "|") != strings.Join(feed, "|") {
		t.Errorf("Items %v are not the flattened Groups %v", feed, flat)
	}
}

func TestGroupSizeBreaksSeverityTiesBeforeEntityID(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	// A singleton with the smallest id, and a pair with a larger id: same top
	// severity, so size wins over id.
	teamPR(t, st, 1)
	teamPR(t, st, 8)
	teamPR(t, st, 9)
	link(t, st, "pr", pid(8), "issue", "ABC-1", "jira")
	link(t, st, "pr", pid(9), "issue", "ABC-1", "jira")
	res := evaluate(t, st, baseConfig())
	if got := groupKeys(res); strings.Join(got, ",") != "issue:ABC-1,pr:"+pid(1) {
		t.Errorf("group order = %v, want the 2-item group first", got)
	}
}

func TestGroupingIsIndependentOfStoreInsertionOrder(t *testing.T) {
	build := func(order []int) string {
		st := store.OpenNewSchemaForTest(t)
		for _, n := range order {
			teamPR(t, st, n)
			if n%2 == 0 {
				link(t, st, "pr", pid(n), "issue", "ABC-1", "jira")
			}
		}
		res := evaluate(t, st, baseConfig())
		return fmt.Sprintf("%v %s", groupKeys(res), itemIDs(res.Items))
	}
	a := build([]int{1, 2, 3, 4, 5, 6})
	b := build([]int{6, 5, 4, 3, 2, 1})
	if a != b {
		t.Errorf("order depends on insertion order:\n%s\n%s", a, b)
	}
}

func TestUnmigratedStoreGroupsByLegacyJiraAndSkipsBeads(t *testing.T) {
	st := store.OpenForTest(t) // version 1
	teamPR(t, st, 1)
	teamPR(t, st, 2)
	teamPR(t, st, 3)
	for _, n := range []int{1, 2} {
		if err := st.UpsertXref(store.Xref{Repo: testRepo, FromType: "pr", FromID: pid(n), ToType: "issue", ToID: "ABC-5", Evidence: "title", FirstSeen: "2026-10-01T00:00:00Z", LastConfirmed: "2026-10-01T00:00:00Z"}); err != nil {
			t.Fatal(err)
		}
	}
	res := evaluate(t, st, baseConfig())
	if !res.Degraded {
		t.Error("an unmigrated store must still report Degraded")
	}
	if it := mustItem(t, res, 1); it.Group != "issue:ABC-5" {
		t.Errorf("PR 1 group = %q, want the legacy jira link to group on an unmigrated store", it.Group)
	}
	if it := mustItem(t, res, 3); it.Group != "pr:"+pid(3) {
		t.Errorf("PR 3 group = %q, want the singleton", it.Group)
	}
	if res.Groups[0].Key != "issue:ABC-5" || len(res.Groups[0].Items) != 2 {
		t.Errorf("first group = %+v, want the 2-item jira group", res.Groups[0])
	}
}

func TestSuppressedEntitiesDoNotFormGroupsOrCountTowardSize(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	teamPR(t, st, 1)
	teamPR(t, st, 2)
	link(t, st, "pr", pid(1), "issue", "ABC-1", "jira")
	link(t, st, "pr", pid(2), "issue", "ABC-1", "jira")
	if err := st.SetAnnotation(store.KVAnnotation{Repo: testRepo, EntityType: "pr", EntityID: pid(2), Key: store.KeySuppress("attention"), Value: "true", Origin: "test", SetBy: "test", SetAt: "2026-10-06T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	res := evaluate(t, st, baseConfig())
	if len(res.Groups) != 1 || len(res.Groups[0].Items) != 1 {
		t.Errorf("groups = %+v, want one group holding only the unsuppressed PR", res.Groups)
	}
}

func TestGroupReadErrorsAreErrors(t *testing.T) {
	boom := errors.New("xref unreadable")
	t.Run("stack source", func(t *testing.T) {
		st := store.OpenNewSchemaForTest(t)
		teamPR(t, st, 1)
		_, err := Evaluate(Inputs{Store: st, Repo: testRepo, Config: baseConfig(), Clock: testClock(), Stacks: failingStacks{err: boom}})
		if !errors.Is(err, boom) {
			t.Errorf("err = %v, want the stack source error", err)
		}
	})
	t.Run("xref read", func(t *testing.T) {
		st := store.OpenNewSchemaForTest(t)
		teamPR(t, st, 1)
		_, err := Evaluate(Inputs{Store: xrefFailingReader{Reader: st, err: boom}, Repo: testRepo, Config: baseConfig(), Clock: testClock()})
		if !errors.Is(err, boom) {
			t.Errorf("err = %v, want the xref read error, never an ungrouped result", err)
		}
	})
}

type xrefFailingReader struct {
	Reader
	err error
}

func (x xrefFailingReader) ListXrefLinksFrom(string, string, string) ([]store.XrefLink, error) {
	return nil, x.err
}

func TestOrderingTiesConfig(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	teamPR(t, st, 1)
	for name, ties := range map[string]string{"unset": "", "the documented default": config.AttentionTiesDefault} {
		cfg := baseConfig()
		cfg.Attention.Ordering.Ties = ties
		if _, err := Evaluate(Inputs{Store: st, Repo: testRepo, Config: cfg, Clock: testClock()}); err != nil {
			t.Errorf("%s: err = %v, want accepted", name, err)
		}
	}
	cfg := baseConfig()
	cfg.Attention.Ordering.Ties = "entity id first"
	_, err := Evaluate(Inputs{Store: st, Repo: testRepo, Config: cfg, Clock: testClock()})
	if err == nil || !strings.Contains(err.Error(), "attention.ordering.ties") {
		t.Errorf("err = %v, want an error naming attention.ordering.ties for an unimplemented tie rule", err)
	}
}
