package effectpolicy

import (
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmddesc"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/evalcontract"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/pathspec"
)

type gitConfigRow struct {
	command string
	want    evalcontract.Decision
	note    string
}

// gitConfigKeyRows pins P16 end to end (tc-o14i5.3.9): the fact class comes
// from cmddesc.LookupGitConfigKey, the verdict only from GitConfigKeyPolicy.
var gitConfigKeyRows = []gitConfigRow{
	// One key from each fact class.
	{"git config core.hooksPath /tmp/h", evalcontract.Reject, "exec-sink"},
	{"git config alias.co checkout", evalcontract.Abstain, "alias without ! has no class"},
	{"git config http.sslVerify false", evalcontract.Reject, "interlock-disable"},
	{"git config remote.origin.url https://example.invalid/r.git", evalcontract.Reject, "push-fetch-redirect"},
	{"git config url.https://example.invalid/.insteadOf gh:", evalcontract.Reject, "push-fetch-redirect"},
	{"git config safe.directory /some/dir", evalcontract.Reject, "trust-widening"},
	{"git config include.path ../other", evalcontract.Reject, "trust-widening (reclassified from the old exec-sink-like entry)"},
	{"git config core.fsmonitor true", evalcontract.Approve, "inert-metadata via the ADR 0050 boolean value fact"},
	{"git config rerere.enabled true", evalcontract.Approve, "inert-metadata via the ADR 0050 pair"},

	// Unlisted key.
	{"git config user.name someone", evalcontract.Abstain, "unlisted key"},
	{"git config core.unknownthing 1", evalcontract.Abstain, "unlisted key"},

	// Value facts override the default class in both directions.
	{"git config core.fsmonitor /tmp/hook", evalcontract.Reject, "non-boolean fsmonitor names a hook program (ADR 0050)"},
	{"git config core.fsmonitor false", evalcontract.Approve, "boolean"},
	{"git config core.editor true", evalcontract.Approve, "ADR 0047 inert editor value"},
	{"git config core.editor :", evalcontract.Approve, "ADR 0047 inert editor value"},
	{"git config core.editor vim", evalcontract.Reject, "any other editor value is an exec sink"},
	{"git config sequence.editor true", evalcontract.Approve, "ADR 0047"},
	{"git config sequence.editor 'sh -c x'", evalcontract.Reject, "exec sink"},

	// The old table's escalated sinks and the P16 additions.
	{"git config core.pager less", evalcontract.Reject, "exec-sink (uniform; the old Reject-vs-Ask split is superseded)"},
	{"git config pager.log less", evalcontract.Reject, "pager.<cmd> section is the same sink"},
	{"git config core.sshCommand ssh", evalcontract.Reject, "exec-sink"},
	{"git config diff.external x", evalcontract.Reject, "exec-sink"},
	{"git config diff.foo.textconv x", evalcontract.Reject, "exec-sink"},
	{"git config merge.foo.driver x", evalcontract.Reject, "exec-sink"},
	{"git config filter.foo.smudge x", evalcontract.Reject, "exec-sink"},
	{"git config credential.https://h.helper store", evalcontract.Reject, "exec-sink"},
	{"git config gpg.ssh.program x", evalcontract.Reject, "exec-sink (new in P16)"},
	{"git config init.templateDir /x", evalcontract.Reject, "exec-sink"},
	{"git config uploadpack.packObjectsHook x", evalcontract.Reject, "exec-sink"},
	{"git config protocol.ext.allow always", evalcontract.Reject, "interlock-disable"},
	{"git config protocol.ext.allow never", evalcontract.Approve, "value fact: never disallows the transport"},
	{"git config clean.requireForce false", evalcontract.Reject, "interlock-disable"},
	{"git config receive.denyCurrentBranch ignore", evalcontract.Reject, "interlock-disable"},
	{"git config remote.origin.pushurl https://x.invalid/", evalcontract.Reject, "push-fetch-redirect"},
	{"git config remote.origin.mirror true", evalcontract.Reject, "push-fetch-redirect"},
	{"git config remote.origin.receivepack x", evalcontract.Reject, "exec-sink"},
	{"git config CORE.HOOKSPATH /tmp/h", evalcontract.Reject, "key names are case-insensitive"},

	// --global/--system turn Approve into Abstain; a Reject stays Reject.
	{"git config --global core.fsmonitor true", evalcontract.Abstain, "global scope affects every repo"},
	{"git config --system core.editor true", evalcontract.Abstain, "system scope affects every repo"},
	{"git config --global core.pager less", evalcontract.Reject, "scope never softens a Reject"},
	{"git config --local core.fsmonitor true", evalcontract.Approve, "local scope is the default"},

	// alias.*: with ! classified by the shell it hides.
	{"git config alias.nuke '!rm -rf /'", evalcontract.Abstain, "classified by the hidden shell's own effects (rm -rf / is path:delete /, which the engine abstains on), not by the key"},
	{"git config alias.show1 '!echo hi'", evalcontract.Approve, "the hidden shell is inert"},
	{"git config alias.lg log", evalcontract.Abstain, "an alias without ! can hide a forbidden verb"},
	{"git config --global alias.show1 '!echo hi'", evalcontract.Abstain, "global scope"},

	// Runtime expansion in the key or value is never judged.
	{`git config core.fsmonitor "$V"`, evalcontract.Abstain, "dynamic value"},
	{`git config "$K" true`, evalcontract.Abstain, "dynamic key"},

	// Read forms are unchanged.
	{"git config --get user.name", evalcontract.Approve, "read form"},
	{"git config user.name", evalcontract.Approve, "bare read form"},
	{"git config --global user.name", evalcontract.Abstain, "scope flag on a read form stays unmodeled (golden git_config_global_key)"},
}

func TestGitConfigKey(t *testing.T) {
	root, _ := fixture(t)
	reg := cmddesc.DefaultRegistry()
	for _, r := range gitConfigKeyRows {
		cmd := strings.ReplaceAll(r.command, "<ROOT>", root)
		resp := Evaluate(evalcontract.Request{Command: cmd, CWD: root, ProjectRoot: root}, reg, DefaultPolicies(), DefaultGraphPolicies())
		if resp.Decision != r.want {
			t.Errorf("%s\n  decision = %s, want %s (%s)\n  reason: %s", r.command, resp.Decision, r.want, r.note, resp.Reason)
		}
	}
}

// TestGitConfigFilePathWrite: `--file PATH` is ALSO a path write judged by
// P5 and the ordinary path policy, independent of the key's class.
func TestGitConfigFilePathWrite(t *testing.T) {
	root, _ := fixture(t)
	reg := cmddesc.DefaultRegistry()
	eval := func(cmd string) evalcontract.Response {
		return Evaluate(evalcontract.Request{Command: cmd, CWD: root, ProjectRoot: root}, reg, DefaultPolicies(), DefaultGraphPolicies())
	}
	// Inert key, in-project file: approved.
	if r := eval("git config --file " + root + "/extra.cfg core.fsmonitor true"); r.Decision != evalcontract.Approve {
		t.Errorf("in-project --file: %s (%s), want approve", r.Decision, r.Reason)
	}
	// Inert key, but the file is outside every writable zone: the PATH write
	// is judged on its own and does not approve.
	if r := eval("git config --file /etc/gitconfig-x core.fsmonitor true"); r.Decision == evalcontract.Approve {
		t.Errorf("--file /etc/...: approved, want the path write to block it (%s)", r.Reason)
	}
	// The file is a repo's .git/config in a NON-temp repo: P5 rejects it even
	// though the key is inert.
	defer pathspec.SetTempClassifierForTest(func(string) bool { return false })()
	if r := eval("git config --file " + root + "/.git/config core.fsmonitor true"); r.Decision != evalcontract.Reject {
		t.Errorf("--file .git/config (non-temp): %s (%s), want reject", r.Decision, r.Reason)
	}
}

// TestGitDirWriteJudgment: P5 at the policy layer - non-git-verb writes to
// .git are Rejected (non-temp repo) while git's own verbs, which declare
// their .git writes as implicit effects, are untouched.
func TestGitDirWriteJudgment(t *testing.T) {
	root, _ := fixture(t)
	reg := cmddesc.DefaultRegistry()
	eval := func(cmd string) evalcontract.Response {
		return Evaluate(evalcontract.Request{Command: cmd, CWD: root, ProjectRoot: root}, reg, DefaultPolicies(), DefaultGraphPolicies())
	}
	t.Run("non-temp repo", func(t *testing.T) {
		defer pathspec.SetTempClassifierForTest(func(string) bool { return false })()
		for _, cmd := range []string{
			"echo x > .git/hooks/pre-commit",
			"echo x > " + root + "/.git/config",
			"tee .git/config",
			"cp README.md .git/hooks/pre-commit",
			"sed -i s/a/b/ .git/config",
		} {
			if r := eval(cmd); r.Decision != evalcontract.Reject {
				t.Errorf("%s: %s (%s), want reject", cmd, r.Decision, r.Reason)
			}
		}
		// git's own verbs are judged by the git spec, not by P5.
		for _, cmd := range []string{"git add -A", "git commit -m x"} {
			if r := eval(cmd); r.Decision == evalcontract.Reject {
				t.Errorf("%s: rejected (%s); a git verb's implicit .git write must not hit P5", cmd, r.Reason)
			}
		}
	})
	t.Run("temp repo has full access (ADR 0059 / R11)", func(t *testing.T) {
		for _, cmd := range []string{"echo x > .git/hooks/pre-commit", "echo x > .git/config"} {
			if r := eval(cmd); r.Decision == evalcontract.Reject {
				t.Errorf("%s: rejected (%s), want not-Reject in a temp repo", cmd, r.Reason)
			}
		}
	})
}
