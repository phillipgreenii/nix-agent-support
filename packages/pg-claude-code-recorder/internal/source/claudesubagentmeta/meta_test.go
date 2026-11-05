package claudesubagentmeta

import "testing"

func TestMatchWorkflowSubagentMeta(t *testing.T) {
	s := New()
	c, ok := s.Match("projects/p/sess/subagents/workflows/wf_ab-12/agent-a1.meta.json")
	if !ok || c["workflow"] != "wf_ab-12" || c["agent"] != "a1" || c["session"] != "sess" {
		t.Fatalf("captures %v ok=%v", c, ok)
	}
	if c, ok := s.Match("projects/p/sess/subagents/agent-a1.meta.json"); !ok || c["workflow"] != "" {
		t.Fatalf("plain %v ok=%v", c, ok)
	}
	for _, p := range []string{
		"projects/p/sess/subagents/workflows/other/agent-a1.meta.json",
		"projects/p/sess/subagents/workflows/wf_1/x/agent-a1.meta.json",
		"projects/p/sess/subagents/workflows/agent-a1.meta.json",
		"projects/p/sess/subagents/workflows/wf_1/journal.jsonl",
	} {
		if _, ok := s.Match(p); ok {
			t.Errorf("Match(%q) accepted", p)
		}
	}
	c, _ = s.Match("projects/p/sess/subagents/workflows/wf_ab-12/agent-a1.meta.json")
	recs, err := s.Parse(c, []byte(`{"agentType":"general-purpose","spawnDepth":1}`))
	if err != nil || len(recs) != 1 {
		t.Fatalf("recs %v err %v", recs, err)
	}
	found := false
	for _, f := range recs[0].Fields {
		if f.Name == "workflow_id" && f.S == "wf_ab-12" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no workflow_id in %v", recs[0].Fields)
	}
}
