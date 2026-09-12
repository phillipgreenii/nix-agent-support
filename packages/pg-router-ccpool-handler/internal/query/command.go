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
)

// Commander runs an executable and returns its stdout (one-method interface,
// like beads.Runner / ccpool.Runner — not a bare func field). Mirrors
// packages/pg-router/internal/query.Commander exactly.
type Commander interface {
	Run(ctx context.Context, argv []string) ([]byte, error)
}

// OSCommander is the default Commander: shells out via os/exec.
type OSCommander struct{}

func (OSCommander) Run(ctx context.Context, argv []string) ([]byte, error) {
	if len(argv) == 0 {
		return nil, fmt.Errorf("empty argv")
	}
	return exec.CommandContext(ctx, argv[0], argv[1:]...).Output()
}
