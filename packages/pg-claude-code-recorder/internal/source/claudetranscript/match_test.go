package claudetranscript

import "testing"

func TestMatchWorkflowSubagent(t *testing.T) {
	s := New()
	c, ok := s.Match("projects/p/sess/subagents/workflows/wf_ab-12/agent-a1.jsonl")
	if !ok || c["project"] != "p" || c["session"] != "sess" || c["workflow"] != "wf_ab-12" || c["agent"] != "a1" {
		t.Fatalf("captures %v ok=%v", c, ok)
	}
	c, ok = s.Match("projects/p/sess/subagents/agent-a1.jsonl")
	if !ok || c["workflow"] != "" || c["agent"] != "a1" {
		t.Fatalf("plain subagent %v ok=%v", c, ok)
	}
	for _, p := range []string{
		"projects/p/sess/subagents/workflows/wf_ab-12/journal.jsonl",
		"projects/p/sess/subagents/workflows/agent-a1.jsonl",
		"projects/p/sess/subagents/workflows/other/agent-a1.jsonl",
		"projects/p/sess/subagents/workflows/wf_1/deeper/agent-a1.jsonl",
		"projects/p/sess/workflows/wf_1/agent-a1.jsonl",
	} {
		if _, ok := s.Match(p); ok {
			t.Errorf("Match(%q) accepted", p)
		}
	}
}

func TestParseCarriesWorkflowID(t *testing.T) {
	s := New()
	c, _ := s.Match("projects/p/sess/subagents/workflows/wf_ab-12/agent-a1.jsonl")
	recs, err := s.Parse(c, []byte(`{"type":"user","sessionId":"sess","message":{"role":"user","content":"hi"}}`))
	if err != nil || len(recs) == 0 {
		t.Fatalf("recs %v err %v", recs, err)
	}
	got := map[string]string{}
	for _, f := range recs[0].Fields {
		got[f.Name] = f.S
	}
	if got["workflow_id"] != "wf_ab-12" || got["agent_id"] != "a1" {
		t.Fatalf("fields %v", got)
	}
	if _, ok := s.Catalog()["workflow_id"]; !ok {
		t.Fatal("catalog lacks workflow_id")
	}
}
