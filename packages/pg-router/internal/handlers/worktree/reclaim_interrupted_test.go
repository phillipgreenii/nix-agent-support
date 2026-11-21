package worktree

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitT(t *testing.T, dir string, args ...string) {
	t.Helper()
	full := append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.invalid"}, args...)
	if out, err := exec.CommandContext(context.Background(), "git", full...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

func plan(t *testing.T, holding, repo string) map[string]ReclaimDecision {
	t.Helper()
	ds, err := PlanReclaimInterrupted(context.Background(), holding, repo)
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]ReclaimDecision{}
	for _, d := range ds {
		m[filepath.Base(d.Entry)] = d
	}
	return m
}

// handMade writes an entry holding the given manifest and patch.
func handMade(t *testing.T, holding, name string, m RecoveryManifest, patch string) string {
	t.Helper()
	dir := filepath.Join(holding, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	m.PatchFile = PatchFile
	m.PatchBytes = len(patch)
	raw, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(dir, ManifestFile), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, PatchFile), []byte(patch), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func wantKeep(t *testing.T, d ReclaimDecision, reasonPart string) {
	t.Helper()
	if d.Remove || d.Rule != "" || !strings.Contains(d.Reason, reasonPart) {
		t.Errorf("decision = %+v, want kept with reason containing %q", d, reasonPart)
	}
}

func TestReclaimInterrupted_missingHoldingDirIsEmptyPlan(t *testing.T) {
	ds, err := PlanReclaimInterrupted(context.Background(), filepath.Join(t.TempDir(), "none"), "")
	if err != nil || len(ds) != 0 {
		t.Errorf("got %v, %v", ds, err)
	}
}

func TestReclaimInterrupted_ruleEmptyPatchAndCleanStatus(t *testing.T) {
	open, repoRoot, wtDir, _ := interruptedFixture(t, func(string) {})
	if _, err := Ensure(context.Background(), open, wtDir, repoRoot, "zr-man"); err != nil {
		t.Fatal(err)
	}
	holding := wtDir + "-interrupted"
	got := plan(t, holding, repoRoot)
	if len(got) != 1 {
		t.Fatalf("plan = %v", got)
	}
	for _, d := range got {
		if !d.Remove || d.Rule != RuleEmptyAndClean {
			t.Errorf("decision = %+v", d)
		}
	}
	if err := ApplyReclaimInterrupted(holding, mustPlan(t, holding, repoRoot)); err != nil {
		t.Fatal(err)
	}
	if left, _ := os.ReadDir(holding); len(left) != 0 {
		t.Errorf("entry not removed: %v", left)
	}
}

func mustPlan(t *testing.T, holding, repo string) []ReclaimDecision {
	t.Helper()
	ds, err := PlanReclaimInterrupted(context.Background(), holding, repo)
	if err != nil {
		t.Fatal(err)
	}
	return ds
}

func dirtyEdit(t *testing.T) func(string) {
	return func(p string) {
		if err := os.WriteFile(filepath.Join(p, "work.txt"), []byte("edited"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(p, "new.txt"), []byte("untracked"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestReclaimInterrupted_ruleKeepsUncommittedEditUntilBranchHoldsIt(t *testing.T) {
	open, repoRoot, wtDir, path := interruptedFixture(t, dirtyEdit(t))
	if _, err := Ensure(context.Background(), open, wtDir, repoRoot, "zr-man"); err != nil {
		t.Fatal(err)
	}
	holding := wtDir + "-interrupted"
	for _, d := range plan(t, holding, repoRoot) {
		wantKeep(t, d, "not contained in the branch tip")
	}
	// The recreated worktree is on the branch: commit exactly the same edit.
	if err := os.WriteFile(filepath.Join(path, "work.txt"), []byte("edited"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "new.txt"), []byte("untracked"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitT(t, path, "add", "-A")
	gitT(t, path, "commit", "-m", "land the edit")
	for _, d := range plan(t, holding, repoRoot) {
		if !d.Remove || d.Rule != RulePatchInBranch {
			t.Errorf("decision = %+v, want removal by %q", d, RulePatchInBranch)
		}
	}
}

func TestReclaimInterrupted_keepsWhenBranchHoldsMoreThanThePatch(t *testing.T) {
	open, repoRoot, wtDir, path := interruptedFixture(t, dirtyEdit(t))
	if _, err := Ensure(context.Background(), open, wtDir, repoRoot, "zr-man"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "work.txt"), []byte("edited"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "new.txt"), []byte("untracked"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "extra.txt"), []byte("more"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitT(t, path, "add", "-A")
	gitT(t, path, "commit", "-m", "edit plus more")
	for _, d := range plan(t, wtDir+"-interrupted", repoRoot) {
		wantKeep(t, d, "not contained in the branch tip")
	}
}

func TestReclaimInterrupted_keepReasons(t *testing.T) {
	repo := newCommittedRepo(t)
	holding := filepath.Join(t.TempDir(), "w-interrupted")
	if err := os.MkdirAll(filepath.Join(holding, "legacy-1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(holding, "legacy-1", "file.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(holding, "bad-1")
	_ = os.MkdirAll(bad, 0o755)
	_ = os.WriteFile(filepath.Join(bad, ManifestFile), []byte("{not json"), 0o644)
	head := strings.TrimSpace(gitOutT(t, repo, "rev-parse", "HEAD"))

	handMade(t, holding, "dirty-status-1", RecoveryManifest{BaseCommit: head, Branch: "main", StatusPorcels: "?? x\n"}, "")
	d := handMade(t, holding, "extra-1", RecoveryManifest{BaseCommit: head, Branch: "main"}, "")
	_ = os.WriteFile(filepath.Join(d, "tree.txt"), []byte("kept"), 0o644)
	d = handMade(t, holding, "truncated-1", RecoveryManifest{BaseCommit: head, Branch: "main"}, "")
	_ = os.WriteFile(filepath.Join(d, PatchFile), []byte("short"), 0o644)
	handMade(t, holding, "binary-1", RecoveryManifest{BaseCommit: head, Branch: "main", StatusPorcels: "?? b"}, "diff --git a/b b/b\nGIT binary patch\nliteral 1\n")
	one := "diff --git a/f b/f\nnew file mode 100644\n--- /dev/null\n+++ b/f\n@@ -0,0 +1 @@\n+x\n"
	handMade(t, holding, "nobranch-1", RecoveryManifest{BaseCommit: head, Branch: "pg-router/gone", StatusPorcels: "?? f"}, one)
	handMade(t, holding, "nobase-1", RecoveryManifest{BaseCommit: strings.Repeat("0", 40), Branch: "main", StatusPorcels: "?? f"}, one)
	handMade(t, holding, "nofields-1", RecoveryManifest{StatusPorcels: "?? f"}, one)
	if err := os.WriteFile(filepath.Join(holding, "stray-file"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := plan(t, holding, repo)
	wantKeep(t, got["legacy-1"], "no manifest")
	wantKeep(t, got["bad-1"], "unreadable manifest")
	wantKeep(t, got["dirty-status-1"], "status is not clean")
	wantKeep(t, got["extra-1"], "unexpected content")
	wantKeep(t, got["truncated-1"], "manifest says")
	wantKeep(t, got["binary-1"], "binary")
	wantKeep(t, got["nobranch-1"], "not found")
	wantKeep(t, got["nobase-1"], "base commit not found")
	wantKeep(t, got["nofields-1"], "lacks branch or base")
	wantKeep(t, got["stray-file"], "not a directory")
	for name, dd := range got {
		if dd.Remove {
			t.Errorf("%s would be removed: %+v", name, dd)
		}
	}
	// No repository given: a non-empty patch cannot be evaluated.
	wantKeep(t, plan(t, holding, "")["nobranch-1"], "no repository")

	// Applying the plan deletes nothing, because nothing was provable.
	if err := ApplyReclaimInterrupted(holding, mustPlan(t, holding, repo)); err != nil {
		t.Fatal(err)
	}
	if es, _ := os.ReadDir(holding); len(es) != len(got) {
		t.Errorf("entries after apply = %d, want %d", len(es), len(got))
	}
}

func gitOutT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.CommandContext(context.Background(), "git", append([]string{"-C", dir}, args...)...).Output()
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func TestApplyReclaimInterrupted_removesExactlyThePlannedSetAndNothingElse(t *testing.T) {
	repo := newCommittedRepo(t)
	head := strings.TrimSpace(gitOutT(t, repo, "rev-parse", "HEAD"))
	holding := filepath.Join(t.TempDir(), "w-interrupted")
	handMade(t, holding, "gone-1", RecoveryManifest{BaseCommit: head, Branch: "main"}, "")
	handMade(t, holding, "kept-1", RecoveryManifest{BaseCommit: head, Branch: "main", StatusPorcels: " M a"}, "")
	_ = os.MkdirAll(filepath.Join(holding, "legacy-1"), 0o755)

	p := mustPlan(t, holding, repo)
	// Planning is a dry run: nothing is deleted.
	if es, _ := os.ReadDir(holding); len(es) != 3 {
		t.Fatalf("plan deleted entries: %v", es)
	}
	if err := ApplyReclaimInterrupted(holding, p); err != nil {
		t.Fatal(err)
	}
	es, _ := os.ReadDir(holding)
	var names []string
	for _, e := range es {
		names = append(names, e.Name())
	}
	if strings.Join(names, ",") != "kept-1,legacy-1" {
		t.Errorf("remaining = %v", names)
	}
}

func TestApplyReclaimInterrupted_refusesPathsOutsideTheHoldingDir(t *testing.T) {
	outside := t.TempDir()
	marker := filepath.Join(outside, "keep")
	_ = os.WriteFile(marker, []byte("x"), 0o644)
	holding := filepath.Join(t.TempDir(), "w-interrupted")
	_ = os.MkdirAll(holding, 0o755)
	err := ApplyReclaimInterrupted(holding, []ReclaimDecision{{Entry: outside, Remove: true, Rule: "x"}})
	if err == nil {
		t.Error("expected refusal")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("outside path touched: %v", err)
	}
}
