package transcript

import "testing"

func TestIsAwaitingInputTrue(t *testing.T) {
	path := t.TempDir() + "/t.jsonl"
	body := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"auq_1","name":"AskUserQuestion","input":{}}]}}` + "\n"
	if err := writeTestFile(path, body); err != nil {
		t.Fatal(err)
	}
	got, err := IsAwaitingInput(path)
	if err != nil {
		t.Fatal(err)
	}
	if !got {
		t.Error("IsAwaitingInput = false, want true")
	}
}

func TestIsAwaitingInputFalseAfterResult(t *testing.T) {
	path := t.TempDir() + "/t.jsonl"
	body := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"auq_1","name":"AskUserQuestion","input":{}}]}}` + "\n" +
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"auq_1","content":"user answered"}]}}` + "\n"
	if err := writeTestFile(path, body); err != nil {
		t.Fatal(err)
	}
	got, err := IsAwaitingInput(path)
	if err != nil {
		t.Fatal(err)
	}
	if got {
		t.Error("IsAwaitingInput = true, want false (result already received)")
	}
}

func TestIsAwaitingInputFalseNoAUQ(t *testing.T) {
	path := t.TempDir() + "/t.jsonl"
	body := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"done"}]}}` + "\n"
	if err := writeTestFile(path, body); err != nil {
		t.Fatal(err)
	}
	got, err := IsAwaitingInput(path)
	if err != nil {
		t.Fatal(err)
	}
	if got {
		t.Error("IsAwaitingInput = true, want false (no AskUserQuestion)")
	}
}
