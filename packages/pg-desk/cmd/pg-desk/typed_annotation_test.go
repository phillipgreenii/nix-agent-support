package main

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func typedHide(t string) *cobra.Command   { return newTypedHideCmd(t) }
func typedUnhide(t string) *cobra.Command { return newTypedUnhideCmd(t) }
func typedWIP(t string) *cobra.Command    { return newTypedWIPCmd(t) }
func typedFeedback(string) *cobra.Command { return newTypedFeedbackCmd() }

// On a new-schema store the typed hide/unhide/wip write the key/value
// annotations with the exact reserved-key shapes, one annotation_changed
// record each.
func TestTypedHideUnhideWIPWriteKeyValueAnnotationsOnNewSchema(t *testing.T) {
	open, seed := seedLinkStore(t, [2]string{"pr", "o/r#5"}, [2]string{"issue", "K-1"})
	withOpenSeams(t, linkTestConfig(), open)

	if _, err := runGroupCmd(t, typedHide, "pr", "5", "noisy PR"); err != nil {
		t.Fatalf("pr hide: %v", err)
	}
	wantAnnotation(t, seed, "pr", "o/r#5", "hidden", `{"value":true,"reason":"noisy PR"}`, "pg-desk", "alice")
	wantOneAnnotationChange(t, seed, "pr", "o/r#5", []string{"created@sync"}, "pg-desk")

	if _, err := runGroupCmd(t, typedUnhide, "pr", "5"); err != nil {
		t.Fatalf("pr unhide: %v", err)
	}
	wantAnnotation(t, seed, "pr", "o/r#5", "hidden", `{"value":false,"reason":null}`, "pg-desk", "alice")

	if _, err := runGroupCmd(t, typedWIP, "pr", "on", "5"); err != nil {
		t.Fatalf("pr wip on: %v", err)
	}
	wantAnnotation(t, seed, "pr", "o/r#5", "wip", "true", "pg-desk", "alice")
	if _, err := runGroupCmd(t, typedWIP, "pr", "off", "5"); err != nil {
		t.Fatalf("pr wip off: %v", err)
	}
	wantAnnotation(t, seed, "pr", "o/r#5", "wip", "false", "pg-desk", "alice")
	// hide and wip are independent keys.
	wantAnnotation(t, seed, "pr", "o/r#5", "hidden", `{"value":false,"reason":null}`, "pg-desk", "alice")

	// The other types work the same, ids verbatim.
	if _, err := runGroupCmd(t, typedHide, "issue", "K-1"); err != nil {
		t.Fatalf("issue hide: %v", err)
	}
	wantAnnotation(t, seed, "issue", "K-1", "hidden", `{"value":true,"reason":null}`, "pg-desk", "alice")
	wantOneAnnotationChange(t, seed, "issue", "K-1", []string{"created@sync"}, "pg-desk")
}

func TestTypedHideUnknownEntityOnNewSchemaFails(t *testing.T) {
	open, _ := seedLinkStore(t, [2]string{"pr", "o/r#5"})
	withOpenSeams(t, linkTestConfig(), open)
	_, err := runGroupCmd(t, typedHide, "pr", "99")
	if err == nil || !strings.Contains(err.Error(), "does not resolve to a stored entity") {
		t.Errorf("error = %v, want a does-not-resolve error", err)
	}
}

func TestTypedWIPRejectsBadState(t *testing.T) {
	open, _ := seedLinkStore(t, [2]string{"pr", "o/r#5"})
	withOpenSeams(t, linkTestConfig(), open)
	if _, err := runGroupCmd(t, typedWIP, "pr", "maybe", "5"); err == nil {
		t.Error("wip maybe: error = nil")
	}
}

// On an old-schema store the typed pr verbs behave exactly like the
// top-level verbs: they write the old per-column annotation.
func TestTypedPRVerbsMatchTopLevelVerbsOnOldSchema(t *testing.T) {
	open, seed := newStoreAt(t, "old")
	withOpenSeams(t, linkTestConfig(), open)

	if _, err := runGroupCmd(t, typedHide, "pr", "1", "noisy"); err != nil {
		t.Fatalf("pr hide: %v", err)
	}
	ann, found, err := seed.GetPRAnnotation("o/r", entityTypePR, "o/r#1")
	if err != nil || !found || ann.Hidden == nil || !*ann.Hidden || ann.HiddenReason != "noisy" {
		t.Fatalf("after pr hide: found=%v ann=%+v err=%v", found, ann, err)
	}
	if _, err := runGroupCmd(t, typedWIP, "pr", "on", "1"); err != nil {
		t.Fatalf("pr wip on: %v", err)
	}
	ann, _, _ = seed.GetPRAnnotation("o/r", entityTypePR, "o/r#1")
	if ann.WIP == nil || !*ann.WIP || ann.Hidden == nil || !*ann.Hidden {
		t.Fatalf("after pr wip on: ann=%+v, want wip set and hidden preserved", ann)
	}
	if _, err := runGroupCmd(t, typedUnhide, "pr", "1"); err != nil {
		t.Fatalf("pr unhide: %v", err)
	}
	ann, _, _ = seed.GetPRAnnotation("o/r", entityTypePR, "o/r#1")
	if ann.Hidden == nil || *ann.Hidden || ann.HiddenReason != "" || ann.WIP == nil || !*ann.WIP {
		t.Fatalf("after pr unhide: ann=%+v", ann)
	}

	// The old-schema error text of the top-level verb carries through.
	_, err = runGroupCmd(t, typedHide, "pr", "999")
	if err == nil || !strings.Contains(err.Error(), "does not resolve") {
		t.Errorf("pr hide unknown: error = %v, want the top-level does-not-resolve error", err)
	}
}

// issue and thread have no old-schema counterpart.
func TestTypedIssueAndThreadVerbsRefuseOldSchema(t *testing.T) {
	open, _ := newStoreAt(t, "old")
	withOpenSeams(t, linkTestConfig(), open)
	for _, typ := range []string{"issue", "thread"} {
		_, err := runGroupCmd(t, typedHide, typ, "X-1")
		wantOldSchemaRefusal(t, err)
		_, err = runGroupCmd(t, typedUnhide, typ, "X-1")
		wantOldSchemaRefusal(t, err)
		_, err = runGroupCmd(t, typedWIP, typ, "on", "X-1")
		wantOldSchemaRefusal(t, err)
	}
}

func TestTypedFeedbackOnOldSchemaMatchesTopLevel(t *testing.T) {
	open, seed := newStoreAt(t, "old")
	seedFeedbackPR(t, seed, "o/r", "o/r#1", [][2]string{{"c1", dispositionOpen}, {"c2", dispositionNoAction}})
	withOpenSeams(t, linkTestConfig(), open)

	if _, err := runGroupCmd(t, typedFeedback, "pr", "set", "1", "c1", "--disposition", "will-fix", "--actor", "bob"); err != nil {
		t.Fatalf("pr feedback set: %v", err)
	}
	ann, found, err := seed.GetAnnotation("o/r", entityTypePR, "o/r#1", "c1")
	if err != nil || !found || ann.Disposition != "will-fix" || ann.SetBy != "bob" {
		t.Fatalf("old annotation = %+v found=%v err=%v", ann, found, err)
	}
	typed, err := runGroupCmd(t, typedFeedback, "pr", "list", "1")
	if err != nil {
		t.Fatalf("pr feedback list: %v", err)
	}
	top, err := runFeedbackListCmd(t, "1")
	if err != nil {
		t.Fatal(err)
	}
	if typed != top || !strings.Contains(typed, "c1\twill-fix (overridden)") {
		t.Errorf("typed list = %q, top-level list = %q", typed, top)
	}
	if _, err := runGroupCmd(t, typedFeedback, "pr", "set", "1", "nope", "--disposition", "will-fix"); err == nil {
		t.Error("set on unknown comment: error = nil")
	}
	if _, err := runGroupCmd(t, typedFeedback, "pr", "set", "1", "c1", "--disposition", "bogus"); err == nil {
		t.Error("set with bogus disposition: error = nil")
	}
}

func TestTypedFeedbackOnNewSchemaWritesDispositionAnnotations(t *testing.T) {
	open, seed := newStoreAt(t, "new")
	seedFeedbackPR(t, seed, "o/r", "o/r#1", [][2]string{{"c1", dispositionOpen}, {"c2", dispositionNoAction}})
	withOpenSeams(t, linkTestConfig(), open)

	if _, err := runGroupCmd(t, typedFeedback, "pr", "set", "1", "c1", "--disposition", "will-fix", "--actor", "bob"); err != nil {
		t.Fatalf("pr feedback set: %v", err)
	}
	wantAnnotation(t, seed, "pr", "o/r#1", "disposition.c1", "will-fix", "pg-desk", "bob")
	wantOneAnnotationChange(t, seed, "pr", "o/r#1", nil, "pg-desk")

	out, err := runGroupCmd(t, typedFeedback, "pr", "list", "1")
	if err != nil {
		t.Fatalf("pr feedback list: %v", err)
	}
	if out != "c1\twill-fix (overridden)\nc2\tno-action\n" {
		t.Errorf("list = %q", out)
	}

	// Default actor, and an unknown comment writes nothing.
	if _, err := runGroupCmd(t, typedFeedback, "pr", "set", "1", "c2", "--disposition", "wont-fix"); err != nil {
		t.Fatal(err)
	}
	wantAnnotation(t, seed, "pr", "o/r#1", "disposition.c2", "wont-fix", "pg-desk", "alice")
	if _, err := runGroupCmd(t, typedFeedback, "pr", "set", "1", "nope", "--disposition", "will-fix"); err == nil {
		t.Error("set on unknown comment: error = nil")
	}
	if _, err := runGroupCmd(t, typedFeedback, "pr", "set", "1", "c1", "--disposition", "bogus"); err == nil {
		t.Error("set with bogus disposition: error = nil")
	}
	if got, _ := seed.ListKVAnnotations("o/r", "pr", "o/r#1"); len(got) != 2 {
		t.Errorf("annotations = %+v, want exactly the two disposition rows", got)
	}
}
