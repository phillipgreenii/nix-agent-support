package effectpolicy

import (
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmddesc"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/evalcontract"
)

// supportToolForm is one invocation of a first-party plugin SUPPORT tool and the
// verdict the effect engine must reach (bead pg2-niyyq; operator ruling, Phillip,
// 2026-10-25: "all custom plugin support tools MUST be auto-approved", each with a
// recorded read-only versus mutating decision). <ROOT> is the git-workspace
// fixture the command runs in, \n a newline (the real rows are two statements).
type supportToolForm struct {
	command string
	want    evalcontract.Decision
	why     string
}

var supportToolForms = []supportToolForm{
	// ---- one approved fixture per tool ------------------------------------
	{"integrate-branch-support --prek-branch-diff", evalcontract.Approve, ""},
	{"integrate-branch-support --facts", evalcontract.Approve, ""},
	{"session-mode set-status finished --handoff-bead pg2-abc12", evalcontract.Approve, ""},
	{"handoff-create --attended --session-id sess-1 --title subject --body-file /tmp/body.md", evalcontract.Approve, ""},
	{"hand-to-human pg2-abc12", evalcontract.Approve, ""},
	{"hand-to-human pg2-abc12 --actor sess-1 --also-remove needs-split-review", evalcontract.Approve, ""},
	{"hand-to-human pg2-abc12 --backend pg-connector-issue-beads-pg2", evalcontract.Approve, ""},
	{"wtdone feature --cc <ROOT>", evalcontract.Approve, ""},
	{"wtnew pg2-abc12", evalcontract.Approve, ""},
	{"wtnew pg2-abc12 --base origin/main --branch drain/pg2-abc12", evalcontract.Approve, ""},
	{"bgcheck flake-check", evalcontract.Approve, ""},
	{"bgwait flake-check --timeout 540", evalcontract.Approve, ""},
	{"pb drain reclaim --bead pg2-abc12 --repo <ROOT> --actor sess-1", evalcontract.Approve, ""},
	{"pg-router status", evalcontract.Approve, ""},
	{"pg-router status --json", evalcontract.Approve, ""},
	{"pg-router gate list --json", evalcontract.Approve, ""},
	{"pg-router config --show --json", evalcontract.Approve, ""},
	{"pg-router config --print-defaults", evalcontract.Approve, ""},
	{"pg-router config check", evalcontract.Approve, ""},
	{"pg-router version", evalcontract.Approve, ""},
	{"pg-router --version", evalcontract.Approve, ""},

	// ---- partly allowed: the mutating and process-starting forms abstain --
	{"pg-router pause", evalcontract.Abstain, "sets the pool-wide SYSTEM_PAUSE gate"},
	{"pg-router resume", evalcontract.Abstain, "clears a gate on the running core"},
	{"pg-router resume --all", evalcontract.Abstain, "clears every gate"},
	{"pg-router gate set LOW_DISK_USAGE", evalcontract.Abstain, "sets a gate"},
	{"pg-router gate clear --all", evalcontract.Abstain, "clears gates"},
	{"pg-router push-inject {}", evalcontract.Abstain, "injects an event into the running core"},
	{"pg-router log compact", evalcontract.Abstain, "rewrites the queue's write-ahead log"},
	{"pg-router run", evalcontract.Abstain, "starts the daemon"},
	{"pg-router run-until-idle", evalcontract.Abstain, "boots a core and runs configured handlers"},
	{"pg-router run-role worker {}", evalcontract.Abstain, "dispatches an event through a role"},
	{"pg-router run-query query:q", evalcontract.Abstain, "runs a configured source's command"},
	{"pg-router doctor", evalcontract.Abstain, "runs every handler's validate"},
	{"pg-router tui", evalcontract.Abstain, "interactive, offers pause and resume"},
	{"pg-router ingest-event", evalcontract.Abstain, "manager-to-core callback"},
	{"pg-router status --socket /tmp/other.sock", evalcontract.Abstain, "a caller-chosen socket is unmodeled"},
	{"pg-router", evalcontract.Abstain, "no subcommand"},
	{"hand-to-human pg2-abc12 --bd-dir /home/nobody/tracker", evalcontract.Abstain, "--bd-dir is a path read outside every known zone"},
	{"hand-to-human pg2-abc12 --bogus", evalcontract.Abstain, "an unmodeled flag stays insufficient"},
	{"wtnew pg2-abc12 --bogus", evalcontract.Abstain, "an unmodeled flag stays insufficient"},
	{"bgkill flake-check", evalcontract.Abstain, "signals a process tree; ADR 0104 approves it only for a job THIS session launched, which a spec cannot express"},

	// ---- the wrapper approves exactly when the wrapped command does -------
	{"bgrun preland-x -- integrate-branch-support --prek-branch-diff", evalcontract.Approve, ""},
	{"bgrun hooks-x -- pg-hooks run pre-land feature", evalcontract.Approve, ""},
	{"bgrun x -- bash -c 'cd <ROOT> && integrate-branch-support --prek-branch-diff'", evalcontract.Approve, ""},
	{"cd <ROOT> && bgrun x -- bash -c 'integrate-branch-support --prek-branch-diff'", evalcontract.Approve, ""},
	{"bgrun x -- pg-router status --json", evalcontract.Approve, ""},
	// The real compound (the 119 rows): the worktree is assigned on its own
	// statement and the double-quoted script is expanded by the OUTER shell.
	{"WT=<ROOT>\nbgrun preland-x -- bash -c \"cd $WT && integrate-branch-support --prek-branch-diff\"", evalcontract.Approve, ""},
	{"WT=<ROOT>; bgrun preland-x -- bash -c \"cd $WT && integrate-branch-support --prek-branch-diff\"", evalcontract.Approve, ""},
	{"WT=<ROOT> && bgrun preland-x -- bash -c \"cd $WT && integrate-branch-support --prek-branch-diff\"", evalcontract.Approve, ""},
	{"WT=<ROOT>\nbgrun x -- bash -c \"cd ${WT} && integrate-branch-support --prek-branch-diff\"", evalcontract.Approve, ""},

	// ---- WRAPPER LAUNDERING: the wrapper never approves what it wraps ------
	{"bgrun x -- rm -rf /home/nobody/x", evalcontract.Abstain, "rm outside the write allowlist"},
	{"bgrun x -- bash -c 'cd <ROOT> && rm -rf /home/nobody/x'", evalcontract.Abstain, "rm outside the write allowlist, inside bash -c"},
	{"bgrun x -- bash -c 'integrate-branch-support --prek-branch-diff; rm -rf /home/nobody/x'", evalcontract.Abstain, "an approved first command does not cover the second"},
	{"WT=<ROOT>\nbgrun x -- bash -c \"cd $WT && rm -rf /home/nobody/x\"", evalcontract.Abstain, "the resolved variable does not launder the unsafe inner command"},
	{"WT=/home/nobody/x\nbgrun x -- bash -c \"cd $WT && rm -rf $WT\"", evalcontract.Abstain, "an unsafe resolved path is judged as the path it is"},
	{"bgrun x -- pg-router pause", evalcontract.Abstain, "the wrapped mutating subcommand is judged"},
	{"bgrun x -- bash -c 'pg-router pause'", evalcontract.Abstain, "the same through bash -c"},
	{"bgrun x -- bgkill other-job", evalcontract.Abstain, "the wrapped bgkill is judged"},

	// ---- the variable is resolved only where the text proves its value ----
	{"WT=<ROOT> bgrun x -- bash -c \"cd $WT && integrate-branch-support --prek-branch-diff\"", evalcontract.Abstain, "a PREFIX assignment is applied after the outer shell expands $WT, so the script really reads an empty value"},
	{"WT=<ROOT>\nbgrun x -- bash -c 'cd $WT && integrate-branch-support --prek-branch-diff'", evalcontract.Abstain, "single-quoted: the child shell reads its own environment, where WT is not exported"},
	{"WT=<ROOT>\nbgrun x -- bash -c \"cd $WT\"' $WT && integrate-branch-support --prek-branch-diff'", evalcontract.Abstain, "a single-quoted $WT in the same word is not the outer shell's"},
	{"WT=<ROOT>\nbgrun x -- bash -c \"cd \\$WT && integrate-branch-support --prek-branch-diff\"", evalcontract.Abstain, "an escaped dollar is not expanded by the outer shell"},
	{"bgrun x -- bash -c \"cd $WT && integrate-branch-support --prek-branch-diff\"", evalcontract.Abstain, "WT is not assigned in this command"},
	{"WT=$(pwd)\nbgrun x -- bash -c \"cd $WT && integrate-branch-support --prek-branch-diff\"", evalcontract.Abstain, "a runtime value is not a literal"},
	{"WT=<ROOT>\nWT=$(pwd)\nbgrun x -- bash -c \"cd $WT && integrate-branch-support --prek-branch-diff\"", evalcontract.Abstain, "a later non-literal write revokes the binding"},
	{"WT=<ROOT>\nunset WT\nbgrun x -- bash -c \"cd $WT && integrate-branch-support --prek-branch-diff\"", evalcontract.Abstain, "an intervening command may rewrite or unset the variable"},
	{"WT=<ROOT>\nread WT\nbgrun x -- bash -c \"cd $WT && integrate-branch-support --prek-branch-diff\"", evalcontract.Abstain, "an intervening read rewrites the variable"},
	{"WT=<ROOT>\nfor WT in /etc; do :; done\nbgrun x -- bash -c \"cd $WT && integrate-branch-support --prek-branch-diff\"", evalcontract.Abstain, "a loop variable rewrites it"},
	{"true || WT=<ROOT>\nbgrun x -- bash -c \"cd $WT && integrate-branch-support --prek-branch-diff\"", evalcontract.Abstain, "the assignment is conditional"},
	{"false && WT=<ROOT>\nbgrun x -- bash -c \"cd $WT && integrate-branch-support --prek-branch-diff\"", evalcontract.Abstain, "the assignment is conditional"},
	{"if true; then WT=<ROOT>; fi\nbgrun x -- bash -c \"cd $WT && integrate-branch-support --prek-branch-diff\"", evalcontract.Abstain, "the assignment sits in a compound body"},
	{"(WT=<ROOT>)\nbgrun x -- bash -c \"cd $WT && integrate-branch-support --prek-branch-diff\"", evalcontract.Abstain, "a subshell's assignment never reaches this shell"},
	{"WT=<ROOT> &\nbgrun x -- bash -c \"cd $WT && integrate-branch-support --prek-branch-diff\"", evalcontract.Abstain, "a backgrounded assignment runs in a subshell"},
	{"WT=<ROOT> | cat\nbgrun x -- bash -c \"cd $WT && integrate-branch-support --prek-branch-diff\"", evalcontract.Abstain, "a pipeline stage assigns in a subshell"},
	{"WT=<ROOT>\nWT[0]=/etc\nbgrun x -- bash -c \"cd $WT && integrate-branch-support --prek-branch-diff\"", evalcontract.Abstain, "an array-element write is a data leaf this seam does not model"},
	{"WT=<ROOT>\nbgrun x -- bash -c \"cd $WT && integrate-branch-support --prek-branch-diff\" | cat", evalcontract.Abstain, "a consumer in a pipeline stage is outside the straight line"},
	{"WT=<ROOT>\nfor d in a; do bgrun x -- bash -c \"cd $WT && integrate-branch-support --prek-branch-diff\"; done", evalcontract.Abstain, "a consumer in a loop body is outside the straight line"},
	{"WT=<ROOT>\nf() { bgrun x -- bash -c \"cd $WT && integrate-branch-support --prek-branch-diff\"; }\nWT=/etc\nf", evalcontract.Abstain, "a function body reads the variable at call time"},
	{"WT='<ROOT> x'\nbgrun x -- bash -c \"cd $WT && integrate-branch-support --prek-branch-diff\"", evalcontract.Abstain, "a value with whitespace is not spliced"},
	{"export WT=<ROOT>\nbgrun x -- bash -c \"cd $WT && integrate-branch-support --prek-branch-diff\"", evalcontract.Abstain, "only plain assignments precede the consumer"},
	{"WT=<ROOT>\nbgrun x -- bash -c \"cd ${WT:-/etc} && integrate-branch-support --prek-branch-diff\"", evalcontract.Abstain, "a parameter-expansion form is not a plain reference"},
}

// TestPluginSupportToolDecisions pins every recorded per-tool decision of bead
// pg2-niyyq through the full effect engine, in a git-workspace fixture so the
// trusted-checkout exec effects judge their directory as a real checkout.
func TestPluginSupportToolDecisions(t *testing.T) {
	root, _ := fixture(t)
	reg := cmddesc.DefaultRegistry()
	for _, f := range supportToolForms {
		if f.want != evalcontract.Approve && f.why == "" {
			t.Errorf("%q: a non-Approve row must record why it is a deliberate exception", f.command)
		}
		cmd := strings.ReplaceAll(strings.ReplaceAll(f.command, "<ROOT>", root), `\n`, "\n")
		resp := Evaluate(evalcontract.Request{Command: cmd, CWD: root, ProjectRoot: root}, reg, DefaultPolicies(), DefaultGraphPolicies())
		if resp.Decision != f.want {
			t.Errorf("%q\n  decision = %s, want %s (%s)\n  reason: %s", f.command, resp.Decision, f.want, f.why, resp.Reason)
		}
	}
}
