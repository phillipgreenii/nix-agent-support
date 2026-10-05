package effectpolicy

import (
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmddesc"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/evalcontract"
)

// TestShellStatementForms (pg2-dbrsg) pins the verdicts of the shell statement
// forms skills instruct — assignments from command substitution, `[ ]` tests,
// loops, case and the read/shift/exit/pwd builtins — and, as importantly, the
// safety edges: an assignment, loop or test never launders a command that does
// not itself approve, and the constructs whose operands are evaluated as
// arithmetic stay unmodeled. reasonHas is a fragment the abstain/reject reason
// must carry, so a row cannot start passing for the wrong reason.
func TestShellStatementForms(t *testing.T) {
	tests := []struct {
		name      string
		command   string
		want      evalcontract.Decision
		reasonHas string
	}{
		// --- X="$(cmd)" ---------------------------------------------------
		{"assign: approved inner command", `X="$(git rev-parse --show-toplevel)"`, evalcontract.Approve, ""},
		{"assign: backtick form", "X=`git rev-parse HEAD`", evalcontract.Approve, ""},
		{"assign: pipeline inside", `X=$(git remote | grep -c .)`, evalcontract.Approve, ""},
		{"assign: static value", `X=literal`, evalcontract.Approve, ""},
		{"assign: parameter-only value", `P="$HOME/x"`, evalcontract.Approve, ""},
		{"assign: two substitutions", `X="$(git rev-parse HEAD)-$(git rev-parse --abbrev-ref HEAD)"`, evalcontract.Approve, ""},
		{"assign: default word substitution", `X=${Y:-$(git rev-parse HEAD)}`, evalcontract.Approve, ""},
		{"assign then use", `X=$(git rev-parse HEAD) && echo "$X"`, evalcontract.Approve, ""},
		{"assign: inner command not approved", `X=$(rm -rf foo)`, evalcontract.Abstain, "rm"},
		{"assign: inner command unknown", `X=$(frobnicate --all)`, evalcontract.Abstain, "no schema for frobnicate"},
		{"assign: nested inner command not approved", `X=$(echo $(rm -rf foo))`, evalcontract.Abstain, "rm"},
		{"assign: one bad substitution among good ones", `X="$(git rev-parse HEAD)$(rm -rf foo)"`, evalcontract.Abstain, "rm"},
		{"assign: inner secret read", `X=$(cat ~/.ssh/id_rsa)`, evalcontract.Reject, "credential"},
		{"prefix assign: inner command not approved", `X=$(rm -rf foo) echo hi`, evalcontract.Abstain, "rm"},
		{"prefix assign: inner command approved", `X=$(git rev-parse HEAD) echo hi`, evalcontract.Approve, ""},
		{"assign: arithmetic is unmodeled", `X=$((1+2))`, evalcontract.Abstain, "arithmetic"},
		{"assign: arithmetic hiding a substitution", `X=$(( $(rm -rf foo) ))`, evalcontract.Abstain, "arithmetic"},
		{"prefix assign: arithmetic is unmodeled", `X=$((1+2)) true`, evalcontract.Abstain, "arithmetic"},
		{"assign: process substitution", `X=<(cat README.md)`, evalcontract.Abstain, "process substitution"},
		{"assign: indirect expansion", `X=${!Y}`, evalcontract.Abstain, "indirect"},
		{"assign: array element assignment", `a[$i]=v`, evalcontract.Abstain, "no executable"},
		{"assign: PATH hijack still rejected", `PATH=/tmp/evil:$PATH; ls`, evalcontract.Reject, "hijack"},
		{"assign: injector name still rejected", `LD_PRELOAD=/tmp/x.so`, evalcontract.Reject, "injector"},
		{"assign: persistent IFS", `IFS=/; echo hi`, evalcontract.Abstain, "IFS"},
		{"assign: persistent CDPATH", `CDPATH=/etc; cd foo`, evalcontract.Abstain, "CDPATH"},
		{"assign: persistent GIT_DIR", `GIT_DIR=/tmp/x; git status`, evalcontract.Reject, "GIT_DIR"},
		{"prefix IFS on read stays approvable", `IFS=: read -r a b`, evalcontract.Approve, ""},
		{"mktemp -d idiom", `T=$(mktemp -d)`, evalcontract.Approve, ""},
		{"mktemp -d with a directory is graded", `T=$(mktemp -d -p /etc)`, evalcontract.Abstain, "mktemp"},

		// --- [ ] / test ---------------------------------------------------
		{"test: -z", `[ -z "$X" ]`, evalcontract.Approve, ""},
		{"test: -n", `[ -n "$X" ]`, evalcontract.Approve, ""},
		{"test: -d", `[ -d /tmp ]`, evalcontract.Approve, ""},
		{"test: -f", `test -f README.md`, evalcontract.Approve, ""},
		{"test: -gt", `[ "$N" -gt 3 ]`, evalcontract.Approve, ""},
		{"test: -eq", `[ "$N" -eq 0 ]`, evalcontract.Approve, ""},
		{"test: string compare", `[ "$a" = "$b" ]`, evalcontract.Approve, ""},
		{"test: negation", `[ ! -e /tmp/x ]`, evalcontract.Approve, ""},
		{"test: -a connective", `[ -n "$a" -a -n "$b" ]`, evalcontract.Approve, ""},
		{"test: and-list", `[ -n "$a" ] && echo yes`, evalcontract.Approve, ""},
		{"test: or-list", `[ -n "$a" ] || echo no`, evalcontract.Approve, ""},
		{"test: if", `if [ -z "$X" ]; then echo hi; fi`, evalcontract.Approve, ""},
		{"test: substitution operand approved", `[ -n "$(git status --porcelain)" ]`, evalcontract.Approve, ""},
		{"test: substitution operand not approved", `[ -n "$(rm -rf foo)" ]`, evalcontract.Abstain, "rm"},
		{"test: -v takes a subscriptable name", `[ -v foo ]`, evalcontract.Abstain, "unknown flag -v"},
		{"test: -R takes a subscriptable name", `[ -R foo ]`, evalcontract.Abstain, "unknown flag -R"},
		{"test: unknown operator", `[ -Q foo ]`, evalcontract.Abstain, "unknown flag -Q"},
		{"double bracket is unmodeled", `[[ -z "$X" ]]`, evalcontract.Abstain, "no executable"},
		{"arithmetic command is unmodeled", `(( i++ ))`, evalcontract.Abstain, "no executable"},
		{"let is unmodeled", `let a=1`, evalcontract.Abstain, "no executable"},

		// --- loops / case -------------------------------------------------
		{"for: literal list", `for f in a b; do bd show "id-$f"; done`, evalcontract.Approve, ""},
		// pg2-5ctay: the loop variable is not resolved to its word list, so a bare
		// "$f" positional could be an option; a literal prefix or an inert
		// command (echo) keeps the loop approvable.
		{"for: loop variable as bare positional", `for f in a b; do bd show "$f"; done`, evalcontract.Abstain, "option injection"},
		{"for: substitution list approved", `for f in $(ls); do echo "$f"; done`, evalcontract.Approve, ""},
		{"for: substitution list not approved", `for f in $(rm -rf foo); do echo "$f"; done`, evalcontract.Abstain, "rm"},
		{"for: body not approved", `for f in a b; do rm -rf "$f"; done`, evalcontract.Abstain, "rm"},
		{"for: body secret read", `for f in a b; do cat ~/.ssh/id_rsa; done`, evalcontract.Reject, "credential"},
		{"while: test condition", `while [ -d /tmp ]; do echo hi; break; done`, evalcontract.Abstain, "break"},
		{"while read: stdin", `while read -r l; do echo "$l"; done`, evalcontract.Approve, ""},
		{"while read: IFS prefix and file", `while IFS='=' read -r k v; do echo "$k"; done < README.md`, evalcontract.Approve, ""},
		{"while read: secret input file", `while read -r l; do echo "$l"; done < ~/.ssh/id_rsa`, evalcontract.Reject, "credential"},
		{"case: arms and assignment", `case "$p" in /*) ;; *) p="$W/$p" ;; esac`, evalcontract.Approve, ""},
		{"case: subject substitution not approved", `case "$(rm -rf foo)" in a) echo hi ;; esac`, evalcontract.Abstain, "rm"},
		{"case: arm body not approved", `case "$x" in a) rm -rf foo ;; esac`, evalcontract.Abstain, "rm"},

		// --- builtins -----------------------------------------------------
		{"pwd", `pwd`, evalcontract.Approve, ""},
		{"pwd -P", `pwd -P`, evalcontract.Approve, ""},
		{"pwd unknown flag", `pwd -z`, evalcontract.Abstain, "unknown flag"},
		{"shift", `shift`, evalcontract.Approve, ""},
		{"shift n", `shift 2`, evalcontract.Approve, ""},
		{"exit", `exit 0`, evalcontract.Approve, ""},
		{"exit negative status", `exit -1`, evalcontract.Approve, ""},
		{"read", `read -r X`, evalcontract.Approve, ""},
		{"read prompt", `read -r -p "continue? " ans`, evalcontract.Approve, ""},
		{"read array", `read -r -a arr`, evalcontract.Approve, ""},
		{"read PATH", `read PATH`, evalcontract.Abstain, "PATH"},
		{"read HOME", `read HOME`, evalcontract.Abstain, "HOME"},
		{"read injector name", `read LD_PRELOAD`, evalcontract.Reject, "injector"},
		{"read IFS", `read IFS`, evalcontract.Abstain, "IFS"},
		{"read from a descriptor", `read -u 3 line`, evalcontract.Abstain, "unknown flag -u"},
		{"read dynamic variable name", `read -r "$name"`, evalcontract.Abstain, "runtime expansion"},
	}
	root, _ := fixture(t)
	reg := cmddesc.DefaultRegistry()
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp := Evaluate(evalcontract.Request{Command: tc.command, CWD: root, ProjectRoot: root}, reg, DefaultPolicies(), DefaultGraphPolicies())
			if resp.Decision != tc.want {
				t.Fatalf("%s\n  decision = %s, want %s\n  reason: %s", tc.command, resp.Decision, tc.want, resp.Reason)
			}
			if tc.reasonHas != "" && !strings.Contains(resp.Reason, tc.reasonHas) {
				t.Errorf("%s\n  reason %q does not contain %q", tc.command, resp.Reason, tc.reasonHas)
			}
		})
	}
}

// TestCapturedContentReachesTheContentFlowPolicy: capturing local content in a
// shell variable (or feeding a loop from a file) must keep that content
// upstream of every later command, or NoContentFlowToUnvettedNetwork loses
// track of it. The graph policy only fires on an OUTBOUND sink, and a vetted
// upload is also gated per-node, so the observable here is the graph policy's
// own finding on the built graph: with captured content upstream it asks
// ("upload of local content to a vetted host requires consent"); in the
// controls nothing local was captured and it says nothing.
func TestCapturedContentReachesTheContentFlowPolicy(t *testing.T) {
	root, _ := fixture(t)
	reg := cmddesc.DefaultRegistry()
	const sink = `curl -X POST --data fixed https://vetted.example/api`
	tests := []struct {
		name    string
		command string
		finding bool
	}{
		{"control: sink alone", sink, false},
		{"control: literal assignment first", `T=1; ` + sink, false},
		{"control: metadata capture first", `D=$(pwd); ` + sink, false},
		{"file content captured, then sink", `T=$(cat README.md); ` + sink, true},
		{"piped content read into a variable, then sink", `cat README.md | read -r L; ` + sink, true},
		{"loop fed from a file, sink in the body", `while read -r l; do ` + sink + `; done < README.md`, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp := Evaluate(evalcontract.Request{Command: tc.command, CWD: root, ProjectRoot: root, VettedHosts: []string{"vetted.example"}}, reg, DefaultPolicies(), DefaultGraphPolicies())
			var got []GraphFinding
			g := resp.Interpreted
			got = NoContentFlowToUnvettedNetwork{}.JudgeGraph(&g, PolicyContext{CWD: root, VettedHosts: []string{"vetted.example"}})
			if tc.finding {
				if len(got) != 1 || !strings.Contains(got[0].Reason, "requires consent") {
					t.Errorf("%s\n  want one 'requires consent' graph finding, got %+v", tc.command, got)
				}
			} else if len(got) != 0 {
				t.Errorf("%s\n  want no graph finding, got %+v", tc.command, got)
			}
		})
	}
}

// TestEnvAssignmentPersistent: a persistent (assignment-only / operand-role)
// write of a shell-behaviour variable is never Permitted on the name alone, a
// persistent GIT_DIR/GIT_INDEX_FILE is Forbidden, and the same names as a
// PREFIX assignment keep their previous verdicts.
func TestEnvAssignmentPersistent(t *testing.T) {
	mk := func(name string, persistent, gitInvoking bool) cmddesc.Effect {
		return cmddesc.Effect{Kind: cmddesc.EffectEnv, EnvName: name, EnvSet: true, EnvPersistent: persistent, EnvGitInvoking: gitInvoking}
	}
	tests := []struct {
		name string
		e    cmddesc.Effect
		want FindingVerdict
	}{
		{"persistent IFS", mk("IFS", true, false), Unknown},
		{"prefix IFS", mk("IFS", false, false), Permitted},
		{"persistent CDPATH", mk("CDPATH", true, false), Unknown},
		{"prefix CDPATH", mk("CDPATH", false, false), Permitted},
		{"persistent GLOBIGNORE", mk("GLOBIGNORE", true, false), Unknown},
		{"persistent PS4", mk("PS4", true, false), Unknown},
		{"persistent TMPDIR", mk("TMPDIR", true, false), Unknown},
		{"persistent GIT_DIR", mk("GIT_DIR", true, false), Forbidden},
		{"persistent GIT_INDEX_FILE", mk("GIT_INDEX_FILE", true, false), Forbidden},
		{"prefix GIT_DIR on a non-git command", mk("GIT_DIR", false, false), Permitted},
		{"prefix GIT_DIR on git", mk("GIT_DIR", false, true), Forbidden},
		{"persistent ordinary name", mk("FOO", true, false), Permitted},
		{"persistent injector", mk("LD_PRELOAD", true, false), Forbidden},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f, applies := EnvAssignment{}.Judge(tc.e, PolicyContext{})
			if !applies || f.Verdict != tc.want {
				t.Errorf("verdict = %v (applies=%v), want %v (%s)", f.Verdict, applies, tc.want, f.Reason)
			}
		})
	}
}
