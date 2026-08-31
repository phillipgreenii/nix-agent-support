package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/pr-pool/internal/config"
)

// prPoolModuleRoot walks up from the test's cwd to this module's go.mod, the
// same technique packages/ccpool/cmd/ccpool/spec_citations_test.go uses to
// find its own module root. It exists so TestPrecedenceCopiesAgree can read
// internal/config/config.go's package doc COMMENT as text — a doc comment is
// not reachable at runtime any other way (it is not a value), unlike
// exampleHeader (a same-module constant) and helpText (this package's own
// constant).
func prPoolModuleRoot(t *testing.T) string {
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
// config file — so config.GatePaths() resolves to <tmp>/gates/{quota-paused,
// cicd-down} regardless of the host machine's real XDG state.
func isolateGateEnv(t *testing.T) string {
	t.Helper()
	logDir := t.TempDir()
	t.Setenv("PR_POOL_LOG_DIR", logDir)
	t.Setenv("PR_POOL_QUOTA_PAUSED", "")
	t.Setenv("PR_POOL_CICD_DOWN", "")
	t.Setenv("PR_POOL_CONFIG", filepath.Join(t.TempDir(), "absent.toml"))
	return logDir
}

func TestPauseGate_createsFileExitsZero(t *testing.T) {
	logDir := isolateGateEnv(t)
	var stdout, stderr bytes.Buffer
	if code := pauseGate(&stdout, &stderr, gateQuotaPaused); code != exitOK {
		t.Fatalf("pauseGate exit = %d, want 0; stderr:\n%s", code, stderr.String())
	}
	path := filepath.Join(logDir, "gates", "quota-paused")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("gate file %q must exist after pause: %v", path, err)
	}
	if !strings.Contains(stdout.String(), gateQuotaPaused) || !strings.Contains(stdout.String(), "since") {
		t.Errorf("pause output must name the gate and report a set time; got %q", stdout.String())
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
	if code := pauseGate(&out1, &stderr, gateQuotaPaused); code != exitOK {
		t.Fatalf("first pause exit = %d", code)
	}
	quotaPaused, _ := config.GatePaths()
	fi1, err := os.Stat(quotaPaused)
	if err != nil {
		t.Fatal(err)
	}
	mtime1 := fi1.ModTime()

	if code := pauseGate(&out2, &stderr, gateQuotaPaused); code != exitOK {
		t.Fatalf("second pause exit = %d", code)
	}
	fi2, err := os.Stat(quotaPaused)
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
	t.Setenv("PR_POOL_CONFIG", bad)
	if _, err := config.Load(); err == nil {
		t.Fatal("premise: malformed config must fail Load()")
	}
	var stdout, stderr bytes.Buffer
	if code := pauseGate(&stdout, &stderr, gateQuotaPaused); code != exitOK {
		t.Fatalf("pauseGate exit = %d, want 0 even though Load() fails; stderr:\n%s", code, stderr.String())
	}
}

func TestResumeGate_clearsGate(t *testing.T) {
	isolateGateEnv(t)
	var stdout, stderr bytes.Buffer
	if code := pauseGate(&stdout, &stderr, gateQuotaPaused); code != exitOK {
		t.Fatalf("pause exit = %d", code)
	}
	quotaPaused, _ := config.GatePaths()
	stdout.Reset()
	if code := resumeGate(&stdout, &stderr, gateQuotaPaused, false); code != exitOK {
		t.Fatalf("resume exit = %d, want 0; stderr:\n%s", code, stderr.String())
	}
	if _, err := os.Stat(quotaPaused); !os.IsNotExist(err) {
		t.Errorf("gate file must be gone after resume, got err=%v", err)
	}
	if !strings.Contains(stdout.String(), gateQuotaPaused) {
		t.Errorf("resume output must name the gate; got %q", stdout.String())
	}
}

// A bare resume clears ONLY the default gate (quota-paused); cicd-down (an
// automation-owned gate) is untouched.
func TestResumeGate_bareResumeClearsOnlyDefaultGate(t *testing.T) {
	isolateGateEnv(t)
	var buf, stderr bytes.Buffer
	if code := pauseGate(&buf, &stderr, gateQuotaPaused); code != exitOK {
		t.Fatalf("pause quota-paused exit = %d", code)
	}
	if code := pauseGate(&buf, &stderr, gateCICDDown); code != exitOK {
		t.Fatalf("pause cicd-down exit = %d", code)
	}
	if code := resumeGate(&buf, &stderr, gateQuotaPaused, false); code != exitOK {
		t.Fatalf("resume exit = %d", code)
	}
	quotaPaused, cicdDown := config.GatePaths()
	if _, err := os.Stat(quotaPaused); !os.IsNotExist(err) {
		t.Errorf("quota-paused must be cleared, got err=%v", err)
	}
	if _, err := os.Stat(cicdDown); err != nil {
		t.Errorf("cicd-down must survive a bare resume (not the default gate), got err=%v", err)
	}
}

func TestResumeGate_allClearsBothGates(t *testing.T) {
	isolateGateEnv(t)
	var buf, stderr bytes.Buffer
	if code := pauseGate(&buf, &stderr, gateQuotaPaused); code != exitOK {
		t.Fatalf("pause quota-paused exit = %d", code)
	}
	if code := pauseGate(&buf, &stderr, gateCICDDown); code != exitOK {
		t.Fatalf("pause cicd-down exit = %d", code)
	}
	buf.Reset()
	if code := resumeGate(&buf, &stderr, "", true); code != exitOK {
		t.Fatalf("resume --all exit = %d, want 0; stderr:\n%s", code, stderr.String())
	}
	quotaPaused, cicdDown := config.GatePaths()
	if _, err := os.Stat(quotaPaused); !os.IsNotExist(err) {
		t.Errorf("quota-paused must be cleared by --all, got err=%v", err)
	}
	if _, err := os.Stat(cicdDown); !os.IsNotExist(err) {
		t.Errorf("cicd-down must be cleared by --all, got err=%v", err)
	}
}

func TestResumeGate_alreadyResumedIsExitZero(t *testing.T) {
	isolateGateEnv(t)
	var stdout, stderr bytes.Buffer
	if code := resumeGate(&stdout, &stderr, gateQuotaPaused, false); code != exitOK {
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
	if r := route([]string{"pr-pool", "pause"}); r.kind != routePause {
		t.Fatalf("route(pause).kind = %v, want routePause", r.kind)
	}
	if r := route([]string{"pr-pool", "resume", "--all"}); r.kind != routeResume || !r.allGates {
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

// helpText must name both gate env vars, PR_POOL_LOG_DIR (absent before this
// packet), and the explicit FILE-DIRECT note that pause/resume deliberately
// break the verb-named-subcommand-is-a-socket-client symmetry.
func TestHelpText_gateEnvVarsAndFileDirectNote(t *testing.T) {
	for _, want := range []string{
		"PR_POOL_QUOTA_PAUSED",
		"PR_POOL_CICD_DOWN",
		"PR_POOL_LOG_DIR",
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
	const phrase = "[pool] wins over PR_POOL_* env, which wins over the built-in default"

	root := prPoolModuleRoot(t)
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
		t.Errorf("cmd/pr-pool's helpText is missing the precedence phrase %q", phrase)
	}
}
