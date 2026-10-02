package runner_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-rescue/internal/app"
)

// counterRand yields 0,1,2,... so run ids are predictable.
type counterRand struct{ n byte }

func (c *counterRand) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = c.n
		c.n++
	}
	return len(p), nil
}

// harness builds an app.Runtime whose clock, randomness, hostname, state and
// config locations are all under a temp directory.
type harness struct {
	t       *testing.T
	root    string
	cwd     string
	cfgPath string
	env     map[string]string
	rt      *app.Runtime
}

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
	}
	if err := os.MkdirAll(h.cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	h.cfgPath = filepath.Join(root, "config.toml")
	if err := os.WriteFile(h.cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	h.rt = &app.Runtime{
		Version:   "test",
		Now:       func() time.Time { return time.Date(2026, 10, 2, 14, 3, 11, 0, time.UTC) },
		Rand:      &counterRand{},
		Hostname:  func() (string, error) { return "testhost", nil },
		KillGrace: 50 * time.Millisecond,
		Getenv:    func(k string) string { return h.env[k] },
		Environ:   func() []string { return nil },
		Getwd:     func() (string, error) { return h.cwd, nil },
		Home:      filepath.Join(root, "home"),
		LookPath:  func(string) (string, error) { return "", errors.New("not found") },
	}
	return h
}

func (h *harness) run(args ...string) (code int, stdout, stderr string) {
	h.t.Helper()
	var out, errb bytes.Buffer
	code = app.Main(h.rt, args, &out, &errb)
	return code, out.String(), errb.String()
}

// runDirs lists the run directories created under the state root.
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
