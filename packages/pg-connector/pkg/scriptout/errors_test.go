package scriptout

import (
	"errors"
	"testing"
)

func TestWrapError_ErrorsIs(t *testing.T) {
	err := WrapError(ErrNotFound, "no such pr")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected errors.Is(err, ErrNotFound), got %v", err)
	}
	if errors.Is(err, ErrUnavailable) {
		t.Fatalf("did not expect errors.Is(err, ErrUnavailable)")
	}
}

func TestCodeForError_AllFiveSentinels(t *testing.T) {
	cases := []struct {
		sentinel error
		code     string
	}{
		{ErrNotFound, "not_found"},
		{ErrUnauthenticated, "unauthenticated"},
		{ErrUnavailable, "unavailable"},
		{ErrUnknownOp, "unknown_op"},
		{ErrVersionMismatch, "version_mismatch"},
	}
	for _, c := range cases {
		wrapped := WrapError(c.sentinel, "detail")
		if got := codeForError(wrapped); got != c.code {
			t.Errorf("codeForError(wrap(%v)) = %q, want %q", c.sentinel, got, c.code)
		}
	}
}

func TestCodeForError_UnwrappedFallsBackToUnavailable(t *testing.T) {
	if got := codeForError(errors.New("some plain error")); got != "unavailable" {
		t.Fatalf("codeForError(plain) = %q, want %q", got, "unavailable")
	}
}

func TestSentinelForCode_RoundTrip(t *testing.T) {
	codes := []string{"not_found", "unauthenticated", "unavailable", "unknown_op", "version_mismatch"}
	for _, code := range codes {
		sentinel := sentinelForCode(code)
		if codeForError(sentinel) != code {
			t.Errorf("sentinelForCode(%q) round trip: got code %q", code, codeForError(sentinel))
		}
	}
}

func TestSentinelForCode_UnknownFallsBackToUnavailable(t *testing.T) {
	if got := sentinelForCode("something_new"); !errors.Is(got, ErrUnavailable) {
		t.Fatalf("sentinelForCode(unknown) = %v, want ErrUnavailable", got)
	}
}
