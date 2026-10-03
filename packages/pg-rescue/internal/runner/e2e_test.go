package runner_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-rescue/internal/app"
	"github.com/phillipgreenii/pg-rescue/internal/report"
	"github.com/phillipgreenii/pg-rescue/internal/runner"
)

// hd declares one handler instance of an end-to-end config.
type hd struct {
	name    string
	argv    []string
	timeout string
	env     map[string]string
	envFile string
	tags    []string
}

// e2e runs the real wrapper (Main with the production ChainExecutor) against
// the helper process, with a real PATH lookup and a real environment.
type e2e struct {
	*harness
	exec  *app.ChainExecutor
	flags []string // wrapper flags added to every run
}

func quoteList(a []string) string {
	q := make([]string, len(a))
	for i, s := range a {
		q[i] = strconv.Quote(s)
	}
	return "[" + strings.Join(q, ", ") + "]"
}

// configText renders handlers and chains as TOML.
func configText(top string, hs []hd, chains map[string][]string) string {
	var b strings.Builder
	b.WriteString(top + "\n")
	for _, h := range hs {
		b.WriteString("[handler." + h.name + "]\ncommand = " + quoteList(h.argv) + "\n")
		if h.timeout != "" {
			b.WriteString("timeout = " + strconv.Quote(h.timeout) + "\n")
		}
		if len(h.tags) > 0 {
			b.WriteString("tags = " + quoteList(h.tags) + "\n")
		}
		if h.envFile != "" {
			b.WriteString("env_file = " + strconv.Quote(h.envFile) + "\n")
		}
		if len(h.env) > 0 {
			b.WriteString("[handler." + h.name + ".env]\n")
			keys := make([]string, 0, len(h.env))
			for k := range h.env {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				b.WriteString(k + " = " + strconv.Quote(h.env[k]) + "\n")
			}
		}
	}
	names := make([]string, 0, len(chains))
	for n := range chains {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		b.WriteString("[chain." + n + "]\nhandlers = " + quoteList(chains[n]) + "\n")
	}
	return b.String()
}

func newE2E(t *testing.T, top string, hs []hd, chains map[string][]string) *e2e {
	t.Helper()
	h := newHarness(t, configText(top, hs, chains))
	e := &e2e{harness: h, exec: &app.ChainExecutor{Foreground: func() bool { return false }}}
	h.rt.Executor = e.exec
	h.rt.LookPath = exec.LookPath
	h.rt.Environ = func() []string {
		env := append(os.Environ(), "GO_WANT_HELPER_PROCESS=1", "HELPER_BIN="+os.Args[0])
		for k, v := range h.env {
			env = append(env, k+"="+v)
		}
		return env
	}
	return e
}

// wrap runs `pg-rescue --handlers LIST [flags] -- cmd`.
func (e *e2e) wrap(handlers string, cmd []string, opts ...string) (code int, stdout, stderr string) {
	e.t.Helper()
	args := []string{"--config", e.cfgPath, "--handlers", handlers}
	args = append(args, e.flags...)
	args = append(args, opts...)
	if cmd != nil {
		args = append(args, "--")
		args = append(args, cmd...)
	}
	return e.run(args...)
}

// result is the in-memory result of the last run.
func (e *e2e) result() *runner.Result {
	e.t.Helper()
	if e.exec.Last == nil {
		e.t.Fatal("the executor was never reached")
	}
	return e.exec.Last
}

// rdir is the last run's directory.
func (e *e2e) rdir() string {
	e.t.Helper()
	r := e.result()
	if r.Report == nil {
		e.t.Fatalf("no report: kind=%s", r.Kind)
	}
	return filepath.Dir(r.Report.OutputFile)
}

// onDisk reads report.json from the last run's directory.
func (e *e2e) onDisk() report.Report {
	e.t.Helper()
	b, err := os.ReadFile(filepath.Join(e.rdir(), "report.json"))
	if err != nil {
		e.t.Fatal(err)
	}
	var r report.Report
	if err := json.Unmarshal(b, &r); err != nil {
		e.t.Fatalf("report.json is not valid: %v\n%s", err, b)
	}
	return r
}

// recorded is what the helper's "record" behavior saw.
type recorded struct {
	Env            map[string]string `json:"env"`
	Cwd            string            `json:"cwd"`
	Args           []string          `json:"args"`
	StdinLen       int               `json:"stdin_len"`
	StdinIsDevNull bool              `json:"stdin_is_devnull"`
	Pid            int               `json:"pid"`
	Pgid           int               `json:"pgid"`
	ParentPgid     int               `json:"parent_pgid"`
	Report         string            `json:"report"`
}

func readRecord(t *testing.T, path string) recorded {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the helper recorded nothing at %s: %v", path, err)
	}
	var r recorded
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

// simple handler declarations used by many tests.
func result(name, outcome string, args ...string) hd {
	return hd{name: name, argv: helperArgv("result", append([]string{"outcome=" + outcome}, args...)...)}
}

func chainOf(names ...string) map[string][]string { return map[string][]string{"c": names} }

func names(hs []hd) []string {
	out := make([]string, len(hs))
	for i, h := range hs {
		out[i] = h.name
	}
	return out
}

func jsonDecode(b []byte, v any) error { return json.Unmarshal(b, v) }
