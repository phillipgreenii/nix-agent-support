package effectpolicy

import (
	"fmt"
	"os"
	"path/filepath"
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
	// Baseline, when HasBaseline is set, is the engine's verdict for the SAME
	// Probe from the comparison working directory (a report that reviews a
	// working-directory trust widening probes each fact twice: inside the
	// widened location, and outside it).
	Baseline    evalcontract.Decision
	HasBaseline bool
}

// Permitted reports whether the fact feeds an approve path (P15's subject).
func (e P15Entry) Permitted() bool { return e.Decision == evalcontract.Approve }

// Widened reports whether the fact approves inside the widened location but
// not from the comparison directory: an approval the change itself added.
func (e P15Entry) Widened() bool {
	return e.HasBaseline && e.Decision == evalcontract.Approve && e.Baseline != evalcontract.Approve
}

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
	// Preamble is optional prose rendered under the header (the scope, the
	// comparison, and what the reviewer is asked to check).
	Preamble string
	// root, when set, is the probe root; Markdown renders it as <ROOT> so the
	// pinned report does not leak a fixture directory.
	root string
	// compare marks a two-directory report (see P15Entry.Baseline).
	compare bool
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

// WidenedEntries returns the entries a working-directory widening added.
func (r *P15Report) WidenedEntries() []P15Entry {
	var out []P15Entry
	for _, e := range r.Entries {
		if e.Widened() {
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

// ZRWriteFormReports is the ordered list of report keys GenerateZRWriteFormsP15
// returns; each key is also the basename of the pinned
// testdata/p15/<key>.md golden.
var ZRWriteFormReports = []string{"gh-pr-edit", "gh-stack-link", "gh-stack-unstack", "pjira-writes", "rc-publish"}

// GenerateZRWriteFormsP15 builds the P15 reports for the class D ZR plugin
// write forms pg2-19hdz landed once this tooling existed (gate 5 of the
// operator rulings on pg2-maars, 2026-10-05): `gh pr edit` (the new "pr-edit"
// remote operation), `gh stack unstack` (the new "stack-unstack" operation),
// `gh stack link` without --open (a plain atomic push, draft PR creations,
// pr-edit base fix-ups and the new "stack-link" operation),
// `pjira comment|create|transition` (tracker-write widened to the "jira"
// resource) and `rc-publish` (push-lease on origin, draft PR creation and the
// PR edit, in one script). Every flag of each schema is probed, plus the
// positional and combination shapes that decide whether a flag the schema
// does NOT model (the ones that would widen what is approved) can slip into an
// approve path. Verdicts come from Evaluate, never from reading a spec value
// (P14). root is a project root the probes run in.
func GenerateZRWriteFormsP15(root string) (map[string]*P15Report, error) {
	reg := cmddesc.DefaultRegistry()
	gh, ok := reg.Lookup("gh")
	if !ok {
		return nil, fmt.Errorf("p15: no gh schema registered")
	}
	edit, ok := gh.Subcommands["pr"].Subcommands["edit"]
	if !ok {
		return nil, fmt.Errorf("p15: no gh pr edit schema registered")
	}
	unstack, ok := gh.Subcommands["stack"].Subcommands["unstack"]
	if !ok {
		return nil, fmt.Errorf("p15: no gh stack unstack schema registered")
	}
	link, ok := gh.Subcommands["stack"].Subcommands["link"]
	if !ok {
		return nil, fmt.Errorf("p15: no gh stack link schema registered")
	}
	pjira, ok := reg.Lookup("pjira")
	if !ok {
		return nil, fmt.Errorf("p15: no pjira schema registered")
	}
	publish, ok := reg.Lookup("rc-publish")
	if !ok {
		return nil, fmt.Errorf("p15: no rc-publish schema registered")
	}
	newReport := func(subject string) *P15Report {
		return &P15Report{root: root, Subject: subject, Generator: "GenerateZRWriteFormsP15", Test: "TestP15ReportZRWriteForms"}
	}
	probes := func(r *P15Report, ps []struct{ fact, probe string }) {
		for _, p := range ps {
			r.probe(root, reg, p.fact, p.probe)
		}
	}
	out := map[string]*P15Report{}

	r := newReport("gh pr edit (the pr-edit remote operation)")
	r.probeFlags(root, reg, edit, "gh pr edit", " 12")
	probes(r, []struct{ fact, probe string }{
		{"(positional) PR number, --add-assignee @me (the rc-publish form)", "gh pr edit 12 --add-assignee @me"},
		{"(positional) no number (the current branch's PR)", "gh pr edit --add-assignee @me"},
		{"(positional) PR URL", "gh pr edit https://github.com/o/r/pull/12 --add-assignee @me"},
		{"(positional) PR branch name", "gh pr edit feature/x --add-assignee @me"},
		{"(positional) runtime-expanded PR number", `gh pr edit "$N" --add-assignee @me`},
		{"(positional) PR number after end-of-options", "gh pr edit --add-assignee @me -- 12"},
		{"(positional) no flag at all", "gh pr edit 12"},
		{"(combination) title and body together (the gh-stack skill form)", `gh pr edit 12 --title "T" --body "B"`},
		{"(combination) short -t and -b spellings", `gh pr edit 12 -t "T" -b "B"`},
		{"(combination) joined --add-assignee=@me", "gh pr edit 12 --add-assignee=@me"},
		{"(combination) body text from a command substitution of a project file", `gh pr edit 12 --body "$(cat README.md)"`},
		{"(combination) body text from a command substitution of a secret file", `gh pr edit 12 --body "$(cat ~/.ssh/id_rsa)"`},
		{"(unmodeled) --base retargets the PR", "gh pr edit 12 --base main"},
		{"(unmodeled) -B retargets the PR", "gh pr edit 12 -B main"},
		{"(unmodeled) --body-file reads a file as the PR text", "gh pr edit 12 --body-file body.md"},
		{"(unmodeled) --body-file of a secret file", "gh pr edit 12 --body-file ~/.ssh/id_rsa"},
		{"(unmodeled) --body-file from standard input", "gh pr edit 12 --body-file -"},
		{"(unmodeled) -F reads a file as the PR text", "gh pr edit 12 -F body.md"},
		{"(unmodeled) --attach uploads a local file to GitHub", "gh pr edit 12 --attach ./shot.png"},
		{"(unmodeled) --add-reviewer requests a review", "gh pr edit 12 --add-reviewer someone"},
		{"(unmodeled) --add-label", "gh pr edit 12 --add-label bug"},
		{"(unmodeled) --add-project", "gh pr edit 12 --add-project Roadmap"},
		{"(unmodeled) --milestone", `gh pr edit 12 --milestone "Version 1"`},
		{"(unmodeled) --remove-assignee", "gh pr edit 12 --remove-assignee someone"},
		{"(unmodeled) --repo edits a PR of another repository", "gh pr edit 12 --repo other/repo --add-assignee @me"},
		{"(unmodeled) -R edits a PR of another repository", "gh pr edit -R other/repo 12 --add-assignee @me"},
		{"(unmodeled) --draft is not a gh pr edit flag", "gh pr edit 12 --draft"},
	})
	out["gh-pr-edit"] = r

	r = newReport("gh stack unstack (the stack-unstack remote operation)")
	r.probeFlags(root, reg, unstack, "gh stack unstack", "")
	probes(r, []struct{ fact, probe string }{
		{"(positional) no stack number (the active stack)", "gh stack unstack"},
		{"(positional) stack number", "gh stack unstack 7"},
		{"(positional) two stack numbers", "gh stack unstack 7 8"},
		{"(positional) runtime-expanded stack number", `gh stack unstack "$N"`},
		{"(combination) --local with a stack number", "gh stack unstack --local 7"},
		{"(combination) --local=false (a value on the no-argument flag)", "gh stack unstack --local=false"},
		{"(unmodeled) the delete alias", "gh stack delete"},
		{"(unmodeled) --open", "gh stack unstack --open"},
		{"(unmodeled) --remote", "gh stack unstack --remote origin"},
		{"(unmodeled) merge is a ruling collision", "gh stack merge --yes"},
	})
	out["gh-stack-unstack"] = r

	r = newReport("gh stack link without --open (plain atomic push, draft PR creations, pr-edit base fix-ups and the stack-link remote operation)")
	r.probeFlags(root, reg, link, "gh stack link", " branch-a branch-b")
	probes(r, []struct{ fact, probe string }{
		{"(positional) two branches (the skill form)", "gh stack link branch-a branch-b"},
		{"(positional) three branches", "gh stack link branch-a branch-b branch-c"},
		{"(positional) a stack number and one new branch", "gh stack link 7 branch-c"},
		{"(positional) PR numbers", "gh stack link 10 20 30"},
		{"(positional) PR URLs", "gh stack link https://github.com/o/r/pull/41 https://github.com/o/r/pull/42"},
		{"(positional) PR numbers and branches mixed", "gh stack link 42 43 feature-auth feature-ui"},
		{"(positional) the primary branch as an operand", "gh stack link main branch-a"},
		{"(positional) one operand (the CLI requires two)", "gh stack link branch-a"},
		{"(positional) no operand", "gh stack link"},
		{"(positional) runtime-expanded first operand", `gh stack link "$A" branch-b`},
		{"(positional) runtime-expanded second operand", `gh stack link branch-a "$B"`},
		{"(positional) operands after end-of-options", "gh stack link -- branch-a branch-b"},
		{"(combination) --base for the bottom of the stack", "gh stack link --base develop branch-a branch-b"},
		{"(combination) --remote naming a configured remote", "gh stack link --remote origin branch-a branch-b"},
		{"(combination) --remote naming a URL", "gh stack link --remote https://example.com/x.git branch-a branch-b"},
		{"(combination) --remote naming a path", "gh stack link --remote ../other branch-a branch-b"},
		{"(combination) --base and --remote together", "gh stack link --base develop --remote origin branch-a branch-b"},
		{"(unmodeled) --open marks new and existing PRs ready for review (class C)", "gh stack link --open branch-a branch-b"},
		{"(unmodeled) --base with --open (class C)", "gh stack link --base develop --open branch-a branch-b branch-c"},
		{"(unmodeled) --open after the operands (class C)", "gh stack link branch-a branch-b --open"},
		{"(unmodeled) --open=true (class C)", "gh stack link --open=true branch-a branch-b"},
		{"(unmodeled) --force", "gh stack link --force branch-a branch-b"},
		{"(unmodeled) --force-with-lease", "gh stack link --force-with-lease branch-a branch-b"},
		{"(unmodeled) --draft is not a gh stack link flag", "gh stack link --draft branch-a branch-b"},
		{"(unmodeled) submit --auto --open stays a class C collision", "gh stack submit --auto --open"},
	})
	out["gh-stack-link"] = r

	r = newReport("pjira comment|create|transition (tracker-write on the jira resource)")
	for _, verb := range []string{"comment", "create", "transition"} {
		sub, ok := pjira.Subcommands[verb]
		if !ok {
			return nil, fmt.Errorf("p15: no pjira %s schema registered", verb)
		}
		suffix := " ABC-123 body"
		if verb == "create" {
			suffix = ""
		}
		r.probeFlags(root, reg, sub, "pjira "+verb, suffix)
	}
	probes(r, []struct{ fact, probe string }{
		{"(positional) comment KEY BODY", "pjira comment ABC-123 'progress update'"},
		{"(positional) comment, one operand", "pjira comment ABC-123"},
		{"(positional) comment, three operands (pjira itself rejects the extra operand)", "pjira comment ABC-123 hi extra"},
		{"(positional) comment after end-of-options", "pjira comment -- ABC-123 hi"},
		{"(positional) comment, runtime-expanded body", `pjira comment ABC-123 "$BODY"`},
		{"(positional) comment, runtime-expanded key", `pjira comment "$KEY" hi`},
		{"(positional) transition KEY STATE", "pjira transition ABC-123 Done"},
		{"(positional) transition, one operand", "pjira transition ABC-123"},
		{"(positional) transition, runtime-expanded state", `pjira transition ABC-123 "$S"`},
		{"(combination) create with every flag", "pjira create --project ABC --type Task --summary s --description d"},
		{"(combination) create, no flags", "pjira create"},
		{"(combination) comment body from a command substitution of a project file", `pjira comment ABC-123 "$(cat README.md)"`},
		{"(combination) comment body from a command substitution of a secret file", `pjira comment ABC-123 "$(cat ~/.ssh/id_rsa)"`},
		{"(combination) comment piped from a secret file", "cat ~/.ssh/id_rsa | pjira comment ABC-123 -"},
		{"(unmodeled) --config naming a credentials file (a write never passes it)", "pjira comment --config ~/.config/pjira/config.toml ABC-123 hi"},
		{"(unmodeled) --config naming a project file", "pjira create --config pjira.toml --project ABC --type Task --summary s"},
		{"(unmodeled) --config naming a secret path", "pjira comment --config ~/.ssh/id_rsa ABC-123 hi"},
		{"(read, unchanged) issue", "pjira issue ABC-123"},
		{"(read, unchanged) issue with --config (reads keep it)", "pjira issue --config pjira.toml ABC-123"},
		{"(read, unchanged) search", "pjira search --jql 'assignee = currentUser()'"},
		{"(unmodeled) delete", "pjira delete ABC-123"},
		{"(unmodeled) assign", "pjira assign ABC-123 me"},
		{"(unmodeled) edit", "pjira edit ABC-123"},
	})
	out["pjira-writes"] = r

	r = newReport("rc-publish (push-lease origin, pr-create-draft and pr-edit on github)")
	r.probeFlags(root, reg, publish, "rc-publish", " . feature/x title body.md")
	probes(r, []struct{ fact, probe string }{
		{"(positional) the instructed shape", "rc-publish . feature/foo 'a title' body.md"},
		{"(positional) absolute worktree and body file under the project root", "rc-publish " + root + "/wt feature/foo 'a title' " + root + "/body.md"},
		{"(positional) body file outside the project root", "rc-publish . feature/foo 'a title' /tmp/rc-body.md"},
		{"(positional) body file is a secret path", "rc-publish . feature/foo 'a title' ~/.ssh/id_rsa"},
		{"(positional) worktree outside the project root", "rc-publish /tmp/some-wt feature/foo 'a title' body.md"},
		{"(positional) runtime-expanded worktree", `rc-publish "$WT" feature/foo 'a title' body.md`},
		{"(positional) runtime-expanded branch", `rc-publish . "$B" 'a title' body.md`},
		{"(positional) runtime-expanded body file", `rc-publish . feature/foo 'a title' "$BODY"`},
		{"(positional) three operands", "rc-publish . feature/foo 'a title'"},
		{"(positional) five operands (the script ignores the extra operand)", "rc-publish . feature/foo 'a title' body.md extra"},
		{"(positional) primary branch name", "rc-publish . main 'a title' body.md"},
		{"(combination) an unmodeled flag", "rc-publish --force . feature/foo 'a title' body.md"},
		{"(combination) with a trailing unmodeled flag", "rc-publish . feature/foo 'a title' body.md --draft"},
	})
	out["rc-publish"] = r
	return out, nil
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
	if r.Preamble != "" {
		b.WriteString(r.Preamble)
		b.WriteString("\n\n")
	}
	if r.compare {
		b.WriteString("## Facts newly approved by the widening\n\n")
		for _, e := range r.WidenedEntries() {
			fmt.Fprintf(&b, "- %s: `%s`\n", e.Fact, e.Probe)
		}
		b.WriteString("\n## Every fact examined\n\n")
		for _, e := range r.Entries {
			fmt.Fprintf(&b, "- %s: `%s` => **%s** inside, **%s** outside\n", e.Fact, e.Probe, e.Decision, e.Baseline)
		}
	} else {
		b.WriteString("## Facts that feed an approve path\n\n")
		for _, e := range r.PermittedEntries() {
			fmt.Fprintf(&b, "- %s: `%s`\n", e.Fact, e.Probe)
		}
		b.WriteString("\n## Every fact examined\n\n")
		for _, e := range r.Entries {
			fmt.Fprintf(&b, "- %s: `%s` => **%s**\n", e.Fact, e.Probe, e.Decision)
		}
	}
	out := b.String()
	if r.root != "" {
		out = strings.ReplaceAll(out, r.root, "<ROOT>")
	}
	return out
}

// markerExecProbes is one representative, valid invocation per command path
// that declares an Exec fact (ExecProducerPaths). Every path MUST appear here
// (TestP15ReportPnWorkspaceMarker fails otherwise), so a new Exec producer
// cannot slip into the marker-trusted set without appearing in the report.
var markerExecProbes = []struct{ path, probe string }{
	{"bats", "bats tests/"},
	{"darwin-rebuild build", "darwin-rebuild build --flake .#host"},
	{"go build", "go build ./..."},
	{"go env", "go env GOPATH"},
	{"go fmt", "go fmt ./..."},
	{"go generate", "go generate ./..."},
	{"go list", "go list ./..."},
	{"go mod download", "go mod download"},
	{"go mod edit", "go mod edit -fmt"},
	{"go mod graph", "go mod graph"},
	{"go mod tidy", "go mod tidy"},
	{"go mod vendor", "go mod vendor"},
	{"go mod verify", "go mod verify"},
	{"go mod why", "go mod why example.com/m"},
	{"go test", "go test ./..."},
	{"go version", "go version"},
	{"go vet", "go vet ./..."},
	{"integrate-branch-support", "integrate-branch-support --prek-branch-diff"},
	{"nix build", "nix build .#x"},
	{"nix eval", "nix eval .#x"},
	{"nix flake check", "nix flake check"},
	{"nix fmt", "nix fmt"},
	{"pg-go-mutate", "pg-go-mutate ./internal/collect --workers 1"},
	{"pg-hooks fix", "pg-hooks fix"},
	{"pg-hooks run", "pg-hooks run pre-commit a.go"},
	{"pn workspace apply", "pn workspace apply"},
	{"pn workspace build", "pn workspace build"},
	{"pn workspace clone", "pn workspace clone"},
	{"pn workspace doctor", "pn workspace doctor"},
	{"pn workspace flake-check", "pn workspace flake-check"},
	{"pn workspace format", "pn workspace format"},
	{"pn workspace lock", "pn workspace lock"},
	{"pn workspace pre-commit-check", "pn workspace pre-commit-check"},
	{"pn workspace push", "pn workspace push"},
	{"pn workspace rebase", "pn workspace rebase main"},
	{"pn workspace update", "pn workspace update"},
	{"pnwf cleanup", "pnwf cleanup my-branch"},
	{"pnwf sync-fetch", "pnwf sync-fetch --set"},
	{"pnwf update-relock", "pnwf update-relock --set"},
	{"prek run", "prek run --files a.go"},
}

// markerControlProbes are forms with NO Exec fact (or whose Exec fact is not
// judged by the marker check): the widening must not change any of their
// verdicts, so every one of them must agree inside and outside the marker.
// Path-classification probes (rm, writes) are deliberately absent: a
// pn-workspace.toml also makes pnKind the path kind of its tree (it guards the
// workforests directory against rm), which is pre-existing, unrelated to
// TrustedCheckoutExec, and would confound the inside/outside comparison.
var markerControlProbes = []string{
	"pn workspace status",
	"pn workspace info --json",
	"pn workspace workforest list",
	"pnwf resolve --set",
	"pnwf land-plan my-branch",
	"pn workspace init",
	"pn workspace upgrade",
	"pn workspace allow",
	"pn workspace doctor --fix",
	"pn workspace workforest remove feat",
	"pn workspace push --no-verify",
	"pnwf cleanup my-branch --force-dirty-worktree-removal",
	"pnwf cleanup my-branch --force-unlanded-branch-removal",
	"go run ./cmd/tool",
	"go get example.com/m",
	"go clean -modcache",
	"nix run .#app",
	"nix develop",
	"just build",
	"npm run build",
	"git push origin feature",
	"git status",
	"cat README.md",
	"curl https://example.invalid/x",
	"sed -i s/a/b/ README.md",
	"bash -c 'cat README.md'",
	"kubectl get pods",
	"ssh host uptime",
}

// ExecProducerPaths lists every command path in reg (space-joined, e.g. "go
// mod tidy") that declares an Exec implicit effect: exactly the facts
// TrustedCheckoutExec judges, hence exactly what a trusted-exec-location
// widening can newly approve. A verb-dispatch family (just/npm/devbox/nix
// run) is NOT listed: its EffectExec carries a Family and is judged by
// judgeBuildToolVerb, which the marker check never reaches.
func ExecProducerPaths(reg cmddesc.Registry) []string {
	var out []string
	var walk func(s cmddesc.CommandSchema, path string)
	walk = func(s cmddesc.CommandSchema, path string) {
		for _, ie := range s.ImplicitEffects {
			if ie.Role.Kind == cmddesc.KindExec {
				out = append(out, path)
				break
			}
		}
		for name, sub := range s.Subcommands {
			walk(sub, path+" "+name)
		}
	}
	for _, n := range reg.Names() {
		s, _ := reg.Lookup(n)
		walk(s, n)
	}
	sort.Strings(out)
	return out
}

// GeneratePnWorkspaceMarkerP15 builds the P15 report for pg2-qy5u8: the
// widening of effectpolicy.TrustedCheckoutExec so a directory at or under a
// pn-workspace.toml is a trusted exec location. Every fact is probed twice
// through the real engine: from a workforest set directory of a pn workspace
// that is NOT a git/go checkout ("inside"), and from a directory with no
// marker anywhere above it ("outside", which behaves exactly as every such
// directory did before the widening). The report therefore lists exactly
// what the marker newly approves. base is an existing empty directory the
// generator builds its two fixture trees in.
func GeneratePnWorkspaceMarkerP15(base string) (*P15Report, error) {
	root := filepath.Join(base, "ws")
	set := filepath.Join(root, ".workforests", "feat")
	bare := filepath.Join(base, "bare")
	for _, d := range []string{set, bare} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, err
		}
	}
	if err := os.WriteFile(filepath.Join(root, "pn-workspace.toml"), nil, 0o644); err != nil {
		return nil, err
	}
	reg := cmddesc.DefaultRegistry()
	rep := &P15Report{
		Subject:   "pn-workspace.toml marker as a trusted exec location (TrustedCheckoutExec)",
		Generator: "GeneratePnWorkspaceMarkerP15",
		Test:      "TestP15ReportPnWorkspaceMarker",
		root:      base,
		compare:   true,
		Preamble: "Change under review: `TrustedCheckoutExec` is Permitted when the working directory is inside a git or go checkout, " +
			"and now ALSO when it is at or under a `pn-workspace.toml` (operator ruling, 2026-10-12). \"inside\" is a workforest set directory of a pn workspace " +
			"that carries no git or go marker; \"outside\" is a directory with no marker above it, which is how every such directory behaved before the change. " +
			"A fact is newly approved when it is approve inside and not approve outside.\n\n" +
			"What to check: the newly approved facts are ONLY Exec producers (one section per command path, listed by `ExecProducerPaths`), " +
			"the same class `go test` already gets inside a git/go checkout; every control fact (no Exec fact, or an Exec fact the marker never reaches) " +
			"has the same verdict inside and outside. `pn`/`pnwf` workspace-code verbs are Exec producers too: before the change they approved from any directory " +
			"(inert-literal model), so for them the net effect against the previous behavior is a narrowing outside a marker, and no change inside.",
	}
	probe := func(fact, command string) {
		in := Evaluate(evalcontract.Request{Command: command, CWD: set, ProjectRoot: set}, reg, DefaultPolicies(), DefaultGraphPolicies())
		out := Evaluate(evalcontract.Request{Command: command, CWD: bare, ProjectRoot: bare}, reg, DefaultPolicies(), DefaultGraphPolicies())
		rep.Entries = append(rep.Entries, P15Entry{Fact: fact, Probe: command, Decision: in.Decision, Baseline: out.Decision, HasBaseline: true})
	}
	for _, p := range markerExecProbes {
		probe("exec producer "+p.path, p.probe)
	}
	// Every flag of the pn/pnwf Exec verbs (the subject of the bead).
	flagSweep := func(prefix string, schema cmddesc.CommandSchema, suffix string) {
		flags := make([]string, 0, len(schema.Flags))
		for f := range schema.Flags {
			flags = append(flags, f)
		}
		sort.Strings(flags)
		for _, f := range flags {
			command := prefix + " " + f
			if schema.Flags[f].Arity == cmddesc.ArityOne {
				command += " x"
			}
			probe("flag "+f+" of "+prefix, command+suffix)
		}
	}
	if pn, ok := reg.Lookup("pn"); ok {
		verbs := pn.Subcommands["workspace"].Subcommands
		names := make([]string, 0, len(verbs))
		for v := range verbs {
			names = append(names, v)
		}
		sort.Strings(names)
		for _, v := range names {
			if len(verbs[v].ImplicitEffects) > 0 {
				flagSweep("pn workspace "+v, verbs[v], "")
			}
		}
	} else {
		return nil, fmt.Errorf("p15: no pn schema registered")
	}
	if pnwf, ok := reg.Lookup("pnwf"); ok {
		names := make([]string, 0, len(pnwf.Subcommands))
		for v := range pnwf.Subcommands {
			names = append(names, v)
		}
		sort.Strings(names)
		for _, v := range names {
			if len(pnwf.Subcommands[v].ImplicitEffects) > 0 {
				suffix := ""
				if v == "cleanup" {
					suffix = " my-branch"
				}
				flagSweep("pnwf "+v, pnwf.Subcommands[v], suffix)
			}
		}
	} else {
		return nil, fmt.Errorf("p15: no pnwf schema registered")
	}
	for _, c := range markerControlProbes {
		probe("(control) no marker-reachable Exec fact", c)
	}
	return rep, nil
}

// requestCWDExec is TrustedCheckoutExec exactly as it behaved BEFORE bead
// pg2-xj975: it ignores the effective (cd-tracked) directories the graph
// builder stamps on an Exec effect and judges the request's cwd instead. It
// exists only so the effective-cwd P15 report can compute the "before" verdict
// through the same engine, rather than from a hand-written oracle.
type requestCWDExec struct{ TrustedCheckoutExec }

// Judge implements Policy.
func (p requestCWDExec) Judge(e cmddesc.Effect, ctx PolicyContext) (Finding, bool) {
	e.CWDs, e.CWDUnknown = "", false
	return p.TrustedCheckoutExec.Judge(e, ctx)
}

// requestCWDPolicies is DefaultPolicies with TrustedCheckoutExec replaced by
// its pre-pg2-xj975 behavior.
func requestCWDPolicies() []Policy {
	ps := DefaultPolicies()
	for i, p := range ps {
		if _, ok := p.(TrustedCheckoutExec); ok {
			ps[i] = requestCWDExec{}
		}
	}
	return ps
}

// P15CWDShape is one compound shape of the effective-cwd report. Command
// builds the probe from the producer command and the cd target.
type P15CWDShape struct {
	Name    string
	Command func(producer, target string) string
}

// P15CWDShapes are the compound shapes the effective-cwd report covers; each
// is one row of effectgraph's "EXEC WORKING DIRECTORY" table. Order is the
// report order.
var P15CWDShapes = []P15CWDShape{
	{"`cd D && P`", func(p, d string) string { return "cd " + d + " && " + p }},
	{"`cd D && P | tail`", func(p, d string) string { return "cd " + d + " && " + p + " 2>&1 | tail -5" }},
	{"`cd D && (P)`", func(p, d string) string { return "cd " + d + " && (" + p + ")" }},
	{"`(cd D && P)`", func(p, d string) string { return "(cd " + d + " && " + p + ")" }},
	{"`cd D && echo $(P)`", func(p, d string) string { return "cd " + d + " && echo $(" + p + ")" }},
	{"`for ...; do cd D && P; done`", func(p, d string) string { return "for i in 1 2; do cd " + d + " && " + p + "; done" }},
	{"`cd D; P`", func(p, d string) string { return "cd " + d + "; " + p }},
	{"`cd D || P`", func(p, d string) string { return "cd " + d + " || " + p }},
	{"`(cd D); P`", func(p, d string) string { return "(cd " + d + "); " + p }},
	{"`cd D | cat; P`", func(p, d string) string { return "cd " + d + " | cat; " + p }},
	{"`P && cd D`", func(p, d string) string { return p + " && cd " + d }},
}

// P15CWDCell is one (shape, request cwd, cd target) cell: every Exec producer
// probed once under the new and once under the previous behavior.
type P15CWDCell struct {
	Shape, Request, Target string
	// Producers is the number of Exec producers probed in the cell.
	Producers int
	// NewApprove and OldApprove count the producers approved after / before.
	NewApprove, OldApprove int
	// NewlyApproved counts producers approved now and not before; NewlyWithheld
	// those approved before and not now.
	NewlyApproved, NewlyWithheld int
	// Uniform is true when every producer got the same (new, old) pair.
	Uniform bool
}

// P15CWDControl is one non-Exec probe evaluated in a cell of the first shape.
type P15CWDControl struct {
	Probe, Request, Target string
	New, Old               evalcontract.Decision
}

// P15CWDReport is the P15 report for bead pg2-xj975.
type P15CWDReport struct {
	Producers []string
	Cells     []P15CWDCell
	Controls  []P15CWDControl
	Requests  []string // request cwd labels, report order
	Targets   []string // cd target labels, report order
}

// cwdFixtureDirs builds the three directory kinds under base and returns them
// keyed by the label used in the report.
func cwdFixtureDirs(base string) (map[string]string, error) {
	dirs := map[string]string{
		"marker dir":   filepath.Join(base, "ws", ".workforests", "feat"),
		"git checkout": filepath.Join(base, "repo"),
		"no marker":    filepath.Join(base, "bare"),
	}
	for _, d := range []string{dirs["marker dir"], filepath.Join(dirs["git checkout"], ".git"), filepath.Join(dirs["git checkout"], "sub"), dirs["no marker"]} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, err
		}
	}
	if err := os.WriteFile(filepath.Join(base, "ws", "pn-workspace.toml"), nil, 0o644); err != nil {
		return nil, err
	}
	return dirs, nil
}

// GenerateEffectiveCWDP15 builds the P15 report for bead pg2-xj975: the
// Exec-fact working directory changed from the request cwd to the cd-tracked
// one, which both GRANTS approvals (a cd into a trusted directory from an
// untrusted request cwd) and WITHHOLDS them (a cd out of a trusted request
// cwd). Every Exec producer (ExecProducerPaths, via markerExecProbes) is
// probed under every compound shape for every (request cwd, cd target) pair
// through the real engine, once with the new policy and once with the
// previous one (requestCWDPolicies). base is an existing empty directory.
func GenerateEffectiveCWDP15(base string) (*P15CWDReport, error) {
	dirs, err := cwdFixtureDirs(base)
	if err != nil {
		return nil, err
	}
	reg := cmddesc.DefaultRegistry()
	rep := &P15CWDReport{
		Requests: []string{"marker dir", "git checkout", "no marker"},
		// "no cd" is the control: the request cwd decides, unchanged.
		Targets: []string{"marker dir", "git checkout", "no marker", "$D (runtime value)", "- (previous dir)", "sub (relative, in a checkout)", ".. (relative, leaves a checkout)"},
	}
	for _, p := range markerExecProbes {
		rep.Producers = append(rep.Producers, p.path)
	}
	target := func(label string) string {
		switch label {
		case "$D (runtime value)":
			return `"$D"`
		case "- (previous dir)":
			return "-"
		case "sub (relative, in a checkout)":
			return "sub"
		case ".. (relative, leaves a checkout)":
			return ".."
		}
		return dirs[label]
	}
	eval := func(policies []Policy, cwd, command string) evalcontract.Decision {
		return Evaluate(evalcontract.Request{Command: command, CWD: cwd, ProjectRoot: cwd}, reg, policies, DefaultGraphPolicies()).Decision
	}
	oldPolicies, newPolicies := requestCWDPolicies(), DefaultPolicies()
	for _, shape := range P15CWDShapes {
		for _, req := range rep.Requests {
			for _, tgt := range rep.Targets {
				// The runtime and relative targets are probed under the first
				// shape only; every shape is probed with the three directories.
				if _, isDir := dirs[tgt]; !isDir && shape.Name != P15CWDShapes[0].Name {
					continue
				}
				cell := P15CWDCell{Shape: shape.Name, Request: req, Target: tgt, Uniform: true}
				var first [2]evalcontract.Decision
				for i, p := range markerExecProbes {
					cmd := shape.Command(p.probe, target(tgt))
					n, o := eval(newPolicies, dirs[req], cmd), eval(oldPolicies, dirs[req], cmd)
					if i == 0 {
						first = [2]evalcontract.Decision{n, o}
					} else if first != [2]evalcontract.Decision{n, o} {
						cell.Uniform = false
					}
					cell.Producers++
					if n == evalcontract.Approve {
						cell.NewApprove++
					}
					if o == evalcontract.Approve {
						cell.OldApprove++
					}
					if n == evalcontract.Approve && o != evalcontract.Approve {
						cell.NewlyApproved++
					}
					if o == evalcontract.Approve && n != evalcontract.Approve {
						cell.NewlyWithheld++
					}
				}
				rep.Cells = append(rep.Cells, cell)
			}
		}
	}
	for _, c := range markerControlProbes {
		for _, req := range rep.Requests {
			for _, tgt := range []string{"marker dir", "git checkout", "no marker"} {
				cmd := "cd " + dirs[tgt] + " && " + c
				rep.Controls = append(rep.Controls, P15CWDControl{Probe: c, Request: req, Target: tgt, New: eval(newPolicies, dirs[req], cmd), Old: eval(oldPolicies, dirs[req], cmd)})
			}
		}
	}
	return rep, nil
}

// Totals returns the number of cells and the producer facts newly approved and
// newly withheld across all of them.
func (r *P15CWDReport) Totals() (cells, approved, withheld int) {
	for _, c := range r.Cells {
		cells++
		approved += c.NewlyApproved
		withheld += c.NewlyWithheld
	}
	return
}

// Markdown renders the report deterministically.
func (r *P15CWDReport) Markdown() string {
	var b strings.Builder
	n := len(r.Producers)
	cells, approved, withheld := r.Totals()
	b.WriteString("# P15 approve-reachability report: Exec facts judged against the effective (cd-tracked) working directory (TrustedCheckoutExec)\n\n")
	b.WriteString("Generated by `GenerateEffectiveCWDP15` (internal/effectpolicy/p15report.go) and pinned by\n")
	b.WriteString("`TestP15ReportEffectiveCWD`; regenerate with `go test ./internal/effectpolicy -run TestP15ReportEffectiveCWD -update`.\n")
	b.WriteString("Every cell below evaluates real commands through the real engine (default registry and graph policies), once with the new\n")
	b.WriteString("`TrustedCheckoutExec` and once with the previous one (`requestCWDPolicies`, which judges the request cwd).\n\n")
	b.WriteString("Change under review: an Exec fact (a `go test`, `pn workspace build`, `nix build`, ... node) used to be judged against the REQUEST cwd; it is now judged against the directory\n")
	b.WriteString("its command runs in after the `cd`s before it, and EVERY directory it may run in must be trusted (a git checkout, a go module, or at/under a `pn-workspace.toml`).\n")
	b.WriteString("So an approval is GRANTED when a `cd` moves an untrusted request cwd into a trusted directory, and WITHHELD when a `cd` moves a trusted request cwd out of trusted ground.\n\n")
	fmt.Fprintf(&b, "Producers: %d Exec producers (`ExecProducerPaths`; one representative valid invocation each), listed under \"Producers probed\" below.\n", n)
	b.WriteString("Directories: `marker dir` is a workforest set under a `pn-workspace.toml` (no git/go marker), `git checkout` is a directory with `.git`, `no marker` has no marker above it.\n\n")
	b.WriteString("## Summary\n\n")
	fmt.Fprintf(&b, "- Cells evaluated: %d (compound shape x request cwd x cd target).\n", cells)
	fmt.Fprintf(&b, "- Exec facts newly APPROVED: %d = the sum over cells of (producers approved now and not before).\n", approved)
	fmt.Fprintf(&b, "- Exec facts newly WITHHELD: %d = the sum over cells of (producers approved before and not now).\n", withheld)
	var grantCells, withholdCells, nonUniform int
	for _, c := range r.Cells {
		if c.NewlyApproved > 0 {
			grantCells++
		}
		if c.NewlyWithheld > 0 {
			withholdCells++
		}
		if !c.Uniform {
			nonUniform++
		}
	}
	fmt.Fprintf(&b, "- Calculation: %d cells that newly approve x %d producers = %d; %d cells that newly withhold x %d producers = %d.\n", grantCells, n, approved, withholdCells, n, withheld)
	fmt.Fprintf(&b, "- Cells where the %d producers do not all agree: %d (every producer is the same exec class, so a mixed cell would be a finding).\n", n, nonUniform)
	var moved int
	for _, c := range r.Controls {
		if c.New != c.Old {
			moved++
		}
	}
	fmt.Fprintf(&b, "- Control facts (no Exec fact, or one the check never reaches) whose verdict moved: %d of %d probes.\n\n", moved, len(r.Controls))

	b.WriteString("## Per shape: producers newly approved (+) or withheld (-), per request cwd (rows) and cd target (columns)\n\n")
	fmt.Fprintf(&b, "Each cell reads `change (verdict now)`. `=` means no producer changed; `**+N**`/`**-N**` is how many of the %d producers are newly approved / newly withheld; ", n)
	b.WriteString("the verdict now is `approved` when every producer approves and `not approved` when none does. A runtime or relative target is probed under the first shape only.\n\n")
	for _, shape := range P15CWDShapes {
		fmt.Fprintf(&b, "### %s\n\n", shape.Name)
		var targets []string
		for _, t := range r.Targets {
			for _, c := range r.Cells {
				if c.Shape == shape.Name && c.Target == t {
					targets = append(targets, t)
					break
				}
			}
		}
		for _, req := range r.Requests {
			fmt.Fprintf(&b, "- request cwd **%s**\n", req)
			for _, t := range targets {
				for _, c := range r.Cells {
					if c.Shape != shape.Name || c.Request != req || c.Target != t {
						continue
					}
					verdict := "not approved"
					if c.NewApprove == c.Producers {
						verdict = "approved"
					} else if c.NewApprove > 0 {
						verdict = fmt.Sprintf("%d/%d approved", c.NewApprove, c.Producers)
					}
					delta := "="
					switch {
					case c.NewlyApproved > 0 && c.NewlyWithheld > 0:
						delta = fmt.Sprintf("+%d/-%d", c.NewlyApproved, c.NewlyWithheld)
					case c.NewlyApproved > 0:
						delta = fmt.Sprintf("**+%d**", c.NewlyApproved)
					case c.NewlyWithheld > 0:
						delta = fmt.Sprintf("**-%d**", c.NewlyWithheld)
					}
					fmt.Fprintf(&b, "  - cd to %s: %s (%s)\n", t, delta, verdict)
				}
			}
		}
		b.WriteString("\n")
	}

	b.WriteString("## Controls: facts with no reachable Exec fact\n\n")
	b.WriteString("Each probe is `cd <dir> && <probe>`; the verdict must be identical new and before in every request cwd and target. Moved controls:\n\n")
	any := false
	for _, c := range r.Controls {
		if c.New != c.Old {
			any = true
			fmt.Fprintf(&b, "- `%s` from %s after cd to %s: before **%s**, now **%s**\n", c.Probe, c.Request, c.Target, c.Old, c.New)
		}
	}
	if !any {
		b.WriteString("- none\n")
	}
	b.WriteString("\n## Producers probed\n\n")
	for _, p := range r.Producers {
		fmt.Fprintf(&b, "- %s\n", p)
	}
	return b.String()
}
