package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/beads"
)

// fakeAuditor serves canned duplicate reports.
type fakeAuditor struct {
	mrs    []beads.DuplicateMergeRequests
	cycles []beads.DuplicateProcessingCycles
}

func (f *fakeAuditor) FindDuplicateMergeRequests(context.Context) ([]beads.DuplicateMergeRequests, error) {
	return f.mrs, nil
}

func (f *fakeAuditor) FindDuplicateProcessingCycles(context.Context) ([]beads.DuplicateProcessingCycles, error) {
	return f.cycles, nil
}

// withDuplicateAuditor swaps the auditor factory and the config loader for the
// duration of a test.
func withDuplicateAuditor(t *testing.T, a duplicateAuditor) {
	t.Helper()
	prevAuditors, prevCfg := newDuplicateAuditors, loadConfigForCLI
	newDuplicateAuditors = func(*config.Config) []duplicateWorkspace {
		return []duplicateWorkspace{{Path: "/tmp/ws", Auditor: a}}
	}
	loadConfigForCLI = func(context.Context) (*config.Config, error) { return &config.Config{}, nil }
	t.Cleanup(func() { newDuplicateAuditors, loadConfigForCLI = prevAuditors, prevCfg })
}

// runDuplicates executes `pg-pr sync duplicates` and returns its stdout.
func runDuplicates(t *testing.T) string {
	t.Helper()
	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"sync", "duplicates"})
	t.Cleanup(func() {
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		rootCmd.SetArgs(nil)
	})
	if err := rootCmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("sync duplicates: %v", err)
	}
	return out.String()
}

func TestSyncDuplicatesReportsExcessBeads(t *testing.T) {
	withDuplicateAuditor(t, &fakeAuditor{
		mrs: []beads.DuplicateMergeRequests{{
			Repo: "o/r", PRNumber: 102096,
			Canonical: beads.MergeRequest{ID: "zr-cvr5v"},
			Excess:    []beads.MergeRequest{{ID: "zr-orr0a"}},
		}},
		cycles: []beads.DuplicateProcessingCycles{{
			Key:       "o/r#102096",
			Canonical: beads.ProcessingCycle{ID: "zr-2rmnv"},
			Excess:    []beads.ProcessingCycle{{ID: "zr-sl2y9"}},
		}},
	})
	got := runDuplicates(t)
	for _, want := range []string{
		"merge-request o/r#102096: keep zr-cvr5v, excess zr-orr0a",
		"process-feedback o/r#102096: keep zr-2rmnv, excess zr-sl2y9",
		"2 excess bead(s). This command does NOT change them.",
		"bd close <excess-id>",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q; got:\n%s", want, got)
		}
	}
}

func TestSyncDuplicatesCleanWorkspace(t *testing.T) {
	withDuplicateAuditor(t, &fakeAuditor{})
	got := runDuplicates(t)
	if !strings.Contains(got, "no duplicated bead identities") {
		t.Errorf("clean workspace output: %q", got)
	}
	if strings.Contains(got, "bd close") {
		t.Errorf("must not suggest a cleanup when there is nothing to clean: %q", got)
	}
}

// TestSyncDuplicatesHasNoMutatingFlag pins the read-only contract: the audit must
// not grow an --apply/--fix mode. Collapsing existing beads is an
// operator-scheduled migration against a live workspace, not a CLI side effect.
func TestSyncDuplicatesHasNoMutatingFlag(t *testing.T) {
	for _, name := range []string{"apply", "fix", "close", "prune", "reconcile", "force", "yes"} {
		if f := syncDuplicatesCmd.Flags().Lookup(name); f != nil {
			t.Errorf("sync duplicates must stay read-only, but defines --%s", name)
		}
	}
}
