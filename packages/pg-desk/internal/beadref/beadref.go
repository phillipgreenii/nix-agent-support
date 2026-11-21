// Package beadref resolves a beads-tracker issue-type entity — an anchor,
// feedback-cycle, or review-request bead, or a title-adopted (no-metadata)
// merge-request bead — to the (repo, entityID) of the PR it is about.
// cmd/pg-desk/run.go's "issue" case (Phase 10, docket pg2-2j5ac.34) calls
// this to resolve a triggering bead id BEFORE re-running stage 2
// (interpret) for its linked PR: it never invokes internal/gather (which
// rejects any entityType other than "pr" outright — see that package's own
// doc comment).
//
// Classification reuses ClassifyBead verbatim — the SAME
// title/metadata recognition rules the sync stage's forward direction (PR
// -> bead) uses, applied here in reverse (bead -> PR) [design: 7.2, 7.5;
// this docket's Binding decisions: "The bead-to-PR match uses the SAME
// keys this docket's sync packet uses to recognize a bead"]. This package
// does not implement a second title/metadata parser for the same shapes.
package beadref

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/phillipgreenii/x/cmdrun"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/client"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/connector"
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
	_, repo, prNumber, ok := ClassifyBead(title, metadata)
	if !ok {
		return "", "", fmt.Errorf(
			"beadref: bead %s (title %q) matches no known bead shape (anchor, feedback-cycle, review-request, or a title-adopted merge-request)",
			beadID, title,
		)
	}
	return repo, repo + "#" + strconv.Itoa(prNumber), nil
}

// runner is the exec seam (a cmdrun.Runner; production is connector.Run).
// Tests swap it for an in-process double.
var runner cmdrun.Runner = connector.Run

// callTimeout bounds one pg-connector invocation.
const callTimeout = 60 * time.Second

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
	backend := ""
	if c.cfg != nil {
		backend = c.cfg.AgentTrackerBackend
	}
	cl := client.Client{Run: runner, Env: c.issueBeadsDirEnv(), Timeout: callTimeout}
	raw, err := cl.Get(ctx, client.GetRequest{Type: "issue", ID: id, Backend: backend})
	if err != nil {
		if client.IsNotFound(err) {
			return "", nil, fmt.Errorf("beadref: pg-connector issue show %s: not_found", id)
		}
		return "", nil, fmt.Errorf("beadref: %w", err)
	}
	var res issueShowResult
	if len(raw) > 0 {
		if decErr := json.Unmarshal(raw, &res); decErr != nil {
			return "", nil, fmt.Errorf("beadref: decode pg-connector issue show %s result: %w", id, decErr)
		}
	}
	return res.Title, res.Metadata, nil
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
