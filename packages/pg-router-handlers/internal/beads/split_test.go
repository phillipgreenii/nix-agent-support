package beads

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"
)

// splitBD is an ordered fake bd for the split-triage write path: `show`
// serves the parent, `create` mints sequential child ids, `dep add <blocked>
// <blocker>` records edges and `dep list <id>` serves them back as the
// dependencies of <id> (what blocks it).
type splitBD struct {
	parent    string // JSON served for `show`
	calls     []string
	argv      [][]string
	nextChild int
	edges     map[string][]string // blocked -> blockers
	reverse   bool                // simulate a reversed edge (blocker recorded as blocked)
	failOn    string
	// refuseGuarded, when set, is returned by every `update` that carries a
	// --if-assignee guard (bd's refusal of a guarded write), after the call is
	// recorded.
	refuseGuarded error
}

func (b *splitBD) Run(_ context.Context, args ...string) (string, error) {
	j := strings.Join(args, " ")
	b.calls = append(b.calls, j)
	b.argv = append(b.argv, args)
	if b.failOn != "" && strings.Contains(j, b.failOn) {
		return "", errors.New("bd down")
	}
	if b.refuseGuarded != nil && args[0] == "update" && strings.Contains(j, "--if-assignee") {
		return "", b.refuseGuarded
	}
	switch {
	case args[0] == "show":
		return b.parent, nil
	case args[0] == "create":
		b.nextChild++
		return fmt.Sprintf("pg2-c%d\n", b.nextChild), nil
	case args[0] == "dep" && args[1] == "add":
		blocked, blocker := args[2], args[3]
		if b.reverse {
			blocked, blocker = blocker, blocked
		}
		if b.edges == nil {
			b.edges = map[string][]string{}
		}
		b.edges[blocked] = append(b.edges[blocked], blocker)
		return "", nil
	case args[0] == "dep" && args[1] == "list":
		var parts []string
		for _, id := range b.edges[args[2]] {
			parts = append(parts, fmt.Sprintf(`{"id":%q}`, id))
		}
		return `{"data":[` + strings.Join(parts, ",") + `]}`, nil
	}
	return "", nil
}

func (b *splitBD) idx(s string) int {
	for i, c := range b.calls {
		if c == s {
			return i
		}
	}
	return -1
}

const parentJSON = `{"id":"pg2-p","status":"%s","labels":["agent-support","pg-router","needs-split-review","budget-stop:s1","budget-stop:s2","budget-stop:s3"]}`

func twoChildren() SplitPlan {
	return SplitPlan{Rationale: "two separable apps", Children: []SplitChild{
		{Title: "part a", Description: "do a; $(rm -rf /)", Acceptance: "- [ ] a done", Priority: "2"},
		{Title: "-part b", Description: "do b", Acceptance: "- [ ] b done"},
	}}
}

func TestApplySplit_goldenPath(t *testing.T) {
	bd := &splitBD{parent: fmt.Sprintf(parentJSON, "in_progress")}
	ids, err := ApplySplit(context.Background(), bd, "pg2-p", twoChildren())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(ids, ",") != "pg2-c1,pg2-c2" {
		t.Fatalf("ids=%v", ids)
	}
	// Children: split-from label, no stop history/needs-split-review inherited,
	// repo labels kept; bead text is ONE argv element each (no shell involved).
	first := bd.argv[bd.idxPrefix("create")]
	want := []string{
		"create", "--title=part a", "--description=do a; $(rm -rf /)", "--acceptance=- [ ] a done",
		"--labels=agent-support,pg-router,split-from:pg2-p", "--type=task", "--silent", "--priority=2",
	}
	if strings.Join(first, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("create argv\n got %q\nwant %q", first, want)
	}
	for _, c := range bd.calls {
		if strings.HasPrefix(c, "create") && (strings.Contains(c, "budget-stop") || strings.Contains(c, "needs-split-review")) {
			t.Errorf("child inherited parent state: %s", c)
		}
	}
	// Edge direction: blocked id (parent) first, default blocks type, children blockers.
	for _, c := range []string{"dep add pg2-p pg2-c1", "dep add pg2-p pg2-c2"} {
		if bd.idx(c) < 0 {
			t.Errorf("missing %q; calls=%v", c, bd.calls)
		}
	}
	if got := bd.edges["pg2-p"]; len(got) != 2 || bd.edges["pg2-c1"] != nil {
		t.Errorf("edges=%v: parent must be blocked-by the children, not the reverse", bd.edges)
	}
	// Parent outcome + ordering: was-split, comment, release (one update), label removal LAST.
	was := bd.idx("update pg2-p --add-label was-split")
	rel := bd.idx("update pg2-p --status=open --assignee=")
	rm := bd.idx("update pg2-p --remove-label needs-split-review")
	if was < 0 || rel < 0 || rm < 0 || !(was < rel && rel < rm) || rm != len(bd.calls)-1 {
		t.Errorf("was=%d release=%d remove=%d of %d; calls=%v", was, rel, rm, len(bd.calls), bd.calls)
	}
	cm := ""
	for _, c := range bd.calls {
		if strings.HasPrefix(c, "comment pg2-p ") {
			cm = c
		}
	}
	if !strings.Contains(cm, "pg2-c1 part a") || !strings.Contains(cm, "rationale: two separable apps") {
		t.Errorf("decomposition comment=%q", cm)
	}
	for _, c := range bd.calls {
		if strings.Contains(c, "budget") && !strings.HasPrefix(c, "show") {
			t.Errorf("split must not touch budgets: %s", c)
		}
	}
}

func (b *splitBD) idxPrefix(p string) int {
	for i, c := range b.calls {
		if strings.HasPrefix(c, p) {
			return i
		}
	}
	return -1
}

func TestApplySplit_notClaimedLeavesStatusAlone(t *testing.T) {
	bd := &splitBD{parent: fmt.Sprintf(parentJSON, "open")}
	if _, err := ApplySplit(context.Background(), bd, "pg2-p", twoChildren()); err != nil {
		t.Fatal(err)
	}
	if bd.idx("update pg2-p --status=open --assignee=") >= 0 {
		t.Errorf("unclaimed parent must not be re-released; calls=%v", bd.calls)
	}
}

func TestApplySplit_reversedEdgeFailsSafeToHuman(t *testing.T) {
	bd := &splitBD{parent: fmt.Sprintf(parentJSON, "in_progress"), reverse: true}
	_, err := ApplySplit(context.Background(), bd, "pg2-p", twoChildren())
	if err == nil || !strings.Contains(err.Error(), "lacks blocked-by") {
		t.Fatalf("want edge verification failure, got %v", err)
	}
	if bd.idx("update pg2-p --add-label human --add-label attention --remove-label escalated") < 0 || bd.idx("update pg2-p --remove-label needs-split-review") < 0 ||
		bd.idx("update pg2-p --status=open --assignee=") < 0 || bd.idx("update pg2-p --add-label was-split") >= 0 {
		t.Errorf("fail-safe path wrong; calls=%v", bd.calls)
	}
}

func TestApplySplit_createFailureFailsSafe(t *testing.T) {
	bd := &splitBD{parent: fmt.Sprintf(parentJSON, "open"), failOn: "--title=-part b"}
	ids, err := ApplySplit(context.Background(), bd, "pg2-p", twoChildren())
	if err == nil || len(ids) != 1 {
		t.Fatalf("ids=%v err=%v", ids, err)
	}
	if bd.idx("update pg2-p --add-label human --add-label attention --remove-label escalated") < 0 {
		t.Errorf("must fail safe to human; calls=%v", bd.calls)
	}
}

func TestApplySplit_refusals(t *testing.T) {
	for name, tc := range map[string]struct{ parent, want string }{
		"closed":    {fmt.Sprintf(parentJSON, "closed"), "closed"},
		"was-split": {`{"id":"pg2-p","status":"open","labels":["was-split"]}`, "already split"},
		"child":     {`{"id":"pg2-p","status":"open","labels":["split-from:pg2-x"]}`, "split child"},
	} {
		t.Run(name, func(t *testing.T) {
			bd := &splitBD{parent: tc.parent}
			_, err := ApplySplit(context.Background(), bd, "pg2-p", twoChildren())
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err=%v", err)
			}
			if bd.idxPrefix("create") >= 0 || bd.idxPrefix("update") >= 0 {
				t.Errorf("a refusal must write nothing; calls=%v", bd.calls)
			}
		})
	}
}

func TestSplitPlan_validate(t *testing.T) {
	for name, p := range map[string]SplitPlan{
		"one child":      {Children: []SplitChild{{Title: "a", Description: "d", Acceptance: "x"}}},
		"no acceptance":  {Children: []SplitChild{{Title: "a", Description: "d", Acceptance: "x"}, {Title: "b", Description: "d"}}},
		"no description": {Children: []SplitChild{{Title: "a", Description: "d", Acceptance: "x"}, {Title: "b", Acceptance: "x"}}},
		"no title":       {Children: []SplitChild{{Title: "a", Description: "d", Acceptance: "x"}, {Description: "d", Acceptance: "x"}}},
	} {
		if p.Validate() == nil {
			t.Errorf("%s: want error", name)
		}
	}
	bd := &splitBD{parent: fmt.Sprintf(parentJSON, "open")}
	if _, err := ApplySplit(context.Background(), bd, "pg2-p", SplitPlan{}); err == nil || len(bd.calls) != 0 {
		t.Errorf("invalid plan must fail before any bd call; err=%v calls=%v", err, bd.calls)
	}
}

func TestMarkUnsplittable(t *testing.T) {
	bd := &splitBD{parent: fmt.Sprintf(parentJSON, "in_progress")}
	if err := MarkUnsplittable(context.Background(), bd, "pg2-p", "single indivisible refactor"); err != nil {
		t.Fatal(err)
	}
	hum := bd.idx("update pg2-p --add-label human --add-label attention --remove-label escalated")
	cm := bd.idx("comment pg2-p split-triage: not splittable; escalated to human. reason: single indivisible refactor. budget stops: 3 (sessions: s1, s2, s3)")
	rel := bd.idx("update pg2-p --status=open --assignee=")
	rm := bd.idx("update pg2-p --remove-label needs-split-review")
	if hum < 0 || cm < 0 || rel < 0 || rm != len(bd.calls)-1 {
		t.Errorf("hum=%d cm=%d rel=%d rm=%d; calls=%v", hum, cm, rel, rm, bd.calls)
	}
	if bd.idxPrefix("create") >= 0 {
		t.Error("unsplittable must create nothing")
	}
	if MarkUnsplittable(context.Background(), bd, "pg2-p", "  ") == nil {
		t.Error("empty reason must be rejected")
	}
}

func TestDecodeSplitPlan(t *testing.T) {
	p, err := DecodeSplitPlan([]byte(`{"rationale":"r","children":[{"title":"t","description":"d","acceptance":"a"}]}`))
	if err != nil || len(p.Children) != 1 || p.Children[0].Acceptance != "a" {
		t.Fatalf("p=%+v err=%v", p, err)
	}
	if _, err := DecodeSplitPlan([]byte("nope")); err == nil {
		t.Error("want decode error")
	}
}

// parentAssignedJSON is parentJSON for a parent claimed by the drain worker's
// actor, a different actor from the split-triager that runs the split.
const parentAssignedJSON = `{"id":"pg2-p","status":"in_progress","assignee":"pgii-pool__drain-pg2","labels":["agent-support","pg-router","needs-split-review"]}`

// guardedRelease is the one release update the split must write for
// parentAssignedJSON: bd 1.3.1 refuses an unguarded cross-actor release, so it
// carries the guard of the claim read at the start (pg2-ybkav, class pg2-qyc3h).
const guardedRelease = "update pg2-p --status=open --assignee= --if-assignee pgii-pool__drain-pg2 --if-status in_progress"

// guardStaleErr is bd's exit 13 refusal of a guarded update whose guard no
// longer holds: a real *exec.ExitError, which is what IsGuardStale reads.
func guardStaleErr(t *testing.T) error {
	t.Helper()
	err := exec.Command("sh", "-c", "exit 13").Run()
	if !IsGuardStale(err) {
		t.Fatalf("test setup: %v is not a stale-guard error", err)
	}
	return err
}

// TestApplySplit_releasesAClaimHeldByAnotherActorGuarded: the parent's claim
// belongs to another actor, so the release is the guarded update, written once,
// before the completion-signal label removal.
func TestApplySplit_releasesAClaimHeldByAnotherActorGuarded(t *testing.T) {
	bd := &splitBD{parent: parentAssignedJSON}
	if _, err := ApplySplit(context.Background(), bd, "pg2-p", twoChildren()); err != nil {
		t.Fatal(err)
	}
	rel := bd.idx(guardedRelease)
	rm := bd.idx("update pg2-p --remove-label needs-split-review")
	if rel < 0 || rm != len(bd.calls)-1 || rel > rm {
		t.Errorf("release=%d remove=%d of %d; calls=%v", rel, rm, len(bd.calls), bd.calls)
	}
	if bd.idx("update pg2-p --status=open --assignee=") >= 0 {
		t.Errorf("the unguarded release bd refuses must not be written; calls=%v", bd.calls)
	}
}

// TestApplySplit_staleGuardIsSkippedNotFailed: the claim changed hands after the
// parent was read, bd refuses the guarded release with exit 13, and that is a
// skip: the split still succeeds, is not failed safe to a human, and the label
// removal still lands.
func TestApplySplit_staleGuardIsSkippedNotFailed(t *testing.T) {
	bd := &splitBD{parent: parentAssignedJSON, refuseGuarded: guardStaleErr(t)}
	ids, err := ApplySplit(context.Background(), bd, "pg2-p", twoChildren())
	if err != nil {
		t.Fatalf("a stale guard must not fail the split: %v", err)
	}
	if len(ids) != 2 {
		t.Errorf("ids=%v", ids)
	}
	if bd.idx(guardedRelease) < 0 {
		t.Errorf("the guarded release must have been attempted; calls=%v", bd.calls)
	}
	if bd.idxPrefix("update pg2-p --add-label human") >= 0 {
		t.Errorf("a stale guard must not hand the split parent to a human; calls=%v", bd.calls)
	}
	if bd.idx("update pg2-p --remove-label needs-split-review") != len(bd.calls)-1 {
		t.Errorf("the completion label must still be removed last; calls=%v", bd.calls)
	}
}

// TestApplySplit_nonGuardReleaseFailureFailsSafe: any other refusal still fails
// the split safe to a human (the children exist).
func TestApplySplit_nonGuardReleaseFailureFailsSafe(t *testing.T) {
	bd := &splitBD{parent: parentAssignedJSON, refuseGuarded: errors.New("bd down")}
	_, err := ApplySplit(context.Background(), bd, "pg2-p", twoChildren())
	if err == nil || !strings.Contains(err.Error(), "failed safe to human") {
		t.Fatalf("want a failed-safe error, got %v", err)
	}
	if bd.idxPrefix("update pg2-p --add-label human") < 0 {
		t.Errorf("must hand the parent to a human; calls=%v", bd.calls)
	}
}

// TestMarkUnsplittable_releasesAClaimHeldByAnotherActorGuarded is the same
// guard for the not-splittable verdict.
func TestMarkUnsplittable_releasesAClaimHeldByAnotherActorGuarded(t *testing.T) {
	bd := &splitBD{parent: parentAssignedJSON}
	if err := MarkUnsplittable(context.Background(), bd, "pg2-p", "indivisible"); err != nil {
		t.Fatal(err)
	}
	if bd.idx(guardedRelease) < 0 || bd.idx("update pg2-p --remove-label needs-split-review") != len(bd.calls)-1 {
		t.Errorf("calls=%v", bd.calls)
	}
}

// TestMarkUnsplittable_staleGuardIsSkippedNotFailed: a stale guard must not
// return an error (which would leave needs-split-review on a human bead).
func TestMarkUnsplittable_staleGuardIsSkippedNotFailed(t *testing.T) {
	bd := &splitBD{parent: parentAssignedJSON, refuseGuarded: guardStaleErr(t)}
	if err := MarkUnsplittable(context.Background(), bd, "pg2-p", "indivisible"); err != nil {
		t.Fatalf("a stale guard must not fail the verdict: %v", err)
	}
	if bd.idx("update pg2-p --remove-label needs-split-review") != len(bd.calls)-1 {
		t.Errorf("needs-split-review must still be removed last; calls=%v", bd.calls)
	}
}
