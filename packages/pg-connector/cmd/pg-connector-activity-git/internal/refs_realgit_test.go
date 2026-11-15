//go:build integration

package internal

import (
	"bytes"
	"encoding/json"
	"testing"
)

// TestListActivity_RealRepo_Refs reads real commits and asserts fields.refs:
// subject and multi-line body are both scanned, and a commit with no
// references still carries an empty JSON array (never null or absent).
func TestListActivity_RealRepo_Refs(t *testing.T) {
	f := newGitFixture(t)
	repo := f.newRepo()
	at := "2026-09-05T10:00:00+00:00"
	env := []string{"GIT_AUTHOR_DATE=" + at, "GIT_COMMITTER_DATE=" + at}
	f.git(repo, env, "commit", "--allow-empty",
		"-m", "feat(PROJ-123): add widget",
		"-m", "Explains the change.\n\nFixes acme/widgets#12\nSee also #7 and PROJ-123.")
	f.git(repo, env, "commit", "--allow-empty", "-m", "no references here")

	cfg := Config{AuthorEmails: []string{meEmail}, RepoPaths: []string{repo}}
	res := listWith(t, realBackend(&bytes.Buffer{}), cfg, rangeSince, rangeBefore)
	if len(res.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(res.Items))
	}
	got := map[string]json.RawMessage{}
	for _, it := range res.Items {
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(it.Fields, &raw); err != nil {
			t.Fatal(err)
		}
		refs, ok := raw["refs"]
		if !ok {
			t.Fatalf("%s: fields has no refs: %s", it.Summary, it.Fields)
		}
		got[it.Summary] = refs
	}
	if string(got["feat(PROJ-123): add widget"]) != `["PROJ-123","acme/widgets#12","#7"]` {
		t.Errorf("refs = %s", got["feat(PROJ-123): add widget"])
	}
	if string(got["no references here"]) != "[]" {
		t.Errorf("refs = %s, want []", got["no references here"])
	}
}
