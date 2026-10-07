package effectpolicy

import (
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmddesc"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/evalcontract"
)

// gitStashForm is one `git stash` command form and the verdict the effect
// engine must reach for it.
type gitStashForm struct {
	command string
	want    evalcontract.Decision
	note    string
}

// gitStashForms pins the grading of the `git stash` family (pg2-y9xc6): ONLY
// `git [-C <path>] stash push [-u|--include-untracked] [-m|--message <literal>]`
// is Approve (skill-instructed by pn-workspace-sync; operator ruling on
// pg2-cjfpy). Every other stash subcommand, every other push flag, any
// pathspec and any non-literal -m operand stays Abstain (ADR 0075 R5: approve
// only when sure; agent rule R3: no stashing on the agent's own initiative).
var gitStashForms = []gitStashForm{
	// ---- approved: with and without -C, -u, -m ----
	{"git stash push", evalcontract.Approve, ""},
	{"git stash push -u", evalcontract.Approve, ""},
	{"git stash push --include-untracked", evalcontract.Approve, ""},
	{"git stash push -m unique-tag", evalcontract.Approve, ""},
	{"git stash push --message unique-tag", evalcontract.Approve, ""},
	{"git stash push --message=unique-tag", evalcontract.Approve, ""},
	{"git stash push -u -m unique-tag", evalcontract.Approve, ""},
	{"git stash push -m unique-tag -u", evalcontract.Approve, ""},
	{`git stash push -u -m "pn-workspace-sync 2026-10-07 abc123"`, evalcontract.Approve, ""},
	{"git stash push -u -m 'pn sync: tag'", evalcontract.Approve, ""},
	{"git -C <ROOT> stash push", evalcontract.Approve, ""},
	{"git -C <ROOT> stash push -u", evalcontract.Approve, ""},
	{"git -C <ROOT> stash push -m unique-tag", evalcontract.Approve, ""},
	{"git -C <ROOT> stash push -u -m unique-tag", evalcontract.Approve, ""},
	{"git -C <ROOT> stash push --include-untracked --message unique-tag", evalcontract.Approve, ""},
	{"git stash push -u --", evalcontract.Approve, ""},
	// the read-only listing form was already approved and must stay so
	{"git stash list", evalcontract.Approve, ""},
	{"git -C <ROOT> stash list", evalcontract.Approve, ""},

	// ---- other stash subcommands: unmodeled ----
	{"git stash drop", evalcontract.Abstain, "stash drop destroys a stash entry"},
	{"git stash drop stash@{0}", evalcontract.Abstain, "stash drop destroys a stash entry"},
	{"git stash clear", evalcontract.Abstain, "stash clear destroys every stash entry"},
	{"git stash pop", evalcontract.Abstain, "stash pop rewrites the working tree and can conflict"},
	{"git stash apply", evalcontract.Abstain, "stash apply rewrites the working tree and can conflict"},
	{"git stash branch newbranch", evalcontract.Abstain, "stash branch creates a branch and checks it out"},
	{"git stash show", evalcontract.Abstain, "unmodeled stash subcommand"},
	{"git stash create", evalcontract.Abstain, "unmodeled stash subcommand"},
	{"git stash store abc123", evalcontract.Abstain, "unmodeled stash subcommand"},
	{"git stash save wip", evalcontract.Abstain, "deprecated alias of push, unmodeled"},
	{"git stash", evalcontract.Abstain, "bare shorthand for push is not the instructed form (R3)"},
	{"git stash -u", evalcontract.Abstain, "bare shorthand for push is not the instructed form (R3)"},
	{"git stash -m tag", evalcontract.Abstain, "bare shorthand for push is not the instructed form (R3)"},
	{"git -C <ROOT> stash drop", evalcontract.Abstain, "stash drop destroys a stash entry"},
	{"git -C <ROOT> stash clear", evalcontract.Abstain, "stash clear destroys every stash entry"},
	{"git -C <ROOT> stash pop", evalcontract.Abstain, "stash pop rewrites the working tree"},
	{"git -C <ROOT> stash apply", evalcontract.Abstain, "stash apply rewrites the working tree"},
	{"git -C <ROOT> stash branch newbranch", evalcontract.Abstain, "stash branch creates a branch"},
	{"git -C <ROOT> stash", evalcontract.Abstain, "bare shorthand for push is not the instructed form (R3)"},

	// ---- push with a pathspec: limits what is stashed, unmodeled ----
	{"git stash push -- a.go", evalcontract.Abstain, "pathspec after --"},
	{"git stash push -u -- a.go b.go", evalcontract.Abstain, "pathspec after --"},
	{"git stash push a.go", evalcontract.Abstain, "pathspec without --"},
	{"git stash push -u -m unique-tag a.go", evalcontract.Abstain, "pathspec after the message"},
	{"git -C <ROOT> stash push -u -m unique-tag -- a.go", evalcontract.Abstain, "pathspec after --"},
	{"git stash push --pathspec-from-file=paths.txt", evalcontract.Abstain, "pathspec read from a file"},

	// ---- push with a flag outside the instructed set ----
	{"git stash push -k", evalcontract.Abstain, "-k/--keep-index is not the instructed form"},
	{"git stash push --keep-index", evalcontract.Abstain, "-k/--keep-index is not the instructed form"},
	{"git stash push --no-keep-index", evalcontract.Abstain, "--no-keep-index is not the instructed form"},
	{"git stash push -a", evalcontract.Abstain, "-a/--all also stashes ignored files"},
	{"git stash push --all", evalcontract.Abstain, "-a/--all also stashes ignored files"},
	{"git stash push -p", evalcontract.Abstain, "-p/--patch is interactive"},
	{"git stash push --patch", evalcontract.Abstain, "-p/--patch is interactive"},
	{"git stash push -S", evalcontract.Abstain, "-S/--staged is not the instructed form"},
	{"git stash push --staged", evalcontract.Abstain, "-S/--staged is not the instructed form"},
	{"git stash push -q", evalcontract.Abstain, "-q is not the instructed form"},
	{"git stash push -u -a", evalcontract.Abstain, "-a is not the instructed form even beside -u"},
	{"git stash push -u -k -m unique-tag", evalcontract.Abstain, "-k is not the instructed form even beside -u/-m"},
	{"git stash push -uk", evalcontract.Abstain, "clustered short flags are unmodeled"},
	{"git stash push --include", evalcontract.Abstain, "an abbreviated long flag is unmodeled"},
	{"git -C <ROOT> stash push -u -p", evalcontract.Abstain, "-p/--patch is interactive"},

	// ---- push with a non-literal or malformed -m operand ----
	{`git stash push -u -m "$TAG"`, evalcontract.Abstain, "-m operand is a runtime expansion"},
	{`git stash push -u -m $TAG`, evalcontract.Abstain, "-m operand is a runtime expansion"},
	{`git stash push -u -m "$(date +%s)"`, evalcontract.Abstain, "-m operand is a command substitution"},
	{"git stash push -u -m `date`", evalcontract.Abstain, "-m operand is a command substitution"},
	{`git stash push -u -m "tag-$TAG"`, evalcontract.Abstain, "-m operand embeds a runtime expansion"},
	{`git stash push --message="$TAG"`, evalcontract.Abstain, "--message operand is a runtime expansion"},
	{`git stash push -m ""`, evalcontract.Abstain, "-m operand is empty"},
	{"git stash push -m", evalcontract.Abstain, "-m has no operand"},
	{"git stash push -u -m", evalcontract.Abstain, "-m has no operand"},
	{"git stash push -m -p", evalcontract.Abstain, "-m operand begins with a dash"},
	{"git stash push -m -u", evalcontract.Abstain, "-m operand begins with a dash"},
	{`git -C <ROOT> stash push -u -m "$TAG"`, evalcontract.Abstain, "-m operand is a runtime expansion"},

	// ---- -C path handling follows the other `git -C` forms ----
	{`git -C "$WT" stash push -u -m unique-tag`, evalcontract.Abstain, "the -C directory is a runtime expansion"},
	{"git -C /etc stash push -u -m unique-tag", evalcontract.Abstain, "the working tree modified is outside the project"},
}

// TestGitStashPushGrading runs every gitStashForms row through the full effect
// engine in a git-workspace fixture and asserts the verdict. It is the
// executable statement of the pg2-y9xc6 scope: push only, -u and -m <literal>
// only, with or without `git -C <path>`; everything else Abstain.
func TestGitStashPushGrading(t *testing.T) {
	root, _ := fixture(t)
	reg := cmddesc.DefaultRegistry()
	for _, f := range gitStashForms {
		cmd := strings.ReplaceAll(f.command, "<ROOT>", root)
		resp := Evaluate(evalcontract.Request{Command: cmd, CWD: root, ProjectRoot: root}, reg, DefaultPolicies(), DefaultGraphPolicies())
		if resp.Decision != f.want {
			t.Errorf("%s\n  decision = %s, want %s (%s)\n  reason: %s", f.command, resp.Decision, f.want, f.note, resp.Reason)
		}
		if f.want != evalcontract.Approve && f.note == "" {
			t.Errorf("%s: a non-Approve row must record why it stays Abstain", f.command)
		}
	}
}
