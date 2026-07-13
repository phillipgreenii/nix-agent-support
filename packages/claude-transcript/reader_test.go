package claudetranscript

import "testing"

func TestLastAssistantText_returnsFinalAssistantText(t *testing.T) {
	got, err := LastAssistantText("testdata/turn.jsonl")
	if err != nil {
		t.Fatalf("LastAssistantText: %v", err)
	}
	if got != "BANANA" {
		t.Errorf("got %q, want BANANA", got)
	}
}

func TestLastAssistantText_emptyWhenNoAssistant(t *testing.T) {
	got, err := LastAssistantText("testdata/awaiting.jsonl")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != "" {
		t.Errorf("got %q, want empty (last assistant turn is a tool_use, no text)", got)
	}
}

func TestIsAwaitingInput_trueForDanglingQuestion(t *testing.T) {
	ok, err := IsAwaitingInput("testdata/awaiting.jsonl")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !ok {
		t.Error("expected awaiting=true for dangling AskUserQuestion")
	}
}

func TestIsAwaitingInput_falseForCompletedTurn(t *testing.T) {
	ok, err := IsAwaitingInput("testdata/turn.jsonl")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if ok {
		t.Error("expected awaiting=false for a completed turn")
	}
}

// Regression: a single assistant turn is written as one JSONL line per content
// block sharing a message id. A dangling AskUserQuestion emitted in an earlier
// line of the turn must not be wiped by a later line of the SAME turn (e.g. a
// trailing text block). Before the turn-folding fix, resetting per JSONL event
// cleared the pending question and reported false "not awaiting".
func TestIsAwaitingInput_trueForDanglingQuestionAcrossMultiEventTurn(t *testing.T) {
	ok, err := IsAwaitingInput("testdata/awaiting-multievent.jsonl")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !ok {
		t.Error("expected awaiting=true for a dangling AskUserQuestion followed by a same-turn event")
	}
}
