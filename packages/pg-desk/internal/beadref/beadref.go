// Package beadref resolves a beads-tracker issue-type entity — an anchor,
// feedback-cycle, or review-request bead, or a title-adopted (no-metadata)
// merge-request bead — to the (repo, entityID) of the PR it is about.
// cmd/pg-desk/run.go's "issue" case (Phase 10, docket pg2-2j5ac.34) calls
// this to resolve a triggering bead id BEFORE re-running stage 2
// (interpret) for its linked PR: it never invokes internal/gather (which
// rejects any entityType other than "pr" outright — see that package's own
// doc comment) and is not itself the sync stage.
//
// Classification reuses internal/sync.ClassifyBead verbatim — the SAME
// title/metadata recognition rules the sync stage's forward direction (PR
// -> bead) uses, applied here in reverse (bead -> PR) [design: 7.2, 7.5;
// this docket's Binding decisions: "The bead-to-PR match uses the SAME
// keys this docket's sync packet uses to recognize a bead"]. This package
// does not implement a second title/metadata parser for the same shapes.
package beadref

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	desksync "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/sync"
)

// issueShower is the subset of `pg-connector issue show` this package
// depends on, defined locally (rather than a concrete type) so tests can
// inject a fake implementation without a real pg-connector subprocess on
// $PATH — mirrors internal/pipeline/pipeline.go's own gatherer/syncer
// local-interface pattern, one layer down.
type issueShower interface {
	Show(ctx context.Context, id string) (title string, metadata map[string]string, err error)
}

// Resolver resolves a bead id to the (repo, entityID) of the PR it is
// about.
type Resolver struct {
	show issueShower
}

// NewResolver constructs a Resolver backed by a real
// `pg-connector issue show` call, pinned to cfg's beads workspace/backend
// exactly like internal/sync's own issueClient
// (cmd/pg-connector/issue.go's `--backend` flag;
// PG_CONNECTOR_ISSUE_BEADS_DIR).
func NewResolver(cfg *config.Config) *Resolver {
	return &Resolver{show: &connectorIssueShower{cfg: cfg}}
}

// ResolvePR resolves beadID's CURRENT title/metadata (read fresh via
// `pg-connector issue show`, never cached — cmd/pg-desk/run.go's "issue"
// case does not go through internal/gather.Gatherer.Gather, which rejects
// any entity type other than "pr" outright) to the (repo, entityID) of the
// PR it is about. entityID is the qualified "<repo>#<n>" form
// cmd/pg-desk/desk.go's resolvePRRef and internal/pipeline/internal/store
// both use.
//
// Returns a clear, non-panicking error when the bead cannot be shown, or
// when its title/metadata match none of the three bead shapes (anchor,
// feedback-cycle, review-request) nor the title-adopted merge-request
// fallback [Binding decisions; acceptance criteria: "A bead whose metadata
// and title both fail to match any known shape returns a clear,
// non-panicking error"].
func (r *Resolver) ResolvePR(ctx context.Context, beadID string) (repo, entityID string, err error) {
	title, metadata, err := r.show.Show(ctx, beadID)
	if err != nil {
		return "", "", fmt.Errorf("beadref: show bead %s: %w", beadID, err)
	}
	_, repo, prNumber, ok := desksync.ClassifyBead(title, metadata)
	if !ok {
		return "", "", fmt.Errorf(
			"beadref: bead %s (title %q) matches no known bead shape (anchor, feedback-cycle, review-request, or a title-adopted merge-request)",
			beadID, title,
		)
	}
	return repo, repo + "#" + strconv.Itoa(prNumber), nil
}

// pgConnectorBinary/execCmdFactory mirror internal/gather and
// internal/sync's own identical constants/vars, one more layer (this
// package's own `issue show` call) — never a compile-time import of
// packages/pg-connector (D10). See those packages' doc comments for why
// each exec-capable package keeps its own private copy rather than a
// shared one.
const pgConnectorBinary = "pg-connector"

var execCmdFactory = exec.CommandContext

// connectorIssueShower is the production issueShower: it execs
// `pg-connector issue show <id>` and decodes the result's title/metadata.
type connectorIssueShower struct {
	cfg *config.Config
}

// issueShowResult is the minimal subset of schema.Issue this package
// decodes for itself — Title/Metadata only, the bead-shape recognition
// inputs ClassifyBead needs — mirroring internal/gather's own
// workBeadEntity hand-decode precedent (D10 is about exec, not import;
// staying off pkg/schema keeps this package decoupled from pg-connector's
// Go API, talking to it only over the CLI/wire surface).
type issueShowResult struct {
	Title    string            `json:"title"`
	Metadata map[string]string `json:"metadata"`
}

// Show execs `pg-connector issue show <id>` and classifies its outcome per
// pg-connector's own 0/4/1 targeted-op exit-code scheme
// (cmd/pg-connector/issue.go's own doc comment: "Each of these four verbs
// ... uses the Tier-1 targeted-op exit-code scheme (0/4/1)") — mirroring
// internal/gather/gather.go's targetedCall and internal/sync/connector.go's
// issueClient.targetedCall exactly, one layer further (issue show rather
// than pr show or issue create/update/transition).
func (c *connectorIssueShower) Show(ctx context.Context, id string) (string, map[string]string, error) {
	args := []string{"issue", "show", id}
	args = append(args, c.backendFlag()...)

	cmd := execCmdFactory(ctx, pgConnectorBinary, args...)
	if env := c.issueBeadsDirEnv(); len(env) > 0 {
		base := cmd.Env
		if base == nil {
			base = os.Environ()
		}
		cmd.Env = append(append([]string{}, base...), env...)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()
	exitCode := 0
	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			return "", nil, fmt.Errorf("beadref: exec pg-connector %v: %w", args, runErr)
		}
	}

	switch exitCode {
	case 0:
		var env wireEnvelope
		if decErr := json.Unmarshal(stdout.Bytes(), &env); decErr != nil {
			return "", nil, fmt.Errorf("beadref: decode pg-connector %v stdout: %w", args, decErr)
		}
		var res issueShowResult
		if len(env.Result) > 0 {
			if decErr := json.Unmarshal(env.Result, &res); decErr != nil {
				return "", nil, fmt.Errorf("beadref: decode pg-connector %v result: %w", args, decErr)
			}
		}
		return res.Title, res.Metadata, nil
	case 4:
		return "", nil, fmt.Errorf("beadref: pg-connector %v: not_found", args)
	default:
		return "", nil, fmt.Errorf("beadref: pg-connector %v: exit %d: %s", args, exitCode, wireErrorMessage(stdout.Bytes()))
	}
}

// backendFlag pins dispatch to cfg.AgentTrackerBackend when configured —
// design section 7.5's "pinned to agent_tracker_backend", mirroring
// internal/sync's own issueClient.backendFlag exactly.
func (c *connectorIssueShower) backendFlag() []string {
	if c.cfg == nil || c.cfg.AgentTrackerBackend == "" {
		return nil
	}
	return []string{"--backend", c.cfg.AgentTrackerBackend}
}

// issueBeadsDirEnv builds the extraEnv slice this call carries:
// PG_CONNECTOR_ISSUE_BEADS_DIR=<the configured repo's beads_dir>, and
// nothing at all when it is unset — mirroring internal/gather and
// internal/sync's own identical helper exactly (design sections 7.3, 7.5:
// "the workspace variable rule gather already follows").
func (c *connectorIssueShower) issueBeadsDirEnv() []string {
	if c.cfg == nil || len(c.cfg.Repos) == 0 {
		return nil
	}
	dir := c.cfg.Repos[0].BeadsDir
	if dir == "" {
		return nil
	}
	return []string{"PG_CONNECTOR_ISSUE_BEADS_DIR=" + dir}
}

// wireEnvelope/wireError mirror internal/gather/gather.go's own decode
// shapes for pkg/scriptout's Response envelope.
type wireEnvelope struct {
	Result json.RawMessage `json:"result"`
	Error  *wireError      `json:"error"`
}

type wireError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func wireErrorMessage(stdout []byte) string {
	var env wireEnvelope
	if err := json.Unmarshal(stdout, &env); err == nil && env.Error != nil {
		return env.Error.Code + ": " + env.Error.Message
	}
	return strings.TrimSpace(string(stdout))
}
