package reviewstage

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func TestResult_SaveLoadClear_Roundtrip(t *testing.T) {
	dir := t.TempDir()
	in := &Result{
		Repo:      "foo/bar",
		PR:        42,
		Ownership: "mine",
		HeadSHA:   "abc123",
		BeadID:    "pg2-x.1",
		Verdict:   "comment",
	}
	path, err := SaveResult(dir, in)
	if err != nil {
		t.Fatalf("SaveResult: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("stat: %v", err)
	}

	got, err := LoadResult(dir, "foo/bar", 42)
	if err != nil {
		t.Fatalf("LoadResult: %v", err)
	}
	if got.Repo != "foo/bar" || got.PR != 42 || got.Ownership != "mine" ||
		got.HeadSHA != "abc123" || got.BeadID != "pg2-x.1" || got.Verdict != "comment" {
		t.Fatalf("roundtrip mismatch: %+v", got)
	}

	if err := ClearResult(dir, "foo/bar", 42); err != nil {
		t.Fatalf("ClearResult: %v", err)
	}
	if _, err := LoadResult(dir, "foo/bar", 42); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected ErrNotExist after ClearResult, got %v", err)
	}
}

func TestResult_LoadMissing_IsErrNotExist(t *testing.T) {
	dir := t.TempDir()
	if _, err := LoadResult(dir, "foo/bar", 7); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected ErrNotExist for missing result, got %v", err)
	}
}

func TestResult_ClearMissing_IsNoOp(t *testing.T) {
	dir := t.TempDir()
	if err := ClearResult(dir, "foo/bar", 99); err != nil {
		t.Fatalf("ClearResult of missing: %v", err)
	}
}

// The Result path scheme MUST be distinct from the Draft path scheme so the
// human-editable Draft and the machine sidecar never collide on disk.
func TestResult_PathIsDistinctFromDraftPath(t *testing.T) {
	dir := t.TempDir()
	rp := ResultPathFor(dir, "foo/bar", 42)
	dp := PathFor(dir, "foo/bar", 42)
	if rp == dp {
		t.Fatalf("Result path %q must differ from Draft path %q", rp, dp)
	}
	if !strings.HasSuffix(rp, ".result.json") {
		t.Fatalf("Result path %q must end with .result.json", rp)
	}
}

// Verdict is advisory and omitempty; an empty Verdict must not appear in JSON.
func TestResult_VerdictOmitEmpty(t *testing.T) {
	dir := t.TempDir()
	path, err := SaveResult(dir, &Result{Repo: "foo/bar", PR: 1, Ownership: "team", HeadSHA: "h", BeadID: "b"})
	if err != nil {
		t.Fatalf("SaveResult: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if strings.Contains(string(raw), "verdict") {
		t.Fatalf("empty Verdict must be omitted from JSON, got: %s", raw)
	}
}
