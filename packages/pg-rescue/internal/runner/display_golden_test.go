// display_golden_test.go: golden files for the display. They are built from
// the design's worked example (chain sync over `git pull --rebase`: the first
// handler declines, the second claims a fix that verify rejects, the third
// resolves it), and from three variations of it that end in the other results:
// deferred, unhandled and interrupted. Each result is rendered at every
// verbosity level.
//
// The fake `git` and the five handlers are shell scripts first on PATH, so the
// header reads `git pull --rebase` exactly as in the design. The goldens are
// normalized for the run directory, $HOME, durations and run ids. Regenerate
// them with `go test ./internal/runner -run TestDisplayGolden -update`.
package runner_test

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"
)

var update = flag.Bool("update", false, "rewrite the display golden files")

const (
	commandStdout = "Auto-merging README.md\n"
	commandStderr = "CONFLICT (content): Merge conflict in README.md\n"
)

// levels are the four verbosity levels, with the golden file's name for each.
var levels = []struct {
	name string
	flag string
}{{"vv", "-vv"}, {"v", "-v"}, {"default", ""}, {"q", "-q"}}

const verifyScript = `if [ -f "$PG_RESCUE_RUN_DIR/fixed" ]; then echo 'Current branch main is up to date.'; else echo 'error: cannot pull with rebase: You have unstaged changes.' >&2; exit 128; fi`

// scenarioConfig is the design's config, trimmed to the handlers the example
// uses. fixLargeTimeout lets the deferred scenario time that handler out (1s: a timeout in whole units is not mistaken for a duration by normalize).
func scenarioConfig(fixLargeTimeout string) string {
	return fmt.Sprintf(`
[handler.flake-lock-conflict]
command = ["pg-rescue-flake-lock-conflict"]
timeout = "2m"
tags = ["deterministic"]

[handler.fix-small]
command = ["pg-rescue-claude", "--model", "haiku", "--time-limit", "2m", "--strict-mcp-config"]
timeout = "3m"
description = "haiku, 2m, no MCP"
tags = ["agent"]

[handler.fix-large]
command = ["pg-rescue-claude", "--model", "sonnet", "--time-limit", "8m"]
timeout = %q
description = "sonnet, 8m"
tags = ["agent"]

[handler.p1-later]
command = ["pg-rescue-bead", "--priority", "1"]
timeout = "1m"
tags = ["deferral"]

[handler.notify]
command = ["pg-rescue-notify"]
timeout = "10s"

[chain.sync]
handlers = ["flake-lock-conflict", "fix-small", "fix-large", "p1-later", "notify"]
`, fixLargeTimeout)
}

// script is one fake executable.
type script struct{ name, body string }

func installScripts(t *testing.T, scripts ...script) {
	t.Helper()
	bin := t.TempDir()
	for _, s := range scripts {
		path := filepath.Join(bin, s.name)
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"+s.body), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

var gitScript = script{"git", "echo 'Auto-merging README.md'\necho 'CONFLICT (content): Merge conflict in README.md' >&2\nexit 1\n"}

var flakeDeclines = script{
	"pg-rescue-flake-lock-conflict",
	`printf '%s\n' '{"summary":"Conflict spans source files, not just flake.lock"}'
exit 2
`,
}

// claude dispatches on the instance name, since both agent instances run the
// same executable.
func claudeScript(small, large string) script {
	return script{"pg-rescue-claude", "case \"$PG_RESCUE_HANDLER\" in\nfix-small)\n" + small + "\n;;\nfix-large)\n" + large + "\n;;\nesac\n"}
}

const (
	smallResolvesWrongly = `echo 'fix-small: editing README.md' >&2
printf '%s\n' '{"outcome":"resolved","summary":"Resolved the conflict markers and continued the rebase","details":"Removed markers in README.md and ran ` + "`git add README.md && git rebase --continue`" + `.","meta":{"session_id":"sess-small","total_cost_usd":0.04}}'
exit 0`
	largeResolves = `echo 'fix-large: reading fix-small attempt' >&2
touch "$PG_RESCUE_RUN_DIR/fixed"
printf '%s\n' '{"outcome":"resolved","summary":"Finished the half-continued rebase; kept both sides of the README conflict","details":"fix-small edit left README.md unstaged.\nRe-staged README.md and ran git rebase --continue; 2 commits replayed cleanly.","meta":{"session_id":"sess-large","total_cost_usd":0.31,"num_turns":14,"model":"sonnet"}}'
exit 0`
	smallDeclines = `printf '%s\n' '{"summary":"Conflict needs a design decision between two rewrites of the same function"}'
exit 2`
	largeDeclines = `exit 2`
	largeHangs    = `sleep 30`
)

var (
	p1Defers = script{"pg-rescue-bead", `printf '%s\n' '{"summary":"Filed pg2-abc12 (P1): pg-rescue: git pull --rebase failed in demo-repo","meta":{"item_id":"pg2-abc12","action":"created"}}'
exit 3
`}
	p1Fails   = script{"pg-rescue-bead", "echo 'tracker unreachable' >&2\nexit 1\n"}
	p1Unused  = script{"pg-rescue-bead", "exit 2\n"}
	notifyOK  = script{"pg-rescue-notify", `printf '%s\n' '{"summary":"Posted a notification"}'` + "\nexit 2\n"}
	notifyBad = script{"pg-rescue-notify", "exit 2\n"}
)

type scenarioSpec struct {
	name      string
	config    string
	scripts   []script
	wantExit  int
	interrupt bool // SIGINT while fix-large runs
}

func scenarios() []scenarioSpec {
	return []scenarioSpec{
		{
			name:     "resolved",
			config:   scenarioConfig("10m"),
			scripts:  []script{gitScript, flakeDeclines, claudeScript(smallResolvesWrongly, largeResolves), p1Unused, notifyBad},
			wantExit: 0,
		},
		{
			name:     "deferred",
			config:   scenarioConfig("1s"),
			scripts:  []script{gitScript, flakeDeclines, claudeScript(smallDeclines, largeHangs), p1Defers, notifyBad},
			wantExit: 75,
		},
		{
			name:     "unhandled",
			config:   scenarioConfig("10m"),
			scripts:  []script{gitScript, flakeDeclines, claudeScript(smallDeclines, largeDeclines), p1Fails, notifyOK},
			wantExit: 1,
		},
		{
			name:      "interrupted",
			config:    scenarioConfig("10m"),
			scripts:   nil, // built in the test: it needs the started-file path
			wantExit:  130,
			interrupt: true,
		},
	}
}

var (
	reRunID    = regexp.MustCompile(`\d{8}T\d{6}Z-[0-9a-f]{8}`)
	reDuration = regexp.MustCompile(`\b\d+h\d+m\d+s\b|\b\d+m\d+s\b|\b\d+\.\ds\b|\b\d+ms\b`)
)

// normalize replaces what varies between runs and machines: the temp root, the
// run id and measured durations. $HOME has already been abbreviated to "~" by
// the display.
func normalize(s, root string) string {
	s = strings.ReplaceAll(s, root, "<ROOT>")
	s = reRunID.ReplaceAllString(s, "<RUNID>")
	// Durations only appear on delimiter lines; leave handler text alone.
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if strings.HasPrefix(l, "== [") || strings.HasPrefix(l, "-- verify:") {
			lines[i] = reDuration.ReplaceAllString(l, "<DUR>")
		}
	}
	return strings.Join(lines, "\n")
}

func goldenPath(scenario, level string) string {
	return filepath.Join("testdata", "golden", scenario+"."+level+".txt")
}

func checkGolden(t *testing.T, scenario, level, got string) {
	t.Helper()
	path := goldenPath(scenario, level)
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update to create it)", err)
	}
	if got != string(want) {
		t.Errorf("%s differs from the golden file %s\n--- got ---\n%s\n--- want ---\n%s", scenario+"/"+level, path, got, want)
	}
}

// runScenario runs one scenario at one level in a fresh state directory and
// returns the exit code, the wrapper's two streams and the normalized
// display.log.
func runScenario(t *testing.T, sc scenarioSpec, flagText string) (code int, stdout, stderr, displayLog string, e *e2e) {
	t.Helper()
	scripts := sc.scripts
	var started string
	if sc.interrupt {
		started = filepath.Join(t.TempDir(), "started")
		// The stderr line is written BEFORE started is published, so it is
		// already in the pipe when the SIGINT arrives (otherwise the display
		// would sometimes lack it). started is published by atomic rename.
		// `exec` makes the fake the process that gets signalled: a shell
		// parked in a foreground `sleep` prints "Terminated: 15 sleep 30" to
		// stderr when the sleep is killed, but only if it outlives the sleep,
		// which varies run to run and made display.log differ between levels.
		hang := `echo 'fix-large: working' >&2` + "\n" +
			`echo started > ` + started + ".tmp && mv " + started + ".tmp " + started + "\n" +
			"exec sleep 30"
		scripts = []script{gitScript, flakeDeclines, claudeScript(smallDeclines, hang), p1Unused, notifyBad}
	}
	installScripts(t, scripts...)
	e = newE2E(t, "", nil, nil)
	if err := os.WriteFile(e.cfgPath, []byte(sc.config), 0o600); err != nil {
		t.Fatal(err)
	}
	// Put the state under $HOME, as it is on a real machine, so the run
	// directory line reads ~/.local/state/pg-rescue/runs/<id>.
	e.env["XDG_STATE_HOME"] = filepath.Join(e.rt.Home, ".local", "state")

	args := []string{"--config", e.cfgPath, "--chain", "sync", "-C", e.cwd, "--context", "sync-projects: rebase repo onto origin", "--verify", verifyScript}
	if flagText != "" {
		args = append(args, flagText)
	}
	args = append(args, "--", "git", "pull", "--rebase")

	if sc.interrupt {
		e.exec.Signals = make(chan os.Signal, 8)
		type out struct {
			code           int
			stdout, stderr string
		}
		done := make(chan out, 1)
		go func() {
			c, so, se := e.run(args...)
			done <- out{c, so, se}
		}()
		waitForFile(t, started)
		e.exec.Signals <- syscall.SIGINT
		select {
		case o := <-done:
			code, stdout, stderr = o.code, o.stdout, o.stderr
		case <-time.After(30 * time.Second):
			t.Fatal("the wrapper did not end after the signal")
		}
	} else {
		code, stdout, stderr = e.run(args...)
	}

	b, err := os.ReadFile(filepath.Join(e.rdir(), "display.log"))
	if err != nil {
		t.Fatalf("display.log must be written whenever a handler ran, at every verbosity: %v", err)
	}
	return code, stdout, stderr, normalize(string(b), e.root), e
}

func TestDisplayGolden(t *testing.T) {
	for _, sc := range scenarios() {
		t.Run(sc.name, func(t *testing.T) {
			var vvDisplayLog string
			for _, lv := range levels {
				t.Run(lv.name, func(t *testing.T) {
					code, stdout, stderr, displayLog, e := runScenario(t, sc, lv.flag)
					if code != sc.wantExit {
						t.Errorf("exit = %d; want %d\nstderr:\n%s", code, sc.wantExit, stderr)
					}
					if stdout != commandStdout {
						t.Errorf("stdout = %q; want only the command's own %q", stdout, commandStdout)
					}
					if !strings.HasPrefix(stderr, commandStderr) {
						t.Errorf("stderr must start with the command's own stderr:\n%s", stderr)
					}
					checkGolden(t, sc.name, lv.name, normalize(stderr, e.root))

					// display.log is the -vv display, whatever the level.
					if lv.name == "vv" {
						vvDisplayLog = displayLog
						if want := normalize(strings.TrimPrefix(stderr, commandStderr+"\n"), e.root); displayLog != want {
							t.Errorf("display.log differs from the -vv display on stderr:\n%s\n---\n%s", displayLog, want)
						}
					} else if displayLog != vvDisplayLog {
						t.Errorf("display.log at %s differs from the one at -vv:\n%s\n---\n%s", lv.name, displayLog, vvDisplayLog)
					}
				})
			}
		})
	}
}
