package main

import (
	"encoding/json"
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
