package feedbackclassify

import "testing"

func TestFingerprintPolicy(t *testing.T) {
	// CI: check+sha → distinct per revision.
	a := Fingerprint("ci-failure", FPParts{CheckName: "build", SubjectSHA: "aaa"})
	b := Fingerprint("ci-failure", FPParts{CheckName: "build", SubjectSHA: "bbb"})
	if a == b {
		t.Fatal("ci-failure fingerprint must differ across SHAs")
	}

	// Code thread: stable across SHAs (path + normalized body).
	c1 := Fingerprint("code-comment-thread", FPParts{File: "x.go", Body: "fix this", SubjectSHA: "aaa"})
	c2 := Fingerprint("code-comment-thread", FPParts{File: "x.go", Body: "fix this", SubjectSHA: "bbb"})
	if c1 != c2 {
		t.Fatal("code-comment-thread fingerprint must be revision-stable")
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
