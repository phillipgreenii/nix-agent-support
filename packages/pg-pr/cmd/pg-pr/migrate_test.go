package main

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
)

// fakeFeedbackCloser is a test double for feedbackCloser.
type fakeFeedbackCloser struct {
	ids          []string
	listErr      error
	closeErr     error
	closedIDs    []string
	closeReasons []string
}

func (f *fakeFeedbackCloser) ListFeedbackBeadIDs(_ context.Context) ([]string, error) {
	return f.ids, f.listErr
}

func (f *fakeFeedbackCloser) CloseFeedback(_ context.Context, id, reason string) error {
	if f.closeErr != nil {
		return f.closeErr
	}
	f.closedIDs = append(f.closedIDs, id)
	f.closeReasons = append(f.closeReasons, reason)
	return nil
}

func resetMigrateFlags() {
	mgF = migrateFlags{}
}

func TestMigrateFeedback_ClosesAllBeads(t *testing.T) {
	resetMigrateFlags()
	fake := &fakeFeedbackCloser{ids: []string{"f-1", "f-2", "f-3"}}
	prev := migrateBeadsClient
	t.Cleanup(func() { migrateBeadsClient = prev })
	migrateBeadsClient = func(_ string) feedbackCloser { return fake }

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"migrate-feedback"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("migrate-feedback: %v (stderr=%s)", err, stderr.String())
	}

	if len(fake.closedIDs) != 3 {
		t.Fatalf("expected 3 beads closed, got %d: %v", len(fake.closedIDs), fake.closedIDs)
	}
	// Every close must use the canonical migration reason.
	for i, reason := range fake.closeReasons {
		if reason != "migrated-to-store" {
			t.Errorf("close %d: reason = %q, want migrated-to-store", i, reason)
		}
	}
	// IDs must match what the lister returned.
	for _, want := range []string{"f-1", "f-2", "f-3"} {
		found := false
		for _, got := range fake.closedIDs {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected bead %q to be closed; closed=%v", want, fake.closedIDs)
		}
	}
	// Human-readable output must mention the count.
	out := stdout.String()
	if !strings.Contains(out, "3") {
		t.Errorf("expected count in output, got: %q", out)
	}
}

func TestMigrateFeedback_DryRunClosesNothing(t *testing.T) {
	resetMigrateFlags()
	fake := &fakeFeedbackCloser{ids: []string{"f-1", "f-2"}}
	prev := migrateBeadsClient
	t.Cleanup(func() { migrateBeadsClient = prev })
	migrateBeadsClient = func(_ string) feedbackCloser { return fake }

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"migrate-feedback", "--dry-run"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("migrate-feedback --dry-run: %v (stderr=%s)", err, stderr.String())
	}

	// Nothing should have been closed.
	if len(fake.closedIDs) != 0 {
		t.Fatalf("dry-run must not close beads; got closedIDs=%v", fake.closedIDs)
	}
	// Output must mention the beads that would be closed.
	out := stdout.String()
	if !strings.Contains(out, "f-1") || !strings.Contains(out, "f-2") {
		t.Errorf("dry-run output should list bead ids; got: %q", out)
	}
	if !strings.Contains(out, "dry-run") {
		t.Errorf("dry-run output should mention dry-run; got: %q", out)
	}
}

func TestMigrateFeedback_NoBeads(t *testing.T) {
	resetMigrateFlags()
	fake := &fakeFeedbackCloser{ids: nil}
	prev := migrateBeadsClient
	t.Cleanup(func() { migrateBeadsClient = prev })
	migrateBeadsClient = func(_ string) feedbackCloser { return fake }

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"migrate-feedback"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := stdout.String()
	if !strings.Contains(out, "no legacy") {
		t.Errorf("expected 'no legacy' message, got: %q", out)
	}
}

func TestMigrateFeedback_RepoFlagPassedToClient(t *testing.T) {
	resetMigrateFlags()
	var gotRepo string
	prev := migrateBeadsClient
	t.Cleanup(func() { migrateBeadsClient = prev })
	migrateBeadsClient = func(repo string) feedbackCloser {
		gotRepo = repo
		return &fakeFeedbackCloser{}
	}

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"migrate-feedback", "--repo", "/some/monorepo"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotRepo != "/some/monorepo" {
		t.Errorf("--repo not forwarded; got %q", gotRepo)
	}
}

func TestMigrateFeedback_ListError(t *testing.T) {
	resetMigrateFlags()
	fake := &fakeFeedbackCloser{listErr: fmt.Errorf("bd not found")}
	prev := migrateBeadsClient
	t.Cleanup(func() { migrateBeadsClient = prev })
	migrateBeadsClient = func(_ string) feedbackCloser { return fake }

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"migrate-feedback"})

	if err := rootCmd.Execute(); err == nil {
		t.Fatal("expected error when list fails")
	}
}
