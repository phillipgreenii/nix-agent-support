package effectpolicy

import (
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmddesc"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/evalcontract"
)

// pluginForm is one shell command form a first-party plugin skill, command or
// agent INSTRUCTS an agent to run, literalized the way an agent would type it
// (placeholders such as <id> or $WT replaced by realistic literals; <ROOT> is
// the checkout the command runs in). want is the verdict the NEW effect engine
// must reach; note records WHY a form is deliberately not approved.
type pluginForm struct {
	plugin  string
	command string
	want    evalcontract.Decision
	note    string
}

// pluginForms is the executable INVENTORY of pg2-cjfpy.2 (parent epic
// pg2-cjfpy; operator ruling, Phillip, 2026-10-04, verbatim: "any command
// which is supposed to work as part of a skill should be autoapproved"): every
// command form the agent-support marketplace plugins instruct (wayfinder-beads,
// bead-grooming, beads-lifecycle, beads-dolt-doctor, plan-decompose,
// session-wrapup, integrate-branch, pg-go-mutate, pg-ccaudit, pg-wi-flow,
// claude-extended-tool-approver, bash-scripting, behavior-docs-conformance;
// claude-activity, bash-lsp and service-daemon-checklist instruct no
// commands, and pb / pg-pr are excluded by the epic). Approve rows are the
// ruling made true; every Abstain/Reject row carries the reason it is a
// deliberate exception — a ruling collision (guardrail: listed, not
// overridden), a safety exception (R5: approve only when sure), or a form that
// is not a real command — so a future change that quietly flips one is caught
// here.
//
// SHELL STATEMENT FORMS (pg2-dbrsg, found by pg2-cjfpy.2): the forms that need
// shell machinery rather than a command schema — `X="$(cmd)"` assignments, `[ ]`
// tests, `for`/`while`/`case`, and the builtins read/shift/exit/pwd — are the
// rows after the "shell statement forms" marker below. Each approves exactly
// when every command it contains approves (an assignment's `$(...)` is graded by
// the same effect analysis as a command typed on its own). What stays
// non-approvable carries its reason in the row:
//
//   - A QUOTED VARIABLE USED AS A PATH (`git -C "$WT" ...`, `cd "$WT"`) is a
//     runtime value, and this engine does not resolve in-command variables
//     (the old engine did, through cmdparse.InCommandVars; threading that seam
//     into the effect engine is a separate piece of work, not done here), so
//     even `WT=/abs && git -C "$WT" status` abstains. Approval applies to the
//     LITERAL-path form agents can type (`git -C /abs/path status`). Skill
//     templates that say `"$WT"` should instruct a literal absolute path.
//   - A LIVE EXPANSION AS A POSITIONAL (`bd show "$ID"`, `git log $X`;
//     pg2-5ctay) is a runtime value that may start with `-` and be parsed as an
//     option the schema never modeled (X=--output=/tmp/pwn), so it abstains
//     even where the same command with a literal argument approves. The
//     exceptions are positionals after `--`, words that open with a literal
//     character (`main..$X`), commands whose options are all inert (echo,
//     true, false), and `test`/`[` operands beside a literal operator. This
//     is the same remedy as the quoted-variable path forms above: type the
//     literal value.
//   - `[[ ]]`, `(( ))`, `let`, an array-element assignment and `$((...))`
//     evaluate their operands as ARITHMETIC, which runs a command substitution
//     found in variable text; `[ -v ]`/`[ -R ]` take a name that may carry an
//     array subscript (same hazard). None is approved.
//   - A persistent assignment of a shell-behaviour variable (IFS, CDPATH, ...)
//     or of GIT_DIR/GIT_INDEX_FILE changes every later command's meaning.
//   - `date`, `tr` have no schema, so a `$(date +%F)` substitution abstains
//     until they get one (adding a schema also needs its --help hash recorded in
//     the pinned nix sandbox, which cannot be done from a dev shell).
//   - Builtins not listed (break, continue, return, set, trap, local, wait, ...)
//     have no schema and abstain; the four the skills use are modeled.
var pluginForms = []pluginForm{
	{"wayfinder-beads", "bd show pg2-abc12", evalcontract.Approve, ""},
	{"wayfinder-beads", "bd list --json", evalcontract.Approve, ""},
	{"wayfinder-beads", "bd search \"terms\"", evalcontract.Approve, ""},
	{"wayfinder-beads", "bd ready", evalcontract.Approve, ""},
	{"wayfinder-beads", "bd ready --parent pg2-map1 --unassigned", evalcontract.Approve, ""},
	{"wayfinder-beads", "bd ready --claim --actor sess-1", evalcontract.Approve, ""},
	{"wayfinder-beads", "bd update pg2-abc12 --claim --actor sess-1", evalcontract.Approve, ""},
	{"wayfinder-beads", "bd update pg2-abc12 --status open --assignee \"\" --actor sess-1", evalcontract.Approve, ""},
	{"wayfinder-beads", "bd update pg2-abc12 --status blocked --assignee \"\" --actor sess-1", evalcontract.Approve, ""},
	{"wayfinder-beads", "bd update pg2-abc12 --add-label a,b", evalcontract.Approve, ""},
	{"wayfinder-beads", "bd update pg2-map1 --body-file /tmp/body.md", evalcontract.Approve, ""},
	{"wayfinder-beads", "bd comment pg2-abc12 \"the answer\"", evalcontract.Approve, ""},
	{"wayfinder-beads", "bd close pg2-abc12 --reason \"one-line gist\" --actor sess-1", evalcontract.Approve, ""},
	{"wayfinder-beads", "bd create \"title\" -t task -p 2 -d \"body\"", evalcontract.Approve, ""},
	{"wayfinder-beads", "bd create \"title\" --parent pg2-map1", evalcontract.Approve, ""},
	{"wayfinder-beads", "bd create \"dest\" -t epic -l wayfinder:map --body-file /tmp/body.md --actor sess-1", evalcontract.Approve, ""},
	{"wayfinder-beads", "bd create \"question\" --parent pg2-map1 --no-inherit-labels -l wayfinder:question -d \"## Question\" --actor sess-1", evalcontract.Approve, ""},
	{"wayfinder-beads", "bd dep add pg2-a --blocked-by pg2-b", evalcontract.Approve, ""},
	{"wayfinder-beads", "bd dep list pg2-a", evalcontract.Approve, ""},
	{"bead-grooming", "bd list --status open --exclude-label human,has-acceptance-criteria --limit 0", evalcontract.Approve, ""},
	{"bead-grooming", "bd show pg2-abc12", evalcontract.Approve, ""},
	{"bead-grooming", "bd search \"key terms\"", evalcontract.Approve, ""},
	{"bead-grooming", "bd dep list pg2-abc12", evalcontract.Approve, ""},
	{"bead-grooming", "bd update pg2-abc12 --acceptance \"- [ ] statement one\"", evalcontract.Approve, ""},
	{"bead-grooming", "bd update pg2-abc12 --add-label has-acceptance-criteria", evalcontract.Approve, ""},
	{"bead-grooming", "bd update pg2-abc12 --append-notes \"Open questions\"", evalcontract.Approve, ""},
	{"bead-grooming", "bd update pg2-abc12 --add-label human", evalcontract.Approve, ""},
	{"bead-grooming", "bd update pg2-abc12 --title \"t\" --type task -p 2 --description \"d\"", evalcontract.Approve, ""},
	{"bead-grooming", "bd lint", evalcontract.Approve, ""},
	{"bead-grooming", "bd human list", evalcontract.Approve, ""},
	{"bead-grooming", "bd q \"quick capture\"", evalcontract.Approve, ""},
	{"beads-lifecycle", "bd list", evalcontract.Approve, ""},
	{"beads-lifecycle", "bd show pg2-abc12 --json", evalcontract.Approve, ""},
	{"beads-lifecycle", "bd update pg2-abc12 --claim --actor sess-1", evalcontract.Approve, ""},
	{"beads-lifecycle", "bd ready --claim --actor sess-1", evalcontract.Approve, ""},
	{"beads-lifecycle", "bd update pg2-abc12 --status open --assignee \"\"", evalcontract.Approve, ""},
	{"beads-lifecycle", "bd update pg2-abc12 --add-label human,worktree-review --priority 0 --append-notes \"[worktree-review 2026-10-04] note. Promoted P2->P0.\" --actor sess-1", evalcontract.Approve, ""},
	{"beads-lifecycle", "bd update pg2-abc12 --remove-label human,worktree-review --priority 2 --status open --assignee \"\" --append-notes \"[worktree-review-resolved 2026-10-04] verdict. Restored P0->P2.\" --actor sess-1", evalcontract.Approve, ""},
	{"beads-lifecycle", "bd update pg2-abc12 --remove-label worktree-review --priority 2 --append-notes \"resolved\" --actor sess-1", evalcontract.Approve, ""},
	{"beads-lifecycle", "bd close pg2-abc12 --reason \"verdict\" --actor sess-1", evalcontract.Approve, ""},
	{"beads-lifecycle", "bd dep add pg2-a --blocked-by pg2-b", evalcontract.Approve, ""},
	{"beads-lifecycle", "bd dep list pg2-a", evalcontract.Approve, ""},
	{"beads-lifecycle", "bd dep cycles", evalcontract.Approve, ""},
	{"beads-lifecycle", "bd dep add --help", evalcontract.Approve, ""},
	{"beads-lifecycle", "bd update pg2-abc12 --remove-label human --status open --assignee \"\" --actor sess-1", evalcontract.Approve, ""},
	{"beads-lifecycle", "bd ready --label human", evalcontract.Approve, ""},
	{"beads-lifecycle", "bd ready --exclude-label human --label bb", evalcontract.Approve, ""},
	{"beads-lifecycle", "bd list --parent pg2-abc12 --status all -n 0", evalcontract.Approve, ""},
	{"beads-lifecycle", "bd children pg2-abc12", evalcontract.Approve, ""},
	{"beads-lifecycle", "bd comments pg2-abc12", evalcontract.Approve, ""},
	{"beads-lifecycle", "bd list --desc-contains 'artifact' --status all -n 0 --json", evalcontract.Approve, ""},
	{"beads-lifecycle", "bd show pg2-abc12 --json --include-comments", evalcontract.Approve, ""},
	{"beads-lifecycle", "bd show pg2-abc12 --json | jq -r '(if type==\"object\" and has(\"data\") then .data else . end)[0].notes // \"\"' | rg -o 'Promoted P[0-9]->P[0-9]' | tail -1", evalcontract.Approve, ""},
	{"beads-lifecycle", "bd list --desc-contains \"artifact\" --status all -n 0 --json | jq -r '(if type==\"object\" and has(\"data\") then .data else . end)[] | \"== \\(.id) ==\"' | rg -in 'operator ruled|superseded'", evalcontract.Approve, ""},
	{"beads-lifecycle", "bd create \"title\" --deps \"discovered-from:pg2-abc12\"", evalcontract.Approve, ""},
	{"beads-lifecycle", "bd version", evalcontract.Approve, ""},
	{"beads-lifecycle", "git merge-base --is-ancestor abc1234 main", evalcontract.Approve, ""},
	{"beads-lifecycle", "git fetch --quiet origin", evalcontract.Approve, ""},
	{"beads-lifecycle", "git branch -r --contains abc1234", evalcontract.Approve, ""},
	{"beads-lifecycle", "git cherry -v main feature-x", evalcontract.Approve, ""},
	{"beads-lifecycle", "git ls-tree -r --name-only main -- docs/plan.md", evalcontract.Approve, ""},
	{"beads-lifecycle", "git grep -c -- 'symbol' main -- packages/foo", evalcontract.Approve, ""},
	{"beads-lifecycle", "git range-diff main..old main..new", evalcontract.Approve, ""},
	{"beads-lifecycle", "fd -HI artifact <ROOT>", evalcontract.Approve, ""},
	{"beads-lifecycle", "rg -uu -in 'operator ruled|superseded' -g '*artifact*' <ROOT>", evalcontract.Approve, ""},
	{"beads-lifecycle", "handoff-create --unattended --session-id sess-1 --title \"subject\" --body-file /tmp/body.md --label auto-session-wrapped", evalcontract.Approve, ""},
	{"beads-lifecycle", "handoff-create --attended --session-id sess-1 --title \"subject\" --body-file /tmp/body.md", evalcontract.Approve, ""},
	{"beads-lifecycle", "bd comment pg2-abc12 \"ABSORBED: item => pg2-def34\"", evalcontract.Approve, ""},
	{"beads-lifecycle", "bd close pg2-abc12 --reason \"handoff absorbed\" --actor sess-1", evalcontract.Approve, ""},
	{"beads-lifecycle", "bd show pg2-abc12 --json | jq -r '(if type==\"object\" and has(\"data\") then .data else . end) | .[0] | \"\\(.issue_type)\\t\\(.metadata.handed_off_from_session // \"\")\"'", evalcontract.Approve, ""},
	{"beads-dolt-doctor", "bd dolt show", evalcontract.Approve, ""},
	{"beads-dolt-doctor", "bd dolt status", evalcontract.Approve, ""},
	{"beads-dolt-doctor", "bd dolt test", evalcontract.Approve, ""},
	{"beads-dolt-doctor", "bd stats", evalcontract.Approve, ""},
	{"beads-dolt-doctor", "du -sh ./.beads/dolt", evalcontract.Approve, ""},
	{"beads-dolt-doctor", "ps -axo pid,ppid,lstart,command | grep '[d]olt sql-server'", evalcontract.Approve, ""},
	{"beads-dolt-doctor", "ps -p 123 -o command=", evalcontract.Approve, ""},
	{"beads-dolt-doctor", "lsof -nP -iTCP:25252 | grep LISTEN", evalcontract.Approve, ""},
	{"beads-dolt-doctor", "launchctl list org.nixos.beads-dolt-server", evalcontract.Approve, ""},
	{"beads-dolt-doctor", "launchctl bootout gui/501/org.nixos.beads-dolt-server", evalcontract.Abstain, "service-lifecycle change: not approved (R5; runbook step after confirming data safety)"},
	{"beads-dolt-doctor", "pkill -f 'dolt sql-server'", evalcontract.Abstain, "destructive process kill: not approved (R5; peer sessions share build commands)"},
	{"beads-dolt-doctor", "launchctl bootstrap gui/501 ~/Library/LaunchAgents/org.nixos.beads-dolt-server.plist", evalcontract.Abstain, "service-lifecycle change: not approved (R5)"},
	{"beads-dolt-doctor", "mysql -h 127.0.0.1 -P 25252 -u root --skip-password", evalcontract.Abstain, "interactive client against the shared Dolt server: arbitrary SQL, not approved"},
	{"plan-decompose", "bd list --label docket --status all -n 0 --json", evalcontract.Approve, ""},
	{"plan-decompose", "bd label remove pg2-abc12 docket", evalcontract.Approve, ""},
	{"plan-decompose", "bd dep list pg2-trig1", evalcontract.Approve, ""},
	{"plan-decompose", "bd list --parent pg2-up1 --status closed -n 0 --json", evalcontract.Approve, ""},
	{"plan-decompose", "bd update pg2-phase1 --design-file /tmp/design.md", evalcontract.Approve, ""},
	{"plan-decompose", "bd create \"title\" -t task --labels human -d \"body\"", evalcontract.Approve, ""},
	{"plan-decompose", "bd dep add pg2-trig1 --blocked-by pg2-esc1", evalcontract.Approve, ""},
	{"plan-decompose", "bd show pg2-packet1 --json | jq -c '.data[0].metadata'", evalcontract.Approve, ""},
	{"plan-decompose", "bd defer pg2-packet1", evalcontract.Approve, ""},
	{"plan-decompose", "bd undefer pg2-packet1", evalcontract.Approve, ""},
	{"plan-decompose", "bd create \"title\" -t epic --parent pg2-prog1 --no-inherit-labels --label docket,phase --design-file /tmp/slice.md --metadata '{\"pd_phase\":\"x\"}'", evalcontract.Approve, ""},
	{"plan-decompose", "bd create \"title\" -t task --parent pg2-prog1 --no-inherit-labels --label phase-trigger -p 2 -d \"Run phase-decompose\"", evalcontract.Approve, ""},
	{"plan-decompose", "bd dep add pg2-phase1 --blocked-by pg2-trig1", evalcontract.Approve, ""},
	{"plan-decompose", "bd update pg2-trig1 --append-notes \"placeholder note\"", evalcontract.Approve, ""},
	{"plan-decompose", "bd update pg2-trig1 --priority 2", evalcontract.Approve, ""},
	{"plan-decompose", "bd comment pg2-trig1 \"text\"", evalcontract.Approve, ""},
	{"plan-decompose", "bd update pg2-pk1 --set-metadata k=v", evalcontract.Approve, ""},
	{"plan-decompose", "bd update pg2-pk1 --unset-metadata pd_stale", evalcontract.Approve, ""},
	{"plan-decompose", "bd update pg2-pk1 --body-file /tmp/body.md", evalcontract.Approve, ""},
	{"plan-decompose", "bd update pg2-pk1 --set-metadata pd_curated_rev=R --set-metadata pd_stale=R", evalcontract.Approve, ""},
	{"plan-decompose", "bd update --help", evalcontract.Approve, ""},
	{"plan-decompose", "bd comment pg2-t1 --file /tmp<ROOT>rt.md", evalcontract.Approve, ""},
	{"plan-decompose", "bd comments pg2-abc12 --json", evalcontract.Approve, ""},
	{"plan-decompose", "bd list --parent pg2-doc1 --status all -n 50 --offset 50 --json", evalcontract.Approve, ""},
	{"plan-decompose", "bd close pg2-epic1 --reason \"outcome\"", evalcontract.Approve, ""},
	{"plan-decompose", "bd update pg2-doc1 --set-metadata pd_phase=released:partial", evalcontract.Approve, ""},
	{"plan-decompose", "claude-marketplace/plan-decompose/scripts/create-packet.sh --parent pg2-epic1 --title \"title\" --body-file /tmp/body.md --acceptance \"criteria\" --metadata '{}' --actor sess-1", evalcontract.Approve, ""},
	{"plan-decompose", "claude-marketplace/plan-decompose/scripts/chunk-for-bd-field.sh /tmp/in.md /tmp/out-prefix", evalcontract.Approve, ""},
	{"plan-decompose", "claude-marketplace/plan-decompose/scripts/audit-docket-label-leak.sh", evalcontract.Approve, ""},
	{"plan-decompose", "git apply --unidiff-zero /tmp/diff.patch", evalcontract.Abstain, "paths written are inside the patch: not statically modelable"},
	{"plan-decompose", "git diff --no-index /tmp/a /tmp/b", evalcontract.Approve, ""},
	{"plan-decompose", "git ls-files packages/foo", evalcontract.Approve, ""},
	{"plan-decompose", "nix build .#checks.aarch64-darwin.foo", evalcontract.Approve, ""},
	{"plan-decompose", "nix flake check", evalcontract.Approve, ""},
	{"session-wrapup", "session-mode show", evalcontract.Approve, ""},
	{"session-wrapup", "session-mode start wrap-up-session", evalcontract.Approve, ""},
	{"session-wrapup", "session-mode set-status stopping", evalcontract.Approve, ""},
	{"session-wrapup", "session-mode set-status finished", evalcontract.Approve, ""},
	{"session-wrapup", "session-mode set-status finished --handoff-bead pg2-abc12", evalcontract.Approve, ""},
	{"session-wrapup", "bd close pg2-a pg2-b --reason=\"done\" --actor sess-1", evalcontract.Approve, ""},
	{"session-wrapup", "bd create --title=\"t\" --description=\"why\" --type=task -p 2", evalcontract.Approve, ""},
	{"session-wrapup", "bd ready --claim", evalcontract.Approve, ""},
	{"session-wrapup", "bd list --status in_progress", evalcontract.Approve, ""},
	{"session-wrapup", "bd list --status in_progress --assignee me", evalcontract.Approve, ""},
	{"session-wrapup", "bd list --type=merge-request", evalcontract.Approve, ""},
	{"session-wrapup", "bd update pg2-abc12 --add-label auto-session-wrapped", evalcontract.Approve, ""},
	{"session-wrapup", "prek run --files a.go b.go", evalcontract.Approve, ""},
	{"session-wrapup", "pg-hooks run pre-commit a.go b.go", evalcontract.Approve, ""},
	{"session-wrapup", "pg-hooks status --porcelain", evalcontract.Approve, ""},
	{"session-wrapup", "pg-hooks fix", evalcontract.Approve, ""},
	{"session-wrapup", "nix build .#checks.aarch64-darwin.foo", evalcontract.Approve, ""},
	{"session-wrapup", "go test ./...", evalcontract.Approve, ""},
	{"session-wrapup", "git worktree add <ROOT>/.worktrees/x", evalcontract.Approve, ""},
	{"session-wrapup", "git -C <ROOT> rev-parse --abbrev-ref HEAD", evalcontract.Approve, ""},
	{"session-wrapup", "git -C <ROOT> status --porcelain", evalcontract.Approve, ""},
	{"session-wrapup", "git checkout main", evalcontract.Abstain, "R3: agent must not re-checkout the canonical clone on its own initiative"},
	{"session-wrapup", "git stash -u", evalcontract.Abstain, "R3: agent must not stash the canonical clone on its own initiative"},
	{"session-wrapup", "git stash pop", evalcontract.Abstain, "R3: agent must not stash the canonical clone on its own initiative"},
	{"session-wrapup", "git log @{u}..", evalcontract.Approve, ""},
	{"session-wrapup", "git log main..", evalcontract.Approve, ""},
	{"session-wrapup", "git status", evalcontract.Approve, ""},
	{"session-wrapup", "git rev-list --count @{u}..HEAD", evalcontract.Approve, ""},
	{"session-wrapup", "git rev-list --count origin/main..main", evalcontract.Approve, ""},
	{"session-wrapup", "git merge --ff-only origin/feature", evalcontract.Approve, ""},
	{"session-wrapup", "git branch -d feature", evalcontract.Approve, ""},
	{"session-wrapup", "pn workspace push", evalcontract.Approve, ""},
	{"session-wrapup", "pn workspace update", evalcontract.Approve, ""},
	{"session-wrapup", "pn workspace apply", evalcontract.Approve, ""},
	{"session-wrapup", "pn workspace doctor", evalcontract.Approve, ""},
	{"session-wrapup", "pn workspace doctor --fix", evalcontract.Abstain, "session-wrapup forbids doctor --fix (ff-merges the canonical clone); repo-base schema omits it"},
	{"session-wrapup", "pn workspace workforest list", evalcontract.Approve, ""},
	{"session-wrapup", "pn workspace workforest remove feature", evalcontract.Abstain, "pg2-4zyqf: workforest remove is NOT relieved by the 2026-09-17 operator ruling (repo-base schema omits it)"},
	{"session-wrapup", "pn workspace workforest prune", evalcontract.Approve, ""},
	{"session-wrapup", "wtdone feature --cc <ROOT>", evalcontract.Approve, ""},
	{"integrate-branch", "integrate-branch-support", evalcontract.Approve, ""},
	{"integrate-branch", "integrate-branch-support --facts", evalcontract.Approve, ""},
	{"integrate-branch", "integrate-branch-support --prek-branch-diff", evalcontract.Approve, ""},
	{"integrate-branch", "integrate-branch-support --bundle-refresh abc1234", evalcontract.Approve, ""},
	{"integrate-branch", "git config --get pgii-integrate-branch.primaryBranch", evalcontract.Approve, ""},
	{"integrate-branch", "git config pgii-integrate-branch.primaryBranch", evalcontract.Approve, ""},
	{"integrate-branch", "git config pgii-integrate-branch.strategy ff-merge-to-main", evalcontract.Abstain, "git config writes are judged per key (P16, a later phase)"},
	{"integrate-branch", "git rev-parse --abbrev-ref HEAD", evalcontract.Approve, ""},
	{"integrate-branch", "git rev-parse --git-common-dir", evalcontract.Approve, ""},
	{"integrate-branch", "git rev-parse --path-format=absolute --git-common-dir", evalcontract.Approve, ""},
	{"integrate-branch", "git -C <ROOT> rev-parse --abbrev-ref HEAD", evalcontract.Approve, ""},
	{"integrate-branch", "git -C <ROOT> status --porcelain", evalcontract.Approve, ""},
	{"integrate-branch", "git -C <ROOT>/.worktrees/wt rev-parse --git-path rebase-merge", evalcontract.Approve, ""},
	{"integrate-branch", "git -C <ROOT>/.worktrees/wt rev-parse --abbrev-ref --symbolic-full-name @{u}", evalcontract.Approve, ""},
	{"integrate-branch", "git -C <ROOT>/.worktrees/wt remote", evalcontract.Approve, ""},
	{"integrate-branch", "git -C <ROOT>/.worktrees/wt push -u origin feature", evalcontract.Approve, ""},
	{"integrate-branch", "git -C <ROOT>/.worktrees/wt fetch origin feature", evalcontract.Approve, ""},
	{"integrate-branch", "git -c rerere.enabled=false -C <ROOT>/.worktrees/wt rebase main", evalcontract.Approve, ""},
	{"integrate-branch", "git -c rerere.enabled=false -C <ROOT>/.worktrees/wt rebase origin/feature", evalcontract.Approve, ""},
	{"integrate-branch", "git -C <ROOT>/.worktrees/wt push --force-with-lease -u origin feature", evalcontract.Approve, ""},
	{"integrate-branch", "git -C <ROOT>/.worktrees/wt diff --name-only --diff-filter=U", evalcontract.Approve, ""},
	{"integrate-branch", "git -C <ROOT>/.worktrees/wt checkout --theirs -- flake.lock", evalcontract.Approve, ""},
	{"integrate-branch", "git -C <ROOT>/.worktrees/wt add flake.lock", evalcontract.Approve, ""},
	{"integrate-branch", "git -C <ROOT>/.worktrees/wt rebase --continue", evalcontract.Approve, ""},
	{"integrate-branch", "git -C <ROOT>/.worktrees/wt rebase --abort", evalcontract.Approve, ""},
	{"integrate-branch", "git -C <ROOT>/.worktrees/wt commit -m 'chore: relock flake.lock after rebase'", evalcontract.Approve, ""},
	{"integrate-branch", "git -C <ROOT>/.worktrees/wt status --porcelain -- flake.lock", evalcontract.Approve, ""},
	{"integrate-branch", "git -C <ROOT> merge --ff-only feature", evalcontract.Approve, ""},
	{"integrate-branch", "git -C <ROOT> rev-parse main", evalcontract.Approve, ""},
	{"integrate-branch", "git -C <ROOT>/.worktrees/wt diff --name-only main...", evalcontract.Approve, ""},
	{"integrate-branch", "git diff --name-only main...", evalcontract.Approve, ""},
	{"integrate-branch", "git status --porcelain", evalcontract.Approve, ""},
	{"integrate-branch", "git stash list", evalcontract.Approve, ""},
	{"integrate-branch", "git worktree remove <ROOT>/.worktrees/wt", evalcontract.Abstain, "clean-worktree state ladder (operator ruling tc-vn5z): approves only for a verified-clean worktree"},
	{"integrate-branch", "git worktree prune", evalcontract.Approve, ""},
	{"integrate-branch", "git branch -d feature", evalcontract.Approve, ""},
	{"integrate-branch", "git rebase --continue", evalcontract.Approve, ""},
	{"integrate-branch", "git rebase --abort", evalcontract.Approve, ""},
	{"integrate-branch", "nix flake update", evalcontract.Approve, ""},
	{"integrate-branch", "nix flake lock", evalcontract.Approve, ""},
	{"integrate-branch", "nix flake update nixpkgs", evalcontract.Approve, ""},
	{"integrate-branch", "pg-hooks run pre-land feature", evalcontract.Approve, ""},
	{"integrate-branch", "pg-hooks status --porcelain", evalcontract.Approve, ""},
	{"integrate-branch", "prek run --all-files", evalcontract.Approve, ""},
	{"integrate-branch", "nix build .#checks.aarch64-darwin.foo", evalcontract.Approve, ""},
	{"integrate-branch", "bgcheck pg-hooks-refresh-repo", evalcontract.Approve, ""},
	{"integrate-branch", "wtdone feature --cc <ROOT>", evalcontract.Approve, ""},
	{"integrate-branch", "gh pr view feature --json url,state,number", evalcontract.Approve, ""},
	{"integrate-branch", "gh pr create --draft --head feature --base main --fill", evalcontract.Approve, ""},
	{"integrate-branch", "git push -u origin feature", evalcontract.Approve, ""},
	{"integrate-branch", "git push", evalcontract.Approve, ""},
	{"pg-go-mutate", "pg-go-mutate ./internal/collect --workers 1", evalcontract.Approve, ""},
	{"pg-go-mutate", "pg-go-mutate ./internal/collect --workers 1 --json >pgm.json", evalcontract.Approve, ""},
	{"pg-go-mutate", "jq --arg file collect.go --argjson line 95 --arg type branch_condition '.statistics' pgm.json", evalcontract.Approve, ""},
	{"pg-go-mutate", "go build ./...", evalcontract.Approve, ""},
	{"pg-go-mutate", "go test ./...", evalcontract.Approve, ""},
	{"pg-ccaudit", "pg-ccaudit status", evalcontract.Approve, ""},
	{"pg-ccaudit", "pg-ccaudit queries", evalcontract.Approve, ""},
	{"pg-ccaudit", "pg-ccaudit queries --verbose", evalcontract.Approve, ""},
	{"pg-ccaudit", "pg-ccaudit query error-rate-by-tool --since 2026-07-22 --until 2026-07-30", evalcontract.Approve, ""},
	{"pg-ccaudit", "pg-ccaudit query session-concentration 'sig' --since 2026-07-22 --until 2026-07-30", evalcontract.Approve, ""},
	{"pg-ccaudit", "pg-ccaudit query first-seen 'sig' --since 2026-07-22 --until 2026-07-30", evalcontract.Approve, ""},
	{"pg-ccaudit", "pg-ccaudit candidates --since 2026-07-22 --until 2026-07-30", evalcontract.Approve, ""},
	{"pg-ccaudit", "pg-ccaudit classify --classifier cli --since 2026-07-22 --until 2026-07-30 --max 100", evalcontract.Approve, ""},
	{"pg-ccaudit", "pg-ccaudit classify status --since 2026-07-22 --until 2026-07-30", evalcontract.Approve, ""},
	{"pg-ccaudit", "pg-ccaudit report --classifier cli --since 2026-07-22 --until 2026-07-30 --max 100", evalcontract.Approve, ""},
	{"pg-ccaudit", "pg-ccaudit evaluate --classifier cli --since 2026-07-22 --until 2026-07-30", evalcontract.Approve, ""},
	{"pg-ccaudit", "pg-ccaudit cost", evalcontract.Approve, ""},
	{"pg-ccaudit", "pg-ccaudit gold status", evalcontract.Approve, ""},
	{"pg-ccaudit", "pg-ccaudit gold seed", evalcontract.Approve, ""},
	{"pg-ccaudit", "pg-ccaudit ingest", evalcontract.Abstain, "operator must authorize ingest (skill + old-engine Ask): not approved"},
	{"pg-ccaudit", "bd -C <ROOT> show pg2-abc12 --json", evalcontract.Approve, ""},
	{"pg-ccaudit", "bd -C <ROOT> update pg2-abc12 --append-notes 'checkpoint'", evalcontract.Approve, ""},
	{"pg-ccaudit", "bd -C <ROOT> list --label improvement-retro --sort created -n 1 --json", evalcontract.Approve, ""},
	{"pg-ccaudit", "bd -C <ROOT> create \"Improvement retro\" --labels improvement-retro", evalcontract.Approve, ""},
	{"pg-ccaudit", "bd -C <ROOT> update pg2-abc12 --claim --actor sess-1", evalcontract.Approve, ""},
	{"pg-ccaudit", "bd -C <ROOT> update pg2-abc12 --body-file -", evalcontract.Approve, ""},
	{"pg-ccaudit", "bd -C <ROOT> close pg2-abc12 --reason 'retro complete'", evalcontract.Approve, ""},
	{"pg-wi-flow", "pg-wi-flow next", evalcontract.Approve, ""},
	{"pg-wi-flow", "pg-wi-flow next --stage s", evalcontract.Approve, ""},
	{"pg-wi-flow", "pg-wi-flow next --id pg2-abc12", evalcontract.Approve, ""},
	{"pg-wi-flow", "pg-wi-flow explain pg2-abc12", evalcontract.Approve, ""},
	{"pg-wi-flow", "pg-wi-flow release pg2-abc12", evalcontract.Approve, ""},
	{"pg-wi-flow", "pg-wi-flow claim pg2-abc12", evalcontract.Approve, ""},
	{"pg-wi-flow", "pg-wi-flow round pg2-abc12", evalcontract.Approve, ""},
	{"pg-wi-flow", "pg-wi-flow resolve pg2-abc12 --answer A", evalcontract.Approve, ""},
	{"pg-wi-flow", "pg-wi-flow resolve pg2-abc12 --decision D --rationale R", evalcontract.Approve, ""},
	{"pg-wi-flow", "pg-wi-flow resolve pg2-abc12 --abandon --reason-code r", evalcontract.Approve, ""},
	{"pg-wi-flow", "pg-wi-flow resolve pg2-abc12 --defer 2026-10-10", evalcontract.Approve, ""},
	{"pg-wi-flow", "pg-wi-flow escalate pg2-abc12", evalcontract.Approve, ""},
	{"pg-wi-flow", "pg-wi-flow list --stale --reserved-hours 2", evalcontract.Approve, ""},
	{"pg-wi-flow", "pg-wi-flow list --attended", evalcontract.Approve, ""},
	{"claude-extended-tool-approver", "claude-extended-tool-approver evaluate --settings=<ROOT>/.claude/settings.local.json --format=json > /tmp/ceta-settings-eval.json", evalcontract.Approve, ""},
	{"claude-extended-tool-approver", "claude-extended-tool-approver evaluate --misses-only --format=json > /tmp/ceta-misses.json", evalcontract.Approve, ""},
	{"claude-extended-tool-approver", "claude-extended-tool-approver evaluate --format=json > /tmp/ceta-all.json", evalcontract.Approve, ""},
	{"claude-extended-tool-approver", "claude-extended-tool-approver evaluate --corpus /tmp/c.json --format=json", evalcontract.Approve, ""},
	{"claude-extended-tool-approver", "claude-extended-tool-approver show 1 2 3 --format=json", evalcontract.Approve, ""},
	{"claude-extended-tool-approver", "claude-extended-tool-approver lint --embedded", evalcontract.Approve, ""},
	{"claude-extended-tool-approver", "claude-extended-tool-approver lint /tmp/spec.json", evalcontract.Approve, ""},
	{"claude-extended-tool-approver", "claude-extended-tool-approver spec-drift-check --embedded", evalcontract.Approve, ""},
	{"claude-extended-tool-approver", "jq 'length' /tmp/ceta-misses.json", evalcontract.Approve, ""},
	{"claude-extended-tool-approver", "jq '.permissions.allow' <ROOT>/.claude/settings.local.json", evalcontract.Approve, ""},
	{"claude-extended-tool-approver", "ls -la <ROOT>/.claude/settings.local.json 2>/dev/null", evalcontract.Approve, ""},
	{"claude-extended-tool-approver", "bd create --title=\"Absorb setting\" --description=\"d\" --type=task --priority=2", evalcontract.Approve, ""},
	{"claude-extended-tool-approver", "bd label add pg2-abc12 claude-extended-tool-approver", evalcontract.Approve, ""},
	{"claude-extended-tool-approver", "go run ./cmd/claude-extended-tool-approver lint --embedded", evalcontract.Abstain, "operator ruling tc-vn5z 2026-09-07: go run abstains"},
	{"claude-extended-tool-approver", "go run ./cmd/claude-extended-tool-approver spec-drift-check --embedded", evalcontract.Abstain, "operator ruling tc-vn5z 2026-09-07: go run abstains"},
	{"claude-extended-tool-approver", "go run ./cmd/genspecs", evalcontract.Abstain, "operator ruling tc-vn5z 2026-09-07: go run abstains"},
	{"claude-extended-tool-approver", "git checkout -- internal/embeddedspecs/data/jq.json", evalcontract.Approve, ""},
	{"claude-extended-tool-approver", "jq --help", evalcontract.Abstain, "generic '<tool> --help': executes an arbitrary program; not approved"},
	{"claude-extended-tool-approver", "jq --version", evalcontract.Abstain, "generic '<tool> --help': executes an arbitrary program; not approved"},
	{"claude-extended-tool-approver", "man jq", evalcontract.Approve, ""},
	{"bash-scripting", "bats tests/", evalcontract.Approve, ""},
	{"bash-scripting", "nix flake check", evalcontract.Approve, ""},
	{"bash-scripting", "nix build .#checks.aarch64-darwin.foo", evalcontract.Approve, ""},
	{"bash-scripting", "nix run .#install-pre-commit-hooks", evalcontract.Abstain, "requires an operator buildToolVerbs declaration (installable-reference class)"},
	{"bash-scripting", "git clean -fd", evalcontract.Abstain, "operator ruling pg2-4yy4r item 3: git clean abstains in every spelling"},
	{"bash-scripting", "nix build .#foo", evalcontract.Approve, ""},
	{"behavior-docs-conformance", "claude-marketplace/behavior-docs-conformance/skills/behavior-docs-impl-conformance/scripts/impl-traces.sh set impl", evalcontract.Approve, ""},
	{"behavior-docs-conformance", "claude-marketplace/behavior-docs-conformance/skills/behavior-docs-inter-conformance/scripts/resolve-imports.sh owner implementer", evalcontract.Approve, ""},
	{"behavior-docs-conformance", "claude-marketplace/behavior-docs-conformance/skills/behavior-docs-inter-conformance/scripts/name-collisions.sh setA setB", evalcontract.Approve, ""},
	{"behavior-docs-conformance", "claude-marketplace/behavior-docs-conformance/skills/behavior-docs-intra-conformance/scripts/self-checks.sh target", evalcontract.Approve, ""},
	{"behavior-docs-conformance", "claude-marketplace/behavior-docs-conformance/skills/behavior-docs-intra-conformance/scripts/trace-extract.sh target", evalcontract.Approve, ""},
	{"behavior-docs-conformance", "claude-marketplace/behavior-docs-conformance/skills/behavior-docs-intra-conformance/scripts/relocation-check.sh target", evalcontract.Approve, ""},
	{"integrate-branch", "bgrun flake-check -- nix flake check", evalcontract.Approve, ""},
	{"integrate-branch", "bgrun hooks-refresh -- pg-hooks run pre-land feature", evalcontract.Approve, ""},
	{"integrate-branch", "bgcheck flake-check", evalcontract.Approve, ""},
	{"integrate-branch", "bgcheck", evalcontract.Approve, ""},
	{"pg-ccaudit", "git diff --no-ext-diff", evalcontract.Approve, ""},
	{"bash-scripting", "nix run .#install-pre-commit-hooks", evalcontract.Abstain, "requires an operator buildToolVerbs declaration (installable-reference class)"},
	{"claude-extended-tool-approver", "jq --help", evalcontract.Abstain, "generic '<tool> --help': executes an arbitrary program; not approved"},
	{"claude-extended-tool-approver", "man jq", evalcontract.Approve, ""},
	{"wayfinder-beads", "bd list --json | jq length", evalcontract.Approve, ""},

	// ---- shell statement forms (pg2-dbrsg) --------------------------------
	// Assignments from command substitution (X="$(cmd)"): approve iff the inner
	// command approves.
	{"integrate-branch", `REMOTE="$(git -C <ROOT> remote)"`, evalcontract.Approve, ""},
	{"integrate-branch", `SHA="$(git -C <ROOT> rev-parse HEAD)"`, evalcontract.Approve, ""},
	{"integrate-branch", `FB="$(git rev-parse --abbrev-ref HEAD)"`, evalcontract.Approve, ""},
	{"integrate-branch", `OLD_PRIMARY=$(git -C <ROOT> rev-parse main)`, evalcontract.Approve, ""},
	{"integrate-branch", `path="$(git -C <ROOT> rev-parse --git-path rebase-merge)"`, evalcontract.Approve, ""},
	{"integrate-branch", `n="$(git -C <ROOT> remote | grep -c .)"`, evalcontract.Approve, ""},
	{"integrate-branch", `REMOTE="$(git -C <ROOT> rev-parse --abbrev-ref --symbolic-full-name "@{u}" 2>/dev/null | cut -d/ -f1)"`, evalcontract.Approve, ""},
	{"integrate-branch", `WT="$(pwd)"`, evalcontract.Approve, ""},
	{"beads-lifecycle", `READY="$(bd ready --json -n 0)"`, evalcontract.Approve, ""},
	{"beads-lifecycle", `BRANCH=$(bd show pg2-abc12 --json | jq -r '.metadata.branch')`, evalcontract.Approve, ""},
	{"bash-scripting", `worktrees=$(jq -r '.worktrees[]' /tmp/cfg.json)`, evalcontract.Approve, ""},
	{"bash-scripting", `TEST_DIR="$(mktemp -d)"`, evalcontract.Approve, ""},
	{"bash-scripting", `MOCK_BIN=$(mktemp -d)`, evalcontract.Approve, ""},
	{"integrate-branch", `X=literal`, evalcontract.Approve, ""},
	// An assignment never launders an inner command that does not approve.
	{"integrate-branch", `X=$(rm -rf <ROOT>/.worktrees/wt)`, evalcontract.Abstain, "inner command is graded like a top-level one: rm of a writable path needs consent"},
	{"integrate-branch", `X=$(rm -rf <ROOT>/.worktrees/wt) echo hi`, evalcontract.Abstain, "a prefix assignment's substitution is graded too (it used to ride on echo's verdict)"},
	{"integrate-branch", `D="$(date +%F)"`, evalcontract.Abstain, "date has no schema (needs a recorded --help hash from the nix sandbox)"},
	{"integrate-branch", `T=$(echo x | tr a b)`, evalcontract.Abstain, "tr has no schema (needs a recorded --help hash from the nix sandbox)"},
	{"integrate-branch", `REPO=$(pg-connector scm branch detect | jq -r '.result.repo')`, evalcontract.Abstain, "pg-connector scm is an unmodeled subcommand (ZR plugin schema, separate bead)"},
	{"integrate-branch", `CC="$(cd "$(git rev-parse --git-common-dir)/.." && pwd)"`, evalcontract.Abstain, "cd to a runtime path: the directory is not statically known"},
	{"integrate-branch", `N=$((1+2))`, evalcontract.Abstain, "arithmetic expansion evaluates variable text, which can run a command substitution"},

	// `[ ]` / test operators.
	{"integrate-branch", `[ -z "$REMOTE" ]`, evalcontract.Approve, ""},
	{"integrate-branch", `[ -n "$conflicted_inputs" ]`, evalcontract.Approve, ""},
	{"integrate-branch", `[ -d <ROOT>/.git ]`, evalcontract.Approve, ""},
	{"integrate-branch", `[ -f "$path" ]`, evalcontract.Approve, ""},
	{"integrate-branch", `[ "$n" -gt 1 ]`, evalcontract.Approve, ""},
	{"integrate-branch", `[ "$a" != "$b" ]`, evalcontract.Approve, ""},
	{"integrate-branch", `test -z "$X"`, evalcontract.Approve, ""},
	{"integrate-branch", `[ -n "$(git -C <ROOT> status --porcelain -- flake.lock)" ] && echo dirty`, evalcontract.Approve, ""},
	{"integrate-branch", `[ -v NAME ]`, evalcontract.Abstain, "-v takes a variable name that may carry an array subscript (arithmetic evaluation)"},
	{"integrate-branch", `[[ -z "$X" ]]`, evalcontract.Abstain, "[[ ]] evaluates integer operands as arithmetic"},
	{"integrate-branch", `(( i++ ))`, evalcontract.Abstain, "arithmetic command"},

	// Loops, case, and the builtins read / shift / exit / pwd.
	{"integrate-branch", `for name in rebase-merge rebase-apply; do path="$(git -C <ROOT> rev-parse --git-path "$name")"; case "$path" in /*) ;; *) path="<ROOT>/$path" ;; esac; if [ -d "$path" ]; then echo "in progress: $path"; fi; done`, evalcontract.Approve, ""},
	{"integrate-branch", `for a in x y; do bd show pg2-abc12; done`, evalcontract.Approve, ""},
	{"bash-scripting", `while IFS='=' read -r key value; do case "$key" in a) echo A;; esac; done < /tmp/kv.env`, evalcontract.Approve, ""},
	{"bash-scripting", `while read -r line; do echo "$line"; done`, evalcontract.Approve, ""},
	{"bash-scripting", `read -r answer`, evalcontract.Approve, ""},
	{"bash-scripting", `shift`, evalcontract.Approve, ""},
	{"bash-scripting", `shift 2`, evalcontract.Approve, ""},
	{"integrate-branch", `exit 1`, evalcontract.Approve, ""},
	{"integrate-branch", `if [ -z "$REMOTE" ]; then echo none; exit 1; fi`, evalcontract.Approve, ""},
	{"integrate-branch", `pwd`, evalcontract.Approve, ""},
	{"integrate-branch", `pwd -P`, evalcontract.Approve, ""},
	{"integrate-branch", `for f in $(rm -rf <ROOT>/.worktrees/wt); do echo "$f"; done`, evalcontract.Abstain, "a loop word-list substitution is graded like any command"},
	{"bash-scripting", `read PATH`, evalcontract.Abstain, "read assigns the variable it names; PATH is a guarded name"},
	{"bash-scripting", `read -u 3 line`, evalcontract.Abstain, "-u reads from a descriptor this model cannot see"},
	{"bash-scripting", `while read -r l; do echo "$l"; done < ~/.ssh/id_rsa`, evalcontract.Reject, "the compound's input redirection reads a secret path"},

	// Quoted-variable path forms stay non-approvable; the literal-path form
	// approves (see the SHELL STATEMENT FORMS note above).
	{"integrate-branch", `git -C "$WT" status`, evalcontract.Abstain, "quoted variable as a path: a runtime value, approvable only as a literal path"},
	{"integrate-branch", `WT=<ROOT> && git -C "$WT" status`, evalcontract.Abstain, "even a literal in-command assignment is not resolved by this engine (the old engine's InCommandVars seam is not threaded in); type the literal path"},
	{"integrate-branch", `WT="$(pwd)"; git -C "$WT" status`, evalcontract.Abstain, "the variable holds a runtime value, not a literal"},
	{"integrate-branch", `git -C <ROOT> status`, evalcontract.Approve, ""},

	// A LIVE EXPANSION AS A POSITIONAL (pg2-5ctay): a bare $X / ${X} / "$X" /
	// $(cmd) that begins a positional word may carry an option the schema
	// never saw, so it abstains unless the command's options are all inert
	// (echo), the word sits beside a literal test operator, or it follows
	// `--`. Skill templates that pass an identifier through a variable should
	// instruct the literal value instead (`bd show pg2-abc12`).
	{"beads-lifecycle", `bd show "$ID"`, evalcontract.Abstain, "a live positional could expand to an option (option injection); type the literal id"},
	{"integrate-branch", `git log $X`, evalcontract.Abstain, "X may hold --output=/tmp/pwn (git log writes the file); a live positional is not inert"},
	{"integrate-branch", `X=--output=/tmp/pwn; git log $X`, evalcontract.Abstain, "the in-command assignment is not resolved, and even a resolved option-shaped value must not ride as a literal"},
	{"integrate-branch", `export X=--output=/tmp/pwn && git log $X`, evalcontract.Abstain, "an earlier call's export reaches a later call's $X; the live positional is the gate"},
	{"integrate-branch", `git log -- $X`, evalcontract.Approve, ""},
	{"integrate-branch", `git log main..$X`, evalcontract.Approve, ""},
	{"integrate-branch", `echo "$X"`, evalcontract.Approve, ""},
	{"integrate-branch", `[ "$X" "$Y" ]`, evalcontract.Abstain, "two adjacent expansions could form the unmodeled test -v NAME[$(cmd)] operator"},

	// Persistent assignments of variables that change later commands' meaning.
	{"bash-scripting", `IFS=/; echo hi`, evalcontract.Abstain, "persistent IFS changes how every later expansion splits"},
	{"bash-scripting", `CDPATH=/etc; cd foo`, evalcontract.Abstain, "persistent CDPATH retargets a later cd"},
	{"bash-scripting", `GIT_DIR=/tmp/x; git status`, evalcontract.Reject, "persistent GIT_DIR redirects every later git command"},
}

// TestPluginInstructedForms runs every inventoried form through the full
// effect engine in a git-workspace fixture (so trusted-checkout exec effects
// — go/nix/bats/prek/pg-hooks — judge their CWD as a recognised workspace,
// exactly as in a real session inside a repo).
func TestPluginInstructedForms(t *testing.T) {
	root, _ := fixture(t)
	reg := cmddesc.DefaultRegistry()
	for _, f := range pluginForms {
		cmd := strings.ReplaceAll(f.command, "<ROOT>", root)
		resp := Evaluate(evalcontract.Request{Command: cmd, CWD: root, ProjectRoot: root}, reg, DefaultPolicies(), DefaultGraphPolicies())
		if resp.Decision != f.want {
			t.Errorf("[%s] %s\n  decision = %s, want %s (%s)\n  reason: %s", f.plugin, f.command, resp.Decision, f.want, f.note, resp.Reason)
		}
		if f.want != evalcontract.Approve && f.note == "" {
			t.Errorf("[%s] %s: a non-Approve inventory row must record why it is a deliberate exception", f.plugin, f.command)
		}
	}
}
