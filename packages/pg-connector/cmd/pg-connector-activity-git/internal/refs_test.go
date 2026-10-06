package internal

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
)

func TestExtractRefs(t *testing.T) {
	cases := []struct {
		name          string
		subject, body string
		want          []string
	}{
		{"ticket in a conventional-commit scope", "feat(PROJ-123): add widget", "", []string{"PROJ-123"}},
		{"bare pull request number", "Fixes #12", "", []string{"#12"}},
		{"qualified pull request in the body", "tidy", "Fixes acme/widgets#12", []string{"acme/widgets#12"}},
		{"qualified is not also reported bare", "Refs acme/widgets#12", "", []string{"acme/widgets#12"}},
		{"repeated key reported once", "PROJ-123 first", "see PROJ-123 again", []string{"PROJ-123"}},
		{"lowercase key yields nothing", "proj-123 lowercase", "", []string{}},
		{"parenthesised key", "fix thing (PROJ-45)", "", []string{"PROJ-45"}},
		{"bare number in the body", "x", "Closes #7", []string{"#7"}},
		{"no references", "plain subject", "plain body\nsecond line", []string{}},
		{"empty message", "", "", []string{}},
		{"order of first appearance across subject and body", "PROJ-2 and #5", "acme/widgets#9\nPROJ-1 #5 PROJ-2", []string{"PROJ-2", "#5", "acme/widgets#9", "PROJ-1"}},
		{"qualified and bare of the same number are distinct", "acme/widgets#12", "also #12", []string{"acme/widgets#12", "#12"}},
		{"hyphenated owner and dotted repo", "Fixes my-org/my.repo#3", "", []string{"my-org/my.repo#3"}},
		{"hash glued to a word is not a reference", "page.md#12 abc#34", "", []string{}},
		{"number followed by letters is not a reference", "#12abc", "", []string{}},
		{"two keys, one per line", "PROJ-1", "PROJ-22\nPROJ-1", []string{"PROJ-1", "PROJ-22"}},
		{"punctuation around a bare number", "(#7), #8.", "", []string{"#7", "#8"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractRefs(tc.subject, tc.body)
			if got == nil {
				t.Fatal("extractRefs returned nil; want a non-nil slice")
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("extractRefs(%q, %q) = %q, want %q", tc.subject, tc.body, got, tc.want)
			}
		})
	}
}

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
