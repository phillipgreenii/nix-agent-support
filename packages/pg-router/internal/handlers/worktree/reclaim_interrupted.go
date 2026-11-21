package worktree

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Reclaim rules. An entry in the "<worktreeDir>-interrupted" holding area is
// removed ONLY when one of these proves nothing is lost; every other entry is
// kept with a reason.
const (
	// RuleEmptyAndClean: the manifest's patch is empty and its recorded
	// `git status --porcelain` is empty, so the tree held no uncommitted work
	// (unique commits live in the branch ref).
	RuleEmptyAndClean = "empty patch and clean status"
	// RulePatchInBranch: the manifest's patch has the same `git patch-id
	// --stable` as the diff from the manifest's base commit to the branch tip,
	// so the uncommitted work was committed to the branch.
	RulePatchInBranch = "patch already contained in branch tip (git patch-id --stable)"
)

// ReclaimDecision is one holding-area entry's verdict. Exactly one of Rule
// (the entry is removed) and Reason (the entry is kept) is set.
type ReclaimDecision struct {
	Entry  string
	Remove bool
	Rule   string
	Reason string
	SizeKB int64
}

func keep(entry, reason string) ReclaimDecision {
	return ReclaimDecision{Entry: entry, Reason: reason}
}

// PlanReclaimInterrupted evaluates every direct child of holdingDir and returns
// one decision per entry, sorted by name. It deletes nothing. repoRoot is the
// repository the manifests' branches live in. A missing holdingDir is an empty
// plan. Anything the rules cannot prove safe is kept.
func PlanReclaimInterrupted(ctx context.Context, holdingDir, repoRoot string) ([]ReclaimDecision, error) {
	des, err := os.ReadDir(holdingDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", holdingDir, err)
	}
	sort.Slice(des, func(i, j int) bool { return des[i].Name() < des[j].Name() })
	out := make([]ReclaimDecision, 0, len(des))
	for _, de := range des {
		out = append(out, decideEntry(ctx, filepath.Join(holdingDir, de.Name()), de, repoRoot))
	}
	return out, nil
}

func decideEntry(ctx context.Context, dir string, de os.DirEntry, repoRoot string) ReclaimDecision {
	if !de.IsDir() || de.Type()&os.ModeSymlink != 0 {
		return keep(dir, "not a directory entry")
	}
	names, err := os.ReadDir(dir)
	if err != nil {
		return keep(dir, "unreadable entry: "+err.Error())
	}
	raw, err := os.ReadFile(filepath.Join(dir, ManifestFile))
	if err != nil {
		if os.IsNotExist(err) {
			return keep(dir, "no manifest")
		}
		return keep(dir, "unreadable manifest: "+err.Error())
	}
	var m RecoveryManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return keep(dir, "unreadable manifest: "+err.Error())
	}
	if m.PatchFile != PatchFile {
		return keep(dir, fmt.Sprintf("manifest names unexpected patch file %q", m.PatchFile))
	}
	// Only a pure manifest entry is removable: anything else (a full tree kept
	// after a failed manifest write, stray files) is not covered by the rules.
	for _, n := range names {
		if n.Name() != ManifestFile && n.Name() != PatchFile {
			return keep(dir, fmt.Sprintf("unexpected content %q beside the manifest", n.Name()))
		}
	}
	patch, err := os.ReadFile(filepath.Join(dir, PatchFile))
	if err != nil {
		return keep(dir, "unreadable patch: "+err.Error())
	}
	if len(patch) != m.PatchBytes {
		return keep(dir, fmt.Sprintf("patch is %d bytes, manifest says %d", len(patch), m.PatchBytes))
	}
	size := int64((len(raw) + len(patch) + 1023) / 1024)

	if len(patch) == 0 {
		if strings.TrimSpace(m.StatusPorcels) != "" {
			return keep(dir, "patch is empty but recorded status is not clean")
		}
		return ReclaimDecision{Entry: dir, Remove: true, Rule: RuleEmptyAndClean, SizeKB: size}
	}
	if bytes.Contains(patch, []byte("GIT binary patch")) {
		return keep(dir, "patch holds binary changes; patch-id cannot prove containment")
	}
	if repoRoot == "" {
		return keep(dir, "no repository given to evaluate the branch tip")
	}
	if m.Branch == "" || m.BaseCommit == "" {
		return keep(dir, "manifest lacks branch or base commit")
	}
	want, err := patchID(ctx, repoRoot, patch)
	if err != nil || want == "" {
		return keep(dir, "cannot compute the patch's patch-id")
	}
	if _, err := gitOut(ctx, repoRoot, nil, "cat-file", "-e", m.BaseCommit+"^{commit}"); err != nil {
		return keep(dir, "base commit not found in the repository")
	}
	if _, err := gitOut(ctx, repoRoot, nil, "rev-parse", "--verify", "--quiet", "refs/heads/"+m.Branch); err != nil {
		return keep(dir, "branch "+m.Branch+" not found in the repository")
	}
	tipDiff, err := gitOut(ctx, repoRoot, nil, "diff", "--binary", "--no-ext-diff", "--src-prefix=a/", "--dst-prefix=b/", m.BaseCommit, "refs/heads/"+m.Branch)
	if err != nil {
		return keep(dir, "cannot diff base commit against branch tip")
	}
	got, err := patchID(ctx, repoRoot, tipDiff)
	if err != nil || got == "" {
		return keep(dir, "patch is not contained in the branch tip (branch adds nothing beyond the base commit)")
	}
	if got != want {
		return keep(dir, "patch is not contained in the branch tip (patch-id differs)")
	}
	return ReclaimDecision{Entry: dir, Remove: true, Rule: RulePatchInBranch, SizeKB: size}
}

// patchID returns the first field of `git patch-id --stable` for patch.
func patchID(ctx context.Context, repoRoot string, patch []byte) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", repoRoot, "patch-id", "--stable")
	cmd.Stdin = bytes.NewReader(patch)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git patch-id: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	f := strings.Fields(string(out))
	if len(f) == 0 {
		return "", nil
	}
	return f[0], nil
}

// ApplyReclaimInterrupted removes exactly the entries a plan marked Remove and
// returns the first error. Each removal re-checks that the path is still a
// direct, non-symlink child of holdingDir.
func ApplyReclaimInterrupted(holdingDir string, plan []ReclaimDecision) error {
	for _, d := range plan {
		if !d.Remove {
			continue
		}
		if filepath.Dir(d.Entry) != filepath.Clean(holdingDir) {
			return fmt.Errorf("refusing to remove %s: not a direct child of %s", d.Entry, holdingDir)
		}
		fi, err := os.Lstat(d.Entry)
		if err != nil {
			return fmt.Errorf("remove %s: %w", d.Entry, err)
		}
		if !fi.IsDir() {
			return fmt.Errorf("refusing to remove %s: not a directory", d.Entry)
		}
		if err := os.RemoveAll(d.Entry); err != nil {
			return fmt.Errorf("remove %s: %w", d.Entry, err)
		}
	}
	return nil
}
