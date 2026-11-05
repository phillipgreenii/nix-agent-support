package scope

import "testing"

const (
	exUUID  = "0f8fad5b-d9cb-469f-a165-70867728950e"
	exUUID2 = "1a8fad5b-d9cb-469f-a165-70867728950f"
)

func TestShapeAllowlist(t *testing.T) {
	s := New("/c", "/ctl/ends")
	p := "/c/projects/example-project/"
	files := []struct {
		path string
		want bool
	}{
		{"/c/history.jsonl", true},
		{"/c/sessions/1.json", true},
		{p + exUUID + ".jsonl", true},
		{p + exUUID + ".status.jsonl", true},
		{p + exUUID + ".session-mode.json", true},
		{p + exUUID + ".compaction.json", true},
		{p + exUUID + "/subagents/agent-1.jsonl", true},
		{p + exUUID + "/subagents/agent-1.meta.json", true},
		{p + exUUID + "/tool-results/r1.txt", true},
		{p + exUUID + "/tool-results/deep/r2.txt", true},
		{p + exUUID + "/workflows/w.json", true},
		{p + exUUID + "/subagents/workflows/wf_1/agent-1.jsonl", true},
		{p + exUUID + "/subagents/workflows/wf_1/agent-1.meta.json", true},
		{"/ctl/ends/s.1.json", true},
		// rejected classes
		{p + "sessions-index.json", false},
		{p + ".orphaned-a.jsonl", false},
		{p + "notes.md", false},
		{p + "spikes-example/a.json", false},
		{p + "memory/m.md", false},
		{p + "artifacts/a.bin", false},
		{p + "not-a-uuid/subagents/agent-1.jsonl", false},
		{p + exUUID + "/other/x.json", false},
		{p + exUUID + "/scratchpad/x.md", false},
		{p + exUUID + "/scratchpad/deep/y.json", false},
		{p + exUUID + "/loose.json", false},
		{"/c/projects/loose.jsonl", false},
		{"/c/file-history/x", false},
		{"/c/plugins/a/b", false},
		{"/c/paste-cache/x", false},
		{"/c/security/x", false},
		{"/c/settings.json", false},
	}
	for _, c := range files {
		if got := s.Match(c.path).Ingest; got != c.want {
			t.Errorf("Match(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}

// Every directory Match could need must be descended, and nothing outside the
// allowlist may be: DescendDir and Match share one shape table.
func TestDescendDir(t *testing.T) {
	s := New("/c", "/ctl/ends")
	p := "/c/projects/example-project"
	cases := []struct {
		dir  string
		want bool
	}{
		{"/c", false},
		{"/c/projects", true},
		{"/c/sessions", true},
		{p, true},
		{p + "/" + exUUID, true},
		{p + "/" + exUUID + "/subagents", true},
		{p + "/" + exUUID + "/subagents/workflows/wf_1", true},
		{p + "/" + exUUID + "/tool-results", true},
		{p + "/" + exUUID + "/tool-results/deep", true},
		{p + "/" + exUUID + "/scratchpad", false},
		{p + "/" + exUUID + "/scratchpad/deep", false},
		{p + "/" + exUUID + "/workflows", true},
		{p + "/" + exUUID + "/other", false},
		{p + "/spikes-example", false},
		{p + "/memory", false},
		{p + "/artifacts", false},
		{p + "/" + exUUID + "/subagents/node_modules", false},
		{p + "/" + exUUID + "/subagents/" + QuarantineDir, false},
		{"/c/file-history", false},
		{"/c/plugins", false},
		{"/c/paste-cache", false},
		{"/ctl/ends", true},
		{"/elsewhere", false},
	}
	for _, c := range cases {
		if got := s.DescendDir(c.dir); got != c.want {
			t.Errorf("DescendDir(%q) = %v, want %v", c.dir, got, c.want)
		}
	}
}

// Behavior for every path ingested today as a non-unparsed source is unchanged.
func TestMatchKeepsEveryConsumedKind(t *testing.T) {
	s := New("/c", "/ctl/ends")
	for _, path := range []string{
		"/c/projects/p/s.jsonl", // a non-UUID stem is still a transcript
		"/c/projects/p/s.status.jsonl",
		"/c/projects/p/s.session-mode.json",
		"/c/projects/p/s.compaction.json",
		"/c/projects/p/" + exUUID2 + "/subagents/agent-1.jsonl",
		"/c/projects/p/" + exUUID2 + "/tool-results/r1",
		"/c/history.jsonl",
		"/c/sessions/9.json",
	} {
		if src, _ := s.Classify(path); src == SrcUnparsed {
			t.Fatalf("fixture %q is unparsed", path)
		}
		if !s.Match(path).Ingest {
			t.Errorf("Match(%q) rejects a consumed kind", path)
		}
	}
}

func TestSweepTargets(t *testing.T) {
	s := New("/c", "/ctl/ends")
	d := s.SweepDirs()
	if len(d) != 3 || d[0] != "/c/projects" || d[1] != "/c/sessions" || d[2] != "/ctl/ends" {
		t.Fatalf("SweepDirs %v", d)
	}
	if f := s.SweepFiles(); len(f) != 1 || f[0] != "/c/history.jsonl" {
		t.Fatalf("SweepFiles %v", f)
	}
	if d := New("/c", "").SweepDirs(); len(d) != 2 {
		t.Fatalf("no ends: %v", d)
	}
}

func BenchmarkMatchRejectedHint(b *testing.B) {
	s := New("/c", "")
	for i := 0; i < b.N; i++ {
		s.Match("/c/file-history/abc/def/ghi.txt")
	}
}
