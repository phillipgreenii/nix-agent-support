package servetest

import (
	"testing"
)

func TestSanitizeMatchesTheScript(t *testing.T) {
	if got := Sanitize(`a = "b" AND c`); got != "a____b__AND_c" {
		t.Errorf("Sanitize = %q", got)
	}
}
