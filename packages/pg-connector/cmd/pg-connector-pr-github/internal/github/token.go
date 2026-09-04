package github

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-pr-github/internal/gitenv"
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
	return cmd
}

func (ghCLITokenSource) Token(ctx context.Context) (string, error) {
	out, err := ghAuthTokenCommand(ctx).Output()
	if err != nil {
		return "", fmt.Errorf("gh auth token: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
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

// envWithoutGHToken returns env with any GH_TOKEN/GITHUB_TOKEN entries removed.
func envWithoutGHToken(env []string) []string {
	out := env[:0:0]
	for _, kv := range env {
		if strings.HasPrefix(kv, "GH_TOKEN=") || strings.HasPrefix(kv, "GITHUB_TOKEN=") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// envWithGHToken returns env with GH_TOKEN/GITHUB_TOKEN stripped and a single
// GH_TOKEN=tok appended.
func envWithGHToken(env []string, tok string) []string {
	return append(envWithoutGHToken(env), "GH_TOKEN="+tok)
}
