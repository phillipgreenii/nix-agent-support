package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/browser"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/dashboard"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/ownership"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/snapshot"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
	"github.com/spf13/cobra"
)

// ----------------------------------------------------------------------
// Filtering
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
		{"--reason prefix narrowed to one label", openFlags{all: true, reason: "label:team/lbl-one"}, []int{3}},
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

// TestAttentionOnlyDefaultsPerSource is the regression test for the defect the
// operator hit: --mine alone printed "(no PRs match)" because the
// needs-attention default was applied uniformly, and a MineRow's attention
// signal is composed and frequently all-false. The default must differ by
// source, and stay overridable in the direction it forecloses.
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

// TestSelectRowsMineDefaultKeepsUnactionableRows exercises the same fix through
// selectRows, using rows whose attention signal is all-false — the exact shape
// that made --mine look empty.
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

// TestSelectRowsPreservesSnapshotOrder pins that filtering never reorders: the
// daemon's ordering is the review order the operator sees in Grafana, and tab
// order in the opened window must match it.
func TestSelectRowsPreservesSnapshotOrder(t *testing.T) {
	rows := []openRow{
		{Number: 9, NeedsAttention: true},
		{Number: 1, NeedsAttention: true},
		{Number: 5, NeedsAttention: true},
	}
	assertNumbers(t, selectRows(rows, openFlags{}), 9, 1, 5)
}

// ----------------------------------------------------------------------
// Projection
// ----------------------------------------------------------------------

func TestProjectRowsTeamCarriesEveryFilterableField(t *testing.T) {
	snap := &snapshot.Snapshot{Team: []snapshot.TeamRow{{
		Number: 4242, Owner: "teammate", Title: "safe middleware parsing",
		URL: "https://example.test/pull/4242", CIStatus: "failure",
		HumanApproved: true, AgentApproved: false,
		HumanApprovers: 1, AgentApprovers: 0,
		FilesChanged: 171, LinesChanged: 556,
		NeedsAttention: true, MatchReason: []string{"label:team/lbl-one"},
	}}}
	got := projectRows(snap, false)
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	r := got[0]
	if r.Number != 4242 || r.Owner != "teammate" || r.URL != "https://example.test/pull/4242" {
		t.Errorf("identity fields lost: %+v", r)
	}
	if !r.NeedsAttention || r.HumanApprovers != 1 || r.FilesChanged != 171 || r.LinesChanged != 556 {
		t.Errorf("filterable fields lost: %+v", r)
	}
	if !hasReason(r.MatchReason, "label:") {
		t.Errorf("MatchReason lost: %+v", r.MatchReason)
	}
}

// TestProjectRowsMineComposesNeedsAttention pins the mapping documented on
// projectRows: a MineRow has no NeedsAttention field, so it is composed from
// the three signals that mean the operator personally has to act.
func TestProjectRowsMineComposesNeedsAttention(t *testing.T) {
	tests := []struct {
		name string
		row  snapshot.MineRow
		want bool
	}{
		{"waiting on me", snapshot.MineRow{WaitingOnMe: true}, true},
		{"forgot to merge", snapshot.MineRow{NeedsMergeReminder: true}, true},
		{"conflicts only I can fix", snapshot.MineRow{HasConflicts: true}, true},
		{"nothing pending", snapshot.MineRow{}, false},
		{"CI failure alone is not my action", snapshot.MineRow{CIStatus: "failure"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := projectRows(&snapshot.Snapshot{Mine: []snapshot.MineRow{tt.row}}, true)
			if len(got) != 1 {
				t.Fatalf("len = %d, want 1", len(got))
			}
			if got[0].NeedsAttention != tt.want {
				t.Errorf("NeedsAttention = %v, want %v", got[0].NeedsAttention, tt.want)
			}
		})
	}
}

// Both projection halves MUST carry the per-approver counts through
// independently (pg2-4dz88.1.9). The two halves map different row types onto
// openRow with separate literals, so a count dropped from one is invisible in
// the other's tests — and a dropped count renders as `-`, silently claiming
// nobody has approved.
func TestProjectRowsCarryApproverCountsBothHalves(t *testing.T) {
	mine := projectRows(&snapshot.Snapshot{Mine: []snapshot.MineRow{
		{Number: 1, URL: "u1", HumanApprovers: 2, AgentApprovers: 1},
	}}, true)
	if len(mine) != 1 {
		t.Fatalf("mine: len = %d, want 1", len(mine))
	}
	if mine[0].HumanApprovers != 2 || mine[0].AgentApprovers != 1 {
		t.Errorf("mine half: human %d / agent %d, want 2/1", mine[0].HumanApprovers, mine[0].AgentApprovers)
	}
	if got := approvedCell(mine[0]); got != "human(2),agent" {
		t.Errorf("mine half rendered %q, want %q", got, "human(2),agent")
	}

	team := projectRows(&snapshot.Snapshot{Team: []snapshot.TeamRow{
		{Number: 2, URL: "u2", HumanApprovers: 1, AgentApprovers: 2},
	}}, false)
	if len(team) != 1 {
		t.Fatalf("team: len = %d, want 1", len(team))
	}
	if team[0].HumanApprovers != 1 || team[0].AgentApprovers != 2 {
		t.Errorf("team half: human %d / agent %d, want 1/2", team[0].HumanApprovers, team[0].AgentApprovers)
	}
	if got := approvedCell(team[0]); got != "human,agent(2)" {
		t.Errorf("team half rendered %q, want %q", got, "human,agent(2)")
	}
}

// --unapproved drops a PR a HUMAN has already approved, and only that: an
// agent-only approval is not a human one, and a count of zero is not an
// approval at all.
func TestSelectRowsUnapprovedKeysOnHumanApprovers(t *testing.T) {
	rows := []openRow{
		{Number: 1, NeedsAttention: true},                    // nobody approved: kept
		{Number: 2, NeedsAttention: true, HumanApprovers: 1}, // human approved: dropped
		{Number: 3, NeedsAttention: true, HumanApprovers: 2}, // two humans: dropped
		{Number: 4, NeedsAttention: true, AgentApprovers: 1}, // agent only: kept
	}
	got := numbersOf(selectRows(rows, openFlags{unapproved: true}))
	want := []int{1, 4}
	if len(got) != len(want) {
		t.Fatalf("--unapproved kept %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("--unapproved kept %v, want %v", got, want)
		}
	}
}

// TestProjectRowsMineExcludesMergedRows is the pg2-ew4kf regression guard: the
// dashboard snapshot now retains a merged PR of mine for a 24h grace period
// (MineRow.Merged), but `pg-pr open --mine` must never treat one as
// actionable — an already-merged PR has nothing left to open in a browser.
// The exclusion must hold with --all too, since --all bypasses the
// NeedsAttention filter entirely and would otherwise readmit it.
func TestProjectRowsMineExcludesMergedRows(t *testing.T) {
	snap := &snapshot.Snapshot{Mine: []snapshot.MineRow{
		{Number: 1, URL: "u1"},                    // active: kept
		{Number: 2, URL: "u2", Merged: true},      // merged/retained: excluded
		{Number: 3, URL: "u3", WaitingOnMe: true}, // active: kept
	}}
	got := projectRows(snap, true)
	assertNumbers(t, got, 1, 3)

	// --all must not readmit the merged row (it bypasses NeedsAttention, not
	// the projection-level exclusion).
	selected := selectRows(got, openFlags{mine: true, all: true})
	assertNumbers(t, selected, 1, 3)
}

// TestProjectRowsSelectsTheRequestedHalf guards against reading the wrong array.
func TestProjectRowsSelectsTheRequestedHalf(t *testing.T) {
	snap := &snapshot.Snapshot{
		Mine: []snapshot.MineRow{{Number: 11, WaitingOnMe: true}},
		Team: []snapshot.TeamRow{{Number: 22, NeedsAttention: true}},
	}
	assertNumbers(t, projectRows(snap, true), 11)
	assertNumbers(t, projectRows(snap, false), 22)
}

// ----------------------------------------------------------------------
// Flag validation
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

// ----------------------------------------------------------------------
// Rendering
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

// stripOSC8 removes OSC 8 hyperlink escapes, leaving what a terminal that
// ignores them would still put on screen. It is how the tests below assert the
// URL is VISIBLE rather than merely embedded as a link target.
func stripOSC8(s string) string {
	for {
		start := strings.Index(s, "\x1b]8;;")
		if start < 0 {
			return s
		}
		rest := s[start:]
		end := strings.Index(rest, "\x1b\\")
		if end < 0 {
			return s
		}
		s = s[:start] + rest[end+2:]
	}
}

// TestRenderOpenRowsURLColumnOnlyWhenNotHyperlinked pins the layout split: the
// URL column exists exactly when the title is not carrying the link.
//
// It replaces an earlier always-show-the-URL assertion. That one guarded against
// a terminal silently ignoring OSC 8, which would leave nothing clickable and no
// URL to copy — a real failure the operator hit. The premise was then tested
// directly and did not hold (both affordances work), so the redundant column no
// longer earns its width on a terminal. The plain half of this test is what keeps
// the recovery path honest: --no-hyperlinks and redirected output must still
// carry a bare, greppable URL.
func TestRenderOpenRowsURLColumnOnlyWhenNotHyperlinked(t *testing.T) {
	rows := []openRow{{Number: 7, Owner: "alice", Title: "fix the thing", URL: "https://example.test/pull/7"}}

	var linked bytes.Buffer
	if err := renderOpenRows(&linked, rows, true); err != nil {
		t.Fatalf("renderOpenRows(link) error = %v", err)
	}
	out := linked.String()
	if strings.Contains(out, "URL") {
		t.Errorf("hyperlinked layout must not spend a column repeating the link target:\n%s", out)
	}
	// Stripping the escapes must leave NO bare URL — proof the column is gone
	// rather than merely relocated.
	if visible := stripOSC8(out); strings.Contains(visible, "https://example.test/pull/7") {
		t.Errorf("hyperlinked layout still prints the URL outside the escape:\n%q", visible)
	}

	var plain bytes.Buffer
	if err := renderOpenRows(&plain, rows, false); err != nil {
		t.Fatalf("renderOpenRows(plain) error = %v", err)
	}
	out = plain.String()
	if strings.Contains(out, "\x1b") {
		t.Errorf("plain layout must contain no escapes:\n%q", out)
	}
	if !strings.Contains(out, "URL") || !strings.Contains(out, "https://example.test/pull/7") {
		t.Errorf("plain layout must carry a bare, greppable URL — it is the only target there:\n%s", out)
	}
}

// TestApprovedAndSizeCells pins the rendered approval column. The first four
// rows are the BACKWARD-COMPATIBILITY contract for the per-approver cutover
// (pg2-4dz88.1.9): the expected strings are unchanged byte-for-byte from before
// the openRow booleans became counts, so no approver count of 0 or 1 can shift
// the column. Only a class with MORE THAN ONE standing approver renders
// differently, because that case previously had no representation at all.
func TestApprovedAndSizeCells(t *testing.T) {
	tests := []struct {
		name     string
		row      openRow
		approved string
		size     string
	}{
		{"neither", openRow{}, "-", "-"},
		{"human only", openRow{HumanApprovers: 1}, "human", "-"},
		{"agent only", openRow{AgentApprovers: 1}, "agent", "-"},
		{"both", openRow{HumanApprovers: 1, AgentApprovers: 1}, "human,agent", "-"},
		{"sized", openRow{FilesChanged: 171, LinesChanged: 556}, "-", "171f/556L"},
		// Two humans approved: the column says TWO. The old boolean rendered
		// this identically to "human only" above, which is the whole defect.
		{"two humans", openRow{HumanApprovers: 2}, "human(2)", "-"},
		{"two agents", openRow{AgentApprovers: 2}, "agent(2)", "-"},
		{"two of each", openRow{HumanApprovers: 2, AgentApprovers: 2}, "human(2),agent(2)", "-"},
		{"two humans, one agent", openRow{HumanApprovers: 2, AgentApprovers: 1}, "human(2),agent", "-"},
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

// TestApprovedCellTwoApproversOneStale is the CLI end of the leaf's headline
// case, driven from the snapshot rather than a hand-built openRow so the whole
// seam is under test: two approvers on the PR, one of whose approvals no longer
// stands.
//
//   - Both standing → `human(2)`: two distinct approvers, reported as two.
//   - One DISMISSED → `human`: the stale approval drops out, leaving the
//     single-approver rendering exactly as it has always been.
//
// The pre-cutover booleans rendered BOTH fixtures as `human`, so the column
// could not tell "two people approved this" from "one did, and another's
// approval was thrown away".
func TestApprovedCellTwoApproversOneStale(t *testing.T) {
	cell := func(approvals []store.Approval) string {
		snap := snapshot.Build(snapshot.BuilderInput{
			Self:        "me",
			TeamMembers: []string{"author"},
			PRs: []snapshot.PRInput{{
				PR:        api.PR{Repo: "o/r", Number: 1, Author: "author", HeadSHA: "h1"},
				Ownership: ownership.Team,
				Revisions: []store.Revision{{Seq: 1, HeadSHA: "h1"}},
				Approvals: approvals,
			}},
		})
		rows := projectRows(snap, false)
		if len(rows) != 1 {
			t.Fatalf("want 1 projected row, got %d", len(rows))
		}
		return approvedCell(rows[0])
	}

	bothStanding := cell([]store.Approval{
		{Approver: "carol", State: "approved", HeadSHA: "h1"},
		{Approver: "dave", State: "approved", HeadSHA: "h1"},
	})
	if bothStanding != "human(2)" {
		t.Errorf("two standing approvers: approvedCell = %q, want %q", bothStanding, "human(2)")
	}

	oneStale := cell([]store.Approval{
		{Approver: "carol", State: "approved", HeadSHA: "h1"},
		{Approver: "dave", State: "approved", HeadSHA: "h1", Dismissed: true},
	})
	if oneStale != "human" {
		t.Errorf("one stale of two: approvedCell = %q, want %q (byte-identical to the single-approver rendering)",
			oneStale, "human")
	}
}

// TestUseHyperlinksOffForNonTerminal pins the by-construction guarantee that
// redirected output is plain: a bytes.Buffer is not an *os.File, so every
// piped invocation and every test takes the plain path.
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
// Command behaviour
// ----------------------------------------------------------------------

// runOpenCmd drives openCmd.RunE against a daemon serving snap, with the
// browser stubbed. It returns the URLs the browser was asked to open plus the
// command's stdout and stderr.
func runOpenCmd(t *testing.T, snap snapshot.Snapshot, f openFlags) (opened []string, stdout, stderr string, err error) {
	t.Helper()

	raw, marshalErr := json.Marshal(snap)
	if marshalErr != nil {
		t.Fatalf("marshal snapshot: %v", marshalErr)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != dashboard.Path {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(raw)
	}))
	t.Cleanup(srv.Close)

	called := false
	origOpen := browser.OpenWindow
	browser.OpenWindow = func(urls []string) error {
		called = true
		opened = urls
		return nil
	}
	t.Cleanup(func() { browser.OpenWindow = origOpen })

	origFlags := opFlags
	t.Cleanup(func() { opFlags = origFlags })
	f.addr = strings.TrimPrefix(srv.URL, "http://")
	opFlags = f

	var outBuf, errBuf bytes.Buffer
	c := &cobra.Command{}
	c.SetContext(context.Background())
	c.SetOut(&outBuf)
	c.SetErr(&errBuf)

	err = openCmd.RunE(c, nil)
	if !called {
		opened = nil
	}
	return opened, outBuf.String(), errBuf.String(), err
}

func openTestSnapshot() snapshot.Snapshot {
	return snapshot.Snapshot{
		StaleAfterSeconds: 120,
		Team: []snapshot.TeamRow{
			{Number: 1, Owner: "alice", URL: "https://example.test/pull/1", NeedsAttention: true},
			{Number: 2, Owner: "bob", URL: "https://example.test/pull/2", NeedsAttention: false},
			{Number: 3, Owner: "carol", URL: "https://example.test/pull/3", NeedsAttention: true},
		},
	}
}

func TestOpenCmdOpensOnlyNeedsAttentionURLsInOrder(t *testing.T) {
	opened, _, stderr, err := runOpenCmd(t, openTestSnapshot(), openFlags{})
	if err != nil {
		t.Fatalf("RunE() error = %v", err)
	}
	want := []string{"https://example.test/pull/1", "https://example.test/pull/3"}
	if strings.Join(opened, ",") != strings.Join(want, ",") {
		t.Errorf("opened %v, want %v", opened, want)
	}
	if stderr != "" {
		t.Errorf("unexpected stderr: %q", stderr)
	}
}

// TestOpenCmdMaxZeroOpensEverything pins the operator's explicit choice that
// the default is uncapped.
func TestOpenCmdMaxZeroOpensEverything(t *testing.T) {
	opened, _, _, err := runOpenCmd(t, openTestSnapshot(), openFlags{all: true, max: 0})
	if err != nil {
		t.Fatalf("RunE() error = %v", err)
	}
	if len(opened) != 3 {
		t.Errorf("opened %d URLs, want all 3", len(opened))
	}
}

func TestOpenCmdMaxTruncatesAndWarns(t *testing.T) {
	opened, _, stderr, err := runOpenCmd(t, openTestSnapshot(), openFlags{all: true, max: 2})
	if err != nil {
		t.Fatalf("RunE() error = %v", err)
	}
	if len(opened) != 2 {
		t.Fatalf("opened %d URLs, want 2", len(opened))
	}
	if !strings.Contains(stderr, "--max") || !strings.Contains(stderr, "3 PRs matched") {
		t.Errorf("stderr does not report the truncation: %q", stderr)
	}
}

// TestOpenCmdWarnsOnStaleButStillOpens pins the approved staleness policy:
// warn, then proceed — the operator decides whether old data is worth acting on.
func TestOpenCmdWarnsOnStaleButStillOpens(t *testing.T) {
	snap := openTestSnapshot()
	snap.Stale = true
	snap.AgeSeconds = 900

	opened, _, stderr, err := runOpenCmd(t, snap, openFlags{})
	if err != nil {
		t.Fatalf("RunE() error = %v, want nil (stale must not block)", err)
	}
	if len(opened) == 0 {
		t.Error("stale payload must still open the browser")
	}
	if !strings.Contains(stderr, "warning") || !strings.Contains(stderr, "900") {
		t.Errorf("stderr does not warn with the age: %q", stderr)
	}
}

func TestOpenCmdEmptySelectionExitsZeroWithoutBrowser(t *testing.T) {
	snap := snapshot.Snapshot{Team: []snapshot.TeamRow{{Number: 1, URL: "u1", NeedsAttention: false}}}
	opened, stdout, _, err := runOpenCmd(t, snap, openFlags{})
	if err != nil {
		t.Fatalf("RunE() error = %v, want nil", err)
	}
	if opened != nil {
		t.Errorf("browser must not be launched for an empty selection, got %v", opened)
	}
	if !strings.Contains(stdout, "no PRs match") {
		t.Errorf("stdout = %q, want a no-match message", stdout)
	}
}

func TestOpenCmdPrintListsWithoutOpeningBrowser(t *testing.T) {
	opened, stdout, _, err := runOpenCmd(t, openTestSnapshot(), openFlags{printOnly: true})
	if err != nil {
		t.Fatalf("RunE() error = %v", err)
	}
	if opened != nil {
		t.Errorf("--print must not launch a browser, got %v", opened)
	}
	if !strings.Contains(stdout, "#1") || !strings.Contains(stdout, "#3") {
		t.Errorf("stdout does not list the selection:\n%s", stdout)
	}
	if strings.Contains(stdout, "#2") {
		t.Errorf("--print must apply the same filters as opening:\n%s", stdout)
	}
	// stdout is a bytes.Buffer here, so the plain (greppable) layout applies.
	if !strings.Contains(stdout, "https://example.test/pull/1") {
		t.Errorf("stdout does not carry bare URLs:\n%s", stdout)
	}
}

// TestOpenCmdMineAloneOpensAllOfMyPRs is the end-to-end regression test for the
// operator's report: `pg-pr open --mine` returned nothing and they had to type
// `--mine --all`. The fixture's rows are all unactionable, which is the common
// real shape.
func TestOpenCmdMineAloneOpensAllOfMyPRs(t *testing.T) {
	snap := snapshot.Snapshot{Mine: []snapshot.MineRow{
		{Number: 11, URL: "https://example.test/pull/11"},
		{Number: 12, URL: "https://example.test/pull/12", WaitingOnMe: true},
		{Number: 13, URL: "https://example.test/pull/13"},
	}}

	opened, stdout, _, err := runOpenCmd(t, snap, openFlags{mine: true})
	if err != nil {
		t.Fatalf("RunE() error = %v", err)
	}
	if len(opened) != 3 {
		t.Errorf("--mine alone opened %d of 3 PRs (stdout: %q) — it must not need --all", len(opened), stdout)
	}

	narrowed, _, _, err := runOpenCmd(t, snap, openFlags{mine: true, needsAttention: true})
	if err != nil {
		t.Fatalf("RunE() --needs-attention error = %v", err)
	}
	if len(narrowed) != 1 || narrowed[0] != "https://example.test/pull/12" {
		t.Errorf("--mine --needs-attention opened %v, want only the WaitingOnMe PR", narrowed)
	}
}

func TestOpenCmdRejectsInvalidFlagCombinationBeforeFetching(t *testing.T) {
	_, _, _, err := runOpenCmd(t, openTestSnapshot(), openFlags{mine: true, owner: "alice"})
	if err == nil {
		t.Fatal("RunE() error = nil, want a usage error")
	}
	if !strings.Contains(err.Error(), "--owner") {
		t.Errorf("error %q does not name the offending flag", err)
	}
}
