package main

import (
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
		"$ bd show <id>\n" +
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

	want := []string{
		"bd show <id>",
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

	// The first candidate's line number must point at the `$ bd show <id>`
	// line (1-based), not at the fence or the dropped comment/blank lines
	// before it.
	if got[0].line != 8 {
		t.Errorf("candidate 0 line = %d, want 8 (the `$ bd show <id>` line)", got[0].line)
	}
}

func TestExtractCandidates_NoFencedBlocks(t *testing.T) {
	content := "Just prose with an inline `bd show <id>` backtick span and no fenced block.\n"
	got := extractCandidates("skill.md", content)
	if len(got) != 0 {
		t.Errorf("got %d candidates from a file with no fenced bash/sh/shell/zsh block, want 0: %+v", len(got), got)
	}
}
