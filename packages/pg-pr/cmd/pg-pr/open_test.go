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
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/snapshot"
	"github.com/spf13/cobra"
)

// ----------------------------------------------------------------------
// Filtering
// ----------------------------------------------------------------------

func openTestRows() []openRow {
	return []openRow{
		{Number: 1, Owner: "alice", URL: "u1", NeedsAttention: true, MatchReason: []string{"review-requested"}},
		{Number: 2, Owner: "bob", URL: "u2", NeedsAttention: false, MatchReason: []string{"team-authored"}},
		{Number: 3, Owner: "alice", URL: "u3", NeedsAttention: true, HumanApproved: true, MatchReason: []string{"label:team/findev"}},
		{Number: 4, Owner: "carol", URL: "u4", NeedsAttention: true, MatchReason: []string{"label:team/jvm-guild"}},
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
		{"--reason prefix narrowed to one label", openFlags{all: true, reason: "label:team/findev"}, []int{3}},
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
		Number: 98622, Owner: "bradleysmagacz", Title: "safe middleware parsing",
		URL: "https://example.test/pull/98622", CIStatus: "failure",
		HumanApproved: true, AgentApproved: false,
		FilesChanged: 171, LinesChanged: 556,
		NeedsAttention: true, MatchReason: []string{"label:team/findev"},
	}}}
	got := projectRows(snap, false)
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	r := got[0]
	if r.Number != 98622 || r.Owner != "bradleysmagacz" || r.URL != "https://example.test/pull/98622" {
		t.Errorf("identity fields lost: %+v", r)
	}
	if !r.NeedsAttention || !r.HumanApproved || r.FilesChanged != 171 || r.LinesChanged != 556 {
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

func TestValidateOpenFlagsAllowsValidCombinations(t *testing.T) {
	for _, f := range []openFlags{
		{},
		{mine: true},
		{mine: true, unapproved: true, max: 5},
		{all: true, owner: "alice", reason: "label:"},
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
	// The URL must appear ONLY inside the escape, never as its own column —
	// otherwise the hyperlinked layout gained a redundant column.
	if strings.Contains(out, "URL") {
		t.Errorf("hyperlinked layout must not carry a URL column:\n%s", out)
	}
	if !strings.Contains(out, "#7") || !strings.Contains(out, "3f/40L") {
		t.Errorf("row cells missing:\n%s", out)
	}
}

func TestRenderOpenRowsPlainKeepsURLGreppable(t *testing.T) {
	rows := []openRow{{Number: 7, Owner: "alice", Title: "fix the thing", URL: "https://example.test/pull/7"}}
	var buf bytes.Buffer
	if err := renderOpenRows(&buf, rows, false); err != nil {
		t.Fatalf("renderOpenRows() error = %v", err)
	}
	out := buf.String()

	if strings.Contains(out, "\x1b") {
		t.Errorf("plain layout must contain no escape sequences:\n%q", out)
	}
	if !strings.Contains(out, "URL") {
		t.Errorf("plain layout must carry a URL column:\n%s", out)
	}
	if !strings.Contains(out, "https://example.test/pull/7") {
		t.Errorf("plain layout must print the bare URL:\n%s", out)
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
		{"human only", openRow{HumanApproved: true}, "human", "-"},
		{"agent only", openRow{AgentApproved: true}, "agent", "-"},
		{"both", openRow{HumanApproved: true, AgentApproved: true}, "human,agent", "-"},
		{"sized", openRow{FilesChanged: 171, LinesChanged: 556}, "-", "171f/556L"},
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

func TestOpenCmdRejectsInvalidFlagCombinationBeforeFetching(t *testing.T) {
	_, _, _, err := runOpenCmd(t, openTestSnapshot(), openFlags{mine: true, owner: "alice"})
	if err == nil {
		t.Fatal("RunE() error = nil, want a usage error")
	}
	if !strings.Contains(err.Error(), "--owner") {
		t.Errorf("error %q does not name the offending flag", err)
	}
}
