package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/ledger"
)

// guard is the set of things a ccpool analyst must not change: the scratch
// worktree (empty diff), the configuration directory, the run's staging
// directory and the run's own ledger row (the analyst never files; the runner
// does). It is read-only verification: it only reads and compares.
type guard struct {
	scratch    string
	configDir  string
	staging    string
	configHash string
	stageHash  string
}

// newGuard records the state of the guarded paths.
func newGuard(scratch, configDir, staging string) (*guard, error) {
	g := &guard{scratch: scratch, configDir: configDir, staging: staging}
	var err error
	if g.configHash, err = hashTree(configDir); err != nil {
		return nil, err
	}
	if g.stageHash, err = hashTree(staging); err != nil {
		return nil, err
	}
	return g, nil
}

// verify returns an error naming what changed since newGuard.
func (g *guard) verify(ctx context.Context, l *ledger.Ledger, runID int64) error {
	out, err := exec.CommandContext(ctx, "git", "-C", g.scratch, "status", "--porcelain").Output()
	if err != nil {
		return fmt.Errorf("postcheck: scratch worktree status: %w", err)
	}
	if strings.TrimSpace(string(out)) != "" {
		return fmt.Errorf("postcheck: the analyst modified the scratch worktree")
	}
	if h, err := hashTree(g.configDir); err != nil {
		return err
	} else if h != g.configHash {
		return fmt.Errorf("postcheck: the analyst changed the configuration directory")
	}
	if h, err := hashTree(g.staging); err != nil {
		return err
	} else if h != g.stageHash {
		return fmt.Errorf("postcheck: the analyst changed the staging directory")
	}
	r, err := l.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	if r.State != ledger.RunRunning || r.ItemsFiled != 0 {
		return fmt.Errorf("postcheck: the analyst filed findings itself (run is %s)", r.State)
	}
	return nil
}

// hashTree is the SHA-256 of a directory's file listing and contents; a
// missing directory hashes as "absent".
func hashTree(dir string) (string, error) {
	if dir == "" {
		return "absent", nil
	}
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return "absent", nil
	}
	h := sha256.New()
	err := filepath.WalkDir(dir, func(p string, de fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		fmt.Fprintf(h, "%s\x00%v\x00", rel, de.IsDir())
		if de.IsDir() {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		h.Write(sum[:])
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("postcheck: hash %s: %w", dir, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// makeScratch creates the scratch git worktree under the literal /tmp.
func makeScratch(ctx context.Context) (string, error) {
	p, err := os.MkdirTemp(stagingRoot, "pg-review-scratch-")
	if err != nil {
		return "", err
	}
	for _, args := range [][]string{
		{"init", "-q"},
		{"-c", "user.name=pg-review", "-c", "user.email=pg-review@invalid", "commit", "--allow-empty", "-q", "-m", "scratch"},
	} {
		if out, err := exec.CommandContext(ctx, "git", append([]string{"-C", p}, args...)...).CombinedOutput(); err != nil {
			_ = os.RemoveAll(p)
			return "", fmt.Errorf("scratch worktree: git %s: %v: %s", args[0], err, out)
		}
	}
	return p, nil
}
