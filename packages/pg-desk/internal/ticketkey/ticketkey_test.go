package ticketkey

import (
	"bytes"
	"log/slog"
	"reflect"
	"strings"
	"testing"
)

// TestCompilePatterns_invalidPatternEmitsWarn mirrors
// packages/pg-pr/internal/ticketlink's identical test: an invalid regex
// pattern is skipped, with a slog.Warn naming it, rather than breaking the
// caller.
func TestCompilePatterns_invalidPatternEmitsWarn(t *testing.T) {
	var buf bytes.Buffer
	handler := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})
	testLogger := slog.New(handler)
	orig := slog.Default()
	slog.SetDefault(testLogger)
	t.Cleanup(func() { slog.SetDefault(orig) })

	patterns := []string{`[invalid`, `[A-Z]+-\d+`}
	compiled := compilePatterns(patterns)

	if len(compiled) != 1 {
		t.Errorf("compilePatterns: got %d compiled patterns, want 1 (valid only)", len(compiled))
	}

	logged := buf.String()
	if !strings.Contains(logged, "WARN") {
		t.Errorf("compilePatterns: expected WARN log for invalid pattern; got %q", logged)
	}
	if !strings.Contains(logged, "[invalid") {
		t.Errorf("compilePatterns: expected log to mention the bad pattern; got %q", logged)
	}
}

// TestParse ports packages/pg-pr/internal/ticketlink's own TestParse table
// verbatim (same algorithm, same cases) — this package's Parse is a port,
// not a reimplementation, and MUST match byte-for-byte.
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
			title:    "fix(api): resolve PROJ-42 timeout",
			body:     "no ticket here",
			patterns: defaultPatterns,
			want:     []string{"PROJ-42"},
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
			want:     []string{"PROJ-1", "PROJ-2"},
		},
		{
			name:     "dedup same key from branch and title",
			branch:   "user.PROJ-99.bugfix",
			title:    "fix PROJ-99: something",
			body:     "See PROJ-99 for details.",
			patterns: defaultPatterns,
			want:     []string{"PROJ-99"},
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
		{
			name:     "single-field call (gather's per-field scan) scans only that field",
			branch:   "user.PROJ-1.feature",
			title:    "",
			body:     "",
			patterns: defaultPatterns,
			want:     []string{"PROJ-1"},
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

// TestMatchesShape proves run.go's own dispatch need: an incoming id that
// matches a configured pattern IN FULL (anchored) is Jira-shaped; a beads
// id (or anything that only partially matches) is not, and an empty
// patterns list never recognizes anything.
func TestMatchesShape(t *testing.T) {
	patterns := []string{`[A-Z]+-\d+`}

	cases := []struct {
		name     string
		id       string
		patterns []string
		want     bool
	}{
		{"full match", "PROJ-1234", patterns, true},
		{"beads id does not match", "zr-cvr5v", patterns, false},
		{"dotted beads id does not match", "pg2-2j5ac.40.2", patterns, false},
		{"partial match is not a full-string match", "prefix-PROJ-1234-suffix", patterns, false},
		{"empty id never matches", "", patterns, false},
		{"nil patterns never matches", "PROJ-1234", nil, false},
		{"empty patterns never matches", "PROJ-1234", []string{}, false},
		{"invalid pattern skipped, valid one still matches", "PROJ-1234", []string{`[invalid`, `[A-Z]+-\d+`}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := MatchesShape(tc.id, tc.patterns); got != tc.want {
				t.Errorf("MatchesShape(%q, %v) = %v, want %v", tc.id, tc.patterns, got, tc.want)
			}
		})
	}
}
