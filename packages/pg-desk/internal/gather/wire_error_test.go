package gather

import (
	"errors"
	"fmt"
	"testing"
)

func TestErrorCodeReadsTheTypeNotTheText(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"typed not_found with a message about a timeout", NewWireError("not_found", "timed out after 30s"), "not_found"},
		{"typed unavailable", NewWireError("unavailable", "issue not found"), "unavailable"},
		{"wrapped typed error", fmt.Errorf("gather: %w", NewWireError("invalid_argument", "x")), "invalid_argument"},
		{"local deadline wraps the unavailable sentinel", fmt.Errorf("timed out: %w", ErrUnavailable), "unavailable"},
		{"plain error that says not_found", errors.New("not_found: gone"), ""},
		{"typed error with no code", NewWireError("", "exit 1"), ""},
		{"nil", nil, ""},
	}
	for _, c := range cases {
		if got := ErrorCode(c.err); got != c.want {
			t.Errorf("%s: ErrorCode = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestExitErrorCarriesTheWireCodeAndOnlyUnavailableMatchesTheSentinel(t *testing.T) {
	out := []byte(`{"error":{"code":"not_found","message":"no such issue"}}`)
	err := exitError([]string{"issue", "show"}, 1, out)
	if got := ErrorCode(err); got != "not_found" {
		t.Fatalf("ErrorCode = %q", got)
	}
	if errors.Is(err, ErrUnavailable) {
		t.Fatal("not_found matched ErrUnavailable")
	}
	un := exitError(nil, 1, []byte(`{"error":{"code":"unavailable","message":"down"}}`))
	if !errors.Is(un, ErrUnavailable) || ErrorCode(un) != "unavailable" {
		t.Fatalf("unavailable err = %v", un)
	}
}
