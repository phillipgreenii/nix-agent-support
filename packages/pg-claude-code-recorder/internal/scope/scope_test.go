package scope

import "testing"

func TestMatchAllowAndBlock(t *testing.T) {
	s := New("/c", "/ctl/ends")
	cases := []struct {
		path string
		want bool
		why  string
	}{
		{"/c/projects/p/s.jsonl", true, ""},
		{"/c/projects/p/s.status.jsonl", true, ""},
		{"/c/sessions/123.json", true, ""},
		{"/c/history.jsonl", true, ""},
		{"/ctl/ends/s.1.json", true, ""},
		{"/c/sessions/abc.key", false, "session_key"},
		{"/c/projects/p/.session-mode.tmp.9", false, "atomic_tmp"},
		{"/c/projects/p/x.tmp.1", false, "atomic_tmp"},
		{"/c/projects/.DS_Store", false, "ds_store"},
		{"/c/projects/p/w/node_modules/a/b.js", false, "node_modules"},
		{"/c/projects/p/w/other.js", false, "outside_roots"},
		{"/c/file-history/x", false, "outside_roots"},
		{"/c/paste-cache/x", false, "outside_roots"},
		{"/elsewhere/projects/x", false, "outside_roots"},
		{"/c/settings.json", false, "outside_roots"},
	}
	for _, c := range cases {
		d := s.Match(c.path)
		if d.Ingest != c.want || d.Reason != c.why {
			t.Errorf("Match(%q) = %+v, want ingest=%v reason=%q", c.path, d, c.want, c.why)
		}
	}
}

func TestEndsRootOutsideClaudeDirAndUnconfigured(t *testing.T) {
	if !New("/c", "/ctl/ends").Match("/ctl/ends/a.json").Ingest {
		t.Fatal("ends root outside the Claude dir must be ingested")
	}
	if New("/c", "").Match("/ctl/ends/a.json").Ingest {
		t.Fatal("unconfigured ends root must not ingest")
	}
}

func TestClassify(t *testing.T) {
	s := New("/c", "/ctl/ends")
	cases := []struct {
		path string
		src  string
		mode Mode
	}{
		{"/c/projects/p/s.jsonl", SrcTranscript, Append},
		{"/c/projects/p/s/subagents/agent-1.jsonl", SrcTranscript, Append},
		{"/c/projects/p/s/subagents/agent-1.meta.json", SrcSubagentMD, Snapshot},
		{"/c/projects/p/s/tool-results/r1", SrcToolResult, Snapshot},
		{"/c/history.jsonl", SrcHistory, Append},
		{"/c/sessions/9.json", SrcRegistry, Snapshot},
		{"/c/projects/p/s.status.jsonl", SrcStatus, Append},
		{"/c/projects/p/s.session-mode.json", SrcSessionMode, Snapshot},
		{"/c/projects/p/s.compaction.json", SrcCompaction, Snapshot},
		{"/ctl/ends/s.1.json", SrcControlEnd, Snapshot},
		{"/c/projects/p/workflows/x.json", SrcUnparsed, Snapshot},
		{"/c/projects/p/memory/m.md", SrcUnparsed, Snapshot},
		{"/c/projects/p/sessions-index.json", SrcUnparsed, Snapshot},
		{"/c/projects/p/.orphaned-a.jsonl", SrcUnparsed, Snapshot},
	}
	for _, c := range cases {
		src, mode := s.Classify(c.path)
		if src != c.src || mode != c.mode {
			t.Errorf("Classify(%q) = %s/%s, want %s/%s", c.path, src, mode, c.src, c.mode)
		}
	}
}

func TestQuarantineDirectoryIsBlocked(t *testing.T) {
	s := New("/c", "")
	for _, p := range []string{
		"/c/projects/p/" + QuarantineDir + "/12-s.jsonl",
		"/c/projects/p/s/" + QuarantineDir + "/12-agent.jsonl",
		"/c/projects/p/" + QuarantineDir,
	} {
		if d := s.Match(p); d.Ingest || d.Reason != "quarantine" {
			t.Errorf("Match(%q) = %+v", p, d)
		}
	}
}
