package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestIsPluginSource(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"/repo/claude-marketplace/foo/skills/bar/SKILL.md", true},
		{"/repo/claude-marketplace/foo/commands/do-thing.md", true},
		{"/repo/claude-marketplace/foo/agents/reviewer.md", true},
		{"/repo/claude-marketplace/foo/hooks/pretooluse.sh", true},
		{"/repo/modules/zm/pg-router/ccpool-prompt.txt", true},
		// Not a match: wrong basename, wrong parent dir, wrong suffix.
		{"/repo/claude-marketplace/foo/README.md", false},
		{"/repo/claude-marketplace/foo/lib/helper.md", false},
		{"/repo/claude-marketplace/foo/commands/do-thing.txt", false},
		{"/repo/docs/adr/0074-something.md", false},
	}
	for _, tt := range tests {
		if got := isPluginSource(tt.path); got != tt.want {
			t.Errorf("isPluginSource(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}

func TestExtractCandidates(t *testing.T) {
	content := "# Some skill\n" +
		"\n" +
		"Prose before the block.\n" +
		"\n" +
		"```bash\n" +
		"# a comment, dropped\n" +
		"\n" +
		"$ bd show <notaplaceholder\n" +
		"kill -0 \"$PID\" \\\n" +
		"  && echo RUNNING\n" +
		"```\n" +
		"\n" +
		"Not fenced, not extracted: `bd ready`\n" +
		"\n" +
		"```python\n" +
		"print('not a shell block, not extracted')\n" +
		"```\n"

	got := extractCandidates("skill.md", content)

	// "bd show <notaplaceholder" deliberately does NOT close its angle
	// bracket, so it must NOT be swept up by the placeholder filter (that
	// filter is scoped to bare `<word>` tokens): it stays a real candidate.
	want := []string{
		"bd show <notaplaceholder",
		"kill -0 \"$PID\"  && echo RUNNING",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d candidates, want %d: %+v", len(got), len(want), got)
	}
	for i, c := range got {
		if c.text != want[i] {
			t.Errorf("candidate %d text = %q, want %q", i, c.text, want[i])
		}
		if c.file != "skill.md" {
			t.Errorf("candidate %d file = %q, want %q", i, c.file, "skill.md")
		}
	}

	// The first candidate's line number must point at the `$ bd show ...`
	// line (1-based), not at the fence or the dropped comment/blank lines
	// before it.
	if got[0].line != 8 {
		t.Errorf("candidate 0 line = %d, want 8 (the `$ bd show ...` line)", got[0].line)
	}
}

func TestExtractCandidates_NoFencedBlocks(t *testing.T) {
	content := "Just prose with an inline `bd show <id>` backtick span and no fenced block.\n"
	got := extractCandidates("skill.md", content)
	if len(got) != 0 {
		t.Errorf("got %d candidates from a file with no fenced bash/sh/shell/zsh block, want 0: %+v", len(got), got)
	}
}

// TestExtractCandidates_Placeholders pins the doc-convention placeholder
// filter (pg2-amzvw): a bare `<word>` token is a fill-in-the-blank
// placeholder, not a real command, and must not be extracted at all — this
// was the single biggest false-positive class found empirically (`bd show
// <id>`, `git -C <repo> ...`, `<verifiable statement 1>` inside a markdown
// checklist, etc. all resolved to "non-decisive" purely because `<id>`
// parses as an unterminated shell redirect).
func TestExtractCandidates_Placeholders(t *testing.T) {
	content := "```bash\n" +
		"bd show <id>\n" +
		"git -C <worktree-path> status --porcelain\n" +
		"echo real command with no placeholder\n" +
		"```\n"
	got := extractCandidates("skill.md", content)
	want := []string{"echo real command with no placeholder"}
	if len(got) != len(want) {
		t.Fatalf("got %d candidates, want %d: %+v", len(got), len(want), got)
	}
	if got[0].text != want[0] {
		t.Errorf("candidate text = %q, want %q", got[0].text, want[0])
	}
}

// TestExtractCandidates_MarkdownBulletsAndStructuralKeywords pins two more
// mechanical non-command filters: a markdown checklist bullet sharing a
// fence with real commands, and a bare shell keyword/brace that only has
// meaning as part of a surrounding compound statement.
func TestExtractCandidates_MarkdownBulletsAndStructuralKeywords(t *testing.T) {
	content := "```bash\n" +
		"- [ ] a checklist item, not a command\n" +
		"if\n" +
		"fi\n" +
		"{\n" +
		"}\n" +
		"echo real\n" +
		"```\n"
	got := extractCandidates("skill.md", content)
	if len(got) != 1 || got[0].text != "echo real" {
		t.Errorf("got %+v, want exactly one candidate %q", got, "echo real")
	}
}

// TestExtractCandidates_CompoundHeadersAndDottedPlaceholders pins two more
// filters found by running the tool for real against this repo's own
// claude-marketplace/ tree (pg2-amzvw): a compound-statement header line
// (never executable standalone; its body is separately-extracted commands)
// and a doc placeholder containing a dot/slash (e.g. a file path).
func TestExtractCandidates_CompoundHeadersAndDottedPlaceholders(t *testing.T) {
	content := "```bash\n" +
		"if [ -n \"$(git status --porcelain)\" ]; then\n" +
		"  echo dirty\n" +
		"fi\n" +
		"for repo in a b; do\n" +
		"  echo \"$repo\"\n" +
		"done\n" +
		"jq '.x' <path-to-settings.local.json>\n" +
		"echo real\n" +
		"```\n"
	got := extractCandidates("skill.md", content)
	var texts []string
	for _, c := range got {
		texts = append(texts, c.text)
	}
	want := []string{"echo dirty", "echo \"$repo\"", "echo real"}
	if len(texts) != len(want) {
		t.Fatalf("got candidates %v, want %v", texts, want)
	}
	for i := range want {
		if texts[i] != want[i] {
			t.Errorf("candidate %d = %q, want %q", i, texts[i], want[i])
		}
	}
}

// TestExtractCandidates_FunctionDefOpenerAndCaseArms pins the function
// definition opener filter and the case-arm filter (anything ending in
// `;;`, which is only ever valid inside a `case` block).
func TestExtractCandidates_FunctionDefOpenerAndCaseArms(t *testing.T) {
	content := "```bash\n" +
		"show_help() {\n" +
		"  echo usage\n" +
		"}\n" +
		"-h|--help) show_help; exit 0 ;;\n" +
		"*) POSITIONAL_ARGS+=(\"$1\") ;;\n" +
		"```\n"
	got := extractCandidates("skill.md", content)
	if len(got) != 1 || got[0].text != "echo usage" {
		t.Errorf("got %+v, want exactly one candidate %q", got, "echo usage")
	}
}

// TestExtractCandidates_HeredocAbsorption pins that a heredoc body (from its
// `<<'DELIM'` opener through the literal terminator line) joins into ONE
// candidate rather than fragmenting into one unparseable "command" per line
// of usage text.
func TestExtractCandidates_HeredocAbsorption(t *testing.T) {
	content := "```bash\n" +
		"cat <<'HELP'\n" +
		"Usage: command-name [OPTIONS]\n" +
		"\n" +
		"Options:\n" +
		"  -h, --help     Show this help message\n" +
		"HELP\n" +
		"echo after\n" +
		"```\n"
	got := extractCandidates("skill.md", content)
	if len(got) != 2 {
		t.Fatalf("got %d candidates, want 2 (the heredoc-bearing command, joined, plus the trailing echo): %+v", len(got), got)
	}
	if !strings.HasPrefix(got[0].text, "cat <<'HELP'") || !strings.Contains(got[0].text, "Options:") || !strings.HasSuffix(got[0].text, "HELP") {
		t.Errorf("candidate 0 text = %q, want it to contain the whole absorbed heredoc body", got[0].text)
	}
	if got[1].text != "echo after" {
		t.Errorf("candidate 1 text = %q, want %q", got[1].text, "echo after")
	}
}

// TestExtractCandidates_MultilineJQContinuesOnUnclosedQuote pins that an
// unterminated single-quoted jq filter (opened on one physical line, closed
// several lines later) joins into one candidate instead of each `| ...`
// continuation line fragmenting into its own unparseable "command".
func TestExtractCandidates_MultilineJQContinuesOnUnclosedQuote(t *testing.T) {
	content := "```bash\n" +
		"jq '.[] \\\n" +
		"| group_by(.approval_source) \\\n" +
		"| map({k: .[0], n: length})' /tmp/ceta-all.json\n" +
		"```\n"
	got := extractCandidates("skill.md", content)
	if len(got) != 1 {
		t.Fatalf("got %d candidates, want 1 (the whole multi-line jq invocation joined): %+v", len(got), got)
	}
	if !strings.HasPrefix(got[0].text, "jq '") || !strings.HasSuffix(got[0].text, "/tmp/ceta-all.json") {
		t.Errorf("candidate text = %q, want the whole jq invocation joined into one candidate", got[0].text)
	}
}

// TestExtractCandidates_ImplicitOperatorContinuation pins that a trailing
// `&&`/`||`/`|` (no backslash needed) joins onto the next physical line,
// same as an explicit `\` continuation — found empirically (pg2-amzvw) in
// several multi-line jq pipelines in this repo's own docs.
func TestExtractCandidates_ImplicitOperatorContinuation(t *testing.T) {
	content := "```bash\n" +
		"jq -e '.data' <<<\"$READY\" >/dev/null &&\n" +
		"echo ready\n" +
		"```\n"
	got := extractCandidates("skill.md", content)
	if len(got) != 1 {
		t.Fatalf("got %d candidates, want 1 (joined on the trailing &&): %+v", len(got), got)
	}
	want := `jq -e '.data' <<<"$READY" >/dev/null && echo ready`
	if got[0].text != want {
		t.Errorf("candidate text = %q, want %q", got[0].text, want)
	}
}

// TestAllowlistMatches pins the --allow prefix-matching semantics: a match
// requires the prefix to align with a full token boundary, so an unrelated
// command sharing a textual prefix (but not a word boundary) does not get
// swept in by accident.
func TestAllowlistMatches(t *testing.T) {
	a := allowlist{"gh stack"}
	tests := []struct {
		text string
		want bool
	}{
		{"gh stack merge --yes", true},
		{"gh stack", true},
		{"gh stacked-thing", false}, // shares a text prefix, not a word boundary
		{"gh pr merge", false},
	}
	for _, tt := range tests {
		if got := a.matches(tt.text); got != tt.want {
			t.Errorf("allowlist(%v).matches(%q) = %v, want %v", a, tt.text, got, tt.want)
		}
	}

	// A boundary can also be punctuation, not just whitespace — e.g. a
	// heredoc operator glued directly onto the command name.
	catAllow := allowlist{"cat"}
	if !catAllow.matches("cat <<'HELP'\nusage\nHELP") {
		t.Error(`allowlist{"cat"}.matches("cat <<'HELP'...") = false, want true (punctuation boundary)`)
	}
	if catAllow.matches("category foo") {
		t.Error(`allowlist{"cat"}.matches("category foo") = true, want false (not a word boundary)`)
	}
}

// TestReasonAllowlistMatchesReason pins the --allow-reason substring
// semantics, covering the actual cross-cutting engine classification found
// empirically (pg2-amzvw): a bare `x="$(cmd)"` assignment statement.
func TestReasonAllowlistMatchesReason(t *testing.T) {
	a := reasonAllowlist{"env assignments only"}
	tests := []struct {
		reason string
		want   bool
	}{
		{"env assignments only, no rule has an opinion (nothing is executed)", true},
		{"safe-commands: all commands are safe", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := a.matchesReason(tt.reason); got != tt.want {
			t.Errorf("reasonAllowlist(%v).matchesReason(%q) = %v, want %v", a, tt.reason, got, tt.want)
		}
	}
}

// TestExtractCandidates_PlaceholderVariants pins the two placeholder shapes
// found empirically beyond a bare `<word>` (pg2-amzvw): a multi-word
// free-text placeholder description, and the bare ellipsis placeholder
// `<...>`.
func TestExtractCandidates_PlaceholderVariants(t *testing.T) {
	content := "```bash\n" +
		"PRECONDITION: <observable outcome that must hold before this is workable>\n" +
		"echo <...>\n" +
		"echo real\n" +
		"```\n"
	got := extractCandidates("skill.md", content)
	if len(got) != 1 || got[0].text != "echo real" {
		t.Errorf("got %+v, want exactly one candidate %q", got, "echo real")
	}
}

// TestRun_UsageError pins the exit-code contract's usage-error case: no
// roots given.
func TestRun_UsageError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	got := run([]string{}, &stdout, &stderr)
	if got != 2 {
		t.Errorf("run with no roots => exit %d, want 2", got)
	}
}

// TestRun_MissingRoot pins the exit-code contract's generic-error case: a
// root that does not exist on disk.
func TestRun_MissingRoot(t *testing.T) {
	var stdout, stderr bytes.Buffer
	got := run([]string{"/nonexistent-root-pg2-amzvw"}, &stdout, &stderr)
	if got != 1 {
		t.Errorf("run with a nonexistent root => exit %d, want 1", got)
	}
}
