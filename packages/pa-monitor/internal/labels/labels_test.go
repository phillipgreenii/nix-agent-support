package labels

import "testing"

func TestSet_MergeEmptyValueDropped(t *testing.T) {
	a := Set{"workspace.repo": "github.com/x/y"}
	b := Set{"workspace.scope": ""}
	m := a.Merge(b)
	if _, ok := m["workspace.scope"]; ok {
		t.Error("empty value should be dropped")
	}
	if m["workspace.repo"] != "github.com/x/y" {
		t.Errorf("repo lost: %+v", m)
	}
}

func TestSet_MergeOverwritesByLatest(t *testing.T) {
	a := Set{"agent.role": "user"}
	b := Set{"agent.role": "polecat"}
	m := a.Merge(b)
	if m["agent.role"] != "polecat" {
		t.Errorf("override failed: %+v", m)
	}
}
