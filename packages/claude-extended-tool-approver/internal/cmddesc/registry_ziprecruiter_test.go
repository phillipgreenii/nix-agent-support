package cmddesc

import (
	"reflect"
	"testing"
)

// TestZiprecruiterSchemasAreFailClosedData is the data-only proof for
// pg2-cjfpy.4: every schema it registers is a plain value with a Provenance,
// no named interpreter, and the fail-closed unknown-flag policy -- at EVERY
// level of the subcommand tree -- so no flag or subcommand the plugins do not
// instruct can approve by default.
func TestZiprecruiterSchemasAreFailClosedData(t *testing.T) {
	reg := DefaultRegistry()
	for _, s := range ziprecruiterToolSchemas() {
		got, ok := reg.Lookup(s.Name)
		if !ok {
			t.Errorf("%s: not registered in DefaultRegistry", s.Name)
			continue
		}
		var walk func(path string, s CommandSchema)
		walk = func(path string, s CommandSchema) {
			if s.Provenance == "" {
				t.Errorf("%s: no provenance", path)
			}
			if s.Interpreter != "" {
				t.Errorf("%s: names interpreter %q; must be generic data", path, s.Interpreter)
			}
			if s.UnknownFlag != UnknownFlagInsufficient {
				t.Errorf("%s: unknown flags are not fail-closed", path)
			}
			for name, sub := range s.Subcommands {
				walk(path+" "+name, sub)
			}
		}
		walk(got.Name, got)
	}
}

// TestZiprecruiterUnlandedVerbsAreAbsent pins the verbs that are still NOT
// modeled: they MUST stay unmodeled (so they abstain) until the open question
// each one carries is answered. Adding one is a policy change, not a schema
// fact. pg2-maars landed the class D forms that reach APPROVE with the
// existing operations and pg2-19hdz landed the ones that needed a NEW
// effectpolicy judgment (see TestZiprecruiterClassDLandedForms); what is left:
//   - `gh stack link`: nothing documents the draft default of the PRs it
//     creates (its --open flag "marks new and existing PRs as ready for
//     review"), and it also edits existing PR bases;
//   - `gh stack merge`, `gh stack modify`: ruling collisions / not instructed;
//   - `rc-validate`: its caller-supplied gate command (after `--`) needs a
//     child-command interpreter that rebases the child's cwd onto <wt>, and a
//     realistic Gradle gate cannot reach approve even then (no `gradlew`
//     schema), so the form is dropped rather than half-built.
func TestZiprecruiterUnlandedVerbsAreAbsent(t *testing.T) {
	gh, _ := DefaultRegistry().Lookup("gh")
	stack, ok := gh.Subcommands["stack"]
	if !ok {
		t.Fatal("gh stack not registered")
	}
	for _, verb := range []string{"link", "merge", "modify", "delete"} {
		if _, present := stack.Subcommands[verb]; present {
			t.Errorf("gh stack %s must stay unmodeled (undocumented draft default / ruling collision / not instructed alias)", verb)
		}
	}
	if _, present := gh.Subcommands["extension"].Subcommands["install"]; present {
		t.Error("gh extension install must stay unmodeled (installs third-party code)")
	}
	for _, name := range []string{"rc-validate"} {
		if _, registered := DefaultRegistry().Lookup(name); registered {
			t.Errorf("%s needs a child-command interpreter and an approvable gate command, and must stay unmodeled", name)
		}
	}
	// The flags of the landed write verbs that would widen what they approve
	// stay unmodeled: gh pr edit's --base/--body-file/--attach/-R and the
	// reviewer, label, project and milestone flags.
	edit := gh.Subcommands["pr"].Subcommands["edit"]
	for _, flag := range []string{"--base", "-B", "--body-file", "-F", "--attach", "--repo", "-R", "--add-reviewer", "--add-label", "--add-project", "--milestone", "--remove-assignee"} {
		if _, present := edit.Flags[flag]; present {
			t.Errorf("gh pr edit %s must stay unmodeled (widens what the PR metadata write approves)", flag)
		}
	}
}

// TestZiprecruiterClassDLandedForms pins the shape of the pg2-maars class D
// schemas: each beads-writing script declares the tracker-write effect on the
// beads resource (so it can never silently become a pure read), and the
// `gh stack` write verbs keep their load-bearing pieces: --remote is a
// push-lease operand, `submit`'s --auto retargets mutate to the draft-first
// PR creation, `unstack`'s --local drops the remote mutation, and --open is
// not modeled anywhere.
func TestZiprecruiterClassDLandedForms(t *testing.T) {
	reg := DefaultRegistry()
	hasEffect := func(s CommandSchema, op, target string) bool {
		for _, ie := range s.ImplicitEffects {
			if ie.Role.Kind == KindRemote && ie.Role.Operation == op && ie.Target == target {
				return true
			}
		}
		return false
	}
	for _, name := range []string{"df-close-focus", "df-wire", "df-pull", "lat-wire", "rc-claim", "rc-park", "rc-fp"} {
		s, ok := reg.Lookup(name)
		if !ok {
			t.Errorf("%s: not registered", name)
			continue
		}
		if !hasEffect(s, "tracker-write", "beads") {
			t.Errorf("%s: no tracker-write effect on beads", name)
		}
	}
	deferred, _ := reg.Lookup("df-deferred")
	if !hasEffect(deferred.Subcommands["write"], "tracker-write", "beads") {
		t.Error("df-deferred write: no tracker-write effect on beads")
	}
	if hasEffect(deferred.Subcommands["read"], "tracker-write", "beads") || !hasEffect(deferred.Subcommands["read"], "read", "beads") {
		t.Error("df-deferred read must be a beads READ only")
	}
	gh, _ := reg.Lookup("gh")
	stack := gh.Subcommands["stack"]
	for _, verb := range []string{"push", "submit", "sync"} {
		sub, ok := stack.Subcommands[verb]
		if !ok {
			t.Errorf("gh stack %s not registered", verb)
			continue
		}
		if !hasEffect(sub, "push-lease", "<default-remote>") {
			t.Errorf("gh stack %s: no default-remote push-lease effect", verb)
		}
		if spec, ok := sub.Flags["--remote"]; !ok || spec.Operand.Kind != KindRemote || spec.Operand.Operation != "push-lease" {
			t.Errorf("gh stack %s: --remote must be a push-lease remote operand", verb)
		}
	}
	submit := stack.Subcommands["submit"]
	if !hasEffect(submit, "mutate", "github") || submit.Flags["--auto"].Transform.To != "pr-create-draft" {
		t.Error("gh stack submit: --auto must be what retargets the github mutate effect to pr-create-draft")
	}
	// pg2-19hdz: without --local the verb dissolves the stack grouping on
	// GitHub (the "stack-unstack" operation); --local is what retargets it to
	// a read.
	unstack := stack.Subcommands["unstack"]
	if !hasEffect(unstack, "stack-unstack", "github") || hasEffect(unstack, "mutate", "github") {
		t.Error("gh stack unstack: the github effect must be the stack-unstack operation, never a plain mutate")
	}
	if local := unstack.Flags["--local"].Transform; local.Kind != TransformRetargetRemote || local.From != "stack-unstack" || local.To != "read" {
		t.Errorf("gh stack unstack: --local must retarget stack-unstack to read, got %+v", local)
	}
	for _, verb := range []string{"submit", "sync", "push", "unstack"} {
		if _, ok := stack.Subcommands[verb].Flags["--open"]; ok {
			t.Errorf("gh stack %s: --open (marks PRs ready, pg2-psiqh Abstain) must stay unmodeled", verb)
		}
	}
}

// TestZiprecruiterPolicyJudgedWriteForms pins the shape of the write forms
// pg2-19hdz landed once the P15 tooling existed: each carries its NEW
// operation (or the widened tracker-write resource) as a load-bearing
// implicit effect, so none can silently become a pure read, and the read
// verbs of pjira stay reads.
func TestZiprecruiterPolicyJudgedWriteForms(t *testing.T) {
	reg := DefaultRegistry()
	effects := func(s CommandSchema) map[string]bool {
		out := map[string]bool{}
		for _, ie := range s.ImplicitEffects {
			if ie.Role.Kind == KindRemote {
				out[ie.Role.Operation+" "+ie.Target] = true
			}
		}
		return out
	}
	gh, _ := reg.Lookup("gh")
	if got := effects(gh.Subcommands["pr"].Subcommands["edit"]); len(got) != 1 || !got["pr-edit github"] {
		t.Errorf("gh pr edit: implicit remote effects = %v, want exactly {pr-edit github}", got)
	}
	pjira, _ := reg.Lookup("pjira")
	for _, verb := range []string{"comment", "create", "transition"} {
		if got := effects(pjira.Subcommands[verb]); len(got) != 1 || !got["tracker-write jira"] {
			t.Errorf("pjira %s: implicit remote effects = %v, want exactly {tracker-write jira}", verb, got)
		}
		// --config names the TOML with the Jira base URL and credential source:
		// the read verbs keep it, a write must not (no plugin passes it).
		if _, ok := pjira.Subcommands[verb].Flags["--config"]; ok {
			t.Errorf("pjira %s: --config must stay unmodeled on a write verb", verb)
		}
	}
	for _, verb := range []string{"issue", "search", "auth-status"} {
		if _, ok := pjira.Subcommands[verb].Flags["--config"]; !ok {
			t.Errorf("pjira %s: the read verbs keep --config", verb)
		}
	}
	for _, verb := range []string{"issue", "search", "auth-status"} {
		if got := effects(pjira.Subcommands[verb]); len(got) != 1 || !got["read jira"] {
			t.Errorf("pjira %s: implicit remote effects = %v, want exactly {read jira}", verb, got)
		}
	}
	publish, ok := reg.Lookup("rc-publish")
	if !ok {
		t.Fatal("rc-publish not registered")
	}
	want := map[string]bool{"push-lease origin": true, "read github": true, "pr-create-draft github": true, "pr-edit github": true}
	if got := effects(publish); !reflect.DeepEqual(got, want) {
		t.Errorf("rc-publish: implicit remote effects = %v, want %v", got, want)
	}
	if len(publish.Flags) != 2 {
		t.Errorf("rc-publish takes no flags besides -h/--help, got %v", publish.Flags)
	}
}

// TestGhSchemaIsSingleAndFoldsBothSiblings pins the pg2-cjfpy.2/.4 merge:
// NewRegistry keys by Name and the later entry silently wins, so two `gh`
// schemas would drop one sibling's forms without any error. Exactly one `gh`
// schema is built, and it carries .2's `pr` forms and .4's `auth`, `extension`
// and `stack` forms, each fail-closed at every level.
func TestGhSchemaIsSingleAndFoldsBothSiblings(t *testing.T) {
	var all []CommandSchema
	all = append(all, coreSchemas()...)
	all = append(all, pluginToolSchemas()...)
	all = append(all, repoBaseToolSchemas()...)
	all = append(all, ziprecruiterToolSchemas()...)
	n := 0
	for _, s := range all {
		if s.Name == "gh" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d `gh` schemas are built; want exactly 1 (a duplicate silently drops one sibling's subcommands)", n)
	}
	gh, ok := DefaultRegistry().Lookup("gh")
	if !ok {
		t.Fatal("gh not registered")
	}
	for path, sub := range map[string][]string{
		"pr":        {"view", "list", "status", "diff", "checks", "create", "ready", "edit"},
		"auth":      {"status"},
		"extension": {"list"},
		"stack":     {"view", "up", "down", "top", "bottom", "trunk", "init", "add", "rebase", "checkout", "push", "submit", "sync", "unstack"},
	} {
		parent, ok := gh.Subcommands[path]
		if !ok {
			t.Errorf("gh %s missing from the combined schema", path)
			continue
		}
		for _, verb := range sub {
			if _, ok := parent.Subcommands[verb]; !ok {
				t.Errorf("gh %s %s missing from the combined schema", path, verb)
			}
		}
	}
	var walk func(path string, s CommandSchema)
	walk = func(path string, s CommandSchema) {
		if s.Provenance == "" {
			t.Errorf("%s: no provenance", path)
		}
		if s.UnknownFlag != UnknownFlagInsufficient {
			t.Errorf("%s: unknown flags are not fail-closed", path)
		}
		for name, sub := range s.Subcommands {
			walk(path+" "+name, sub)
		}
	}
	walk("gh", gh)
}

// TestPgConnectorPrShowListsFreshAsNoValueBool pins bead pg2-e2h4q:
// pg-desk's gather calls `pg-connector pr show <id> --fresh` (pg2-cw6b3.2),
// and --fresh is a Cobra Bool flag (no value), so the schema MUST list it as
// an arity-none inert flag. An unlisted flag is fail-closed
// (UnknownFlagInsufficient) and would defer the call.
func TestPgConnectorPrShowListsFreshAsNoValueBool(t *testing.T) {
	pg, ok := DefaultRegistry().Lookup("pg-connector")
	if !ok {
		t.Fatal("pg-connector not registered")
	}
	show := pg.Subcommands["pr"].Subcommands["show"]
	spec, ok := show.Flags["--fresh"]
	if !ok {
		t.Fatal("pg-connector pr show: --fresh not listed")
	}
	if !reflect.DeepEqual(spec, inert) {
		t.Errorf("pg-connector pr show --fresh must be an inert no-value flag, got %+v", spec)
	}
	if show.UnknownFlag != UnknownFlagInsufficient {
		t.Error("pg-connector pr show must stay fail-closed for unlisted flags")
	}
}
