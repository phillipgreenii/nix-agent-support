package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/sync"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/beads"
)

// claudeSpawner is the production sync.Spawner: it delegates review PRODUCTION
// to a spawned `claude -p` running the pg-pr-review-orchestrator (the daemon,
// being Go, cannot run the Task-tool orchestrator itself — design §2.3.1).
//
// The spawned orchestrator adds a worktree, fans out reviewer subagents, stages
// a Draft via `pg-pr review draft`, and reports the checked-out head SHA on
// stdout as a JSON line {"head_sha":"..."}. A non-zero exit (e.g. claude/max
// mode unavailable) surfaces as an error so the hook fails gracefully.
type claudeSpawner struct {
	// bin is the claude binary; defaults to "claude" when empty.
	bin string
	// repoPath maps a repo remote → monorepo path so the orchestrator runs in
	// the right checkout.
	repoPath map[string]string
}

func newClaudeSpawner(cfg *config.Config) *claudeSpawner {
	rp := make(map[string]string, len(cfg.Repos))
	for _, r := range cfg.Repos {
		if r.Path != "" {
			rp[r.Remote] = r.Path
		}
	}
	return &claudeSpawner{bin: cfg.ClaudeBin, repoPath: rp}
}

// resolveBin returns bin unchanged when non-empty, or "claude" when empty so
// callers that have no explicit path fall back to the PATH-resolved binary.
func resolveBin(bin string) string {
	if bin == "" {
		return "claude"
	}
	return bin
}

// claudeArgs builds the argv (after the binary) for the headless review spawn.
//
// The daemon runs claude non-interactively, so it MUST bypass permission
// prompts: with the default permission mode a headless `claude -p` cannot grant
// the pg-pr-review-orchestrator agent the tools it needs (Bash for the worktree,
// `pg-pr review draft`, Edit), so the run produces nothing and the hook reports
// "no Draft staged". bypassPermissions is the human-less-worker mode (the same
// mode ccpool uses for its workers). pg2-jpfw.2.
//
// NOTE: this is the interim fix on the synchronous `claude -p` path; a follow-up
// (pg2-jpfw.9) replaces this Spawner with ccpool for autonomous handling +
// tmux-attach monitorability.
func claudeArgs(prompt string) []string {
	// --model sonnet forces the whole orchestration onto Sonnet (matching every
	// review agent def's `model: sonnet`). Without it, a headless `claude -p`
	// runs on the default model (Opus), which is ~3x slower and unnecessary for
	// this delegating orchestrator.
	return []string{"-p", prompt, "--permission-mode", "bypassPermissions", "--model", "sonnet"}
}

func (s *claudeSpawner) Produce(ctx context.Context, ref sync.ReviewRef) (string, error) {
	bin := resolveBin(s.bin)
	ownership := "team"
	if ref.Mine {
		ownership = "mine"
	}
	prompt := fmt.Sprintf(
		"Run the pg-pr-review-orchestrator for %s#%d (ownership=%s, bead=%s). "+
			"After staging the review, print a single JSON line to stdout: "+
			`{"head_sha":"<the SHA the worktree was checked out at>"}.`,
		ref.Repo, ref.Number, ownership, ref.BeadID,
	)

	cmd := exec.CommandContext(ctx, bin, claudeArgs(prompt)...)
	if dir := s.repoPath[ref.Repo]; dir != "" {
		cmd.Dir = dir
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("claude -p (%s#%d): %w: %s", ref.Repo, ref.Number, err, strings.TrimSpace(stderr.String()))
	}
	return parseHeadSHAFromOutput(stdout.String()), nil
}

// parseHeadSHAFromOutput scans the spawned agent's stdout for a
// {"head_sha":"..."} JSON line and returns the value (last one wins). Returns
// "" when no such line is present.
func parseHeadSHAFromOutput(out string) string {
	var sha string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var v struct {
			HeadSHA string `json:"head_sha"`
		}
		if err := json.Unmarshal([]byte(line), &v); err == nil && v.HeadSHA != "" {
			sha = v.HeadSHA
		}
	}
	return sha
}

// multiRepoReviewBeads fans review-hook bd operations across every configured
// repo's own `.beads/` workspace. Draft-review beads live in the monorepo's
// workspace (per-repo bd model), so a single client cannot see all of them.
//
//   - ListReadyDraftReviews unions results from every repo's workspace.
//   - Per-bead operations route to the workspace that owns the bead. Because a
//     ready ref carries its repo, and per-bead ops (claim/close/…) act on a
//     bead we already resolved from a ref, we key the client by repo. For
//     FindDraftReviewForPR (re-review gate) the repo is the argument.
//
// Bead IDs are globally unique across workspaces in practice; when an op is
// asked for a bead whose repo is not tracked, it falls back to trying each
// workspace (rare — only the re-review reopen path can pass an unmapped id).
type multiRepoReviewBeads struct {
	// clients maps repo remote → per-repo beads client.
	clients map[string]*beads.Client
	// beadRepo maps a bead id we've seen (from a ready ref) → its repo, so
	// per-bead ops route to the right workspace.
	beadRepo map[string]string
}

func newMultiRepoReviewBeads(cfg *config.Config) *multiRepoReviewBeads {
	clients := make(map[string]*beads.Client, len(cfg.Repos))
	for _, r := range cfg.Repos {
		clients[r.Remote] = beads.NewClientForRepo(r.Path) // empty path → cwd workspace
	}
	return &multiRepoReviewBeads{clients: clients, beadRepo: map[string]string{}}
}

func (m *multiRepoReviewBeads) clientForBead(id string) *beads.Client {
	if repo, ok := m.beadRepo[id]; ok {
		if c := m.clients[repo]; c != nil {
			return c
		}
	}
	// Fallback: any client (single-workspace configs are the common case).
	for _, c := range m.clients {
		return c
	}
	return beads.NewClient()
}

func (m *multiRepoReviewBeads) clientForRepo(repo string) *beads.Client {
	if c := m.clients[repo]; c != nil {
		return c
	}
	return beads.NewClient()
}

func (m *multiRepoReviewBeads) ListReadyDraftReviews(ctx context.Context) ([]beads.DraftReviewRef, error) {
	var out []beads.DraftReviewRef
	seen := map[string]struct{}{}
	for repo, c := range m.clients {
		refs, err := c.ListReadyDraftReviews(ctx)
		if err != nil {
			return nil, fmt.Errorf("list ready draft-reviews (%s): %w", repo, err)
		}
		for _, r := range refs {
			if _, dup := seen[r.ID]; dup {
				continue
			}
			seen[r.ID] = struct{}{}
			// Remember which workspace this ready bead lives in so per-bead
			// ops route correctly. Prefer the ref's own repo, else the
			// workspace we found it in.
			owner := r.Repo
			if _, ok := m.clients[owner]; !ok {
				owner = repo
			}
			m.beadRepo[r.ID] = owner
			out = append(out, r)
		}
	}
	return out, nil
}

func (m *multiRepoReviewBeads) ClaimDraftReview(ctx context.Context, id string) error {
	return m.clientForBead(id).ClaimDraftReview(ctx, id)
}

func (m *multiRepoReviewBeads) UnclaimDraftReview(ctx context.Context, id string) error {
	return m.clientForBead(id).UnclaimDraftReview(ctx, id)
}

func (m *multiRepoReviewBeads) CloseDraftReview(ctx context.Context, id, reason string) error {
	return m.clientForBead(id).CloseDraftReview(ctx, id, reason)
}

func (m *multiRepoReviewBeads) ReopenDraftReview(ctx context.Context, id string) error {
	return m.clientForBead(id).ReopenDraftReview(ctx, id)
}

func (m *multiRepoReviewBeads) DeadLetterDraftReview(ctx context.Context, id string) error {
	return m.clientForBead(id).DeadLetterDraftReview(ctx, id)
}

func (m *multiRepoReviewBeads) ReviewFailCount(ctx context.Context, id string) (int, error) {
	return m.clientForBead(id).ReviewFailCount(ctx, id)
}

func (m *multiRepoReviewBeads) BumpReviewFailCount(ctx context.Context, id string) (int, error) {
	return m.clientForBead(id).BumpReviewFailCount(ctx, id)
}

func (m *multiRepoReviewBeads) ResetReviewFailCount(ctx context.Context, id string) error {
	return m.clientForBead(id).ResetReviewFailCount(ctx, id)
}

func (m *multiRepoReviewBeads) FindDraftReviewForPR(ctx context.Context, repo string, number int) (string, bool, bool, error) {
	id, closed, found, err := m.clientForRepo(repo).FindDraftReviewForPR(ctx, repo, number)
	if found && id != "" {
		m.beadRepo[id] = repo // remember so the reopen routes to the right workspace
	}
	return id, closed, found, err
}

// compile-time: the multiplexer satisfies the review hook's bd surface.
var _ sync.ReviewBeadClient = (*multiRepoReviewBeads)(nil)
