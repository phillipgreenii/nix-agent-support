package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

var typedOpenTestNow = time.Date(2026, 9, 29, 14, 10, 0, 0, time.UTC)

// typedOpenFixture is a new-schema store plus a frozen clock and a browser
// seam that records the URLs it was asked to open.
type typedOpenFixture struct {
	t      *testing.T
	seed   *store.Store
	opened [][]string
}

func newTypedOpenFixture(t *testing.T) *typedOpenFixture {
	t.Helper()
	open, seed := seedLinkStore(t)
	withOpenSeams(t, openTestConfig("o/r"), open)
	f := &typedOpenFixture{t: t, seed: seed}
	origNow, origWin := typedOpenNow, typedOpenWindow
	t.Cleanup(func() { typedOpenNow, typedOpenWindow = origNow, origWin })
	typedOpenNow = func() time.Time { return typedOpenTestNow }
	typedOpenWindow = func(urls []string, _ string) error {
		f.opened = append(f.opened, urls)
		return nil
	}
	return f
}

func (f *typedOpenFixture) pr(number int, title, ownership, panel string, asOf string, hidden bool) {
	f.t.Helper()
	id := fmt.Sprintf("o/r#%d", number)
	facts := fmt.Sprintf(`{"pr_show":{"number":%d,"title":%q,"url":"https://code.example/o/r/pull/%d","additions":10,"deletions":5,"head_sha":"9f3c1e2aaaa"}}`, number, title, number)
	if err := f.seed.UpsertEntity(store.Entity{Repo: "o/r", EntityType: "pr", EntityID: id, Facts: facts, AsOf: asOf}); err != nil {
		f.t.Fatal(err)
	}
	if err := f.seed.UpsertInterpretation(store.Interpretation{
		Repo: "o/r", EntityType: "pr", EntityID: id, Ownership: ownership,
		Approvals: `{}`, MatchReasons: `["review-requested"]`, Panel: panel, AsOf: asOf,
	}); err != nil {
		f.t.Fatal(err)
	}
	if hidden {
		f.annotate("pr", id, "hidden", `{"value":true,"reason":"noisy"}`)
	}
}

func (f *typedOpenFixture) annotate(typ, id, key, value string) {
	f.t.Helper()
	if err := f.seed.SetAnnotation(store.KVAnnotation{
		Repo: "o/r", EntityType: typ, EntityID: id, Key: key, Value: value,
		Origin: "pg-desk", SetBy: "operator", SetAt: "2026-09-29T14:05:00Z",
	}); err != nil {
		f.t.Fatal(err)
	}
}

func (f *typedOpenFixture) entity(typ, id, facts, asOf string, hidden bool) {
	f.t.Helper()
	if err := f.seed.UpsertEntity(store.Entity{Repo: "o/r", EntityType: typ, EntityID: id, Facts: facts, AsOf: asOf}); err != nil {
		f.t.Fatal(err)
	}
	if hidden {
		f.annotate(typ, id, "hidden", `{"value":true,"reason":null}`)
	}
}

func runTypedOpenCmd(t *testing.T, entityType string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	c := newTypedOpenCmd(entityType)
	var out, errb bytes.Buffer
	c.SetOut(&out)
	c.SetErr(&errb)
	c.SetArgs(args)
	c.SilenceUsage = true
	c.SilenceErrors = true
	err = c.ExecuteContext(context.Background())
	return out.String(), errb.String(), err
}

func TestTypedPROpenListPrintsAsOfStaleness(t *testing.T) {
	f := newTypedOpenFixture(t)
	f.pr(123, "Add retry to client", "team", panelTeamAwaitingMe, "2026-09-29T14:08:00Z", false)
	f.pr(87, "Fix pagination", "team", panelTeamAwaitingMe, "2026-09-29T14:01:00Z", false)
	f.pr(90, "Not for attention", "team", panelTeamAwaitingTeam, "2026-09-29T14:08:00Z", false)

	out, _, err := runTypedOpenCmd(t, "pr", "--print")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "AS_OF") || !strings.Contains(out, "2m ago") || !strings.Contains(out, "9m ago") {
		t.Errorf("listing lacks the as_of staleness column:\n%s", out)
	}
	if strings.Contains(out, "#90") {
		t.Errorf("the default team selection kept a non-attention PR:\n%s", out)
	}
	// Every flag of the old open still applies.
	all, _, err := runTypedOpenCmd(t, "pr", "--print", "--all")
	if err != nil || !strings.Contains(all, "#90") {
		t.Errorf("--all: %v\n%s", err, all)
	}
}

func TestTypedPROpenKeepsEveryOldFlag(t *testing.T) {
	typed := newTypedOpenCmd("pr")
	for _, name := range []string{
		"all", "needs-attention", "mine", "reason", "owner", "not-owner", "unapproved",
		"include-hidden", "promotable", "max", "print", "no-hyperlinks", "json",
	} {
		if typed.Flags().Lookup(name) == nil {
			t.Errorf("typed pr open lacks --%s", name)
		}
		if old := openCmd.Flags().Lookup(name); old == nil || typed.Flags().Lookup(name).DefValue != old.DefValue {
			t.Errorf("--%s default differs from the old open", name)
		}
	}
}

func TestTypedPROpenIDFormIgnoresSelectionFlagsAndHiddenState(t *testing.T) {
	f := newTypedOpenFixture(t)
	f.pr(90, "Not for attention", "team", panelTeamAwaitingTeam, "2026-09-29T14:08:00Z", true)
	out, _, err := runTypedOpenCmd(t, "pr", "o/r#90", "--print")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "#90") || !strings.Contains(out, "2m ago") || strings.Count(out, "\n") != 2 {
		t.Errorf("id form should list exactly the named PR:\n%s", out)
	}
	// A bare number resolves like every other typed verb.
	if out2, _, err := runTypedOpenCmd(t, "pr", "90", "--print"); err != nil || out2 != out {
		t.Errorf("bare number: %v\n%s", err, out2)
	}
	if _, _, err := runTypedOpenCmd(t, "pr", "o/r#404", "--print"); err == nil || !strings.Contains(err.Error(), "does not resolve") {
		t.Errorf("unknown PR: %v", err)
	}
	// Without --print the id form opens that one PR.
	if _, _, err := runTypedOpenCmd(t, "pr", "90"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.opened, [][]string{{"https://code.example/o/r/pull/90"}}) {
		t.Errorf("opened %v", f.opened)
	}
}

func TestTypedPROpenHiddenIsReadFromTheNewSchema(t *testing.T) {
	f := newTypedOpenFixture(t)
	f.pr(1, "visible", "team", panelTeamAwaitingMe, "2026-09-29T14:08:00Z", false)
	f.pr(2, "hidden one", "team", panelTeamAwaitingMe, "2026-09-29T14:08:00Z", true)
	out, _, err := runTypedOpenCmd(t, "pr", "--print")
	if err != nil || !strings.Contains(out, "#1") || strings.Contains(out, "#2") {
		t.Fatalf("default: %v\n%s", err, out)
	}
	out, _, err = runTypedOpenCmd(t, "pr", "--print", "--include-hidden")
	if err != nil || !strings.Contains(out, "#2") || !strings.Contains(out, "noisy") {
		t.Fatalf("--include-hidden: %v\n%s", err, out)
	}
}

func TestTypedPROpenJSONCarriesIDAndAsOf(t *testing.T) {
	f := newTypedOpenFixture(t)
	f.pr(123, "Add retry", "team", panelTeamAwaitingMe, "2026-09-29T14:08:00Z", false)
	out, _, err := runTypedOpenCmd(t, "pr", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(out), &rows); err != nil || len(rows) != 1 {
		t.Fatalf("%v\n%s", err, out)
	}
	r := rows[0]
	if r["id"] != "o/r#123" || r["as_of"] != "2026-09-29T14:08:00Z" || r["age_seconds"] != float64(120) || r["number"] != float64(123) {
		t.Errorf("row = %v", r)
	}
	// The empty selection is a bare [].
	empty, _, err := runTypedOpenCmd(t, "pr", "--json", "--mine")
	if err != nil || strings.TrimSpace(empty) != "[]" {
		t.Errorf("empty: %v %q", err, empty)
	}
}

func TestTypedIssueAndThreadOpenList(t *testing.T) {
	f := newTypedOpenFixture(t)
	f.entity("issue", "bd-2", `{"issue_show":{"id":"bd-2","title":"second","url":"https://tracker.example/bd-2"}}`, "2026-09-29T14:00:00Z", false)
	f.entity("issue", "bd-1", `{"issue_show":{"id":"bd-1","title":"first","url":"https://tracker.example/bd-1"}}`, "2026-09-29T14:09:00Z", false)
	f.entity("issue", "bd-3", `{"issue_show":{"id":"bd-3","title":"hidden","url":"https://tracker.example/bd-3"}}`, "2026-09-29T14:09:00Z", true)
	f.entity("thread", "C1/1.5", `{"thread_show":{"id":"C1/1.5","permalink":"https://chat.example/p15","text":"look at this"}}`, "2026-09-29T08:10:00Z", false)

	out, _, err := runTypedOpenCmd(t, "issue", "--print")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Index(out, "bd-1") > strings.Index(out, "bd-2") || !strings.Contains(out, "1m ago") || !strings.Contains(out, "10m ago") || strings.Contains(out, "bd-3") {
		t.Errorf("issue listing:\n%s", out)
	}
	out, _, err = runTypedOpenCmd(t, "issue", "--print", "--include-hidden")
	if err != nil || !strings.Contains(out, "bd-3") {
		t.Errorf("--include-hidden: %v\n%s", err, out)
	}
	out, _, err = runTypedOpenCmd(t, "thread", "--print")
	if err != nil || !strings.Contains(out, "C1/1.5") || !strings.Contains(out, "6h ago") || !strings.Contains(out, "look at this") {
		t.Errorf("thread listing: %v\n%s", err, out)
	}

	// --max truncates with a warning; the browser form opens the URLs.
	_, stderr, err := runTypedOpenCmd(t, "issue", "--max", "1")
	if err != nil || !strings.Contains(stderr, "showing the first 1") || !reflect.DeepEqual(f.opened, [][]string{{"https://tracker.example/bd-1"}}) {
		t.Errorf("--max: %v stderr %q opened %v", err, stderr, f.opened)
	}

	// The id form handles one entity.
	out, _, err = runTypedOpenCmd(t, "issue", "bd-2", "--json")
	var rows []map[string]any
	if jerr := json.Unmarshal([]byte(out), &rows); err != nil || jerr != nil || len(rows) != 1 || rows[0]["id"] != "bd-2" || rows[0]["age_seconds"] != float64(600) {
		t.Errorf("id form: %v %v\n%s", err, jerr, out)
	}
	if _, _, err := runTypedOpenCmd(t, "issue", "nope", "--print"); err == nil {
		t.Error("unknown issue id succeeded")
	}
}

func TestTypedIssueOpenTakesOnlyGenericFlags(t *testing.T) {
	c := newTypedOpenCmd("issue")
	for _, name := range []string{"max", "print", "json", "include-hidden"} {
		if c.Flags().Lookup(name) == nil {
			t.Errorf("issue open lacks --%s", name)
		}
	}
	for _, name := range []string{"all", "mine", "reason", "owner", "promotable", "no-hyperlinks"} {
		if c.Flags().Lookup(name) != nil {
			t.Errorf("issue open invented/inherited --%s", name)
		}
	}
}

func TestTypedOpenEmptySelectionSaysSo(t *testing.T) {
	newTypedOpenFixture(t)
	for typ, want := range map[string]string{"pr": "(no PRs match)\n", "issue": "(no issues match)\n", "thread": "(no threads match)\n"} {
		out, _, err := runTypedOpenCmd(t, typ, "--print")
		if err != nil || out != want {
			t.Errorf("%s: %v %q", typ, err, out)
		}
	}
}

func TestTypedOpenRefusesOldSchemaStore(t *testing.T) {
	path := storeAtVersion(t, "old")
	withOpenSeams(t, openTestConfig("o/r"), func() (*store.Store, error) { return store.Open(path) })
	for _, typ := range []string{"pr", "issue", "thread"} {
		out, _, err := runTypedOpenCmd(t, typ, "--print")
		if !errors.Is(err, store.ErrOldSchema) || !strings.Contains(err.Error(), "pg-desk migrate --cutover") {
			t.Errorf("%s: err = %v, want the old-schema refusal naming pg-desk migrate --cutover", typ, err)
		}
		if out != "" || exitCodeFor(err) != 1 {
			t.Errorf("%s: stdout %q, exit %d", typ, out, exitCodeFor(err))
		}
	}
}

func TestAgeOf(t *testing.T) {
	for _, tc := range []struct {
		asOf, want string
	}{
		{"2026-09-29T14:09:30Z", "30s ago"},
		{"2026-09-29T14:08:00Z", "2m ago"},
		{"2026-09-29T11:10:00Z", "3h ago"},
		{"2026-09-27T14:10:00Z", "2d ago"},
		{"2026-09-29T15:00:00Z", "0s ago"}, // future clamps to zero
		{"", "-"},
		{"garbage", "-"},
	} {
		if got, _ := ageOf(tc.asOf, typedOpenTestNow); got != tc.want {
			t.Errorf("ageOf(%q) = %q, want %q", tc.asOf, got, tc.want)
		}
	}
}
