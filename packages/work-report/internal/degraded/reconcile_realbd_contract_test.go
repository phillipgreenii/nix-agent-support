//go:build contract

package degraded_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/work-report/internal/degraded"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/work-report/internal/pgconn"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/work-report/internal/pull"
)

// Real-bd regression guard for the degraded-source reconcile's close (bead
// pg2-jp5m8).
//
// WHY THIS FILE EXISTS (global rule V-3): the close path depends on a REAL
// tool's semantics. bd >= 1.3.1 refuses to close an issue that still has an
// open blocker unless --force is passed, and a person or procedure can put a
// blocks edge on an alert bead after work-report created it. The unit tests
// in degraded_test.go use a scripted pg-connector and so can only impersonate
// that refusal; a mock of the tool does not count. These tests drive
// degraded.Reconcile through the REAL pg-connector and pg-connector-issue-beads
// binaries (built from this repo's packages/pg-connector) against a REAL bd in
// a throwaway store.
//
// Hermetic store: every test runs `bd init` in its own t.TempDir() and asserts
// the resulting .beads/metadata.json says "dolt_mode": "embedded", a private
// on-disk engine owned by that directory. The environment handed to bd and
// pg-connector drops BEADS_DIR, WORKSPACE_ROOT, every BEADS_* / BD_* variable
// (so nothing can point at a shared dolt server or another workspace) and then
// pins BEADS_DOLT_AUTO_START=0 and BD_JSON_ENVELOPE=1; XDG_STATE_HOME is
// redirected into the temp tree. No dolt server is started or stopped by the
// test, and no other beads workspace is read or written.
//
// Behind the `contract` build tag like the repo's other real-bd suites. It is
// wired into the commit-time and pre-land hooks by the
// `work-report-degraded-contract-test` hook in flake.nix. Run it directly with
//
//	nice -n 10 go test -p 4 -count=1 -tags contract -timeout 10m -run RealBD ./internal/degraded/

const realBDDeadline = 3 * time.Minute

// beadsBackend is the registry instance name; Reconcile pins every call to it.
const beadsBackend = "beads"

type realBD struct {
	t      *testing.T
	dir    string
	bin    string
	env    []string
	bdPath string
}

func newRealBD(t *testing.T) *realBD {
	t.Helper()
	bdPath, err := exec.LookPath("bd")
	if err != nil {
		t.Skip("bd not on PATH")
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("go not on PATH, cannot build the real pg-connector binaries: %v", err)
	}
	f := &realBD{t: t, dir: t.TempDir(), bin: t.TempDir(), bdPath: bdPath}

	// Module root of pg-connector: internal/degraded -> work-report -> packages.
	moduleRoot, err := filepath.Abs("../../../pg-connector")
	if err != nil {
		t.Fatalf("resolve pg-connector module root: %v", err)
	}
	for _, name := range []string{"pg-connector", "pg-connector-issue-beads"} {
		ctx, cancel := context.WithTimeout(context.Background(), realBDDeadline)
		cmd := exec.CommandContext(ctx, goBin, "build", "-o", filepath.Join(f.bin, name), "./cmd/"+name)
		cmd.Dir = moduleRoot
		out, buildErr := cmd.CombinedOutput()
		cancel()
		if buildErr != nil {
			t.Fatalf("go build ./cmd/%s: %v\n%s", name, buildErr, out)
		}
	}

	// The dedup query Reconcile runs is a NAMED query the deployment defines
	// in the backend's queries block (see degraded.DedupQuery).
	registry := filepath.Join(t.TempDir(), "config.yaml")
	registryYAML := "connector:\n  issue:\n    - name: " + beadsBackend + "\n      command: [pg-connector-issue-beads]\n" +
		"backends:\n  " + beadsBackend + ":\n    queries:\n" +
		"      " + degraded.DedupQuery + ": \"list --label escalated --status open,in_progress,blocked,deferred\"\n"
	if err := os.WriteFile(registry, []byte(registryYAML), 0o600); err != nil {
		t.Fatalf("write connector registry: %v", err)
	}
	f.env = hermeticEnv(map[string]string{
		"PG_PR_CONFIG":          registry,
		"XDG_STATE_HOME":        t.TempDir(),
		"BEADS_DOLT_AUTO_START": "0",
		"BD_JSON_ENVELOPE":      "1",
		"PATH":                  f.bin + string(os.PathListSeparator) + os.Getenv("PATH"),
	})

	prefix := "wr" + strings.ToLower(strings.NewReplacer("/", "", "_", "").Replace(t.Name()))
	if len(prefix) > 12 {
		prefix = prefix[:12]
	}
	f.bd("init", "--prefix", prefix, "--non-interactive", "-q", "--skip-agents", "--skip-hooks")
	meta, err := os.ReadFile(filepath.Join(f.dir, ".beads", "metadata.json"))
	if err != nil {
		t.Fatalf("read bd metadata: %v", err)
	}
	if !strings.Contains(string(meta), `"dolt_mode": "embedded"`) {
		t.Fatalf("bd init did not produce a private embedded store, refusing to continue:\n%s", meta)
	}
	// pg-connector-issue-beads resolves its workspace from this variable.
	f.env = append(f.env, "PG_CONNECTOR_ISSUE_BEADS_DIR="+f.dir)
	return f
}

// hermeticEnv is this process's environment minus everything that could bind
// bd to another workspace or to a shared dolt server, plus overrides.
func hermeticEnv(overrides map[string]string) []string {
	var out []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if _, over := overrides[k]; over {
			continue
		}
		switch {
		case k == "WORKSPACE_ROOT", k == "ZR_MACHINE_SUPPORT_WORKSPACE_ROOT",
			strings.HasPrefix(k, "BEADS_"), strings.HasPrefix(k, "BD_"),
			// A commit hook run exports GIT_DIR/GIT_INDEX_FILE of the OUTER
			// repo; inherited, bd init treats the temp dir as part of that
			// repo's beads workspace and aborts ("already initialized").
			strings.HasPrefix(k, "GIT_"),
			k == "PG_CONNECTOR_ISSUE_BEADS_DIR", k == "PG_CONNECTOR_ISSUE_BEADS_ACTOR",
			k == degraded.EnvBackend:
			continue
		}
		out = append(out, kv)
	}
	for k, v := range overrides {
		out = append(out, k+"="+v)
	}
	return out
}

func (f *realBD) bd(args ...string) string {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), realBDDeadline)
	defer cancel()
	cmd := exec.CommandContext(ctx, f.bdPath, args...)
	cmd.Dir = f.dir
	cmd.Env = f.env
	out, err := cmd.CombinedOutput()
	if err != nil {
		f.t.Fatalf("bd %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// create files a bead and returns its id.
func (f *realBD) create(title string, labels ...string) string {
	f.t.Helper()
	args := []string{"create", title, "-t", "task", "--silent"}
	if len(labels) > 0 {
		args = append(args, "--labels", strings.Join(labels, ","))
	}
	lines := strings.Split(strings.TrimSpace(f.bd(args...)), "\n")
	id := strings.TrimSpace(lines[len(lines)-1])
	if id == "" || strings.Contains(id, " ") {
		f.t.Fatalf("bd create %q: cannot read the new id from %q", title, lines)
	}
	return id
}

func (f *realBD) status(id string) string {
	f.t.Helper()
	out := f.bd("show", id, "--json")
	_, rest, ok := strings.Cut(out, `"status": "`)
	if !ok {
		f.t.Fatalf("bd show %s --json carries no status:\n%s", id, out)
	}
	status, _, _ := strings.Cut(rest, `"`)
	return status
}

// alert files an escalated, work-report-labeled alert bead for source, exactly
// as Reconcile's own create would title and label it.
func (f *realBD) alert(source string) string {
	return f.create(degraded.Title(source), "escalated", "work-report")
}

// connector is the pgconn.Runner Reconcile drives: the REAL pg-connector
// binary under the fixture's hermetic environment, recording every argv.
type connector struct {
	f     *realBD
	calls [][]string
	// beforeClose, when set, runs just before each `issue close` call.
	beforeClose func()
}

func (c *connector) Run(ctx context.Context, args ...string) ([]byte, int, error) {
	o, err := c.RunDetailed(ctx, args...)
	return o.Stdout, o.ExitCode, err
}

func (c *connector) RunDetailed(ctx context.Context, args ...string) (pgconn.Output, error) {
	c.calls = append(c.calls, append([]string{}, args...))
	if c.beforeClose != nil && len(args) >= 2 && args[0] == "issue" && args[1] == "close" {
		c.beforeClose()
	}
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, filepath.Join(c.f.bin, pgconn.Binary), args...)
	cmd.Env = c.f.env
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	out := pgconn.Output{}
	err := cmd.Run()
	out.Stdout, out.Stderr = stdout.Bytes(), stderr.Bytes()
	if err == nil {
		return out, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		out.ExitCode = exitErr.ExitCode()
		return out, nil
	}
	return out, err
}

func (f *realBD) reconcile(c *connector, rows ...pull.OutcomeRow) ([]degraded.Item, error) {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), realBDDeadline)
	defer cancel()
	return degraded.Reconcile(ctx, c, beadsBackend, rows, rng, now, repull)
}

// TestReconcile_RealBD_UnblockedAlertClosesWithTheStandardReason is the
// pre-existing behaviour that MUST NOT regress: an alert bead with no blocks
// edge is closed by the first succeeded row, with the standard reason text.
func TestReconcile_RealBD_UnblockedAlertClosesWithTheStandardReason(t *testing.T) {
	f := newRealBD(t)
	id := f.alert("backend-a")
	other := f.alert("backend-b") // its source did not succeed: must stay open

	c := &connector{f: f}
	if _, err := f.reconcile(c, okRow("backend-a")); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got := f.status(id); got != "closed" {
		t.Errorf("status of %s = %q, want closed", id, got)
	}
	if got := f.status(other); got == "closed" {
		t.Errorf("%s was closed although its source did not succeed", other)
	}
	wantReason := "work-report pull for backend-a succeeded again at " + now.Format(time.RFC3339)
	if shown := f.bd("show", id, "--json"); !strings.Contains(shown, wantReason) {
		t.Errorf("close reason %q not recorded on %s:\n%s", wantReason, id, shown)
	}
}

// TestReconcile_RealBD_BlockedAlertStillClosesAndLeavesItsBlocker is the
// pg2-jp5m8 case: a person or procedure linked the alert to its fix bead with
// a blocks edge, so a plain `bd close` is refused. The alert's close condition
// (its source succeeded again) does not depend on that fix bead, so Reconcile
// must close the alert anyway, and must not touch the blocker.
func TestReconcile_RealBD_BlockedAlertStillClosesAndLeavesItsBlocker(t *testing.T) {
	f := newRealBD(t)
	alert := f.alert("backend-a")
	fix := f.create("fix bead for backend-a")
	f.bd("dep", "add", alert, fix) // alert depends on (is blocked by) fix

	// Guard the premise against the real tool: without force, bd refuses.
	refusal := exec.Command(f.bdPath, "close", alert, "--reason", "premise probe")
	refusal.Dir, refusal.Env = f.dir, f.env
	if out, err := refusal.CombinedOutput(); err == nil {
		t.Fatalf("bd closed a blocked bead without --force; this bd no longer exhibits the refusal this test guards:\n%s", out)
	}
	if got := f.status(alert); got == "closed" {
		t.Fatalf("premise probe closed %s", alert)
	}

	c := &connector{f: f}
	if _, err := f.reconcile(c, okRow("backend-a")); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got := f.status(alert); got != "closed" {
		t.Errorf("blocked alert status = %q, want closed", got)
	}
	if got := f.status(fix); got == "closed" {
		t.Errorf("the blocker %s was closed along with the alert; it must stay open", fix)
	}
}

// TestReconcile_RealBD_FailedCloseIsReturnedAsAFailure covers the visible-
// failure half against a real failure: the bead is found by the dedup lookup
// and then deleted before the close runs, so the real binary's close fails.
// Reconcile must return exactly one structured Failure naming the source, the
// action and the bead, which the pull command writes to the reconcile log.
func TestReconcile_RealBD_FailedCloseIsReturnedAsAFailure(t *testing.T) {
	f := newRealBD(t)
	id := f.alert("backend-a")
	c := &connector{f: f, beforeClose: func() { f.bd("delete", id, "--force") }}

	_, err := f.reconcile(c, okRow("backend-a"))
	if err == nil {
		t.Fatal("Reconcile reported success although the close failed")
	}
	fs := degraded.Failures(err)
	if len(fs) != 1 {
		t.Fatalf("failures = %+v (err %v), want exactly 1", fs, err)
	}
	if fs[0].Source != "backend-a" || fs[0].Action != degraded.ActionClose || fs[0].BeadID != id {
		t.Errorf("failure = %+v, want source backend-a, action close, bead %s", fs[0], id)
	}
}
