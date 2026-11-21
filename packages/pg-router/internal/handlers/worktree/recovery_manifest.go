package worktree

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	// ManifestFile and PatchFile are the two files that replace a full interrupted
	// checkout in the "<worktreeDir>-interrupted" holding area.
	ManifestFile = "manifest.json"
	PatchFile    = "changes.patch"
)

// RecoveryManifest records what makes an interrupted checkout recoverable once
// the full tree is dropped: the commit it was based on, its `git status
// --porcelain`, and (in PatchFile beside the manifest) a patch of every
// uncommitted and untracked edit. Unique commits live in the branch ref.
type RecoveryManifest struct {
	BeadID        string    `json:"bead_id"`
	Branch        string    `json:"branch"`
	OriginalPath  string    `json:"original_path"`
	BaseCommit    string    `json:"base_commit"`
	StatusPorcels string    `json:"status_porcelain"`
	PatchFile     string    `json:"patch_file"`
	PatchBytes    int       `json:"patch_bytes"`
	CreatedAt     time.Time `json:"created_at"`
}

// manifestWriter is the seam tests replace to simulate a manifest-write failure.
var manifestWriter = writeRecoveryManifest

func gitOut(ctx context.Context, dir string, env []string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), env...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// writeRecoveryManifest writes ManifestFile and PatchFile into destDir (created
// if absent) from the live worktree at path, then re-reads both to verify. The
// worktree's own index is never modified: untracked files are captured through
// a throwaway index, which also works while a stale index.lock is present.
func writeRecoveryManifest(ctx context.Context, path, destDir, beadID, branch string) error {
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return fmt.Errorf("mkdir manifest dir: %w", err)
	}
	base, err := gitOut(ctx, path, nil, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	status, err := gitOut(ctx, path, []string{"GIT_OPTIONAL_LOCKS=0"}, "status", "--porcelain")
	if err != nil {
		return err
	}
	idx := filepath.Join(destDir, ".tmp-index")
	defer func() { _ = os.Remove(idx) }()
	env := []string{"GIT_INDEX_FILE=" + idx}
	if _, err := gitOut(ctx, path, env, "read-tree", "HEAD"); err != nil {
		return err
	}
	if _, err := gitOut(ctx, path, env, "add", "-A"); err != nil {
		return err
	}
	patch, err := gitOut(ctx, path, env, "diff", "--cached", "--binary", "--no-ext-diff", "HEAD")
	if err != nil {
		return err
	}
	m := RecoveryManifest{
		BeadID:        beadID,
		Branch:        branch,
		OriginalPath:  path,
		BaseCommit:    strings.TrimSpace(string(base)),
		StatusPorcels: string(status),
		PatchFile:     PatchFile,
		PatchBytes:    len(patch),
		CreatedAt:     time.Now().UTC(),
	}
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(destDir, PatchFile), patch, 0o644); err != nil {
		return fmt.Errorf("write patch: %w", err)
	}
	if err := os.WriteFile(filepath.Join(destDir, ManifestFile), raw, 0o644); err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}
	return verifyRecoveryManifest(destDir, m)
}

// verifyRecoveryManifest re-reads what was written and checks it round-trips.
func verifyRecoveryManifest(destDir string, want RecoveryManifest) error {
	raw, err := os.ReadFile(filepath.Join(destDir, ManifestFile))
	if err != nil {
		return fmt.Errorf("verify manifest: %w", err)
	}
	var got RecoveryManifest
	if err := json.Unmarshal(raw, &got); err != nil {
		return fmt.Errorf("verify manifest: %w", err)
	}
	if got.BaseCommit == "" || got.BaseCommit != want.BaseCommit {
		return fmt.Errorf("verify manifest: base commit mismatch")
	}
	p, err := os.ReadFile(filepath.Join(destDir, got.PatchFile))
	if err != nil {
		return fmt.Errorf("verify patch: %w", err)
	}
	if len(p) != want.PatchBytes {
		return fmt.Errorf("verify patch: %d bytes, want %d", len(p), want.PatchBytes)
	}
	return nil
}
