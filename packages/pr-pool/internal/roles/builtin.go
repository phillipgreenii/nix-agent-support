package roles

import (
	"text/template"

	"github.com/phillipgreenii/pr-pool/internal/budget"
	"github.com/phillipgreenii/pr-pool/internal/prompt"
	"github.com/phillipgreenii/pr-pool/internal/query"
)

// BuiltinParams carries the scalars the built-in roles need from config defaults.
type BuiltinParams struct {
	WorktreeDir   string
	SkillMD       string
	WorkerSkillMD string
	MaxFeedback   int
	MaxWorker     int
	WorkerBudget  budget.Budget
}

// feedbackPromptBody / workerPromptBody are the task prompts (worker rails removed —
// they are injected by the authorship preamble). Re-expressed from the former
// roles.feedbackNudge / workerNudge as text/template.
const feedbackPromptBody = `Read {{.SkillMD}} and process process-feedback cycle {{.BeadID}}: claim it, read its feedback children (bd children {{.BeadID}}), resolve the parent PR bead and review the PR's existing open work beads (bd children <PR> --status=open). For each feedback, create a work bead (task/bug) as a child of the PR bead, discovered-from the feedback, and label it worker-ready (bd update <work> --add-label worker-ready) so the worker role will pick it up — but if that work matches an existing open work bead, link/update it (ensuring it is labeled worker-ready) instead of creating a duplicate. Do NOT apply fixes and do NOT work the new work beads. Close each feedback bead, then close the cycle with a one-line summary.`

const workerPromptBody = `Read {{.SkillMD}} and implement work bead {{.BeadID}}. Claim it (bd update {{.BeadID}} --claim). Resolve its PR + head branch bead-first from the parent merge-request bead's metadata (repo, pr_number, branch — no gh needed). Work in a clean isolated git worktree for that branch under {{.WorktreeDir}} (never start or leave it dirty), implement the change the bead describes, and commit it. Push ONLY if the bead's instructions say to, following the push rules in the preamble. Record what you did with bd comment FIRST, then end by EITHER closing the bead (bd close {{.BeadID}} — including when the work is already present at HEAD) OR, if handing it back, unclaiming it (bd update {{.BeadID}} --status=open --assignee=""). NEVER leave the bead in_progress; do not push by default. You are running autonomously with no human available: do NOT use the AskUserQuestion tool — it is disabled and will be denied. If you would otherwise ask, instead proceed with your best judgment; if a decision genuinely needs a human, record it with bd comment on the bead and continue or hand the bead back.`

func mustParse(name, body string) *template.Template {
	t, err := prompt.Parse(name, body)
	if err != nil {
		panic("roles: built-in prompt failed to parse: " + err.Error())
	}
	return t
}

// BuiltinRoleSet returns the in-Go default role set (feedback then worker), identical
// in behavior to today. It is also what config/example.go serializes to TOML.
func BuiltinRoleSet(p BuiltinParams) RoleSet {
	return RoleSet{
		{
			Name: "feedback", Type: "ccpool", Cap: p.MaxFeedback, Enabled: true,
			Query: query.BeadsReady{Labels: []string{"mine"}, ExcludeLabels: []string{"human"},
				TitlePrefix: "process-feedback:", ItemType: "task"},
			CCPool: &CCPoolConfig{
				Actor: "pgii-pool__process-feedback", SkillMD: p.SkillMD,
				Completion: CloseOnly, OnFailure: Unclaim, OnDispatchFail: DispatchUnclaim,
				AuthorshipGuard: false, PromptBody: feedbackPromptBody, Prompt: mustParse("feedback", feedbackPromptBody),
				Budget: budget.Budget{}, // unlimited => no watchdog
			},
		},
		{
			Name: "worker", Type: "ccpool", Cap: p.MaxWorker, Enabled: true,
			Query: query.BeadsReady{Labels: []string{"worker-ready"}, ExcludeLabels: []string{"human"}},
			CCPool: &CCPoolConfig{
				Actor: "pgii-pool__worker", SkillMD: p.WorkerSkillMD,
				Completion: CloseOrHandback, OnFailure: AddHuman, OnDispatchFail: DispatchLeave,
				AuthorshipGuard: true, PromptBody: workerPromptBody, Prompt: mustParse("worker", workerPromptBody),
				Budget: p.WorkerBudget,
			},
		},
	}
}
