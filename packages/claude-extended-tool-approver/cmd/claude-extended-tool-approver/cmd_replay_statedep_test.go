package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeStateDepCorpus writes a one-row corpus file whose row carries tags.
func writeStateDepCorpus(t *testing.T, tags []string) string {
	t.Helper()
	rows := []map[string]any{{
		"case":             "statedep-test",
		"tags":             tags,
		"tool_input":       map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "true"}},
		"cwd_path_state":   map[string]any{"cwd": "/repo", "project_root": "/repo"},
		"mode":             "default",
		"settings":         map[string]any{},
		"rules_config":     map[string]any{},
		"expected_verdict": "approve",
	}}
	b, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "corpus.json")
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// replayStateDepSection replays the fixture ask log against corpus and returns
// the replay's stdout from the state-dependent heading on, plus the key of the
// fixture's one state-dependent shape.
func replayStateDepSection(t *testing.T, f replayFixture, corpus string) (section string, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	err := runReplay(&out, &errb, replayOpts{
		since: "2026-10-02", db: f.db, shapes: f.shapes, out: f.out, corpus: corpus,
		src: sourceFlags{source: sourceCeta},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, after, ok := strings.Cut(out.String(), "## State-dependent (excluded from gates)\n")
	if !ok {
		t.Fatalf("no state-dependent heading:\n%s", out.String())
	}
	return after, errb.String()
}

func stateDepShapeKey(t *testing.T, f replayFixture) string {
	t.Helper()
	for k, m := range readReplay(t, f.out) {
		if m["state_dependent"] == true {
			return k
		}
	}
	t.Fatal("fixture has no state-dependent shape")
	return ""
}

// TestStateDepReplaySummaryCountsFixtureCoveredShapes: the state-dependent
// section prints "fixture-covered shapes=<m> of <s>", where a shape counts when
// a corpus row carries both the state-dependent tag and shape:<its key>.
func TestStateDepReplaySummaryCountsFixtureCoveredShapes(t *testing.T) {
	f := newReplayFixture(t)

	// Pass 1 with an empty corpus: nothing is covered; also learns the key.
	section, _ := replayStateDepSection(t, f, writeStateDepCorpus(t, []string{"state-dependent"}))
	if !strings.HasPrefix(section, "state-dependent shapes=1 rows=1\nfixture-covered shapes=0 of 1\n") {
		t.Errorf("uncovered shape: section =\n%s", section)
	}
	key := stateDepShapeKey(t, f)

	cases := []struct {
		name string
		tags []string
		want string
	}{
		{"covered", []string{"state-dependent", "family:Write", "shape:" + key}, "fixture-covered shapes=1 of 1\n"},
		{"shape tag without state-dependent tag", []string{"shape:" + key}, "fixture-covered shapes=0 of 1\n"},
		{"other shape", []string{"state-dependent", "shape:tool=Other"}, "fixture-covered shapes=0 of 1\n"},
	}
	for _, c := range cases {
		section, _ := replayStateDepSection(t, f, writeStateDepCorpus(t, c.tags))
		if !strings.Contains(section, c.want) {
			t.Errorf("%s: section lacks %q:\n%s", c.name, c.want, section)
		}
		// The per-shape line still follows the count.
		if !strings.Contains(section, "consulted=filesystem") {
			t.Errorf("%s: per-shape line missing:\n%s", c.name, section)
		}
	}
}

// TestStateDepReplayUnreadableCorpusCoversNothing: a corpus that cannot be read
// is reported on stderr and covers no shape; the replay still succeeds.
func TestStateDepReplayUnreadableCorpusCoversNothing(t *testing.T) {
	f := newReplayFixture(t)
	section, stderr := replayStateDepSection(t, f, filepath.Join(t.TempDir(), "absent.json"))
	if !strings.Contains(section, "fixture-covered shapes=0 of 1\n") {
		t.Errorf("section =\n%s", section)
	}
	if !strings.Contains(stderr, "fixture-covered:") {
		t.Errorf("stderr lacks the corpus warning: %q", stderr)
	}
}
