package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pr-pool/internal/prpoolacl"
)

// stubBD is a no-op beads.Runner: every command returns the canned out/err. Used
// to drive reconcileACL's exit-code policy without a real bd.
type stubBD struct {
	out string
	err error
}

func (s stubBD) Run(_ context.Context, _ ...string) (string, error) { return s.out, s.err }

// TestReconcileACL_PgPrUnreachableExitsZero: if `pg-pr pr list` fails, the ACL
// step logs and treats it as zero PRs, returning exitOK — a non-zero here would
// abort a following drain's discovery (H6).
func TestReconcileACL_PgPrUnreachableExitsZero(t *testing.T) {
	var buf bytes.Buffer
	listFn := func(context.Context, string) ([]prpoolacl.PR, error) {
		return nil, errors.New("pg-pr: executable file not found")
	}
	code := reconcileACL(context.Background(), &buf, stubBD{}, "/repo", listFn)
	if code != exitOK {
		t.Fatalf("exit = %d, want exitOK(%d) when pg-pr is unreachable", code, exitOK)
	}
}

// TestReconcileACL_EmptyExitsZero: no open PRs -> exitOK, nothing ensured.
func TestReconcileACL_EmptyExitsZero(t *testing.T) {
	var buf bytes.Buffer
	listFn := func(context.Context, string) ([]prpoolacl.PR, error) { return nil, nil }
	code := reconcileACL(context.Background(), &buf, stubBD{}, "/repo", listFn)
	if code != exitOK {
		t.Fatalf("exit = %d, want exitOK", code)
	}
}

// TestReconcileACL_StalePRsExitZeroAndEnsureNothing: when pg-pr's rows are past
// their freshness bound the ACL refuses to act on them, but the PASS still exits
// 0 with nothing ensured — a freshness refusal must not be escalated into a
// non-zero that would abort the following drain's discovery (H6), because it is a
// transient "sync is behind" condition that self-heals.
func TestReconcileACL_StalePRsExitZeroAndEnsureNothing(t *testing.T) {
	var buf bytes.Buffer
	listFn := func(context.Context, string) ([]prpoolacl.PR, error) {
		return []prpoolacl.PR{{
			Repo: "o/r", Number: 7, State: "open", Ownership: "mine",
			LastSyncedAt: time.Now().UTC().Add(-time.Hour).Format(time.RFC3339),
			Stale:        true,
		}}, nil
	}
	// stubBD returns "" for every bd command, so the bead snapshots decode as
	// empty; the stale PR is filtered before any of that matters.
	code := reconcileACL(context.Background(), &buf, stubBD{out: `{"data":[]}`}, "/repo", listFn)
	if code != exitOK {
		t.Fatalf("exit = %d, want exitOK(%d) when every PR is stale", code, exitOK)
	}
	if !strings.Contains(buf.String(), "no review-pr") {
		t.Errorf("expected an empty-result line when every PR is refused, got %q", buf.String())
	}
}

func TestRenderACLResult(t *testing.T) {
	var buf bytes.Buffer
	renderACLResult(&buf, []string{"zr-rv7", "zr-rv9"})
	out := buf.String()
	if !strings.Contains(out, "zr-rv7") || !strings.Contains(out, "zr-rv9") {
		t.Errorf("expected both review ids in output: %q", out)
	}

	var empty bytes.Buffer
	renderACLResult(&empty, nil)
	if !strings.Contains(empty.String(), "no review-pr") {
		t.Errorf("expected an empty-result line, got %q", empty.String())
	}
}
