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
		name string
		e    Entry
		bad  bool
	}{
		{"abs string", Entry{Shell: "/usr/bin/env A=1"}, false},
		{"rel string", Entry{Shell: "env A=1"}, true},
		{"abs array", Entry{Argv: []string{"/usr/bin/env", "A=1"}}, false},
		{"rel array", Entry{Argv: []string{"env", "A=1"}}, true},
		{"braces are literal text in a string entry", Entry{Shell: "/bin/echo {{.BeadID}}"}, false},
		{"braces are literal text in an array entry", Entry{Argv: []string{"/bin/echo", "{{.BeadID}}"}}, false},
		{"braces in the first element are not absolute", Entry{Argv: []string{"{{.Bin}}", "x"}}, true},
		{"both set", Entry{Shell: "/bin/x", Argv: []string{"/bin/x"}}, true},
		{"neither set", Entry{}, true},
		{"leading space string", Entry{Shell: "  /bin/echo hi"}, false},
		{"variable first word", Entry{Shell: "$HOME/bin/x"}, true},
	}
	for _, c := range cases {
		err := Validate([]Entry{c.e})
		if (err != nil) != c.bad {
			t.Errorf("%s: err=%v wantBad=%v", c.name, err, c.bad)
		}
	}
	if err := Validate(nil); err != nil {
		t.Errorf("empty list must be valid: %v", err)
	}
	err := Validate([]Entry{{Shell: "/bin/x"}, {Argv: []string{"rel"}}})
	if err == nil || !strings.HasPrefix(err.Error(), "wrap[1]:") {
		t.Errorf("the failing entry's index must lead the message: %v", err)
	}
}

func TestComposeShape(t *testing.T) {
	got, err := Compose([]Entry{
		{Shell: "/usr/bin/env A=1"},
		{Argv: []string{"/bin/echo", "{{.BeadID}}"}},
	}, []string{"/bin/true", "x y"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/bin/sh", "-c", `exec /usr/bin/env A=1 "$@"`, "sh", "/bin/echo", "{{.BeadID}}", "/bin/true", "x y"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q want %q", got, want)
	}
	if got, _ := Compose(nil, []string{"/bin/true"}); !reflect.DeepEqual(got, []string{"/bin/true"}) {
		t.Fatalf("empty wrap: %q", got)
	}
	if _, err := Compose([]Entry{{}}, nil); err == nil {
		t.Fatal("empty entry must fail")
	}
}

func TestLayer(t *testing.T) {
	got, err := Layer(Entry{Shell: "/usr/bin/env A=1"})
	if err != nil || !reflect.DeepEqual(got, []string{"/bin/sh", "-c", `exec /usr/bin/env A=1 "$@"`, "sh"}) {
		t.Fatalf("string layer: %q %v", got, err)
	}
	argv := []string{"/usr/bin/env", "A=1"}
	got, err = Layer(Entry{Argv: argv})
	if err != nil || !reflect.DeepEqual(got, argv) {
		t.Fatalf("array layer: %q %v", got, err)
	}
	got[0] = "changed"
	if argv[0] != "/usr/bin/env" {
		t.Fatal("Layer must copy an array entry's elements")
	}
	if _, err := Layer(Entry{}); err == nil {
		t.Fatal("an empty entry is not a layer")
	}
}

func TestFirstWord(t *testing.T) {
	for _, c := range []struct {
		e    Entry
		want string
	}{
		{Entry{Shell: "  /usr/bin/env A=1"}, "/usr/bin/env"},
		{Entry{Argv: []string{"/x", "y"}}, "/x"},
		{Entry{}, ""},
	} {
		if got := c.e.FirstWord(); got != c.want {
			t.Errorf("FirstWord(%+v) = %q, want %q", c.e, got, c.want)
		}
	}
}

// run executes argv under the base env and returns stdout, stderr and the error.
func run(t *testing.T, argv []string, extraEnv ...string) (string, string, error) {
	t.Helper()
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = append(BaseEnv(os.Environ(), nil), extraEnv...)
	var so, se strings.Builder
	cmd.Stdout, cmd.Stderr = &so, &se
	err := cmd.Run()
	return so.String(), se.String(), err
}

func TestNestedStringEntriesKeepBothPathPrefixes(t *testing.T) {
	entries := []Entry{
		{Shell: "/usr/bin/env PATH=/opt/a:$PATH"},
		{Shell: "/usr/bin/env PATH=/opt/b:$PATH"},
	}
	argv, err := Compose(entries, []string{"/usr/bin/env"})
	if err != nil {
		t.Fatal(err)
	}
	out, stderr, err := run(t, argv)
	if err != nil {
		t.Fatalf("%v: %s", err, stderr)
	}
	if !strings.Contains("\n"+out, "\nPATH=/opt/b:/opt/a:/usr/bin:/bin:/usr/sbin:/sbin\n") {
		t.Fatalf("PATH not composed per layer:\n%s", out)
	}
}

func TestArrayEntryNotShellParsed(t *testing.T) {
	argv, err := Compose([]Entry{{Argv: []string{"/usr/bin/env", "X=a b; $(echo pwned)"}}}, []string{"/usr/bin/env"})
	if err != nil {
		t.Fatal(err)
	}
	out, stderr, err := run(t, argv)
	if err != nil {
		t.Fatalf("%v: %s", err, stderr)
	}
	if !strings.Contains("\n"+out, "\nX=a b; $(echo pwned)\n") {
		t.Fatalf("array element was shell-parsed or lost:\n%s", out)
	}
}

func TestPayloadArgvNeverShellParsed(t *testing.T) {
	argv, err := Compose([]Entry{{Shell: "/usr/bin/env A=1"}},
		[]string{"/bin/sh", "-c", `printf '%s' "$1"`, "sh", "$(echo pwned); `id`"})
	if err != nil {
		t.Fatal(err)
	}
	out, stderr, err := run(t, argv)
	if err != nil {
		t.Fatalf("%v: %s", err, stderr)
	}
	if out != "$(echo pwned); `id`" {
		t.Fatalf("payload argv altered: %q", out)
	}
}

func TestEmptyWrapListRunsThePayload(t *testing.T) {
	argv, err := Compose(nil, []string{"/bin/sh", "-c", "echo ran"})
	if err != nil {
		t.Fatal(err)
	}
	out, _, err := run(t, argv)
	if err != nil || out != "ran\n" {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

func TestExitStatusPassesThrough(t *testing.T) {
	argv, _ := Compose([]Entry{{Shell: "/usr/bin/env A=1"}}, []string{"/bin/sh", "-c", "exit 7"})
	_, _, err := run(t, argv)
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
	out, _, err := run(t, []string{"/usr/bin/env"})
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
