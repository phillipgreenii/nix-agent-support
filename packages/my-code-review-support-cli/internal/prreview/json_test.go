package prreview

import (
	"testing"
)

func TestExtractJSON(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr error
	}{
		{
			name:  "clean JSON",
			input: `{"comments": []}`,
			want:  `{"comments": []}`,
		},
		{
			name:  "JSON with prefix text",
			input: "Here is the review:\n```json\n{\"comments\": []}\n```",
			want:  `{"comments": []}`,
		},
		{
			name:  "JSON with suffix text",
			input: `{"comments": []}` + "\n\nReview complete.",
			want:  `{"comments": []}`,
		},
		{
			name:  "JSON embedded in markdown",
			input: "## Review Summary\n\nFound issues:\n\n```json\n{\"comments\": [{\"path\": \"foo.go\", \"lines\": [1], \"message\": \"test\", \"severity\": \"error\"}]}\n```\n\nPlease fix.",
			want:  `{"comments": [{"path": "foo.go", "lines": [1], "message": "test", "severity": "error"}]}`,
		},
		{
			name:    "no JSON",
			input:   "no json here",
			wantErr: ErrNoJSONFound,
		},
		{
			name:    "incomplete JSON - no closing brace",
			input:   `{"comments": [`,
			wantErr: ErrNoJSONFound,
		},
		{
			name:    "invalid JSON structure",
			input:   `{not valid json}`,
			wantErr: ErrInvalidJSON,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ExtractJSON(tt.input)

			if tt.wantErr != nil {
				if err == nil {
					t.Errorf("ExtractJSON() error = nil, wantErr %v", tt.wantErr)
					return
				}
				return
			}

			if err != nil {
				t.Errorf("ExtractJSON() unexpected error = %v", err)
				return
			}

			if got != tt.want {
				t.Errorf("ExtractJSON() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseReviewOutput(t *testing.T) {
	tests := []struct {
		name          string
		input         string
		wantComments  int
		wantFirstPath string
		wantErr       bool
	}{
		{
			name:         "empty comments",
			input:        `{"comments": []}`,
			wantComments: 0,
		},
		{
			name: "single comment",
			input: `{
				"comments": [
					{"path": "src/main.go", "lines": [42], "message": "Bug here", "severity": "error"}
				]
			}`,
			wantComments:  1,
			wantFirstPath: "src/main.go",
		},
		{
			name: "multiple comments",
			input: `{
				"comments": [
					{"path": "a.go", "lines": [1], "message": "Issue 1", "severity": "error"},
					{"path": "b.go", "lines": [2, 5], "message": "Issue 2", "severity": "warning"},
					{"path": null, "lines": null, "message": "PR comment", "severity": "suggestion"}
				]
			}`,
			wantComments:  3,
			wantFirstPath: "a.go",
		},
		{
			name:    "invalid JSON",
			input:   `{not json}`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseReviewOutput(tt.input)

			if tt.wantErr {
				if err == nil {
					t.Error("ParseReviewOutput() error = nil, wantErr true")
				}
				return
			}

			if err != nil {
				t.Errorf("ParseReviewOutput() unexpected error = %v", err)
				return
			}

			if len(got.Comments) != tt.wantComments {
				t.Errorf("ParseReviewOutput() comments = %d, want %d", len(got.Comments), tt.wantComments)
			}

			if tt.wantFirstPath != "" && len(got.Comments) > 0 {
				if got.Comments[0].Path == nil || *got.Comments[0].Path != tt.wantFirstPath {
					path := "<nil>"
					if got.Comments[0].Path != nil {
						path = *got.Comments[0].Path
					}
					t.Errorf("ParseReviewOutput() first path = %s, want %s", path, tt.wantFirstPath)
				}
			}
		})
	}
}

func TestTransformToGitHubComments(t *testing.T) {
	path := "test.go"
	tests := []struct {
		name             string
		comments         []ReviewComment
		wantFileCount    int
		wantPRLevelCount int
		checkFirst       func(t *testing.T, c GitHubComment)
	}{
		{
			name:             "empty",
			comments:         []ReviewComment{},
			wantFileCount:    0,
			wantPRLevelCount: 0,
		},
		{
			name: "single-line comment",
			comments: []ReviewComment{
				{Path: &path, Lines: []int{42}, Message: "Issue", Severity: "error"},
			},
			wantFileCount:    1,
			wantPRLevelCount: 0,
			checkFirst: func(t *testing.T, c GitHubComment) {
				if c.Line == nil || *c.Line != 42 {
					t.Error("expected line 42")
				}
				if c.StartLine != nil {
					t.Error("expected no start_line for single-line")
				}
				if c.Side != "RIGHT" {
					t.Errorf("expected side RIGHT, got %s", c.Side)
				}
			},
		},
		{
			name: "multi-line comment",
			comments: []ReviewComment{
				{Path: &path, Lines: []int{10, 20}, Message: "Range issue", Severity: "warning"},
			},
			wantFileCount:    1,
			wantPRLevelCount: 0,
			checkFirst: func(t *testing.T, c GitHubComment) {
				if c.StartLine == nil || *c.StartLine != 10 {
					t.Error("expected start_line 10")
				}
				if c.Line == nil || *c.Line != 20 {
					t.Error("expected line 20")
				}
			},
		},
		{
			name: "file-level comment",
			comments: []ReviewComment{
				{Path: &path, Lines: nil, Message: "File issue", Severity: "warning"},
			},
			wantFileCount:    1,
			wantPRLevelCount: 0,
			checkFirst: func(t *testing.T, c GitHubComment) {
				if c.SubjectType != "file" {
					t.Errorf("expected subject_type file, got %s", c.SubjectType)
				}
				if c.Line != nil {
					t.Error("expected no line for file-level")
				}
			},
		},
		{
			name: "PR-level comment",
			comments: []ReviewComment{
				{Path: nil, Lines: nil, Message: "Overall issue", Severity: "suggestion"},
			},
			wantFileCount:    0,
			wantPRLevelCount: 1,
		},
		{
			name: "mixed comments",
			comments: []ReviewComment{
				{Path: &path, Lines: []int{1}, Message: "Line issue", Severity: "error"},
				{Path: nil, Lines: nil, Message: "PR issue", Severity: "suggestion"},
				{Path: &path, Lines: nil, Message: "File issue", Severity: "warning"},
			},
			wantFileCount:    2,
			wantPRLevelCount: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fileComments, prLevel := TransformToGitHubComments(tt.comments)

			if len(fileComments) != tt.wantFileCount {
				t.Errorf("file comments = %d, want %d", len(fileComments), tt.wantFileCount)
			}
			if len(prLevel) != tt.wantPRLevelCount {
				t.Errorf("PR-level comments = %d, want %d", len(prLevel), tt.wantPRLevelCount)
			}

			if tt.checkFirst != nil && len(fileComments) > 0 {
				tt.checkFirst(t, fileComments[0])
			}
		})
	}
}

func TestBuildReviewBody(t *testing.T) {
	tests := []struct {
		name     string
		comments []ReviewComment
		want     string
	}{
		{
			name:     "empty",
			comments: []ReviewComment{},
			want:     "",
		},
		{
			name: "single comment",
			comments: []ReviewComment{
				{Message: "Add tests", Severity: "suggestion"},
			},
			want: "- **[suggestion]** Add tests\n\n🤖",
		},
		{
			name: "multiple comments",
			comments: []ReviewComment{
				{Message: "Issue 1", Severity: "error"},
				{Message: "Issue 2", Severity: "warning"},
			},
			want: "- **[error]** Issue 1\n\n- **[warning]** Issue 2\n\n🤖",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BuildReviewBody(tt.comments)
			if got != tt.want {
				t.Errorf("BuildReviewBody() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestIsDuplicate(t *testing.T) {
	line42 := 42
	line43 := 43

	existing := []ExistingComment{
		{Path: "a.go", Line: &line42, Body: "This is a duplicate comment\n\n🤖"},
	}

	tests := []struct {
		name    string
		comment GitHubComment
		want    bool
	}{
		{
			name:    "exact duplicate",
			comment: GitHubComment{Path: "a.go", Line: &line42, Body: "This is a duplicate comment\n\n🤖"},
			want:    true,
		},
		{
			name:    "different path",
			comment: GitHubComment{Path: "b.go", Line: &line42, Body: "This is a duplicate comment\n\n🤖"},
			want:    false,
		},
		{
			name:    "different line",
			comment: GitHubComment{Path: "a.go", Line: &line43, Body: "This is a duplicate comment\n\n🤖"},
			want:    false,
		},
		{
			name:    "different body",
			comment: GitHubComment{Path: "a.go", Line: &line42, Body: "Different message\n\n🤖"},
			want:    false,
		},
		{
			name:    "same prefix but different suffix (short bodies)",
			comment: GitHubComment{Path: "a.go", Line: &line42, Body: "This is a duplicate comment with different ending"},
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsDuplicate(tt.comment, existing); got != tt.want {
				t.Errorf("IsDuplicate() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDeduplicateComments(t *testing.T) {
	line1 := 1
	line2 := 2

	existing := []ExistingComment{
		{Path: "a.go", Line: &line1, Body: "Existing comment"},
	}

	comments := []GitHubComment{
		{Path: "a.go", Line: &line1, Body: "Existing comment"},
		{Path: "a.go", Line: &line2, Body: "New comment"},
		{Path: "b.go", Line: &line1, Body: "Another new"},
	}

	unique, skipped := DeduplicateComments(comments, existing)

	if skipped != 1 {
		t.Errorf("skipped = %d, want 1", skipped)
	}
	if len(unique) != 2 {
		t.Errorf("unique = %d, want 2", len(unique))
	}
}
