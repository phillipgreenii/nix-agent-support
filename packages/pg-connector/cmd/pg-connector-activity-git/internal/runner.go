// runner.go: the seam every git invocation in this backend runs through, so a
// fake runner can drive unit tests and a real fixture repo drives the
// end-to-end ones.
package internal

import (
	"context"
	"fmt"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
	"github.com/phillipgreenii/x/cmdrun"
)

// Runner execs `git <args...>` with its working directory set to dir. It
// returns git's stdout (trailing whitespace trimmed) or an error folding in
// stderr.
type Runner interface {
	Run(ctx context.Context, dir string, args ...string) (string, error)
}

type execRunner struct{ run cmdrun.Runner }

// NewExecRunner returns the production Runner: the real `git` binary resolved
// from PATH, under the reduced PATH+HOME child environment.
func NewExecRunner() Runner { return execRunner{run: cmdrun.Exec} }

func (e execRunner) Run(ctx context.Context, dir string, args ...string) (string, error) {
	r := e.run(ctx, gitCall(dir, args...))
	if err := scriptout.ResultErr(r); err != nil {
		if txt := scriptout.TruncateForFold(r.Stderr); txt != "" {
			return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, txt)
		}
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimRight(string(r.Stdout), " \t\r\n"), nil
}
