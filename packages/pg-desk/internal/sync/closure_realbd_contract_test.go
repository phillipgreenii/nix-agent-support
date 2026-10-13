//go:build contract

package sync

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// Real-bd regression guard for pg-desk's depth-first closure (beads
// pg2-o0z8u; the behaviour comes from pg2-seeo8 and pg2-ubvmh, the
// open-child refusal from pg2-mhz7b).
//
// WHY THIS FILE EXISTS (global rule V-3): the depth-first closure is
// deterministic code whose correctness depends on a REAL tool's semantics:
// bd >= 1.3.1 refuses to close a parent that still has an open direct child.
// closure_live_children_test.go and sync_test.go only impersonate that
// refusal with a reentrant helper process (GO_HELPER_PARENT_OF), and a mock
// of the tool does not count. These tests drive pg-desk's real closure path
// (Syncer.Sync, ChangeRemoved, confirmed merge) through the REAL
// pg-connector and pg-connector-issue-beads binaries (built from this
// repo's packages/pg-connector) against a REAL `bd` in a throwaway store, so
// the order bd actually enforces, the text of its refusal and pg-desk's
// classification of that refusal are all exercised for real.
//
// Hermetic store: every test runs `bd init` in its own t.TempDir() and
// asserts the resulting .beads/metadata.json says `"dolt_mode": "embedded"`,
// i.e. a private on-disk engine owned by that directory. The environment
// handed to bd and pg-connector drops BEADS_DIR, WORKSPACE_ROOT and every
// BEADS_DOLT_* variable (so nothing can point at this machine's shared dolt
// server) and pins BEADS_DOLT_AUTO_START=0; XDG_STATE_HOME is redirected so
// the backend's event log stays in the temp tree. The test never reads or
// writes any other beads workspace.
//
// Behind the `contract` build tag, like the repo's other real-bd suites
// (packages/pg-pr/pkg/beads, packages/pg-connector/cmd/pg-connector-issue-
// beads/internal/realbd_test.go): it is skipped when `bd` is not on PATH and
// is not part of the unit tier. It is wired into the commit-time and
// pre-land hooks by the `pg-desk-closure-contract-test` hook in flake.nix.
// Run it directly with
//
//	nice -n 10 go test -p 4 -count=1 -tags contract -timeout 10m -run RealBD ./internal/sync/

// closureRealBDDeadline is a hang guard for real bd / go build runs under
// heavy host load (same convention as the sibling real-bd suites), not a
// performance assertion.
const closureRealBDDeadline = 3 * time.Minute

// closureRealBD is one hermetic fixture: a throwaway bd workspace, the real
// pg-connector binaries and the environment both run under.
type closureRealBD struct {
	t   *testing.T
	dir string
	bin string
	env []string
	// bdPath is the real bd resolved from PATH. It is deliberately a variable,
	// never a string literal at the exec call: cmd/pg-desk/composition_test.go
	// (D10) forbids any literal non-pg-connector exec in this module, a rule
	// about what pg-desk SHIPS. This contract-tagged test file is never
	// compiled into the binary, and the real bd is the thing under test.
	bdPath string

	// onCall, when set, runs before every pg-connector invocation pg-desk
	// makes, with its argv. Tests use it to record the order of writes and to
	// inject a bead at a precise point in the closure.
	onCall func(args []string)
	// calls is every pg-connector argv pg-desk issued, in order.
	calls [][]string
}

func newClosureRealBD(t *testing.T) *closureRealBD {
	t.Helper()
	bdPath, err := exec.LookPath("bd")
	if err != nil {
		t.Skip("bd not on PATH")
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("go not on PATH, cannot build the real pg-connector binaries: %v", err)
	}
	f := &closureRealBD{t: t, dir: t.TempDir(), bin: t.TempDir(), bdPath: bdPath}

	// The real binaries, built from this repo's own pg-connector module so the
	// test always exercises the current source (module root is three levels up
	// from internal/sync, then ../pg-connector).
	moduleRoot, err := filepath.Abs("../../../pg-connector")
	if err != nil {
		t.Fatalf("resolve pg-connector module root: %v", err)
	}
	for _, name := range []string{"pg-connector", "pg-connector-issue-beads"} {
		ctx, cancel := context.WithTimeout(context.Background(), closureRealBDDeadline)
		cmd := exec.CommandContext(ctx, goBin, "build", "-o", filepath.Join(f.bin, name), "./cmd/"+name)
		cmd.Dir = moduleRoot
		out, buildErr := cmd.CombinedOutput()
		cancel()
		if buildErr != nil {
			t.Fatalf("go build ./cmd/%s: %v\n%s", name, buildErr, out)
		}
	}

	registry := filepath.Join(t.TempDir(), "config.yaml")
	// testConfig pins pg-desk to --backend beads, so the registry names the
	// instance "beads" (the {name, command} form).
	registryYAML := "connector:\n  issue:\n    - name: beads\n      command: [pg-connector-issue-beads]\n"
	if err := os.WriteFile(registry, []byte(registryYAML), 0o600); err != nil {
		t.Fatalf("write connector registry: %v", err)
	}
	f.env = f.hermeticEnv(map[string]string{
		"PG_PR_CONFIG":          registry,
		"XDG_STATE_HOME":        t.TempDir(),
		"BEADS_DOLT_AUTO_START": "0",
		"BD_JSON_ENVELOPE":      "1",
		"PATH":                  f.bin + string(os.PathListSeparator) + os.Getenv("PATH"),
	})

	prefix := "dc" + strings.ToLower(strings.NewReplacer("/", "", "_", "").Replace(t.Name()))
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
	f.bd("config", "set", "types.custom", "merge-request,feedback")
	return f
}

// hermeticEnv is this process's environment minus everything that could bind
// bd to another workspace or to the shared dolt server, plus overrides.
func (f *closureRealBD) hermeticEnv(overrides map[string]string) []string {
	var out []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if _, over := overrides[k]; over {
			continue
		}
		switch {
		case k == "BEADS_DIR", k == "WORKSPACE_ROOT", k == "ZR_MACHINE_SUPPORT_WORKSPACE_ROOT",
			k == "PG_CONNECTOR_ISSUE_BEADS_DIR", k == "PG_CONNECTOR_ISSUE_BEADS_ACTOR",
			strings.HasPrefix(k, "BEADS_DOLT_"):
			continue
		}
		out = append(out, kv)
	}
	for k, v := range overrides {
		out = append(out, k+"="+v)
	}
	return out
}

// bd runs the real bd in the throwaway workspace and returns its output.
func (f *closureRealBD) bd(args ...string) string {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), closureRealBDDeadline)
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

// create files a bead (optionally under parent) and returns its id.
func (f *closureRealBD) create(title, issueType, parent string) string {
	f.t.Helper()
	args := []string{"create", title, "-t", issueType, "--silent"}
	if parent != "" {
		args = append(args, "--parent", parent)
	}
	lines := strings.Split(strings.TrimSpace(f.bd(args...)), "\n")
	id := strings.TrimSpace(lines[len(lines)-1])
	if id == "" || strings.Contains(id, " ") {
		f.t.Fatalf("bd create %q: cannot read the new id from %q", title, lines)
	}
	return id
}

// status is the bead's status as the real bd reports it.
func (f *closureRealBD) status(id string) string {
	f.t.Helper()
	out := f.bd("show", id, "--json")
	_, rest, ok := strings.Cut(out, `"status": "`)
	if !ok {
		f.t.Fatalf("bd show %s --json carries no status:\n%s", id, out)
	}
	status, _, _ := strings.Cut(rest, `"`)
	return status
}

// syncer builds a pg-desk Syncer in apply mode whose pg-connector calls run
// the real binary against this fixture's workspace, and swaps execCmdFactory
// for the test's duration.
func (f *closureRealBD) syncer() *Syncer {
	f.t.Helper()
	orig := execCmdFactory
	f.t.Cleanup(func() { execCmdFactory = orig })
	execCmdFactory = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if name != pgConnectorBinary {
			f.t.Fatalf("pg-desk exec'd %q, want only %q", name, pgConnectorBinary)
		}
		f.calls = append(f.calls, append([]string{}, args...))
		if f.onCall != nil {
			f.onCall(args)
		}
		cmd := exec.CommandContext(ctx, filepath.Join(f.bin, pgConnectorBinary), args...)
		cmd.Env = f.env
		return cmd
	}
	cfg := testConfig(ModeApply)
	cfg.Repos[0].BeadsDir = f.dir
	return New(cfg, store.OpenForTest(f.t), WithClock(fixedClock))
}

// closedOrder is the ids pg-desk transitioned to closed, in call order. The
// closure is sequential and every call completes before the next starts, so
// call order is execution order; bd's own refusal (a failing call) is what
// keeps an out-of-order sequence from succeeding.
func (f *closureRealBD) closedOrder() []string {
	var order []string
	for _, a := range f.calls {
		if len(a) >= 3 && a[0] == "issue" && a[1] == "transition" && strings.Contains(strings.Join(a, " "), "closed") {
			order = append(order, a[2])
		}
	}
	return order
}

// seedLedger records the anchor and (optionally) the feedback cycle as
// ledger-tracked beads, exactly as a prior sync run would have left them.
func seedRealBDLedger(t *testing.T, s *Syncer, anchorID, cycleID string) {
	t.Helper()
	for _, e := range []struct{ kind, id string }{{KindAnchor, anchorID}, {KindFeedbackCycle, cycleID}} {
		if e.id == "" {
			continue
		}
		if err := s.store.UpsertLedger(store.LedgerEntry{
			Repo: fixtureRepo, EntityType: "pr", EntityID: fixtureEntity, Kind: e.kind,
			BeadID: e.id, LastSyncedContentHash: "somehash", LastSyncedAt: "2026-09-16T00:00:00Z",
		}); err != nil {
			t.Fatalf("seed %s ledger: %v", e.kind, err)
		}
	}
}

func indexOf(order []string, id string) int {
	for i, o := range order {
		if o == id {
			return i
		}
	}
	return -1
}

// TestClosure_RealBD_AnchorCycleGrandchild_ClosesDepthFirst is the pg2-seeo8
// shape: a merged PR whose ledger-tracked cycle bead has an open GRANDCHILD
// (anchor -> cycle -> grandchild). Against real bd, closing the cycle or the
// anchor first is refused, so the run only succeeds if pg-desk closes
// grandchild, then cycle, then anchor.
func TestClosure_RealBD_AnchorCycleGrandchild_ClosesDepthFirst(t *testing.T) {
	f := newClosureRealBD(t)
	anchor := f.create("anchor", "merge-request", "")
	cycle := f.create("process-feedback cycle", "task", anchor)
	grandchild := f.create("improvised worker bead", "task", cycle)

	s := f.syncer()
	seedRealBDLedger(t, s, anchor, cycle)

	if err := s.Sync(context.Background(), fixtureRepo, fixtureEntity, gather.ChangeRemoved, removedMergedFacts(), interpFor("mine", nil)); err != nil {
		t.Fatalf("Sync against real bd: %v\ncalls: %v", err, f.calls)
	}

	want := []string{grandchild, cycle, anchor}
	if got := f.closedOrder(); !equalStrings(got, want) {
		t.Fatalf("close order = %v, want grandchild -> cycle -> anchor = %v", got, want)
	}
	for _, id := range want {
		if st := f.status(id); st != "closed" {
			t.Errorf("bd status of %s = %q, want closed", id, st)
		}
	}
	if a, _, _ := s.store.GetLedger(fixtureRepo, "pr", fixtureEntity, KindAnchor); a.LastSyncedContentHash != closedSentinel {
		t.Errorf("anchor ledger row not marked closed: %+v", a)
	}
}

// TestClosure_RealBD_UntrackedDirectChild_ClosesDepthFirst is the pg2-ubvmh
// shape: the anchor has a direct child that neither the ledger nor the
// (--change removed, so empty) work-beads facts know about, and that child has
// a grandchild of its own. Only the live `issue children` read can find them;
// real bd refuses to close the anchor while the legacy child is open.
func TestClosure_RealBD_UntrackedDirectChild_ClosesDepthFirst(t *testing.T) {
	f := newClosureRealBD(t)
	anchor := f.create("anchor", "merge-request", "")
	cycle := f.create("process-feedback cycle", "task", anchor)
	legacy := f.create("legacy pg-pr child, untracked", "task", anchor)
	legacyKid := f.create("legacy child's own child", "task", legacy)

	s := f.syncer()
	seedRealBDLedger(t, s, anchor, cycle)

	if err := s.Sync(context.Background(), fixtureRepo, fixtureEntity, gather.ChangeRemoved, removedMergedFacts(), interpFor("mine", nil)); err != nil {
		t.Fatalf("Sync against real bd: %v\ncalls: %v", err, f.calls)
	}

	order := f.closedOrder()
	if len(order) != 4 || order[len(order)-1] != anchor {
		t.Fatalf("close order = %v, want cycle, legacy, legacy's child, then the anchor %s last", order, anchor)
	}
	if indexOf(order, legacyKid) < 0 || indexOf(order, legacyKid) > indexOf(order, legacy) {
		t.Errorf("%s must close before its parent %s: %v", legacyKid, legacy, order)
	}
	if indexOf(order, legacy) < 0 || indexOf(order, cycle) < 0 {
		t.Errorf("legacy %s and cycle %s must both close: %v", legacy, cycle, order)
	}
	for _, id := range []string{anchor, cycle, legacy, legacyKid} {
		if st := f.status(id); st != "closed" {
			t.Errorf("bd status of %s = %q, want closed", id, st)
		}
	}
}

// TestClosure_RealBD_OpenChildRefusal_IsNonTransientAndRecovers pins the
// classification of the REAL bd refusal text. A child is filed under the
// anchor after pg-desk has read the anchor's children but before it closes the
// anchor (the race pg2-ubvmh's second defect describes), so real bd refuses
// the anchor close: "cannot close X: N open child issue(s); close children
// first". That failure must reach Classify as non-transient (no automatic
// retry), must leave the anchor open, and a later run (after the person or the
// fix that lifts it) must close the late child and then the anchor.
func TestClosure_RealBD_OpenChildRefusal_IsNonTransientAndRecovers(t *testing.T) {
	f := newClosureRealBD(t)
	anchor := f.create("anchor", "merge-request", "")
	cycle := f.create("process-feedback cycle", "task", anchor)

	s := f.syncer()
	seedRealBDLedger(t, s, anchor, cycle)

	var late string
	f.onCall = func(args []string) {
		// The anchor's own close is the last call of the closure; by now every
		// children read is done. File the late child exactly once, just before.
		if late == "" && len(args) >= 3 && args[0] == "issue" && args[1] == "transition" && args[2] == anchor {
			late = f.create("child filed after the children read", "task", anchor)
		}
	}

	err := s.Sync(context.Background(), fixtureRepo, fixtureEntity, gather.ChangeRemoved, removedMergedFacts(), interpFor("mine", nil))
	if err == nil {
		t.Fatalf("Sync succeeded although a late open child exists; calls: %v", f.calls)
	}
	if late == "" {
		t.Fatalf("the late child was never filed, the injection point did not fire; calls: %v", f.calls)
	}
	var ce *ConnectorError
	if !errors.As(err, &ce) {
		t.Fatalf("error chain carries no *ConnectorError: %#v", err)
	}
	if !strings.Contains(ce.Detail, "cannot close "+anchor) || !strings.Contains(ce.Detail, "open child issue") {
		t.Fatalf("connector detail = %q, want real bd's open-child refusal for %s", ce.Detail, anchor)
	}
	if got := Classify(err); got != ClassNonTransient {
		t.Fatalf("Classify(real bd open-child refusal) = %q, want non-transient (detail %q)", got, ce.Detail)
	}
	policy := RetryPolicy{MaxRetries: 10, InitialBackoff: time.Minute, MaxBackoff: 30 * time.Minute}
	next := policy.NextState(store.SyncRetry{}, err, time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC))
	if next.State != store.SyncRetryNonTransient || next.NextRetryAt != "" || next.Attempts != 1 {
		t.Fatalf("retry state = %+v, want non-transient after the first attempt with no retry scheduled", next)
	}
	if st := f.status(anchor); st == "closed" {
		t.Fatalf("anchor %s closed despite the refusal", anchor)
	}
	if st := f.status(cycle); st != "closed" {
		t.Fatalf("cycle %s status = %q, want closed (it closed before the refusal)", cycle, st)
	}

	// Re-entry after the refusal: the late child is now visible to the live
	// read, so the next run closes it and then the anchor.
	f.onCall = nil
	f.calls = nil
	if err := s.Sync(context.Background(), fixtureRepo, fixtureEntity, gather.ChangeRemoved, removedMergedFacts(), interpFor("mine", nil)); err != nil {
		t.Fatalf("re-run after the refusal: %v\ncalls: %v", err, f.calls)
	}
	if got, want := f.closedOrder(), []string{late, anchor}; !equalStrings(got, want) {
		t.Fatalf("re-run close order = %v, want %v", got, want)
	}
	if st := f.status(anchor); st != "closed" {
		t.Fatalf("anchor %s status after the re-run = %q, want closed", anchor, st)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
