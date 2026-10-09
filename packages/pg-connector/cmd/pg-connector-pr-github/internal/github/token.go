package github

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-pr-github/internal/gitenv"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// TokenSource retrieves a GitHub auth token. The default reads gh's own auth;
// alternative sources (env, file, GitHub App) can be swapped in without
// touching callers.
type TokenSource interface {
	Token(ctx context.Context) (string, error)
}

// envTokenSource returns the first non-empty of the named env vars.
type envTokenSource struct{ vars []string }

func (e envTokenSource) Token(_ context.Context) (string, error) {
	for _, v := range e.vars {
		if t := strings.TrimSpace(os.Getenv(v)); t != "" {
			return t, nil
		}
	}
	return "", nil // empty (not an error) → chain falls through
}

// ghCLITokenSource execs `gh auth token`. CRITICAL: it strips GH_TOKEN/
// GITHUB_TOKEN from the child env, otherwise `gh auth token` just echoes that
// env value instead of reading the stored (keychain) credential. This is a
// minimal, SEPARATE exec from cliGHRunner (which injects GH_TOKEN) — keeping
// them separate avoids a chicken-and-egg.
type ghCLITokenSource struct{}

// ghAuthTokenCommand builds the token resolver's own `gh auth token` command.
// Split out from Token so the constructed *exec.Cmd can be asserted on
// directly in tests without executing gh.
//
// Like cliGHRunner.command (ghexec.go), this passes the child env through
// gitenv.Hermetic: `gh auth token` is account-scoped rather than
// repository-scoped, so a leaked GIT_DIR cannot make it answer about the
// wrong repo, but it is still the same defect shape (bead pg2-5xn2j) — an
// os.Environ() passthrough hands the whole GIT_* family to a gh child for no
// reason gh needs.
func ghAuthTokenCommand(ctx context.Context) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "gh", "auth", "token")
	cmd.Env = envWithoutGHToken(gitenv.Hermetic(os.Environ()))
	// See scriptout.DefaultWaitDelay's doc comment [bead pg2-332z8 #13].
	cmd.WaitDelay = scriptout.DefaultWaitDelay
	return cmd
}

// Token execs `gh auth token` and returns its stdout. On failure it surfaces
// the subprocess's actual stderr (via *exec.Cmd.Output's ExitError.Stderr,
// populated automatically because cmd.Stderr is left nil) instead of the
// bare "exit status N" .Output() would otherwise leave callers with — every
// credential failure used to collapse to a generic "run gh auth login"
// message regardless of the real cause (bead pg2-y23d4 #32).
//
// A lookup that never finished (killed at the context deadline or by a
// signal, or stuck past WaitDelay) says nothing about the credential, so it
// wraps errTokenLookupInterrupted and cliGHRunner.command keeps it out of the
// auth-invalid class [bead pg2-ev2uf]. Only a lookup that ran to completion
// and exited non-zero is left for the caller to treat as a credential problem.
func (ghCLITokenSource) Token(ctx context.Context) (string, error) {
	out, err := ghAuthTokenCommand(ctx).Output()
	if err != nil {
		if tokenLookupInterrupted(ctx, err) {
			return "", fmt.Errorf("gh auth token: %w: %w", errTokenLookupInterrupted, err)
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			if st := strings.TrimSpace(string(exitErr.Stderr)); st != "" {
				// Capped [bead pg2-332z8 #26]: see scriptout.TruncateForFold.
				return "", fmt.Errorf("gh auth token: %s: %w", scriptout.TruncateForFold(exitErr.Stderr), err)
			}
		}
		return "", fmt.Errorf("gh auth token: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// errTokenLookupInterrupted marks a `gh auth token` that did not run to
// completion: the per-call budget (context deadline) or a signal killed it, or
// it outlived cmd.WaitDelay. The credential was never judged, so this is an
// availability problem, not an authentication one [bead pg2-ev2uf].
var errTokenLookupInterrupted = errors.New("gh credential lookup did not complete")

// tokenLookupInterrupted reports whether err from running `gh auth token`
// means the process never finished, as opposed to finishing with a non-zero
// exit. A process ended by a signal has ExitCode() == -1.
func tokenLookupInterrupted(ctx context.Context, err error) bool {
	if ctx.Err() != nil || errors.Is(err, exec.ErrWaitDelay) {
		return true
	}
	var exitErr *exec.ExitError
	return errors.As(err, &exitErr) && exitErr.ExitCode() == -1
}

// chainTokenSource returns the first source that yields a non-empty token.
type chainTokenSource struct{ sources []TokenSource }

func (c chainTokenSource) Token(ctx context.Context) (string, error) {
	var lastErr error
	for _, s := range c.sources {
		t, err := s.Token(ctx)
		if err != nil {
			lastErr = err
			continue
		}
		if t != "" {
			return t, nil
		}
	}
	if lastErr != nil {
		return "", lastErr
	}
	return "", fmt.Errorf("no gh token available (set GH_TOKEN or run `gh auth login`)")
}

// defaultTokenSource honors an existing GH_TOKEN/GITHUB_TOKEN, else reads gh's
// stored credential.
func defaultTokenSource() TokenSource {
	return chainTokenSource{sources: []TokenSource{
		envTokenSource{vars: []string{"GH_TOKEN", "GITHUB_TOKEN"}},
		ghCLITokenSource{},
	}}
}

// ghEnvVarsToStrip enumerates every gh CLI environment variable this
// package strips from a child gh process (bead pg2-y23d4 #21):
//
//   - GH_TOKEN / GITHUB_TOKEN: the credential this package itself resolves
//     and re-injects (via envWithGHToken) or reads (ghAuthTokenCommand) —
//     an ambient value must not survive, or `gh auth token` just echoes it
//     back instead of reading the stored credential.
//   - GH_ENTERPRISE_TOKEN / GITHUB_ENTERPRISE_TOKEN: the enterprise-host
//     token pair gh prefers over GH_TOKEN whenever GH_HOST names an
//     enterprise host. Leaving these in place lets an ambient enterprise
//     credential silently outrank the token this package resolved.
//   - GH_HOST: which host gh talks to.
//   - GH_REPO: which repository gh targets — an inherited value would
//     override an explicit --repo a caller passes (the ci backend always
//     passes one).
//   - GH_CONFIG_DIR: which config/credential store gh reads.
//
// gitenv.Hermetic (applied alongside this) only filters the GIT_*-prefixed
// family and does not help here — these are gh's own variables, not git's.
var ghEnvVarsToStrip = []string{
	"GH_TOKEN",
	"GITHUB_TOKEN",
	"GH_ENTERPRISE_TOKEN",
	"GITHUB_ENTERPRISE_TOKEN",
	"GH_HOST",
	"GH_REPO",
	"GH_CONFIG_DIR",
}

// envWithoutGHToken returns env with every entry in ghEnvVarsToStrip
// removed.
func envWithoutGHToken(env []string) []string {
	out := env[:0:0]
	for _, kv := range env {
		if hasStrippedGHEnvPrefix(kv) {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// hasStrippedGHEnvPrefix reports whether kv (a "NAME=value" environment
// entry) names one of ghEnvVarsToStrip.
func hasStrippedGHEnvPrefix(kv string) bool {
	for _, name := range ghEnvVarsToStrip {
		if strings.HasPrefix(kv, name+"=") {
			return true
		}
	}
	return false
}

// envWithGHToken returns env with GH_TOKEN/GITHUB_TOKEN stripped and a single
// GH_TOKEN=tok appended.
func envWithGHToken(env []string, tok string) []string {
	return append(envWithoutGHToken(env), "GH_TOKEN="+tok)
}
