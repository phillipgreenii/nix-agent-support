package prepare

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/phillipgreenii/pg-desk-shadow/internal/safety"
	"github.com/phillipgreenii/pg-desk-shadow/internal/scratch"
)

// fakeBackend stands in for pg-connector-issue-beads: like the real binary, a
// --beads-dir flag beats BEADS_DIR / PG_CONNECTOR_ISSUE_BEADS_DIR. It prints
// the directory it would read, and touches nothing.
const fakeBackend = `#!/bin/sh
dir="${PG_CONNECTOR_ISSUE_BEADS_DIR:-$BEADS_DIR}"
while [ $# -gt 0 ]; do
  case "$1" in
    --beads-dir) dir="$2"; shift ;;
    --beads-dir=*) dir="${1#--beads-dir=}" ;;
  esac
  shift
done
printf '%s\n' "$dir"
`

const liveDeskForReadPath = `
self_login: tester
repos:
  - remote: acme/api
    beads_dir: %LIVE%
`

const livePRForReadPath = `
connector:
  issue:
    - pg-connector-issue-jira
    - name: beads-pg2
      command: [pg-connector-issue-beads, --beads-dir, %LIVE%]
    - name: beads-zr
      command: [pg-connector-issue-beads, "--beads-dir=%LIVE%"]
`

// effectiveDirs returns the directory each registered beads instance of the
// pg-pr config at prPath would read when run under env, by running the fake
// backend with the instance's argv.
func effectiveDirs(t *testing.T, prPath, binDir string, env []string) []string {
	t.Helper()
	raw, err := os.ReadFile(prPath)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Connector struct {
			Issue []any `yaml:"issue"`
		} `yaml:"connector"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range doc.Connector.Issue {
		m, ok := e.(map[string]any)
		if !ok {
			continue
		}
		var argv []string
		for _, w := range m["command"].([]any) {
			argv = append(argv, w.(string))
		}
		cmd := exec.Command(filepath.Join(binDir, argv[0]), argv[1:]...)
		cmd.Env = env
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("run %v: %v", argv, err)
		}
		got = append(got, strings.TrimSpace(string(out)))
	}
	return got
}

// TestScratchPRConfigReadsOnlyTheConfiguredBeadsDir covers pg2-ghmw0
// end to end without a live tracker: it derives the scratch configs from a live
// pair whose pg-pr config bakes a --beads-dir into its registered instances,
// builds the policy and the child environment the collector would use, and runs
// stand-in backends from the derived registry to see which directory they read.
func TestScratchPRConfigReadsOnlyTheConfiguredBeadsDir(t *testing.T) {
	for _, mode := range []string{"passthrough", "hermetic"} {
		t.Run(mode, func(t *testing.T) {
			root := safety.Resolve(t.TempDir())
			l := scratch.Layout{Root: root}
			if err := l.MkdirAll(); err != nil {
				t.Fatal(err)
			}
			live := filepath.Join(safety.Resolve(t.TempDir()), "live-tracker")
			readOnly := filepath.Join(safety.Resolve(t.TempDir()), "read-only-tracker")
			for _, d := range []string{live, readOnly, l.BeadsWS()} {
				if err := os.MkdirAll(d, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(l.BinDir(), "pg-connector-issue-beads"), []byte(fakeBackend), 0o755); err != nil {
				t.Fatal(err)
			}

			// The live desk config names the read-only dir in passthrough mode.
			// The live pg-pr config bakes in the LIVE tracker.
			deskLive := strings.ReplaceAll(liveDeskForReadPath, "%LIVE%", readOnly)
			prLive := strings.ReplaceAll(livePRForReadPath, "%LIVE%", live)
			hermetic := mode == "hermetic"
			deskYAML, info, err := scratch.DeriveDeskConfig([]byte(deskLive), scratch.DeskParams{Queries: []string{"mine"}, HermeticBD: hermetic, HermeticDir: l.BeadsWS()})
			if err != nil {
				t.Fatal(err)
			}
			prYAML, err := scratch.DerivePRConfig([]byte(prLive), info.BeadsDir)
			if err != nil {
				t.Fatal(err)
			}
			for path, b := range map[string][]byte{l.DeskConfig(): deskYAML, l.PRConfig(): prYAML} {
				if err := os.WriteFile(path, b, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			want := readOnly
			if hermetic {
				want = l.BeadsWS()
			}

			pol := l.Policy(scratch.Manifest{BeadsDir: info.BeadsDir, Home: t.TempDir()})
			env := safety.ChildEnv(pol, "/usr/bin:/bin")
			if err := safety.Verify(env, pol); err != nil {
				t.Fatalf("the derived scratch config must pass Verify: %v", err)
			}
			dirs := effectiveDirs(t, l.PRConfig(), l.BinDir(), env)
			if len(dirs) != 2 {
				t.Fatalf("instances = %v, want 2 (Jira removed)", dirs)
			}
			for _, d := range dirs {
				if d != want {
					t.Errorf("an instance reads %s, want the read-only %s", d, want)
				}
			}

			// Control: the live config copied verbatim reads the LIVE tracker
			// (the pg2-ghmw0 defect), and Verify refuses it.
			if err := os.WriteFile(l.PRConfig(), []byte(prLive), 0o600); err != nil {
				t.Fatal(err)
			}
			for _, d := range effectiveDirs(t, l.PRConfig(), l.BinDir(), env) {
				if d != live {
					t.Fatalf("control: the fake backend must honour the flag over the env (got %s)", d)
				}
			}
			if err := safety.Verify(env, pol); err == nil {
				t.Error("Verify must refuse an unpinned scratch pg-pr config")
			}
		})
	}
}
