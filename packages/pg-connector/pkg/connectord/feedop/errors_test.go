package feedop

import (
	"errors"
	"fmt"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/feed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

func TestMapError(t *testing.T) {
	expired := &feed.CursorExpiredError{Message: "cursor_expired: the position 3 is outside the log; re-run with --reset"}
	conflict := &feed.KeyConflictError{Existing: "(c, flagged, mine)", Message: "the cursor (c, flagged, mine) exists; one consumer may not hold both"}
	auth := errors.New("example credentials refused")
	cases := []struct {
		name    string
		err     error
		code    string
		message string // when set, the exact message
	}{
		{"cursor expired", expired, "invalid_argument", expired.Message},
		{"cursor conflict", conflict, "invalid_argument", conflict.Message},
		{"ack beyond head", fmt.Errorf("%w: next_seq 9, head 3", feed.ErrAckBeyondHead), "invalid_argument", ""},
		{"unknown query", fmt.Errorf("%w: %q is not a configured query", feed.ErrQueryNotRecognized, "x"), "query_not_recognized", ""},
		{"governor refusal", fmt.Errorf("run: %w", &governor.Refusal{Reason: "the core cap is spent"}), "unavailable", "changes: the core cap is spent"},
		{"authentication failure", fmt.Errorf("origin: %w", auth), "unauthenticated", ""},
		{"anything else", errors.New("boom"), "unavailable", "changes: boom"},
		{"a wire error passes", scriptout.WrapError(scriptout.ErrNotFound, "no such thing"), "not_found", ""},
	}
	for _, c := range cases {
		got := MapError("changes", c.err, []error{auth})
		if code := scriptout.CodeForError(got); code != c.code {
			t.Errorf("%s: code = %s (%v), want %s", c.name, code, got, c.code)
		}
		if c.message != "" && got.Error() != c.message {
			t.Errorf("%s: message = %q, want %q", c.name, got.Error(), c.message)
		}
		if !errors.Is(got, c.err) {
			t.Errorf("%s: the mapped error does not match the original", c.name)
		}
	}
	if MapError("changes", nil, nil) != nil {
		t.Error("MapError(nil) is not nil")
	}
}

func TestWithoutASentinelAnAuthenticationFailureIsUnavailable(t *testing.T) {
	err := errors.New("example credentials refused")
	if code := scriptout.CodeForError(MapError("changes", err, nil)); code != "unavailable" {
		t.Fatalf("code = %s", code)
	}
}
