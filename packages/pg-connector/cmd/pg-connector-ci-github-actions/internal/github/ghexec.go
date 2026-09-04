package github

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-ci-github-actions/internal/gitenv"
)

// CLI is the token-protected gateway through which this backend invokes the
// `gh` binary, ported unchanged (structure and behavior) from the sibling
// backend's cliGHRunner/CLI split across its auth.go/github.go
// [design: §4.6]. Every invocation resolves a GitHub token FIRST and
// injects it as the child's single GH_TOKEN:
//
//   - `gh` is NEVER executed without a resolved token — Command/Run return
//     an error wrapping ErrGHAuthInvalid *before* any process is created
//     when the token cannot be resolved.
//   - No ambient credential leaks into the child: envWithGHToken strips any
//     inherited GH_TOKEN/GITHUB_TOKEN and appends exactly one resolved
//     GH_TOKEN.
type CLI struct{ r *cliGHRunner }

// NewCLI returns a CLI backed by the default token source (GH_TOKEN /
// GITHUB_TOKEN, else gh's own stored credential).
func NewCLI() *CLI { return &CLI{r: &cliGHRunner{src: defaultTokenSource()}} }

// NewCLIWithTokenSource returns a CLI backed by src. Tests use it to force
// a resolved token, or a resolution failure, without touching the
// environment.
func NewCLIWithTokenSource(src TokenSource) *CLI { return &CLI{r: &cliGHRunner{src: src}} }

// Command resolves a token and returns a ready-to-run `gh <args...>` with
// the token injected into its env. On resolution failure it returns a nil
// *exec.Cmd and an error wrapping ErrGHAuthInvalid: no process is created.
func (c *CLI) Command(ctx context.Context, args ...string) (*exec.Cmd, error) {
	return c.r.command(ctx, args...)
}

// Run invokes `gh <args...>` and returns its stdout.
func (c *CLI) Run(ctx context.Context, args ...string) ([]byte, error) {
	return c.r.Run(ctx, args...)
}

// RunStdin invokes `gh <args...>` feeding stdin to the subprocess.
func (c *CLI) RunStdin(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	return c.r.RunStdin(ctx, stdin, args...)
}

// cliGHRunner is the production runner that invokes the real `gh` binary.
// It resolves a GitHub token once (lazily, success-cached) via its
// TokenSource and injects GH_TOKEN into every child env.
type cliGHRunner struct {
	src  TokenSource
	mu   sync.Mutex
	tok  string
	have bool
}

// token returns the resolved token, resolving (and caching) it at most
// once. Failures are NOT cached so a transient resolution error can be
// retried.
func (r *cliGHRunner) token(ctx context.Context) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.have {
		return r.tok, nil
	}
	t, err := r.src.Token(ctx)
	if err != nil {
		return "", err // do NOT cache failure
	}
	r.tok, r.have = t, true
	return t, nil
}

// command is the choke point: token first, process second. Every gh
// invocation in this package goes through here.
//
// gh resolves "which repository am I talking about" by shelling out to git
// in its working directory, and those git children inherit gh's
// environment — gitenv.Hermetic strips the GIT_DIR family while leaving
// gh's own needs (PATH, HOME, GH_*, proxies, SSH) untouched, matching the
// sibling backend's own copy of this defect fix.
func (r *cliGHRunner) command(ctx context.Context, args ...string) (*exec.Cmd, error) {
	tok, err := r.token(ctx)
	if err != nil {
		return nil, fmt.Errorf("gh %s: no usable GitHub credential; run `gh auth login`: %w",
			strings.Join(args, " "), errors.Join(ErrGHAuthInvalid, err))
	}
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Env = envWithGHToken(gitenv.Hermetic(os.Environ()), tok)
	return cmd, nil
}

func (r *cliGHRunner) Run(ctx context.Context, args ...string) ([]byte, error) {
	return r.RunStdin(ctx, nil, args...)
}

func (r *cliGHRunner) RunStdin(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	cmd, err := r.command(ctx, args...)
	if err != nil {
		return nil, err
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			st := strings.TrimSpace(stderr.String())
			if IsAuthFailure(exitErr.ExitCode(), st) {
				return stdout.Bytes(), fmt.Errorf("gh %s: %s: run `gh auth login`: %w",
					strings.Join(args, " "), st, ErrGHAuthInvalid)
			}
			return stdout.Bytes(), fmt.Errorf("gh %s: %w: %s", strings.Join(args, " "), err, st)
		}
		return stdout.Bytes(), fmt.Errorf("gh %s: %w (is gh on PATH?)", strings.Join(args, " "), err)
	}
	return stdout.Bytes(), nil
}
