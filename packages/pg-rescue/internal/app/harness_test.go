package app

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// recorder is the Executor seam: it stands in for "spawn the command", so a
// test can prove the command was (or was never) reached.
type recorder struct {
	calls []*Plan
	exit  int
}

func (r *recorder) Execute(p *Plan) int {
	r.calls = append(r.calls, p)
	return r.exit
}

// counterRand yields 0,1,2,... so run ids are predictable.
type counterRand struct{ n byte }

func (c *counterRand) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = c.n
		c.n++
	}
	return len(p), nil
}

type harness struct {
	t       *testing.T
	root    string
	cwd     string
	cfgPath string
	env     map[string]string
	bins    map[string]string // command[0] -> resolved path for LookPath
	rec     *recorder
	rt      *Runtime
}

const goodConfig = `
[handler.fix-small]
command = ["pg-rescue-claude", "--model", "haiku"]
description = "haiku"
tags = ["agent"]

[handler.fix-large]
command = ["pg-rescue-claude", "--model", "sonnet"]

[handler.notify]
command = ["pg-rescue-notify"]
timeout = "10s"

[handler.p1-later]
command = ["pg-rescue-bead"]

[chain.sync]
handlers = ["fix-small", "fix-large", "p1-later", "notify"]
`

func newHarness(t *testing.T, cfg string) *harness {
	t.Helper()
	root := t.TempDir()
	h := &harness{
		t:    t,
		root: root,
		cwd:  filepath.Join(root, "cwd"),
		env: map[string]string{
			"XDG_STATE_HOME":  filepath.Join(root, "state"),
			"XDG_CONFIG_HOME": filepath.Join(root, "xdg"),
		},
		bins: map[string]string{},
		rec:  &recorder{exit: 42},
	}
	if err := os.MkdirAll(h.cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	h.cfgPath = filepath.Join(root, "config.toml")
	if cfg != "" {
		h.writeConfig(h.cfgPath, cfg)
	}
	h.rt = &Runtime{
		Version:   "test",
		Now:       func() time.Time { return time.Date(2026, 10, 2, 14, 3, 11, 0, time.UTC) },
		Rand:      &counterRand{},
		Hostname:  func() (string, error) { return "testhost", nil },
		KillGrace: 50 * time.Millisecond,
		Getenv:    func(k string) string { return h.env[k] },
		Environ:   func() []string { return nil },
		Getwd:     func() (string, error) { return h.cwd, nil },
		Home:      filepath.Join(root, "home"),
		LookPath: func(name string) (string, error) {
			if p, ok := h.bins[name]; ok {
				return p, nil
			}
			return "", errors.New("not found")
		},
		Executor: h.rec,
	}
	return h
}

func (h *harness) writeConfig(path, text string) {
	h.t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		h.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) run(args ...string) (code int, stdout, stderr string) {
	h.t.Helper()
	var out, errb bytes.Buffer
	code = Main(h.rt, args, &out, &errb)
	return code, out.String(), errb.String()
}

// runsDir lists the run directories created under the state root.
func (h *harness) runDirs() []string {
	entries, err := os.ReadDir(filepath.Join(h.env["XDG_STATE_HOME"], "pg-rescue", "runs"))
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func contains(t *testing.T, what, s string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(s, w) {
			t.Errorf("%s lacks %q:\n%s", what, w, s)
		}
	}
}
