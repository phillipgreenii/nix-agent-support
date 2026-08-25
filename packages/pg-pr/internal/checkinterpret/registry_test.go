package checkinterpret

import "testing"

func TestClaimMatchingAndNonMatching(t *testing.T) {
	reg := New([]Interpreter{{Patterns: []string{"^approval-gate"}, Type: ApprovalGateType}})

	if typ, ok := reg.Claim("approval-gate: team-a"); !ok || typ != ApprovalGateType {
		t.Errorf("Claim(matching) = %q, %v; want %q, true", typ, ok, ApprovalGateType)
	}
	if typ, ok := reg.Claim("check-a"); ok {
		t.Errorf("Claim(non-matching) = %q, true; want unclaimed", typ)
	}
}

// TestClaimUnclaimedFallback is the mandatory unknown-check-fallback proof
// pg2-4dz88.2.4 exists for: a check/status name no configured interpreter
// claims must be reported distinctly (ok=false) from any interpreted
// Result, so a future integration point (pg2-4dz88.2.6, out of scope
// here) can safely fall through unchanged to cirollup.Classify's existing
// behavior for that name. This test asserts only THIS package's own
// Claim/Classify contract — it deliberately does not import
// internal/cirollup to compare against Classify's live behavior, which
// would create a new inter-package dependency solely for this test.
func TestClaimUnclaimedFallback(t *testing.T) {
	reg := New([]Interpreter{{Patterns: []string{"^approval-gate"}, Type: ApprovalGateType}})

	if typ, ok := reg.Claim("check-a"); ok {
		t.Fatalf("Claim(%q) = %q, true; want unclaimed", "check-a", typ)
	}

	result, ok := reg.Classify("check-a", "success", "")
	if ok {
		t.Fatalf("Classify(unclaimed name) ok = true, want false")
	}
	if result != (Result{}) {
		t.Errorf("Classify(unclaimed name) result = %+v, want zero Result (distinct from any interpreted Result)", result)
	}
}

// TestClaimPrecedenceFirstDeclaredWins pins the deterministic-precedence
// requirement using two DIFFERENT interpreter Types that could both claim
// one name, so the winning Type is observable regardless of which Type
// this package happens to dispatch — proving the precedence rule itself,
// not an accident of ApprovalGateType being "special".
func TestClaimPrecedenceFirstDeclaredWins(t *testing.T) {
	name := "gate-bot: rule-a"

	firstWins := New([]Interpreter{
		{Patterns: []string{"gate-bot"}, Type: ApprovalGateType},
		{Patterns: []string{"gate-bot"}, Type: "other-type"},
	})
	if typ, ok := firstWins.Claim(name); !ok || typ != ApprovalGateType {
		t.Errorf("Claim(%q) = %q, %v; want first-declared %q, true", name, typ, ok, ApprovalGateType)
	}

	reversed := New([]Interpreter{
		{Patterns: []string{"gate-bot"}, Type: "other-type"},
		{Patterns: []string{"gate-bot"}, Type: ApprovalGateType},
	})
	if typ, ok := reversed.Claim(name); !ok || typ != "other-type" {
		t.Errorf("Claim(%q) = %q, %v; want first-declared %q, true", name, typ, ok, "other-type")
	}
}

// TestNewSkipsInvalidPattern is the successor to
// cirollup_test.go's TestNewExcluderSkipsInvalid: an invalid pattern is
// warn-and-skip, not fatal, and a later valid pattern in the same
// declaration still matches.
func TestNewSkipsInvalidPattern(t *testing.T) {
	reg := New([]Interpreter{{Patterns: []string{"(unclosed", "^gate-bot"}, Type: ApprovalGateType}})
	if typ, ok := reg.Claim("gate-bot: rule-a"); !ok || typ != ApprovalGateType {
		t.Errorf("valid pattern after invalid one should still claim; got %q, %v", typ, ok)
	}
}

// TestEmptyPatternListClaimsNothing pins the explicit acceptance criterion
// that an interpreter configured with an empty pattern list claims
// nothing, not everything.
func TestEmptyPatternListClaimsNothing(t *testing.T) {
	reg := New([]Interpreter{{Type: ApprovalGateType}}) // no Patterns at all
	if typ, ok := reg.Claim("check-a"); ok {
		t.Errorf("Claim(%q) = %q, true; want unclaimed for an empty pattern list", "check-a", typ)
	}
}

// TestNilAndEmptyRegistrySafe is the successor to
// cirollup_test.go's TestClassifyNilExcluder: a nil or empty registry
// behaves as "no interpreters configured" — claims nothing, panics never.
func TestNilAndEmptyRegistrySafe(t *testing.T) {
	var nilReg *Registry
	if typ, ok := nilReg.Claim("check-a"); ok {
		t.Errorf("nil registry Claim(%q) = %q, true; want unclaimed", "check-a", typ)
	}
	if result, ok := nilReg.Classify("check-a", "success", ""); ok || result != (Result{}) {
		t.Errorf("nil registry Classify(%q) = %+v, %v; want zero Result, false", "check-a", result, ok)
	}

	for _, reg := range []*Registry{New(nil), New([]Interpreter{})} {
		if typ, ok := reg.Claim("check-a"); ok {
			t.Errorf("empty registry Claim(%q) = %q, true; want unclaimed", "check-a", typ)
		}
	}
}
