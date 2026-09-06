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

func TestCodeForError_AllSixSentinels(t *testing.T) {
	cases := []struct {
		sentinel error
		code     string
	}{
		{ErrNotFound, "not_found"},
		{ErrUnauthenticated, "unauthenticated"},
		{ErrUnavailable, "unavailable"},
		{ErrUnknownOp, "unknown_op"},
		{ErrVersionMismatch, "version_mismatch"},
		{ErrInvalidArgument, "invalid_argument"},
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
	codes := []string{"not_found", "unauthenticated", "unavailable", "unknown_op", "version_mismatch", "invalid_argument"}
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

// TestExitCodeForError_AllSixSentinels_AreDistinctAndAtLeastTwo proves the
// backend-process exit code (bead pg2-7vgn5) satisfies the workspace's
// code-file-standards convention: every sentinel-backed error gets a
// distinct exit code >=2 (never the generic-reserved 1, never 0), and the
// scheme carries at least the required two distinct branchable meanings —
// here, six.
func TestExitCodeForError_AllSixSentinels_AreDistinctAndAtLeastTwo(t *testing.T) {
	sentinels := []error{
		ErrNotFound, ErrUnauthenticated, ErrUnavailable,
		ErrUnknownOp, ErrVersionMismatch, ErrInvalidArgument,
	}
	seen := map[int]error{}
	for _, s := range sentinels {
		code := ExitCodeForError(WrapError(s, "detail"))
		if code < 2 {
			t.Fatalf("ExitCodeForError(wrap(%v)) = %d, want >=2", s, code)
		}
		if prior, dup := seen[code]; dup {
			t.Fatalf("exit code %d assigned to both %v and %v, want distinct codes", code, prior, s)
		}
		seen[code] = s
	}
	if len(seen) != len(sentinels) {
		t.Fatalf("got %d distinct exit codes for %d sentinels, want one each", len(seen), len(sentinels))
	}
}

// TestExitCodeForError_MatchesCodeForError proves ExitCodeForError and the
// JSON body's error.code (codeForError) can never disagree: both derive
// their classification the same way, for every sentinel and for a plain
// unwrapped error.
func TestExitCodeForError_MatchesCodeForError(t *testing.T) {
	errs := []error{
		WrapError(ErrNotFound, "x"),
		WrapError(ErrUnauthenticated, "x"),
		WrapError(ErrUnavailable, "x"),
		WrapError(ErrUnknownOp, "x"),
		WrapError(ErrVersionMismatch, "x"),
		WrapError(ErrInvalidArgument, "x"),
		errors.New("plain, unwrapped"),
	}
	for _, err := range errs {
		code := codeForError(err)
		if got, want := ExitCodeForError(err), ExitCodeForCode(code); got != want {
			t.Errorf("ExitCodeForError(%v) = %d, but ExitCodeForCode(%q) = %d", err, got, code, want)
		}
	}
}

// TestExitCodeForError_UnwrappedFallsBackToUnavailablesCode proves an
// unwrapped/unclassified error still gets a real, non-1, non-0 exit code
// via codeForError's own "unavailable" fallback — not the old flat 1.
func TestExitCodeForError_UnwrappedFallsBackToUnavailablesCode(t *testing.T) {
	if got, want := ExitCodeForError(errors.New("boom")), ExitCodeForCode("unavailable"); got != want {
		t.Fatalf("ExitCodeForError(plain) = %d, want %d (unavailable)", got, want)
	}
}

// TestExitCodeForCode_UnknownCodeReturnsZero proves an unrecognized code
// string (should not occur against the closed six-value taxonomy) returns
// the zero value rather than colliding with a real assignment (1-7).
func TestExitCodeForCode_UnknownCodeReturnsZero(t *testing.T) {
	if got := ExitCodeForCode("something_new"); got != 0 {
		t.Fatalf("ExitCodeForCode(unknown) = %d, want 0", got)
	}
}
