package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestConfigCheckCommand(t *testing.T) {
	c := newCLI(t)
	bad := writeConfig(t, "[repos.\"/x\"]\nmin = 3\nmax = 2\n")
	code, out, _ := c.runRaw("config", "check", "--config", bad)
	if code != 2 || !strings.Contains(out, "/x") || !strings.Contains(out, "error") {
		t.Errorf("min > max: exit %d, out %q", code, out)
	}

	// Warnings only (reuse without clean) exit 0.
	warn := writeConfig(t, "[repos.\"/x\"]\nmin = 2\nreuse-reclaimed-worktrees = true\n")
	code, out, _ = c.runRaw("config", "check", "--config", warn)
	if code != 0 || !strings.Contains(out, "warning") {
		t.Errorf("warnings only: exit %d, out %q", code, out)
	}

	// A clean file prints ok; an unknown key is a usage error.
	good := writeConfig(t, "[repos.\"/x\"]\nmin = 2\nmax = 4\n")
	if code, out, _ = c.runRaw("config", "check", "--config", good); code != 0 || !strings.Contains(out, "ok:") {
		t.Errorf("clean: exit %d, out %q", code, out)
	}
	unknown := writeConfig(t, "[repos.\"/x\"]\nbogus = 1\n")
	if code, _, _ = c.runRaw("config", "check", "--config", unknown); code != 2 {
		t.Errorf("unknown key: exit %d", code)
	}
	if code, _, _ = c.runRaw("config"); code != 2 {
		t.Errorf("config alone: exit %d", code)
	}
}

func TestConfigCheckLiveFindsUnresolvedRepo(t *testing.T) {
	c := newCLI(t)
	missing := filepath.Join(t.TempDir(), "nowhere")
	cfgDir := os.Getenv("XDG_CONFIG_HOME")
	if err := os.MkdirAll(filepath.Join(cfgDir, "pg-gwm"), 0o700); err != nil {
		t.Fatal(err)
	}
	body := "[repos.\"" + missing + "\"]\nmin = 1\n"
	if err := os.WriteFile(filepath.Join(cfgDir, "pg-gwm", "config.toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, _ := c.runRaw("config", "check")
	if code != 0 || !strings.Contains(out, "does not resolve") {
		t.Errorf("live: exit %d, out %q", code, out)
	}
}

// runRaw runs the command without the --repo flag the other tests append.
func (c *cli) runRaw(args ...string) (code int, stdout, stderr string) {
	c.t.Helper()
	for _, f := range []*os.File{c.out, c.errf} {
		if err := f.Truncate(0); err != nil {
			c.t.Fatal(err)
		}
		if _, err := f.Seek(0, 0); err != nil {
			c.t.Fatal(err)
		}
	}
	code = c.a.run(args)
	o, _ := os.ReadFile(c.out.Name())
	e, _ := os.ReadFile(c.errf.Name())
	return code, string(o), string(e)
}
