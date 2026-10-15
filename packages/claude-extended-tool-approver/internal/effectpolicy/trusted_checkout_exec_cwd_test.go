package effectpolicy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmddesc"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/evalcontract"
)

// cwdFixture is a throwaway tree with every kind of directory the effective
// working directory tests need: root (a pn workspace: marker only), set (a
// marker-less workforest set under it), git (a git checkout with a "sub"
// directory), and bare (no marker anywhere above it).
type cwdFixture struct{ root, set, git, bare string }

func newCWDFixture(t *testing.T) cwdFixture {
	t.Helper()
	root, set, bare := markerWorkspace(t)
	git := filepath.Join(filepath.Dir(root), "repo")
	for _, d := range []string{filepath.Join(git, ".git"), filepath.Join(git, "sub")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return cwdFixture{root: root, set: set, git: git, bare: bare}
}

func (f cwdFixture) dir(name string) string {
	switch name {
	case "root":
		return f.root
	case "set":
		return f.set
	case "git":
		return f.git
	default:
		return f.bare
	}
}

// TestTrustedCheckoutExecEffectiveCWD pins bead pg2-xj975: an Exec fact is
// judged against the directory its command runs in (the cd-tracked one), in
// both directions, and every compound shape the tracking cannot follow soundly
// fails closed. The rows are the executable form of the shape table in
// effectgraph's "EXEC WORKING DIRECTORY" comment. {set}/{git}/{bare}/{root}
// expand to the fixture directories.
func TestTrustedCheckoutExecEffectiveCWD(t *testing.T) {
	f := newCWDFixture(t)
	reg := cmddesc.DefaultRegistry()
	const (
		approve = evalcontract.Approve
		abstain = evalcontract.Abstain
	)
	type row struct {
		name string
		from string // request cwd: root | set | git | bare
		cmd  string
		want evalcontract.Decision
	}
	rows := []row{
		// --- unchanged: no cd, the request cwd decides ---
		{"no cd, marker cwd", "set", "pn workspace build", approve},
		{"no cd, git cwd", "git", "go test ./...", approve},
		{"no cd, bare cwd", "bare", "go test ./...", abstain},

		// --- direction A: cd INTO a trusted dir from an untrusted request cwd ---
		{"cd marker set && pn from bare", "bare", "cd {set} && pn workspace build", approve},
		{"cd marker root && pnwf from bare", "bare", "cd {root} && pnwf sync-fetch --set", approve},
		{"cd git && go test from bare", "bare", "cd {git} && go test ./...", approve},
		{"cd into marker, pipeline stage keeps the guard", "bare", "cd {set} && pn workspace build 2>&1 | tail -5", approve},
		{"cd into marker, subshell right side keeps the guard", "bare", "cd {set} && (pn workspace build)", approve},
		{"cd into marker, block right side keeps the guard", "bare", "cd {set} && { pn workspace build; }", approve},
		{"cd into marker, third member of the chain", "bare", "cd {set} && true && pn workspace build", approve},
		{"cd into marker, inside a subshell", "bare", "(cd {set} && pn workspace build)", approve},
		{"cd into marker, substitution after the cd", "bare", "cd {set} && echo $(pn workspace build)", approve},
		{"cd into marker, bash -c child", "bare", "cd {set} && bash -c 'pn workspace build'", approve},
		{"cd marker, then a second cd back into marker", "bare", "cd {set} && cd {root} && pn workspace build", approve},
		{"relative cd under a marker request cwd", "root", "cd .workforests/feat && pn workspace build", approve},
		{"relative cd with dot-dot joins lexically", "bare", "cd ../ws/.workforests/feat && pn workspace build", approve},
		{"cd in an absolute loop body", "bare", "for i in 1 2; do cd {set} && pn workspace build; done", approve},

		// --- direction B: a cd OUT of a trusted request cwd no longer rides on it ---
		{"cd bare && go test from git", "git", "cd {bare} && go test ./...", abstain},
		{"cd bare && pn from marker", "set", "cd {bare} && pn workspace build", abstain},
		{"cd parent of the checkout && go test", "git", "cd .. && go test ./...", abstain},
		{"cd bare, pipeline stage", "git", "cd {bare} && go test ./... | tail -5", abstain},
		{"cd bare, subshell", "git", "(cd {bare} && go test ./...)", abstain},
		{"cd bare, inner subshell", "git", "cd {bare} && (go test ./...)", abstain},
		{"cd bare, substitution after the cd", "git", "cd {bare} && echo $(go test ./...)", abstain},
		{"cd bare, bash -c child", "git", "cd {bare} && bash -c 'go test ./...'", abstain},
		{"cd bare, then back into git still judged by every member", "git", "cd {bare} && cd {git} && go test ./...", approve},
		{"cd within the checkout stays trusted", "git", "cd sub && go test ./...", approve},
		{"cd to another checkout from bare cwd", "bare", "cd {git}/sub && go test ./...", approve},

		// --- order: only a cd BEFORE the leaf counts ---
		{"cd after the exec does not matter (bare target)", "git", "go test ./... && cd {bare}", approve},
		{"cd after the exec does not matter (trusted target)", "bare", "go test ./... && cd {git}", abstain},
		{"substitution runs before a later cd", "bare", "echo $(go test ./...) && cd {git}", abstain},
		{"a confined subshell cd does not reach later leaves", "git", "(cd {bare}); go test ./...", approve},
		{"a confined subshell cd does not help later leaves", "bare", "(cd {git}); go test ./...", abstain},
		{"a pipeline-stage cd is confined", "git", "cd {bare} | cat; go test ./...", approve},

		// --- a cd that may not have taken effect: EVERY possible dir must be trusted ---
		{"cd; exec, target untrusted", "git", "cd {bare}; go test ./...", abstain},
		{"cd; exec, target trusted but request cwd untrusted", "bare", "cd {git}; go test ./...", abstain},
		{"cd; exec, both trusted", "git", "cd {set}; go test ./...", approve},
		{"cd || exec", "bare", "cd {git} || go test ./...", abstain},
		{"cd with || true then exec", "bare", "cd {set} || true; pn workspace build", abstain},
		{"cd && true, then ; exec (chain broken)", "git", "cd {bare} && true; go test ./...", abstain},
		{"cd in an if body", "bare", "if true; then cd {git}; fi; go test ./...", abstain},
		{"cd in an if body, both trusted", "git", "if true; then cd {set}; fi; go test ./...", approve},
		{"cd in a block, then exec", "bare", "{ cd {git}; }; go test ./...", abstain},

		// --- shapes the tracking cannot follow: fail closed ---
		{"cd to a variable", "git", `cd "$D" && go test ./...`, abstain},
		{"cd to a variable, request cwd untrusted", "bare", `cd "$D" && go test ./...`, abstain},
		{"cd - (previous directory)", "git", "cd - && go test ./...", abstain},
		{"cd to a variable-prefixed path", "git", `cd "$HOME/x" && go test ./...`, abstain},
		{"cd to a command substitution", "git", "cd $(pwd) && go test ./...", abstain},
		{"pushd (no schema)", "bare", "pushd {git} && go test ./...", abstain},
		{"popd (no schema)", "git", "popd && go test ./...", abstain},
		{"relative cd in a loop is re-applied", "git", "for i in 1 2; do cd sub && go test ./...; done", abstain},
		{"cd to ~user", "git", "cd ~root && go test ./...", abstain},
		{"cd with two operands", "git", "cd a b && go test ./...", abstain},
		{"builtin-prefixed cd (not modeled)", "bare", "builtin cd {git} && go test ./...", abstain},
	}
	for _, tc := range rows {
		t.Run(tc.name, func(t *testing.T) {
			cmd := tc.cmd
			for _, n := range []string{"root", "set", "git", "bare"} {
				cmd = strings.ReplaceAll(cmd, "{"+n+"}", f.dir(n))
			}
			cwd := f.dir(tc.from)
			resp := Evaluate(evalcontract.Request{Command: cmd, CWD: cwd, ProjectRoot: cwd}, reg, DefaultPolicies(), DefaultGraphPolicies())
			if resp.Decision != tc.want {
				t.Errorf("from %s: %q\n decision = %s, want %s (reason: %s)", tc.from, cmd, resp.Decision, tc.want, resp.Reason)
			}
		})
	}
}

// TestTrustedCheckoutExecEffectiveCWD_Home pins `~` handling: a bare `cd` and
// `cd ~/x` go to the evaluator's HOME, so they are judged there.
func TestTrustedCheckoutExecEffectiveCWD_Home(t *testing.T) {
	f := newCWDFixture(t)
	reg := cmddesc.DefaultRegistry()
	run := func(home, from, cmd string) evalcontract.Decision {
		t.Setenv("HOME", home)
		cwd := f.dir(from)
		return Evaluate(evalcontract.Request{Command: cmd, CWD: cwd, ProjectRoot: cwd}, reg, DefaultPolicies(), DefaultGraphPolicies()).Decision
	}
	if got := run(f.set, "bare", "cd && pn workspace build"); got != evalcontract.Approve {
		t.Errorf("bare cd with HOME in a marker dir from bare: %s, want approve", got)
	}
	if got := run(f.root, "bare", "cd ~/.workforests/feat && pn workspace build"); got != evalcontract.Approve {
		t.Errorf("cd ~/x with HOME at a marker root: %s, want approve", got)
	}
	if got := run(f.bare, "git", "cd && go test ./..."); got != evalcontract.Abstain {
		t.Errorf("bare cd with HOME outside any marker from a git cwd: %s, want abstain", got)
	}
	if got := run(f.bare, "git", "cd ~ && go test ./..."); got != evalcontract.Abstain {
		t.Errorf("cd ~ with HOME outside any marker from a git cwd: %s, want abstain", got)
	}
}

// TestTrustedCheckoutExecPolicy_StampedDirs pins the policy half in isolation:
// every stamped directory must be trusted; unknown, empty, relative and
// expansion-bearing ones are Unknown; an unstamped effect uses the request cwd.
func TestTrustedCheckoutExecPolicy_StampedDirs(t *testing.T) {
	f := newCWDFixture(t)
	mk := func(dirs ...string) cmddesc.Effect {
		e := cmddesc.Effect{Kind: cmddesc.EffectExec, Source: "go test"}
		e.SetCWDs(dirs)
		return e
	}
	for _, tc := range []struct {
		name string
		e    cmddesc.Effect
		req  string
		want FindingVerdict
	}{
		{"unstamped uses the request cwd", cmddesc.Effect{Kind: cmddesc.EffectExec}, f.git, Permitted},
		{"unstamped, untrusted request cwd", cmddesc.Effect{Kind: cmddesc.EffectExec}, f.bare, Unknown},
		{"stamp overrides the request cwd", mk(f.bare), f.git, Unknown},
		{"stamp overrides an untrusted request cwd", mk(f.git), f.bare, Permitted},
		{"every member must be trusted", mk(f.git, f.bare), f.git, Unknown},
		{"all members trusted", mk(f.git, f.set), f.bare, Permitted},
		{"empty member", mk("", f.git), f.git, Unknown},
		{"relative member", mk("sub"), f.git, Unknown},
		{"member with an expansion", mk("/tmp/$X"), f.git, Unknown},
		{"member with a backtick", mk("/tmp/`x`"), f.git, Unknown},
		{"tilde with no evaluator", mk("~/x"), f.git, Unknown},
		{"unknown flag", func() cmddesc.Effect { e := mk(f.git); e.CWDUnknown = true; return e }(), f.git, Unknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fnd, applies := (TrustedCheckoutExec{}).Judge(tc.e, PolicyContext{CWD: tc.req})
			if !applies || fnd.Verdict != tc.want {
				t.Errorf("applies=%v verdict=%s (%s), want %s", applies, fnd.Verdict, fnd.Reason, tc.want)
			}
		})
	}
}

// TestTrustedCheckoutExecPolicy_StampedDirsBuildToolFamily: a build-tool-family
// Exec fact (just build) is judged per stamped directory too: the operator
// declaration plus the workspace's own justfile must hold in EVERY directory
// the command may run in.
func TestTrustedCheckoutExecPolicy_StampedDirsBuildToolFamily(t *testing.T) {
	f := newCWDFixture(t)
	if err := os.WriteFile(filepath.Join(f.git, "justfile"), []byte("build:\n    true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := PolicyContext{CWD: f.bare, BuildToolVerbs: []evalcontract.VerbScopedApproval{{Tool: "just", Verb: "build"}}}
	mk := func(dirs ...string) cmddesc.Effect {
		e := cmddesc.Effect{Kind: cmddesc.EffectExec, Family: "just", Operation: "build"}
		e.SetCWDs(dirs)
		return e
	}
	for _, tc := range []struct {
		name string
		e    cmddesc.Effect
		want FindingVerdict
	}{
		{"the declaring directory", mk(f.git), Permitted},
		{"a directory with no justfile", mk(f.bare), Unknown},
		{"one of two directories lacks it", mk(f.git, f.bare), Unknown},
		{"unstamped falls back to the (unmarked) request cwd", cmddesc.Effect{Kind: cmddesc.EffectExec, Family: "just", Operation: "build"}, Unknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fnd, applies := (TrustedCheckoutExec{}).Judge(tc.e, ctx)
			if !applies || fnd.Verdict != tc.want {
				t.Errorf("applies=%v verdict=%s (%s), want %s", applies, fnd.Verdict, fnd.Reason, tc.want)
			}
		})
	}
}
