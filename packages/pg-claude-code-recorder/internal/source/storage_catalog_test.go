package source_test

import (
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/source"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/sources"
)

// Every compiled-in source's tags are valid, each catalogued field has exactly
// one storage, and the lookup-routed fields name the columns the records table
// has (the parser routes by these declarations).
func TestCompiledInSourcesDeclareTheLookupColumns(t *testing.T) {
	want := map[string]map[string]string{
		"claude-transcript": {"cwd": "record:cwd_id", "git_branch": "record:branch_id", "version": "record:version_id", "entrypoint": "record:entrypoint_id", "user_type": "record:user_type_id"},
		"session-registry":  {"cwd": "record:cwd_id", "version": "record:version_id", "entrypoint": "record:entrypoint_id"},
	}
	for _, s := range sources.All() {
		st, ok := s.(source.Stored)
		if !ok {
			if len(want[s.Name()]) != 0 {
				t.Errorf("%s does not implement Stored", s.Name())
			}
			continue
		}
		got := st.Storage() // panics on an invalid declaration
		routed := map[string]string{}
		for name, d := range got {
			if d.Kind == source.StoreRecord {
				routed[name] = d.String()
			}
		}
		if len(routed) != len(want[s.Name()]) {
			t.Errorf("%s routed %v, want %v", s.Name(), routed, want[s.Name()])
		}
		for name, w := range want[s.Name()] {
			if routed[name] != w {
				t.Errorf("%s field %s: %q, want %q", s.Name(), name, routed[name], w)
			}
		}
	}
}
