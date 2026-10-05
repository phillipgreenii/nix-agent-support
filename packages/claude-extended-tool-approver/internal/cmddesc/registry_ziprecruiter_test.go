package cmddesc

import "testing"

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

// TestZiprecruiterUnlandedVerbsAreAbsent pins the verbs that need a NEW
// effectpolicy judgment or collide with an explicit REJECT/ABSTAIN ruling:
// they MUST stay unmodeled (so they abstain) until a human rules on the
// parent epic's gate-5 question. Adding one is a policy change, not a
// schema fact.
func TestZiprecruiterUnlandedVerbsAreAbsent(t *testing.T) {
	gh, _ := DefaultRegistry().Lookup("gh")
	stack, ok := gh.Subcommands["stack"]
	if !ok {
		t.Fatal("gh stack not registered")
	}
	for _, verb := range []string{"push", "submit", "sync", "link", "unstack", "merge", "modify"} {
		if _, present := stack.Subcommands[verb]; present {
			t.Errorf("gh stack %s must stay unmodeled (policy change / ruling collision)", verb)
		}
	}
	if _, present := gh.Subcommands["extension"].Subcommands["install"]; present {
		t.Error("gh extension install must stay unmodeled (installs third-party code)")
	}
	pjira, _ := DefaultRegistry().Lookup("pjira")
	for _, verb := range []string{"comment", "create", "transition"} {
		if _, present := pjira.Subcommands[verb]; present {
			t.Errorf("pjira %s is a Jira write and must stay unmodeled", verb)
		}
	}
	for _, name := range []string{"df-close-focus", "df-wire", "df-deferred", "df-pull", "lat-wire", "rc-claim", "rc-park", "rc-fp", "rc-publish", "rc-validate"} {
		if _, registered := DefaultRegistry().Lookup(name); registered {
			t.Errorf("%s writes beads/remotes or runs a caller-supplied command and must stay unmodeled", name)
		}
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
		"pr":        {"view", "list", "status", "diff", "checks", "create"},
		"auth":      {"status"},
		"extension": {"list"},
		"stack":     {"view", "up", "down", "top", "bottom", "trunk", "init", "add", "rebase", "checkout"},
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
