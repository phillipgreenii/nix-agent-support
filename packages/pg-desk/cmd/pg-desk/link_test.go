package main

import (
	"bytes"
	"context"
	"errors"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// runGroupCmd runs a fresh command group built by build(entityType) with
// args (the verb and its arguments).
func runGroupCmd(t *testing.T, build func(string) *cobra.Command, entityType string, args ...string) (stdout string, err error) {
	t.Helper()
	c := build(entityType)
	var buf bytes.Buffer
	c.SetOut(&buf)
	c.SetErr(&bytes.Buffer{})
	c.SetArgs(args)
	c.SilenceUsage = true
	c.SilenceErrors = true
	err = c.ExecuteContext(context.Background())
	return buf.String(), err
}

func runLink(t *testing.T, entityType string, args ...string) (string, error) {
	t.Helper()
	return runGroupCmd(t, newLinkCmd, entityType, args...)
}

// linkTestConfig is a config with an actor, so link verbs need no --actor.
func linkTestConfig() *config.Config {
	cfg := openTestConfig("o/r")
	cfg.Actor = "alice"
	return cfg
}

// seedLinkStore cuts a fresh store over and writes the named entities
// (pr ids "o/r#N", issue ids as given), returning the open func and a seed
// handle for further direct writes.
func seedLinkStore(t *testing.T, ents ...[2]string) (open func() (*store.Store, error), seed *store.Store) {
	t.Helper()
	path := storeAtVersion(t, "new")
	open = func() (*store.Store, error) { return store.Open(path) }
	seed, err := open()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = seed.Close() })
	for _, e := range ents {
		if _, found, gerr := seed.GetEntity("o/r", e[0], e[1]); gerr != nil {
			t.Fatal(gerr)
		} else if found {
			continue
		}
		if _, err := seed.WriteEntityWithLog(
			store.Entity{Repo: "o/r", EntityType: e[0], EntityID: e[1], Facts: `{}`, AsOf: "2026-09-29T00:00:00Z"},
			0, []string{"created"}, "sync", "2026-09-29T10:00:00Z",
		); err != nil {
			t.Fatalf("seed %v: %v", e, err)
		}
	}
	return open, seed
}

// kindsOf lists "kind@origin" for each record of one entity, oldest first.
func kindsOf(t *testing.T, st *store.Store, entityType, id string) []string {
	t.Helper()
	rows, err := st.ListEntityHistory("o/r", entityType, id, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for i := len(rows) - 1; i >= 0; i-- {
		out = append(out, strings.Join(rows[i].Kinds, "+")+"@"+rows[i].Origin)
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestLinkVerbsRegisteredUnderEveryTypeGroup(t *testing.T) {
	for _, typ := range []string{"pr", "issue", "thread"} {
		for _, verb := range []string{"add", "remove"} {
			c, _, err := rootCmd.Find([]string{typ, "link", verb})
			if err != nil || c.Name() != verb || c.Parent().Parent() != typeGroup(typ) {
				t.Errorf("%s link %s not registered: %v, %v", typ, verb, c, err)
			}
		}
	}
}

func TestLinkAddRecordsExternalClaimWithDefaults(t *testing.T) {
	open, seed := seedLinkStore(t, [2]string{"pr", "o/r#5"}, [2]string{"issue", "K-1"})
	withOpenSeams(t, linkTestConfig(), open)

	if _, err := runLink(t, "pr", "add", "5", "issue:K-1", "--reason", "same work"); err != nil {
		t.Fatalf("link add: %v", err)
	}
	links, err := seed.ListXrefLinksFrom("o/r", "pr", "o/r#5")
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 1 {
		t.Fatalf("links = %+v, want exactly one", links)
	}
	l := links[0]
	if l.ToType != "issue" || l.ToID != "K-1" || l.Relation != "references" || l.Origin != "external:alice" ||
		l.Actor != "alice" || l.Reason != "same work" || l.ActedAt == "" {
		t.Errorf("external claim = %+v", l)
	}
}

func TestLinkAddExplicitRelationAndActor(t *testing.T) {
	open, seed := seedLinkStore(t, [2]string{"issue", "K-1"}, [2]string{"issue", "K-2"})
	withOpenSeams(t, linkTestConfig(), open)

	if _, err := runLink(t, "issue", "add", "K-1", "issue:K-2", "--relation", "blocks", "--actor", "bob"); err != nil {
		t.Fatal(err)
	}
	links, _ := seed.ListXrefLinksFrom("o/r", "issue", "K-1")
	if len(links) != 1 || links[0].Relation != "blocks" || links[0].Origin != "external:bob" {
		t.Errorf("links = %+v", links)
	}
}

func TestLinkAddAppendsLinkChangedForBothEntities(t *testing.T) {
	open, seed := seedLinkStore(t, [2]string{"pr", "o/r#5"}, [2]string{"issue", "K-1"})
	withOpenSeams(t, linkTestConfig(), open)

	if _, err := runLink(t, "pr", "add", "5", "issue:K-1"); err != nil {
		t.Fatal(err)
	}
	want := []string{"created@sync", "link_changed@pg-desk"}
	if got := kindsOf(t, seed, "pr", "o/r#5"); !equalStrings(got, want) {
		t.Errorf("pr records = %v, want %v", got, want)
	}
	if got := kindsOf(t, seed, "issue", "K-1"); !equalStrings(got, want) {
		t.Errorf("issue records = %v, want %v", got, want)
	}
}

func TestLinkAddOwnWorkItemAppendsWorkChangedForPR(t *testing.T) {
	open, seed := seedLinkStore(t, [2]string{"pr", "o/r#5"}, [2]string{"issue", "K-1"})
	withOpenSeams(t, linkTestConfig(), open)

	if _, err := runLink(t, "issue", "add", "K-1", "pr:5", "--relation", "work"); err != nil {
		t.Fatal(err)
	}
	if got, want := kindsOf(t, seed, "pr", "o/r#5"), []string{"created@sync", "work_changed@pg-desk"}; !equalStrings(got, want) {
		t.Errorf("pr records = %v, want %v", got, want)
	}
	if got, want := kindsOf(t, seed, "issue", "K-1"), []string{"created@sync", "link_changed@pg-desk"}; !equalStrings(got, want) {
		t.Errorf("issue records = %v, want %v", got, want)
	}
}

func TestLinkAddSkipsRecordForEndWithNoStoredRow(t *testing.T) {
	open, seed := seedLinkStore(t, [2]string{"pr", "o/r#5"}) // issue K-9 never hydrated
	withOpenSeams(t, linkTestConfig(), open)

	if _, err := runLink(t, "pr", "add", "5", "issue:K-9"); err != nil {
		t.Fatalf("link add with an unhydrated end: %v", err)
	}
	if got, want := kindsOf(t, seed, "pr", "o/r#5"), []string{"created@sync", "link_changed@pg-desk"}; !equalStrings(got, want) {
		t.Errorf("pr records = %v, want %v", got, want)
	}
	if got := kindsOf(t, seed, "issue", "K-9"); len(got) != 0 {
		t.Errorf("issue K-9 has no row but got records %v", got)
	}
}

func TestLinkAddOnDerivedLinkRecordsExternalClaimToo(t *testing.T) {
	open, seed := seedLinkStore(t, [2]string{"pr", "o/r#5"}, [2]string{"issue", "K-1"})
	withOpenSeams(t, linkTestConfig(), open)
	if err := seed.ReplaceDerivedXrefs("o/r", "pr", "o/r#5", []store.XrefLink{{
		Repo: "o/r", FromType: "pr", FromID: "o/r#5", ToType: "issue", ToID: "K-1",
		Relation: "references", Origin: "derived:jira-key",
		FirstSeen: "2026-09-29T00:00:00Z", LastConfirmed: "2026-09-29T00:00:00Z",
	}}); err != nil {
		t.Fatal(err)
	}

	if _, err := runLink(t, "pr", "add", "5", "issue:K-1"); err != nil {
		t.Fatal(err)
	}
	links, _ := seed.ListXrefLinksFrom("o/r", "pr", "o/r#5")
	var origins []string
	for _, l := range links {
		origins = append(origins, l.Origin)
	}
	if !equalStrings(origins, []string{"derived:jira-key", "external:alice"}) {
		t.Errorf("origins = %v, want the derived claim kept and the external one added", origins)
	}
	// The external claim is a link change of its own, so both ends are told.
	if got := kindsOf(t, seed, "issue", "K-1"); !equalStrings(got, []string{"created@sync", "link_changed@pg-desk"}) {
		t.Errorf("issue records = %v", got)
	}
}

func TestLinkAddTwiceIsIdempotent(t *testing.T) {
	open, seed := seedLinkStore(t, [2]string{"pr", "o/r#5"}, [2]string{"issue", "K-1"})
	withOpenSeams(t, linkTestConfig(), open)
	for i := 0; i < 2; i++ {
		if _, err := runLink(t, "pr", "add", "5", "issue:K-1", "--reason", "r"); err != nil {
			t.Fatal(err)
		}
	}
	if got := kindsOf(t, seed, "pr", "o/r#5"); len(got) != 2 {
		t.Errorf("pr records = %v, want the second add to record nothing new", got)
	}
}

func TestLinkRemoveRemovesOnlyActorsExternalRowsInEitherDirection(t *testing.T) {
	open, seed := seedLinkStore(t, [2]string{"pr", "o/r#5"}, [2]string{"issue", "K-1"})
	withOpenSeams(t, linkTestConfig(), open)
	if err := seed.ReplaceDerivedXrefs("o/r", "pr", "o/r#5", []store.XrefLink{{
		Repo: "o/r", FromType: "pr", FromID: "o/r#5", ToType: "issue", ToID: "K-1",
		Relation: "references", Origin: "derived:jira-key",
		FirstSeen: "2026-09-29T00:00:00Z", LastConfirmed: "2026-09-29T00:00:00Z",
	}}); err != nil {
		t.Fatal(err)
	}
	// alice: one forward (pr->issue, two relations) and one reverse (issue->pr); bob: one.
	for _, a := range [][]string{
		{"pr", "add", "5", "issue:K-1"},
		{"pr", "add", "5", "issue:K-1", "--relation", "fixes"},
		{"issue", "add", "K-1", "pr:5", "--relation", "mentions"},
		{"pr", "add", "5", "issue:K-1", "--relation", "blocks", "--actor", "bob"},
	} {
		if _, err := runLink(t, a[0], a[1:]...); err != nil {
			t.Fatalf("%v: %v", a, err)
		}
	}

	out, err := runLink(t, "issue", "remove", "K-1", "pr:5")
	if err != nil {
		t.Fatalf("link remove: %v", err)
	}
	if !strings.Contains(out, "removed 3") {
		t.Errorf("output = %q, want it to report 3 removed", out)
	}
	var left []string
	pair, _ := seed.ListXrefLinksFrom("o/r", "pr", "o/r#5")
	rev, _ := seed.ListXrefLinksFrom("o/r", "issue", "K-1")
	for _, l := range append(pair, rev...) {
		left = append(left, l.Origin+"/"+l.Relation)
	}
	sort.Strings(left)
	if !equalStrings(left, []string{"derived:jira-key/references", "external:bob/blocks"}) {
		t.Errorf("left = %v, want only the derived row and bob's row", left)
	}
}

func TestLinkRemoveAppendsRecordsForBothEntities(t *testing.T) {
	open, seed := seedLinkStore(t, [2]string{"pr", "o/r#5"}, [2]string{"issue", "K-1"})
	withOpenSeams(t, linkTestConfig(), open)
	if _, err := runLink(t, "pr", "add", "5", "issue:K-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := runLink(t, "pr", "remove", "5", "issue:K-1"); err != nil {
		t.Fatal(err)
	}
	want := []string{"created@sync", "link_changed@pg-desk", "link_changed@pg-desk"}
	for _, e := range [][2]string{{"pr", "o/r#5"}, {"issue", "K-1"}} {
		if got := kindsOf(t, seed, e[0], e[1]); !equalStrings(got, want) {
			t.Errorf("%s records = %v, want %v", e[0], got, want)
		}
	}
}

func TestLinkRemoveOwnWorkItemAppendsWorkChanged(t *testing.T) {
	open, seed := seedLinkStore(t, [2]string{"pr", "o/r#5"}, [2]string{"issue", "K-1"})
	withOpenSeams(t, linkTestConfig(), open)
	if _, err := runLink(t, "issue", "add", "K-1", "pr:5", "--relation", "work"); err != nil {
		t.Fatal(err)
	}
	if _, err := runLink(t, "issue", "remove", "K-1", "pr:5"); err != nil {
		t.Fatal(err)
	}
	want := []string{"created@sync", "work_changed@pg-desk", "work_changed@pg-desk"}
	if got := kindsOf(t, seed, "pr", "o/r#5"); !equalStrings(got, want) {
		t.Errorf("pr records = %v, want %v", got, want)
	}
}

func TestLinkRemoveDerivedOnlyErrorsAndChangesNothing(t *testing.T) {
	open, seed := seedLinkStore(t, [2]string{"pr", "o/r#5"}, [2]string{"issue", "K-1"})
	withOpenSeams(t, linkTestConfig(), open)
	if err := seed.ReplaceDerivedXrefs("o/r", "pr", "o/r#5", []store.XrefLink{{
		Repo: "o/r", FromType: "pr", FromID: "o/r#5", ToType: "issue", ToID: "K-1",
		Relation: "references", Origin: "derived:jira-key",
		FirstSeen: "2026-09-29T00:00:00Z", LastConfirmed: "2026-09-29T00:00:00Z",
	}}); err != nil {
		t.Fatal(err)
	}

	_, err := runLink(t, "pr", "remove", "5", "issue:K-1")
	if err == nil || !strings.Contains(err.Error(), "derived from jira-key; change the source entity") {
		t.Fatalf("err = %v, want the derived-only refusal", err)
	}
	if links, _ := seed.ListXrefLinksFrom("o/r", "pr", "o/r#5"); len(links) != 1 {
		t.Errorf("derived link disturbed: %+v", links)
	}
	if got := kindsOf(t, seed, "pr", "o/r#5"); len(got) != 1 {
		t.Errorf("a refused remove appended records: %v", got)
	}
}

func TestLinkRemoveOtherActorsExternalLinkIsNotTheirs(t *testing.T) {
	open, seed := seedLinkStore(t, [2]string{"pr", "o/r#5"}, [2]string{"issue", "K-1"})
	withOpenSeams(t, linkTestConfig(), open)
	if _, err := runLink(t, "pr", "add", "5", "issue:K-1", "--actor", "bob"); err != nil {
		t.Fatal(err)
	}
	_, err := runLink(t, "pr", "remove", "5", "issue:K-1") // alice
	if err == nil || !strings.Contains(err.Error(), "no external link by alice") {
		t.Fatalf("err = %v, want a no-external-link error", err)
	}
	if links, _ := seed.ListXrefLinksFrom("o/r", "pr", "o/r#5"); len(links) != 1 {
		t.Errorf("bob's link was removed: %+v", links)
	}
}

func TestLinkRemoveKeepsDerivedWhenActorHasExternalToo(t *testing.T) {
	open, seed := seedLinkStore(t, [2]string{"pr", "o/r#5"}, [2]string{"issue", "K-1"})
	withOpenSeams(t, linkTestConfig(), open)
	if err := seed.ReplaceDerivedXrefs("o/r", "pr", "o/r#5", []store.XrefLink{{
		Repo: "o/r", FromType: "pr", FromID: "o/r#5", ToType: "issue", ToID: "K-1",
		Relation: "references", Origin: "derived:jira-key",
		FirstSeen: "2026-09-29T00:00:00Z", LastConfirmed: "2026-09-29T00:00:00Z",
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := runLink(t, "pr", "add", "5", "issue:K-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := runLink(t, "pr", "remove", "5", "issue:K-1"); err != nil {
		t.Fatalf("remove of the actor's own external claim on a derived link: %v", err)
	}
	links, _ := seed.ListXrefLinksFrom("o/r", "pr", "o/r#5")
	if len(links) != 1 || links[0].Origin != "derived:jira-key" {
		t.Errorf("links = %+v, want only the derived claim", links)
	}
}

func TestLinkVerbsRefuseOldSchemaStore(t *testing.T) {
	path := storeAtVersion(t, "old")
	withOpenSeams(t, linkTestConfig(), func() (*store.Store, error) { return store.Open(path) })
	for _, args := range [][]string{
		{"add", "1", "issue:K-1"},
		{"remove", "1", "issue:K-1"},
	} {
		t.Run(args[0], func(t *testing.T) {
			out, err := runLink(t, "pr", args...)
			if !errors.Is(err, store.ErrOldSchema) {
				t.Fatalf("err = %v, want store.ErrOldSchema", err)
			}
			if !strings.Contains(err.Error(), "pg-desk migrate --cutover") {
				t.Errorf("refusal text %q lacks pg-desk migrate --cutover", err)
			}
			if out != "" {
				t.Errorf("refusal printed to stdout: %q", out)
			}
		})
	}
}

func TestLinkRejectsBadArguments(t *testing.T) {
	open, _ := seedLinkStore(t, [2]string{"pr", "o/r#5"})
	withOpenSeams(t, linkTestConfig(), open)
	for name, args := range map[string][]string{
		"no other ref":      {"add", "5"},
		"no colon":          {"add", "5", "K-1"},
		"unknown type":      {"add", "5", "bead:K-1"},
		"empty id":          {"add", "5", "issue:"},
		"self link":         {"add", "5", "pr:5"},
		"empty relation":    {"add", "5", "issue:K-1", "--relation", ""},
		"remove no colon":   {"remove", "5", "K-1"},
		"remove self":       {"remove", "5", "pr:o/r#5"},
		"remove unknown ty": {"remove", "5", "x:1"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := runLink(t, "pr", args...); err == nil {
				t.Errorf("link %v accepted", args)
			}
		})
	}
}

func TestLinkRequiresAnActor(t *testing.T) {
	open, _ := seedLinkStore(t, [2]string{"pr", "o/r#5"})
	withOpenSeams(t, openTestConfig("o/r"), open) // no actor configured
	for _, verb := range []string{"add", "remove"} {
		_, err := runLink(t, "pr", verb, "5", "issue:K-1")
		if err == nil || !strings.Contains(err.Error(), "no actor") {
			t.Errorf("%s without an actor: err = %v", verb, err)
		}
	}
}

func TestParseTypedRefSplitsAtFirstColon(t *testing.T) {
	typ, ref, err := parseTypedRef("thread:C123/1759.0001:x")
	if err != nil || typ != "thread" || ref != "C123/1759.0001:x" {
		t.Errorf("parseTypedRef = %q, %q, %v", typ, ref, err)
	}
}
