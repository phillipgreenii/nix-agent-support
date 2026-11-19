package hygiene

import (
	"context"

	"github.com/phillipgreenii/pg-router/internal/handlers/beads"
	"github.com/phillipgreenii/pg-router/internal/handlers/ccpool"
	"github.com/phillipgreenii/pg-router/internal/handlers/worktree"
)

// testPorts is the teardown ports a test uses by default: production behavior
// except that no test depends on lsof, so the closed-bead teardown sees no
// live process unless a test builds its own ports with a probe.
func testPorts() teardownPorts { return teardownPorts{probe: noLiveProcess} }

// The wrappers below let a test that needs no special port call a teardown
// pass without naming the ports; a test that substitutes one calls the method
// on its own teardownPorts value.

func closeSession(ctx context.Context, cc ccpool.Runner, open worktree.Opener, repoRoot, worktreeDir string, s ccpool.Session, keepWorktree bool) bool {
	return testPorts().closeSession(ctx, cc, open, repoRoot, worktreeDir, s, keepWorktree)
}

func closeSessionAndWorktree(ctx context.Context, cc ccpool.Runner, open worktree.Opener, repoRoot, worktreeDir string, s ccpool.Session) bool {
	return testPorts().closeSessionAndWorktree(ctx, cc, open, repoRoot, worktreeDir, s)
}

func closeSinglePhase(ctx context.Context, cc ccpool.Runner, open worktree.Opener, repoRoot string, s ccpool.Session, keepWorktree bool) bool {
	return testPorts().closeSinglePhase(ctx, cc, open, repoRoot, s, keepWorktree)
}

func finishPurge(ctx context.Context, cc ccpool.Runner, open worktree.Opener, repoRoot, worktreeDir string, s ccpool.Session, keepWorktree bool) bool {
	return testPorts().finishPurge(ctx, cc, open, repoRoot, worktreeDir, s, keepWorktree)
}

func reconcileClosedBeadSessions(ctx context.Context, cc ccpool.Runner, open worktree.Opener, br beads.Runner, prefix, repoRoot, worktreeDir string, quiet quietCheck) int {
	return testPorts().reconcileClosedBeadSessions(ctx, cc, open, br, prefix, repoRoot, worktreeDir, quiet)
}

func reconcileStaleDefaultPoolSessions(ctx context.Context, cc ccpool.Runner, open worktree.Opener, br beads.Runner, prefix, repoRoot, worktreeDir string, quiet quietCheck, guard sessionGuard) int {
	return testPorts().reconcileStaleDefaultPoolSessions(ctx, cc, open, br, prefix, repoRoot, worktreeDir, quiet, guard)
}
