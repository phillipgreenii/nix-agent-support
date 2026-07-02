package feedbackclassify

import "testing"

// self-review fingerprints are HEAD-scoped: a re-run of the reviewer at the same
// head + same finding dedups (idempotent), but a re-review at a new head SHA is
// a new finding. Keyed on (subject_sha, file, line, normalized body).
func TestFingerprint_SelfReview_HeadScoped(t *testing.T) {
	// Same head + same finding text → identical (re-run dedups).
	a := Fingerprint("self-review", FPParts{SubjectSHA: "h1", File: "x.go", Line: 10, Body: "prefer errors.Is"})
	b := Fingerprint("self-review", FPParts{SubjectSHA: "h1", File: "x.go", Line: 10, Body: "prefer errors.Is"})
	if a != b {
		t.Fatal("same head + same finding must produce the same fingerprint (idempotent re-run)")
	}

	// New head SHA, same finding → new fingerprint (re-review is a new finding).
	c := Fingerprint("self-review", FPParts{SubjectSHA: "h2", File: "x.go", Line: 10, Body: "prefer errors.Is"})
	if a == c {
		t.Fatal("self-review fingerprint must differ across head SHAs (re-review = new finding)")
	}

	// Distinct findings at the same head are distinct (body differs).
	d := Fingerprint("self-review", FPParts{SubjectSHA: "h1", File: "x.go", Line: 10, Body: "add a test"})
	if a == d {
		t.Fatal("distinct self-review findings at the same head must have distinct fingerprints")
	}

	// A fileless PR-level finding (Body only) is stable per head.
	e1 := Fingerprint("self-review", FPParts{SubjectSHA: "h1", Body: "overall LGTM but split the PR"})
	e2 := Fingerprint("self-review", FPParts{SubjectSHA: "h1", Body: "overall LGTM but split the PR"})
	if e1 != e2 {
		t.Fatal("fileless self-review finding must be stable per head")
	}
	if e1 == a {
		t.Fatal("a PR-level finding must differ from an inline finding")
	}

	// Body whitespace is normalized like the other kinds.
	f1 := Fingerprint("self-review", FPParts{SubjectSHA: "h1", Body: "hello   world"})
	f2 := Fingerprint("self-review", FPParts{SubjectSHA: "h1", Body: "hello world"})
	if f1 != f2 {
		t.Fatal("self-review body whitespace should be normalized")
	}
}
