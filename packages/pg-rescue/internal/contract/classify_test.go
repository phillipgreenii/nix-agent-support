package contract

import (
	"bytes"
	"errors"
	"strconv"
	"strings"
	"testing"
)

type classifyCase struct {
	name      string
	exit      int
	stdout    string
	truncated bool

	outcome Outcome
	// reason is matched exactly when reasonExact is true, else as a substring.
	reason      string
	reasonExact bool
	summary     string
	details     string
	meta        string
}

func runClassify(t *testing.T, cases []classifyCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			outcome, reason, rep := Classify(tc.exit, []byte(tc.stdout), tc.truncated)
			if outcome != tc.outcome {
				t.Errorf("outcome = %q; want %q (reason %q)", outcome, tc.outcome, reason)
			}
			if tc.reasonExact && reason != tc.reason {
				t.Errorf("reason = %q; want exactly %q", reason, tc.reason)
			}
			if !tc.reasonExact && !strings.Contains(reason, tc.reason) {
				t.Errorf("reason = %q; want it to contain %q", reason, tc.reason)
			}
			if reason == "" {
				t.Error("reason must never be empty")
			}
			if rep.Summary != tc.summary || rep.Details != tc.details || string(rep.Meta) != tc.meta {
				t.Errorf("reported = %+v; want summary %q details %q meta %q", rep, tc.summary, tc.details, tc.meta)
			}
			if rep.DetailsFile != "" {
				t.Errorf("Classify must never set DetailsFile, got %q", rep.DetailsFile)
			}
		})
	}
}

// Every row of the handler exit-code table, with and without stdout.
func TestClassifyExitCodeTable(t *testing.T) {
	var cases []classifyCase
	for _, row := range []struct {
		exit int
		want Outcome
	}{{0, Resolved}, {2, Declined}, {3, Deferred}} {
		cases = append(
			cases,
			classifyCase{name: "bare " + string(row.want), exit: row.exit, outcome: row.want, reason: reasonExit(row.exit), reasonExact: true},
			classifyCase{
				name: "agreeing outcome " + string(row.want), exit: row.exit,
				stdout: `{"outcome":"` + string(row.want) + `","summary":"s"}`, outcome: row.want, reason: reasonExit(row.exit), reasonExact: true, summary: "s",
			},
		)
	}
	for _, code := range []int{1, 4, 70, 75, 126, 127, 128, 130, 255, -1} {
		cases = append(cases, classifyCase{name: "other " + reasonExit(code), exit: code, outcome: Failed, reason: reasonExit(code), reasonExact: true})
	}
	runClassify(t, cases)
}

func reasonExit(code int) string { return "exit " + strconv.Itoa(code) }

// Stdout is optional; empty (or only whitespace) leaves the exit code alone.
func TestClassifyEmptyStdout(t *testing.T) {
	runClassify(t, []classifyCase{
		{name: "empty", exit: 0, stdout: "", outcome: Resolved, reason: "exit 0", reasonExact: true},
		{name: "newline only", exit: 3, stdout: "\n", outcome: Deferred, reason: "exit 3", reasonExact: true},
		{name: "mixed whitespace", exit: 2, stdout: " \t\r\n ", outcome: Declined, reason: "exit 2", reasonExact: true},
	})
}

// Exactly one object, optionally followed by whitespace, is valid; anything
// else after it is not.
func TestClassifyOneObjectPlusWhitespace(t *testing.T) {
	runClassify(t, []classifyCase{
		{name: "object only", exit: 0, stdout: `{"summary":"a"}`, outcome: Resolved, reason: "exit 0", reasonExact: true, summary: "a"},
		{name: "trailing newline", exit: 0, stdout: "{\"summary\":\"a\"}\n", outcome: Resolved, reason: "exit 0", reasonExact: true, summary: "a"},
		{name: "trailing mixed whitespace", exit: 0, stdout: "{\"summary\":\"a\"} \t\r\n\n", outcome: Resolved, reason: "exit 0", reasonExact: true, summary: "a"},
		{name: "leading whitespace", exit: 0, stdout: "\n  {\"summary\":\"a\"}", outcome: Resolved, reason: "exit 0", reasonExact: true, summary: "a"},
		{name: "empty object", exit: 2, stdout: `{}`, outcome: Declined, reason: "exit 2", reasonExact: true},
		{
			name: "two objects", exit: 0, stdout: `{"summary":"a"}{"summary":"b"}`, outcome: Failed,
			reason: `stdout has data after the JSON object (starts "{\"summary\":\"a\"}{\"summary\":\"b\"}"); handlers must print exactly one object and log to stderr`, reasonExact: true,
		},
		{name: "two objects on two lines", exit: 0, stdout: "{}\n{}\n", outcome: Failed, reason: "data after the JSON object"},
		{name: "trailing junk", exit: 0, stdout: `{"summary":"a"} done`, outcome: Failed, reason: "data after the JSON object"},
		{name: "trailing junk on a deferred exit", exit: 3, stdout: `{} x`, outcome: Failed, reason: "data after the JSON object"},
		{name: "junk before the object", exit: 0, stdout: `done {"summary":"a"}`, outcome: Failed, reason: "stdout is not JSON"},
		{
			name: "array", exit: 0, stdout: `[{"summary":"a"}]`, outcome: Failed,
			reason: `stdout is JSON but not an object (starts "[{\"summary\":\"a\"}]"); handlers must print one object`, reasonExact: true,
		},
		{name: "scalar", exit: 0, stdout: `42`, outcome: Failed, reason: "stdout is JSON but not an object"},
		{name: "string", exit: 0, stdout: `"ok"`, outcome: Failed, reason: "stdout is JSON but not an object"},
		{name: "null", exit: 0, stdout: `null`, outcome: Failed, reason: "stdout is JSON but not an object"},
		{name: "truncated object", exit: 0, stdout: `{"summary":`, outcome: Failed, reason: "stdout is not JSON"},
		{name: "unterminated string", exit: 0, stdout: `{"summary":"abc`, outcome: Failed, reason: "stdout is not JSON"},
	})
}

// The specific reason that tells a handler author what to fix.
func TestClassifyNotJSONReasonQuotesWhatItSaw(t *testing.T) {
	runClassify(t, []classifyCase{
		{
			name: "short log line", exit: 0, stdout: "Successfully rebased\n", outcome: Failed,
			reason: `stdout is not JSON (starts "Successfully rebased"); handlers must log to stderr`, reasonExact: true,
		},
		{
			name: "long log line is cut with an ellipsis", exit: 0,
			stdout: "Successfully rebased and updated refs/heads/main.\n", outcome: Failed,
			reason: `stdout is not JSON (starts "Successfully rebased and updated refs/he…"); handlers must log to stderr`, reasonExact: true,
		},
		{
			name: "exactly 40 runes is not cut", exit: 0,
			stdout: strings.Repeat("x", 40), outcome: Failed,
			reason: `stdout is not JSON (starts "` + strings.Repeat("x", 40) + `"); handlers must log to stderr`, reasonExact: true,
		},
		{
			name: "41 runes is cut", exit: 0,
			stdout: strings.Repeat("x", 41), outcome: Failed,
			reason: `stdout is not JSON (starts "` + strings.Repeat("x", 40) + `…"); handlers must log to stderr`, reasonExact: true,
		},
		{
			name: "leading whitespace skipped, newlines escaped", exit: 0, stdout: "\n\n  first\nsecond", outcome: Failed,
			reason: `stdout is not JSON (starts "first\nsecond"); handlers must log to stderr`, reasonExact: true,
		},
		{
			name: "multibyte runes counted as runes", exit: 0, stdout: strings.Repeat("é", 41), outcome: Failed,
			reason: `stdout is not JSON (starts "` + strings.Repeat("é", 40) + `…"); handlers must log to stderr`, reasonExact: true,
		},
		{
			name: "invalid UTF-8 is replaced", exit: 0, stdout: "bad\xff\xfebytes", outcome: Failed,
			reason: "stdout is not JSON (starts \"bad�bytes\"); handlers must log to stderr", reasonExact: true,
		},
	})
}

func TestClassifyDuplicateKeys(t *testing.T) {
	runClassify(t, []classifyCase{
		{
			name: "top level", exit: 0, stdout: `{"summary":"a","summary":"b"}`, outcome: Failed,
			reason: `stdout JSON has a duplicate key "summary"`, reasonExact: true,
		},
		{name: "duplicate outcome", exit: 0, stdout: `{"outcome":"resolved","outcome":"resolved"}`, outcome: Failed, reason: `duplicate key "outcome"`},
		{name: "inside meta", exit: 0, stdout: `{"meta":{"a":1,"a":2}}`, outcome: Failed, reason: `duplicate key "a"`},
		{name: "inside meta array", exit: 0, stdout: `{"meta":{"l":[{"k":1,"k":2}]}}`, outcome: Failed, reason: `duplicate key "k"`},
		{name: "escaped spelling of the same key", exit: 0, stdout: `{"summary":"a","summary":"b"}`, outcome: Failed, reason: `duplicate key "summary"`},
		{
			name: "same key in sibling objects is fine", exit: 0, stdout: `{"meta":{"a":{"x":1},"b":{"x":2}}}`, outcome: Resolved, reason: "exit 0", reasonExact: true,
			meta: `{"a":{"x":1},"b":{"x":2}}`,
		},
		{name: "duplicate key still fails a deferred exit", exit: 3, stdout: `{"summary":"a","summary":"b"}`, outcome: Failed, reason: "duplicate key"},
	})
}

func TestClassifyWrongFieldTypes(t *testing.T) {
	runClassify(t, []classifyCase{
		{name: "outcome number", exit: 0, stdout: `{"outcome":1}`, outcome: Failed, reason: `stdout JSON: field "outcome" must be a string, not a number`, reasonExact: true},
		{name: "outcome null", exit: 0, stdout: `{"outcome":null}`, outcome: Failed, reason: `field "outcome" must be a string, not null`},
		{name: "outcome object", exit: 0, stdout: `{"outcome":{}}`, outcome: Failed, reason: `field "outcome" must be a string, not an object`},
		{name: "summary number", exit: 0, stdout: `{"summary":5}`, outcome: Failed, reason: `stdout JSON: field "summary" must be a string, not a number`, reasonExact: true},
		{name: "summary bool", exit: 0, stdout: `{"summary":true}`, outcome: Failed, reason: `field "summary" must be a string, not a boolean`},
		{name: "summary false", exit: 0, stdout: `{"summary":false}`, outcome: Failed, reason: `field "summary" must be a string, not a boolean`},
		{name: "summary array", exit: 0, stdout: `{"summary":["a"]}`, outcome: Failed, reason: `field "summary" must be a string, not an array`},
		{name: "summary null", exit: 0, stdout: `{"summary":null}`, outcome: Failed, reason: `field "summary" must be a string, not null`},
		{name: "details object", exit: 0, stdout: `{"details":{"a":1}}`, outcome: Failed, reason: `stdout JSON: field "details" must be a string, not an object`, reasonExact: true},
		{name: "details number", exit: 0, stdout: `{"details":-1.5e3}`, outcome: Failed, reason: `field "details" must be a string, not a number`},
		{name: "meta string", exit: 0, stdout: `{"meta":"x"}`, outcome: Failed, reason: `stdout JSON: field "meta" must be an object, not a string`, reasonExact: true},
		{name: "meta array", exit: 0, stdout: `{"meta":[1]}`, outcome: Failed, reason: `field "meta" must be an object, not an array`},
		{name: "meta null", exit: 0, stdout: `{"meta":null}`, outcome: Failed, reason: `field "meta" must be an object, not null`},
		{name: "meta number", exit: 0, stdout: `{"meta":0}`, outcome: Failed, reason: `field "meta" must be an object, not a number`},
		{name: "good fields do not rescue a bad one", exit: 0, stdout: `{"summary":"ok","details":3}`, outcome: Failed, reason: `field "details"`},
		{name: "wrong type fails a deferred exit too", exit: 3, stdout: `{"summary":3}`, outcome: Failed, reason: `field "summary"`},
	})
}

func TestClassifyUnknownFieldsAreIgnored(t *testing.T) {
	runClassify(t, []classifyCase{
		{name: "unknown scalar", exit: 0, stdout: `{"summary":"a","future":1}`, outcome: Resolved, reason: "exit 0", reasonExact: true, summary: "a"},
		{name: "unknown with odd types", exit: 2, stdout: `{"x":null,"y":[1,{"z":2}],"details":"d"}`, outcome: Declined, reason: "exit 2", reasonExact: true, details: "d"},
		{name: "case differs so it is unknown", exit: 0, stdout: `{"Summary":"a","OUTCOME":"declined"}`, outcome: Resolved, reason: "exit 0", reasonExact: true},
		{name: "unknown field on an unlisted exit code", exit: 1, stdout: `{"future":true,"summary":"s"}`, outcome: Failed, reason: "exit 1", reasonExact: true, summary: "s"},
	})
}

func TestClassifyPassesFieldsThrough(t *testing.T) {
	runClassify(t, []classifyCase{
		{
			name: "summary and details", exit: 0, stdout: `{"summary":"line one\nline two","details":"multi\nline \"quoted\" é"}`,
			outcome: Resolved, reason: "exit 0", reasonExact: true,
			summary: "line one\nline two", details: "multi\nline \"quoted\" é",
		},
		{name: "empty strings", exit: 0, stdout: `{"summary":"","details":""}`, outcome: Resolved, reason: "exit 0", reasonExact: true},
		{
			name: "meta verbatim keeps order, spacing and big numbers", exit: 3,
			stdout:  `{"meta": { "z":1,  "a":[1, 2.50, 12345678901234567890], "s":"é\/" } }`,
			outcome: Deferred, reason: "exit 3", reasonExact: true,
			meta: `{ "z":1,  "a":[1, 2.50, 12345678901234567890], "s":"é\/" }`,
		},
		{name: "empty meta object", exit: 0, stdout: `{"meta":{}}`, outcome: Resolved, reason: "exit 0", reasonExact: true, meta: `{}`},
		{
			name: "meta member order in the object is irrelevant", exit: 0, stdout: `{"meta":{"k":1},"summary":"s"}`, outcome: Resolved, reason: "exit 0", reasonExact: true,
			summary: "s", meta: `{"k":1}`,
		},
	})
}

func TestClassifyMetaDoesNotAliasStdout(t *testing.T) {
	stdout := []byte(`{"meta":{"k":"v"}}`)
	_, _, rep := Classify(0, stdout, false)
	for i := range stdout {
		stdout[i] = 'X'
	}
	if string(rep.Meta) != `{"k":"v"}` {
		t.Errorf("meta changed when the caller reused its buffer: %q", rep.Meta)
	}
}

// A handler may only name resolved, deferred or declined; failed is the
// wrapper's alone.
func TestClassifyOutcomeField(t *testing.T) {
	runClassify(t, []classifyCase{
		{
			name: "failed is not reportable", exit: 0, stdout: `{"outcome":"failed"}`, outcome: Failed,
			reason: `stdout JSON: unknown outcome "failed"; valid: resolved, deferred, declined`, reasonExact: true,
		},
		{name: "unknown name", exit: 0, stdout: `{"outcome":"ok"}`, outcome: Failed, reason: `unknown outcome "ok"`},
		{name: "wrong case", exit: 0, stdout: `{"outcome":"Resolved"}`, outcome: Failed, reason: `unknown outcome "Resolved"`},
		{name: "empty name", exit: 2, stdout: `{"outcome":""}`, outcome: Failed, reason: `unknown outcome ""`},
		{
			name: "unknown name on an unlisted exit code is ignored with a note", exit: 1, stdout: `{"outcome":"bogus","summary":"s"}`, outcome: Failed,
			reason: `exit 1; stdout ignored: stdout JSON: unknown outcome "bogus"`,
		},
	})
}

// A mismatch between the stdout outcome and the exit code fails the attempt.
func TestClassifyMismatchedOutcome(t *testing.T) {
	runClassify(t, []classifyCase{
		{
			name: "resolved claimed on declined exit", exit: 2, stdout: `{"outcome":"resolved","summary":"s"}`, outcome: Failed,
			reason: `stdout outcome "resolved" disagrees with exit code 2 (declined)`, reasonExact: true, summary: "s",
		},
		{
			name: "declined claimed on resolved exit", exit: 0, stdout: `{"outcome":"declined"}`, outcome: Failed,
			reason: `stdout outcome "declined" disagrees with exit code 0 (resolved)`, reasonExact: true,
		},
		{
			name: "deferred claimed on resolved exit", exit: 0, stdout: `{"outcome":"deferred","details":"d"}`, outcome: Failed,
			reason: `stdout outcome "deferred" disagrees with exit code 0 (resolved)`, reasonExact: true, details: "d",
		},
		{
			name: "resolved claimed on deferred exit", exit: 3, stdout: `{"outcome":"resolved","meta":{"a":1}}`, outcome: Failed,
			reason: `stdout outcome "resolved" disagrees with exit code 3 (deferred)`, reasonExact: true, meta: `{"a":1}`,
		},
	})
}

// For an exit code outside the table, a valid object still contributes its
// claims, a present outcome is reported as a mismatch, and the outcome stays
// failed.
func TestClassifyExitOneWithJSON(t *testing.T) {
	runClassify(t, []classifyCase{
		{
			name: "claims contributed", exit: 1, stdout: `{"summary":"s","details":"d","meta":{"k":1}}`, outcome: Failed,
			reason: "exit 1", reasonExact: true, summary: "s", details: "d", meta: `{"k":1}`,
		},
		{
			name: "claimed resolved is a mismatch", exit: 1, stdout: `{"outcome":"resolved","summary":"s"}`, outcome: Failed,
			reason: `exit 1; stdout outcome "resolved" disagrees with exit code 1`, reasonExact: true, summary: "s",
		},
		{
			name: "claimed declined is a mismatch", exit: 4, stdout: `{"outcome":"declined"}`, outcome: Failed,
			reason: `exit 4; stdout outcome "declined" disagrees with exit code 4`, reasonExact: true,
		},
		{
			name: "invalid stdout is ignored with the problem noted", exit: 1, stdout: "panic: boom\n", outcome: Failed,
			reason: `exit 1; stdout ignored: stdout is not JSON (starts "panic: boom"); handlers must log to stderr`, reasonExact: true,
		},
		{
			name: "duplicate key contributes nothing", exit: 1, stdout: `{"summary":"a","summary":"b"}`, outcome: Failed,
			reason: `exit 1; stdout ignored: stdout JSON has a duplicate key "summary"`, reasonExact: true,
		},
		{
			name: "wrong type contributes nothing", exit: 1, stdout: `{"summary":"ok","details":1}`, outcome: Failed,
			reason: `exit 1; stdout ignored: stdout JSON: field "details" must be a string, not a number`, reasonExact: true,
		},
		{name: "empty stdout", exit: 1, stdout: "", outcome: Failed, reason: "exit 1", reasonExact: true},
	})
}

// The 1 MiB cap is the caller's to enforce; Classify only learns whether
// anything was cut off.
func TestClassifyTruncatedFlag(t *testing.T) {
	runClassify(t, []classifyCase{
		{
			name: "truncated resolved", exit: 0, stdout: `{"summary":"a"}`, truncated: true, outcome: Failed,
			reason: "stdout is larger than 1048576 bytes (1 MiB cap) and was cut off", reasonExact: true,
		},
		{name: "truncated declined", exit: 2, stdout: ``, truncated: true, outcome: Failed, reason: "1 MiB cap"},
		{name: "truncated deferred", exit: 3, stdout: `{"outcome":"deferred"}`, truncated: true, outcome: Failed, reason: "1 MiB cap"},
		{
			name: "truncated on an unlisted exit code", exit: 1, stdout: `{"summary":"a"}`, truncated: true, outcome: Failed,
			reason: "exit 1; stdout ignored: stdout is larger than 1048576 bytes (1 MiB cap) and was cut off", reasonExact: true,
		},
		{name: "not truncated is fine", exit: 0, stdout: `{"summary":"a"}`, truncated: false, outcome: Resolved, reason: "exit 0", reasonExact: true, summary: "a"},
	})
}

// A result exactly at the cap is valid; one byte more is not. This is the
// path the wrapper takes: LimitedBuffer in front, Classify behind.
func TestClassifyAtTheCapThroughLimitedBuffer(t *testing.T) {
	prefix := `{"details":"`
	suffix := `"}`
	pad := func(total int) string {
		return prefix + strings.Repeat("a", total-len(prefix)-len(suffix)) + suffix
	}
	for _, tc := range []struct {
		name    string
		size    int
		outcome Outcome
	}{
		{"exactly 1 MiB", MaxResultBytes, Resolved},
		{"1 MiB plus one byte", MaxResultBytes + 1, Failed},
		{"1 MiB minus one byte", MaxResultBytes - 1, Resolved},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := pad(tc.size)
			if len(payload) != tc.size {
				t.Fatalf("test setup: payload is %d bytes, want %d", len(payload), tc.size)
			}
			buf := NewResultBuffer()
			// Write in pieces, like a pipe copier would.
			for rest := []byte(payload); len(rest) > 0; {
				n := min(len(rest), 4096)
				if w, err := buf.Write(rest[:n]); err != nil || w != n {
					t.Fatalf("Write = %d, %v; the writer must never fail or short-write", w, err)
				}
				rest = rest[n:]
			}
			got, _, _ := Classify(0, buf.Bytes(), buf.Truncated())
			if got != tc.outcome {
				t.Errorf("outcome = %q; want %q (kept %d bytes, truncated=%v)", got, tc.outcome, len(buf.Bytes()), buf.Truncated())
			}
		})
	}
}

func TestLimitedBuffer(t *testing.T) {
	b := &LimitedBuffer{Max: 5}
	if n, err := b.Write([]byte("abc")); n != 3 || err != nil || b.Truncated() {
		t.Fatalf("first write: %d %v truncated=%v", n, err, b.Truncated())
	}
	if n, err := b.Write([]byte("def")); n != 3 || err != nil {
		t.Fatalf("second write must report the full length: %d %v", n, err)
	}
	if !b.Truncated() || !bytes.Equal(b.Bytes(), []byte("abcde")) {
		t.Errorf("kept %q truncated=%v; want abcde, true", b.Bytes(), b.Truncated())
	}
	if n, err := b.Write([]byte("zzz")); n != 3 || err != nil || string(b.Bytes()) != "abcde" {
		t.Errorf("a full buffer keeps discarding: %d %v %q", n, err, b.Bytes())
	}
	exact := &LimitedBuffer{Max: 3}
	_, _ = exact.Write([]byte("abc"))
	if exact.Truncated() {
		t.Error("writing exactly Max bytes is not truncation")
	}
	_, _ = exact.Write(nil)
	_, _ = exact.Write([]byte{})
	if exact.Truncated() {
		t.Error("an empty write is not truncation")
	}
	_, _ = exact.Write([]byte("d"))
	if !exact.Truncated() {
		t.Error("one byte over Max is truncation")
	}
	if NewResultBuffer().Max != MaxResultBytes || MaxResultBytes != 1048576 {
		t.Errorf("NewResultBuffer must cap at 1 MiB, got %d / %d", NewResultBuffer().Max, MaxResultBytes)
	}
}

// ParseObject's error kinds are what Classify words its reasons from.
func TestParseObjectErrorKinds(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want error
	}{
		{``, ErrNotJSON},
		{`x`, ErrNotJSON},
		{`{"a":`, ErrNotJSON},
		{`{"a" 1}`, ErrNotJSON},
		{`[1]`, ErrNotObject},
		{`1`, ErrNotObject},
		{`"s"`, ErrNotObject},
		{`null`, ErrNotObject},
		{`{} {}`, ErrTrailingData},
		{`{} x`, ErrTrailingData},
		{`{}}`, ErrTrailingData},
	} {
		_, err := ParseObject([]byte(tc.in))
		if err == nil || !errors.Is(err, tc.want) {
			t.Errorf("ParseObject(%q) error = %v; want kind %v", tc.in, err, tc.want)
		}
	}
	_, err := ParseObject([]byte(`{"k":1,"k":2}`))
	var dup *DuplicateKeyError
	if !errors.As(err, &dup) || dup.Key != "k" {
		t.Errorf("want *DuplicateKeyError for k, got %v", err)
	}
	got, err := ParseObject([]byte(` {"b": 2 , "a":{"x": [1,  2]}} `))
	if err != nil || string(got["a"]) != `{"x": [1,  2]}` || string(got["b"]) != `2` {
		t.Errorf("members must come back raw and verbatim, got %q %v", got, err)
	}
}

func TestKindNamesEveryJSONType(t *testing.T) {
	for in, want := range map[string]string{
		`"s"`: "a string", `{}`: "an object", `[]`: "an array", `true`: "a boolean", `false`: "a boolean",
		`null`: "null", `0`: "a number", `-1.5e3`: "a number", ` {"a":1}`: "an object", "": "empty", " \n": "empty",
	} {
		if got := kind([]byte(in)); got != want {
			t.Errorf("kind(%q) = %q; want %q", in, got, want)
		}
	}
	if kind(nil) != "empty" {
		t.Error("kind(nil) must not panic and must say empty")
	}
}

// Result.Render refuses what it cannot encode, and says why.
func TestRenderReportsEncodingFailures(t *testing.T) {
	out, err := Result{Outcome: Resolved, Meta: []byte(`{not json`)}.Render()
	if err == nil || out != nil {
		t.Errorf("Render with invalid meta = %q, %v; want an error and no output", out, err)
	}
}

// Syntax errors inside the object, wherever the decoder trips, are "not JSON".
func TestParseObjectMalformedInsideTheObject(t *testing.T) {
	for _, in := range []string{`{"a":1 "b":2}`, `{1:2}`, `{"a":1,}`, `{"a":[1 2]}`, `{"a":{"b":1 "c":2}}`, `{"a":[}`, `{"a"}`} {
		_, err := ParseObject([]byte(in))
		if !errors.Is(err, ErrNotJSON) {
			t.Errorf("ParseObject(%q) error = %v; want kind ErrNotJSON", in, err)
		}
	}
}
