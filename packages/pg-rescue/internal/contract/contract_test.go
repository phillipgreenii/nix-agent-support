package contract

import (
	"strings"
	"testing"
)

func TestOutcomeExitCodes(t *testing.T) {
	for _, tc := range []struct {
		o    Outcome
		code int
	}{{Resolved, 0}, {Declined, 2}, {Deferred, 3}} {
		got, ok := tc.o.ExitCode()
		if !ok || got != tc.code {
			t.Errorf("%s.ExitCode() = %d,%v; want %d,true", tc.o, got, ok, tc.code)
		}
		if back := OutcomeForExit(tc.code); back != tc.o {
			t.Errorf("OutcomeForExit(%d) = %s; want %s", tc.code, back, tc.o)
		}
	}
	if _, ok := Failed.ExitCode(); ok {
		t.Error("Failed must not be reportable by a handler")
	}
	for _, code := range []int{1, 4, 70, 75, 126, 127, 130, -1} {
		if got := OutcomeForExit(code); got != Failed {
			t.Errorf("OutcomeForExit(%d) = %s; want failed", code, got)
		}
	}
}

func TestParseReportable(t *testing.T) {
	for _, s := range []string{"resolved", "deferred", "declined"} {
		if o, err := ParseReportable(s); err != nil || string(o) != s {
			t.Errorf("ParseReportable(%q) = %v, %v", s, o, err)
		}
	}
	for _, s := range []string{"failed", "Resolved", "", "ok"} {
		_, err := ParseReportable(s)
		if err == nil || !strings.Contains(err.Error(), "valid: resolved, deferred, declined") {
			t.Errorf("ParseReportable(%q) error = %v; want one listing the valid names", s, err)
		}
	}
}

func TestRenderIsOneCompactObjectWithNewline(t *testing.T) {
	out, err := Result{Outcome: Resolved, Summary: "s", Meta: []byte(`{"a":1}`)}.Render()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(out), `{"outcome":"resolved","summary":"s","meta":{"a":1}}`+"\n"; got != want {
		t.Errorf("got %q want %q", got, want)
	}
	if _, err := ParseObject(out); err != nil {
		t.Errorf("rendered result is not strictly valid: %v", err)
	}
	if _, err := (Result{Outcome: Failed}).Render(); err == nil {
		t.Error("rendering outcome failed must be an error")
	}
}

func TestParseObject(t *testing.T) {
	valid := []string{`{}`, ` {"a":1} `, "{\"a\":[1,{\"b\":2}]}\n\t ", `{"a":{"x":1},"b":{"x":2}}`}
	for _, in := range valid {
		if _, err := ParseObject([]byte(in)); err != nil {
			t.Errorf("ParseObject(%q): %v", in, err)
		}
	}
	invalid := map[string]string{
		"two objects":        `{"a":1}{"b":2}`,
		"trailing junk":      `{"a":1} x`,
		"array":              `[1]`,
		"scalar":             `1`,
		"empty":              ``,
		"duplicate":          `{"a":1,"a":2}`,
		"nested duplicate":   `{"a":{"b":1,"b":2}}`,
		"duplicate in array": `{"a":[{"b":1,"b":2}]}`,
		"truncated":          `{"a":`,
	}
	for name, in := range invalid {
		if _, err := ParseObject([]byte(in)); err == nil {
			t.Errorf("%s: ParseObject(%q) accepted invalid input", name, in)
		}
	}
}
