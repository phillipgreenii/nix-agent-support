package labels

import "testing"

func TestCardinalityCap_PassesThroughUnderLimit(t *testing.T) {
	c := NewCardinalityCap(3)
	for _, v := range []string{"a", "b", "c"} {
		if got := c.Cap("workspace.repo", v); got != v {
			t.Errorf("Cap(%q) = %q, want passthrough", v, got)
		}
	}
}

func TestCardinalityCap_OverflowBucketsAsOther(t *testing.T) {
	c := NewCardinalityCap(2)
	c.Cap("workspace.repo", "a")
	c.Cap("workspace.repo", "b")
	if got := c.Cap("workspace.repo", "c"); got != "other" {
		t.Errorf("third value should bucket: got %q, want other", got)
	}
}

func TestCardinalityCap_DifferentKeysIndependent(t *testing.T) {
	c := NewCardinalityCap(1)
	c.Cap("workspace.repo", "a")
	if got := c.Cap("agent.role", "x"); got != "x" {
		t.Errorf("different key should be independent, got %q", got)
	}
}

func TestCardinalityCap_RepeatedValuePassesAfterAdmission(t *testing.T) {
	c := NewCardinalityCap(1)
	c.Cap("k", "a")
	if got := c.Cap("k", "a"); got != "a" {
		t.Errorf("repeat = %q, want pass", got)
	}
}
