package servetest

import (
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// runShim runs the fake pjira the way the daemon does: under exactly the
// environment the shim's own directory and PATH make.
func runShim(t *testing.T, s *Shim, args ...string) (stdout string, exit int) {
	t.Helper()
	cmd := exec.Command(s.Path, args...)
	cmd.Dir = "/"
	cmd.Env = []string{"FAKE_PJIRA_DIR=" + s.Dir, "PATH=" + DaemonPath()}
	out, err := cmd.Output()
	if ee, ok := err.(*exec.ExitError); ok {
		return string(out), ee.ExitCode()
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(out), 0
}

func TestShimAnswersSearchesFromTheIssuesAndQueriesItWasGiven(t *testing.T) {
	s := newShim(t, ShortDir(t))
	a, b := Open("EXA-1"), Open("EXA-2")
	s.SetIssues(MineJQL, a, b)

	out, code := runShim(t, s, "search", "--jql", MineJQL, "--all", "--expand", "changelog,comments")
	if code != 0 || !strings.Contains(out, `"key":"EXA-1"`) || !strings.Contains(out, `"key":"EXA-2"`) || !strings.Contains(out, `"truncated":false`) {
		t.Errorf("a configured query: exit %d %s", code, out)
	}
	out, _ = runShim(t, s, "search", "--jql", `key in ("EXA-2", "EXA-9")`, "--all")
	if strings.Contains(out, "EXA-1") || !strings.Contains(out, `"key":"EXA-2"`) || strings.Contains(out, "EXA-9") {
		t.Errorf("a key batch answers the keys it shows and only those: %s", out)
	}
	out, _ = runShim(t, s, "search", "--jql", `key = "EXA-1"`, "--all")
	if !strings.Contains(out, `"key":"EXA-1"`) || strings.Contains(out, "EXA-2") {
		t.Errorf("a search by key: %s", out)
	}
	out, _ = runShim(t, s, "search", "--jql", "reporter = currentUser() ORDER BY created DESC", "--limit", "1")
	if !strings.Contains(out, "reporter") {
		t.Errorf("the identity lookup carries a reporter: %s", out)
	}

	s.Truncated(true)
	if out, _ = runShim(t, s, "search", "--jql", MineJQL, "--all"); !strings.Contains(out, `"truncated":true`) {
		t.Errorf("truncated: %s", out)
	}
	s.RemoveIssue("EXA-1")
	if out, _ = runShim(t, s, "search", "--jql", MineJQL, "--all"); strings.Contains(out, "EXA-1") {
		t.Errorf("a removed issue is absent: %s", out)
	}
}

func TestShimIssueFailuresAndTheCallLog(t *testing.T) {
	s := newShim(t, ShortDir(t))
	s.SetIssue(Open("EXA-1"))
	if out, code := runShim(t, s, "issue", "--", "EXA-1"); code != 0 || !strings.Contains(out, `"key":"EXA-1"`) {
		t.Errorf("issue: exit %d %s", code, out)
	}
	if _, code := runShim(t, s, "issue", "--", "EXA-404"); code != 1 {
		t.Errorf("an unknown key exits 1, got %d", code)
	}
	s.FailVerb("auth-status", 401)
	if _, code := runShim(t, s, "auth-status"); code != 1 {
		t.Errorf("a failing verb exits 1, got %d", code)
	}
	s.FailVerb("auth-status", 0)
	if out, code := runShim(t, s, "auth-status"); code != 0 || strings.TrimSpace(out) != "OK" {
		t.Errorf("auth-status after the failure ended: exit %d %q", code, out)
	}
	runShim(t, s, "comment", "--", "EXA-1", "hello")

	var kinds []string
	for _, c := range s.Calls() {
		kinds = append(kinds, c.Kind())
	}
	if want := []string{"issue", "issue", "auth_status", "auth_status", "write"}; !slices.Equal(kinds, want) {
		t.Errorf("kinds = %v, want %v", kinds, want)
	}
	s.ClearCalls()
	if n := len(s.Calls()); n != 0 {
		t.Errorf("ClearCalls left %d calls", n)
	}
}

func TestShimRecordsTheEnvironmentItRunsUnder(t *testing.T) {
	s := newShim(t, ShortDir(t))
	s.RecordEnv()
	runShim(t, s, "auth-status")
	envs := s.Env()
	if len(envs) != 1 {
		t.Fatalf("recorded %d environments, want 1", len(envs))
	}
	joined := strings.Join(envs[0], "\n")
	if !strings.Contains(joined, "FAKE_PJIRA_DIR="+s.Dir) {
		t.Errorf("environment %q lacks the directory", joined)
	}
}

func TestSanitizeMatchesTheScript(t *testing.T) {
	if got := Sanitize(`a = "b" AND c`); got != "a____b__AND_c" {
		t.Errorf("Sanitize = %q", got)
	}
}
