// resolver.go: resolves a PR id to the (repo, head branch) pair the ported
// GitHub Actions client needs — `gh run list` filters by branch, not PR
// [carry-over basis, per this packet's contract's PRResolver hook].
//
// Freedom-boundary decision, recorded per this packet's contract (§4
// binding decisions): the default choice is to COMPOSE pg-connector's own
// "pr show <id>" verb rather than reach around it to talk to GitHub
// directly, by analogy to §4.4's "use pg-connector's own capability verbs
// rather than talking to backend systems directly for cross-cutting
// judgment" principle — this backend is not itself a standalone
// attention/search plugin, but the same reasoning applies: this backend
// already knows the PR id, and the pr capability's own "show" op already
// returns both Repo and Branch on schema.PR, so re-deriving them by parsing
// the pr backend's own id convention (and separately re-implementing a
// GitHub branch lookup) would duplicate logic the pr backend already owns
// and would silently drift if that backend's id convention or branch
// resolution ever changed. Composing `pg-connector pr show <id>` as a
// subprocess is not impractical for this backend's own process boundary
// (a one-shot exec is exactly this backend's own existing shape for `gh`),
// so the fallback ("a minimal direct GitHub branch lookup") named in the
// freedom boundary is not exercised here.
package internal

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// PRResolver resolves a PR id to the repo and head branch GitHub Actions
// needs to enumerate its runs. The production implementation
// (execPRResolver) composes pg-connector's own "pr show" verb; tests inject
// a fake.
type PRResolver interface {
	Resolve(ctx context.Context, prID string) (repo, branch string, err error)
}

// execPRResolver is the production PRResolver: it shells out to the
// pg-connector umbrella binary's own "pr show <id>" verb (on PATH) and
// decodes its wire-shaped stdout, the same envelope every scriptout backend
// writes (pkg/scriptout.Response) — reportPrTargetedOutcome in
// cmd/pg-connector/pr.go writes exactly this shape.
type execPRResolver struct {
	// binary is the pg-connector executable name/path. Overridable by
	// tests; production wiring leaves it at "pg-connector" and relies on
	// PATH, matching how this backend already relies on PATH for `gh`.
	binary string
}

// newExecPRResolver returns the production PRResolver.
func newExecPRResolver() *execPRResolver {
	return &execPRResolver{binary: "pg-connector"}
}

// prShowResult is the slice of schema.PR's fields this resolver needs.
// Decoded directly rather than importing pkg/schema for one struct's worth
// of fields shared with a CLI's own JSON output — either is fine since
// pkg/schema is shared surface; this keeps the decode-site minimal and
// explicit about exactly what's consumed.
type prShowResult struct {
	Repo   string `json:"repo"`
	Branch string `json:"branch"`
}

func (r *execPRResolver) Resolve(ctx context.Context, prID string) (string, string, error) {
	cmd := exec.CommandContext(ctx, r.binary, "pr", "show", prID)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()

	if stdout.Len() == 0 {
		if runErr != nil {
			return "", "", fmt.Errorf("pg-connector-ci-github-actions: %s pr show %s: %w (stderr: %s)",
				r.binary, prID, runErr, strings.TrimSpace(stderr.String()))
		}
		return "", "", fmt.Errorf("pg-connector-ci-github-actions: %s pr show %s: no output", r.binary, prID)
	}

	var resp scriptout.Response
	if err := json.Unmarshal(stdout.Bytes(), &resp); err != nil {
		return "", "", fmt.Errorf("pg-connector-ci-github-actions: decode %s pr show %s response: %w", r.binary, prID, err)
	}
	if resp.Error != nil {
		return "", "", scriptout.WrapError(sentinelForWireCode(resp.Error.Code),
			fmt.Sprintf("%s pr show %s: %s", r.binary, prID, resp.Error.Message))
	}

	var pr prShowResult
	if err := json.Unmarshal(resp.Result, &pr); err != nil {
		return "", "", fmt.Errorf("pg-connector-ci-github-actions: decode %s pr show %s result: %w", r.binary, prID, err)
	}
	if pr.Repo == "" || pr.Branch == "" {
		return "", "", fmt.Errorf("pg-connector-ci-github-actions: %s pr show %s returned no repo/branch", r.binary, prID)
	}
	return pr.Repo, pr.Branch, nil
}

// sentinelForWireCode maps a wire-level error code (pkg/scriptout's closed
// five-value taxonomy, §4.2) to its Go sentinel. pkg/scriptout's own
// equivalent (sentinelForCode) is unexported, so this is a small,
// deliberate local duplication of the same five-case mapping rather than a
// new shared export — the taxonomy is closed and unlikely to change
// independently of pkg/scriptout's own Err* sentinels.
func sentinelForWireCode(code string) error {
	switch code {
	case "not_found":
		return scriptout.ErrNotFound
	case "unauthenticated":
		return scriptout.ErrUnauthenticated
	case "unknown_op":
		return scriptout.ErrUnknownOp
	case "version_mismatch":
		return scriptout.ErrVersionMismatch
	default:
		return scriptout.ErrUnavailable
	}
}
