package sync

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/interpret"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// Area labels (bead pg2-lvoye): the merge-request anchor derives them from
// the configured area_labels rules, and review-pr / process-feedback
// children copy them. Every vocabulary word below is a generic placeholder;
// real vocabularies are deployment config.

func areaConfig(mode string) *config.Config {
	cfg := testConfig(mode)
	cfg.AreaLabels = []config.AreaLabelRule{
		{Pattern: `^feat\(svc/alpha\)`, Labels: []string{"alpha", "alpha-sub"}},
		{Pattern: `BETA-[0-9]+`, Field: config.AreaFieldBranch, Labels: []string{"beta"}},
	}
	return cfg
}

func areaSyncer(t *testing.T, cfg *config.Config) *Syncer {
	t.Helper()
	return New(cfg, store.OpenForTest(t), WithClock(fixedClock))
}

const (
	areaTitle  = "feat(svc/alpha): fix the bug"
	areaBranch = "user.BETA-12.thing"
)

func areaPR() map[string]any { return map[string]any{"title": areaTitle, "branch": areaBranch} }

// createdLabels returns the sorted --labels values of the `issue create`
// whose argv contains marker, and whether such a create happened.
func createdLabels(t *testing.T, recordFile, marker string) ([]string, bool) {
	t.Helper()
	for _, r := range readCallRecords(t, recordFile) {
		if r.verb() != "issue create" || !strings.Contains(strings.Join(r.Args, " "), marker) {
			continue
		}
		for i, a := range r.Args {
			if a == "--labels" && i+1 < len(r.Args) {
				return strings.Split(r.Args[i+1], ","), true
			}
		}
		return nil, true
	}
	return nil, false
}

// addedLabels returns every --add-label value on `issue update` calls for id.
func addedLabels(t *testing.T, recordFile, id string) []string {
	t.Helper()
	var out []string
	for _, r := range readCallRecords(t, recordFile) {
		if r.verb() != "issue update" || !strings.Contains(strings.Join(r.Args, " "), id) {
			continue
		}
		for i, a := range r.Args {
			if a == "--add-label" && i+1 < len(r.Args) {
				out = append(out, r.Args[i+1])
			}
		}
	}
	return out
}

func TestAreaLabels_CreatePathLabelsAnchorAndChildren(t *testing.T) {
	s := areaSyncer(t, areaConfig(ModeApply))
	recordFile := withFactory(t)
	facts := gather.Facts{PRShow: prFixture(areaPR()), HeadSHA: fixtureHeadSHA}
	interp := interpFor("mine", []interpret.Disposition{openDisposition("c1")})
	if err := s.Sync(context.Background(), fixtureRepo, fixtureEntity, gather.ChangeAdded, facts, interp); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	want := []string{"alpha", "alpha-sub", "beta"}
	for marker, name := range map[string]string{"merge-request": "anchor", "review-pr:": "review-pr", "process-feedback:": "process-feedback"} {
		got, created := createdLabels(t, recordFile, marker)
		if !created {
			t.Fatalf("%s was not created; records: %+v", name, readCallRecords(t, recordFile))
		}
		for _, w := range want {
			if !hasLabel(got, w) {
				t.Errorf("%s create labels = %v, want to include %q", name, got, w)
			}
		}
	}
}

func TestAreaLabels_NoConfigAddsNoLabels(t *testing.T) {
	s := areaSyncer(t, testConfig(ModeApply))
	recordFile := withFactory(t)
	facts := gather.Facts{PRShow: prFixture(areaPR()), HeadSHA: fixtureHeadSHA}
	if err := s.Sync(context.Background(), fixtureRepo, fixtureEntity, gather.ChangeAdded, facts, interpFor("mine", nil)); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	for _, marker := range []string{"merge-request", "review-pr:"} {
		got, created := createdLabels(t, recordFile, marker)
		if !created || len(got) != 0 {
			t.Errorf("%s: created=%v labels=%v, want created with no labels", marker, created, got)
		}
	}
}

func TestAreaLabels_NonMatchingPRGetsNoAreaLabels(t *testing.T) {
	s := areaSyncer(t, areaConfig(ModeApply))
	recordFile := withFactory(t)
	facts := gather.Facts{PRShow: prFixture(nil), HeadSHA: fixtureHeadSHA} // "fix the bug" / "feature"
	if err := s.Sync(context.Background(), fixtureRepo, fixtureEntity, gather.ChangeAdded, facts, interpFor("mine", nil)); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if got, created := createdLabels(t, recordFile, "review-pr:"); !created || len(got) != 0 {
		t.Errorf("review-pr: created=%v labels=%v, want created with no labels", created, got)
	}
}

// An adopted anchor missing area labels gets ONLY the missing ones added, and
// nothing is ever removed (operator-added labels survive).
func TestAreaLabels_ExistingAnchorBackfillsMissingOnly(t *testing.T) {
	s := areaSyncer(t, areaConfig(ModeApply))
	recordFile := withFactory(t)
	anchor := map[string]any{
		"id": "bd-anchor-x", "title": fixturePRKey + ": " + areaTitle, "state": "open",
		"priority": "P2", "labels": []string{"alpha", "operator-added"},
		"metadata": map[string]string{"repo": fixtureRepo, "pr_number": "42"},
	}
	facts := gather.Facts{PRShow: prFixture(areaPR()), HeadSHA: fixtureHeadSHA, WorkBeads: workBeadsFixture(anchor)}
	if err := s.Sync(context.Background(), fixtureRepo, fixtureEntity, gather.ChangeAdded, facts, interpFor("mine", nil)); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	got := addedLabels(t, recordFile, "bd-anchor-x")
	if !reflect.DeepEqual(sortedCopy(got), []string{"alpha-sub", "beta"}) {
		t.Errorf("anchor --add-label = %v, want [alpha-sub beta]", got)
	}
	for _, r := range readCallRecords(t, recordFile) {
		if strings.Contains(strings.Join(r.Args, " "), "--remove-label") {
			t.Errorf("sync removed a label: %v", r.Args)
		}
	}
}

// A child copies an area label its parent anchor carries even when the
// configured rules derive nothing for this PR (hand-labelled anchor).
func TestAreaLabels_ChildCopiesParentAreaLabels(t *testing.T) {
	s := areaSyncer(t, areaConfig(ModeApply))
	recordFile := withFactory(t)
	anchor := map[string]any{
		"id": "bd-anchor-y", "title": fixturePRKey + ": fix the bug", "state": "open",
		"priority": "P2", "labels": []string{"beta", "operator-added", "co-owned"},
		"metadata": map[string]string{"repo": fixtureRepo, "pr_number": "42"},
	}
	facts := gather.Facts{PRShow: prFixture(nil), HeadSHA: fixtureHeadSHA, WorkBeads: workBeadsFixture(anchor)}
	if err := s.Sync(context.Background(), fixtureRepo, fixtureEntity, gather.ChangeAdded, facts, interpFor("mine", nil)); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	got, created := createdLabels(t, recordFile, "review-pr:")
	if !created || !reflect.DeepEqual(got, []string{"beta"}) {
		t.Errorf("review-pr create: created=%v labels=%v, want exactly [beta] (non-area parent labels are not copied)", created, got)
	}
}

// A review-pr reopen on head advance back-fills missing area labels in the
// same update.
func TestAreaLabels_ReviewReopenAddsMissingAreaLabels(t *testing.T) {
	s := areaSyncer(t, areaConfig(ModeApply))
	recordFile := withFactory(t)
	if err := s.store.UpsertLedger(store.LedgerEntry{
		Repo: fixtureRepo, EntityType: "pr", EntityID: fixtureEntity, Kind: KindReviewRequest,
		BeadID: "bd-review-z", LastSyncedContentHash: "old", LastSyncedAt: "2026-09-16T00:00:00Z",
		LastReviewedHeadSHA: "old-sha",
		FirstSeenHeadSHA:    "new-sha", FirstSeenHeadAt: "2026-09-16T00:00:00Z", // settled (pg2-a9yhn)
	}); err != nil {
		t.Fatalf("seed ledger: %v", err)
	}
	review := map[string]any{
		"id": "bd-review-z", "title": "review-pr: " + fixturePRKey, "state": "closed",
		"priority": "P2", "labels": []string{"beta"},
		"metadata": map[string]string{"repo": fixtureRepo, "pr_number": "42"},
	}
	facts := gather.Facts{PRShow: prFixture(areaPR()), HeadSHA: "new-sha", WorkBeads: workBeadsFixture(review)}
	if err := s.Sync(context.Background(), fixtureRepo, fixtureEntity, gather.ChangeChanged, facts, interpFor("mine", nil)); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	got := addedLabels(t, recordFile, "bd-review-z")
	if !reflect.DeepEqual(sortedCopy(got), []string{"alpha", "alpha-sub"}) {
		t.Errorf("review --add-label = %v, want [alpha alpha-sub]", got)
	}
}
