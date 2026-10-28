package transient

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/phillipgreenii/pg-router/internal/wireclient"
)

// The orchestrator's TestClassifyTransient covers the full table through its
// delegate; this pins the contract metrics relies on (the class names and the
// order unavailable is checked after killed and deadline).
func TestClassify(t *testing.T) {
	cases := []struct {
		name  string
		err   error
		class string
		ok    bool
	}{
		{"nil", nil, "", false},
		{"unavailable text", errors.New(`wireclient: role "pr-change" exited 1: unavailable: tracker`), ClassUnavailable, true},
		{"unavailable is case-insensitive", errors.New(`UNAVAILABLE: daemon not reachable`), ClassUnavailable, true},
		{"killed wins over unavailable", errors.New(`unavailable: scriptout: signal: killed`), ClassKilled, true},
		{"typed exit -1", fmt.Errorf("x: %w", &wireclient.ExitError{Role: "r", Code: -1}), ClassKilled, true},
		{"deadline wins over unavailable", errors.New(`unavailable: deadline exceeded`), ClassDeadline, true},
		{"context deadline", fmt.Errorf("x: %w", context.DeadlineExceeded), ClassDeadline, true},
		{"cancelled is never transient", fmt.Errorf("unavailable: %w", context.Canceled), "", false},
		{"deterministic exit", &wireclient.ExitError{Role: "r", Code: 1, Detail: "invalid_argument: bad"}, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			class, ok := Classify(c.err)
			if class != c.class || ok != c.ok {
				t.Fatalf("Classify(%v) = (%q, %v), want (%q, %v)", c.err, class, ok, c.class, c.ok)
			}
		})
	}
}
