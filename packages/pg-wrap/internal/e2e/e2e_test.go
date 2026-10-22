// Package e2e drives the two real pg-wrap binaries, built once per test run, over
// real specs and real programs on the host (/usr/bin/env, /bin/sh). Nothing here
// mocks pg-wrap: the exit codes, streams and environments are what a caller sees.
package e2e

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

var binDir string

func TestMain(m *testing.M) {
	code, err := build(m)
	if err != nil {
		fmt.Fprintln(os.Stderr, "e2e:", err)
		code = 2
	}
	os.Exit(code)
}

func build(m *testing.M) (int, error) {
	dir, err := os.MkdirTemp("", "pg-wrap-e2e")
	if err != nil {
		return 0, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	binDir = dir
	for _, name := range []string{"pg-wrap", "pg-wrap-exec"} {
		cmd := exec.Command("go", "build", "-o", filepath.Join(dir, name), "./cmd/"+name)
		cmd.Dir = filepath.Join("..", "..")
		if out, err := cmd.CombinedOutput(); err != nil {
			return 0, fmt.Errorf("go build %s: %v\n%s", name, err, out)
		}
	}
	return m.Run(), nil
}

// probeScript is the payload. Its first argument is a report path prefix; the
// remaining argv (NUL-separated) and the environment are written beside it. It
// exits with $PROBE_EXIT (default 0).
const probeScript = `#!/bin/sh
out=$1
shift
: > "$out.argv"
for a in "$@"; do printf '%s\0' "$a" >> "$out.argv"; done
env > "$out.env"
exit "${PROBE_EXIT:-0}"
`

// fixture is one test's scratch directory with a probe program in it.
type fixture struct {
	t     *testing.T
	dir   string
	probe string
	out   string // report prefix
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	f := &fixture{t: t, dir: dir, probe: filepath.Join(dir, "probe.sh"), out: filepath.Join(dir, "report")}
	if err := os.WriteFile(f.probe, []byte(probeScript), 0o755); err != nil {
		t.Fatal(err)
	}
	return f
}

// spec writes a spec file and returns its path.
func (f *fixture) spec(text string) string {
	f.t.Helper()
	p := filepath.Join(f.dir, "spec.toml")
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		f.t.Fatal(err)
	}
	return p
}

type outcome struct {
	exit   int
	stdout string
	stderr string
}

// run executes one of the binaries with exactly the given environment.
func run(t *testing.T, bin string, env []string, args ...string) outcome {
	t.Helper()
	cmd := exec.Command(filepath.Join(binDir, bin), args...)
	cmd.Env = env
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	err := cmd.Run()
	o := outcome{stdout: so.String(), stderr: se.String()}
	var ee *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &ee):
		o.exit = ee.ExitCode()
	default:
		t.Fatalf("%s did not start: %v", bin, err)
	}
	return o
}

// baseEnv is a small environment every run starts from.
func baseEnv(extra ...string) []string {
	return append([]string{"PATH=/usr/bin:/bin", "HOME=/tmp"}, extra...)
}

func (f *fixture) argv() []string {
	b, err := os.ReadFile(f.out + ".argv")
	if err != nil {
		f.t.Fatalf("the payload did not run: %v", err)
	}
	if len(b) == 0 {
		return nil
	}
	return strings.Split(strings.TrimSuffix(string(b), "\x00"), "\x00")
}

func (f *fixture) env() map[string]string {
	b, err := os.ReadFile(f.out + ".env")
	if err != nil {
		f.t.Fatalf("the payload did not run: %v", err)
	}
	m := map[string]string{}
	for _, l := range strings.Split(string(b), "\n") {
		if k, v, ok := strings.Cut(l, "="); ok {
			m[k] = v
		}
	}
	return m
}

func (f *fixture) ran() bool {
	_, err := os.Stat(f.out + ".env")
	return err == nil
}

func names(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ---- run ----

func TestRunAppliesLayersAndKeepsArgvIntact(t *testing.T) {
	f := newFixture(t)
	spec := f.spec(`schema_version = 1
wrap = [
  "/usr/bin/env FROM_STRING=1 'QUOTED=a b'",
  ["/usr/bin/env", "FROM_ARRAY=$(touch SENT)"],
]
`)
	payloadArgs := []string{"a b", "--", `q"uote`, "", "$(touch SENT)", ";touch SENT", "run", "check"}
	args := append([]string{"run", "--spec", spec, "--", f.probe, f.out}, payloadArgs...)
	o := run(t, "pg-wrap", baseEnv(), args...)
	if o.exit != 0 || o.stdout != "" || o.stderr != "" {
		t.Fatalf("exit=%d stdout=%q stderr=%q", o.exit, o.stdout, o.stderr)
	}
	if got := f.argv(); !reflect.DeepEqual(got, payloadArgs) {
		t.Fatalf("argv = %q, want %q", got, payloadArgs)
	}
	env := f.env()
	if env["FROM_STRING"] != "1" || env["QUOTED"] != "a b" {
		t.Fatalf("the string entry did not apply: %v", env)
	}
	if env["FROM_ARRAY"] != "$(touch SENT)" {
		t.Fatalf("an array entry was shell-parsed: %q", env["FROM_ARRAY"])
	}
	if _, err := os.Stat("SENT"); err == nil {
		t.Fatal("a sentinel file was created: something shell-parsed data")
	}
}

func TestRunWithEmptyWrapAndNoExecJustRunsThePayload(t *testing.T) {
	f := newFixture(t)
	spec := f.spec("schema_version = 1\n")
	o := run(t, "pg-wrap", baseEnv(), "run", "--spec", spec, "--", f.probe, f.out, "x")
	if o.exit != 0 {
		t.Fatalf("exit=%d stderr=%q", o.exit, o.stderr)
	}
	if got := f.argv(); !reflect.DeepEqual(got, []string{"x"}) {
		t.Fatalf("argv = %q", got)
	}
}

func TestRunResolvesABarePayloadAgainstTheStartingPath(t *testing.T) {
	f := newFixture(t)
	spec := f.spec("schema_version = 1\nstrict_env = true\n")
	// Under strict_env the PATH is the minimal one, which holds env.
	o := run(t, "pg-wrap", baseEnv("PATH=/nowhere"), "run", "--spec", spec, "--", "env")
	if o.exit != 0 || !strings.Contains(o.stdout, "PATH=/usr/bin:/bin:/usr/sbin:/sbin") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", o.exit, o.stdout, o.stderr)
	}
	_ = f
}

func TestRunExecPrefixPrecedesTheArgs(t *testing.T) {
	f := newFixture(t)
	spec := f.spec(fmt.Sprintf("schema_version = 1\nexec = [%q, %q]\n", f.probe, f.out))
	o := run(t, "pg-wrap", baseEnv(), "run", "--spec", spec, "--", "one", "two words")
	if o.exit != 0 {
		t.Fatalf("exit=%d stderr=%q", o.exit, o.stderr)
	}
	if got := f.argv(); !reflect.DeepEqual(got, []string{"one", "two words"}) {
		t.Fatalf("argv = %q: the payload is exec followed by the args", got)
	}
}

func TestRunPassesThePayloadsExitStatusIncludingNine(t *testing.T) {
	f := newFixture(t)
	spec := f.spec("schema_version = 1\nwrap = [\"/usr/bin/env A=1\"]\n")
	for _, code := range []string{"0", "1", "9", "42", "97", "124"} {
		o := run(t, "pg-wrap", baseEnv("PROBE_EXIT="+code), "run", "--spec", spec, "--", f.probe, f.out)
		if fmt.Sprint(o.exit) != code {
			t.Fatalf("PROBE_EXIT=%s came back as %d (stderr %q)", code, o.exit, o.stderr)
		}
	}
}

func TestRunALayerExitingNineIsPassedThrough(t *testing.T) {
	f := newFixture(t)
	spec := f.spec(`schema_version = 1
wrap = ["/bin/sh -c 'echo busy >&2; exit 9' sh"]
`)
	o := run(t, "pg-wrap", baseEnv(), "run", "--spec", spec, "--", f.probe, f.out)
	if o.exit != 9 || f.ran() {
		t.Fatalf("exit=%d ran=%v: pg-wrap cannot remap a layer's exit", o.exit, f.ran())
	}
}

// ---- the environment ----

func TestStrictEnvKeepsOnlyTheMinimalNamesAndKeepByExactName(t *testing.T) {
	f := newFixture(t)
	spec := f.spec(`schema_version = 1
strict_env = true
keep = ["PA_MONITOR_NO_NUDGE", "TMUX"]
`)
	env := []string{
		"PATH=/usr/bin:/bin", "HOME=/home/x", "USER=u", "LOGNAME=u", "TMPDIR=/t", "LANG=C", "TERM=xterm",
		"COLORTERM=truecolor",
		"PA_MONITOR_NO_NUDGE=1", "PA_MONITOR_NO_NUDGE_EXTRA=2", "TMUX=/tmp/tmux-1/default,1,0",
		"CCPOOL_POOL_X=leak", "CLAUDECODE=", "SECRET_TOKEN=leak", "PG_WRAP_SPEC=" + spec,
	}
	o := run(t, "pg-wrap", env, "run", "--spec", spec, "--", f.probe, f.out)
	if o.exit != 0 {
		t.Fatalf("exit=%d stderr=%q", o.exit, o.stderr)
	}
	got := f.env()
	// The shell adds PWD, SHLVL and _ to what it runs; they are not pg-wrap's.
	for _, shellOwn := range []string{"PWD", "OLDPWD", "SHLVL", "_"} {
		delete(got, shellOwn)
	}
	want := map[string]string{
		"PATH": "/usr/bin:/bin:/usr/sbin:/sbin", "HOME": "/home/x", "USER": "u", "LOGNAME": "u",
		"TMPDIR": "/t", "LANG": "C", "TERM": "xterm", "COLORTERM": "truecolor",
		"PA_MONITOR_NO_NUDGE": "1", "TMUX": "/tmp/tmux-1/default,1,0",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("env = %v\nwant %v (extra names: %v)", got, want, names(got))
	}
}

func TestInheritedEnvPassesThroughWhenStrictEnvIsOff(t *testing.T) {
	f := newFixture(t)
	spec := f.spec("schema_version = 1\n")
	env := baseEnv("CCPOOL_POOL_X=1", "CLAUDECODE=", "PG_WRAP_SPEC="+spec)
	o := run(t, "pg-wrap", env, "run", "--spec", spec, "--", f.probe, f.out)
	if o.exit != 0 {
		t.Fatalf("exit=%d stderr=%q", o.exit, o.stderr)
	}
	got := f.env()
	if v, ok := got["CCPOOL_POOL_X"]; !ok || v != "1" {
		t.Fatalf("inherited CCPOOL_POOL_X lost: %v", got)
	}
	if v, ok := got["CLAUDECODE"]; !ok || v != "" {
		t.Fatalf("inherited empty CLAUDECODE lost: %v", got)
	}
	if _, ok := got["PG_WRAP_SPEC"]; ok {
		t.Fatal("PG_WRAP_SPEC reached the payload")
	}
}

func TestSpecPathVariableIsRemovedEvenWhenKept(t *testing.T) {
	f := newFixture(t)
	spec := f.spec("schema_version = 1\nstrict_env = true\nkeep = [\"PG_WRAP_SPEC\"]\n")
	o := run(t, "pg-wrap", baseEnv("PG_WRAP_SPEC="+spec), "run", "--spec", spec, "--", f.probe, f.out)
	if o.exit != 0 {
		t.Fatalf("exit=%d stderr=%q", o.exit, o.stderr)
	}
	if _, ok := f.env()["PG_WRAP_SPEC"]; ok {
		t.Fatal("PG_WRAP_SPEC reached the payload although keep named it")
	}
}

func TestStrictEnvWithAWrapperStillReachesToolsByAbsolutePath(t *testing.T) {
	f := newFixture(t)
	spec := f.spec("schema_version = 1\nstrict_env = true\nwrap = [\"/usr/bin/env W=1\"]\n")
	o := run(t, "pg-wrap", baseEnv(), "run", "--spec", spec, "--", f.probe, f.out)
	if o.exit != 0 || f.env()["W"] != "1" {
		t.Fatalf("exit=%d stderr=%q", o.exit, o.stderr)
	}
}

// ---- pg-wrap-exec ----

func TestExecWithoutASpecSaysWhyExits97AndNeverStartsThePayload(t *testing.T) {
	f := newFixture(t)
	for name, env := range map[string][]string{
		"unset": baseEnv(),
		"empty": baseEnv("PG_WRAP_SPEC="),
	} {
		t.Run(name, func(t *testing.T) {
			o := run(t, "pg-wrap-exec", env, f.probe, f.out)
			want := "pg-wrap[spec]: PG_WRAP_SPEC is not set in the environment this launcher received; " +
				"a terminal-multiplexer pane does not inherit your shell's variables\n"
			if o.exit != 97 || o.stderr != want || o.stdout != "" {
				t.Fatalf("exit=%d stderr=%q stdout=%q", o.exit, o.stderr, o.stdout)
			}
			if f.ran() {
				t.Fatal("the payload started")
			}
		})
	}
}

func TestExecPassesArgsIntactIncludingALeadingRunOrCheck(t *testing.T) {
	f := newFixture(t)
	spec := f.spec(fmt.Sprintf("schema_version = 1\nexec = [%q, %q]\nwrap = [\"/usr/bin/env W=1\"]\n", f.probe, f.out))
	cases := [][]string{
		{"a b", "--", `q"uote`, "", "$(touch SENT)"},
		{"run", "--spec", "x", "--", "y"},
		{"check", "--spec", "x"},
		{"--", "run"},
		{""},
		{},
	}
	for _, args := range cases {
		_ = os.Remove(f.out + ".argv")
		o := run(t, "pg-wrap-exec", baseEnv("PG_WRAP_SPEC="+spec), args...)
		if o.exit != 0 || o.stderr != "" {
			t.Fatalf("args %q: exit=%d stderr=%q", args, o.exit, o.stderr)
		}
		got := f.argv()
		if !reflect.DeepEqual(got, args) && !(len(got) == 0 && len(args) == 0) {
			t.Fatalf("args %q arrived as %q", args, got)
		}
	}
	if f.env()["W"] != "1" {
		t.Fatal("the layer did not apply")
	}
	if _, ok := f.env()["PG_WRAP_SPEC"]; ok {
		t.Fatal("PG_WRAP_SPEC reached the payload")
	}
}

func TestExecWithABadSpecFailsBeforeThePayload(t *testing.T) {
	f := newFixture(t)
	spec := f.spec("schema_version = 9\n")
	o := run(t, "pg-wrap-exec", baseEnv("PG_WRAP_SPEC="+spec), f.probe, f.out)
	want := "pg-wrap[spec]: " + spec + " has schema_version 9, this pg-wrap supports <= 1.\n"
	if o.exit != 97 || o.stderr != want || f.ran() {
		t.Fatalf("exit=%d stderr=%q ran=%v", o.exit, o.stderr, f.ran())
	}
}

// ---- 97 ----

// Every failure pg-wrap detects exits 97 before the payload, with a
// pg-wrap[<stage>]: message on stderr and nothing on stdout.
func TestEveryDetectedFailureExits97BeforeThePayload(t *testing.T) {
	f := newFixture(t)
	missing := filepath.Join(f.dir, "no-such-layer")
	cases := []struct {
		name   string
		bin    string
		spec   string // spec text; "" means no spec file
		env    []string
		args   func(spec string) []string
		stage  string // expected pg-wrap[<stage>]
		detail string
	}{
		{"unknown key", "pg-wrap", "schema_version = 1\nbogus = 1\n", nil, runArgs(f), "spec", "unknown key"},
		{"wrong type", "pg-wrap", "schema_version = 1\nstrict_env = 3\n", nil, runArgs(f), "spec", "strict_env"},
		{"missing schema_version", "pg-wrap", "strict_env = true\n", nil, runArgs(f), "spec", "schema_version is required"},
		{"newer schema_version", "pg-wrap", "schema_version = 2\n", nil, runArgs(f), "spec", "supports <= 1."},
		{"relative first word", "pg-wrap", "schema_version = 1\nwrap = [\"env A=1\"]\n", nil, runArgs(f), "layer 0", "absolute path"},
		{
			"relative first element, second layer", "pg-wrap",
			"schema_version = 1\nwrap = [\"/usr/bin/env A=1\", [\"env\", \"B=2\"]]\n", nil, runArgs(f), "layer 1", "absolute path",
		},
		{"relative exec", "pg-wrap", "schema_version = 1\nexec = [\"probe\"]\n", nil, runArgs(f), "spec", "exec"},
		{"missing layer", "pg-wrap", "schema_version = 1\nwrap = [[" + quote(missing) + "]]\n", nil, runArgs(f), "exec", "no-such-layer"},
		{
			"missing payload with no layers", "pg-wrap", "schema_version = 1\n", nil,
			func(spec string) []string { return []string{"run", "--spec", spec, "--", missing} }, "exec", "no-such-layer",
		},
		{
			"nothing to run", "pg-wrap", "schema_version = 1\n", nil,
			func(spec string) []string { return []string{"run", "--spec", spec} }, "exec", "nothing to run",
		},
		{"bad keep name", "pg-wrap", "schema_version = 1\nstrict_env = true\nkeep = [\"A=B\"]\n", nil, runArgs(f), "env", "not a variable name"},
		{
			"unreadable spec", "pg-wrap", "", nil,
			func(string) []string {
				return []string{"run", "--spec", filepath.Join(f.dir, "absent.toml"), "--", f.probe}
			}, "spec", "cannot read",
		},
		{
			"no --spec", "pg-wrap", "", nil,
			func(string) []string { return []string{"run", "--", f.probe} }, "spec", "--spec <file> is required",
		},
		{
			"unknown flag", "pg-wrap", "", nil,
			func(string) []string { return []string{"run", "--nope"} }, "spec", "usage:",
		},
		{
			"no command", "pg-wrap", "", nil,
			func(string) []string { return nil }, "spec", "no command given",
		},
		{
			"unknown command", "pg-wrap", "", nil,
			func(string) []string { return []string{"frobnicate"} }, "spec", "unknown command",
		},
		{
			"PG_WRAP_SPEC unset", "pg-wrap-exec", "", nil,
			func(string) []string { return []string{f.probe} }, "spec", "PG_WRAP_SPEC is not set",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_ = os.Remove(f.out + ".env")
			spec := ""
			if c.spec != "" {
				spec = f.spec(c.spec)
			}
			env := baseEnv()
			if c.bin == "pg-wrap-exec" && spec != "" {
				env = baseEnv("PG_WRAP_SPEC=" + spec)
			}
			o := run(t, c.bin, env, c.args(spec)...)
			if o.exit != 97 {
				t.Fatalf("exit = %d, want 97 (stderr %q)", o.exit, o.stderr)
			}
			if o.stdout != "" {
				t.Fatalf("pg-wrap wrote to stdout: %q", o.stdout)
			}
			if !strings.HasPrefix(o.stderr, "pg-wrap["+c.stage+"]: ") || !strings.Contains(o.stderr, c.detail) {
				t.Fatalf("stderr = %q, want pg-wrap[%s]: ... %s", o.stderr, c.stage, c.detail)
			}
			if f.ran() {
				t.Fatal("the payload started although pg-wrap failed")
			}
		})
	}
}

func runArgs(f *fixture) func(string) []string {
	return func(spec string) []string { return []string{"run", "--spec", spec, "--", f.probe, f.out} }
}

func quote(s string) string { return fmt.Sprintf("%q", s) }

// 97 collides with none of the codes a caller reads as something else: 1 (a
// generic error), 9 (busy, retry later), 124 (timeout) and 125-127 (launcher).
func TestFailureCodeIsNoneOfTheReservedOnes(t *testing.T) {
	const code = 97
	reserved := []int{1, 9, 124, 125, 126, 127}
	for _, r := range reserved {
		if code == r {
			t.Fatalf("97 collides with %d", r)
		}
	}
	f := newFixture(t)
	spec := f.spec("schema_version = 2\n")
	o := run(t, "pg-wrap", baseEnv(), "run", "--spec", spec, "--", f.probe)
	for _, r := range reserved {
		if o.exit == r {
			t.Fatalf("a pg-wrap failure exited %d, one of the codes a caller reads otherwise", r)
		}
	}
	if o.exit != code {
		t.Fatalf("exit = %d, want %d", o.exit, code)
	}
}

// ---- check ----

func TestCheckAcceptsAGoodSpecSilently(t *testing.T) {
	f := newFixture(t)
	spec := f.spec(fmt.Sprintf("schema_version = 1\nwrap = [\"/usr/bin/env A=1\", [\"/usr/bin/env\", \"B=2\"]]\nexec = [%q]\n", f.probe))
	o := run(t, "pg-wrap", baseEnv(), "check", "--spec", spec)
	if o.exit != 0 || o.stdout != "" || o.stderr != "" {
		t.Fatalf("exit=%d stdout=%q stderr=%q", o.exit, o.stdout, o.stderr)
	}
	if f.ran() {
		t.Fatal("check started a payload")
	}
}

func TestCheckReportsADirectoryABrokenSymlinkAndANonExecutableFileOneLineEach(t *testing.T) {
	f := newFixture(t)
	dir := filepath.Join(f.dir, "a-directory")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	broken := filepath.Join(f.dir, "broken-link")
	if err := os.Symlink(filepath.Join(f.dir, "no-target"), broken); err != nil {
		t.Fatal(err)
	}
	plain := filepath.Join(f.dir, "plain-file")
	if err := os.WriteFile(plain, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(plain, 0o644); err != nil {
		t.Fatal(err)
	}
	spec := f.spec(fmt.Sprintf("schema_version = 1\nwrap = [%q, [%q, \"arg\"], %q, \"/usr/bin/env A=1\"]\nexec = [%q]\n",
		dir+" x", broken, plain, f.probe))
	o := run(t, "pg-wrap", baseEnv(), "check", "--spec", spec)
	if o.exit != 97 || o.stdout != "" {
		t.Fatalf("exit=%d stdout=%q stderr=%q", o.exit, o.stdout, o.stderr)
	}
	lines := strings.Split(strings.TrimSuffix(o.stderr, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("want one line per problem (3), got %d: %q", len(lines), lines)
	}
	for i, want := range []string{"is a directory", "broken symlink", "is not executable"} {
		if !strings.HasPrefix(lines[i], "pg-wrap[spec]: wrap["+fmt.Sprint(i)+"]: ") || !strings.Contains(lines[i], want) {
			t.Errorf("line %d = %q, want it to name wrap[%d] and %q", i, lines[i], i, want)
		}
	}
	if f.ran() {
		t.Fatal("check started a payload")
	}
}

func TestCheckReportsARelativePathAndAnExecProblem(t *testing.T) {
	f := newFixture(t)
	spec := f.spec("schema_version = 1\nwrap = [\"env A=1\"]\nexec = [\"/no/such/exec\"]\n")
	o := run(t, "pg-wrap", baseEnv(), "check", "--spec", spec)
	if o.exit != 97 {
		t.Fatalf("exit=%d stderr=%q", o.exit, o.stderr)
	}
	lines := strings.Split(strings.TrimSuffix(o.stderr, "\n"), "\n")
	if len(lines) != 2 ||
		!strings.Contains(lines[0], "wrap[0]") || !strings.Contains(lines[0], "absolute path") ||
		!strings.Contains(lines[1], "exec") || !strings.Contains(lines[1], "/no/such/exec") {
		t.Fatalf("lines = %q", lines)
	}
}

func TestCheckDecodesStrictly(t *testing.T) {
	f := newFixture(t)
	spec := f.spec("schema_version = 1\nbogus = true\n")
	o := run(t, "pg-wrap", baseEnv(), "check", "--spec", spec)
	if o.exit != 97 || !strings.Contains(o.stderr, "unknown key bogus") {
		t.Fatalf("exit=%d stderr=%q", o.exit, o.stderr)
	}
}

// ---- show ----

func TestShowPrintsTheComposedLayersWithoutRunningAnything(t *testing.T) {
	f := newFixture(t)
	spec := f.spec(fmt.Sprintf(`schema_version = 1
strict_env = true
keep = ["TMUX"]
wrap = ["/usr/bin/env A=1", ["/usr/bin/env", "B=2 3"]]
exec = [%q]
`, f.probe))
	o := run(t, "pg-wrap", baseEnv(), "show", "--spec", spec)
	if o.exit != 0 || o.stderr != "" {
		t.Fatalf("exit=%d stderr=%q", o.exit, o.stderr)
	}
	want := "schema_version: 1\n" +
		"env: minimal, keep: TMUX\n" +
		"layers:\n" +
		`  0: /bin/sh -c 'exec /usr/bin/env A=1 "$@"' sh` + "\n" +
		"  1: /usr/bin/env 'B=2 3'\n" +
		"exec: " + f.probe + "\n" +
		"payload: " + f.probe + " [args...]\n"
	if o.stdout != want {
		t.Fatalf("stdout:\n%s\nwant:\n%s", o.stdout, want)
	}
	if f.ran() {
		t.Fatal("show started a payload")
	}
}

func TestShowOfAnEmptySpec(t *testing.T) {
	f := newFixture(t)
	spec := f.spec("schema_version = 1\n")
	o := run(t, "pg-wrap", baseEnv(), "show", "--spec", spec)
	want := "schema_version: 1\nenv: inherited\nlayers: none\npayload: [args...]\n"
	if o.exit != 0 || o.stdout != want {
		t.Fatalf("exit=%d stdout=%q", o.exit, o.stdout)
	}
}

func TestShowRefusesABadSpec(t *testing.T) {
	f := newFixture(t)
	spec := f.spec("schema_version = 5\n")
	o := run(t, "pg-wrap", baseEnv(), "show", "--spec", spec)
	if o.exit != 97 || o.stdout != "" || !strings.Contains(o.stderr, "supports <= 1.") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", o.exit, o.stdout, o.stderr)
	}
}

func TestHelpGoesToStdoutAndExitsZero(t *testing.T) {
	o := run(t, "pg-wrap", baseEnv(), "--help")
	if o.exit != 0 || !strings.Contains(o.stdout, "usage: pg-wrap run --spec") {
		t.Fatalf("exit=%d stdout=%q", o.exit, o.stdout)
	}
}
