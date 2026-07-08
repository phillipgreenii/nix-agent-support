package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

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
