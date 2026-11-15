package internal

import (
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
