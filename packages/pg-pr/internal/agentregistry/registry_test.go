package agentregistry

import "testing"

func TestIsAgent(t *testing.T) {
	r, err := New([]Entry{{Login: "claude[bot]", ApprovalRegex: `(?im)^verdict:\s*approve`}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if !r.IsAgent("claude[bot]") {
		t.Error("expected claude[bot] to be classified as agent")
	}
	if r.IsAgent("alice") {
		t.Error("expected alice to not be agent")
	}
}

func TestMatchApproval(t *testing.T) {
	r, _ := New([]Entry{{Login: "claude[bot]", ApprovalRegex: `(?im)^verdict:\s*approve`}})
	if !r.MatchApproval("claude[bot]", "Verdict: Approve\nLGTM") {
		t.Error("expected approval match")
	}
	if r.MatchApproval("claude[bot]", "Verdict: request-changes") {
		t.Error("expected no match for non-approve body")
	}
	if r.MatchApproval("alice", "Verdict: Approve") {
		t.Error("expected no match for non-agent author")
	}
}

func TestInvalidRegex(t *testing.T) {
	if _, err := New([]Entry{{Login: "x", ApprovalRegex: "[unclosed"}}); err == nil {
		t.Fatal("expected error on invalid regex")
	}
}
