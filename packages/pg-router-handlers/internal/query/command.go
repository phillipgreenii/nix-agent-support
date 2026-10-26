// Package query carries the narrow argv-execution seam the moved
// ccpool/command executor logic needs: Commander plus its default OS
// implementation. This is NOT a copy of packages/pg-router/internal/query:
// that package is pg-router's full typed-union of event SOURCES (beads
// queries, command queries, triggers) and stays in-core, unreachable from
// here (Go's internal-package visibility rule — docs/adr/0065's Addendum).
// Only the tiny Commander interface travels — the moved code's one remaining
// need from it (workforestIsolation's `pn workspace` invocations, and the
// executor's own default argv-runner seam).
package query

import (
	"context"
	"fmt"
	"os/exec"
	"time"

	"github.com/phillipgreenii/pg-router/procenv"
)

// CancelGrace is how long a cancelled RunEnv payload's process group has between
// SIGTERM and SIGKILL (the same 5 s the core uses for its own commands).
const CancelGrace = 5 * time.Second

// Commander runs an executable and returns its stdout (one-method interface,
// like beads.Runner / ccpool.Runner — not a bare func field). Mirrors
// packages/pg-router/internal/query.Commander exactly.
type Commander interface {
	Run(ctx context.Context, argv []string) ([]byte, error)
}

// EnvCommander is a Commander that can also run an argv with an explicit
// environment, in its own process group. The command executor uses it for a
// command role's payload; a Commander that is not an EnvCommander (a test
// double) is run through Run, without the environment or the process group.
type EnvCommander interface {
	Commander
	RunEnv(ctx context.Context, argv []string, env []string) ([]byte, error)
}

// OSCommander is the default Commander: shells out via os/exec.
type OSCommander struct{}

func (OSCommander) Run(ctx context.Context, argv []string) ([]byte, error) {
	if len(argv) == 0 {
		return nil, fmt.Errorf("empty argv")
	}
	return exec.CommandContext(ctx, argv[0], argv[1:]...).Output()
}

// RunEnv runs argv with exactly env as its environment (os.Environ() form),
// in its own process group. A cancelled ctx sends SIGTERM to the whole group,
// then SIGKILL after CancelGrace, so a non-exec child (a timeout layer, a shell
// script) cannot orphan its payload. Stdout is returned; on failure the
// *exec.ExitError carries the stderr, as with Run.
func (OSCommander) RunEnv(ctx context.Context, argv []string, env []string) ([]byte, error) {
	if len(argv) == 0 {
		return nil, fmt.Errorf("empty argv")
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = env
	procenv.Group(cmd, CancelGrace)
	return cmd.Output()
}
