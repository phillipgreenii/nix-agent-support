package main

import (
	"reflect"
	"strings"
	"testing"
)

// The registry entry shape (bead pg2-91y12, INV-REG-4): plain string or
// {name, command}, accepted under every registration key.

const instanceBeadsCmd = "[pg-connector-issue-beads, --beads-dir, /example/pg2]"

func mustParse(t *testing.T, yamlDoc string) *Registry {
	t.Helper()
	reg, err := parseRegistry([]byte(yamlDoc), "test.yaml")
	if err != nil {
		t.Fatalf("parseRegistry: %v", err)
	}
	return reg
}

func TestRegistryInstances_AcceptedUnderEveryKey(t *testing.T) {
	inst := "{name: inst-a, command: " + instanceBeadsCmd + "}"
	reg := mustParse(t, `
connector:
  pr: [plain-pr, `+inst+`]
  issue: [`+inst+`]
  ci: [`+inst+`]
  scm: `+inst+`
  thread: [`+inst+`]
  calendar: [`+inst+`]
  agentsession: [`+inst+`]
  alert: [`+inst+`]
  mail: [`+inst+`]
attention: {sources: [`+inst+`]}
search: {sources: [`+inst+`]}
activity: {sources: [`+inst+`]}
`)
	for _, typ := range []string{"pr", "issue", "ci", "thread", "calendar", "agentsession", "alert", "mail"} {
		got, err := reg.List(typ)
		if err != nil {
			t.Fatalf("List(%s): %v", typ, err)
		}
		if got[len(got)-1] != "inst-a" {
			t.Errorf("List(%s) = %v, want trailing inst-a", typ, got)
		}
	}
	if scm, err := reg.Single("scm"); err != nil || scm != "inst-a" {
		t.Errorf("Single(scm) = %q, %v", scm, err)
	}
	for name, fn := range map[string]func() ([]string, error){
		"attention": reg.AttentionSources, "search": reg.SearchSources, "activity": reg.ActivitySources,
	} {
		if got, err := fn(); err != nil || !reflect.DeepEqual(got, []string{"inst-a"}) {
			t.Errorf("%s sources = %v, %v", name, got, err)
		}
	}
	if err := reg.Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}
}

func TestRegistryInstances_CommandResolution(t *testing.T) {
	reg := mustParse(t, `
connector:
  issue:
    - plain-jira
    - {name: inst-pg2, command: `+instanceBeadsCmd+`}
`)
	if got := reg.Command("inst-pg2"); !reflect.DeepEqual(got, []string{"pg-connector-issue-beads", "--beads-dir", "/example/pg2"}) {
		t.Errorf("explicit Command = %v", got)
	}
	for name, got := range map[string][]string{
		"plain-jira": reg.Command("plain-jira"), // plain string => [name]
		"unknown":    reg.Command("unknown"),    // unregistered => [name]
	} {
		if !reflect.DeepEqual(got, []string{name}) {
			t.Errorf("Command(%s) = %v, want [%s]", name, got, name)
		}
	}
	var nilReg *Registry
	if got := nilReg.Command("x"); !reflect.DeepEqual(got, []string{"x"}) {
		t.Errorf("nil registry Command = %v", got)
	}
	if tgt := reg.Target("inst-pg2"); tgt.Name != "inst-pg2" || tgt.Command[0] != "pg-connector-issue-beads" {
		t.Errorf("Target = %+v", tgt)
	}
	if got := reg.Explicit(); len(got) != 1 || got["inst-pg2"] == nil {
		t.Errorf("Explicit = %v, want only inst-pg2", got)
	}
}

func TestRegistryInstances_Errors(t *testing.T) {
	tests := []struct {
		name    string
		doc     string
		access  func(*Registry) error
		wantErr string
	}{
		{
			"duplicate name in a list", "connector:\n  issue:\n    - {name: a, command: [bin]}\n    - {name: a, command: [bin, x]}\n",
			listIssue, `registry: connector.issue: duplicate backend name "a"`,
		},
		{
			"duplicate plain vs instance", "connector:\n  issue:\n    - a\n    - {name: a, command: [bin]}\n",
			listIssue, `duplicate backend name "a"`,
		},
		{
			"empty command list", "connector:\n  issue:\n    - {name: a, command: []}\n",
			listIssue, `backend "a": command must not be an empty list`,
		},
		{
			"command missing", "connector:\n  issue:\n    - {name: a}\n",
			listIssue, `backend "a": command is required`,
		},
		{
			"command is a shell string", "connector:\n  issue:\n    - {name: a, command: \"bin --flag x\"}\n",
			listIssue, "command must be a list of strings (an argv list), not a single value",
		},
		{
			"command element not a string", "connector:\n  issue:\n    - {name: a, command: [bin, [x]]}\n",
			listIssue, "command[1] must be a string",
		},
		{
			"command null element", "connector:\n  issue:\n    - {name: a, command: [bin, ~]}\n",
			listIssue, "command[1] must be a string",
		},
		{
			"empty first word", "connector:\n  issue:\n    - {name: a, command: [\"\", x]}\n",
			listIssue, "command[0] \"\" must be a bare binary name",
		},
		{
			"first word with slash", "connector:\n  issue:\n    - {name: a, command: [/usr/bin/bin, x]}\n",
			listIssue, "command[0] \"/usr/bin/bin\" must be a bare binary name",
		},
		{
			"first word with whitespace", "connector:\n  issue:\n    - {name: a, command: [\"bin --flag\"]}\n",
			listIssue, "command[0] \"bin --flag\" must be a bare binary name",
		},
		{
			"name missing", "connector:\n  issue:\n    - {command: [bin]}\n",
			listIssue, "connector.issue[0]: backend entry must have a name",
		},
		{
			"unknown key", "connector:\n  issue:\n    - {name: a, command: [bin], env: x}\n",
			listIssue, `unknown or duplicate key "env"`,
		},
		{
			"entry is a list", "connector:\n  issue:\n    - [a, b]\n",
			listIssue, "connector.issue[0]: a backend must be a bare binary name or a {name, command} mapping",
		},
		{
			"empty name", "connector:\n  issue:\n    - {name: \"\", command: [bin]}\n",
			listIssue, "backend name must not be empty",
		},
		{
			"name with slash", "connector:\n  issue:\n    - {name: a/b, command: [bin]}\n",
			listIssue, `backend name "a/b" must be a bare binary name, not a path`,
		},
		{
			"name with double underscore", "connector:\n  issue:\n    - {name: a__b, command: [bin]}\n",
			listIssue, `must not contain "__"`,
		},
		{
			"plain name with double underscore", "connector:\n  issue:\n    - a__b\n",
			listIssue, `must not contain "__"`,
		},
		{
			"scm list rejected", "connector:\n  scm: [a]\n",
			func(r *Registry) error { _, err := r.Single("scm"); return err }, "must be a single backend binary name",
		},
		{
			"attention entry errors", "attention:\n  sources:\n    - {name: a, command: []}\n",
			func(r *Registry) error { _, err := r.AttentionSources(); return err }, "command must not be an empty list",
		},
		{
			"search entry errors", "search:\n  sources:\n    - {name: a, command: [x/y]}\n",
			func(r *Registry) error { _, err := r.SearchSources(); return err }, "command[0]",
		},
		{
			"activity entry errors", "activity:\n  sources:\n    - {name: a, command: [bin]}\n    - {name: a, command: [bin]}\n",
			func(r *Registry) error { _, err := r.ActivitySources(); return err }, `registry: activity.sources: duplicate backend name "a"`,
		},
		{
			"Validate reaches activity", "connector:\n  issue: [ok]\nactivity:\n  sources:\n    - {name: a}\n",
			func(r *Registry) error { return r.Validate() }, "command is required",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reg, err := parseRegistry([]byte(tc.doc), "test.yaml")
			if err == nil {
				err = tc.access(reg)
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want substring %q", err, tc.wantErr)
			}
		})
	}
}

func listIssue(r *Registry) error { _, err := r.List("issue"); return err }

func TestRegistryInstances_SameNameDifferentCommandConflicts(t *testing.T) {
	for _, tc := range []struct{ name, doc string }{
		{"mapping vs mapping", "connector:\n  issue: [{name: a, command: [bin, x]}]\nactivity:\n  sources: [{name: a, command: [bin, y]}]\n"},
		{"string vs mapping", "connector:\n  issue: [a]\nactivity:\n  sources: [{name: a, command: [bin]}]\n"},
		{"mapping vs string", "connector:\n  issue: [{name: a, command: [bin]}]\nsearch:\n  sources: [a]\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseRegistry([]byte(tc.doc), "test.yaml")
			if err == nil || !strings.Contains(err.Error(), `backend "a" is registered with different commands`) {
				t.Fatalf("err = %v", err)
			}
		})
	}
	// The identical command under several keys is fine.
	mustParse(t, "connector:\n  issue: [{name: a, command: [bin, x]}]\nactivity:\n  sources: [{name: a, command: [bin, x]}]\n")
}

func TestRegistryInstances_CapabilityOnlyCheckedOnNameAndCommandWord(t *testing.T) {
	for _, tc := range []struct{ name, doc string }{
		{"name", "connector:\n  issue: [{name: pg-connector-activity-x, command: [bin]}]\n"},
		{"command word", "connector:\n  issue: [{name: inst, command: [pg-connector-activity-git, --x]}]\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseRegistry([]byte(tc.doc), "test.yaml")
			if err == nil || !strings.Contains(err.Error(), "capability-only backend") {
				t.Fatalf("err = %v", err)
			}
		})
	}
	// Under activity.sources the same entries are fine.
	mustParse(t, "activity:\n  sources: [{name: inst, command: [pg-connector-activity-git, --x]}]\n")
}

func TestRegistryInstances_AllBackendsDedupesByNameKeepsOrder(t *testing.T) {
	reg := mustParse(t, `
connector:
  pr: [shared, {name: inst-b, command: [bin, --b]}]
  issue:
    - {name: inst-a, command: [bin, --a]}
    - {name: inst-b, command: [bin, --b]}
    - shared
  scm: {name: inst-scm, command: [bin, --scm]}
`)
	got, err := reg.AllBackends()
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"shared", "inst-b", "inst-a", "inst-scm"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("AllBackends = %v, want %v", got, want)
	}
}

func TestRegistryInstances_SourcesAbsentVersusEmptyUnchanged(t *testing.T) {
	// Explicit empty list is still rejected; a typo'd sub-key is absent.
	reg := mustParse(t, "activity:\n  sources: []\n")
	if _, err := reg.ActivitySources(); err == nil || !strings.Contains(err.Error(), "empty list") {
		t.Errorf("explicit empty sources err = %v", err)
	}
	reg = mustParse(t, "attention:\n  souce: [a]\n")
	if got, err := reg.AttentionSources(); err != nil || got != nil {
		t.Errorf("typo'd sources = %v, %v; want nil, nil", got, err)
	}
}

func TestRegistryInstances_BackendConfigStaysKeyedByName(t *testing.T) {
	reg := mustParse(t, `
connector:
  issue:
    - {name: inst-pg2, command: [bin, --a]}
    - {name: inst-zr, command: [bin, --b]}
backends:
  inst-pg2: {activity_actors: [One]}
  inst-zr: {activity_actors: [Two]}
`)
	a, _ := reg.BackendConfig("inst-pg2")
	b, _ := reg.BackendConfig("inst-zr")
	if !strings.Contains(string(a), "One") || !strings.Contains(string(b), "Two") {
		t.Errorf("configs = %s / %s", a, b)
	}
	if c, _ := reg.BackendConfig("bin"); c != nil {
		t.Errorf("config keyed by the binary must not exist, got %s", c)
	}
}
