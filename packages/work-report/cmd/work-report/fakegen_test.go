package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/work-report/internal/runner"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/work-report/internal/store"
)

// The fake generator is this test binary re-executed: TestMain reads
// WR_FAKE_GEN and behaves as a generator instead of running tests. It needs
// no separate build and works on macOS and Linux. Its registration MUST list
// WR_FAKE_GEN (and WR_FAKE_DUMP) in its env, because the environment is an
// allowlist. Which behavior it shows is the preset config's "fake" key, so
// one generator registration serves presets that need different behaviors.
//
// A second re-execution runs main() itself (WR_AS_MAIN=1), so the exit-code
// test drives the real command line, process exit status included.

const (
	envFakeGen = "WR_FAKE_GEN"
	envDump    = "WR_FAKE_DUMP"
	envAsMain  = "WR_AS_MAIN"
)

// fakeDump is what the fake generator writes to WR_FAKE_DUMP: the envelope it
// was given, the environment it saw and its working directory.
type fakeDump struct {
	Env      []string       `json:"env"`
	Cwd      string         `json:"cwd"`
	Envelope map[string]any `json:"envelope"`
}

const fakeManifest = `{"contract_version":1,"files":[{"path":"report.md","content_type":"text/markdown","title":"Fake"}],` +
	`"summary":"fake summary","warnings":["fake warning"]}`

// fakeGenMain is the fake generator as a re-executed process.
func fakeGenMain() int {
	raw, _ := io.ReadAll(os.Stdin)
	cwd, _ := os.Getwd()
	return fakeGen(raw, cwd, os.Environ(), os.Stdout, os.Stderr)
}

// fakeGen is the fake generator's behavior over explicit inputs, so the same
// code serves the re-executed process and the in-process exec seam: raw is
// the envelope, dir the working directory, environ the environment.
func fakeGen(raw []byte, dir string, environ []string, stdout, stderr io.Writer) int {
	var env map[string]any
	if err := json.Unmarshal(raw, &env); err != nil {
		fmt.Fprintln(stderr, "fake generator: bad envelope:", err)
		return 2
	}
	var dumpPath string
	for _, kv := range environ {
		if v, ok := strings.CutPrefix(kv, envDump+"="); ok {
			dumpPath = v
		}
	}
	if dumpPath != "" {
		b, _ := json.Marshal(fakeDump{Env: environ, Cwd: dir, Envelope: env})
		_ = os.WriteFile(dumpPath, b, 0o600)
	}
	write := func(name string, data []byte) { _ = os.WriteFile(filepath.Join(dir, name), data, 0o600) }
	cfg, _ := env["config"].(map[string]any)
	behavior, _ := cfg["fake"].(string)
	validate := env["mode"] == "validate"

	switch behavior {
	case "reject":
		fmt.Fprintln(stderr, "promptFile: cannot read /nonexistent/addendum.txt")
		fmt.Fprintln(stderr, "model: unknown")
		return 7
	case "crash":
		fmt.Fprintln(stderr, "boom: entry text echoed")
		return 1
	case "needsconfig":
		if len(cfg) == 0 {
			fmt.Fprintln(stderr, "model is required")
			return 7
		}
	case "hang":
		time.Sleep(time.Minute)
	case "dirty":
		if validate {
			write("stray.txt", []byte("x"))
			return 0
		}
	}
	if validate {
		return 0
	}
	switch behavior {
	case "twofiles":
		write("a.md", []byte("# a\n"))
		write("b.md", []byte("# b\n"))
		fmt.Fprint(stdout, `{"contract_version":1,"files":[{"path":"a.md","content_type":"text/markdown"},{"path":"b.md","content_type":"text/markdown"}]}`)
		return 0
	case "binary":
		write("report.bin", []byte{0, 1, 2})
		fmt.Fprint(stdout, `{"contract_version":1,"files":[{"path":"report.bin","content_type":"application/octet-stream"}]}`)
		return 0
	case "big", "exact":
		n := printLimit
		if behavior == "big" {
			n++
		}
		write("report.md", []byte(strings.Repeat("x", n)))
		fmt.Fprint(stdout, `{"contract_version":1,"files":[{"path":"report.md","content_type":"text/markdown"}]}`)
		return 0
	}
	write("report.md", []byte("# fake report\n"))
	fmt.Fprint(stdout, fakeManifest)
	return 0
}

// inProcessRunner is the Runner the unit tests use: the fake generator runs
// in this process through the exec seam instead of as a re-executed test
// binary, which costs a process launch per run. A generator that is not the
// fake (a missing command) or that hangs needs a real process and gets one.
// Tests that guard process behavior call realProcesses.
func inProcessRunner() *runner.Runner {
	exe, _ := os.Executable()
	real := runner.New(Version)
	r := runner.New(Version)
	r.ExecFn = func(ctx context.Context, spec runner.ExecSpec) runner.ExecResult {
		var env struct {
			Config map[string]any `json:"config"`
		}
		_ = json.Unmarshal(spec.Stdin, &env)
		if len(spec.Argv) == 0 || spec.Argv[0] != exe || env.Config["fake"] == "hang" {
			return real.Exec(ctx, spec)
		}
		var out, errOut bytes.Buffer
		code := fakeGen(spec.Stdin, spec.Dir, spec.Env, &out, &errOut)
		return runner.ExecResult{Stdout: out.Bytes(), Stderr: errOut.String(), ExitCode: &code}
	}
	return r
}

// realProcesses makes the test run every generator as a real process.
func realProcesses(t *testing.T) {
	t.Helper()
	saved := newRunner
	newRunner = func() *runner.Runner { return runner.New(Version) }
	t.Cleanup(func() { newRunner = saved })
}

// shortBusyTimeout shortens the SQLite busy timeout the commands apply, so a
// test of a locked store does not wait out the production five seconds.
func shortBusyTimeout(t *testing.T) {
	t.Helper()
	saved := storeOptions
	storeOptions = []store.Option{store.WithBusyTimeout(100 * time.Millisecond)}
	t.Cleanup(func() { storeOptions = saved })
}

func TestProductionSeamsAreAtTheirDefaults(t *testing.T) {
	if len(storeOptions) != 0 {
		t.Fatalf("storeOptions = %v, want none (the store's 5000 ms busy timeout applies)", storeOptions)
	}
}
