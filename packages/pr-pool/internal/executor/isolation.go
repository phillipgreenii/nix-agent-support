package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/phillipgreenii/pr-pool/internal/query"
	"github.com/phillipgreenii/pr-pool/internal/roles"
	"github.com/phillipgreenii/pr-pool/internal/worktree"
)

// Isolation prepares the WORKSPACE_ROOT a dispatched ccpool session runs in,
// given the item id being dispatched. Selected per-role by IsolationConfig.Type
// (roles.IsolationConfig); see newIsolation. Pulled out of ccpoolRun.run so the
// single-repo git-worktree assumption that used to be baked into every ccpool
// dispatch is now one strategy among several, not the only option — a role
// whose own prompt/skill already does correct isolation (e.g. across a
// multi-repo pn-workspace workforest, or none at all) no longer has to fight an
// unconditional, unrelated worktree pr-pool creates for it.
type Isolation interface {
	Ensure(ctx context.Context, itemID string) (workspaceRoot string, err error)
}

// newIsolation resolves a role's isolation strategy. An empty/"worktree" Type
// preserves the long-standing only behavior — a fresh per-item git worktree off
// RepoRoot — so an existing config that never sets [role.ccpool.isolation] is
// unaffected. config.buildIsolation already rejects any Type outside this set
// at config-load time, so the default case here is unreachable through normal
// config decoding; it only guards a roles.IsolationConfig built directly
// (e.g. in a test) with a bad Type.
func newIsolation(cfg roles.IsolationConfig, deps Deps) Isolation {
	switch cfg.Type {
	case "", "worktree":
		return worktreeIsolation{git: deps.git(), worktreeDir: deps.Cfg.WorktreeDir, repoRoot: deps.Cfg.RepoRoot}
	case "none":
		return noneIsolation{repoRoot: deps.Cfg.RepoRoot}
	case "path":
		return pathIsolation{path: cfg.Path}
	case "workforest":
		return workforestIsolation{cmd: deps.Cmd}
	default:
		return errIsolation{err: fmt.Errorf("unknown isolation type %q", cfg.Type)}
	}
}

// worktreeIsolation is the long-standing default: a fresh, isolated per-item
// git worktree so a worker never runs on whatever branch RepoRoot happens to be
// on (pg2-yukh root cause #2). Thin wrapper around the unchanged
// internal/worktree.Ensure — behavior is byte-for-byte identical to before this
// type existed.
type worktreeIsolation struct {
	git         worktree.Git
	worktreeDir string
	repoRoot    string
}

func (w worktreeIsolation) Ensure(ctx context.Context, itemID string) (string, error) {
	return worktree.Ensure(ctx, w.git, w.worktreeDir, w.repoRoot, itemID)
}

// noneIsolation creates nothing: the dispatched session's WORKSPACE_ROOT is
// RepoRoot directly. For a role whose own prompt/skill manages its own
// isolation internally (e.g. one that runs a skill which claims/isolates a
// single work item per its own convention, regardless of single-repo or
// multi-repo scope) and gets no benefit from a worktree pr-pool would
// otherwise create and never reference.
type noneIsolation struct{ repoRoot string }

func (n noneIsolation) Ensure(context.Context, string) (string, error) { return n.repoRoot, nil }

// pathIsolation create-or-reuses one fixed configured directory, not derived
// from the item id — for a role that always wants one static scratch dir
// shared across every dispatch.
type pathIsolation struct{ path string }

func (p pathIsolation) Ensure(context.Context, string) (string, error) {
	if err := os.MkdirAll(p.path, 0o755); err != nil {
		return "", fmt.Errorf("isolation path %s: %w", p.path, err)
	}
	return p.path, nil
}

// workforestIsolation create-or-reuses a coordinated multi-repo workforest set
// keyed by item id, via the `pn workspace workforest` CLI — the same mechanism
// the pn-workspace-rules:fork-workforest skill uses. pr-pool stays ignorant of
// what a workforest IS or why one is needed (GOAL-MIN-1): it only knows the
// invocations to run. Reuse is checked via `workforest list` (no --json on that
// subcommand today, so this matches on the branch name appearing in its
// human-readable output — brittle if that output format changes, but no worse
// than shelling out at all; harden if/when a role actually adopts this
// strategy).
type workforestIsolation struct{ cmd query.Commander }

func (w workforestIsolation) Ensure(ctx context.Context, itemID string) (string, error) {
	root, err := w.workspaceRoot(ctx)
	if err != nil {
		return "", fmt.Errorf("workforest %s: %w", itemID, err)
	}
	path := root + "/.workforests/" + itemID
	listed, err := w.cmd.Run(ctx, []string{"pn", "workspace", "workforest", "list"})
	if err != nil {
		return "", fmt.Errorf("workforest list: %w", err)
	}
	if strings.Contains(string(listed), itemID) {
		return path, nil // already exists — reuse (mirrors worktree.Ensure's idempotency)
	}
	if _, err := w.cmd.Run(ctx, []string{"pn", "workspace", "workforest", "add", itemID}); err != nil {
		return "", fmt.Errorf("workforest add %s: %w", itemID, err)
	}
	return path, nil
}

func (w workforestIsolation) workspaceRoot(ctx context.Context) (string, error) {
	out, err := w.cmd.Run(ctx, []string{"pn", "workspace", "info", "--json"})
	if err != nil {
		return "", fmt.Errorf("workspace info: %w", err)
	}
	var info struct {
		Root string `json:"root"`
	}
	if err := json.Unmarshal(out, &info); err != nil {
		return "", fmt.Errorf("workspace info: decode: %w", err)
	}
	if info.Root == "" {
		return "", fmt.Errorf("workspace info: empty root")
	}
	return info.Root, nil
}

// errIsolation always fails Ensure — see newIsolation's default case.
type errIsolation struct{ err error }

func (e errIsolation) Ensure(context.Context, string) (string, error) { return "", e.err }
