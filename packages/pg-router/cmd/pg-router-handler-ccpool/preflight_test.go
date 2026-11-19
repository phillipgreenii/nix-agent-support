package main

import (
	"context"
	"errors"
	"testing"
)

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

// Regression for pg2-hc67 (ported alongside precheck itself): precheck must
// pass from a monorepo worktree/slot that has NO local .beads dir, as long
// as bd resolves the store there.
func TestPrecheck_passesWithoutLocalBeadsDir(t *testing.T) {
	br := fakeBR{out: map[string]string{
		"list --limit 1 --json":   "[]",
		"config get issue_prefix": "zr",
	}}
	if err := precheck(context.Background(), "/Volumes/acme/slot-a", "zr", br); err != nil {
		t.Errorf("precheck should pass without a local .beads dir; got %v", err)
	}
}

func TestPrecheck_bdUnreachable(t *testing.T) {
	if err := precheck(context.Background(), "/x", "zr", fakeBR{err: errors.New("bd down")}); err == nil {
		t.Error("unreachable bd should fail precheck")
	}
}

// An unset (empty) expected prefix skips the assertion entirely: bd is not even
// asked for the store's prefix, so a store with any prefix (or none) passes.
func TestPrecheckPrefix_unsetSkipsAssertion(t *testing.T) {
	for name, br := range map[string]fakeBR{
		"other prefix": {out: map[string]string{"config get issue_prefix": "anything"}},
		"bd errors":    {err: errors.New("bd boom")},
	} {
		if err := precheckPrefix(context.Background(), br, ""); err != nil {
			t.Errorf("%s: unset prefix should skip the assertion, got %v", name, err)
		}
	}
}

func TestPrecheck_unsetPrefixPassesAndSetPrefixStillEnforced(t *testing.T) {
	br := fakeBR{out: map[string]string{
		"list --limit 1 --json":   "[]",
		"config get issue_prefix": "pfx",
	}}
	if err := precheck(context.Background(), "/x", "", br); err != nil {
		t.Errorf("unset prefix should pass precheck, got %v", err)
	}
	if err := precheck(context.Background(), "/x", "pfx", br); err != nil {
		t.Errorf("matching set prefix should pass precheck, got %v", err)
	}
	if err := precheck(context.Background(), "/x", "other", br); err == nil {
		t.Error("a set prefix that differs from the store's must still fail precheck")
	}
}
