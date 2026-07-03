// Package ticketlink provides config-driven extraction of external ticket keys
// from a PR's branch name, title, and body.
package ticketlink

import (
	"bytes"
	"log/slog"
	"reflect"
	"strings"
	"testing"
)

// TestCompilePatterns_invalidPatternEmitsWarn verifies that compilePatterns
// emits a slog.Warn for each invalid regex pattern and still returns the
// valid patterns.
func TestCompilePatterns_invalidPatternEmitsWarn(t *testing.T) {
	var buf bytes.Buffer
	handler := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})
	testLogger := slog.New(handler)
	orig := slog.Default()
	slog.SetDefault(testLogger)
	t.Cleanup(func() { slog.SetDefault(orig) })

	patterns := []string{`[invalid`, `[A-Z]+-\d+`}
	compiled := compilePatterns(patterns)

	// The invalid pattern must be skipped (only valid one compiled).
	if len(compiled) != 1 {
		t.Errorf("compilePatterns: got %d compiled patterns, want 1 (valid only)", len(compiled))
	}

	// A Warn must have been logged for the invalid pattern.
	logged := buf.String()
	if !strings.Contains(logged, "WARN") {
		t.Errorf("compilePatterns: expected WARN log for invalid pattern; got %q", logged)
	}
	if !strings.Contains(logged, "[invalid") {
		t.Errorf("compilePatterns: expected log to mention the bad pattern; got %q", logged)
	}
}

// TestParse is a comprehensive table-driven test for Parse. It covers:
//   - branch-only match
//   - title-only match
//   - body-only match
//   - precedence ordering (branch > title > body)
//   - multi-reference de-duplication (same key from multiple sources)
//   - multiple distinct keys in a single field
//   - no match — graceful empty return
//   - empty patterns slice — no match
//   - custom patterns
func TestParse(t *testing.T) {
	defaultPatterns := []string{`[A-Z]+-\d+`}

	cases := []struct {
		name     string
		branch   string
		title    string
		body     string
		patterns []string
		want     []string
	}{
		{
			name:     "branch only",
			branch:   "phillipg.PROJ-123.my-feature",
			title:    "add something",
			body:     "no ticket here",
			patterns: defaultPatterns,
			want:     []string{"PROJ-123"},
		},
		{
			name:     "title only",
			branch:   "fix-stuff",
			title:    "fix(api): resolve FINDEV-42 timeout",
			body:     "no ticket here",
			patterns: defaultPatterns,
			want:     []string{"FINDEV-42"},
		},
		{
			name:     "body only",
			branch:   "my-branch",
			title:    "update things",
			body:     "This PR addresses INFRA-999 and the underlying issue.",
			patterns: defaultPatterns,
			want:     []string{"INFRA-999"},
		},
		{
			name:     "branch wins over title when different keys",
			branch:   "user.PROJ-1.feature",
			title:    "fixes PROJ-2: something",
			body:     "",
			patterns: defaultPatterns,
			// branch key first, then title key
			want: []string{"PROJ-1", "PROJ-2"},
		},
		{
			name:     "dedup same key from branch and title",
			branch:   "user.FINDEV-99.bugfix",
			title:    "fix FINDEV-99: something",
			body:     "See FINDEV-99 for details.",
			patterns: defaultPatterns,
			want:     []string{"FINDEV-99"},
		},
		{
			name:     "dedup same key from title and body",
			branch:   "plain-branch",
			title:    "fix FINDEV-55",
			body:     "Closes FINDEV-55.",
			patterns: defaultPatterns,
			want:     []string{"FINDEV-55"},
		},
		{
			name:     "multiple distinct keys in body",
			branch:   "plain-branch",
			title:    "merge work",
			body:     "Relates to API-10 and API-20. Also see API-30.",
			patterns: defaultPatterns,
			want:     []string{"API-10", "API-20", "API-30"},
		},
		{
			name:     "branch and body each have different keys",
			branch:   "user.X-1.thing",
			title:    "normal title",
			body:     "Addresses Y-2 and Y-3.",
			patterns: defaultPatterns,
			want:     []string{"X-1", "Y-2", "Y-3"},
		},
		{
			name:     "no match in any field",
			branch:   "my-feature-branch",
			title:    "update README",
			body:     "just a description with no ticket",
			patterns: defaultPatterns,
			want:     nil,
		},
		{
			name:     "empty patterns returns nil",
			branch:   "user.PROJ-1.feature",
			title:    "fix PROJ-1",
			body:     "",
			patterns: []string{},
			want:     nil,
		},
		{
			name:     "nil patterns returns nil",
			branch:   "user.PROJ-1.feature",
			title:    "fix PROJ-1",
			body:     "",
			patterns: nil,
			want:     nil,
		},
		{
			name:     "custom pattern extracts numeric-only key",
			branch:   "ticket-4567",
			title:    "fix ticket-4567",
			body:     "",
			patterns: []string{`ticket-\d+`},
			want:     []string{"ticket-4567"},
		},
		{
			name:     "lowercase keys preserved as-is",
			branch:   "feat/proj-42-new-ui",
			title:    "add new ui",
			body:     "",
			patterns: []string{`proj-\d+`},
			want:     []string{"proj-42"},
		},
		{
			name:     "invalid regex pattern is skipped gracefully",
			branch:   "user.PROJ-1.feature",
			title:    "fix PROJ-1",
			body:     "",
			patterns: []string{`[invalid`, `[A-Z]+-\d+`},
			want:     []string{"PROJ-1"},
		},
		{
			name:     "all empty inputs returns nil",
			branch:   "",
			title:    "",
			body:     "",
			patterns: defaultPatterns,
			want:     nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Parse(tc.branch, tc.title, tc.body, tc.patterns)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Parse(%q, %q, %q, %v)\n  got  %v\n  want %v",
					tc.branch, tc.title, tc.body, tc.patterns, got, tc.want)
			}
		})
	}
}
