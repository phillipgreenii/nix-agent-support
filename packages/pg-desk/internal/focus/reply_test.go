package focus

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func intp(n int) *int { return &n }

// Traces INV-FOCUS-19: a reply is parsed completely before any state changes.
func TestReplyGrammarTable(t *testing.T) {
	type want struct {
		ok          bool
		plus, minus []string
		cap         *int
	}
	good := []struct {
		name  string
		input string
		want  want
	}{
		{"ok", "ok", want{ok: true}},
		{"ok uppercase", "OK", want{ok: true}},
		{"ok mixed case", "oK", want{ok: true}},
		{"ok and strike", "ok -X", want{ok: true, minus: []string{"X"}}},
		{"strike and add", "-a +b", want{plus: []string{"b"}, minus: []string{"a"}}},
		{"cap only", "cap=3", want{cap: intp(3)}},
		{"cap uppercase", "CAP=3", want{cap: intp(3)}},
		{"cap mixed case", "Cap=12", want{cap: intp(12)}},
		{"commas", "ok,-a,+b,cap=2", want{ok: true, plus: []string{"b"}, minus: []string{"a"}, cap: intp(2)}},
		{"comma and space", "ok , -a ,  +b", want{ok: true, plus: []string{"b"}, minus: []string{"a"}}},
		{"newlines", "ok\n-a\n+b\n", want{ok: true, plus: []string{"b"}, minus: []string{"a"}}},
		{"CRLF", "ok\r\n-a\r\n", want{ok: true, minus: []string{"a"}}},
		{"tabs and mixed separators", "ok\t-a,\t+b ,cap=4\r\n", want{ok: true, plus: []string{"b"}, minus: []string{"a"}, cap: intp(4)}},
		{"leading separators", " , ok", want{ok: true}},
		{"duplicate ok", "ok ok", want{ok: true}},
		{"duplicate plus", "+a +a", want{plus: []string{"a"}}},
		{"duplicate minus", "-a -a", want{minus: []string{"a"}}},
		{"order kept", "+z +a -m -b", want{plus: []string{"z", "a"}, minus: []string{"m", "b"}}},
		{"URL key", "+https://example.test/o/r/pull/9", want{plus: []string{"https://example.test/o/r/pull/9"}}},
		{"owner repo key", "-o/r#7", want{minus: []string{"o/r#7"}}},
		{"numeric key", "-42 +43", want{plus: []string{"43"}, minus: []string{"42"}}},
		{"keyword-looking key", "+ok-ish", want{plus: []string{"ok-ish"}}},
		{"cap leading zero", "cap=007", want{cap: intp(7)}},
	}
	for _, tc := range good {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseReply(strings.NewReader(tc.input))
			if err != nil {
				t.Fatalf("ParseReply(%q): %v", tc.input, err)
			}
			if got.Ok != tc.want.ok {
				t.Errorf("Ok = %v, want %v", got.Ok, tc.want.ok)
			}
			if !reflect.DeepEqual(got.Plus, tc.want.plus) {
				t.Errorf("Plus = %v, want %v", got.Plus, tc.want.plus)
			}
			if !reflect.DeepEqual(got.Minus, tc.want.minus) {
				t.Errorf("Minus = %v, want %v", got.Minus, tc.want.minus)
			}
			if (got.Cap == nil) != (tc.want.cap == nil) || (got.Cap != nil && *got.Cap != *tc.want.cap) {
				t.Errorf("Cap = %v, want %v", got.Cap, tc.want.cap)
			}
		})
	}

	bad := []struct {
		name, input, msg string
	}{
		{"empty stdin", "", "reply is empty"},
		{"only whitespace", " \t\r\n ", "reply is empty"},
		{"only commas", ",,,", "reply is empty"},
		{"typo", "okay", "unrecognized token"},
		{"bare key", "42", "unrecognized token"},
		{"bare word with ok", "ok please", "unrecognized token"},
		{"plus alone", "+", "names no key"},
		{"minus alone", "ok -", "names no key"},
		{"conflicting pair", "-X +X", "conflicting"},
		{"conflicting pair reversed", "+X ok -X", "conflicting"},
		{"cap zero", "cap=0", "positive integer"},
		{"cap negative", "cap=-1", "positive integer"},
		{"cap letters", "cap=abc", "positive integer"},
		{"cap empty", "cap=", "empty"},
		{"cap plus sign", "cap=+3", "positive integer"},
		{"cap decimal", "cap=2.5", "positive integer"},
		{"cap overflow", "cap=99999999999999999999999", "too large"},
		{"repeated cap", "cap=2 cap=3", "more than once"},
		{"repeated cap same value", "cap=2 CAP=2", "more than once"},
		{"cap and typo", "cap=3 oops", "unrecognized token"},
		{"cap inside token", "xcap=3", "unrecognized token"},
		{"ok equals", "ok=1", "unrecognized token"},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseReply(strings.NewReader(tc.input))
			if err == nil {
				t.Fatalf("ParseReply(%q) = %+v, want an error", tc.input, got)
			}
			if !errors.Is(err, ErrUsage) {
				t.Errorf("error %v is not ErrUsage", err)
			}
			if !strings.Contains(err.Error(), tc.msg) {
				t.Errorf("error %q lacks %q", err, tc.msg)
			}
			var ue *UsageError
			if !errors.As(err, &ue) || ue.Remedy == "" || strings.Contains(err.Error(), "\n") {
				t.Errorf("error %q is not a one-line UsageError with a remedy", err)
			}
			if !reflect.DeepEqual(got, Reply{}) {
				t.Errorf("an error returned a non-zero reply %+v", got)
			}
		})
	}
}

func TestParseReplyEmptyRemedyNamesOk(t *testing.T) {
	_, err := ParseReply(strings.NewReader(""))
	if err == nil || !strings.Contains(err.Error(), "reply ok to accept") {
		t.Fatalf("empty reply error = %v, want it to say 'reply ok to accept'", err)
	}
}

func TestParseReplyTooLong(t *testing.T) {
	_, err := ParseReply(strings.NewReader(strings.Repeat("ok ", maxReplyBytes)))
	if !errors.Is(err, ErrUsage) {
		t.Fatalf("a huge reply error = %v, want ErrUsage", err)
	}
	// At the limit it still parses.
	atLimit := "ok" + strings.Repeat(" ", maxReplyBytes-2)
	if _, err := ParseReply(strings.NewReader(atLimit)); err != nil {
		t.Fatalf("a reply of exactly the limit: %v", err)
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("boom") }

func TestParseReplyReadErrorIsNotUsage(t *testing.T) {
	_, err := ParseReply(errReader{})
	if err == nil || errors.Is(err, ErrUsage) {
		t.Fatalf("a read error = %v, want a non-usage error", err)
	}
}

func TestUsageErrorFormat(t *testing.T) {
	e := &UsageError{Msg: "m", Remedy: "r"}
	if e.Error() != "m; r" {
		t.Errorf("Error() = %q, want %q", e.Error(), "m; r")
	}
	if (&UsageError{Msg: "m"}).Error() != "m" {
		t.Errorf("a remedy-less error must print the message alone")
	}
	if !errors.Is(e, ErrUsage) || errors.Is(e, errors.New("other")) {
		t.Errorf("UsageError must match ErrUsage only")
	}
}

// A token whose first byte sorts below the signs is unrecognized, not a key.
func TestReplyTokensBelowTheSignsAreUnrecognized(t *testing.T) {
	for _, tok := range []string{"!x", "*x", "'x", "\x01x", "(x", ")x", ".x", "/x"} {
		_, err := ParseReply(strings.NewReader("ok " + tok))
		if err == nil || !strings.Contains(err.Error(), "unrecognized token") {
			t.Errorf("%q: err = %v, want an unrecognized-token error", tok, err)
		}
	}
}

func TestParseReplyLimitIsExact(t *testing.T) {
	over := "ok" + strings.Repeat(" ", maxReplyBytes-1)
	if len(over) != maxReplyBytes+1 {
		t.Fatalf("fixture length %d", len(over))
	}
	if _, err := ParseReply(strings.NewReader(over)); !errors.Is(err, ErrUsage) {
		t.Errorf("a reply one byte over the limit: err = %v", err)
	}
}

func TestParseReplyEmptyKeyRemedies(t *testing.T) {
	for _, in := range []string{"+", "-"} {
		_, err := ParseReply(strings.NewReader(in))
		var ue *UsageError
		if !errors.As(err, &ue) || ue.Remedy == "" || !strings.Contains(ue.Msg, "names no key") {
			t.Errorf("%q: %v", in, err)
		}
	}
}
