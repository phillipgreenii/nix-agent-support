package main

import (
	"reflect"
	"strings"
	"testing"
)

// previous_names on a {name, command} registration (bead pg2-ik9ew): the
// instance's former backend names, whose ledgers loadLedgerAdopting adopts
// on first use so a rename does not orphan them.

func TestPreviousNames_ParsedPerInstance(t *testing.T) {
	reg := mustParse(t, `
connector:
  issue:
    - plain-jira
    - {name: inst-pg2, command: `+instanceBeadsCmd+`, previous_names: [old-beads]}
    - {name: inst-zr, command: [bin, --zr], previous_names: [old-beads, older-beads]}
    - {name: inst-none, command: [bin, --none]}
`)
	for name, want := range map[string][]string{
		"inst-pg2":   {"old-beads"},
		"inst-zr":    {"old-beads", "older-beads"},
		"inst-none":  nil,
		"plain-jira": nil, // a plain string cannot carry previous_names
		"unknown":    nil,
	} {
		if got := reg.PreviousNames(name); !reflect.DeepEqual(got, want) {
			t.Errorf("PreviousNames(%s) = %v, want %v", name, got, want)
		}
	}
	var nilReg *Registry
	if got := nilReg.PreviousNames("x"); got != nil {
		t.Errorf("nil registry PreviousNames = %v", got)
	}
	if err := reg.Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}
	// previous_names does not change what Command resolves to.
	if got := reg.Command("inst-pg2"); !reflect.DeepEqual(got, []string{"pg-connector-issue-beads", "--beads-dir", "/example/pg2"}) {
		t.Errorf("Command = %v", got)
	}
}

func TestPreviousNames_EmptyListIsNoPreviousNames(t *testing.T) {
	reg := mustParse(t, "connector:\n  issue: [{name: a, command: [bin], previous_names: []}]\n")
	if got := reg.PreviousNames("a"); len(got) != 0 {
		t.Errorf("PreviousNames = %v, want none", got)
	}
}

func TestPreviousNames_SameListUnderSeveralKeysIsFine(t *testing.T) {
	reg := mustParse(t, `
connector:
  issue: [{name: a, command: [bin], previous_names: [old]}]
attention:
  sources: [{name: a, command: [bin], previous_names: [old]}]
activity:
  sources: [{name: a, command: [bin], previous_names: [old]}]
`)
	if got := reg.PreviousNames("a"); !reflect.DeepEqual(got, []string{"old"}) {
		t.Errorf("PreviousNames = %v", got)
	}
}

func TestPreviousNames_Errors(t *testing.T) {
	tests := []struct {
		name    string
		doc     string
		wantErr string
	}{
		{
			"not a list", "connector:\n  issue: [{name: a, command: [bin], previous_names: old}]\n",
			"previous_names must be a list of strings",
		},
		{
			"element not a string", "connector:\n  issue: [{name: a, command: [bin], previous_names: [[x]]}]\n",
			"previous_names[0] must be a string",
		},
		{
			"element null", "connector:\n  issue: [{name: a, command: [bin], previous_names: [~]}]\n",
			"previous_names[0] must be a string",
		},
		{
			"alias element", "x: &n old\nconnector:\n  issue: [{name: a, command: [bin], previous_names: [*n]}]\n",
			"previous_names[0] must be a string",
		},
		{
			"empty name", "connector:\n  issue: [{name: a, command: [bin], previous_names: [\"\"]}]\n",
			"backend name must not be empty",
		},
		{
			"name with slash", "connector:\n  issue: [{name: a, command: [bin], previous_names: [x/y]}]\n",
			"must be a bare binary name, not a path",
		},
		{
			"name with double underscore", "connector:\n  issue: [{name: a, command: [bin], previous_names: [x__y]}]\n",
			`must not contain "__"`,
		},
		{
			"name with trailing underscore", "connector:\n  issue: [{name: a, command: [bin], previous_names: [x_]}]\n",
			`must not start or end with "_"`,
		},
		{
			"equals own name", "connector:\n  issue: [{name: a, command: [bin], previous_names: [a]}]\n",
			`previous name "a" must not equal the backend's own name`,
		},
		{
			"duplicate in list", "connector:\n  issue: [{name: a, command: [bin], previous_names: [old, old]}]\n",
			`duplicate previous name "old"`,
		},
		{
			"names another registered backend", "connector:\n  issue:\n    - {name: a, command: [bin], previous_names: [b]}\n    - {name: b, command: [bin, --b]}\n",
			`previous name "b" of backend "a" is also a registered backend`,
		},
		{
			"names a plain registered backend", "connector:\n  issue:\n    - {name: a, command: [bin], previous_names: [b]}\n    - b\n",
			`previous name "b" of backend "a" is also a registered backend`,
		},
		{
			"different lists under two keys", "connector:\n  issue: [{name: a, command: [bin], previous_names: [old]}]\nactivity:\n  sources: [{name: a, command: [bin], previous_names: [other]}]\n",
			`backend "a" is registered with different previous_names`,
		},
		{
			"list under one key, none under another", "connector:\n  issue: [{name: a, command: [bin], previous_names: [old]}]\nactivity:\n  sources: [{name: a, command: [bin]}]\n",
			`backend "a" is registered with different previous_names`,
		},
		{
			"duplicate previous_names key", "connector:\n  issue: [{name: a, command: [bin], previous_names: [x], previous_names: [y]}]\n",
			`unknown or duplicate key "previous_names"`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reg, err := parseRegistry([]byte(tc.doc), "test.yaml")
			if err == nil {
				err = reg.Validate()
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want substring %q", err, tc.wantErr)
			}
		})
	}
}
