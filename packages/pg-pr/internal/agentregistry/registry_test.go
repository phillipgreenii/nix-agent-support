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
	// pg2-4dz88.1.3: a legacy entry (login+approval_regex only, no
	// `approver` key) MUST NOT be implicitly allowlisted just because it
	// compiled a working ApprovalRegex — Approver defaults to false and
	// allowlisting is never implied.
	if e.Approver {
		t.Error("expected legacy entry to decode with Approver=false (zero value)")
	}
	if r.IsApprover("claude[bot]") {
		t.Error("expected legacy entry to NOT be an approver despite a matching ApprovalRegex")
	}
}

// TestEntry_ApproverField verifies the `approver` YAML key round-trips.
func TestEntry_ApproverField(t *testing.T) {
	const src = `
login: approver-one
approval_regex: '(?im)^GEN2-CLEAN$'
approver: true
`
	var e Entry
	if err := yaml.Unmarshal([]byte(src), &e); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !e.Approver {
		t.Error("expected approver: true to decode as Approver=true")
	}
}

// TestIsApprover_SeparateFromIsAgent verifies that approver-allowlist
// membership is a SEPARATE set from agent registration: a login can be a
// registered agent (IsAgent true, comment ingestion unaffected) while
// never counting as an approver (IsApprover false), and the two are
// structurally distinguishable via the two accessors.
func TestIsApprover_SeparateFromIsAgent(t *testing.T) {
	r, err := New([]Entry{
		{Login: "approver-one", ApprovalRegex: `(?im)^GEN2-CLEAN$`, Approver: true},
		{Login: "bot-not-an-approver", ApprovalRegex: `(?im)^GEN2-CLEAN$`, Approver: false},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Both are registered agents; ingestion/IsAgent sees no difference.
	if !r.IsAgent("approver-one") {
		t.Error("expected approver-one to be IsAgent")
	}
	if !r.IsAgent("bot-not-an-approver") {
		t.Error("expected bot-not-an-approver to be IsAgent")
	}

	// Only the allowlisted entry counts as an approver.
	if !r.IsApprover("approver-one") {
		t.Error("expected approver-one to be IsApprover")
	}
	if r.IsApprover("bot-not-an-approver") {
		t.Error("expected bot-not-an-approver to NOT be IsApprover despite being a registered agent")
	}

	// A non-agent login is neither.
	if r.IsAgent("alice") || r.IsApprover("alice") {
		t.Error("expected alice (unregistered) to be neither IsAgent nor IsApprover")
	}

	// Both entries still match approval bodies via the pre-existing
	// approval-regex machinery — Approver gates a DIFFERENT axis
	// (allowlist membership), not comment-body matching.
	if !r.MatchApproval("bot-not-an-approver", "GEN2-CLEAN") {
		t.Error("expected bot-not-an-approver's ApprovalRegex matching to be unaffected by Approver=false")
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
