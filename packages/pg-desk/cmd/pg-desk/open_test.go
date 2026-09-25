package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// ----------------------------------------------------------------------
// Filtering (ported from packages/pg-pr/cmd/pg-pr/open_test.go)
// ----------------------------------------------------------------------

func openTestRows() []openRow {
	return []openRow{
		{Number: 1, Owner: "alice", URL: "u1", NeedsAttention: true, MatchReason: []string{"review-requested"}},
		{Number: 2, Owner: "bob", URL: "u2", NeedsAttention: false, MatchReason: []string{"team-authored"}},
		{Number: 3, Owner: "alice", URL: "u3", NeedsAttention: true, HumanApprovers: 1, MatchReason: []string{"label:team/lbl-one"}},
		{Number: 4, Owner: "carol", URL: "u4", NeedsAttention: true, MatchReason: []string{"label:team/lbl-two"}},
	}
}

func numbersOf(rows []openRow) []int {
	out := make([]int, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Number)
	}
	return out
}

func assertNumbers(t *testing.T, got []openRow, want ...int) {
	t.Helper()
	gotNums := numbersOf(got)
	if len(gotNums) != len(want) {
		t.Fatalf("selected %v, want %v", gotNums, want)
	}
	for i := range want {
		if gotNums[i] != want[i] {
			t.Fatalf("selected %v, want %v", gotNums, want)
		}
	}
}

func TestSelectRows(t *testing.T) {
	tests := []struct {
		name  string
		flags openFlags
		want  []int
	}{
		{"defaults to needs-attention", openFlags{}, []int{1, 3, 4}},
		{"--all keeps everything", openFlags{all: true}, []int{1, 2, 3, 4}},
		{"--reason exact", openFlags{all: true, reason: "review-requested"}, []int{1}},
		{"--reason prefix matches the label family", openFlags{all: true, reason: "label:"}, []int{3, 4}},
		{"--owner", openFlags{all: true, owner: "alice"}, []int{1, 3}},
		{"--not-owner", openFlags{all: true, notOwner: "alice"}, []int{2, 4}},
		{"--unapproved drops human-approved", openFlags{all: true, unapproved: true}, []int{1, 2, 4}},
		{"filters compose", openFlags{owner: "alice", unapproved: true}, []int{1}},
		{"no match yields empty, not nil-panic", openFlags{all: true, owner: "nobody"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertNumbers(t, selectRows(openTestRows(), tt.flags), tt.want...)
		})
	}
}

func TestAttentionOnlyDefaultsPerSource(t *testing.T) {
	tests := []struct {
		name  string
		flags openFlags
		want  bool
	}{
		{"team default narrows to attention", openFlags{}, true},
		{"mine default shows everything", openFlags{mine: true}, false},
		{"--all widens team", openFlags{all: true}, false},
		{"--needs-attention narrows mine", openFlags{mine: true, needsAttention: true}, true},
		{"--all is a no-op for mine", openFlags{mine: true, all: true}, false},
		{"--needs-attention is a no-op for team", openFlags{needsAttention: true}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := attentionOnly(tt.flags); got != tt.want {
				t.Errorf("attentionOnly(%+v) = %v, want %v", tt.flags, got, tt.want)
			}
		})
	}
}

func TestSelectRowsMineDefaultKeepsUnactionableRows(t *testing.T) {
	rows := []openRow{
		{Number: 11, URL: "u11", NeedsAttention: false},
		{Number: 12, URL: "u12", NeedsAttention: false},
		{Number: 13, URL: "u13", NeedsAttention: true},
	}
	assertNumbers(t, selectRows(rows, openFlags{mine: true}), 11, 12, 13)
	assertNumbers(t, selectRows(rows, openFlags{mine: true, needsAttention: true}), 13)
	// Team with the same rows still narrows — the fix must not leak across.
	assertNumbers(t, selectRows(rows, openFlags{}), 13)
}

func TestSelectRowsPreservesOrder(t *testing.T) {
	rows := []openRow{
		{Number: 9, NeedsAttention: true},
		{Number: 1, NeedsAttention: true},
		{Number: 5, NeedsAttention: true},
	}
	assertNumbers(t, selectRows(rows, openFlags{}), 9, 1, 5)
}

func TestSelectRowsHiddenExcludedByDefault_EvenWithAll(t *testing.T) {
	rows := []openRow{
		{Number: 1, NeedsAttention: true},
		{Number: 2, NeedsAttention: true, Hidden: true, HiddenReason: "noisy"},
	}
	assertNumbers(t, selectRows(rows, openFlags{}), 1)
	assertNumbers(t, selectRows(rows, openFlags{all: true}), 1)
	assertNumbers(t, selectRows(rows, openFlags{all: true, includeHidden: true}), 1, 2)
}

func TestSelectRowsPromotable(t *testing.T) {
	rows := []openRow{
		{Number: 1, NeedsAttention: true, ReadyToPromote: true},
		{Number: 2, NeedsAttention: true, ReadyToPromote: false},
	}
	assertNumbers(t, selectRows(rows, openFlags{all: true, promotable: true}), 1)
}

func TestSelectRowsUnapprovedKeysOnHumanApprovers(t *testing.T) {
	rows := []openRow{
		{Number: 1, NeedsAttention: true},
		{Number: 2, NeedsAttention: true, HumanApprovers: 1},
		{Number: 3, NeedsAttention: true, HumanApprovers: 2},
	}
	assertNumbers(t, selectRows(rows, openFlags{unapproved: true}), 1)
}

func TestHiddenCell(t *testing.T) {
	tests := []struct {
		name string
		row  openRow
		want string
	}{
		{"not hidden", openRow{}, "-"},
		{"hidden with reason", openRow{Hidden: true, HiddenReason: "noisy"}, "noisy"},
		{"hidden with no reason", openRow{Hidden: true}, "hidden"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hiddenCell(tt.row); got != tt.want {
				t.Errorf("hiddenCell() = %q, want %q", got, tt.want)
			}
		})
	}
}

// ----------------------------------------------------------------------
// Flag validation (ported verbatim)
// ----------------------------------------------------------------------

func TestValidateOpenFlagsRejectsTeamOnlyFlagsWithMine(t *testing.T) {
	tests := []struct {
		name  string
		flags openFlags
		names string
	}{
		{"--reason", openFlags{mine: true, reason: "team-authored"}, "--reason"},
		{"--owner", openFlags{mine: true, owner: "alice"}, "--owner"},
		{"--not-owner", openFlags{mine: true, notOwner: "alice"}, "--not-owner"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateOpenFlags(tt.flags)
			if err == nil {
				t.Fatalf("validateOpenFlags(%+v) = nil, want a usage error", tt.flags)
			}
			if !strings.Contains(err.Error(), tt.names) {
				t.Errorf("error %q does not name the offending flag %q", err, tt.names)
			}
		})
	}
}

func TestValidateOpenFlagsRejectsContradictoryAttentionFlags(t *testing.T) {
	err := validateOpenFlags(openFlags{all: true, needsAttention: true})
	if err == nil {
		t.Fatal("--all with --needs-attention = nil, want a usage error")
	}
	if !strings.Contains(err.Error(), "--all") || !strings.Contains(err.Error(), "--needs-attention") {
		t.Errorf("error %q does not name both flags", err)
	}
}

func TestValidateOpenFlagsAllowsValidCombinations(t *testing.T) {
	for _, f := range []openFlags{
		{},
		{mine: true},
		{mine: true, needsAttention: true},
		{mine: true, all: true},
		{mine: true, unapproved: true, max: 5},
		{all: true, owner: "alice", reason: "label:"},
		{needsAttention: true, owner: "alice"},
	} {
		if err := validateOpenFlags(f); err != nil {
			t.Errorf("validateOpenFlags(%+v) = %v, want nil", f, err)
		}
	}
}

func TestOpenFlagValidation_JSONCombinations(t *testing.T) {
	err := validateOpenFlags(openFlags{jsonOutput: true, printOnly: true})
	if err == nil {
		t.Fatal("validateOpenFlags(--json, --print) = nil, want a usage error")
	}
	if !strings.Contains(err.Error(), "--json") || !strings.Contains(err.Error(), "--print") {
		t.Errorf("error %q does not name both flags", err)
	}
}

// ----------------------------------------------------------------------
// Rendering (ported verbatim)
// ----------------------------------------------------------------------

func TestRenderOpenRowsHyperlinksTheTitle(t *testing.T) {
	rows := []openRow{{Number: 7, Owner: "alice", Title: "fix the thing", URL: "https://example.test/pull/7", CIStatus: "success", FilesChanged: 3, LinesChanged: 40}}
	var buf bytes.Buffer
	if err := renderOpenRows(&buf, rows, true); err != nil {
		t.Fatalf("renderOpenRows() error = %v", err)
	}
	out := buf.String()

	if !strings.Contains(out, "\x1b]8;;https://example.test/pull/7\x1b\\fix the thing\x1b]8;;\x1b\\") {
		t.Errorf("title is not wrapped in an OSC 8 hyperlink:\n%q", out)
	}
	if !strings.Contains(out, "#7") || !strings.Contains(out, "3f/40L") {
		t.Errorf("row cells missing:\n%s", out)
	}
}

func TestRenderOpenRowsURLColumnOnlyWhenNotHyperlinked(t *testing.T) {
	rows := []openRow{{Number: 7, Owner: "alice", Title: "fix the thing", URL: "https://example.test/pull/7"}}

	var linked bytes.Buffer
	if err := renderOpenRows(&linked, rows, true); err != nil {
		t.Fatalf("renderOpenRows(link) error = %v", err)
	}
	if strings.Contains(linked.String(), "URL") {
		t.Errorf("hyperlinked layout must not spend a column repeating the link target:\n%s", linked.String())
	}

	var plain bytes.Buffer
	if err := renderOpenRows(&plain, rows, false); err != nil {
		t.Fatalf("renderOpenRows(plain) error = %v", err)
	}
	if !strings.Contains(plain.String(), "URL") || !strings.Contains(plain.String(), "https://example.test/pull/7") {
		t.Errorf("plain layout must carry a bare, greppable URL:\n%s", plain.String())
	}
}

func TestApprovedAndSizeCells(t *testing.T) {
	tests := []struct {
		name     string
		row      openRow
		approved string
		size     string
	}{
		{"neither", openRow{}, "-", "-"},
		{"human only", openRow{HumanApprovers: 1}, "human", "-"},
		{"sized", openRow{FilesChanged: 171, LinesChanged: 556}, "-", "171f/556L"},
		{"two humans", openRow{HumanApprovers: 2}, "human(2)", "-"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := approvedCell(tt.row); got != tt.approved {
				t.Errorf("approvedCell() = %q, want %q", got, tt.approved)
			}
			if got := sizeCell(tt.row); got != tt.size {
				t.Errorf("sizeCell() = %q, want %q", got, tt.size)
			}
		})
	}
}

func TestUseHyperlinksOffForNonTerminal(t *testing.T) {
	var buf bytes.Buffer
	if isTTY(&buf) {
		t.Error("isTTY(bytes.Buffer) = true, want false")
	}
	if useHyperlinks(&buf, openFlags{}) {
		t.Error("useHyperlinks(bytes.Buffer) = true, want false")
	}
	if useHyperlinks(&buf, openFlags{noHyperlinks: true}) {
		t.Error("--no-hyperlinks must stay off")
	}
}

// ----------------------------------------------------------------------
// Command behaviour, against a fixture store
// ----------------------------------------------------------------------

// seedOpenPR writes one PR's entity + interpretation rows (and, if hidden,
// its annotation row) directly to st, for open's own end-to-end tests.
func seedOpenPR(t *testing.T, st *store.Store, repo string, number int, title, url, author, ownership, panel string, humanApprovers int, matchReasons []string, hidden bool, readyToPromote bool) {
	t.Helper()
	id := numToID(number)
	facts := map[string]any{
		"number": number, "title": title, "url": url, "author": author,
		"additions": 10, "deletions": 5,
	}
	factsRaw, err := json.Marshal(map[string]any{"pr_show": facts})
	if err != nil {
		t.Fatalf("marshal facts: %v", err)
	}
	if err := st.UpsertEntity(store.Entity{
		Repo: repo, EntityType: entityTypePR, EntityID: id,
		Facts: string(factsRaw), AsOf: "2026-09-16T00:00:00Z",
	}); err != nil {
		t.Fatalf("upsert entity: %v", err)
	}

	approvalsRaw, _ := json.Marshal(map[string]any{"human_approvers": humanApprovers})
	matchRaw, _ := json.Marshal(matchReasons)
	if err := st.UpsertInterpretation(store.Interpretation{
		Repo: repo, EntityType: entityTypePR, EntityID: id,
		Ownership: ownership, Approvals: string(approvalsRaw),
		MatchReasons: string(matchRaw), Panel: panel,
		ReadyToPromote: readyToPromote, AsOf: "2026-09-16T00:00:00Z",
	}); err != nil {
		t.Fatalf("upsert interpretation: %v", err)
	}

	if hidden {
		h := true
		if err := st.UpsertAnnotation(store.Annotation{
			Repo: repo, EntityType: entityTypePR, EntityID: id,
			Hidden: &h, HiddenReason: "duplicate work", SetBy: "operator", SetAt: "2026-09-16T00:00:00Z",
		}); err != nil {
			t.Fatalf("upsert annotation: %v", err)
		}
	}
}

func numToID(n int) string {
	return strconv.Itoa(n)
}

func openTestConfig(repo string) *config.Config {
	return &config.Config{
		SelfLogin: "me",
		Repos:     []config.RepoConfig{{Remote: repo}},
	}
}

// openTestStore opens a store under a fresh temp directory for direct
// fixture seeding/assertions (the returned seed), plus an openFresh func
// that reopens a NEW connection to that SAME on-disk file every call —
// mirroring production's deskStoreOpen (store.Open(store.DefaultPath())),
// since every command's RunE unconditionally defers Close() on whatever
// deskStoreOpen returns. Returning one shared *store.Store across
// multiple command invocations within a single test would let the first
// invocation's own defer close it out from under the second — a
// test-only "database is closed" artifact, not a production bug.
func openTestStore(t *testing.T) (seed *store.Store, openFresh func() (*store.Store, error)) {
	t.Helper()
	store.SetSynchronousForTests("OFF")
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, func() (*store.Store, error) { return store.Open(path) }
}

// withOpenSeams stubs deskConfigLoad/deskStoreOpen for the duration of one
// test, restoring both on cleanup. openFresh is normally openTestStore's
// second return value; doctor's own tests (which never open a store) pass
// nil.
func withOpenSeams(t *testing.T, cfg *config.Config, openFresh func() (*store.Store, error)) {
	t.Helper()
	origConfigLoad := deskConfigLoad
	origStoreOpen := deskStoreOpen
	t.Cleanup(func() {
		deskConfigLoad = origConfigLoad
		deskStoreOpen = origStoreOpen
	})
	deskConfigLoad = func(ctx context.Context) (*config.Config, error) { return cfg, nil }
	deskStoreOpen = openFresh
}

func runOpenCmd(t *testing.T, f openFlags) (stdout, stderr string, err error) {
	t.Helper()
	origFlags := opFlags
	t.Cleanup(func() { opFlags = origFlags })
	opFlags = f

	var outBuf, errBuf bytes.Buffer
	c, _, ferr := rootCmd.Find([]string{"open"})
	if ferr != nil {
		t.Fatalf("rootCmd has no open subcommand: %v", ferr)
	}
	c.SetContext(context.Background())
	c.SetOut(&outBuf)
	c.SetErr(&errBuf)

	err = c.RunE(c, nil)
	return outBuf.String(), errBuf.String(), err
}

func TestOpenCmdPrintListsTeamAwaitingMeByDefault(t *testing.T) {
	st, openFresh := openTestStore(t)
	cfg := openTestConfig("o/r")
	withOpenSeams(t, cfg, openFresh)

	seedOpenPR(t, st, "o/r", 1, "one", "https://example.test/pull/1", "alice", "team", panelTeamAwaitingMe, 0, []string{"review-requested"}, false, false)
	seedOpenPR(t, st, "o/r", 2, "two", "https://example.test/pull/2", "bob", "team", panelTeamAwaitingOwner, 0, []string{"team-authored"}, false, false)

	stdout, _, err := runOpenCmd(t, openFlags{printOnly: true})
	if err != nil {
		t.Fatalf("RunE() error = %v", err)
	}
	if !strings.Contains(stdout, "#1") {
		t.Errorf("stdout missing the act-now PR:\n%s", stdout)
	}
	if strings.Contains(stdout, "#2") {
		t.Errorf("default team selection must exclude a non-act-now PR:\n%s", stdout)
	}
}

// TestOpenCmdAllOpensTeamAwaitingTeamToo proves panelTeamAwaitingTeam rows are
// admitted to the team candidate set (so --all can surface them) while the
// default attention-only filter still excludes them — the exact admission
// gap that let a panelTeamAwaitingTeam PR go missing from `open` entirely,
// even with --all.
func TestOpenCmdAllOpensTeamAwaitingTeamToo(t *testing.T) {
	st, openFresh := openTestStore(t)
	cfg := openTestConfig("o/r")
	withOpenSeams(t, cfg, openFresh)

	seedOpenPR(t, st, "o/r", 1, "one", "https://example.test/pull/1", "alice", "team", panelTeamAwaitingMe, 0, []string{"review-requested"}, false, false)
	seedOpenPR(t, st, "o/r", 2, "two", "https://example.test/pull/2", "bob", "team", panelTeamAwaitingTeam, 0, []string{"team-authored"}, false, false)

	stdout, _, err := runOpenCmd(t, openFlags{printOnly: true})
	if err != nil {
		t.Fatalf("RunE() error = %v", err)
	}
	if !strings.Contains(stdout, "#1") {
		t.Errorf("stdout missing the act-now PR:\n%s", stdout)
	}
	if strings.Contains(stdout, "#2") {
		t.Errorf("default (attention-only) team selection must exclude an awaiting_team PR:\n%s", stdout)
	}

	stdoutAll, _, err := runOpenCmd(t, openFlags{all: true, printOnly: true})
	if err != nil {
		t.Fatalf("RunE() --all error = %v", err)
	}
	if !strings.Contains(stdoutAll, "#1") || !strings.Contains(stdoutAll, "#2") {
		t.Errorf("--all must admit the awaiting_team PR alongside the awaiting_me one:\n%s", stdoutAll)
	}
}

func TestOpenCmdMineAloneOpensAllOfMyPRs(t *testing.T) {
	st, openFresh := openTestStore(t)
	cfg := openTestConfig("o/r")
	withOpenSeams(t, cfg, openFresh)

	seedOpenPR(t, st, "o/r", 11, "eleven", "https://example.test/pull/11", "me", "mine", panelMineAwaitingTeam, 0, nil, false, false)
	seedOpenPR(t, st, "o/r", 12, "twelve", "https://example.test/pull/12", "me", "mine", panelMineAwaitingMe, 0, nil, false, false)

	stdout, _, err := runOpenCmd(t, openFlags{mine: true, printOnly: true})
	if err != nil {
		t.Fatalf("RunE() error = %v", err)
	}
	if !strings.Contains(stdout, "#11") || !strings.Contains(stdout, "#12") {
		t.Errorf("--mine alone must open every mine-panel PR:\n%s", stdout)
	}
}

func TestOpenCmdHiddenExcludedByDefault_IncludedWithFlag(t *testing.T) {
	st, openFresh := openTestStore(t)
	cfg := openTestConfig("o/r")
	withOpenSeams(t, cfg, openFresh)

	seedOpenPR(t, st, "o/r", 1, "one", "https://example.test/pull/1", "alice", "team", panelTeamAwaitingMe, 0, []string{"review-requested"}, false, false)
	seedOpenPR(t, st, "o/r", 2, "two", "https://example.test/pull/2", "bob", "team", panelTeamAwaitingMe, 0, []string{"review-requested"}, true, false)

	stdout, _, err := runOpenCmd(t, openFlags{printOnly: true})
	if err != nil {
		t.Fatalf("RunE() error = %v", err)
	}
	if strings.Contains(stdout, "#2") {
		t.Errorf("hidden PR must be excluded by default:\n%s", stdout)
	}

	stdoutAll, _, err := runOpenCmd(t, openFlags{all: true, printOnly: true})
	if err != nil {
		t.Fatalf("RunE() --all error = %v", err)
	}
	if strings.Contains(stdoutAll, "#2") {
		t.Errorf("--all must not readmit the hidden PR:\n%s", stdoutAll)
	}

	stdoutIncl, _, err := runOpenCmd(t, openFlags{all: true, includeHidden: true, printOnly: true})
	if err != nil {
		t.Fatalf("RunE() --include-hidden error = %v", err)
	}
	if !strings.Contains(stdoutIncl, "#2") || !strings.Contains(stdoutIncl, "duplicate work") {
		t.Errorf("--include-hidden must surface the hidden PR and its reason:\n%s", stdoutIncl)
	}
}

func TestOpenCmdEmptySelectionExitsZeroWithoutBrowser(t *testing.T) {
	_, openFresh := openTestStore(t)
	cfg := openTestConfig("o/r")
	withOpenSeams(t, cfg, openFresh)

	stdout, _, err := runOpenCmd(t, openFlags{})
	if err != nil {
		t.Fatalf("RunE() error = %v, want nil", err)
	}
	if !strings.Contains(stdout, "no PRs match") {
		t.Errorf("stdout = %q, want a no-match message", stdout)
	}
}

func TestOpenCmdRejectsInvalidFlagCombinationBeforeStoreOpen(t *testing.T) {
	origStoreOpen := deskStoreOpen
	t.Cleanup(func() { deskStoreOpen = origStoreOpen })
	called := false
	deskStoreOpen = func() (*store.Store, error) { called = true; return nil, nil }

	_, _, err := runOpenCmd(t, openFlags{mine: true, owner: "alice"})
	if err == nil {
		t.Fatal("RunE() error = nil, want a usage error")
	}
	if !strings.Contains(err.Error(), "--owner") {
		t.Errorf("error %q does not name the offending flag", err)
	}
	if called {
		t.Error("deskStoreOpen must not be called when flag validation fails")
	}
}
