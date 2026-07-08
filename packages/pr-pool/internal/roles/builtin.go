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

// reviewPromptBody is the ported pg-pr-review-orchestrator workflow, adapted to
// the pr-pool executor's worktree. It is self-contained (no SkillMD). The PR
// coords are templated directly from the review-pr bead's metadata (stamped by
// the pr-pool ACL: repo/pr_number/branch/head_sha). Because the executor mints
// the worktree branch off the monorepo's HEAD — NOT the PR head — the agent MUST
// fetch + check out the PR head before reviewing (NH4). It posts back by calling
// pg-pr directly (pg-pr owns the GitHub write) and completes by closing the bead.
const reviewPromptBody = `Review pull request {{index .Item.Metadata "repo"}}#{{index .Item.Metadata "pr_number"}} and post the review back through pg-pr. Claim this bead first: bd update {{.BeadID}} --claim.

You are in a fresh git worktree at {{.WorktreeDir}} on a scratch branch cut from the monorepo's default HEAD — this is NOT the PR's head. Before reviewing you MUST check out the exact reviewed commit there:
  git -C {{.WorktreeDir}} fetch origin pull/{{index .Item.Metadata "pr_number"}}/head
  git -C {{.WorktreeDir}} checkout {{index .Item.Metadata "head_sha"}}
(head branch: {{index .Item.Metadata "branch"}}). If the fetch or checkout fails, do NOT review the wrong tree — hand the bead back (see below).

Review the PR's changes against its base: read the diff, and produce findings as inline comments (each keyed to file path + line) plus a short overall summary. This is a READ-ONLY review: do not modify, commit, or push any code.

Post the review back by calling pg-pr directly (never call gh — pg-pr owns the GitHub write). Submit via pg-pr review submit {{index .Item.Metadata "pr_number"}} --repo {{index .Item.Metadata "repo"}}, piping a review JSON on stdin (see pg-pr review --help). That JSON MUST include "head_sha": "{{index .Item.Metadata "head_sha"}}" so pg-pr anchors the inline comments to the exact reviewed commit (an unanchored post 422s if the PR head has advanced). Post a PENDING review (no approve/request-changes event).

Record a one-line result with bd comment {{.BeadID}} FIRST, then complete by EITHER closing the bead (bd close {{.BeadID}}) once the review is posted, OR, if you could not review/post, handing it back by unclaiming it (bd update {{.BeadID}} --status=open --assignee=""). NEVER leave the bead in_progress. You are running autonomously with no human available: the AskUserQuestion tool is disabled — use your best judgment and record any decision that needs a human with bd comment.`

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
			Query: query.BeadsReady{
				Labels: []string{"mine"}, ExcludeLabels: []string{"human"},
				TitlePrefix: "process-feedback:", ItemType: "task",
			},
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
		{
			// review dispatches the review-pr beads the pr-pool ACL projects from
			// pg-pr (bead pg2-ynhr.2/.3). The "review-pr: " title prefix uniquely
			// selects them (distinct from feedback's "process-feedback:" and from
			// pg-pr's own "draft-review:" beads), so no positive label is needed.
			// AuthorshipGuard is FALSE: reviews cover teammate PRs too, so the
			// guard's "author is me + my branch" assertion must NOT gate them.
			Name: "review", Type: "ccpool", Cap: p.MaxWorker, Enabled: true,
			Query: query.BeadsReady{
				ExcludeLabels: []string{"human"},
				TitlePrefix:   "review-pr: ", ItemType: "task",
			},
			CCPool: &CCPoolConfig{
				Actor: "pgii-pool__review", SkillMD: "",
				Completion: CloseOrHandback, OnFailure: AddHuman, OnDispatchFail: DispatchLeave,
				AuthorshipGuard: false, PromptBody: reviewPromptBody, Prompt: mustParse("review", reviewPromptBody),
				Budget: p.WorkerBudget,
			},
		},
	}
}
