package agentregistry

import (
	"testing"

	"gopkg.in/yaml.v3"
)

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

// TestEntry_YAML verifies that the extended Entry fields round-trip from YAML.
func TestEntry_YAML(t *testing.T) {
	const src = `
login: coderabbitai[bot]
agent_name: coderabbit
body_marker: "<!-- This is an auto-generated comment"
policy:
  ingest: true
  managed_upstream: true
  default_severity: low
`
	var e Entry
	if err := yaml.Unmarshal([]byte(src), &e); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if e.Login != "coderabbitai[bot]" {
		t.Errorf("login: got %q", e.Login)
	}
	if e.AgentName != "coderabbit" {
		t.Errorf("agent_name: got %q", e.AgentName)
	}
	if e.BodyMarker != "<!-- This is an auto-generated comment" {
		t.Errorf("body_marker: got %q", e.BodyMarker)
	}
	if !e.Policy.Ingest {
		t.Error("expected policy.ingest=true")
	}
	if !e.Policy.ManagedUpstream {
		t.Error("expected policy.managed_upstream=true")
	}
	if e.Policy.DefaultSeverity != "low" {
		t.Errorf("default_severity: got %q", e.Policy.DefaultSeverity)
	}
}

// TestEntry_LegacyYAML verifies a legacy entry (login+approval_regex only) still loads and works.
func TestEntry_LegacyYAML(t *testing.T) {
	const src = `
login: claude[bot]
approval_regex: '(?im)^verdict:\s*approve'
`
	var e Entry
	if err := yaml.Unmarshal([]byte(src), &e); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	r, err := New([]Entry{e})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if !r.IsAgent("claude[bot]") {
		t.Error("expected legacy entry to still be IsAgent")
	}
	if !r.MatchApproval("claude[bot]", "Verdict: Approve\nLGTM") {
		t.Error("expected legacy approval regex to still match")
	}
}

// TestNew_EmptyApprovalRegex verifies that an entry with no ApprovalRegex is accepted.
func TestNew_EmptyApprovalRegex(t *testing.T) {
	r, err := New([]Entry{{Login: "coderabbitai[bot]", AgentName: "coderabbit"}})
	if err != nil {
		t.Fatalf("New with empty ApprovalRegex: %v", err)
	}
	if !r.IsAgent("coderabbitai[bot]") {
		t.Error("expected bot-only entry to be IsAgent")
	}
	if r.MatchApproval("coderabbitai[bot]", "some body") {
		t.Error("expected MatchApproval=false for entry with no ApprovalRegex")
	}
}

// TestPolicyFor verifies PolicyFor returns the right policy.
func TestPolicyFor(t *testing.T) {
	e := Entry{
		Login:     "coderabbitai[bot]",
		AgentName: "coderabbit",
		Policy: Policy{
			Ingest:          true,
			ManagedUpstream: true,
			DefaultSeverity: "low",
		},
	}
	r, err := New([]Entry{e})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	pol, ok := r.PolicyFor("coderabbitai[bot]")
	if !ok {
		t.Fatal("expected PolicyFor to return ok=true")
	}
	if !pol.Ingest {
		t.Error("expected Ingest=true")
	}
	if !pol.ManagedUpstream {
		t.Error("expected ManagedUpstream=true")
	}
	if pol.DefaultSeverity != "low" {
		t.Errorf("DefaultSeverity: got %q", pol.DefaultSeverity)
	}
	if _, ok := r.PolicyFor("unknown"); ok {
		t.Error("expected PolicyFor to return ok=false for unknown login")
	}
}

// TestToClassifyRegistry verifies the adapter produces correct Classify results.
func TestToClassifyRegistry(t *testing.T) {
	e := Entry{
		Login:      "coderabbitai[bot]",
		AgentName:  "coderabbit",
		BodyMarker: "",
		Policy: Policy{
			ManagedUpstream: true,
			DefaultSeverity: "low",
		},
	}
	r, err := New([]Entry{e})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cr := r.ToClassifyRegistry()
	author := cr.Classify("coderabbitai[bot]", "Bot", "x", "self")
	if author.AgentName != "coderabbit" {
		t.Errorf("AgentName: got %q want %q", author.AgentName, "coderabbit")
	}
	if author.Kind != "agent" {
		t.Errorf("Kind: got %q want agent", author.Kind)
	}
	if !author.ManagedUpstream {
		t.Error("expected ManagedUpstream=true")
	}
	if author.DefaultSeverity != "low" {
		t.Errorf("DefaultSeverity: got %q want low", author.DefaultSeverity)
	}
}
