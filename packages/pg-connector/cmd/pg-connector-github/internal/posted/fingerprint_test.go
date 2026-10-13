package posted

import (
	"reflect"
	"strings"
	"testing"
)

func TestPointFingerprint_LFAndCRLFAgree(t *testing.T) {
	lf := PointFingerprint("a/b.go", "RIGHT", 10, "line one\nline two\n")
	crlf := PointFingerprint("a/b.go", "RIGHT", 10, "line one\r\nline two\r\n")
	if lf != crlf {
		t.Fatalf("LF %s != CRLF %s", lf, crlf)
	}
	if len(lf) != FingerprintLen {
		t.Fatalf("len = %d, want %d", len(lf), FingerprintLen)
	}
}

func TestPointFingerprint_TrailingWhitespaceTrimmed(t *testing.T) {
	a := PointFingerprint("p", "RIGHT", 1, "body")
	b := PointFingerprint("p", "RIGHT", 1, "body \t\r\n\n")
	if a != b {
		t.Fatalf("trailing whitespace changed the fingerprint: %s vs %s", a, b)
	}
	if PointFingerprint("p", "RIGHT", 1, " body") == a {
		t.Fatal("leading whitespace must stay significant")
	}
}

func TestPointFingerprint_EmptySideIsRight(t *testing.T) {
	if PointFingerprint("p", "", 3, "x") != PointFingerprint("p", "RIGHT", 3, "x") {
		t.Fatal(`"" and RIGHT must agree`)
	}
	if PointFingerprint("p", "right", 3, "x") != PointFingerprint("p", "RIGHT", 3, "x") {
		t.Fatal("side must be upper-cased")
	}
	if PointFingerprint("p", "LEFT", 3, "x") == PointFingerprint("p", "RIGHT", 3, "x") {
		t.Fatal("LEFT and RIGHT must differ")
	}
}

func TestPointFingerprint_DistinguishesInputs(t *testing.T) {
	base := PointFingerprint("p", "RIGHT", 3, "x")
	for name, other := range map[string]string{
		"path": PointFingerprint("q", "RIGHT", 3, "x"),
		"line": PointFingerprint("p", "RIGHT", 4, "x"),
		"body": PointFingerprint("p", "RIGHT", 3, "y"),
	} {
		if other == base {
			t.Errorf("changing %s did not change the fingerprint", name)
		}
	}
}

func TestFingerprint_KnownVectors(t *testing.T) {
	// Hex values from `printf <preimage> | shasum -a 256 | cut -c1-16`, pinning
	// the exact preimage layout so a refactor cannot silently change every key.
	if got, want := PointFingerprint("a.go", "", 7, "hello\r\n"), "9ff81ac89a6032c7"; got != want {
		t.Errorf("point: got %s want %s", got, want)
	}
	if got, want := ReplyFingerprint("T1", "hi\n"), "aa38315b4393d5de"; got != want {
		t.Errorf("reply: got %s want %s", got, want)
	}
}

func TestReplyFingerprint(t *testing.T) {
	a := ReplyFingerprint("PRRT_abc", "thanks\r\n")
	if a != ReplyFingerprint("PRRT_abc", "thanks") {
		t.Fatal("reply fingerprint must normalize the body")
	}
	if a == ReplyFingerprint("PRRT_def", "thanks") {
		t.Fatal("thread id must be part of the reply fingerprint")
	}
	if a == PointFingerprint("PRRT_abc", "RIGHT", 0, "thanks") {
		t.Fatal("reply and point fingerprints must not share a preimage")
	}
	if len(a) != FingerprintLen {
		t.Fatalf("len = %d", len(a))
	}
}

func TestMarkerRoundTrip(t *testing.T) {
	fp := PointFingerprint("p", "", 1, "x")
	m := Marker(fp)
	if m != "<!-- pg-fp:"+fp+" -->" {
		t.Fatalf("marker = %q", m)
	}
	body := "Some comment.\n\n*Posted by pg-connector at abc1234.* " + m + "\n"
	got := ExtractFingerprints(body)
	if !reflect.DeepEqual(got, []string{fp}) {
		t.Fatalf("got %v", got)
	}
}

func TestExtractFingerprints_Multiple(t *testing.T) {
	a, b := ReplyFingerprint("t", "a"), ReplyFingerprint("t", "b")
	text := strings.Join([]string{Marker(a), "middle", Marker(b), Marker(a)}, "\n")
	got := ExtractFingerprints(text)
	if !reflect.DeepEqual(got, []string{a, b, a}) {
		t.Fatalf("got %v", got)
	}
}

func TestExtractFingerprints_IgnoresMalformed(t *testing.T) {
	for _, text := range []string{
		"",
		"no marker here",
		"<!-- pg-fp: -->",
		"<!-- pg-fp:XYZ -->",
		"<!-- pg-fp:0123456789abcde -->",   // 15 chars
		"<!-- pg-fp:0123456789abcdef0 -->", // 17 chars
		"<!-- other:0123456789abcdef -->",
	} {
		if got := ExtractFingerprints(text); len(got) != 0 {
			t.Errorf("%q -> %v, want none", text, got)
		}
	}
}
