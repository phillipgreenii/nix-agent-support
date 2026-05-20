package transcript

import "testing"

func TestOpenSubagentsFromFixture(t *testing.T) {
	n, err := OpenSubagents("../../tests/fixtures/transcripts/basic.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	// fixture: Task tool_use id tu_1, followed by tool_result with tool_use_id tu_1 → closed
	if n != 0 {
		t.Errorf("OpenSubagents = %d, want 0", n)
	}
}

func TestOpenSubagentsUnclosed(t *testing.T) {
	path := t.TempDir() + "/t.jsonl"
	body := `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"a","name":"Task"}]}}
{"type":"assistant","message":{"content":[{"type":"tool_use","id":"b","name":"Task"}]}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"a","content":"ok"}]}}
`
	if err := writeTestFile(path, body); err != nil {
		t.Fatal(err)
	}
	n, err := OpenSubagents(path)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("OpenSubagents = %d, want 1 (b still open)", n)
	}
}
