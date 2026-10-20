package wrap_test

// Helpers for the wrapper contract suite (contract_*_test.go). The suite runs
// on the host against the REAL tools (/usr/bin/env, /bin/sh, GNU timeout, flox,
// nix develop, tmux). A case whose tool is absent skips with a one-line reason
// naming the tool; /usr/bin/env and /bin/sh are base-system tools and never skip.
//
// The tests stand in for an adapter: each builds an entry list, appends
// wrap.MarkerStage() as the LAST entry, calls wrap.Compose and runs the argv under
// wrap.BaseEnv(...) plus PG_WRAP_READY_MARKER, with a probe script as the payload.

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"text/template"
	"time"

	"github.com/phillipgreenii/pg-router/wrap"
)

const (
	envFloxFixture = "PG_WRAP_CONTRACT_FLOX_ENV"
	envNixFixture  = "PG_WRAP_CONTRACT_NIX_FLAKE"

	// chainTimeout is the WC-6 hang guard: a chain that prompts or hangs fails.
	chainTimeout = 20 * time.Second
)

// probeScript is the payload. Its first argument is a report path prefix; the
// remaining argv, stdin, env, pid, process group, foreground group and tty are
// written to files beside it. It exits with $PROBE_EXIT (default 0). With
// PROBE_WAIT set it traps TERM/INT/HUP (recording which one arrived), writes
// "<prefix>.ready" and waits.
const probeScript = `#!/bin/sh
out=$1
shift
printf '%s\0' "$@" > "$out.argv"
if [ ! -t 0 ]; then cat > "$out.stdin"; fi
env | sort > "$out.env"
set -- $(ps -o pgid=,tpgid=,tty= -p $$)
printf '%s %s %s %s\n' "$$" "$1" "$2" "$3" > "$out.proc"
echo PROBE-STDOUT
echo probe-stderr >&2
if [ -n "${PROBE_WAIT:-}" ]; then
  sleep 600 &
  child=$!
  trap 'echo TERM > "$out.sig"; kill $child 2>/dev/null; exit 143' TERM
  trap 'echo INT > "$out.sig"; kill $child 2>/dev/null; exit 130' INT
  trap 'echo HUP > "$out.sig"; kill $child 2>/dev/null; exit 129' HUP
  : > "$out.ready"
  wait
fi
exit "${PROBE_EXIT:-0}"
`

// report is what the probe observed about itself.
type report struct {
	argv  []string
	stdin string
	env   map[string]string
	pid   int
	pgid  int
	tpgid int
	tty   string
}

func readReport(t *testing.T, prefix string) *report {
	t.Helper()
	procB, err := os.ReadFile(prefix + ".proc")
	if err != nil {
		return nil
	}
	r := &report{env: map[string]string{}}
	f := strings.Fields(string(procB))
	if len(f) != 4 {
		t.Fatalf("bad probe proc line %q", procB)
	}
	r.pid, _ = strconv.Atoi(f[0])
	r.pgid, _ = strconv.Atoi(f[1])
	r.tpgid, _ = strconv.Atoi(f[2])
	r.tty = f[3]
	if b, err := os.ReadFile(prefix + ".argv"); err == nil && len(b) > 0 {
		r.argv = strings.Split(strings.TrimSuffix(string(b), "\x00"), "\x00")
	}
	if b, err := os.ReadFile(prefix + ".stdin"); err == nil {
		r.stdin = string(b)
	}
	if b, err := os.ReadFile(prefix + ".env"); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			if i := strings.IndexByte(l, '='); i > 0 {
				r.env[l[:i]] = l[i+1:]
			}
		}
	}
	return r
}

// newProbe writes the probe script under dir and returns its absolute path.
func newProbe(t *testing.T, dir string) string {
	t.Helper()
	p := filepath.Join(dir, "probe.sh")
	if err := os.WriteFile(p, []byte(probeScript), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// runOpts describes one chain run.
type runOpts struct {
	entries []wrap.Entry
	render  wrap.RenderFunc
	args    []string // payload argv after the report prefix
	stdin   string   // empty means /dev/null
	env     []string // extra NAME=value appended after the base env
	source  []string // BaseEnv source; nil means os.Environ()

	noMarker      bool // leave PG_WRAP_READY_MARKER unset
	emptyMarker   bool // set PG_WRAP_READY_MARKER to ""
	noMarkerStage bool // do not append wrap.MarkerStage()
	group         bool // start under wrap.Group
}

// chain is a built, not yet started, wrap chain.
type chain struct {
	cmd    *exec.Cmd
	stdout *bytes.Buffer
	stderr *bytes.Buffer
	prefix string // probe report prefix
	marker string
	dir    string // working directory (sentinel files would appear here)
	argv   []string
	cancel context.CancelFunc
}

func buildChain(t *testing.T, o runOpts) *chain {
	t.Helper()
	dir := t.TempDir()
	probe := newProbe(t, dir)
	c := &chain{
		prefix: filepath.Join(dir, "report"),
		marker: filepath.Join(dir, "ready.marker"),
		dir:    filepath.Join(dir, "cwd"),
		stdout: &bytes.Buffer{},
		stderr: &bytes.Buffer{},
	}
	if err := os.Mkdir(c.dir, 0o755); err != nil {
		t.Fatal(err)
	}
	entries := append([]wrap.Entry(nil), o.entries...)
	if !o.noMarkerStage {
		entries = append(entries, wrap.MarkerStage())
	}
	payload := append([]string{probe, c.prefix}, o.args...)
	argv, err := wrap.Compose(entries, o.render, payload)
	if err != nil {
		t.Fatal(err)
	}
	c.argv = argv
	ctx, cancel := context.WithTimeout(context.Background(), chainTimeout)
	c.cancel = cancel
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	if o.group {
		wrap.Group(cmd, time.Second)
	}
	src := o.source
	if src == nil {
		src = os.Environ()
	}
	env := wrap.BaseEnv(src, nil)
	switch {
	case o.noMarker:
	case o.emptyMarker:
		env = append(env, wrap.ReadyMarkerEnv+"=")
	default:
		env = append(env, wrap.ReadyMarkerEnv+"="+c.marker)
	}
	cmd.Env = append(env, o.env...)
	cmd.Dir = c.dir
	if o.stdin != "" {
		cmd.Stdin = strings.NewReader(o.stdin)
	}
	cmd.Stdout, cmd.Stderr = c.stdout, c.stderr
	c.cmd = cmd
	return c
}

// result is one finished run.
type result struct {
	*chain
	exit     int // -1 when the chain did not exit normally
	startErr error
	rep      *report
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

func runChain(t *testing.T, o runOpts) result {
	t.Helper()
	c := buildChain(t, o)
	err := c.cmd.Run()
	var ee *exec.ExitError
	if err != nil && !errors.As(err, &ee) {
		t.Fatalf("chain did not run: %v (argv %s)", err, wrap.Display(c.argv))
	}
	return result{chain: c, exit: exitCode(err), rep: readReport(t, c.prefix)}
}

// noSentinel fails when item data was executed by a shell: every hostile value
// contains `touch SENT`, so a file SENT in the working directory means a layer
// parsed item data as shell text.
func noSentinel(t *testing.T, c *chain) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(c.dir, "SENT")); err == nil {
		t.Fatal("item data was executed as shell text: sentinel file SENT exists")
	}
}

// ---- tool discovery and skipping ----

func lookTool(t *testing.T, name string) string {
	t.Helper()
	p, err := exec.LookPath(name)
	if err != nil {
		t.Skipf("%s is not on PATH", name)
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		t.Skipf("%s: %v", name, err)
	}
	return abs
}

// gnuTimeout returns the absolute path of GNU timeout (timeout, else gtimeout),
// accepted only when --version prints "GNU coreutils".
func gnuTimeout(t *testing.T) string {
	t.Helper()
	for _, n := range []string{"timeout", "gtimeout"} {
		p, err := exec.LookPath(n)
		if err != nil {
			continue
		}
		out, err := exec.Command(p, "--version").CombinedOutput()
		if err == nil && strings.Contains(string(out), "GNU coreutils") {
			abs, _ := filepath.Abs(p)
			return abs
		}
	}
	t.Skip("GNU timeout (timeout or gtimeout from coreutils) is not on PATH")
	return ""
}

func fixture(t *testing.T, tool, envName string) string {
	t.Helper()
	v := os.Getenv(envName)
	if v == "" {
		t.Skipf("%s is not set (a locked, prebuilt %s fixture is required)", envName, tool)
	}
	return v
}

func floxEntry(t *testing.T) wrap.Entry {
	t.Helper()
	flox := lookTool(t, "flox")
	env := fixture(t, "flox", envFloxFixture)
	return wrap.Entry{Argv: []string{flox, "activate", "-d", env, "--"}}
}

func nixDevelopEntry(t *testing.T) wrap.Entry {
	t.Helper()
	nix := lookTool(t, "nix")
	ref := fixture(t, "nix develop", envNixFixture)
	return wrap.Entry{Argv: []string{nix, "develop", ref, "-c"}}
}

// wrapperCase is one real wrapper, resolved lazily so absence skips the subtest.
type wrapperCase struct {
	name string
	// exec is true when the wrapper execs its payload (the payload keeps the
	// wrapper's pid); false for timeout, which stays as the parent.
	exec  bool
	entry func(t *testing.T) wrap.Entry
}

func wrapperCases() []wrapperCase {
	return []wrapperCase{
		{"env", true, func(t *testing.T) wrap.Entry {
			return wrap.Entry{Shell: "/usr/bin/env WRAPPED=env"}
		}},
		{"sh-string", true, func(t *testing.T) wrap.Entry {
			return wrap.Entry{Shell: `/bin/sh -c 'exec "$@"' sh`}
		}},
		{"timeout", false, func(t *testing.T) wrap.Entry {
			return wrap.Entry{Argv: []string{gnuTimeout(t), "60"}}
		}},
		{"timeout-foreground", false, func(t *testing.T) wrap.Entry {
			return wrap.Entry{Argv: []string{gnuTimeout(t), "--foreground", "60"}}
		}},
		{"flox", true, floxEntry},
		{"nix-develop", true, nixDevelopEntry},
	}
}

// eachWrapper runs fn once per real wrapper as a subtest.
func eachWrapper(t *testing.T, fn func(t *testing.T, w wrapperCase, e wrap.Entry)) {
	t.Helper()
	for _, w := range wrapperCases() {
		t.Run(w.name, func(t *testing.T) {
			fn(t, w, w.entry(t))
		})
	}
}

// itemRender renders array-entry elements with text/template over a small item,
// the same engine family as the handler's renderArgv.
type item struct{ ItemID string }

func itemRender(it item) wrap.RenderFunc {
	return func(elem string) (string, error) {
		tp, err := template.New("e").Option("missingkey=error").Parse(elem)
		if err != nil {
			return "", err
		}
		var b bytes.Buffer
		if err := tp.Execute(&b, it); err != nil {
			return "", err
		}
		return b.String(), nil
	}
}

// hostile is item data that breaks anything which shell-parses it. Every shell
// metacharacter case that could execute carries `touch SENT`.
var hostile = []string{
	"$(touch SENT)",
	";touch SENT",
	"`touch SENT`",
	"'single'; touch SENT",
	`"double" && touch SENT`,
	"a b  c",
	"$HOME",
	"*",
	"~",
	"",
	"-n",
	"--",
	"line1\nline2",
	`back\slash`,
	"%s %d",
}
