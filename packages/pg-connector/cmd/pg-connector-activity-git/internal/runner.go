// runner.go: the seam every git invocation in this backend runs through, so a
// fake runner can drive unit tests and a real fixture repo drives the
// end-to-end ones.
package internal

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// Runner execs `git <args...>` with its working directory set to dir. It
// returns git's stdout (trailing whitespace trimmed) or an error folding in
// stderr.
type Runner interface {
	Run(ctx context.Context, dir string, args ...string) (string, error)
}

type execRunner struct{}

// NewExecRunner returns the production Runner: the real `git` binary resolved
// from PATH, under the reduced PATH+HOME child environment.
func NewExecRunner() Runner { return execRunner{} }

func (execRunner) Run(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := gitCommand(ctx, dir, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if txt := scriptout.TruncateForFold(stderr.Bytes()); txt != "" {
			return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, txt)
		}
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimRight(stdout.String(), " \t\r\n"), nil
}
