package shim

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fields(s string) []string { return strings.Fields(s) }

func TestGHRejectsEveryWriteVerb(t *testing.T) {
	writes := []string{
		"api -X POST repos/o/r/issues",
		"api --method POST repos/o/r/issues/1/comments -f body=hi",
		"api --method=PATCH repos/o/r/pulls/1",
		"api -X PUT repos/o/r/pulls/1/merge",
		"api -X DELETE repos/o/r/issues/1",
		"api repos/o/r/issues -f title=x",
		"api repos/o/r/issues -F title=x",
		"api repos/o/r/issues --raw-field title=x",
		"pr create --title x",
		"pr merge 1",
		"pr review 1 --approve",
		"pr comment 1 --body x",
		"pr edit 1 --title x",
		"pr close 1",
		"pr reopen 1",
		"pr ready 1",
		"pr lock 1",
		"pr update-branch 1",
		"issue create --title x",
		"issue comment 1 --body x",
		"issue edit 1",
		"issue close 1",
		"issue delete 1",
		"repo create x",
		"repo delete x --yes",
		"release create v1",
		"gist create f",
		"label create x",
		"workflow run ci.yml",
		"secret set X",
		"auth login",
		"auth logout",
		"config set x y",
	}
	for _, w := range writes {
		if v, why := ClassifyGH(fields(w)); v != RejectWrite {
			t.Errorf("gh %q: verdict %s (%s), want reject-write", w, v, why)
		}
	}
	// GraphQL mutation in either field flavor.
	for _, w := range [][]string{
		{"api", "graphql", "-f", "query=mutation { addComment(input: {}) { clientMutationId } }"},
		{"api", "graphql", "-F", "query=Mutation{x}"},
		{"api", "graphql", "--raw-field", "query=mutation{x}"},
	} {
		if v, _ := ClassifyGH(w); v != RejectWrite {
			t.Errorf("gh %v: verdict %s, want reject-write", w, v)
		}
	}
	// A body that cannot be inspected is refused too.
	if v, _ := ClassifyGH(fields("api graphql --input -")); v != RejectUnknown {
		t.Errorf("api graphql --input: %s, want reject-unknown", v)
	}
	// Unknown top-level verbs are refused, never allowed by default.
	if v, _ := ClassifyGH(fields("browse")); v == Allow {
		t.Errorf("an unknown verb must not be allowed")
	}
}

func TestGHAllowsKnownReads(t *testing.T) {
	reads := [][]string{
		{"api", "graphql", "-f", "query={ rateLimit { remaining resetAt } }"},
		{"api", "graphql", "-F", "query=query { viewer { login } }"},
		{"api", "repos/o/r/compare/a...b?per_page=100&page=1"},
		{"api", "-X", "GET", "repos/o/r/pulls", "-f", "state=open"},
		{"api", "--method", "GET", "repos/o/r/pulls"},
		{"pr", "view", "12", "--repo", "o/r", "--json", "number"},
		{"pr", "list", "--repo", "o/r"},
		{"pr", "checks", "12"},
		{"pr", "diff", "12"},
		{"search", "prs", "x"},
		{"auth", "token"},
		{"auth", "status"},
		{"run", "view", "5"},
		{"--version"},
		{"version"},
	}
	for _, r := range reads {
		if v, why := ClassifyGH(r); v != Allow {
			t.Errorf("gh %v: verdict %s (%s), want allow", r, v, why)
		}
	}
}

func TestBDRejectsEveryWriteVerb(t *testing.T) {
	for _, w := range []string{
		"create x", "update x --status open", "close x", "dep add a b", "dep remove a b", "comment x hi", "comments add x hi",
		"label add x y", "delete x", "claim x", "init", "import", "export", "sql select", "dolt start", "reopen x", "edit x",
		"-C /tmp/ws create x", "--json update x",
	} {
		if v, why := ClassifyBD(fields(w)); v != RejectWrite {
			t.Errorf("bd %q: verdict %s (%s), want reject-write", w, v, why)
		}
	}
	if v, _ := ClassifyBD(fields("frobnicate")); v != RejectUnknown {
		t.Errorf("unknown bd verb must be reject-unknown")
	}
	if v, _ := ClassifyBD(nil); v == Allow {
		t.Errorf("bd with no verb must not be allowed")
	}
}

func TestBDAllowsReads(t *testing.T) {
	for _, w := range []string{
		"-C /ws list --status open --json", "-C /ws show x", "ready", "blocked", "deps x", "dep list x", "dep tree x", "search foo", "count", "where", "version", "--version",
	} {
		if v, why := ClassifyBD(fields(w)); v != Allow {
			t.Errorf("bd %q: verdict %s (%s), want allow", w, v, why)
		}
	}
}

func TestRunLogsAndRejects(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "shim.log")
	var out, errb bytes.Buffer
	code := Run(Options{Tool: "gh", Real: "/nonexistent/gh", Log: log, Stdout: &out, Stderr: &errb}, fields("pr merge 1"))
	if code != ExitRejected {
		t.Fatalf("exit %d, want %d", code, ExitRejected)
	}
	if !strings.Contains(errb.String(), "read-only") {
		t.Errorf("stderr should explain the refusal: %q", errb.String())
	}
	w, u, off, err := CountRejects(log, 0)
	if err != nil || w != 1 || u != 0 || off == 0 {
		t.Fatalf("CountRejects = %d,%d,%d,%v", w, u, off, err)
	}
	// Incremental: nothing new past the offset.
	if w, u, _, _ := CountRejects(log, off); w != 0 || u != 0 {
		t.Errorf("offset not honoured: %d %d", w, u)
	}
}

func TestRunExecsRealOnlyForReads(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "gh")
	marker := filepath.Join(dir, "ran")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho ran > "+marker+"\nexit 3\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := Run(Options{Tool: "gh", Real: fake, Log: filepath.Join(dir, "l"), Stdout: &out, Stderr: &errb}, fields("pr merge 1")); code != ExitRejected {
		t.Fatalf("write exit %d", code)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the real tool ran for a rejected write")
	}
	if code := Run(Options{Tool: "gh", Real: fake, Log: filepath.Join(dir, "l"), Stdout: &out, Stderr: &errb}, fields("pr view 1")); code != 3 {
		t.Fatalf("read should return the real exit code 3, got %d", code)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("the real tool did not run for a read")
	}
}

func TestHermeticBD(t *testing.T) {
	var out, errb bytes.Buffer
	if code := Run(Options{Tool: "bd", Real: "/nonexistent", HermeticBD: true, Stdout: &out, Stderr: &errb}, fields("-C /ws list --json")); code != 0 || strings.TrimSpace(out.String()) != "[]" {
		t.Fatalf("hermetic list: %d %q", code, out.String())
	}
}
