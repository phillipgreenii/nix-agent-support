package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/marker"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/reviewinput"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/reviewstage"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/provider/vcs"
)

// agentDocumentedPayload is the payload shape documented in
// claude-marketplace/pg-pr/agents/pg-pr-review-code-changes.md (the code-changes
// reviewer), pg-pr-review-pr-structure.md (PR-level, null path/lines) and
// pg-pr-review-jira-alignment.md (same PR-level shape), as the orchestrator
// concatenates them: `comments` plus the `warnings` key it adds for a subagent
// that failed.
//
// `severity` carries a concrete value here; the assets document
// "error|warning|suggestion" as the ENUMERATION, not as a literal value (a
// literal enumeration is rejected — see reviewinput's tests).
const agentDocumentedPayload = `{
  "comments": [
    {
      "path": "src/main.go",
      "lines": [42],
      "message": "Unchecked error return; the err from json.Unmarshal is discarded.",
      "severity": "error"
    },
    {
      "path": null,
      "lines": null,
      "message": "Commit 3 only fixes commit 2; squash them.",
      "severity": "suggestion"
    }
  ],
  "warnings": ["pg-pr-review-jira-alignment: JIRA access unavailable. Cannot verify alignment."]
}`

// TestReviewDraft_GoldenAgentPayloadRoundTrips is pg2-cns7a acceptance criterion
// 1, end to end through the real CLI: feed `pg-pr review draft` the exact JSON
// shape the reviewer agent assets document, then assert every finding survives
// into the staged draft with a NON-EMPTY body and its CORRECT line.
//
// Red before the fix: readDraftInput json.Unmarshal'ed straight into
// reviewstage.Draft, so `message`/`lines`/`severity`/`warnings` had no target
// field, encoding/json dropped them, and all three assertions failed at once
// (body "", line 0, warnings gone).
func TestReviewDraft_GoldenAgentPayloadRoundTrips(t *testing.T) {
	resetReviewFlags()
	dir := t.TempDir()
	t.Setenv("PG_PR_STATE_HOME", dir)

	rootCmd.SetIn(strings.NewReader(agentDocumentedPayload))
	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"review", "draft", "42", "--repo", "foo/bar"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("draft: %v (stderr=%s)", err, stderr.String())
	}

	draft, err := reviewstage.Load(reviewstage.DefaultDir(), "foo/bar", 42)
	if err != nil {
		t.Fatalf("load staged draft: %v", err)
	}
	if len(draft.Comments) != 2 {
		t.Fatalf("staged comments = %d, want 2 (no finding may be dropped)", len(draft.Comments))
	}

	// Finding 1: inline, anchored to line 42 of src/main.go.
	inline := draft.Comments[0]
	if inline.Body == "" {
		t.Error("inline comment body is empty — the pg2-cns7a defect")
	}
	if !strings.Contains(inline.Body, "Unchecked error return") {
		t.Errorf("inline body = %q, want the agent's message text", inline.Body)
	}
	if !strings.HasPrefix(inline.Body, "**error**: ") {
		t.Errorf("inline body = %q, want the severity rendered into the body", inline.Body)
	}
	if inline.Path != "src/main.go" {
		t.Errorf("inline path = %q, want src/main.go", inline.Path)
	}
	if inline.Line != 42 {
		t.Errorf(`inline line = %d, want 42 (from the documented "lines":[42])`, inline.Line)
	}

	// Finding 2: PR-level (path null, lines null) — un-anchored but still text.
	prLevel := draft.Comments[1]
	if prLevel.Body == "" {
		t.Error("PR-level comment body is empty — the pg2-cns7a defect")
	}
	if !strings.Contains(prLevel.Body, "squash them") {
		t.Errorf("PR-level body = %q, want the agent's message text", prLevel.Body)
	}
	if prLevel.Path != "" || prLevel.Line != 0 {
		t.Errorf("PR-level path/line = %q/%d, want unanchored", prLevel.Path, prLevel.Line)
	}

	// The orchestrator's `warnings` key had no target field either; it must
	// reach the review body rather than vanish.
	if !strings.Contains(draft.Body, "JIRA access unavailable") {
		t.Errorf("draft body = %q, want the orchestrator's warning carried into it", draft.Body)
	}
}

// TestReviewSubmit_GoldenAgentPayloadPostsRealComments is the same golden
// payload down the LIVE post path (the one the pr-pool review role drives):
// the comments handed to the provider must carry real bodies and lines.
func TestReviewSubmit_GoldenAgentPayloadPostsRealComments(t *testing.T) {
	resetReviewFlags()
	t.Setenv("PG_PR_STATE_HOME", t.TempDir())
	prev := vcsProviderFor
	t.Cleanup(func() { vcsProviderFor = prev })
	fake := &reviewFakeVCS{}
	vcsProviderFor = func(string) vcs.Provider { return fake }

	rootCmd.SetIn(strings.NewReader(agentDocumentedPayload))
	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"review", "submit", "42", "--repo", "foo/bar"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("submit: %v (stderr=%s)", err, stderr.String())
	}

	if len(fake.postedComments) != 2 {
		t.Fatalf("posted comments = %d, want 2", len(fake.postedComments))
	}
	for i, c := range fake.postedComments {
		if strings.TrimSpace(marker.Strip(c.Body)) == "" {
			t.Errorf("posted comments[%d] has no content beyond the marker: %q", i, c.Body)
		}
	}
	if fake.postedComments[0].Line != 42 {
		t.Errorf("posted comments[0].line = %d, want 42", fake.postedComments[0].Line)
	}
	if !strings.Contains(fake.postedBody, "JIRA access unavailable") {
		t.Errorf("posted body = %q, want the warning", fake.postedBody)
	}
}

// multiLineFindingPayload is a reviewer-agent finding that genuinely spans three
// lines, in the `lines: [...]` ARRAY form the assets emit. Before pg2-3c8mo this
// was un-postable BY DESIGN: pg2-cns7a's decoder rejected a multi-entry array
// rather than truncate it to lines[0] and misplace the comment.
const multiLineFindingPayload = `{
  "comments": [
    {
      "path": "src/main.go",
      "lines": [10, 11, 12],
      "severity": "error",
      "message": "This three-line retry block swallows the error on every attempt."
    }
  ]
}`

// TestReviewSubmit_MultiLineFindingPostsAsMultiLineComment is pg2-3c8mo AC4 down
// the live CLI post path: a contiguous multi-line finding reaches the provider as
// one comment spanning start_line 10..line 12 — neither rejected, nor truncated to
// a single line, nor folded into the review body.
func TestReviewSubmit_MultiLineFindingPostsAsMultiLineComment(t *testing.T) {
	resetReviewFlags()
	t.Setenv("PG_PR_STATE_HOME", t.TempDir())
	prev := vcsProviderFor
	t.Cleanup(func() { vcsProviderFor = prev })
	fake := &reviewFakeVCS{}
	vcsProviderFor = func(string) vcs.Provider { return fake }

	rootCmd.SetIn(strings.NewReader(multiLineFindingPayload))
	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"review", "submit", "42", "--repo", "foo/bar"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("submit: %v (stderr=%s)", err, stderr.String())
	}

	if len(fake.postedComments) != 1 {
		t.Fatalf("posted comments = %d, want 1", len(fake.postedComments))
	}
	got := fake.postedComments[0]
	if got.StartLine != 10 || got.Line != 12 {
		t.Errorf("posted span = start_line %d/line %d, want 10/12", got.StartLine, got.Line)
	}
	if got.Path != "src/main.go" {
		t.Errorf("posted path = %q, want src/main.go", got.Path)
	}
	if !strings.Contains(got.Body, "swallows the error") {
		t.Errorf("posted body = %q, want the finding text", got.Body)
	}
	if strings.Contains(fake.postedBody, "swallows the error") {
		t.Errorf("the finding must post inline, not fold into the review body: %q", fake.postedBody)
	}
}

// TestReviewDraft_RejectsNonContiguousLines: a gapped `lines` array has no GitHub
// representation, so it stays a LOUD error (pg2-3c8mo AC3) — collapsing it to its
// endpoints would claim lines the finding never named.
func TestReviewDraft_RejectsNonContiguousLines(t *testing.T) {
	resetReviewFlags()
	dir := t.TempDir()
	t.Setenv("PG_PR_STATE_HOME", dir)

	rootCmd.SetIn(strings.NewReader(`{"comments":[{"path":"a.go","lines":[10,12],"body":"x"}]}`))
	rootCmd.SetOut(io_discard)
	rootCmd.SetErr(io_discard)
	rootCmd.SetArgs([]string{"review", "draft", "42", "--repo", "foo/bar"})

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected a non-zero exit for a non-contiguous lines array")
	}
	if !strings.Contains(err.Error(), "contiguous") {
		t.Errorf("error %q should say the range is not contiguous", err)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "reviews", "*.json"))
	if len(files) != 0 {
		t.Errorf("a rejected payload must stage nothing, got %v", files)
	}
}

// TestReviewDraft_RejectsUnmappableCommentKey is pg2-cns7a acceptance criterion
// 3 for `review draft`: a payload carrying a comment key the schema cannot map
// fails with a non-zero exit whose message names the key, and stages nothing.
func TestReviewDraft_RejectsUnmappableCommentKey(t *testing.T) {
	resetReviewFlags()
	dir := t.TempDir()
	t.Setenv("PG_PR_STATE_HOME", dir)

	in := `{"comments":[{"path":"a.go","line":1,"body":"x","confidence":0.9}]}`
	rootCmd.SetIn(strings.NewReader(in))
	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"review", "draft", "42", "--repo", "foo/bar"})

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected a non-zero exit for an unmappable comment key, got nil")
	}
	if !strings.Contains(err.Error(), `"confidence"`) {
		t.Errorf("error %q must name the offending key", err)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "reviews", "*.json"))
	if len(files) != 0 {
		t.Errorf("a rejected payload must stage nothing, got %v", files)
	}
}

// TestReviewSubmit_RejectsUnmappableTopLevelKey is acceptance criterion 3 for
// `review submit`: rejection happens BEFORE any upstream write.
func TestReviewSubmit_RejectsUnmappableTopLevelKey(t *testing.T) {
	resetReviewFlags()
	t.Setenv("PG_PR_STATE_HOME", t.TempDir())
	prev := vcsProviderFor
	t.Cleanup(func() { vcsProviderFor = prev })
	fake := &reviewFakeVCS{}
	vcsProviderFor = func(string) vcs.Provider { return fake }

	// The jira-alignment agent's own output keys: valid for that subagent, but
	// the orchestrator must map them into `warnings`, not forward them here.
	in := `{"comments":[],"tickets_found":["DE-123"],"tickets_accessible":false}`
	rootCmd.SetIn(strings.NewReader(in))
	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"review", "submit", "42", "--repo", "foo/bar"})

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected a non-zero exit for an unmappable top-level key, got nil")
	}
	if !strings.Contains(err.Error(), "tickets_found") {
		t.Errorf("error %q must name the offending key", err)
	}
	if fake.postCalls != 0 {
		t.Errorf("nothing may be posted for a rejected payload, got %d call(s)", fake.postCalls)
	}
}

// TestReviewDraft_RejectsBlankCommentBody: the exact observable symptom of
// pg2-cns7a (a comment with no text) is now an error, not a blank posted comment.
func TestReviewDraft_RejectsBlankCommentBody(t *testing.T) {
	resetReviewFlags()
	t.Setenv("PG_PR_STATE_HOME", t.TempDir())

	rootCmd.SetIn(strings.NewReader(`{"comments":[{"path":"a.go","line":1}]}`))
	rootCmd.SetOut(io_discard)
	rootCmd.SetErr(io_discard)
	rootCmd.SetArgs([]string{"review", "draft", "42", "--repo", "foo/bar"})

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected a non-zero exit for a comment with no body")
	}
	if !strings.Contains(err.Error(), "comments[0]") {
		t.Errorf("error %q should locate the offending comment", err)
	}
}

// TestReviewPost_RefusesLegacyBlankStagedDraft: `review post` reads the staged
// FILE, bypassing the input schema, so a draft staged by a pre-fix pg-pr (every
// comment blank) would still publish marker-only comments upstream. It must fail
// loudly and post nothing instead.
func TestReviewPost_RefusesLegacyBlankStagedDraft(t *testing.T) {
	resetReviewFlags()
	dir := t.TempDir()
	t.Setenv("PG_PR_STATE_HOME", dir)
	prev := vcsProviderFor
	t.Cleanup(func() { vcsProviderFor = prev })
	fake := &reviewFakeVCS{}
	vcsProviderFor = func(string) vcs.Provider { return fake }

	// Exactly what the pre-fix decoder produced from an agent payload.
	legacy := &reviewstage.Draft{
		Repo: "foo/bar", PR: 42,
		Comments: []api.Comment{{Path: "src/main.go", Line: 0, Body: ""}},
	}
	if _, err := reviewstage.Save(reviewstage.DefaultDir(), legacy); err != nil {
		t.Fatalf("stage legacy draft: %v", err)
	}

	rootCmd.SetIn(strings.NewReader(""))
	rootCmd.SetOut(io_discard)
	rootCmd.SetErr(io_discard)
	rootCmd.SetArgs([]string{"review", "post", "42", "--repo", "foo/bar"})

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected a non-zero exit for a staged draft with blank comments")
	}
	if !strings.Contains(err.Error(), "no text") {
		t.Errorf("error %q should say the comments have no text", err)
	}
	if fake.postCalls != 0 {
		t.Errorf("nothing may be posted, got %d call(s)", fake.postCalls)
	}
}

// TestReviewHelp_DocumentsThePayloadSchema is pg2-cns7a acceptance criterion 4:
// the reviewer-agent assets and the pr-pool review-role prompt both tell their
// agent to "see `pg-pr review --help`", which before the fix resolved to no
// schema at all. The help MUST carry the schema and one complete example.
func TestReviewHelp_DocumentsThePayloadSchema(t *testing.T) {
	long := reviewCmd.Long
	for _, want := range []string{
		"TOP-LEVEL KEYS", "COMMENT KEYS", "EXAMPLE",
		`"severity"`, "warnings", "REJECTED",
	} {
		if !strings.Contains(long, want) {
			t.Errorf("`review --help` does not document %q", want)
		}
	}
	// The example must be the real, decodable one — not prose about it.
	if !strings.Contains(long, reviewinput.ExampleJSON) &&
		!strings.Contains(strings.ReplaceAll(long, "  ", ""), strings.ReplaceAll(reviewinput.ExampleJSON, "  ", "")) {
		t.Error("`review --help` does not embed reviewinput.ExampleJSON verbatim")
	}
}
