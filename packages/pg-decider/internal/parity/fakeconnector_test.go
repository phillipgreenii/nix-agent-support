package parity

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fakeFixture is a small synthetic scenario whose bead title carries an
// apostrophe, to prove the generated script quotes fixture text safely.
const fakeFixture = `{
  "name": "fake",
  "description": "d",
  "entities": ["acme/api#7"],
  "prs": [{"number": 7, "author": "teammate", "head_sha": "h7",
    "comments": [{"id": "c1", "author": "review-bot", "body": "it's \"quoted\" $(not run) ` + "`nor this`" + `"}],
    "ci": [{"id": "9", "name": "build", "conclusion": "failure"}]}],
  "beads": [{"id": "bd-1", "title": "acme/api#7: it's a title", "metadata": {"repo": "acme/api", "pr_number": "7"}}]
}`

func writeFake(t *testing.T) (bin, callLog string) {
	t.Helper()
	fx, err := ParseFixture([]byte(fakeFixture))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	bin = filepath.Join(dir, "pg-connector")
	callLog = filepath.Join(dir, "calls.log")
	if err := os.WriteFile(bin, []byte(fakeConnectorScript(fx, callLog)), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, callLog
}

func runFake(t *testing.T, bin string, args ...string) (stdout string, code int) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
	err := cmd.Run()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return out.String(), 0
	case errors.As(err, &ee):
		return out.String(), ee.ExitCode()
	}
	t.Fatalf("run %v: %v", args, err)
	return "", -1
}

func TestFixtureFakeConnectorAnswersTheGatherCalls(t *testing.T) {
	bin, callLog := writeFake(t)

	type env struct {
		Result json.RawMessage `json:"result"`
	}
	cases := []struct {
		args []string
		code int
		// check receives stdout (empty on a not-found exit).
		check func(t *testing.T, out string)
	}{
		{[]string{"pr", "show", "acme/api#7"}, 0, func(t *testing.T, out string) {
			var e env
			mustDecode(t, []byte(out), &e)
			var pr struct {
				ID      string
				Author  string
				Comment []struct{ Body string } `json:"comments"`
			}
			mustDecode(t, e.Result, &pr)
			if pr.ID != "acme/api#7" || pr.Author != "teammate" || len(pr.Comment) != 1 ||
				!strings.Contains(pr.Comment[0].Body, "it's") || !strings.Contains(pr.Comment[0].Body, "$(not run)") {
				t.Errorf("pr show = %+v", pr)
			}
		}},
		{[]string{"pr", "show", "acme/api#99"}, 4, nil},
		{[]string{"pr", "files", "acme/api#7"}, 0, nil},
		{[]string{"pr", "commits", "acme/api#7"}, 0, func(t *testing.T, out string) {
			var e env
			mustDecode(t, []byte(out), &e)
			var c struct{ Commits []struct{ Author string } }
			mustDecode(t, e.Result, &c)
			if len(c.Commits) != 1 || c.Commits[0].Author != "teammate" {
				t.Errorf("commits = %+v", c)
			}
		}},
		{[]string{"ci", "list", "acme/api#7"}, 0, func(t *testing.T, out string) {
			// A fan-out answer is printed bare, with no envelope.
			var ci struct{ Runs []struct{ Conclusion string } }
			mustDecode(t, []byte(out), &ci)
			if len(ci.Runs) != 1 || ci.Runs[0].Conclusion != "failure" {
				t.Errorf("ci = %+v", ci)
			}
		}},
		{[]string{"ci", "list", "acme/api#99"}, 0, func(t *testing.T, out string) {
			var ci struct{ Runs []json.RawMessage }
			mustDecode(t, []byte(out), &ci)
			if len(ci.Runs) != 0 {
				t.Errorf("unknown PR ci = %+v", ci)
			}
		}},
		{[]string{"issue", "list", "--query", "work-beads"}, 0, func(t *testing.T, out string) {
			var l struct{ Entities []struct{ ID, Title string } }
			mustDecode(t, []byte(out), &l)
			if len(l.Entities) != 1 || l.Entities[0].Title != "acme/api#7: it's a title" {
				t.Errorf("work-beads = %+v", l)
			}
		}},
		{[]string{"issue", "show", "bd-1"}, 0, nil},
		{[]string{"issue", "show", "bd-404"}, 4, nil},
		{[]string{"issue", "deps", "bd-1", "--full"}, 0, nil},
		{[]string{"pr", "review", "pending", "acme/api#7"}, 0, func(t *testing.T, out string) {
			var e env
			mustDecode(t, []byte(out), &e)
			var p struct {
				Pending *bool
				HeadSHA string `json:"head_sha"`
			}
			mustDecode(t, e.Result, &p)
			if p.Pending == nil || *p.Pending || p.HeadSHA != "h7" {
				t.Errorf("pending = %+v", p)
			}
		}},
	}
	for _, c := range cases {
		out, code := runFake(t, bin, c.args...)
		if code != c.code {
			t.Errorf("%v: exit %d, want %d", c.args, code, c.code)
			continue
		}
		if c.code == 4 && out != "" {
			t.Errorf("%v: a not-found answer must print nothing, got %q", c.args, out)
		}
		if c.check != nil {
			c.check(t, out)
		}
	}

	log, err := os.ReadFile(callLog)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(log), "\n"); got != len(cases) {
		t.Errorf("call log has %d lines, want %d:\n%s", got, len(cases), log)
	}
	if !strings.Contains(string(log), "issue list --query work-beads\n") {
		t.Errorf("call log misses the work-beads call:\n%s", log)
	}
}

func TestFixtureFakeConnectorRejectsWhatItDoesNotKnow(t *testing.T) {
	bin, callLog := writeFake(t)
	out, code := runFake(t, bin, "thread", "show", "T1")
	if code != 1 {
		t.Fatalf("exit %d, want 1 for an unsupported call", code)
	}
	var e struct {
		Error struct{ Code, Message string }
	}
	mustDecode(t, []byte(out), &e)
	if e.Error.Code != "unsupported" || !strings.Contains(e.Error.Message, "thread show T1") {
		t.Errorf("error envelope = %+v", e)
	}
	// An unsupported call is still recorded, so a silent bypass is visible.
	if log, _ := os.ReadFile(callLog); !strings.Contains(string(log), "thread show T1") {
		t.Errorf("call log = %q", log)
	}
}
