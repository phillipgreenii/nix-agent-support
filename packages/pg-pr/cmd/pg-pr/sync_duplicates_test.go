package main

import (
	"bytes"
	"context"
	"encoding/json"
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
		// The population is stated next to the total: an unlabeled number reads as
		// a census and an open-only one decays as beads close (pg2-0z8fw).
		"scanned all statuses (open and closed)",
		"2 excess bead(s) in all statuses (open and closed): 1 merge-request + 1 process-feedback.",
		"This command does NOT change them.",
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

// TestSyncDuplicatesJSONStatesItsPopulation pins the machine-readable half of the
// same guarantee: a consumer diffing totals across runs can see which beads the
// counts cover, so a total is never mistaken for an open-only figure.
func TestSyncDuplicatesJSONStatesItsPopulation(t *testing.T) {
	withDuplicateAuditor(t, &fakeAuditor{
		cycles: []beads.DuplicateProcessingCycles{{
			Key:       "o/r#104236",
			Canonical: beads.ProcessingCycle{ID: "zr-4jpnl", Status: "closed"},
			Excess:    []beads.ProcessingCycle{{ID: "zr-agwaj", Status: "closed"}},
		}},
	})
	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"sync", "duplicates", "--json"})
	t.Cleanup(func() {
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		rootCmd.SetArgs(nil)
		// The flag is a package-level var; leaving it set would silently switch
		// every later human-output test to JSON.
		syncDuplicatesJSON = false
		_ = syncDuplicatesCmd.Flags().Set("json", "false")
	})
	if err := rootCmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("sync duplicates --json: %v", err)
	}
	var got []duplicateReport
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal %q: %v", out.String(), err)
	}
	if len(got) != 1 {
		t.Fatalf("want one workspace report, got %d: %+v", len(got), got)
	}
	if got[0].Population != duplicatePopulation {
		t.Errorf("population = %q, want %q", got[0].Population, duplicatePopulation)
	}
	if len(got[0].ProcessingCycles) != 1 || len(got[0].ProcessingCycles[0].Excess) != 1 {
		t.Errorf("closed duplicate pair missing from the JSON report: %+v", got[0])
	}
}

// TestSyncDuplicatesStatesTheAdjudicationExclusion pins pg2-peyf0's operator-facing
// half. Two things have to be visible or the number is unusable: that the count
// EXCLUDES adjudicated duplicates (so a total that fell is not read as the audit
// having narrowed), and that closing an excess bead is not by itself an
// adjudication — the structural edge is, and without it the count cannot drop,
// which is exactly how the pg2-xqwy6 reconcile closed 201 beads and moved nothing.
func TestSyncDuplicatesStatesTheAdjudicationExclusion(t *testing.T) {
	withDuplicateAuditor(t, &fakeAuditor{
		mrs: []beads.DuplicateMergeRequests{{
			Repo: "o/r", PRNumber: 102096,
			Canonical: beads.MergeRequest{ID: "zr-cvr5v"},
			Excess:    []beads.MergeRequest{{ID: "zr-orr0a"}},
		}},
	})
	got := runDuplicates(t)
	for _, want := range []string{
		"not counted: " + duplicateExclusion,
		"bd dep add <excess-id> <keep-id> -t supersedes",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q; got:\n%s", want, got)
		}
	}
}

// TestSyncDuplicatesJSONStatesTheAdjudicationExclusion is the machine-readable
// half: a consumer diffing totals across runs must be able to tell a drop caused
// by resolved duplicates from a drop caused by a changed audit.
func TestSyncDuplicatesJSONStatesTheAdjudicationExclusion(t *testing.T) {
	withDuplicateAuditor(t, &fakeAuditor{})
	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"sync", "duplicates", "--json"})
	t.Cleanup(func() {
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		rootCmd.SetArgs(nil)
		syncDuplicatesJSON = false
		_ = syncDuplicatesCmd.Flags().Set("json", "false")
	})
	if err := rootCmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("sync duplicates --json: %v", err)
	}
	var got []duplicateReport
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal %q: %v", out.String(), err)
	}
	if len(got) != 1 {
		t.Fatalf("want one workspace report, got %d: %+v", len(got), got)
	}
	if got[0].Exclusion != duplicateExclusion {
		t.Errorf("exclusion = %q, want %q", got[0].Exclusion, duplicateExclusion)
	}
	if got[0].Population != duplicatePopulation {
		t.Errorf("population = %q, want %q — the SCANNED population is unchanged by the exclusion",
			got[0].Population, duplicatePopulation)
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
