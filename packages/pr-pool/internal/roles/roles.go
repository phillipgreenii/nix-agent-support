// Package roles is pr-pool's role registry: the per-role actor, skill, cap, and
// nudge template. The nudge text is copied verbatim from pr-pool.sh — the
// worker/feedback contracts depend on the exact wording.
package roles

import (
	"fmt"

	"github.com/phillipgreenii/pr-pool/internal/config"
)

type RoleKind int

const (
	Feedback RoleKind = iota
	Worker
)

type Role struct {
	Kind    RoleKind
	Name    string // session-name token: "feedback-processor" | "worker"
	Actor   string // BEADS_ACTOR
	SkillMD string
	Cap     int
	Enabled bool // when false, the role is skipped at discovery (no dispatches)
}

type Registry struct {
	Feedback Role
	Worker   Role
}

func NewRegistry(cfg config.Config) Registry {
	return Registry{
		Feedback: Role{
			Kind:    Feedback,
			Name:    "feedback-processor",
			Actor:   "pgii-pool__process-feedback",
			SkillMD: cfg.SkillMD,
			Cap:     cfg.MaxFeedback,
			Enabled: cfg.FeedbackEnabled,
		},
		Worker: Role{
			Kind:    Worker,
			Name:    "worker",
			Actor:   "pgii-pool__worker",
			SkillMD: cfg.WorkerSkillMD,
			Cap:     cfg.MaxWorker,
			Enabled: cfg.WorkerEnabled,
		},
	}
}

// SessionName builds the per-bead ccpool session name: <prefix><role>-<beadid>,
// e.g. "pr-pool-worker-zr-lweh.2".
func (r Role) SessionName(prefix, beadID string) string {
	return prefix + r.Name + "-" + beadID
}

// Nudge returns the role's prompt for the given bead. worktreeDir is only used
// by the worker template.
func (r Role) Nudge(beadID, worktreeDir string) string {
	switch r.Kind {
	case Worker:
		return fmt.Sprintf(workerNudge, r.SkillMD, beadID, beadID, beadID, worktreeDir, beadID, beadID)
	default:
		return fmt.Sprintf(feedbackNudge, r.SkillMD, beadID)
	}
}

// feedbackNudge args: SKILL_MD, cycle id.
const feedbackNudge = `Read %s and process process-feedback cycle %s: claim it, read its feedback children (bd children %[2]s), resolve the parent PR bead and review the PR's existing open work beads (bd children <PR> --status=open). For each feedback, create a work bead (task/bug) as a child of the PR bead, discovered-from the feedback, and label it worker-ready (bd update <work> --add-label worker-ready) so the worker role will pick it up — but if that work matches an existing open work bead, link/update it (ensuring it is labeled worker-ready) instead of creating a duplicate. Do NOT apply fixes and do NOT work the new work beads. Close each feedback bead, then close the cycle with a one-line summary.`

// workerNudge args: WORKER_SKILL_MD, id, id, id, WORKTREE_DIR, id, id.
const workerNudge = `Read %s and implement work bead %s. Claim it (bd update %s --claim). Resolve its PR + head branch bead-first from the parent merge-request bead's metadata (repo, pr_number, branch — no gh needed); assert metadata.author is me AND the branch starts with 'phillipg.'. If you cannot resolve the PR, it is not mine, or the branch is not phillipg.-prefixed, make NO changes, comment why, and add the human label (bd update %s --add-label human). Otherwise work in a clean isolated git worktree for that branch under %s (never start or leave it dirty), implement the change the bead describes, and commit it. Push ONLY if the bead's instructions say to (git push or git push --force-with-lease; NEVER git push --force). Record what you did with bd comment FIRST, then end by EITHER closing the bead (bd close %s — including when the work is already present at HEAD) OR, if handing it back, unclaiming it (bd update %s --status=open --assignee=""). NEVER leave the bead in_progress; do not push by default.`
