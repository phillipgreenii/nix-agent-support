package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-rescue/internal/contract"
)

func runResultCmd(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	h := newHarness(t, "")
	return h.run(append([]string{"result"}, args...)...)
}

func TestResultExitCodesAndShape(t *testing.T) {
	tests := []struct {
		outcome string
		code    int
	}{{"resolved", 0}, {"deferred", 3}, {"declined", 2}}
	for _, tc := range tests {
		t.Run(tc.outcome, func(t *testing.T) {
			code, stdout, stderr := runResultCmd(t, tc.outcome, "one line summary")
			if code != tc.code {
				t.Errorf("exit = %d; want %d", code, tc.code)
			}
			if stderr != "" {
				t.Errorf("stderr = %q", stderr)
			}
			if !strings.HasSuffix(stdout, "\n") || strings.Count(stdout, "\n") != 1 {
				t.Errorf("want exactly one line, got %q", stdout)
			}
			obj, err := contract.ParseObject([]byte(stdout))
			if err != nil {
				t.Fatalf("stdout is not one valid JSON object: %v\n%s", err, stdout)
			}
			var got struct{ Outcome, Summary string }
			_ = json.Unmarshal([]byte(stdout), &got)
			if got.Outcome != tc.outcome || got.Summary != "one line summary" {
				t.Errorf("decoded %+v", got)
			}
			if len(obj) != 2 {
				t.Errorf("only outcome and summary expected, got keys %v", obj)
			}
		})
	}
}

func TestResultSummaryIsOptional(t *testing.T) {
	code, stdout, _ := runResultCmd(t, "declined")
	if code != 2 || stdout != `{"outcome":"declined"}`+"\n" {
		t.Errorf("code=%d stdout=%q", code, stdout)
	}
	// An explicitly empty summary is the same as none.
	_, stdout, _ = runResultCmd(t, "declined", "")
	if stdout != `{"outcome":"declined"}`+"\n" {
		t.Errorf("empty summary stdout = %q", stdout)
	}
}

func TestResultMultilineSummaryStaysOneJSONLine(t *testing.T) {
	_, stdout, _ := runResultCmd(t, "resolved", "first\nsecond \"quoted\"")
	if strings.Count(stdout, "\n") != 1 {
		t.Errorf("stdout = %q", stdout)
	}
	var got contract.Result
	if err := json.Unmarshal([]byte(stdout), &got); err != nil || got.Summary != "first\nsecond \"quoted\"" {
		t.Errorf("round trip: %+v %v", got, err)
	}
}

func TestResultDetailsAndMetaFiles(t *testing.T) {
	dir := t.TempDir()
	details := filepath.Join(dir, "details.txt")
	meta := filepath.Join(dir, "meta.json")
	if err := os.WriteFile(details, []byte("line one\nline two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(meta, []byte("{\n  \"session_id\": \"abc\",\n  \"total_cost_usd\": 0.04\n}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := runResultCmd(t, "resolved", "ok", "--details-file", details, "--meta-file", meta)
	if code != 0 || stderr != "" {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	want := `{"outcome":"resolved","summary":"ok","details":"line one\nline two\n","meta":{"session_id":"abc","total_cost_usd":0.04}}` + "\n"
	if stdout != want {
		t.Errorf("stdout:\n%s\nwant:\n%s", stdout, want)
	}
	if _, err := contract.ParseObject([]byte(stdout)); err != nil {
		t.Errorf("not strictly valid: %v", err)
	}
}

func TestResultFlagsMayPrecedeThePositionals(t *testing.T) {
	meta := filepath.Join(t.TempDir(), "m.json")
	if err := os.WriteFile(meta, []byte(`{"a":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	code, stdout, _ := runResultCmd(t, "--meta-file", meta, "deferred", "filed")
	if code != 3 || !strings.Contains(stdout, `"meta":{"a":1}`) {
		t.Errorf("code=%d stdout=%q", code, stdout)
	}
}

func TestResultErrorsExit70WithEmptyStdout(t *testing.T) {
	dir := t.TempDir()
	notObject := filepath.Join(dir, "array.json")
	dup := filepath.Join(dir, "dup.json")
	junk := filepath.Join(dir, "junk.json")
	for p, body := range map[string]string{notObject: `[1]`, dup: `{"a":1,"a":2}`, junk: `{"a":1} x`} {
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"no outcome", nil, "missing outcome"},
		{"unknown outcome", []string{"failed"}, `unknown outcome "failed"; valid: resolved, deferred, declined`},
		{"misspelled outcome", []string{"resolve"}, "valid: resolved, deferred, declined"},
		{"too many args", []string{"resolved", "a", "b"}, "too many arguments"},
		{"unknown flag", []string{"resolved", "--nope"}, "unknown flag"},
		{"missing details file", []string{"resolved", "--details-file", filepath.Join(dir, "nope")}, "--details-file"},
		{"missing meta file", []string{"resolved", "--meta-file", filepath.Join(dir, "nope")}, "--meta-file"},
		{"meta is an array", []string{"resolved", "--meta-file", notObject}, "exactly one JSON object"},
		{"meta has duplicate keys", []string{"resolved", "--meta-file", dup}, "duplicate key"},
		{"meta has trailing junk", []string{"resolved", "--meta-file", junk}, "exactly one JSON object"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			code, stdout, stderr := runResultCmd(t, tc.args...)
			if code != 70 || stdout != "" {
				t.Errorf("code=%d stdout=%q", code, stdout)
			}
			contains(t, "stderr", stderr, "pg-rescue: ", tc.want)
		})
	}
}

func TestResultNeedsNoConfigAndNoState(t *testing.T) {
	h := newHarness(t, "") // no config file exists
	code, _, _ := h.run("result", "resolved")
	if code != 0 {
		t.Errorf("exit = %d; result must not need a config", code)
	}
	if dirs := h.runDirs(); len(dirs) != 0 || len(h.rec.calls) != 0 {
		t.Errorf("result touched run state: %v", dirs)
	}
}
