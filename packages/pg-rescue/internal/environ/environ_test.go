package environ

import "testing"

func TestLookupLastEntryWinsAndMissingIsEmpty(t *testing.T) {
	env := []string{"A=1", "B=", "A=2", "C=x=y", "NOEQUALS"}
	for key, want := range map[string]string{"A": "2", "B": "", "C": "x=y", "NOEQUALS": "", "MISSING": ""} {
		if got := Lookup(env, key); got != want {
			t.Errorf("Lookup(%q) = %q, want %q", key, got, want)
		}
	}
	if got := Getenv(env)("A"); got != "2" {
		t.Errorf("Getenv(env)(A) = %q, want 2", got)
	}
}
