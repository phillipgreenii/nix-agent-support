package main

import (
	"bytes"
	"regexp"
	"strings"
	"testing"
)

// These tests drive the real `pb actor` cobra command through the real root
// command. Every test sets BOTH session variables explicitly (empty means
// unset to the command), so the live session's own ids never leak in.

const (
	envSessionA = "11111111-2222-4333-8444-555555555555"
	padUUID     = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
)

var randomActorRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}-drain$`)

func runActor(t *testing.T, claudeSession, claudeCodeSession string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	t.Setenv("CLAUDE_SESSION_ID", claudeSession)
	t.Setenv("CLAUDE_CODE_SESSION_ID", claudeCodeSession)
	root := newRootCmd()
	var out, errb bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errb)
	root.SetArgs(append([]string{"actor"}, args...))
	err = root.Execute()
	return out.String(), errb.String(), err
}

func TestActor_envSessionIDGetsRoleSuffix(t *testing.T) {
	out, errOut, err := runActor(t, envSessionA, "", "--role", "drain")
	if err != nil {
		t.Fatal(err)
	}
	if out != envSessionA+"-drain\n" {
		t.Errorf("stdout = %q, want %q", out, envSessionA+"-drain\n")
	}
	if errOut != "" {
		t.Errorf("stderr = %q, want empty", errOut)
	}
}

func TestActor_envSessionIDBeatsScratchpad(t *testing.T) {
	out, _, err := runActor(t, envSessionA, "", "--role", "drain", "--scratchpad", "/tmp/x/"+padUUID+"/scratchpad")
	if err != nil {
		t.Fatal(err)
	}
	if out != envSessionA+"-drain\n" {
		t.Errorf("stdout = %q, want the env id", out)
	}
}

func TestActor_claudeCodeSessionIDIsTheSecondEnvName(t *testing.T) {
	out, _, err := runActor(t, "", envSessionA, "--role", "unstick")
	if err != nil {
		t.Fatal(err)
	}
	if out != envSessionA+"-unstick\n" {
		t.Errorf("stdout = %q", out)
	}
	out, _, err = runActor(t, "documented-name-wins", envSessionA, "--role", "unstick")
	if err != nil {
		t.Fatal(err)
	}
	if out != "documented-name-wins-unstick\n" {
		t.Errorf("stdout = %q, want CLAUDE_SESSION_ID to win", out)
	}
}

func TestActor_scratchpadUUIDWhenNoEnv(t *testing.T) {
	out, errOut, err := runActor(t, "", "", "--role", "unblock", "--scratchpad", "/tmp/claude-1/-proj/"+strings.ToUpper(padUUID)+"/scratchpad")
	if err != nil {
		t.Fatal(err)
	}
	if out != padUUID+"-unblock\n" {
		t.Errorf("stdout = %q, want the lowercased path UUID", out)
	}
	if errOut != "" {
		t.Errorf("stderr = %q, want empty (the id is reproducible)", errOut)
	}
}

func TestActor_randomUUIDWhenNoEnvAndNoUUIDInScratchpad(t *testing.T) {
	for _, args := range [][]string{
		{"--role", "drain"},
		{"--role", "drain", "--scratchpad", "/tmp/claude-1/-proj/not-a-uuid/scratchpad"},
	} {
		out, errOut, err := runActor(t, "", "", args...)
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if !randomActorRe.MatchString(strings.TrimSuffix(out, "\n")) {
			t.Errorf("%v: stdout = %q, want <uuid4>-drain", args, out)
		}
		if !strings.Contains(errOut, "Remember this id") {
			t.Errorf("%v: stderr = %q, want the remember-it note", args, errOut)
		}
		again, _, _ := runActor(t, "", "", args...)
		if again == out {
			t.Errorf("%v: two fresh ids are equal (%q): not random", args, out)
		}
	}
}

func TestActor_roleKeepsActorsDistinctForOneSession(t *testing.T) {
	seen := map[string]string{}
	for _, role := range []string{"drain", "unblock", "unstick"} {
		out, _, err := runActor(t, envSessionA, "", "--role", role)
		if err != nil {
			t.Fatal(err)
		}
		if want := envSessionA + "-" + role + "\n"; out != want {
			t.Errorf("role %s: stdout = %q, want %q", role, out, want)
		}
		if prev, dup := seen[out]; dup {
			t.Errorf("role %s prints the same id as %s", role, prev)
		}
		seen[out] = role
	}
}

func TestActor_unknownRoleExitsNonZeroNamingTheValidRoles(t *testing.T) {
	out, _, err := runActor(t, envSessionA, "", "--role", "bogus")
	if err == nil {
		t.Fatalf("want an error, got stdout %q", out)
	}
	if exitCodeFor(err) == 0 {
		t.Errorf("exit code = 0")
	}
	for _, want := range []string{"bogus", "drain", "unblock", "unstick"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
	if out != "" {
		t.Errorf("stdout = %q, want nothing on failure", out)
	}
}

func TestActor_roleIsRequiredAndOperandsRejected(t *testing.T) {
	if _, _, err := runActor(t, envSessionA, ""); err == nil {
		t.Error("no --role: want an error")
	}
	if _, _, err := runActor(t, envSessionA, "", "--role", "drain", "extra"); err == nil {
		t.Error("stray operand: want an error")
	}
}

func TestActor_rejectsAnUnsafeEnvSessionID(t *testing.T) {
	if out, _, err := runActor(t, "bad id; rm -rf", "", "--role", "drain"); err == nil {
		t.Errorf("want an error for a shell-unsafe id, got stdout %q", out)
	}
}
