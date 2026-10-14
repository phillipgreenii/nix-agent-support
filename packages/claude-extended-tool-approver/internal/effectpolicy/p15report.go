package effectpolicy

import (
	"fmt"
	"sort"
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmddesc"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/evalcontract"
)

// P15Entry is one row of an approve-reachability report: one spec fact (a
// flag of the command's schema, or a positional shape) probed through the
// real engine, with the decision the engine reached.
type P15Entry struct {
	// Fact names the spec fact under review: a flag spelling, or a
	// "(positional) ..." shape.
	Fact string
	// Probe is the command evaluated for this fact.
	Probe string
	// Decision is the engine's verdict for Probe.
	Decision evalcontract.Decision
}

// Permitted reports whether the fact feeds an approve path (P15's subject).
func (e P15Entry) Permitted() bool { return e.Decision == evalcontract.Approve }

// P15Report is the P15 "approve-reachability" report (docket tc-o14i5.3
// design P15): "any change that makes an effect kind Permitted (or adds an
// approve path) MUST ship a generated report of every spec flag that feeds
// that effect, reviewed with the change." It follows internal/speclint's
// Report shape (a Report holding one entry per fact examined), but its
// entries are produced by EVALUATING probes through the real engine
// (Evaluate with DefaultRegistry/DefaultPolicies), never by reading a spec
// value as a verdict (P14).
type P15Report struct {
	// Subject is the command whose approve paths are reviewed ("git push").
	Subject string
	// Generator and Test name the generator function and the pinning test,
	// echoed into the rendered header.
	Generator string
	Test      string
	Entries   []P15Entry
	// root, when set, is the probe root; Markdown renders it as <ROOT> so the
	// pinned report does not leak a fixture directory.
	root string
}

// PermittedEntries returns the entries that feed an approve path.
func (r *P15Report) PermittedEntries() []P15Entry {
	var out []P15Entry
	for _, e := range r.Entries {
		if e.Permitted() {
			out = append(out, e)
		}
	}
	return out
}

// probe evaluates one probe command through the real engine (default
// registry and policies) and appends the entry.
func (r *P15Report) probe(root string, reg cmddesc.Registry, fact, probe string) {
	resp := Evaluate(evalcontract.Request{Command: probe, CWD: root, ProjectRoot: root}, reg, DefaultPolicies(), DefaultGraphPolicies())
	r.Entries = append(r.Entries, P15Entry{Fact: fact, Probe: probe, Decision: resp.Decision})
}

// probeFlags probes every flag of schema, building each probe as
// prefix + flag (+ " x" for a one-argument flag) + suffix, sorted by flag.
func (r *P15Report) probeFlags(root string, reg cmddesc.Registry, schema cmddesc.CommandSchema, prefix, suffix string) {
	flags := make([]string, 0, len(schema.Flags))
	for f := range schema.Flags {
		flags = append(flags, f)
	}
	sort.Strings(flags)
	for _, f := range flags {
		probe := prefix + " " + f
		if schema.Flags[f].Arity == cmddesc.ArityOne {
			probe += " x"
		}
		r.probe(root, reg, "flag "+f, probe+suffix)
	}
}

// GenerateHeldFormsP15 builds the P15 report for the three pb-plugin forms
// pg2-xu7sz approves (operator rulings, Phillip, 2026-10-05): `gh pr ready
// --undo`, `git branch -D` and `git worktree remove` without --force. Every
// flag of each command's schema is probed (so a flag the change did not
// intend to approve shows up as a non-approve row), plus the positional and
// combination shapes the rulings name. Verdicts come from Evaluate, never
// from reading a spec value (P14). root is a project root the probes run in;
// the worktree probes name the fixture's `.worktrees/{clean,dirty,ignored-only}`
// slots, whose state the test's fake worktree-state probe fixes.
func GenerateHeldFormsP15(root string) (*P15Report, error) {
	reg := cmddesc.DefaultRegistry()
	git, ok := reg.Lookup("git")
	if !ok {
		return nil, fmt.Errorf("p15: no git schema registered")
	}
	gh, ok := reg.Lookup("gh")
	if !ok {
		return nil, fmt.Errorf("p15: no gh schema registered")
	}
	branch, ok := git.Subcommands["branch"]
	if !ok {
		return nil, fmt.Errorf("p15: no git branch schema registered")
	}
	wt, ok := git.Subcommands["worktree"].Subcommands["remove"]
	if !ok {
		return nil, fmt.Errorf("p15: no git worktree remove schema registered")
	}
	ready, ok := gh.Subcommands["pr"].Subcommands["ready"]
	if !ok {
		return nil, fmt.Errorf("p15: no gh pr ready schema registered")
	}

	rep := &P15Report{root: root, Subject: "pb held forms (gh pr ready --undo, git branch -D, git worktree remove)", Generator: "GenerateHeldFormsP15", Test: "TestP15ReportHeldForms"}
	rep.probeFlags(root, reg, ready, "gh pr ready", " 12")
	for _, p := range []struct{ fact, probe string }{
		{"(positional) gh pr ready, no --undo", "gh pr ready 12"},
		{"(positional) gh pr ready, no number and no --undo", "gh pr ready"},
		{"(positional) gh pr ready --undo, no number", "gh pr ready --undo"},
		{"(positional) gh pr ready --undo, URL", "gh pr ready --undo https://github.com/o/r/pull/12"},
		{"(positional) gh pr ready --undo, runtime-expanded number", `gh pr ready --undo "$N"`},
		{"(positional) gh pr ready after end-of-options", "gh pr ready -- --undo"},
		{"(combination) --undo with a value", "gh pr ready --undo=false 12"},
		{"(combination) --undo with --repo", "gh pr ready --repo o/r --undo 12"},
	} {
		rep.probe(root, reg, p.fact, p.probe)
	}

	rep.probeFlags(root, reg, branch, "git branch", " feature")
	for _, p := range []struct{ fact, probe string }{
		{"(positional) -D of a drain branch", "git branch -D drain/pg2-abc12"},
		{"(positional) -D of the primary branch", "git branch -D main"},
		{"(positional) -D of a remote-tracking ref", "git branch -r -D origin/feature"},
		{"(positional) -D with -C repo", "git -C " + root + " branch -D feature"},
		{"(positional) -D of a runtime-expanded name", `git branch -D "$B"`},
		{"(combination) -D with -f", "git branch -D -f feature"},
		{"(combination) -D with --force", "git branch -D --force feature"},
		{"(combination) -Df cluster", "git branch -Df feature"},
		{"(combination) -D with -m", "git branch -D -m feature other"},
		{"(combination) -f alone (force-move)", "git branch -f main origin/main"},
	} {
		rep.probe(root, reg, p.fact, p.probe)
	}

	rep.probeFlags(root, reg, wt, "git worktree remove", " .worktrees/clean")
	for _, p := range []struct{ fact, probe string }{
		{"(positional) clean worktree, no --force", "git worktree remove .worktrees/clean"},
		{"(positional) dirty worktree, no --force (git itself refuses)", "git worktree remove .worktrees/dirty"},
		{"(positional) ignored-files-only worktree, no --force (residual risk)", "git worktree remove .worktrees/ignored-only"},
		{"(positional) absolute worktree path, no --force", "git worktree remove " + root + "/.worktrees/clean"},
		{"(positional) no operand", "git worktree remove"},
		{"(positional) runtime-expanded path", `git worktree remove "$WT"`},
		{"(combination) --force of a clean worktree", "git worktree remove --force .worktrees/clean"},
		{"(combination) --force of a dirty worktree", "git worktree remove --force .worktrees/dirty"},
		{"(combination) --force of an ignored-files-only worktree", "git worktree remove -f .worktrees/ignored-only"},
		{"(combination) doubled --force of a clean worktree", "git worktree remove -f -f .worktrees/clean"},
	} {
		rep.probe(root, reg, p.fact, p.probe)
	}
	return rep, nil
}

// GenerateAssignSubstP15 builds the P15 report for pg2-aj84b: a bare
// assignment whose value holds a command substitution (`X=$(cmd ...)`) is
// graded by recursing into the substitution, so it approves exactly when every
// command inside approves. The "spec fact" this approve path hangs on is the
// SHAPE of the assignment and of its inner command, so each row probes one
// shape: the plugin-instructed forms the bead names, the quoting spellings,
// and the negative shapes that must keep abstaining or rejecting (an inner
// command that abstains, a pipeline stage that abstains, a live positional,
// arithmetic, backticks around an abstaining command, a persistent
// shell-behaviour or git-redirecting variable, a nested substitution, and a
// prefix assignment). Verdicts come from Evaluate, never from reading a spec
// value (P14). root is a project root the probes run in.
func GenerateAssignSubstP15(root string) (*P15Report, error) {
	reg := cmddesc.DefaultRegistry()
	if _, ok := reg.Lookup("pnwf"); !ok {
		return nil, fmt.Errorf("p15: no pnwf schema registered")
	}
	rep := &P15Report{root: root, Subject: "assignment with command substitution (X=$(cmd))", Generator: "GenerateAssignSubstP15", Test: "TestP15ReportAssignSubst"}
	for _, p := range []struct{ fact, probe string }{
		{"(inner) approving pnwf verb", "landing=$(pnwf land-plan feature-x)"},
		{"(inner) approving pnwf verb, quoted substitution", `landing="$(pnwf land-plan feature-x)"`},
		{"(inner) approving pnwf verb piped to jq", "canonical_root=$(pnwf resolve | jq -r '.canonical_root')"},
		{"(inner) env -u wrapping pn workspace info piped to jq", "set_count=$(env -u PN_WORKSPACE_ROOT pn workspace info --json | jq '.sets | length')"},
		{"(inner) approving read of the repository", "X=$(git rev-parse --show-toplevel)"},
		{"(inner) approving command, backtick spelling", "X=`git rev-parse --show-toplevel`"},
		{"(inner) approving command with stderr discarded", "X=$(git rev-parse --show-toplevel 2>/dev/null)"},
		{"(inner) bare mktemp -d idiom", "X=$(mktemp -d)"},
		{"(inner) abstaining pnwf verb (not in the schema)", "landing=$(pnwf frobnicate)"},
		{"(inner) abstaining command with no schema", "X=$(frobnicate)"},
		{"(inner) abstaining first pipeline stage", "X=$(frobnicate | jq .)"},
		{"(inner) abstaining last pipeline stage", "canonical_root=$(pnwf resolve | frobnicate)"},
		{"(inner) abstaining second command of a list", "X=$(pnwf resolve; frobnicate)"},
		{"(inner) approving command, live positional", `landing=$(pnwf land-plan "$BRANCH")`},
		{"(inner) delete of a writable path", "X=$(rm -rf foo)"},
		{"(inner) delete wrapped by env -u", "X=$(env -u FOO rm -rf foo)"},
		{"(inner) abstaining command, backtick spelling", "X=`frobnicate`"},
		{"(inner) nested substitution holding an abstaining command", "X=$(echo $(frobnicate))"},
		{"(inner) nested substitution holding an approving command", "X=$(echo $(pnwf resolve))"},
		{"(inner) redirect of the substitution into a forbidden path", "X=$(pnwf resolve > /nix/store/x)"},
		{"(inner) substitution reading a secret", "X=$(cat ~/.ssh/id_rsa)"},
		{"(shape) arithmetic expansion", "X=$((1+2))"},
		{"(shape) prefix assignment on an approving command", "X=$(pnwf resolve) echo hi"},
		{"(shape) prefix assignment holding an abstaining substitution", "X=$(frobnicate) echo hi"},
		{"(shape) persistent IFS assignment from an approving substitution", "IFS=$(pnwf resolve)"},
		{"(shape) persistent PATH assignment from an approving substitution", "PATH=$(pnwf resolve)"},
		{"(shape) persistent GIT_DIR assignment from an approving substitution", "GIT_DIR=$(pnwf resolve); git status"},
		{"(shape) export of an approving substitution", "export X=$(pnwf resolve)"},
		{"(shape) assignment chained to an abstaining command", "X=$(pnwf resolve) && frobnicate"},
		{"(shape) array-element assignment", "a[0]=$(pnwf resolve)"},
	} {
		rep.probe(root, reg, p.fact, p.probe)
	}
	return rep, nil
}

// GenerateGitPushP15 builds the P15 report for `git push` (the first
// instance: R6 makes push Permitted). root is a project root the probes run
// in (the verdicts do not depend on it; Evaluate needs one).
func GenerateGitPushP15(root string) (*P15Report, error) {
	reg := cmddesc.DefaultRegistry()
	git, ok := reg.Lookup("git")
	if !ok {
		return nil, fmt.Errorf("p15: no git schema registered")
	}
	push, ok := git.Subcommands["push"]
	if !ok {
		return nil, fmt.Errorf("p15: no git push schema registered")
	}
	rep := &P15Report{Subject: "git push", Generator: "GenerateGitPushP15", Test: "TestP15ReportGitPush"}
	eval := func(fact, probe string) {
		resp := Evaluate(evalcontract.Request{Command: probe, CWD: root, ProjectRoot: root}, reg, DefaultPolicies(), DefaultGraphPolicies())
		rep.Entries = append(rep.Entries, P15Entry{Fact: fact, Probe: probe, Decision: resp.Decision})
	}
	flags := make([]string, 0, len(push.Flags))
	for f := range push.Flags {
		flags = append(flags, f)
	}
	sort.Strings(flags)
	for _, f := range flags {
		probe := "git push " + f + " origin feature"
		if push.Flags[f].Arity == cmddesc.ArityOne {
			probe = "git push " + f + " x origin feature"
		}
		eval("flag "+f, probe)
	}
	for _, p := range []struct{ fact, probe string }{
		{"(positional) no remote argument (default remote)", "git push"},
		{"(positional) configured remote NAME + plain ref", "git push origin main"},
		{"(positional) src:dst refspec", "git push origin HEAD:main"},
		{"(positional) URL remote", "git push https://example.invalid/r.git main"},
		{"(positional) path remote", "git push ../other main"},
		{"(positional) runtime-expanded remote", `git push "$REMOTE" main`},
		{"(positional) force refspec +src", "git push origin +main"},
		{"(positional) delete refspec of a non-primary branch", "git push origin :feature"},
		{"(positional) delete refspec of the primary branch", "git push origin :main"},
		{"(positional) glob refspec", "git push origin refs/heads/*:refs/heads/*"},
		{"(combination) --delete of a non-primary branch", "git push origin --delete feature"},
		{"(combination) --delete of the primary branch", "git push origin --delete main"},
		{"(combination) --delete of a tag", "git push origin --delete refs/tags/v1"},
		{"(combination) --all with --force-with-lease", "git push --all --force-with-lease origin"},
		{"(combination) --tags with --force", "git push --tags --force origin"},
		{"(combination) --all with --prune", "git push --all --prune origin"},
	} {
		eval(p.fact, p.probe)
	}
	return rep, nil
}

// Markdown renders the report deterministically.
func (r *P15Report) Markdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# P15 approve-reachability report: %s\n\n", r.Subject)
	fmt.Fprintf(&b, "Generated by `%s` (internal/effectpolicy/p15report.go) and pinned by\n", r.Generator)
	fmt.Fprintf(&b, "`%s`; regenerate with `go test ./internal/effectpolicy -run %s -update`.\n", r.Test, r.Test)
	b.WriteString("Each row is one spec fact probed through the real engine (default registry and policies).\n\n")
	b.WriteString("## Facts that feed an approve path\n\n")
	for _, e := range r.PermittedEntries() {
		fmt.Fprintf(&b, "- %s: `%s`\n", e.Fact, e.Probe)
	}
	b.WriteString("\n## Every fact examined\n\n")
	for _, e := range r.Entries {
		fmt.Fprintf(&b, "- %s: `%s` => **%s**\n", e.Fact, e.Probe, e.Decision)
	}
	out := b.String()
	if r.root != "" {
		out = strings.ReplaceAll(out, r.root, "<ROOT>")
	}
	return out
}
