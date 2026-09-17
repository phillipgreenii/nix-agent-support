package sync

import "testing"

func TestClassifyBead(t *testing.T) {
	cases := []struct {
		name     string
		title    string
		metadata map[string]string
		wantKind string
		wantRepo string
		wantNum  int
		wantOK   bool
	}{
		{"anchor by title", "myorg/repo#42: fix the bug", nil, KindAnchor, "myorg/repo", 42, true},
		{
			"anchor with matching metadata", "myorg/repo#42: fix the bug",
			map[string]string{"repo": "myorg/repo", "pr_number": "42"},
			KindAnchor, "myorg/repo", 42, true,
		},
		{
			"anchor with disagreeing metadata", "myorg/repo#42: fix the bug",
			map[string]string{"repo": "myorg/repo", "pr_number": "7"},
			"", "", 0, false,
		},
		{"feedback cycle", "process-feedback: myorg/repo#42", nil, KindFeedbackCycle, "myorg/repo", 42, true},
		{"review request", "review-pr: myorg/repo#42", nil, KindReviewRequest, "myorg/repo", 42, true},
		{"unrelated title", "some other bead", nil, "", "", 0, false},
		{"anchor title with hash in pr title", "myorg/repo#42: fix #123 bug", nil, KindAnchor, "myorg/repo", 42, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kind, repo, num, ok := ClassifyBead(tc.title, tc.metadata)
			if ok != tc.wantOK || kind != tc.wantKind || repo != tc.wantRepo || num != tc.wantNum {
				t.Fatalf("ClassifyBead(%q, %v) = (%q, %q, %d, %v), want (%q, %q, %d, %v)",
					tc.title, tc.metadata, kind, repo, num, ok, tc.wantKind, tc.wantRepo, tc.wantNum, tc.wantOK)
			}
		})
	}
}
