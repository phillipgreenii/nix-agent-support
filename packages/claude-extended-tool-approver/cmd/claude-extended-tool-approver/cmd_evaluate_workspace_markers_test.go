package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/claudecodeadapter"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmddesc"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/effectpolicy"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/evalcontract"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/goldencorpus"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

// TestMaterialize_MarkerDecidesExecVerdict is the end-to-end pin: the same
// pn/go/pnwf commands approve from the materialized marker directory and
// abstain from the synthetic cwd (no marker), through the real engine.
func TestMaterialize_MarkerDecidesExecVerdict(t *testing.T) {
	eval := func(t *testing.T, fx goldencorpus.PathStateFixture, command string) evalcontract.Decision {
		t.Helper()
		cwd, root, cleanup, err := fx.Materialize()
		if err != nil {
			t.Fatal(err)
		}
		defer cleanup()
		raw, err := json.Marshal(hookio.BashToolInput{Command: command})
		if err != nil {
			t.Fatal(err)
		}
		resp, err := claudecodeadapter.Evaluate(&hookio.HookInput{CWD: cwd, ToolName: "Bash", ToolInput: raw},
			claudecodeadapter.RequestConfig{ProjectRoot: root}, cmddesc.DefaultRegistry(), effectpolicy.DefaultPolicies(), effectpolicy.DefaultGraphPolicies())
		if err != nil {
			t.Fatal(err)
		}
		return resp.Decision
	}
	marked := goldencorpus.PathStateFixture{CWD: "/repo", ProjectRoot: "/repo", WorkspaceMarkers: []string{"pn-workspace.toml"}}
	unmarked := goldencorpus.PathStateFixture{CWD: "/repo", ProjectRoot: "/repo"}
	for _, c := range []string{"pn workspace build", "pnwf sync-fetch --set", "go test ./..."} {
		if got := eval(t, marked, c); got != evalcontract.Approve {
			t.Errorf("%q in a marker directory: got %s, want approve", c, got)
		}
		if got := eval(t, unmarked, c); got != evalcontract.Abstain {
			t.Errorf("%q with no marker: got %s, want abstain", c, got)
		}
	}
}

// TestLegacyCorpusMarkerRowsGradeCorrect grades the CHECKED-IN generated
// corpus_legacy.json (pg2-82oo9): every pnwf/pn row the legacy oracle expects
// to approve carries workspace_markers, replays from a real marker directory,
// and approves, and no such row the oracle does not approve does. A stale
// regeneration (rows back on the synthetic /repo cwd) fails here.
func TestLegacyCorpusMarkerRowsGradeCorrect(t *testing.T) {
	rows, err := loadCorpusRows([]string{"../../internal/goldencorpus/testdata/corpus_legacy.json"})
	if err != nil {
		t.Fatal(err)
	}
	reg, policies, graphPolicies := cmddesc.DefaultRegistry(), effectpolicy.DefaultPolicies(), effectpolicy.DefaultGraphPolicies()
	marked, approving := 0, 0
	for _, row := range rows {
		cmd, _ := row.ToolInput.ToolInput["command"].(string)
		first, _, _ := strings.Cut(strings.TrimSpace(cmd), " ")
		if row.ToolInput.ToolName != "Bash" || (first != "pn" && first != "pnwf") {
			continue
		}
		if len(row.CWDPathState.WorkspaceMarkers) == 0 {
			t.Errorf("case %q: pn/pnwf row without workspace_markers (regenerate with cmd/legacyextract)", row.Case)
			continue
		}
		marked++
		cwd, root, cleanup, err := row.CWDPathState.Materialize()
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(hookio.BashToolInput{Command: cmd})
		if err != nil {
			cleanup()
			t.Fatal(err)
		}
		resp, err := claudecodeadapter.Evaluate(&hookio.HookInput{CWD: cwd, ToolName: "Bash", ToolInput: raw},
			claudecodeadapter.RequestConfig{ProjectRoot: root}, reg, policies, graphPolicies)
		cleanup()
		if err != nil {
			t.Fatalf("case %q: %v", row.Case, err)
		}
		if row.ExpectedVerdict == goldencorpus.Approve {
			approving++
			if resp.Decision != evalcontract.Approve {
				t.Errorf("case %q (%q): legacy oracle approves, got %s: %s", row.Case, cmd, resp.Decision, resp.Reason)
			}
		} else if resp.Decision == evalcontract.Approve && !strings.Contains(cmd, "land-plan") {
			// land-plan is the one pre-existing, marker-independent
			// legacy-vs-new disagreement (old not-approve, new approve) in
			// the buildtools family; see the disposition report.
			t.Errorf("case %q (%q): legacy %s, got approve", row.Case, cmd, row.ExpectedVerdict)
		}
	}
	if marked == 0 || approving < 5 {
		t.Fatalf("marked=%d approving=%d; the guard would be vacuous", marked, approving)
	}
}

// TestCorpusFakeWorkspaceRootRowsGradeCorrect pins pg2-ojhog: the corpus.json
// rows whose command names an absolute workspace path use the stable fake root
// /tmp/ceta-corpus-ws (a path no machine has to provide), never a real
// machine's workspace root, and replay to their own expected verdict through
// the real engine. Rows that hard-coded the pre-move root abstained ("path-access
// (zone unknown)") once that directory was gone; the engine was never wrong.
//
// The approval comes from the temp-root zone, not from a marker: the control
// below shows the same command shape under a path in NO zone (/opt) still
// abstains, so the rows keep a real path-access decision to pass.
func TestCorpusFakeWorkspaceRootRowsGradeCorrect(t *testing.T) {
	const fakeRoot = "/tmp/ceta-corpus-ws"
	rows, err := loadCorpusRows([]string{"../../internal/goldencorpus/testdata/corpus.json"})
	if err != nil {
		t.Fatal(err)
	}
	reg, policies, graphPolicies := cmddesc.DefaultRegistry(), effectpolicy.DefaultPolicies(), effectpolicy.DefaultGraphPolicies()
	replay := func(t *testing.T, row goldencorpus.Row, cmd string) evalcontract.Decision {
		t.Helper()
		raw, err := json.Marshal(hookio.BashToolInput{Command: cmd})
		if err != nil {
			t.Fatal(err)
		}
		resp, err := claudecodeadapter.Evaluate(&hookio.HookInput{CWD: row.CWDPathState.CWD, ToolName: "Bash", ToolInput: raw},
			claudecodeadapter.RequestConfig{ProjectRoot: row.CWDPathState.ProjectRoot}, reg, policies, graphPolicies)
		if err != nil {
			t.Fatal(err)
		}
		return resp.Decision
	}
	rooted, controlled := 0, 0
	for _, row := range rows {
		cmd, _ := row.ToolInput.ToolInput["command"].(string)
		if strings.Contains(cmd, "/Users/phillipg/phillipg_mbp") {
			t.Errorf("case %q hard-codes the pre-move workspace root; use %s", row.Case, fakeRoot)
		}
		if !strings.Contains(cmd, fakeRoot) {
			continue
		}
		rooted++
		if got := replay(t, row, cmd); !gradeCorpusRow(row.ExpectedVerdict, got) {
			t.Errorf("case %q: expected %s, replayed %s", row.Case, row.ExpectedVerdict, got)
		}
		if row.ExpectedVerdict == goldencorpus.Approve && strings.Contains(cmd, "-C "+fakeRoot) {
			controlled++
			if got := replay(t, row, strings.ReplaceAll(cmd, fakeRoot, "/opt/ceta-corpus-no-zone")); got == evalcontract.Approve {
				t.Errorf("case %q: approves under a path in no zone (%q); the control is vacuous", row.Case, cmd)
			}
		}
	}
	if rooted < 20 || controlled < 10 {
		t.Fatalf("rooted=%d controlled=%d; the guard would be vacuous", rooted, controlled)
	}
}

// TestLegacyCorpusFakeWorkspaceRootRowsGradeCorrect pins pg2-j6b0b on the
// GENERATED corpus_legacy.json (the extractor re-roots, never a hand edit): no
// row names the pre-move workspace root, the rows that used to quote it replay
// to their own expected verdict through the real engine, and the root they use
// (/opt/ceta-corpus-no-zone) sits in NO zone.
//
// That last property is the point. The legacy `find <root> ...` row is pinned
// not-approve BECAUSE its root is in no configured zone ("zone unknown"). The
// control proves the zone choice is load-bearing: the same command under the
// temp-root zone corpus.json uses (/tmp/ceta-corpus-ws) approves, so re-rooting
// there would have silently flipped this row's meaning rather than spelling.
//
// The four pg-connector EnvVarPrefix rows are NOT asserted to approve: they
// replay abstain ("unmodeled subcommand issue") whatever the root, an
// intentional legacy-oracle disagreement, so only their root is pinned here.
func TestLegacyCorpusFakeWorkspaceRootRowsGradeCorrect(t *testing.T) {
	const (
		oldRoot  = "/Users/phillipg/phillipg_mbp"
		noZone   = "/opt/ceta-corpus-no-zone"
		tempZone = "/tmp/ceta-corpus-ws"
	)
	rows, err := loadCorpusRows([]string{"../../internal/goldencorpus/testdata/corpus_legacy.json"})
	if err != nil {
		t.Fatal(err)
	}
	reg, policies, graphPolicies := cmddesc.DefaultRegistry(), effectpolicy.DefaultPolicies(), effectpolicy.DefaultGraphPolicies()
	replay := func(t *testing.T, row goldencorpus.Row, cmd string) evalcontract.Decision {
		t.Helper()
		raw, err := json.Marshal(hookio.BashToolInput{Command: cmd})
		if err != nil {
			t.Fatal(err)
		}
		resp, err := claudecodeadapter.Evaluate(&hookio.HookInput{CWD: row.CWDPathState.CWD, ToolName: "Bash", ToolInput: raw},
			claudecodeadapter.RequestConfig{ProjectRoot: row.CWDPathState.ProjectRoot}, reg, policies, graphPolicies)
		if err != nil {
			t.Fatal(err)
		}
		return resp.Decision
	}
	rooted, walks := 0, 0
	for _, row := range rows {
		cmd, _ := row.ToolInput.ToolInput["command"].(string)
		if strings.Contains(cmd, oldRoot) || strings.Contains(row.CWDPathState.CWD, oldRoot) {
			t.Errorf("case %q names the pre-move workspace root; regenerate with cmd/legacyextract", row.Case)
		}
		if !strings.Contains(cmd, noZone) {
			continue
		}
		rooted++
		if strings.HasPrefix(cmd, "find "+noZone) {
			walks++
			if got := replay(t, row, cmd); !gradeCorpusRow(row.ExpectedVerdict, got) {
				t.Errorf("case %q: expected %s, replayed %s", row.Case, row.ExpectedVerdict, got)
			}
			if got := replay(t, row, strings.ReplaceAll(cmd, noZone, tempZone)); got != evalcontract.Approve {
				t.Errorf("case %q under %s: got %s, want approve; the no-zone control is vacuous", row.Case, tempZone, got)
			}
		}
	}
	if rooted < 5 || walks < 1 {
		t.Fatalf("rooted=%d walks=%d; the guard would be vacuous", rooted, walks)
	}
}

// TestMaterializeTree_BareCWDAndTokens pins the effective-cwd (bead pg2-xj975)
// extension of the marker fixture: workspace_cwd "bare" evaluates from a
// sibling directory with no marker above it, and the {{WORKSPACE_SET}} /
// {{WORKSPACE_BARE}} tokens in a command expand to the two real directories, so
// a row can `cd` into (or out of) the marker workspace.
func TestMaterializeTree_BareCWDAndTokens(t *testing.T) {
	fx := goldencorpus.PathStateFixture{CWD: "/repo", WorkspaceMarkers: []string{"pn-workspace.toml"}, WorkspaceCWD: "bare"}
	d, err := fx.MaterializeTree()
	if err != nil {
		t.Fatal(err)
	}
	defer d.Cleanup()
	if d.CWD != d.Bare || d.ProjectRoot != d.Bare || d.CWD == d.Set {
		t.Errorf("cwd=%q projectRoot=%q set=%q bare=%q, want the request cwd to be bare", d.CWD, d.ProjectRoot, d.Set, d.Bare)
	}
	for dir := d.Bare; dir != filepath.Dir(dir); dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, "pn-workspace.toml")); err == nil {
			t.Errorf("a marker sits at or above the bare directory (%s)", dir)
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(filepath.Dir(d.Set)), "pn-workspace.toml")); err != nil {
		t.Errorf("marker missing above the set directory: %v", err)
	}
	if got, want := d.Expand("cd {{WORKSPACE_SET}} && cd {{WORKSPACE_BARE}}"), "cd "+d.Set+" && cd "+d.Bare; got != want {
		t.Errorf("expand = %q, want %q", got, want)
	}

	plain, err := goldencorpus.PathStateFixture{CWD: "/repo", ProjectRoot: "/repo"}.MaterializeTree()
	if err != nil {
		t.Fatal(err)
	}
	if got := plain.Expand("cd {{WORKSPACE_SET}}"); got != "cd {{WORKSPACE_SET}}" {
		t.Errorf("a row without markers must not expand tokens, got %q", got)
	}
	if _, err := (goldencorpus.PathStateFixture{CWD: "/repo", WorkspaceMarkers: []string{"pn-workspace.toml"}, WorkspaceCWD: "elsewhere"}).MaterializeTree(); err == nil {
		t.Error("an unknown workspace_cwd must be refused")
	}
}

// TestMaterializeTree_EffectiveCWDDecidesExecVerdict is the end-to-end pin through
// the real engine: from the unmarked sibling, `cd <set> && pn workspace build`
// approves while the bare form and `cd <bare> && ...` do not; from the marked
// set directory, `cd <bare> && go test` stops approving.
func TestMaterializeTree_EffectiveCWDDecidesExecVerdict(t *testing.T) {
	eval := func(t *testing.T, cwdKind, command string) evalcontract.Decision {
		t.Helper()
		d, err := goldencorpus.PathStateFixture{CWD: "/repo", WorkspaceMarkers: []string{"pn-workspace.toml"}, WorkspaceCWD: cwdKind}.MaterializeTree()
		if err != nil {
			t.Fatal(err)
		}
		defer d.Cleanup()
		raw, err := json.Marshal(hookio.BashToolInput{Command: d.Expand(command)})
		if err != nil {
			t.Fatal(err)
		}
		resp, err := claudecodeadapter.Evaluate(&hookio.HookInput{CWD: d.CWD, ToolName: "Bash", ToolInput: raw},
			claudecodeadapter.RequestConfig{ProjectRoot: d.ProjectRoot}, cmddesc.DefaultRegistry(), effectpolicy.DefaultPolicies(), effectpolicy.DefaultGraphPolicies())
		if err != nil {
			t.Fatal(err)
		}
		return resp.Decision
	}
	for _, tc := range []struct {
		from, cmd string
		want      evalcontract.Decision
	}{
		{"bare", "pn workspace build", evalcontract.Abstain},
		{"bare", "cd {{WORKSPACE_SET}} && pn workspace build", evalcontract.Approve},
		{"bare", "cd {{WORKSPACE_BARE}} && pn workspace build", evalcontract.Abstain},
		{"set", "pn workspace build", evalcontract.Approve},
		{"set", "cd {{WORKSPACE_BARE}} && pn workspace build", evalcontract.Abstain},
		{"set", "cd {{WORKSPACE_BARE}} && go test ./...", evalcontract.Abstain},
		{"set", "cd {{WORKSPACE_BARE}}; go test ./...", evalcontract.Abstain},
	} {
		if got := eval(t, tc.from, tc.cmd); got != tc.want {
			t.Errorf("from %s: %q = %s, want %s", tc.from, tc.cmd, got, tc.want)
		}
	}
}
