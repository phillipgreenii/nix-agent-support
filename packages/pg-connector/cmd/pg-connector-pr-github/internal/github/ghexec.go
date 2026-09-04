package github

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-pr-github/internal/gitenv"
)

// CLI is the token-protected gateway through which pg-pr invokes the `gh`
// binary. Every invocation resolves a GitHub token FIRST and injects it as the
// child's single GH_TOKEN, which buys two guarantees (bead pg2-ilzq9):
//
//   - `gh` is NEVER executed without a resolved token. An unauthenticated gh
//     starts its own interactive login — the observed symptom was repeated auth
//     screens and a crashing browser under the launchd sync agent. When the
//     token cannot be resolved, Command/Run return an error wrapping
//     ErrGHAuthInvalid (== vcs.ErrAuthInvalid) *before* any process is created,
//     and the message names `gh auth login` so the fix is actionable.
//   - No ambient credential leaks into the child: envWithGHToken strips any
//     inherited GH_TOKEN/GITHUB_TOKEN and appends exactly one resolved
//     GH_TOKEN.
//
// Callers reach it two ways. Run/RunStdin hand the whole invocation over
// (stdout bytes plus gh-auth classification). Command returns the prepared
// *exec.Cmd for sites that must wire it up themselves — a working directory
// (internal/branch), separately captured stderr (internal/auth,
// internal/worktree, pkg/provider/cicd/ghactions). Those sites declare a
// one-method interface over Command so tests can inject a fake.
//
// The `gh auth token` exec in token.go is the ONE gh invocation that does not
// go through here, and must stay that way: it is how the token is obtained, so
// routing it through this preflight would be circular. That exemption is
// asserted by TestGHExecChokePoint, which fails if any other file in the module
// execs gh directly.
type CLI struct{ r *cliGHRunner }

// NewCLI returns a CLI backed by the default token source (GH_TOKEN /
// GITHUB_TOKEN, else gh's own stored credential). Construction performs no
// I/O; the token is resolved on first use and cached on success, so one CLI
// value amortizes keychain reads across calls.
func NewCLI() *CLI { return &CLI{r: &cliGHRunner{src: defaultTokenSource()}} }

// NewCLIWithTokenSource returns a CLI backed by src. Tests use it to force a
// resolved token, or a resolution failure, without touching the environment.
func NewCLIWithTokenSource(src TokenSource) *CLI { return &CLI{r: &cliGHRunner{src: src}} }

// Command resolves a token and returns a ready-to-run `gh <args...>` with the
// token injected into its env. On resolution failure it returns a nil *exec.Cmd
// and an error wrapping ErrGHAuthInvalid: no process is created, so a caller
// that honors the error cannot reach an unauthenticated gh.
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

// command is the choke point: token first, process second. Every gh invocation
// in this module (except the token resolver itself) is built here.
//
// gh resolves "which repository am I talking about" by shelling out to git in
// its working directory (`git remote -v` / `git rev-parse`), and those git
// children inherit gh's environment. A leaked GIT_DIR/GIT_WORK_TREE/etc.
// therefore redirects gh's git children exactly as it would redirect a direct
// git call (see internal/gitenv), and gh's answer ends up describing the
// LEAKED repository rather than the one a caller pointed it at via cmd.Dir or
// --repo (bead pg2-5xn2j; mechanism proven on pg2-67h4y, fixed for direct git
// exec sites on pg2-lx41y). gitenv.Hermetic is the same allowlist those git
// sites use: it drops the GIT_DIR family while leaving gh's own needs (PATH,
// HOME, GH_*, proxies, SSH) untouched.
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
