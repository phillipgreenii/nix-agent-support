package wrap

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestDecode(t *testing.T) {
	got, err := Decode([]any{"/usr/bin/env A=1", []any{"/bin/sh", "-c"}})
	if err != nil {
		t.Fatal(err)
	}
	want := []Entry{{Shell: "/usr/bin/env A=1"}, {Argv: []string{"/bin/sh", "-c"}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v want %#v", got, want)
	}
	if e, err := Decode(nil); e != nil || err != nil {
		t.Fatalf("nil: %v %v", e, err)
	}
	if e, err := Decode([]any{}); len(e) != 0 || err != nil {
		t.Fatalf("empty list: %v %v", e, err)
	}
	for name, v := range map[string]any{
		"bare string":      "/bin/sh",
		"number element":   []any{1},
		"nested list":      []any{[]any{[]any{"/x"}}},
		"mixed inner":      []any{[]any{"/x", 2}},
		"empty array":      []any{[]any{}},
		"empty string":     []any{""},
		"whitespace only":  []any{"  \t"},
		"map element":      []any{map[string]any{"a": "b"}},
		"bool inner":       []any{[]any{true}},
		"non-list scalar":  42,
		"nil inside array": []any{[]any{nil}},
	} {
		if _, err := Decode(v); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

func TestValidate(t *testing.T) {
	cases := []struct {
		name  string
		e     Entry
		allow bool
		bad   bool
	}{
		{"abs string", Entry{Shell: "/usr/bin/env A=1"}, false, false},
		{"rel string", Entry{Shell: "env A=1"}, false, true},
		{"abs array", Entry{Argv: []string{"/usr/bin/env", "A=1"}}, false, false},
		{"rel array", Entry{Argv: []string{"env", "A=1"}}, false, true},
		{"template in string", Entry{Shell: "/bin/echo {{.BeadID}}"}, true, true},
		{"template in array disallowed", Entry{Argv: []string{"/bin/echo", "{{.BeadID}}"}}, false, true},
		{"template in array allowed", Entry{Argv: []string{"/bin/echo", "{{.BeadID}}"}}, true, false},
		{"unclosed action string", Entry{Shell: "/bin/echo {{"}, true, true},
		{"unclosed action array", Entry{Argv: []string{"/bin/echo", "{{.X"}}, true, true},
		{"templated first element not abs", Entry{Argv: []string{"{{.Bin}}", "x"}}, true, true},
		{"templated first element abs raw", Entry{Argv: []string{"/{{.Bin}}", "x"}}, true, false},
		{"unknown func ok in array", Entry{Argv: []string{"/bin/echo", "{{nosuch .X}}"}}, true, false},
		{"both set", Entry{Shell: "/bin/x", Argv: []string{"/bin/x"}}, true, true},
		{"neither set", Entry{}, true, true},
		{"leading space string", Entry{Shell: "  /bin/echo hi"}, false, false},
	}
	for _, c := range cases {
		err := Validate([]Entry{c.e}, c.allow)
		if (err != nil) != c.bad {
			t.Errorf("%s: err=%v wantBad=%v", c.name, err, c.bad)
		}
	}
	if err := Validate(nil, false); err != nil {
		t.Errorf("empty list must be valid: %v", err)
	}
}

func TestComposeShape(t *testing.T) {
	got, err := Compose([]Entry{
		{Shell: "/usr/bin/env A=1"},
		{Argv: []string{"/bin/echo", "{{.BeadID}}"}},
	}, func(s string) (string, error) { return strings.ReplaceAll(s, "{{.BeadID}}", "pg-1"), nil },
		[]string{"/bin/true", "x y"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/bin/sh", "-c", `exec /usr/bin/env A=1 "$@"`, "sh", "/bin/echo", "pg-1", "/bin/true", "x y"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q want %q", got, want)
	}
	if got, _ := Compose(nil, nil, []string{"/bin/true"}); !reflect.DeepEqual(got, []string{"/bin/true"}) {
		t.Fatalf("empty wrap: %q", got)
	}
	boom := errors.New("boom")
	if _, err := Compose([]Entry{{Argv: []string{"/x", "{{.A}}"}}}, func(string) (string, error) { return "", boom }, nil); !errors.Is(err, boom) {
		t.Fatalf("render error not propagated: %v", err)
	}
	if _, err := Compose([]Entry{{Argv: []string{"/x", "{{.A}}"}}}, nil, nil); err == nil {
		t.Fatal("nil render with template action must fail")
	}
	if _, err := Compose([]Entry{{}}, nil, nil); err == nil {
		t.Fatal("empty entry must fail")
	}
}

// run executes argv under the base env plus the marker variable and returns stdout,
// stderr and the error.
func run(t *testing.T, argv []string, marker string, extraEnv ...string) (string, string, error) {
	t.Helper()
	cmd := exec.Command(argv[0], argv[1:]...)
	env := BaseEnv(os.Environ(), nil)
	if marker != "" {
		env = append(env, ReadyMarkerEnv+"="+marker)
	}
	cmd.Env = append(env, extraEnv...)
	var so, se strings.Builder
	cmd.Stdout, cmd.Stderr = &so, &se
	err := cmd.Run()
	return so.String(), se.String(), err
}

func TestNestedStringEntriesKeepBothPathPrefixes(t *testing.T) {
	entries := []Entry{
		{Shell: "/usr/bin/env PATH=/opt/a:$PATH"},
		{Shell: "/usr/bin/env PATH=/opt/b:$PATH"},
		MarkerStage(),
	}
	argv, err := Compose(entries, nil, []string{"/usr/bin/env"})
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "m")
	out, stderr, err := run(t, argv, marker)
	if err != nil {
		t.Fatalf("%v: %s", err, stderr)
	}
	if !strings.Contains("\n"+out, "\nPATH=/opt/b:/opt/a:/usr/bin:/bin:/usr/sbin:/sbin\n") {
		t.Fatalf("PATH not composed per layer:\n%s", out)
	}
	if !MarkerExists(marker) {
		t.Fatal("marker not written")
	}
}

func TestArrayEntryNotShellParsed(t *testing.T) {
	argv, err := Compose([]Entry{{Argv: []string{"/usr/bin/env", "X={{.V}}"}}, MarkerStage()},
		func(s string) (string, error) { return strings.ReplaceAll(s, "{{.V}}", "a b; $(echo pwned)"), nil },
		[]string{"/usr/bin/env"})
	if err != nil {
		t.Fatal(err)
	}
	out, stderr, err := run(t, argv, filepath.Join(t.TempDir(), "m"))
	if err != nil {
		t.Fatalf("%v: %s", err, stderr)
	}
	if !strings.Contains("\n"+out, "\nX=a b; $(echo pwned)\n") {
		t.Fatalf("item data was shell-parsed or lost:\n%s", out)
	}
}

func TestPayloadArgvNeverShellParsed(t *testing.T) {
	argv, err := Compose([]Entry{{Shell: "/usr/bin/env A=1"}, MarkerStage()}, nil,
		[]string{"/bin/sh", "-c", `printf '%s' "$1"`, "sh", "$(echo pwned); `id`"})
	if err != nil {
		t.Fatal(err)
	}
	out, stderr, err := run(t, argv, filepath.Join(t.TempDir(), "m"))
	if err != nil {
		t.Fatalf("%v: %s", err, stderr)
	}
	if out != "$(echo pwned); `id`" {
		t.Fatalf("payload argv altered: %q", out)
	}
}

func TestMarkerStageEmptyWrapStillWritesMarker(t *testing.T) {
	argv, err := Compose([]Entry{MarkerStage()}, nil, []string{"/bin/sh", "-c", "echo ran"})
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "m")
	out, _, err := run(t, argv, marker)
	if err != nil || out != "ran\n" {
		t.Fatalf("out=%q err=%v", out, err)
	}
	if !MarkerExists(marker) {
		t.Fatal("marker missing for empty wrap list")
	}
	if fi, _ := os.Stat(marker); fi.Size() != 0 {
		t.Fatal("marker must be empty")
	}
}

func TestMarkerStageUnsetOrEmptyFailsBeforePayload(t *testing.T) {
	for _, marker := range []string{"", "EMPTY"} {
		dir := t.TempDir()
		ran := filepath.Join(dir, "ran")
		argv, err := Compose([]Entry{MarkerStage()}, nil, []string{"/bin/sh", "-c", "touch " + ran})
		if err != nil {
			t.Fatal(err)
		}
		var extra []string
		if marker == "EMPTY" {
			extra = []string{ReadyMarkerEnv + "="}
		}
		out, stderr, err := run(t, argv, "", extra...)
		var ee *exec.ExitError
		if !errors.As(err, &ee) || ee.ExitCode() == 0 {
			t.Fatalf("marker=%q: want non-zero exit, got %v", marker, err)
		}
		if !strings.Contains(stderr, "pg-wrap[marker]:") {
			t.Fatalf("stderr lacks pg-wrap[marker]: %q", stderr)
		}
		if out != "" {
			t.Fatalf("marker stage wrote stdout: %q", out)
		}
		if _, err := os.Stat(ran); err == nil {
			t.Fatal("payload ran despite missing marker")
		}
	}
}

func TestMarkerStageUnwritableMarkerFails(t *testing.T) {
	argv, _ := Compose([]Entry{MarkerStage()}, nil, []string{"/bin/sh", "-c", "echo ran"})
	out, stderr, err := run(t, argv, filepath.Join(t.TempDir(), "no-such-dir", "m"))
	if err == nil || out != "" || !strings.Contains(stderr, "pg-wrap[marker]:") {
		t.Fatalf("out=%q stderr=%q err=%v", out, stderr, err)
	}
}

func TestMarkerExists(t *testing.T) {
	if MarkerExists("") || MarkerExists(filepath.Join(t.TempDir(), "x")) {
		t.Fatal("false positive")
	}
}

func TestMarkerStageShape(t *testing.T) {
	m := MarkerStage()
	if m.Shell != "" || len(m.Argv) != 4 || m.Argv[0] != "/bin/sh" || m.Argv[1] != "-c" || m.Argv[3] != "sh" {
		t.Fatalf("unexpected stage %#v", m)
	}
	if err := Validate([]Entry{m}, false); err != nil {
		t.Fatalf("marker stage must pass validation: %v", err)
	}
}

func TestExitStatusPassesThrough(t *testing.T) {
	argv, _ := Compose([]Entry{{Shell: "/usr/bin/env A=1"}, MarkerStage()}, nil, []string{"/bin/sh", "-c", "exit 7"})
	_, _, err := run(t, argv, filepath.Join(t.TempDir(), "m"))
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 7 {
		t.Fatalf("want exit 7, got %v", err)
	}
}

func TestBaseEnv(t *testing.T) {
	src := []string{
		"HOME=/h", "USER=u", "LOGNAME=l", "TMPDIR=/t", "LANG=C", "TERM=xterm", "COLORTERM=truecolor",
		"PATH=/evil", "SECRET=x", "BEADS_ACTOR=a", "BEADS_OTHER=nope", "HOMEX=bad", "EMPTY=",
		"CLAUDECODE=1", "malformed",
	}
	got := BaseEnv(src, []string{"BEADS_ACTOR", "EMPTY", "UNSET", "PATH", "BEADS_ACTOR"})
	want := []string{
		"HOME=/h", "USER=u", "LOGNAME=l", "TMPDIR=/t", "LANG=C", "TERM=xterm", "COLORTERM=truecolor",
		"BEADS_ACTOR=a", "EMPTY=", "PATH=/usr/bin:/bin:/usr/sbin:/sbin",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q\nwant %q", got, want)
	}
	if g := BaseEnv(nil, nil); !reflect.DeepEqual(g, []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin"}) {
		t.Fatalf("empty source: %q", g)
	}
	if g := BaseEnv([]string{"HOME=/a", "HOME=/b"}, nil); g[0] != "HOME=/b" {
		t.Fatalf("last wins: %q", g)
	}
}

func TestBaseEnvDropsUnlistedVariableInPayload(t *testing.T) {
	t.Setenv("PG_WRAP_TEST_SECRET", "leak")
	argv, _ := Compose([]Entry{MarkerStage()}, nil, []string{"/usr/bin/env"})
	out, _, err := run(t, argv, filepath.Join(t.TempDir(), "m"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "PG_WRAP_TEST_SECRET") {
		t.Fatalf("unlisted var leaked:\n%s", out)
	}
}

func TestDisplay(t *testing.T) {
	got := Display([]string{"/bin/sh", "-c", `exec /usr/bin/env A=1 "$@"`, "sh", "", "it's"})
	want := `/bin/sh -c 'exec /usr/bin/env A=1 "$@"' sh '' 'it'\''s'`
	if got != want {
		t.Fatalf("got %s want %s", got, want)
	}
	if Display(nil) != "" {
		t.Fatal("empty argv")
	}
}

func waitFile(t *testing.T, p string) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(p); err == nil && strings.HasSuffix(string(b), "\n") {
			return strings.TrimSpace(string(b))
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", p)
	return ""
}

func gone(pid int) bool {
	return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
}

func waitGone(t *testing.T, pid int, what string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if gone(pid) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s (pid/pgid %d) still alive after cancel", what, pid)
}

func cancelTest(t *testing.T, script string, grace time.Duration) {
	t.Helper()
	pidFile := filepath.Join(t.TempDir(), "sleep.pid")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", script, "sh", pidFile)
	Group(cmd, grace)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pgid := cmd.Process.Pid
	sleepPid, err := strconv.Atoi(waitFile(t, pidFile))
	if err != nil {
		t.Fatal(err)
	}
	if g, err := syscall.Getpgid(sleepPid); err != nil || g != pgid {
		t.Fatalf("sleep not in the payload group: pgid=%d err=%v want %d", g, err, pgid)
	}
	cancel()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Wait did not return after cancel")
	}
	waitGone(t, sleepPid, "sleep child")
	waitGone(t, -pgid, "process group")
}

func TestGroupCancelLeavesNoOrphan(t *testing.T) {
	cancelTest(t, `sleep 600 & echo $! > "$1.tmp"; mv "$1.tmp" "$1"; wait`, 2*time.Second)
}

func TestGroupCancelEscalatesToSigkill(t *testing.T) {
	// SIGTERM is ignored by the shell and (inherited) by sleep, so only the
	// group SIGKILL after grace can end the chain.
	cancelTest(t, `trap '' TERM; sleep 600 & echo $! > "$1.tmp"; mv "$1.tmp" "$1"; wait`, 200*time.Millisecond)
}
