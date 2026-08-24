package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/phillipgreenii/pr-pool/internal/config"
)

func TestParseSelfLogin(t *testing.T) {
	got, err := parseSelfLogin([]byte(`{"self_login":"phillipg","worktree_root":"/x"}`))
	if err != nil || got != "phillipg" {
		t.Fatalf("got %q err %v", got, err)
	}
}

func TestParseSelfLogin_empty(t *testing.T) {
	if _, err := parseSelfLogin([]byte(`{"self_login":""}`)); err == nil {
		t.Error("empty self_login should error")
	}
}

// fakeBR is a fake beads.Runner: it returns canned output keyed by joined args,
// or a single error for every call.
type fakeBR struct {
	out map[string]string
	err error
}

func (f fakeBR) Run(_ context.Context, args ...string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return f.out[strings.Join(args, " ")], nil
}

func TestReadBeadsPrefix_fromBd(t *testing.T) {
	br := fakeBR{out: map[string]string{"config get issue_prefix": "zr\n"}}
	got, err := readBeadsPrefix(context.Background(), br)
	if err != nil {
		t.Fatalf("readBeadsPrefix error: %v", err)
	}
	if got != "zr" {
		t.Errorf("readBeadsPrefix = %q, want zr", got)
	}
}

func TestReadBeadsPrefix_bdError(t *testing.T) {
	if _, err := readBeadsPrefix(context.Background(), fakeBR{err: errors.New("bd boom")}); err == nil {
		t.Error("bd error should propagate")
	}
}

func TestReadBeadsPrefix_emptyOutput(t *testing.T) {
	if _, err := readBeadsPrefix(context.Background(), fakeBR{out: map[string]string{}}); err == nil {
		t.Error("empty prefix should error")
	}
}

func TestPrecheckPrefix_matchAndMismatch(t *testing.T) {
	br := fakeBR{out: map[string]string{"config get issue_prefix": "zr"}}
	if err := precheckPrefix(context.Background(), br, "zr"); err != nil {
		t.Errorf("matching prefix should pass, got %v", err)
	}
	if err := precheckPrefix(context.Background(), br, "wrong"); err == nil {
		t.Error("prefix mismatch should fail")
	}
}

// Regression for pg2-hc67: precheck must pass from a monorepo worktree/slot that
// has NO local .beads dir, as long as bd resolves the store there.
func TestPrecheck_passesWithoutLocalBeadsDir(t *testing.T) {
	br := fakeBR{out: map[string]string{
		"list --limit 1 --json":   "[]",
		"config get issue_prefix": "zr",
	}}
	cfg := config.Config{RepoRoot: "/Volumes/acme/slot-a", BeadsPrefix: "zr"}
	if err := precheck(context.Background(), cfg, br); err != nil {
		t.Errorf("precheck should pass without a local .beads dir; got %v", err)
	}
}

func TestPrecheck_bdUnreachable(t *testing.T) {
	cfg := config.Config{RepoRoot: "/x", BeadsPrefix: "zr"}
	if err := precheck(context.Background(), cfg, fakeBR{err: errors.New("bd down")}); err == nil {
		t.Error("unreachable bd should fail precheck")
	}
}
