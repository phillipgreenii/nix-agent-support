package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-router/internal/config"
)

// pgRouterModuleRoot walks up from the test's cwd to this module's go.mod, the
// same technique packages/ccpool/cmd/ccpool/spec_citations_test.go uses to
// find its own module root. It exists so TestPrecedenceCopiesAgree can read
// internal/config/config.go's package doc COMMENT as text — a doc comment is
// not reachable at runtime any other way (it is not a value), unlike
// exampleHeader (a same-module constant) and helpText (this package's own
// constant).
func pgRouterModuleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.mod found at or above %q", dir)
		}
		dir = parent
	}
}

// isolateGateEnv points every gate-path input this process's environment
// could otherwise supply at deterministic, isolated values (unit tests MUST
// be isolated) — a fresh LogDir under t.TempDir(), and no repo-local/XDG
// config file — so config.GatePaths() resolves to <tmp>/gates/{operator-paused,
// cicd-down,disk-space-low} regardless of the host machine's real XDG state.
func isolateGateEnv(t *testing.T) string {
	t.Helper()
	logDir := t.TempDir()
	t.Setenv("PG_ROUTER_LOG_DIR", logDir)
	t.Setenv("PG_ROUTER_OPERATOR_PAUSED", "")
	t.Setenv("PG_ROUTER_CICD_DOWN", "")
	t.Setenv("PG_ROUTER_DISK_SPACE_LOW", "")
	// bead pg2-efbb0's external kill switch: cleared too, so a stray
	// ambient PG_ROUTER_OPERATOR_PAUSED_DISABLE on the host can never make
	// disabledNoteFor's output non-deterministic in a test that does not
	// exercise it explicitly.
	t.Setenv("PG_ROUTER_OPERATOR_PAUSED_DISABLE", "")
	t.Setenv("PG_ROUTER_CONFIG", filepath.Join(t.TempDir(), "absent.toml"))
	return logDir
}

func TestPauseGate_createsFileExitsZero(t *testing.T) {
	logDir := isolateGateEnv(t)
	var stdout, stderr bytes.Buffer
	if code := pauseGate(&stdout, &stderr, gateOperatorPaused); code != exitOK {
		t.Fatalf("pauseGate exit = %d, want 0; stderr:\n%s", code, stderr.String())
	}
	path := filepath.Join(logDir, "gates", "operator-paused")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("gate file %q must exist after pause: %v", path, err)
	}
	if !strings.Contains(stdout.String(), gateOperatorPaused) || !strings.Contains(stdout.String(), "since") {
		t.Errorf("pause output must name the gate and report a set time; got %q", stdout.String())
	}
}

// TestPauseGate_reportsExternalDisable locks bead pg2-efbb0: pause/resume
// output for operator-paused MUST note when the external kill switch
// (config.OperatorPausedDisablePath()) is currently active, so an operator
// is never left believing the toggle changed dispatch behavior when
// Orchestrator.Gated() will actually ignore it. A gate other than
// operator-paused MUST show no such note, since neither cicd-down nor
// disk-space-low carries a kill switch yet.
func TestPauseGate_reportsExternalDisable(t *testing.T) {
	logDir := isolateGateEnv(t)
	disablePath := filepath.Join(logDir, "gate-overrides", "operator-paused-disabled")
	if err := os.MkdirAll(filepath.Dir(disablePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(disablePath, []byte("disabled\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if code := pauseGate(&stdout, &stderr, gateOperatorPaused); code != exitOK {
		t.Fatalf("pauseGate exit = %d, want 0; stderr:\n%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "externally DISABLED") {
		t.Errorf("pause output for operator-paused while the kill switch is active must note it; got %q", stdout.String())
	}

	var stdout2, stderr2 bytes.Buffer
	if code := resumeGate(&stdout2, &stderr2, gateOperatorPaused, false); code != exitOK {
		t.Fatalf("resumeGate exit = %d, want 0; stderr:\n%s", code, stderr2.String())
	}
	if !strings.Contains(stdout2.String(), "externally DISABLED") {
		t.Errorf("resume output for operator-paused while the kill switch is active must note it; got %q", stdout2.String())
	}

	// cicd-down carries no kill switch — its own pause output must be
	// unaffected by operator-paused's disable file.
	var stdout3, stderr3 bytes.Buffer
	if code := pauseGate(&stdout3, &stderr3, gateCICDDown); code != exitOK {
		t.Fatalf("pauseGate exit = %d, want 0; stderr:\n%s", code, stderr3.String())
	}
	if strings.Contains(stdout3.String(), "externally DISABLED") {
		t.Errorf("pause output for cicd-down must not mention operator-paused's own kill switch; got %q", stdout3.String())
	}
}

// TestPauseGate_noDisableNoteWhenNotDisabled is the negative control for
// TestPauseGate_reportsExternalDisable: with no kill-switch file present,
// pause output must carry no disable note at all.
func TestPauseGate_noDisableNoteWhenNotDisabled(t *testing.T) {
	isolateGateEnv(t)
	var stdout, stderr bytes.Buffer
	if code := pauseGate(&stdout, &stderr, gateOperatorPaused); code != exitOK {
		t.Fatalf("pauseGate exit = %d, want 0; stderr:\n%s", code, stderr.String())
	}
	if strings.Contains(stdout.String(), "externally DISABLED") {
		t.Errorf("pause output must not mention external disable when the kill switch is absent; got %q", stdout.String())
	}
}

// MkdirAll on the gates dir: pausing the FIRST time, with no gates/
// subdirectory yet under LogDir, must still succeed.
func TestPauseGate_mkdirAllGatesDir(t *testing.T) {
	logDir := isolateGateEnv(t)
	if _, err := os.Stat(filepath.Join(logDir, "gates")); !os.IsNotExist(err) {
		t.Fatalf("premise: gates dir must not exist yet, got err=%v", err)
	}
	var stdout, stderr bytes.Buffer
	if code := pauseGate(&stdout, &stderr, gateCICDDown); code != exitOK {
		t.Fatalf("pauseGate exit = %d, want 0; stderr:\n%s", code, stderr.String())
	}
	if fi, err := os.Stat(filepath.Join(logDir, "gates")); err != nil || !fi.IsDir() {
		t.Fatalf("gates dir must exist after pause: err=%v", err)
	}
}

// Re-pause is idempotent-visible (reports "already paused") but MUST NOT
// touch the ORIGINAL mtime.
func TestPauseGate_rePausePreservesMtime(t *testing.T) {
	isolateGateEnv(t)
	var out1, out2, stderr bytes.Buffer
	if code := pauseGate(&out1, &stderr, gateOperatorPaused); code != exitOK {
		t.Fatalf("first pause exit = %d", code)
	}
	operatorPaused, _, _ := config.GatePaths()
	fi1, err := os.Stat(operatorPaused)
	if err != nil {
		t.Fatal(err)
	}
	mtime1 := fi1.ModTime()

	if code := pauseGate(&out2, &stderr, gateOperatorPaused); code != exitOK {
		t.Fatalf("second pause exit = %d", code)
	}
	fi2, err := os.Stat(operatorPaused)
	if err != nil {
		t.Fatal(err)
	}
	if !fi2.ModTime().Equal(mtime1) {
		t.Errorf("re-pause changed mtime: first %v, second %v", mtime1, fi2.ModTime())
	}
	if !strings.Contains(out2.String(), "already paused") {
		t.Errorf("re-pause output must be idempotent-visible (\"already paused\"); got %q", out2.String())
	}
}

// pauseGate/resumeGate never call config.Load() (they use config.GatePaths()
// instead), so pause/resume MUST succeed even against a config that could
// never itself Load() — here, malformed TOML, a guaranteed Load() hard error.
func TestPauseGate_succeedsWhenConfigFailsLoad(t *testing.T) {
	isolateGateEnv(t)
	bad := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(bad, []byte("this is not = valid = toml ["), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PG_ROUTER_CONFIG", bad)
	if _, err := config.Load(); err == nil {
		t.Fatal("premise: malformed config must fail Load()")
	}
	var stdout, stderr bytes.Buffer
	if code := pauseGate(&stdout, &stderr, gateOperatorPaused); code != exitOK {
		t.Fatalf("pauseGate exit = %d, want 0 even though Load() fails; stderr:\n%s", code, stderr.String())
	}
}

func TestResumeGate_clearsGate(t *testing.T) {
	isolateGateEnv(t)
	var stdout, stderr bytes.Buffer
	if code := pauseGate(&stdout, &stderr, gateOperatorPaused); code != exitOK {
		t.Fatalf("pause exit = %d", code)
	}
	operatorPaused, _, _ := config.GatePaths()
	stdout.Reset()
	if code := resumeGate(&stdout, &stderr, gateOperatorPaused, false); code != exitOK {
		t.Fatalf("resume exit = %d, want 0; stderr:\n%s", code, stderr.String())
	}
	if _, err := os.Stat(operatorPaused); !os.IsNotExist(err) {
		t.Errorf("gate file must be gone after resume, got err=%v", err)
	}
	if !strings.Contains(stdout.String(), gateOperatorPaused) {
		t.Errorf("resume output must name the gate; got %q", stdout.String())
	}
}

// A bare resume clears ONLY the default gate (operator-paused); cicd-down
// and disk-space-low (neither the default gate) are untouched.
func TestResumeGate_bareResumeClearsOnlyDefaultGate(t *testing.T) {
	isolateGateEnv(t)
	var buf, stderr bytes.Buffer
	if code := pauseGate(&buf, &stderr, gateOperatorPaused); code != exitOK {
		t.Fatalf("pause operator-paused exit = %d", code)
	}
	if code := pauseGate(&buf, &stderr, gateCICDDown); code != exitOK {
		t.Fatalf("pause cicd-down exit = %d", code)
	}
	if code := pauseGate(&buf, &stderr, gateDiskSpaceLow); code != exitOK {
		t.Fatalf("pause disk-space-low exit = %d", code)
	}
	if code := resumeGate(&buf, &stderr, gateOperatorPaused, false); code != exitOK {
		t.Fatalf("resume exit = %d", code)
	}
	operatorPaused, cicdDown, diskSpaceLow := config.GatePaths()
	if _, err := os.Stat(operatorPaused); !os.IsNotExist(err) {
		t.Errorf("operator-paused must be cleared, got err=%v", err)
	}
	if _, err := os.Stat(cicdDown); err != nil {
		t.Errorf("cicd-down must survive a bare resume (not the default gate), got err=%v", err)
	}
	if _, err := os.Stat(diskSpaceLow); err != nil {
		t.Errorf("disk-space-low must survive a bare resume (not the default gate), got err=%v", err)
	}
}

func TestResumeGate_allClearsEveryGate(t *testing.T) {
	isolateGateEnv(t)
	var buf, stderr bytes.Buffer
	if code := pauseGate(&buf, &stderr, gateOperatorPaused); code != exitOK {
		t.Fatalf("pause operator-paused exit = %d", code)
	}
	if code := pauseGate(&buf, &stderr, gateCICDDown); code != exitOK {
		t.Fatalf("pause cicd-down exit = %d", code)
	}
	if code := pauseGate(&buf, &stderr, gateDiskSpaceLow); code != exitOK {
		t.Fatalf("pause disk-space-low exit = %d", code)
	}
	buf.Reset()
	if code := resumeGate(&buf, &stderr, "", true); code != exitOK {
		t.Fatalf("resume --all exit = %d, want 0; stderr:\n%s", code, stderr.String())
	}
	operatorPaused, cicdDown, diskSpaceLow := config.GatePaths()
	if _, err := os.Stat(operatorPaused); !os.IsNotExist(err) {
		t.Errorf("operator-paused must be cleared by --all, got err=%v", err)
	}
	if _, err := os.Stat(cicdDown); !os.IsNotExist(err) {
		t.Errorf("cicd-down must be cleared by --all, got err=%v", err)
	}
	if _, err := os.Stat(diskSpaceLow); !os.IsNotExist(err) {
		t.Errorf("disk-space-low must be cleared by --all, got err=%v", err)
	}
	if got := buf.String(); !strings.Contains(got, gateDiskSpaceLow) {
		t.Errorf("resume --all output must name disk-space-low among the cleared gates; got %q", got)
	}
}

// TestPauseResumeGate_diskSpaceLow is disk-space-low's own dedicated
// pause/resume round trip (bead pg2-af5ur), mirroring
// TestPauseGate_createsFileExitsZero/TestResumeGate_clearsGate for the two
// pre-existing gates: it must be settable/clearable manually via the SAME
// generic verbs, with no automatic trigger involved.
func TestPauseResumeGate_diskSpaceLow(t *testing.T) {
	logDir := isolateGateEnv(t)
	var stdout, stderr bytes.Buffer
	if code := pauseGate(&stdout, &stderr, gateDiskSpaceLow); code != exitOK {
		t.Fatalf("pauseGate exit = %d, want 0; stderr:\n%s", code, stderr.String())
	}
	path := filepath.Join(logDir, "gates", "disk-space-low")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("gate file %q must exist after pause: %v", path, err)
	}
	if !strings.Contains(stdout.String(), gateDiskSpaceLow) || !strings.Contains(stdout.String(), "since") {
		t.Errorf("pause output must name the gate and report a set time; got %q", stdout.String())
	}

	stdout.Reset()
	if code := resumeGate(&stdout, &stderr, gateDiskSpaceLow, false); code != exitOK {
		t.Fatalf("resumeGate exit = %d, want 0; stderr:\n%s", code, stderr.String())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("gate file must be gone after resume, got err=%v", err)
	}
	if !strings.Contains(stdout.String(), gateDiskSpaceLow) {
		t.Errorf("resume output must name the gate; got %q", stdout.String())
	}
}

func TestResumeGate_alreadyResumedIsExitZero(t *testing.T) {
	isolateGateEnv(t)
	var stdout, stderr bytes.Buffer
	if code := resumeGate(&stdout, &stderr, gateOperatorPaused, false); code != exitOK {
		t.Fatalf("resume of an unset gate exit = %d, want 0", code)
	}
	if !strings.Contains(stdout.String(), "already resumed") {
		t.Errorf("output must report already-resumed; got %q", stdout.String())
	}
}

// Route-level wiring: `pause`/`resume` are their own routes, and are
// advertised in both usageLine and helpText — the same "helpText-mentions"
// pattern push_inject_test.go's TestRoute_pushInject follows.
func TestRoute_pauseResume(t *testing.T) {
	if r := route([]string{"pg-router", "pause"}); r.kind != routePause {
		t.Fatalf("route(pause).kind = %v, want routePause", r.kind)
	}
	if r := route([]string{"pg-router", "resume", "--all"}); r.kind != routeResume || !r.allGates {
		t.Fatalf("route(resume --all) = %+v, want routeResume allGates=true", r)
	}
	for _, want := range []string{"pause", "resume"} {
		if !strings.Contains(usageLine, want) {
			t.Errorf("usageLine does not mention %q", want)
		}
		if !strings.Contains(helpText, want) {
			t.Errorf("helpText does not mention %q", want)
		}
	}
}

// helpText must name both gate env vars, PG_ROUTER_LOG_DIR (absent before this
// packet), and the explicit FILE-DIRECT note that pause/resume deliberately
// break the verb-named-subcommand-is-a-socket-client symmetry.
func TestHelpText_gateEnvVarsAndFileDirectNote(t *testing.T) {
	for _, want := range []string{
		"PG_ROUTER_OPERATOR_PAUSED",
		"PG_ROUTER_CICD_DOWN",
		"PG_ROUTER_DISK_SPACE_LOW",
		"PG_ROUTER_LOG_DIR",
		"FILE-DIRECT",
		"NEVER Discover or Dial",
	} {
		if !strings.Contains(helpText, want) {
			t.Errorf("helpText missing %q", want)
		}
	}
}

// TestPrecedenceCopiesAgree is Task 1.2b's Step 1(e): the precedence sentence
// has THREE copies — internal/config/config.go's package doc, example.go's
// header (reached here through config.ExampleTOML()), and this package's own
// helpText — and they must never drift apart. All three are required to
// embed the SAME literal phrase.
func TestPrecedenceCopiesAgree(t *testing.T) {
	const phrase = "[pool] wins over PG_ROUTER_* env, which wins over the built-in default"

	root := pgRouterModuleRoot(t)
	configSrc, err := os.ReadFile(filepath.Join(root, "internal", "config", "config.go"))
	if err != nil {
		t.Fatalf("read config.go: %v", err)
	}
	if !strings.Contains(string(configSrc), phrase) {
		t.Errorf("internal/config/config.go's package doc is missing the precedence phrase %q", phrase)
	}
	if !strings.Contains(config.ExampleTOML(), phrase) {
		t.Errorf("example.go's header (config.ExampleTOML()) is missing the precedence phrase %q", phrase)
	}
	if !strings.Contains(helpText, phrase) {
		t.Errorf("cmd/pg-router's helpText is missing the precedence phrase %q", phrase)
	}
}
