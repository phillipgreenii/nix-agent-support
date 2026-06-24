package feedbackclassify

import "testing"

func TestFingerprintPolicy(t *testing.T) {
	// CI: check+sha → distinct per revision.
	a := Fingerprint("ci-failure", FPParts{CheckName: "build", SubjectSHA: "aaa"})
	b := Fingerprint("ci-failure", FPParts{CheckName: "build", SubjectSHA: "bbb"})
	if a == b {
		t.Fatal("ci-failure fingerprint must differ across SHAs")
	}

	// Code thread with ThreadID: stable across SHAs and body changes (keyed on thread id).
	tID1 := Fingerprint("code-comment-thread", FPParts{ThreadID: "thread-xyz", File: "x.go", Body: "fix this", SubjectSHA: "aaa"})
	tID2 := Fingerprint("code-comment-thread", FPParts{ThreadID: "thread-xyz", File: "x.go", Body: "fix this", SubjectSHA: "bbb"})
	if tID1 != tID2 {
		t.Fatal("code-comment-thread with ThreadID must be revision-stable")
	}
	// Body change must not affect thread-id fingerprint.
	tID3 := Fingerprint("code-comment-thread", FPParts{ThreadID: "thread-xyz", File: "x.go", Body: "different body"})
	if tID1 != tID3 {
		t.Fatal("code-comment-thread ThreadID fingerprint must be body-stable (keyed on thread id only)")
	}
	// Different thread id → different fingerprint.
	tID4 := Fingerprint("code-comment-thread", FPParts{ThreadID: "thread-other", File: "x.go", Body: "fix this"})
	if tID1 == tID4 {
		t.Fatal("code-comment-thread fingerprints must differ across distinct ThreadIDs")
	}

	// Code thread without ThreadID: falls back to file+body (legacy / defensive).
	c1 := Fingerprint("code-comment-thread", FPParts{File: "x.go", Body: "fix this", SubjectSHA: "aaa"})
	c2 := Fingerprint("code-comment-thread", FPParts{File: "x.go", Body: "fix this", SubjectSHA: "bbb"})
	if c1 != c2 {
		t.Fatal("code-comment-thread fingerprint (no ThreadID) must be revision-stable via file+body")
	}

	// Body whitespace is normalized (so trivial reflow doesn't churn the fingerprint).
	d1 := Fingerprint("pr-comments", FPParts{Body: "hello   world"})
	d2 := Fingerprint("pr-comments", FPParts{Body: "hello world"})
	if d1 != d2 {
		t.Fatal("body whitespace should be normalized")
	}

	// Output is a non-empty hex digest.
	if len(a) != 64 {
		t.Fatalf("fingerprint should be a 64-char sha256 hex, got %d", len(a))
	}
}
