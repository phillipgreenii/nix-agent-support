package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/claudecodeadapter"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmddesc"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/effectpolicy"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/evalcontract"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/goldencorpus"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

// TestCorpusRowCWD_WorkspaceMarkers pins how `evaluate --corpus` handles a row
// whose verdict depends on the working directory being a trusted exec location
// (effectpolicy.TrustedCheckoutExec): a row naming workspace_markers gets a
// real, throwaway directory carrying those marker files in place of its
// synthetic cwd, and a row without them keeps the synthetic cwd untouched.
func TestCorpusRowCWD_WorkspaceMarkers(t *testing.T) {
	plain := goldencorpus.PathStateFixture{CWD: "/repo", ProjectRoot: "/repo"}
	cwd, root, cleanup, err := corpusRowCWD(plain)
	if err != nil {
		t.Fatal(err)
	}
	cleanup()
	if cwd != "/repo" || root != "/repo" {
		t.Errorf("no markers: got cwd=%q root=%q, want the synthetic /repo untouched", cwd, root)
	}

	marked := goldencorpus.PathStateFixture{CWD: "/repo", ProjectRoot: "/repo", WorkspaceMarkers: []string{"pn-workspace.toml"}}
	cwd, root, cleanup, err = corpusRowCWD(marked)
	if err != nil {
		t.Fatal(err)
	}
	if cwd != root {
		t.Errorf("cwd %q and project root %q must agree", cwd, root)
	}
	info, statErr := os.Stat(cwd)
	if statErr != nil || !info.IsDir() {
		t.Fatalf("materialized cwd %q is not a directory: %v", cwd, statErr)
	}
	// The marker sits ABOVE the cwd (a workforest set directory), not in it.
	if _, err := os.Stat(filepath.Join(cwd, "pn-workspace.toml")); err == nil {
		t.Errorf("marker is in the cwd itself; it must sit above it, like a real workforest set")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(filepath.Dir(cwd)), "pn-workspace.toml")); err != nil {
		t.Errorf("marker file missing above the cwd: %v", err)
	}
	cleanup()
	if _, err := os.Stat(cwd); !os.IsNotExist(err) {
		t.Errorf("cleanup left %q behind", cwd)
	}

	for _, bad := range []string{"", "../escape", "a/b", "/abs"} {
		if _, _, _, err := corpusRowCWD(goldencorpus.PathStateFixture{CWD: "/repo", WorkspaceMarkers: []string{bad}}); err == nil {
			t.Errorf("marker name %q: want a refusal, got nil", bad)
		}
	}
}

// TestCorpusRowCWD_MarkerDecidesExecVerdict is the end-to-end pin: the same
// pn/go/pnwf commands approve from the materialized marker directory and
// abstain from the synthetic cwd (no marker), through the real engine.
func TestCorpusRowCWD_MarkerDecidesExecVerdict(t *testing.T) {
	eval := func(t *testing.T, fx goldencorpus.PathStateFixture, command string) evalcontract.Decision {
		t.Helper()
		cwd, root, cleanup, err := corpusRowCWD(fx)
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
