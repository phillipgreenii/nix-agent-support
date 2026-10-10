package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// newStoreAt returns an open func for a store in the given state ("old" or
// "new", see storeAtVersion) and a seed handle on it. Both states hold the
// pr entity o/r#1.
func newStoreAt(t *testing.T, kind string) (open func() (*store.Store, error), seed *store.Store) {
	t.Helper()
	path := storeAtVersion(t, kind)
	open = func() (*store.Store, error) { return store.Open(path) }
	seed, err := open()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = seed.Close() })
	return open, seed
}

func suppressCmd(entityType string) *cobra.Command   { return newSuppressCmd(entityType, true) }
func unsuppressCmd(entityType string) *cobra.Command { return newSuppressCmd(entityType, false) }
func forceReviewCmd(string) *cobra.Command           { return newForceReviewCmd() }

// wantOneAnnotationChange asserts the entity's history is exactly the
// seeded records plus ONE annotation_changed record from origin.
func wantOneAnnotationChange(t *testing.T, st *store.Store, entityType, id string, seeded []string, origin string) {
	t.Helper()
	want := append(append([]string{}, seeded...), "annotation_changed@"+origin)
	if got := kindsOf(t, st, entityType, id); !equalStrings(got, want) {
		t.Errorf("history = %v, want %v", got, want)
	}
}

func wantAnnotation(t *testing.T, st *store.Store, entityType, id, key, value, origin, by string) {
	t.Helper()
	a, found, err := st.GetKVAnnotation("o/r", entityType, id, key)
	if err != nil || !found {
		t.Fatalf("annotation %q: found=%v err=%v", key, found, err)
	}
	if a.Value != value || a.Origin != origin || a.SetBy != by {
		t.Errorf("annotation %q = %+v, want value %q origin %q set_by %q", key, a, value, origin, by)
	}
}

func wantOldSchemaRefusal(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("error = nil, want the old-schema refusal")
	}
	if !errors.Is(err, store.ErrOldSchema) || !strings.Contains(err.Error(), "pg-desk migrate --cutover") {
		t.Errorf("error = %v, want the pg-desk migrate --cutover refusal", err)
	}
}

func TestAnnotationVerbsRegisteredUnderTypeGroups(t *testing.T) {
	for _, typ := range []string{"pr", "issue", "thread"} {
		for _, verb := range []string{"annotate", "suppress", "unsuppress", "hide", "unhide", "wip"} {
			c, _, err := rootCmd.Find([]string{typ, verb})
			if err != nil || c.Name() != verb || c.Parent() != typeGroup(typ) {
				t.Errorf("%s %s not registered: %v, %v", typ, verb, c, err)
			}
		}
	}
	for _, path := range [][]string{{"pr", "force-review"}, {"pr", "feedback", "list"}, {"pr", "feedback", "set"}} {
		c, _, err := rootCmd.Find(path)
		if err != nil || c.Name() != path[len(path)-1] {
			t.Errorf("%v not registered: %v, %v", path, c, err)
		}
	}
	for _, typ := range []string{"issue", "thread"} {
		for _, verb := range []string{"force-review", "feedback"} {
			if c, _, _ := rootCmd.Find([]string{typ, verb}); c != nil && c.Name() == verb {
				t.Errorf("%s %s registered; it is pr-only", typ, verb)
			}
		}
	}
}

func TestAnnotateWritesRowAndOneChangeRecord(t *testing.T) {
	open, seed := seedLinkStore(t, [2]string{"pr", "o/r#5"}, [2]string{"issue", "K-1"})
	withOpenSeams(t, linkTestConfig(), open)

	if _, err := runGroupCmd(t, newAnnotateCmd, "pr", "5", "--key", "decider.land.note", "--value", "looks fine"); err != nil {
		t.Fatalf("annotate: %v", err)
	}
	wantAnnotation(t, seed, "pr", "o/r#5", "decider.land.note", "looks fine", "pg-desk", "alice")
	wantOneAnnotationChange(t, seed, "pr", "o/r#5", []string{"created@sync"}, "pg-desk")

	// issue ids are verbatim; --origin and --actor are honored.
	if _, err := runGroupCmd(t, newAnnotateCmd, "issue", "K-1", "--key", "ready_to_land", "--value", "true",
		"--origin", "decider:land.ready", "--actor", "bob"); err != nil {
		t.Fatalf("annotate issue: %v", err)
	}
	wantAnnotation(t, seed, "issue", "K-1", "ready_to_land", "true", "decider:land.ready", "bob")
	wantOneAnnotationChange(t, seed, "issue", "K-1", []string{"created@sync"}, "decider:land.ready")
}

func TestAnnotateReplacesAValue(t *testing.T) {
	open, seed := seedLinkStore(t, [2]string{"pr", "o/r#5"})
	withOpenSeams(t, linkTestConfig(), open)
	for _, v := range []string{"a", "b"} {
		if _, err := runGroupCmd(t, newAnnotateCmd, "pr", "5", "--key", "free", "--value", v); err != nil {
			t.Fatal(err)
		}
	}
	wantAnnotation(t, seed, "pr", "o/r#5", "free", "b", "pg-desk", "alice")
	if got := kindsOf(t, seed, "pr", "o/r#5"); len(got) != 3 {
		t.Errorf("history = %v, want created + two annotation_changed", got)
	}
}

func TestAnnotateRejectsBadReservedValuesAndMissingFlags(t *testing.T) {
	open, seed := seedLinkStore(t, [2]string{"pr", "o/r#5"})
	withOpenSeams(t, linkTestConfig(), open)

	for _, tc := range []struct{ key, value string }{
		{"hidden", "true"},
		{"hidden", `{"reason":"x"}`},
		{"hidden", `{"value":true,"extra":1}`},
		{"wip", "yes"},
		{"ready_to_land", "1"},
		{"suppress.lint", "false"},
		{"suppress.", "true"},
		{"disposition.c1", "open"},
		{"force_review", " "},
		{"focus_selected", "tomorrow"},
		{"focus_selected", ""},
		{"focus_selected", "2026-9-23"},
		{"focus_selected", "2026-13-01"},
		{" ", "x"},
	} {
		if _, err := runGroupCmd(t, newAnnotateCmd, "pr", "5", "--key", tc.key, "--value", tc.value); err == nil {
			t.Errorf("annotate %q=%q: error = nil, want a value-shape error", tc.key, tc.value)
		}
	}
	if _, err := runGroupCmd(t, newAnnotateCmd, "pr", "5", "--key", "free"); err == nil {
		t.Error("annotate without --value: error = nil")
	}
	if _, err := runGroupCmd(t, newAnnotateCmd, "pr", "5", "--value", "x"); err == nil {
		t.Error("annotate without --key: error = nil")
	}
	// Valid reserved shapes pass.
	for _, tc := range []struct{ key, value string }{
		{"hidden", `{"value":false,"reason":null}`},
		{"wip", "true"},
		{"suppress.lint", "true"},
		{"disposition.c1", "wont-fix"},
		{"force_review", "abc123"},
		{"focus_selected", "2026-09-23"},
		{"focus_selected", "none"},
	} {
		if _, err := runGroupCmd(t, newAnnotateCmd, "pr", "5", "--key", tc.key, "--value", tc.value); err != nil {
			t.Errorf("annotate %q=%q: %v", tc.key, tc.value, err)
		}
	}
	if got := kindsOf(t, seed, "pr", "o/r#5"); len(got) != 8 {
		t.Errorf("history = %v, want created + seven annotation_changed (rejected writes append nothing)", got)
	}
}

func TestAnnotateUnknownEntityFailsWritingNothing(t *testing.T) {
	open, seed := seedLinkStore(t, [2]string{"pr", "o/r#5"})
	withOpenSeams(t, linkTestConfig(), open)

	_, err := runGroupCmd(t, newAnnotateCmd, "pr", "99", "--key", "free", "--value", "x")
	if err == nil || !strings.Contains(err.Error(), "does not resolve to a stored entity") {
		t.Fatalf("error = %v, want a does-not-resolve error", err)
	}
	if got, _ := seed.ListKVAnnotations("o/r", "pr", "o/r#99"); len(got) != 0 {
		t.Errorf("annotations = %+v, want none", got)
	}
}

func TestAnnotateNeedsAnActor(t *testing.T) {
	open, _ := seedLinkStore(t, [2]string{"pr", "o/r#5"})
	cfg := linkTestConfig()
	cfg.Actor = ""
	withOpenSeams(t, cfg, open)
	if _, err := runGroupCmd(t, newAnnotateCmd, "pr", "5", "--key", "free", "--value", "x"); err == nil || !strings.Contains(err.Error(), "no actor") {
		t.Errorf("error = %v, want a no-actor error", err)
	}
}

func TestSuppressUnsuppressRoundTrip(t *testing.T) {
	open, seed := seedLinkStore(t, [2]string{"pr", "o/r#5"})
	withOpenSeams(t, linkTestConfig(), open)

	if _, err := runGroupCmd(t, suppressCmd, "pr", "5", "--kind", "lint"); err != nil {
		t.Fatalf("suppress: %v", err)
	}
	wantAnnotation(t, seed, "pr", "o/r#5", "suppress.lint", "true", "pg-desk", "alice")
	wantOneAnnotationChange(t, seed, "pr", "o/r#5", []string{"created@sync"}, "pg-desk")

	if _, err := runGroupCmd(t, unsuppressCmd, "pr", "5", "--kind", "lint"); err != nil {
		t.Fatalf("unsuppress: %v", err)
	}
	if _, found, _ := seed.GetKVAnnotation("o/r", "pr", "o/r#5", "suppress.lint"); found {
		t.Error("suppress.lint still present after unsuppress")
	}
	if got := kindsOf(t, seed, "pr", "o/r#5"); len(got) != 3 || got[2] != "annotation_changed@pg-desk" {
		t.Errorf("history = %v, want created + suppress + unsuppress records", got)
	}

	// Unsuppressing what is not suppressed is a no-op that appends nothing.
	out, err := runGroupCmd(t, unsuppressCmd, "pr", "5", "--kind", "lint")
	if err != nil || !strings.Contains(out, "nothing to do") {
		t.Errorf("repeat unsuppress: out=%q err=%v", out, err)
	}
	if got := kindsOf(t, seed, "pr", "o/r#5"); len(got) != 3 {
		t.Errorf("history = %v, want no new record", got)
	}
}

func TestSuppressOtherKindsAreIndependent(t *testing.T) {
	open, seed := seedLinkStore(t, [2]string{"issue", "K-1"})
	withOpenSeams(t, linkTestConfig(), open)
	for _, k := range []string{"a", "b"} {
		if _, err := runGroupCmd(t, suppressCmd, "issue", "K-1", "--kind", k); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := runGroupCmd(t, unsuppressCmd, "issue", "K-1", "--kind", "a"); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := seed.GetKVAnnotation("o/r", "issue", "K-1", "suppress.b"); !found {
		t.Error("suppress.b removed by unsuppress --kind a")
	}
	if _, err := runGroupCmd(t, suppressCmd, "issue", "K-1"); err == nil {
		t.Error("suppress without --kind: error = nil")
	}
}

func TestForceReviewStoresHeadSHA(t *testing.T) {
	open, seed := newStoreAt(t, "new")
	if err := seed.UpsertEntity(store.Entity{
		Repo: "o/r", EntityType: "pr", EntityID: "o/r#1", Facts: `{}`, AsOf: "2026-09-29T00:00:00Z", HeadSHA: "deadbeef",
	}); err != nil {
		t.Fatal(err)
	}
	withOpenSeams(t, linkTestConfig(), open)

	if _, err := runGroupCmd(t, forceReviewCmd, "pr", "1"); err != nil {
		t.Fatalf("force-review: %v", err)
	}
	wantAnnotation(t, seed, "pr", "o/r#1", "force_review", "deadbeef", "pg-desk", "alice")
	wantOneAnnotationChange(t, seed, "pr", "o/r#1", nil, "pg-desk")
}

func TestForceReviewNeedsAKnownHead(t *testing.T) {
	open, seed := newStoreAt(t, "new") // o/r#1 has no head SHA
	withOpenSeams(t, linkTestConfig(), open)
	if _, err := runGroupCmd(t, forceReviewCmd, "pr", "1"); err == nil || !strings.Contains(err.Error(), "no recorded head") {
		t.Errorf("error = %v, want a no-head error", err)
	}
	if _, err := runGroupCmd(t, forceReviewCmd, "pr", "99"); err == nil || !strings.Contains(err.Error(), "does not resolve") {
		t.Errorf("error = %v, want a does-not-resolve error", err)
	}
	if got, _ := seed.ListKVAnnotations("o/r", "pr", "o/r#1"); len(got) != 0 {
		t.Errorf("annotations = %+v, want none", got)
	}
}

// seedForceReviewEntity seeds o/r#1 with a recorded head SHA.
func seedForceReviewEntity(t *testing.T, seed *store.Store, head string) {
	t.Helper()
	if err := seed.UpsertEntity(store.Entity{
		Repo: "o/r", EntityType: "pr", EntityID: "o/r#1", Facts: `{}`, AsOf: "2026-09-29T00:00:00Z", HeadSHA: head,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestForceReviewClearRemovesTheFlagWithOneRecord(t *testing.T) {
	open, seed := newStoreAt(t, "new")
	seedForceReviewEntity(t, seed, "deadbeef")
	withOpenSeams(t, linkTestConfig(), open)

	if _, err := runGroupCmd(t, forceReviewCmd, "pr", "1"); err != nil {
		t.Fatalf("force-review: %v", err)
	}
	out, err := runGroupCmd(t, forceReviewCmd, "pr", "1", "--clear", "--origin", "pr-decider", "--actor", "bot")
	if err != nil {
		t.Fatalf("force-review --clear: %v", err)
	}
	if !strings.Contains(out, "cleared") {
		t.Errorf("output = %q, want it to say the flag was cleared", out)
	}
	if _, found, _ := seed.GetKVAnnotation("o/r", "pr", "o/r#1", "force_review"); found {
		t.Error("force_review still present after --clear")
	}
	// set + clear: exactly one more annotation_changed record, from --origin.
	if got, want := kindsOf(t, seed, "pr", "o/r#1"), []string{"annotation_changed@pg-desk", "annotation_changed@pr-decider"}; !equalStrings(got, want) {
		t.Errorf("history = %v, want %v", got, want)
	}
}

func TestForceReviewClearDefaultsToTheDeskOrigin(t *testing.T) {
	open, seed := newStoreAt(t, "new")
	seedForceReviewEntity(t, seed, "deadbeef")
	withOpenSeams(t, linkTestConfig(), open)
	if _, err := runGroupCmd(t, forceReviewCmd, "pr", "1"); err != nil {
		t.Fatal(err)
	}
	if _, err := runGroupCmd(t, forceReviewCmd, "pr", "1", "--clear"); err != nil {
		t.Fatal(err)
	}
	if got, want := kindsOf(t, seed, "pr", "o/r#1"), []string{"annotation_changed@pg-desk", "annotation_changed@pg-desk"}; !equalStrings(got, want) {
		t.Errorf("history = %v, want %v", got, want)
	}
}

func TestForceReviewClearWithNoFlagIsANoOp(t *testing.T) {
	open, seed := newStoreAt(t, "new") // o/r#1 has no head and no flag
	withOpenSeams(t, linkTestConfig(), open)
	out, err := runGroupCmd(t, forceReviewCmd, "pr", "1", "--clear", "--origin", "pr-decider")
	if err != nil || !strings.Contains(out, "nothing to do") {
		t.Errorf("clear with no flag: out=%q err=%v, want exit 0 and nothing to do", out, err)
	}
	if got := kindsOf(t, seed, "pr", "o/r#1"); len(got) != 0 {
		t.Errorf("history = %v, want no record", got)
	}
}

func TestForceReviewClearNeedsAnActorAndAResolvableEntity(t *testing.T) {
	open, _ := newStoreAt(t, "new")
	cfg := linkTestConfig()
	cfg.Actor = ""
	withOpenSeams(t, cfg, open)
	if _, err := runGroupCmd(t, forceReviewCmd, "pr", "1", "--clear"); err == nil || !strings.Contains(err.Error(), "no actor") {
		t.Errorf("error = %v, want a no-actor error", err)
	}
	withOpenSeams(t, linkTestConfig(), open)
	if _, err := runGroupCmd(t, forceReviewCmd, "pr", "99", "--clear"); err == nil || !strings.Contains(err.Error(), "does not resolve") {
		t.Errorf("error = %v, want a does-not-resolve error", err)
	}
}

func TestForceReviewSetRejectsOriginFlag(t *testing.T) {
	open, _ := newStoreAt(t, "new")
	withOpenSeams(t, linkTestConfig(), open)
	if _, err := runGroupCmd(t, forceReviewCmd, "pr", "1", "--origin", "x"); err == nil || !strings.Contains(err.Error(), "--clear") {
		t.Errorf("error = %v, want --origin refused without --clear", err)
	}
}

func TestForceReviewClearRefusesOldSchemaStore(t *testing.T) {
	open, _ := newStoreAt(t, "old")
	withOpenSeams(t, linkTestConfig(), open)
	_, err := runGroupCmd(t, forceReviewCmd, "pr", "1", "--clear")
	wantOldSchemaRefusal(t, err)
}

// The verbs with no old-schema counterpart each refuse an old-schema store,
// each with its own test.

func TestAnnotateRefusesOldSchemaStore(t *testing.T) {
	open, _ := newStoreAt(t, "old")
	withOpenSeams(t, linkTestConfig(), open)
	_, err := runGroupCmd(t, newAnnotateCmd, "pr", "1", "--key", "free", "--value", "x")
	wantOldSchemaRefusal(t, err)
}

func TestSuppressAndUnsuppressRefuseOldSchemaStore(t *testing.T) {
	open, _ := newStoreAt(t, "old")
	withOpenSeams(t, linkTestConfig(), open)
	_, err := runGroupCmd(t, suppressCmd, "pr", "1", "--kind", "lint")
	wantOldSchemaRefusal(t, err)
	_, err = runGroupCmd(t, unsuppressCmd, "pr", "1", "--kind", "lint")
	wantOldSchemaRefusal(t, err)
}

func TestForceReviewRefusesOldSchemaStore(t *testing.T) {
	open, _ := newStoreAt(t, "old")
	withOpenSeams(t, linkTestConfig(), open)
	_, err := runGroupCmd(t, forceReviewCmd, "pr", "1")
	wantOldSchemaRefusal(t, err)
}

func TestHiddenValueShape(t *testing.T) {
	for _, tc := range []struct {
		hidden bool
		reason string
		want   string
	}{
		{true, "noisy", `{"value":true,"reason":"noisy"}`},
		{true, "", `{"value":true,"reason":null}`},
		{false, "", `{"value":false,"reason":null}`},
	} {
		got := hiddenValue(tc.hidden, tc.reason)
		if got != tc.want {
			t.Errorf("hiddenValue(%v,%q) = %s, want %s", tc.hidden, tc.reason, got, tc.want)
		}
		if err := validateAnnotationValue("hidden", got); err != nil {
			t.Errorf("hiddenValue %s fails its own validation: %v", got, err)
		}
		var v map[string]any
		if err := json.Unmarshal([]byte(got), &v); err != nil {
			t.Errorf("hiddenValue %s is not JSON: %v", got, err)
		}
	}
}

func TestAnnotateRemoveDeletesAnExistingKeyWithOneChangeRecord(t *testing.T) {
	open, seed := seedLinkStore(t, [2]string{"pr", "o/r#5"})
	withOpenSeams(t, linkTestConfig(), open)
	if _, err := runGroupCmd(t, newAnnotateCmd, "pr", "5", "--key", "focus_selected", "--value", "2026-09-23"); err != nil {
		t.Fatal(err)
	}
	before := kindsOf(t, seed, "pr", "o/r#5")

	out, err := runGroupCmd(t, newAnnotateCmd, "pr", "5", "--key", "focus_selected", "--remove", "--origin", "pg-desk")
	if err != nil {
		t.Fatalf("annotate --remove: %v", err)
	}
	if !strings.Contains(out, "removed") {
		t.Errorf("output = %q, want it to say removed", out)
	}
	if _, found, err := seed.GetKVAnnotation("o/r", "pr", "o/r#5", "focus_selected"); err != nil || found {
		t.Errorf("annotation after --remove: found=%v err=%v, want it gone", found, err)
	}
	wantOneAnnotationChange(t, seed, "pr", "o/r#5", before, "pg-desk")
}

func TestAnnotateRemoveOfAnUnsetKeyDoesNothing(t *testing.T) {
	open, seed := seedLinkStore(t, [2]string{"pr", "o/r#5"})
	withOpenSeams(t, linkTestConfig(), open)
	before := kindsOf(t, seed, "pr", "o/r#5")

	out, err := runGroupCmd(t, newAnnotateCmd, "pr", "5", "--key", "focus_selected", "--remove")
	if err != nil {
		t.Fatalf("annotate --remove of an unset key: %v, want exit 0", err)
	}
	if !strings.Contains(out, "nothing to do") {
		t.Errorf("output = %q, want it to say there was nothing to do", out)
	}
	if got := kindsOf(t, seed, "pr", "o/r#5"); !equalStrings(got, before) {
		t.Errorf("history = %v, want %v (no record for an unset key)", got, before)
	}
}

func TestAnnotateRemoveConflictsWithValueAndNeedsOneOfThem(t *testing.T) {
	open, seed := seedLinkStore(t, [2]string{"pr", "o/r#5"})
	withOpenSeams(t, linkTestConfig(), open)
	if _, err := runGroupCmd(t, newAnnotateCmd, "pr", "5", "--key", "free", "--value", "x"); err != nil {
		t.Fatal(err)
	}
	before := kindsOf(t, seed, "pr", "o/r#5")

	if _, err := runGroupCmd(t, newAnnotateCmd, "pr", "5", "--key", "free", "--value", "x", "--remove"); err == nil {
		t.Error("--remove with --value: error = nil, want a usage error")
	}
	if _, err := runGroupCmd(t, newAnnotateCmd, "pr", "5", "--key", "free"); err == nil {
		t.Error("neither --value nor --remove: error = nil, want a usage error")
	}
	wantAnnotation(t, seed, "pr", "o/r#5", "free", "x", "pg-desk", "alice")
	if got := kindsOf(t, seed, "pr", "o/r#5"); !equalStrings(got, before) {
		t.Errorf("history = %v, want %v (a rejected call writes nothing)", got, before)
	}
}
