package vocab

import (
	"regexp"
	"testing"
)

var lowerSnake = regexp.MustCompile(`^[a-z][a-z0-9]*(_[a-z0-9]+)*$`)

// wireWords are the fixed words of the wire protocol; a registration's own name
// must never be one of them in the second registration.
var wireWords = []string{"added", "removed", "changed", "other", "unknown", "query", "refresh", "write", "read", "settled", "failed", "timeout"}

func TestEverySecondWordDiffersAndIsAValidName(t *testing.T) {
	words := Words()
	seen := map[string]string{}
	for first, second := range words {
		if first == second {
			t.Errorf("%q maps to itself", first)
		}
		if _, isKey := words[second]; isKey {
			t.Errorf("second word %q of %q is itself a first-registration word", second, first)
		}
		if !lowerSnake.MatchString(second) {
			t.Errorf("second word %q of %q is not lower_snake_case", second, first)
		}
		if other, dup := seen[second]; dup {
			t.Errorf("%q and %q both map to %q", first, other, second)
		}
		seen[second] = first
		for _, w := range wireWords {
			if second == w {
				t.Errorf("second word of %q is the wire word %q", first, w)
			}
		}
	}
}

func TestWIsTheIdentityByDefaultAndPanicsOnAnUnknownWordOverTheSecond(t *testing.T) {
	t.Setenv(Env, "")
	if got := W("anything at all"); got != "anything at all" {
		t.Errorf("W = %q, want the word itself", got)
	}
	t.Setenv(Env, Second)
	if !IsSecond() {
		t.Fatal("the second registration is not selected")
	}
	if got, want := W("alpha"), Words()["alpha"]; got != want || got == "alpha" {
		t.Errorf("W(alpha) = %q, want %q", got, want)
	}
	defer func() {
		if recover() == nil {
			t.Error("W of a word the table lacks did not panic")
		}
	}()
	W("not in the table")
}
