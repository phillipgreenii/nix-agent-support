package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/changes"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

func runDoctorCmd(t *testing.T) (stdout string, err error) {
	t.Helper()
	c, _, ferr := rootCmd.Find([]string{"doctor"})
	if ferr != nil {
		t.Fatalf("rootCmd has no doctor subcommand: %v", ferr)
	}
	var buf bytes.Buffer
	c.SetContext(context.Background())
	c.SetOut(&buf)
	err = c.RunE(c, nil)
	return buf.String(), err
}

// emptyWorkBeadsFanOut is the default doctorFanOutIssueList stub payload
// for every doctor test that does not care about the stranded-cycle check
// specifically — zero entities, so doctorStrandedCycles always reports "0"
// without needing a real pg-connector or any seeded interpretation rows.
const emptyWorkBeadsFanOut = `{"entities":[]}`

func stubDoctorSeams(t *testing.T, lookPathErr, configValidateErr, serveErr error) {
	t.Helper()
	origLookPath := doctorLookPath
	origConfigValidate := doctorConfigValidate
	origServeReachable := doctorServeReachable
	origFanOutIssueList := doctorFanOutIssueList
	t.Cleanup(func() {
		doctorLookPath = origLookPath
		doctorConfigValidate = origConfigValidate
		doctorServeReachable = origServeReachable
		doctorFanOutIssueList = origFanOutIssueList
	})
	doctorLookPath = func(name string) (string, error) {
		if lookPathErr != nil {
			return "", lookPathErr
		}
		return "/usr/local/bin/" + name, nil
	}
	doctorConfigValidate = func(ctx context.Context) error { return configValidateErr }
	doctorServeReachable = func(addr string) error { return serveErr }
	doctorFanOutIssueList = func(ctx context.Context, cfg *config.Config) ([]byte, error) {
		return []byte(emptyWorkBeadsFanOut), nil
	}
	// A non-owning backend, asked for its capabilities, declares one watched
	// query, so a seeded issue entity attributed to doctorSeedBackend does not
	// trip the gate "backend without watched query".
	origCaps := doctorIssueCapabilityer
	t.Cleanup(func() { doctorIssueCapabilityer = origCaps })
	doctorIssueCapabilityer = func(*config.Config) changes.Capabilityer { return declaringCapabilityer{} }
}

// doctorSeedBackend is the non-owning backend instance the doctor tests that
// seed issue entities attribute them to.
const doctorSeedBackend = "beads-seed"

// declaringCapabilityer answers every capabilities call with a non-owning
// backend row that declares one watched query.
type declaringCapabilityer struct{}

func (declaringCapabilityer) Capabilities(_ context.Context, _, backend string) (gather.CapabilitiesAnswer, error) {
	return gather.CapabilitiesAnswer{Backends: []gather.CapBackend{{
		Backend: backend, Vocabulary: map[string]any{"owns_changes": false},
		Queries: map[string]gather.CapQuery{"q": {Watched: true}},
	}}}, nil
}

// withDoctorStore opens a real (empty) test store and wires it through
// deskConfigLoad/deskStoreOpen via withOpenSeams — every doctor test needs
// one now that doctorStrandedCycles opens the store unconditionally
// (mirrors openTestStore's own doc comment: "doctor's own tests ... pass
// nil" no longer holds once doctor reads the store).
func withDoctorStore(t *testing.T, repo string) {
	t.Helper()
	_, openFresh := openTestStore(t)
	withOpenSeams(t, openTestConfig(repo), openFresh)
}

func TestDoctorAllChecksPass(t *testing.T) {
	withDoctorStore(t, "o/r")
	stubDoctorSeams(t, nil, nil, nil)

	stdout, err := runDoctorCmd(t)
	if err != nil {
		t.Fatalf("doctor: %v, want every check to pass", err)
	}
	if !strings.Contains(stdout, "config: ok") {
		t.Errorf("stdout missing config check: %s", stdout)
	}
	if !strings.Contains(stdout, "stranded cycles: 0") {
		t.Errorf("stdout missing the stranded-cycle report: %s", stdout)
	}
	if strings.Contains(stdout, "sync not active in Phase 9") {
		t.Errorf("stdout still contains the retired hardcoded placeholder: %s", stdout)
	}
}

func TestDoctorFailsWhenPgConnectorMissing(t *testing.T) {
	withDoctorStore(t, "o/r")
	stubDoctorSeams(t, errors.New("not found"), nil, nil)

	stdout, err := runDoctorCmd(t)
	if err == nil {
		t.Fatal("doctor: error = nil, want a failure naming the missing binary")
	}
	if !strings.Contains(stdout, "FAIL") {
		t.Errorf("stdout does not mark the failing check: %s", stdout)
	}
	if !strings.Contains(err.Error(), "pg-connector on PATH") {
		t.Errorf("error %q does not name the failing check", err)
	}
}

func TestDoctorFailsWhenConfigValidateFails(t *testing.T) {
	withDoctorStore(t, "o/r")
	stubDoctorSeams(t, nil, errors.New("exit status 1"), nil)

	_, err := runDoctorCmd(t)
	if err == nil {
		t.Fatal("doctor: error = nil, want a failure")
	}
	if !strings.Contains(err.Error(), "config validate") {
		t.Errorf("error %q does not name the failing check", err)
	}
}

func TestDoctorFailsWhenServeUnreachable(t *testing.T) {
	withDoctorStore(t, "o/r")
	stubDoctorSeams(t, nil, nil, errors.New("connection refused"))

	_, err := runDoctorCmd(t)
	if err == nil {
		t.Fatal("doctor: error = nil, want a failure")
	}
	if !strings.Contains(err.Error(), "serve reachable") {
		t.Errorf("error %q does not name the failing check", err)
	}
}

func TestDoctorReportsMultipleFailures(t *testing.T) {
	withDoctorStore(t, "o/r")
	stubDoctorSeams(t, errors.New("not found"), errors.New("exit status 1"), errors.New("refused"))

	_, err := runDoctorCmd(t)
	if err == nil {
		t.Fatal("doctor: error = nil, want a failure")
	}
	if !strings.Contains(err.Error(), "3 check(s) failed") {
		t.Errorf("error %q does not report all 3 failures", err)
	}
}

// strandedFixtureFanOut is one open feedback-cycle bead for o/r#5, titled
// and metadata'd exactly as internal/sync/rules.go's ensureCycle writes
// one (process-feedback: <repo>#<n>, metadata.repo/pr_number) — mirroring
// how the recovered pre-deletion reconcile.go package's own tests
// constructed a stranded-cycle fixture (see this packet's Validation
// section), adapted to pg-desk's bead shapes (internal/sync/classify.go).
// mine=true adds the "mine" label, exercising the "already labeled, not
// stranded" branch.
func strandedFixtureFanOut(mine bool) string {
	labels := `[]`
	if mine {
		labels = `["mine"]`
	}
	return `{"entities":[{"id":"bd-123","title":"process-feedback: o/r#5","state":"open","labels":` + labels + `,"metadata":{"repo":"o/r","pr_number":"5"}}]}`
}

func TestDoctorStrandedCyclesReportsRealFinding(t *testing.T) {
	seed, openFresh := openTestStore(t)
	withOpenSeams(t, openTestConfig("o/r"), openFresh)
	stubDoctorSeams(t, nil, nil, nil)
	doctorFanOutIssueList = func(ctx context.Context, cfg *config.Config) ([]byte, error) {
		return []byte(strandedFixtureFanOut(false)), nil
	}

	if err := seed.UpsertInterpretation(store.Interpretation{
		Repo: "o/r", EntityType: entityTypePR, EntityID: "o/r#5",
		Ownership: ownershipMine, AsOf: "2026-09-22T00:00:00Z",
	}); err != nil {
		t.Fatalf("seed interpretation: %v", err)
	}

	stdout, err := runDoctorCmd(t)
	if err != nil {
		t.Fatalf("doctor: %v, want the stranded-cycle finding to be reported, not a failure", err)
	}
	if !strings.Contains(stdout, "stranded cycles: 1") {
		t.Errorf("stdout does not report the real finding: %s", stdout)
	}
	if !strings.Contains(stdout, "o/r#5") {
		t.Errorf("stdout does not name the stranded PR: %s", stdout)
	}
	if strings.Contains(stdout, "sync not active in Phase 9") {
		t.Errorf("stdout still contains the retired hardcoded placeholder: %s", stdout)
	}
}

func TestDoctorStrandedCyclesSkipsAlreadyLabeledMine(t *testing.T) {
	seed, openFresh := openTestStore(t)
	withOpenSeams(t, openTestConfig("o/r"), openFresh)
	stubDoctorSeams(t, nil, nil, nil)
	doctorFanOutIssueList = func(ctx context.Context, cfg *config.Config) ([]byte, error) {
		return []byte(strandedFixtureFanOut(true)), nil
	}

	if err := seed.UpsertInterpretation(store.Interpretation{
		Repo: "o/r", EntityType: entityTypePR, EntityID: "o/r#5",
		Ownership: ownershipMine, AsOf: "2026-09-22T00:00:00Z",
	}); err != nil {
		t.Fatalf("seed interpretation: %v", err)
	}

	stdout, err := runDoctorCmd(t)
	if err != nil {
		t.Fatalf("doctor: %v, want every check to pass", err)
	}
	if !strings.Contains(stdout, "stranded cycles: 0") {
		t.Errorf("stdout should not flag an already-mine-labeled cycle: %s", stdout)
	}
}

func TestDoctorStrandedCyclesSkipsUnknownOwnership(t *testing.T) {
	withDoctorStore(t, "o/r")
	stubDoctorSeams(t, nil, nil, nil)
	doctorFanOutIssueList = func(ctx context.Context, cfg *config.Config) ([]byte, error) {
		return []byte(strandedFixtureFanOut(false)), nil
	}
	// No interpretation row seeded for o/r#5 -- self is unattributable, so
	// this cycle is conservatively skipped (StrandedSelfCycles' own "no
	// parent bead or unknown self" rule).

	stdout, err := runDoctorCmd(t)
	if err != nil {
		t.Fatalf("doctor: %v, want every check to pass", err)
	}
	if !strings.Contains(stdout, "stranded cycles: 0") {
		t.Errorf("stdout should not flag a cycle with unknown ownership: %s", stdout)
	}
}

func TestDoctorFailsWhenStrandedCyclesFanOutFails(t *testing.T) {
	withDoctorStore(t, "o/r")
	stubDoctorSeams(t, nil, nil, nil)
	doctorFanOutIssueList = func(ctx context.Context, cfg *config.Config) ([]byte, error) {
		return nil, errors.New("exit 3: total failure")
	}

	_, err := runDoctorCmd(t)
	if err == nil {
		t.Fatal("doctor: error = nil, want a failure")
	}
	if !strings.Contains(err.Error(), "stranded cycles") {
		t.Errorf("error %q does not name the failing check", err)
	}
}

func TestDoctorFailsOnNonEmptySyncError(t *testing.T) {
	seed, openFresh := openTestStore(t)
	withOpenSeams(t, openTestConfig("o/r"), openFresh)
	stubDoctorSeams(t, nil, nil, nil)
	if err := seed.UpsertInterpretation(store.Interpretation{
		Repo: "o/r", EntityType: entityTypePR, EntityID: "o/r#9",
		SyncError: "close failed", AsOf: "2026-09-22T00:00:00Z",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	stdout, err := runDoctorCmd(t)
	if err == nil || !strings.Contains(err.Error(), "sync_error rows") {
		t.Fatalf("doctor err = %v, want failure naming sync_error rows", err)
	}
	if !strings.Contains(stdout, "o/r#9: close failed") {
		t.Errorf("stdout does not list the row: %s", stdout)
	}
	// No recorded retry state yet: the row awaits its first retry.
	if !strings.Contains(stdout, "[retrying: awaiting its first automatic retry]") {
		t.Errorf("stdout lacks the retry indicator: %s", stdout)
	}
}

// TestDoctorShowsSyncErrorRetryIndicator: doctor tells a row still being
// retried from an exhausted or non-transient one (bead pg2-xb6fs).
func TestDoctorShowsSyncErrorRetryIndicator(t *testing.T) {
	seed, openFresh := openTestStore(t)
	withOpenSeams(t, openTestConfig("o/r"), openFresh)
	stubDoctorSeams(t, nil, nil, nil)
	for id, r := range map[string]store.SyncRetry{
		"o/r#1": {Attempts: 3, MaxRetries: 10, State: store.SyncRetryRetrying, NextRetryAt: "2026-09-30T12:04:00Z"},
		"o/r#2": {Attempts: 11, MaxRetries: 10, State: store.SyncRetryExhausted},
		"o/r#3": {Attempts: 1, MaxRetries: 10, State: store.SyncRetryNonTransient},
	} {
		if err := seed.UpsertInterpretation(store.Interpretation{
			Repo: "o/r", EntityType: entityTypePR, EntityID: id, SyncError: "boom", AsOf: "2026-09-30T12:00:00Z",
		}); err != nil {
			t.Fatalf("seed interpretation %s: %v", id, err)
		}
		if err := seed.SetSyncRetry(id, r); err != nil {
			t.Fatalf("seed retry state %s: %v", id, err)
		}
	}
	stdout, err := runDoctorCmd(t)
	if err == nil {
		t.Fatal("doctor: want the sync_error gate to fail")
	}
	for _, want := range []string{
		"o/r#1: boom [retrying: 2/10 retries used, next retry at 2026-09-30T12:04:00Z]",
		"o/r#2: boom [exhausted: 10/10 retries failed, no further automatic retry; fix the cause, then pg-desk reconcile --retry-all]",
		"o/r#3: boom [non-transient: not retried automatically; fix the cause, then pg-desk reconcile --retry-all]",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout lacks %q:\n%s", want, stdout)
		}
	}
}

// ---- change-flow checks [design: 11] ----

// doctorNow is the fixed clock the change-flow doctor tests run at.
var doctorNow = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

const doctorRouterFixture = "testdata/doctor_router_config.toml"

// newSchemaDoctor wires a cut-over (change-flow schema) store and cfg into
// the seams, stubs the doctor probes, and freezes the clock; it returns the
// seed handle for fixtures.
func newSchemaDoctor(t *testing.T, cfg *config.Config) *store.Store {
	t.Helper()
	seed, openFresh := openTestStore(t)
	if err := seed.Cutover(); err != nil {
		t.Fatalf("Cutover: %v", err)
	}
	withOpenSeams(t, cfg, openFresh)
	stubDoctorSeams(t, nil, nil, nil)
	origNow := changesNow
	t.Cleanup(func() { changesNow = origNow })
	changesNow = func() time.Time { return doctorNow }
	return seed
}

// runDoctorWithRouter runs doctor with --router-config set (and reset after,
// since the command and its flags are process-global).
func runDoctorWithRouter(t *testing.T, path string) (string, error) {
	t.Helper()
	c, _, ferr := rootCmd.Find([]string{"doctor"})
	if ferr != nil {
		t.Fatal(ferr)
	}
	if err := c.Flags().Set("router-config", path); err != nil {
		t.Fatalf("set --router-config: %v", err)
	}
	t.Cleanup(func() { _ = c.Flags().Set("router-config", "") })
	return runDoctorCmd(t)
}

func watchCfg(queries ...string) *config.Config {
	cfg := openTestConfig("o/r")
	cfg.Watch.PR.Queries = queries
	return cfg
}

func TestDoctorReportsUnmigratedStoreWithoutRefusing(t *testing.T) {
	withDoctorStore(t, "o/r") // openTestStore is an old-schema (not cut over) store
	stubDoctorSeams(t, nil, nil, nil)

	stdout, err := runDoctorCmd(t)
	if err != nil {
		t.Fatalf("doctor on an unmigrated store: %v, want it to report, not refuse\n%s", err, stdout)
	}
	for _, want := range []string{"unmigrated", "pg-desk migrate --cutover", "sync_error rows: 0", "stranded cycles: 0"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout lacks %q:\n%s", want, stdout)
		}
	}
	for _, skipped := range []string{"watched queries:", "consumers:", "sweep bound:"} {
		if strings.Contains(stdout, skipped) {
			t.Errorf("stdout ran the new-schema-only check %q on an unmigrated store:\n%s", skipped, stdout)
		}
	}
}

func TestDoctorReportsUnmigratedStoreWithNoSchemaAtAll(t *testing.T) {
	withRawStoreAt(t, storeAtVersion(t, "empty"))
	stubDoctorSeams(t, nil, nil, nil)
	stdout, err := runDoctorCmd(t)
	if err != nil {
		t.Fatalf("doctor on an empty store: %v\n%s", err, stdout)
	}
	if !strings.Contains(stdout, "unmigrated") {
		t.Errorf("stdout does not report the store as unmigrated:\n%s", stdout)
	}
}

func TestDoctorWatchedQueryResolution(t *testing.T) {
	newSchemaDoctor(t, watchCfg("mine", "nope"))
	rec := filepath.Join(t.TempDir(), "calls.txt")
	installFakePGConnector(t, fmt.Sprintf(`echo "$*" >> %q
case "$*" in
  *"--query nope"*) echo '{"error":{"code":"invalid_argument","message":"query_not_recognized: nope"}}'; exit 1 ;;
esac
echo '{"entities":[]}'`, rec))

	stdout, err := runDoctorCmd(t)
	if err == nil || !strings.Contains(err.Error(), "watched queries") {
		t.Fatalf("doctor err = %v, want a failure naming watched queries\n%s", err, stdout)
	}
	if !strings.Contains(stdout, `pr "mine": ok`) || !strings.Contains(stdout, `pr "nope": FAIL`) {
		t.Errorf("stdout does not separate the resolvable from the unknown query:\n%s", stdout)
	}
	if !strings.Contains(stdout, "query_not_recognized") {
		t.Errorf("stdout does not carry pg-connector's reason:\n%s", stdout)
	}
	calls, _ := os.ReadFile(rec)
	for _, line := range strings.Split(strings.TrimSpace(string(calls)), "\n") {
		if !strings.HasPrefix(line, "pr list --query ") || strings.Contains(line, "changes") {
			t.Errorf("probe exec %q is not the read-only list verb", line)
		}
	}
}

func TestDoctorWatchedQueriesAllResolveAndNoneConfigured(t *testing.T) {
	newSchemaDoctor(t, watchCfg("mine"))
	installFakePGConnector(t, `echo '{"entities":[]}'`)
	stdout, err := runDoctorCmd(t)
	if err != nil {
		t.Fatalf("doctor: %v\n%s", err, stdout)
	}
	if !strings.Contains(stdout, `pr "mine": ok`) {
		t.Errorf("stdout:\n%s", stdout)
	}

	newSchemaDoctor(t, openTestConfig("o/r"))
	stdout, err = runDoctorCmd(t)
	if err != nil {
		t.Fatalf("doctor: %v\n%s", err, stdout)
	}
	if !strings.Contains(stdout, "none configured") {
		t.Errorf("stdout:\n%s", stdout)
	}
}

func TestDoctorStalledConsumerWithRouterConfig(t *testing.T) {
	seed := newSchemaDoctor(t, openTestConfig("o/r"))
	// pr/pg-router has a 60s router period, so 3x = 3m. Seen 4m ago: stalled.
	if err := seed.RegisterConsumer("pg-router", "pr", doctorNow.Add(-4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	// issue/pg-router has a 5m period, so 3x = 15m. Seen 4m ago: fine.
	if err := seed.RegisterConsumer("pg-router", "issue", doctorNow.Add(-4*time.Minute)); err != nil {
		t.Fatal(err)
	}

	stdout, err := runDoctorWithRouter(t, doctorRouterFixture)
	if err == nil || !strings.Contains(err.Error(), "stalled consumers") {
		t.Fatalf("doctor err = %v, want a failure naming stalled consumers\n%s", err, stdout)
	}
	if !strings.Contains(stdout, "pr/pg-router:") || !strings.Contains(stdout, "STALLED") {
		t.Errorf("stdout does not flag the pr consumer:\n%s", stdout)
	}
	for _, line := range strings.Split(stdout, "\n") {
		if strings.Contains(line, "issue/pg-router:") && !strings.HasSuffix(line, ": ok") {
			t.Errorf("issue consumer within 3x its period must be ok: %q", line)
		}
	}
	// reported, not pruned
	cs, err := seed.ListConsumers()
	if err != nil || len(cs) != 2 {
		t.Errorf("consumers after doctor = %+v, %v; doctor must not prune", cs, err)
	}
}

func TestDoctorStalledConsumerWithoutRouterConfigUsesConsumerStaleAfter(t *testing.T) {
	seed := newSchemaDoctor(t, openTestConfig("o/r"))
	if err := seed.RegisterConsumer("fresh", "pr", doctorNow.Add(-4*time.Hour)); err != nil {
		t.Fatal(err)
	}
	stdout, err := runDoctorCmd(t)
	if err != nil {
		t.Fatalf("a consumer seen hours ago is within the 7d default: %v\n%s", err, stdout)
	}
	if err := seed.RegisterConsumer("old", "pr", doctorNow.Add(-8*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	stdout, err = runDoctorCmd(t)
	if err == nil || !strings.Contains(err.Error(), "stalled consumers") {
		t.Fatalf("doctor err = %v, want stalled consumers\n%s", err, stdout)
	}
	if !strings.Contains(stdout, "pr/old:") || !strings.Contains(stdout, "consumer_stale_after") {
		t.Errorf("stdout:\n%s", stdout)
	}
}

func seedActive(t *testing.T, st *store.Store, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		e := store.Entity{Repo: "o/r", EntityType: entityTypePR, EntityID: fmt.Sprintf("o/r#%d", i+1), Facts: `{}`, AsOf: "x"}
		if _, err := st.WriteEntityStateWithLog(e, 0, doctorNow.Format(time.RFC3339), true, []string{"opened"}, "test", doctorNow.Format(time.RFC3339)); err != nil {
			t.Fatal(err)
		}
	}
}

func sweepCfg(maxAge string, maxPerPoll int) *config.Config {
	cfg := openTestConfig("o/r")
	cfg.Sweep.MaxAge = maxAge
	cfg.Sweep.MaxPerPoll = &maxPerPoll
	return cfg
}

func TestDoctorSweepBoundViolation(t *testing.T) {
	// 7 active / N=1 x the fixture's smallest pr period (30s) = 210s > D=3m.
	seed := newSchemaDoctor(t, sweepCfg("3m", 1))
	seedActive(t, seed, 7)

	stdout, err := runDoctorWithRouter(t, doctorRouterFixture)
	if err == nil || !strings.Contains(err.Error(), "sweep bound") {
		t.Fatalf("doctor err = %v, want a failure naming the sweep bound\n%s", err, stdout)
	}
	if !strings.Contains(stdout, "active_count=7 max_per_poll=1 max_age=3m0s poll_interval=30s: VIOLATED") {
		t.Errorf("stdout:\n%s", stdout)
	}
}

func TestDoctorSweepBoundIsCheckedPerTier(t *testing.T) {
	// 7 active / 1 x 30s = 210s: the remote tier (1h) holds, the local tier (3m)
	// is violated.
	cfg := sweepCfg("1h", 1)
	cfg.Sweep.ReconcileAge = "3m"
	seed := newSchemaDoctor(t, cfg)
	seedActive(t, seed, 7)

	stdout, err := runDoctorWithRouter(t, doctorRouterFixture)
	if err == nil || !strings.Contains(err.Error(), "sweep bound") {
		t.Fatalf("doctor err = %v, want a failure naming the sweep bound\n%s", err, stdout)
	}
	if !strings.Contains(stdout, "pr remote: active_count=7 max_per_poll=1 max_age=1h0m0s poll_interval=30s: holds") {
		t.Errorf("remote tier should hold:\n%s", stdout)
	}
	if !strings.Contains(stdout, "pr local: active_count=7 max_per_poll=1 max_age=3m0s poll_interval=30s: VIOLATED") {
		t.Errorf("local tier should be violated:\n%s", stdout)
	}
}

func TestDoctorSweepBoundHoldsAtTheBoundary(t *testing.T) {
	// 6 active / 1 x 30s = 180s == D=3m: holds (<=).
	seed := newSchemaDoctor(t, sweepCfg("3m", 1))
	seedActive(t, seed, 6)

	stdout, err := runDoctorWithRouter(t, doctorRouterFixture)
	if err != nil {
		t.Fatalf("doctor: %v\n%s", err, stdout)
	}
	if !strings.Contains(stdout, "poll_interval=30s: holds") {
		t.Errorf("stdout:\n%s", stdout)
	}
}

func TestDoctorSweepBoundNeedsRouterConfigForAVerdict(t *testing.T) {
	seed := newSchemaDoctor(t, sweepCfg("3m", 1))
	seedActive(t, seed, 7) // would violate IF a poll interval were known

	stdout, err := runDoctorCmd(t)
	if err != nil {
		t.Fatalf("no router config means no verdict, so no failure: %v\n%s", err, stdout)
	}
	if !strings.Contains(stdout, "active_count=7 max_per_poll=1 max_age=3m0s poll_interval: unknown (no verdict)") {
		t.Errorf("stdout does not report the inputs with poll_interval unknown:\n%s", stdout)
	}
	if strings.Contains(stdout, "VIOLATED") || strings.Contains(stdout, "holds") {
		t.Errorf("stdout gave a verdict without a poll interval:\n%s", stdout)
	}
}

func TestDoctorRouterConfigListsRolesBoundPerType(t *testing.T) {
	newSchemaDoctor(t, openTestConfig("o/r"))
	stdout, err := runDoctorWithRouter(t, doctorRouterFixture)
	if err != nil {
		t.Fatalf("doctor: %v\n%s", err, stdout)
	}
	section := stdout[strings.Index(stdout, "router roles:"):]
	for _, want := range []string{"pr: pr-decider", "issue: none", "thread: none"} {
		if !strings.Contains(section, want) {
			t.Errorf("router roles section lacks %q:\n%s", want, section)
		}
	}
	if strings.Contains(section, "note-reader") {
		t.Errorf("a role bound to no desk type must not be listed:\n%s", section)
	}
}

func TestDoctorWithoutRouterConfigOmitsRoles(t *testing.T) {
	newSchemaDoctor(t, openTestConfig("o/r"))
	stdout, err := runDoctorCmd(t)
	if err != nil {
		t.Fatalf("doctor: %v\n%s", err, stdout)
	}
	if strings.Contains(stdout, "router roles:") {
		t.Errorf("roles are reported only with --router-config:\n%s", stdout)
	}
}

func TestDoctorFailsOnUnreadableRouterConfig(t *testing.T) {
	newSchemaDoctor(t, openTestConfig("o/r"))
	stdout, err := runDoctorWithRouter(t, filepath.Join(t.TempDir(), "missing.toml"))
	if err == nil || !strings.Contains(err.Error(), "router config") {
		t.Fatalf("doctor err = %v, want a failure naming router config\n%s", err, stdout)
	}
	if !strings.Contains(stdout, "change_flow:") {
		t.Errorf("the other change-flow checks must still run:\n%s", stdout)
	}
}

func TestDoctorReportsRepeatedDegradedHydrations(t *testing.T) {
	seed := newSchemaDoctor(t, openTestConfig("o/r"))
	if err := seed.SetMeta("change_flow.degraded.pr", `{"o/r#7":{"count":3,"since":"2026-10-01T09:00:00Z"},"o/r#8":{"count":1,"since":"2026-10-01T10:00:00Z"}}`); err != nil {
		t.Fatal(err)
	}
	stdout, err := runDoctorCmd(t)
	if err != nil {
		t.Fatalf("a report, not a gate: %v\n%s", err, stdout)
	}
	if !strings.Contains(stdout, "pr o/r#7: count=3 since=2026-10-01T09:00:00Z") {
		t.Errorf("stdout lacks the repeated entity:\n%s", stdout)
	}
	if strings.Contains(stdout, "o/r#8") {
		t.Errorf("a single degraded hydration is not 'repeated':\n%s", stdout)
	}
}

// bead pg2-903lz: the ledger-based `work-beads reach` check was removed (the
// cutover drops the ledger, so on a version-2 store it could only ever be
// skipped). This guards that a cut-over store runs every remaining store
// check and that none is reported as skipped or fails on a removed table.
func TestDoctorRunsEveryStoreCheckOnACutOverStore(t *testing.T) {
	newSchemaDoctor(t, openTestConfig("o/r"))

	stdout, err := runDoctorCmd(t)
	if err != nil {
		t.Fatalf("doctor on a healthy cut-over store: %v\n%s", err, stdout)
	}
	for _, want := range []string{"sync_error rows: 0", "stranded cycles: 0", "change_flow:"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout lacks %q:\n%s", want, stdout)
		}
	}
	for _, bad := range []string{"skipped", "no such table", "ledger", "work-beads reach"} {
		if strings.Contains(stdout, bad) {
			t.Errorf("stdout contains %q, a check read a removed table or was skipped:\n%s", bad, stdout)
		}
	}
}

// bead pg2-903lz: a serve that accepted the connection but is slow is not
// unreachable, so doctor reports it and exits 0.
func TestDoctorReportsSlowServeWithoutFailing(t *testing.T) {
	withDoctorStore(t, "o/r")
	stubDoctorSeams(t, nil, nil, &serveSlowError{detail: "answered in 4.55s (over 3s)"})

	stdout, err := runDoctorCmd(t)
	if err != nil {
		t.Fatalf("doctor: %v, want a slow serve to be reported, not failed\n%s", err, stdout)
	}
	if !strings.Contains(stdout, "serve reachable (127.0.0.1:9818): slow (answered in 4.55s") {
		t.Errorf("stdout does not report the slow serve:\n%s", stdout)
	}
	if strings.Contains(stdout, "serve reachable (127.0.0.1:9818): ok") || strings.Contains(stdout, "FAIL") {
		t.Errorf("slow must be distinct from ok and FAIL:\n%s", stdout)
	}
}

// The probe tests below run against a REAL listener with a real delayed
// handler (bead pg2-903lz); no part of the HTTP client is mocked.

func probeFixtureAddr(t *testing.T, h http.HandlerFunc) string {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://")
}

func TestProbeServeFastAnswerIsReachable(t *testing.T) {
	addr := probeFixtureAddr(t, func(w http.ResponseWriter, r *http.Request) {})
	if err := probeServe(addr, 5*time.Second, 2*time.Second); err != nil {
		t.Fatalf("probeServe: %v, want nil", err)
	}
}

// Any status counts as reachable (serve's 503 gate still means it answered).
func TestProbeServeAnyStatusIsReachable(t *testing.T) {
	addr := probeFixtureAddr(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) })
	if err := probeServe(addr, 5*time.Second, 2*time.Second); err != nil {
		t.Fatalf("probeServe: %v, want nil for a 503", err)
	}
}

// A daemon that answers, but only after the slow threshold, is slow, not
// failed: the 4.55 s /metrics response that failed the old 3 s deadline.
func TestProbeServeLateAnswerIsSlowNotUnreachable(t *testing.T) {
	addr := probeFixtureAddr(t, func(w http.ResponseWriter, r *http.Request) { time.Sleep(300 * time.Millisecond) })
	err := probeServe(addr, 5*time.Second, 100*time.Millisecond)
	var slow *serveSlowError
	if !errors.As(err, &slow) {
		t.Fatalf("probeServe: %v, want a *serveSlowError", err)
	}
}

// A daemon that accepted the connection but never answers within the deadline
// is slow too: the connection was made, so it is not unreachable.
func TestProbeServeNoAnswerWithinDeadlineIsSlowNotUnreachable(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	addr := probeFixtureAddr(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	err := probeServe(addr, 200*time.Millisecond, 100*time.Millisecond)
	var slow *serveSlowError
	if !errors.As(err, &slow) {
		t.Fatalf("probeServe: %v, want a *serveSlowError", err)
	}
	if !strings.Contains(slow.Error(), "no response within") {
		t.Errorf("detail %q does not say no response arrived", slow.Error())
	}
}

// A refused connection is the only unreachable case.
func TestProbeServeRefusedConnectionIsUnreachable(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	err = probeServe(addr, 5*time.Second, 2*time.Second)
	if err == nil {
		t.Fatal("probeServe: nil, want an error for a closed port")
	}
	var slow *serveSlowError
	if errors.As(err, &slow) {
		t.Fatalf("probeServe: %v, a refused connection must not be reported as slow", err)
	}
}

// seedIssueEntity writes one issue entity with the given facts; active false
// writes it deactivated.
func seedIssueEntity(t *testing.T, st *store.Store, id, facts string, active bool) {
	t.Helper()
	e := store.Entity{Repo: "o/r", EntityType: "issue", EntityID: id, Facts: facts, AsOf: doctorNow.Format(time.RFC3339)}
	if _, err := st.WriteEntityStateWithLog(e, 0, doctorNow.Format(time.RFC3339), active, []string{"opened"}, "test", doctorNow.Format(time.RFC3339)); err != nil {
		t.Fatalf("seed issue %s: %v", id, err)
	}
	inst := doctorSeedBackend
	if err := st.SetEntityColumns("o/r", "issue", id, &inst, nil); err != nil {
		t.Fatalf("attribute issue %s: %v", id, err)
	}
}

// TestDoctorCountsActiveJiraIssuesWithoutStatusCategory: the report counts
// only ACTIVE issue entities shaped like a ticket key whose stored issue_show
// has no status category; beads ids, inactive rows, never-hydrated rows and
// rows with a category are not counted. It is a report line, never a failure.
func TestDoctorCountsActiveJiraIssuesWithoutStatusCategory(t *testing.T) {
	cfg := openTestConfig("o/r")
	cfg.TicketPatterns = []string{`[A-Z][A-Z0-9]+-[0-9]+`}
	seed := newSchemaDoctor(t, cfg)
	seedIssueEntity(t, seed, "ABC-1", `{"issue_show":{"state":"Complete"}}`, true)                        // counted
	seedIssueEntity(t, seed, "ABC-2", `{"issue_show":{"state":"To Do","status_category":"new"}}`, true)   // has category
	seedIssueEntity(t, seed, "ABC-3", `{"issue_show":{"state":"Complete"}}`, false)                       // inactive
	seedIssueEntity(t, seed, "ABC-4", `{}`, true)                                                         // never hydrated
	seedIssueEntity(t, seed, "ABC-5", `{"issue_show":{"state":"Doing","status_category":"weird"}}`, true) // unrecognized: counted
	seedIssueEntity(t, seed, "pg2-abc", `{"issue_show":{"state":"open"}}`, true)                          // beads id, not Jira

	stdout, err := runDoctorCmd(t)
	if err != nil {
		t.Fatalf("a report, not a gate: %v\n%s", err, stdout)
	}
	if !strings.Contains(stdout, "2 active Jira issues lack a status category") {
		t.Errorf("stdout lacks the count of 2:\n%s", stdout)
	}
}

func TestDoctorStatusCategoryLineIsZeroWithoutTicketPatterns(t *testing.T) {
	seed := newSchemaDoctor(t, openTestConfig("o/r"))
	seedIssueEntity(t, seed, "ABC-1", `{"issue_show":{"state":"Complete"}}`, true)
	stdout, err := runDoctorCmd(t)
	if err != nil {
		t.Fatalf("doctor: %v\n%s", err, stdout)
	}
	if !strings.Contains(stdout, "0 active Jira issues lack a status category") {
		t.Errorf("stdout lacks the zero count:\n%s", stdout)
	}
}

// TestSyncErrorRemedyIsPhaseAware: the remedy named for a sync_error row never
// points at pg-desk reconcile --retry-all on a store that dropped the ledger
// (schema 2), where that command fails; it names the pr lane's composed command
// in the lane's mode, or says the lane is off. On a store below schema 2 the
// legacy command stays the remedy.
func TestSyncErrorRemedyIsPhaseAware(t *testing.T) {
	for _, tc := range []struct {
		name     string
		version  int
		laneMode string
		want     string
	}{
		{"schema 1, lane off", 1, "off", "fix the cause, then pg-desk reconcile --retry-all"},
		{"schema 1, lane apply", 1, "apply", "fix the cause, then pg-desk reconcile --retry-all"},
		{"schema 2, lane plan", store.NewSchemaVersion, "plan", "the ledger is dropped, so pg-desk reconcile --retry-all no longer works; the pr lane replaces it: fix the cause, then run pg-pr-change plan o/r#2"},
		{"schema 2, lane apply", store.NewSchemaVersion, "apply", "the ledger is dropped, so pg-desk reconcile --retry-all no longer works; the pr lane replaces it: fix the cause, then run pg-pr-change apply o/r#2"},
		{"schema 2, lane off", store.NewSchemaVersion, "off", "the ledger is dropped, so pg-desk reconcile --retry-all no longer works, and the pr lane is off (pr_lane.mode): nothing retries this row until the lane runs pg-pr-change plan o/r#2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := syncErrorRemedy(tc.version, tc.laneMode, "o/r#2"); got != tc.want {
				t.Errorf("remedy:\n got: %s\nwant: %s", got, tc.want)
			}
		})
	}
	r := store.SyncRetry{Attempts: 11, MaxRetries: 10, State: store.SyncRetryExhausted}
	got := describeSyncRetry(r, true, syncErrorRemedy(store.NewSchemaVersion, "plan", "o/r#2"))
	if strings.Contains(got, "; fix the cause, then pg-desk reconcile") {
		t.Errorf("the schema-2 indicator still names the failing command: %s", got)
	}
}

// ---- Issue change-flow gates of the owning backends (T-8, doctor half) ----

const (
	gateInst = "owning-demo"

	gateCapsOK = `{"sources":[{"source":"owning-demo","status":"succeeded","count":1}],"backends":[{"backend":"owning-demo",` +
		`"vocabulary":{"owns_changes":true},"queries":{"q-mine":{"watched":true},"q-team":{"watched":true}}},` +
		`{"backend":"beads-a","vocabulary":{"owns_changes":false},"queries":{"bd-a":{"watched":true}}},` +
		`{"backend":"beads-b","vocabulary":{"owns_changes":false},"queries":{"bd-b":{"watched":true}}}]}`
	gateHealthOK = `{"sources":[{"source":"owning-demo","status":"succeeded","count":1}],"backends":[{"backend":"owning-demo","status":{"queries":[` +
		`{"name":"q-mine","baseline_at":"2026-10-01T00:00:00Z"},{"name":"q-team","baseline_open_age_seconds":3600}]}}]}`
	gateUnavailable = `{"error":{"code":"unavailable","message":"daemon is down"}}`
)

// gateFixture is a cut-over store and a config with two owning queries on one
// instance and two beads-style queries on two other instances, over a scripted
// pg-connector answering `issue capabilities` and `issue health` from files.
type gateFixture struct {
	t    *testing.T
	seed *store.Store
	cfg  *config.Config
	dir  string
}

func newGateFixture(t *testing.T) *gateFixture {
	t.Helper()
	cfg := openTestConfig("o/r")
	cfg.TicketPatterns = []string{`[A-Z][A-Z0-9]+-[0-9]+`}
	cfg.Watch.Issue.Queries = []string{"q-mine", "q-team", "bd-a", "bd-b"}
	cfg.Watch.Issue.Backends = map[string]config.WatchBackend{
		"q-mine": {Instance: gateInst, OwnsChanges: true},
		"q-team": {Instance: gateInst, OwnsChanges: true},
		"bd-a":   {Instance: "beads-a"},
		"bd-b":   {Instance: "beads-b"},
	}
	f := &gateFixture{t: t, cfg: cfg, dir: t.TempDir()}
	f.seed = newSchemaDoctor(t, cfg)
	orig := doctorProbeQuery
	t.Cleanup(func() { doctorProbeQuery = orig })
	doctorProbeQuery = func(context.Context, *config.Config, string, string) error { return nil }
	// The gates read the scripted pg-connector below, not the stub of
	// stubDoctorSeams.
	doctorIssueCapabilityer = func(cfg *config.Config) changes.Capabilityer { return gather.NewGatherer(cfg, nil) }
	f.caps(gateCapsOK, 0)
	f.health(gateHealthOK, 0)
	installFakePGConnector(t, fmt.Sprintf(`D=%q
echo "$*" >> "$D/calls.log"
case "$2" in
  capabilities) cat "$D/caps.json"; exit "$(cat "$D/caps.exit")" ;;
  health) cat "$D/health.json"; exit "$(cat "$D/health.exit")" ;;
esac
echo '{"entities":[]}'`, f.dir))
	return f
}

func (f *gateFixture) write(name, body string) {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(f.dir, name), []byte(body), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *gateFixture) caps(body string, exit int) {
	f.write("caps.json", body)
	f.write("caps.exit", fmt.Sprint(exit))
}

func (f *gateFixture) health(body string, exit int) {
	f.write("health.json", body)
	f.write("health.exit", fmt.Sprint(exit))
}

// seedOwningEntity writes an issue entity carrying the owning backend (active
// or deactivated).
func (f *gateFixture) seedOwningEntity(id string, active bool) {
	f.t.Helper()
	inst := gateInst
	at := doctorNow.Format(time.RFC3339)
	e := store.Entity{Repo: "o/r", EntityType: "issue", EntityID: id, Facts: `{"issue_show":{"state":"open","status_category":"new"}}`, AsOf: at}
	if _, err := f.seed.WriteEntityStateWithLogOpts(e, 0, at, active, nil, []string{"added"}, "test", at, store.WriteOptions{Backend: &inst}); err != nil {
		f.t.Fatalf("seed owning entity %s: %v", id, err)
	}
}

func (f *gateFixture) failIngest(id string, firstFailedAt time.Time) {
	f.t.Helper()
	if err := f.seed.RecordIngestFailure(store.IngestFailure{
		Instance: gateInst, EntityID: id, Seq: 7, StoreIdentity: "s1", Kinds: []string{"status_changed"},
		FirstFailedAt: firstFailedAt.UTC().Format(time.RFC3339), LastError: "hydration failed: boom",
	}); err != nil {
		f.t.Fatalf("record ingest failure: %v", err)
	}
}

// wantGate runs doctor and requires failure naming gate with every fragment in
// the gate's own output line.
func wantGateFailure(t *testing.T, gate, verdict string, fragments ...string) {
	t.Helper()
	stdout, err := runDoctorCmd(t)
	if err == nil || !strings.Contains(err.Error(), gate) {
		t.Fatalf("doctor err = %v, want a failure naming %q\n%s", err, gate, stdout)
	}
	var line string
	for _, l := range strings.Split(stdout, "\n") {
		if strings.Contains(l, gate+": "+verdict) {
			line = l
		}
	}
	if line == "" {
		t.Fatalf("stdout has no %q line:\n%s", gate+": "+verdict, stdout)
	}
	for _, frag := range fragments {
		if !strings.Contains(line, frag) {
			t.Errorf("gate line lacks %q:\n%s", frag, line)
		}
	}
}

func TestDoctorIssueGatesCleanConfigurationPasses(t *testing.T) {
	f := newGateFixture(t)
	f.seedOwningEntity("J-1", true)
	// A cursor_expired record older than 24 hours and a young failed ingest
	// both pass.
	if err := f.seed.SetMeta(changes.MetaFeedCursorExpiredPrefix+gateInst, doctorNow.Add(-25*time.Hour).Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	f.failIngest("J-9", doctorNow.Add(-5*time.Minute))
	stdout, err := runDoctorCmd(t)
	if err != nil {
		t.Fatalf("doctor on a clean owning configuration: %v\n%s", err, stdout)
	}
	for _, gate := range []string{gateNameDispatch, gateNameTwoBackends, gateNameNoWatchedQuery, gateNameCursorExpired, gateNameFailedIngestAge, gateNameBaselineOpen} {
		if !strings.Contains(stdout, gate+": ok") {
			t.Errorf("stdout lacks %q:\n%s", gate+": ok", stdout)
		}
	}
	if !strings.Contains(stdout, "issue change-flow gates:") {
		t.Errorf("stdout lacks the gates heading:\n%s", stdout)
	}
}

func TestDoctorIssueGatesDispatchMismatchesNameInstanceAndQuery(t *testing.T) {
	cases := []struct {
		name  string
		caps  string
		exit  int
		frags []string
	}{
		{
			"owns_changes differs", `{"sources":[],"backends":[{"backend":"owning-demo","vocabulary":{"owns_changes":false},` +
				`"queries":{"q-mine":{"watched":true},"q-team":{"watched":true}}}]}`, 0,
			[]string{"owns_changes_differs", gateInst},
		},
		{
			"query missing", `{"sources":[],"backends":[{"backend":"owning-demo","vocabulary":{"owns_changes":true},` +
				`"queries":{"q-mine":{"watched":true}}}]}`, 0,
			[]string{"query_missing", gateInst, "q-team"},
		},
		{
			"watched differs", `{"sources":[],"backends":[{"backend":"owning-demo","vocabulary":{"owns_changes":true},` +
				`"queries":{"q-mine":{"watched":true},"q-team":{"watched":false}}}]}`, 0,
			[]string{"watched_differs", gateInst, "q-team"},
		},
		{
			"instance unanswered", `{"error":{"code":"boom","message":"capabilities exploded"}}`, 1,
			[]string{"instance_unanswered", gateInst},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newGateFixture(t)
			f.caps(tc.caps, tc.exit)
			wantGateFailure(t, gateNameDispatch, "FAIL", tc.frags...)
		})
	}
}

func TestDoctorIssueGatesAnIDServedByTwoBackendsFails(t *testing.T) {
	f := newGateFixture(t)
	// Two non-owning queries of two different instances hold the same id, and
	// the entity row has no backend column yet.
	for _, q := range []string{"bd-a", "bd-b"} {
		if err := f.seed.SetMeta("change_flow.watchset.issue."+q, `["pg2-77"]`); err != nil {
			t.Fatal(err)
		}
	}
	at := doctorNow.Format(time.RFC3339)
	if _, err := f.seed.WriteEntityStateWithLog(store.Entity{Repo: "o/r", EntityType: "issue", EntityID: "pg2-77", Facts: `{}`, AsOf: at}, 0, at, true, []string{"added"}, "test", at); err != nil {
		t.Fatal(err)
	}
	wantGateFailure(t, gateNameTwoBackends, "FAIL", "pg2-77", "beads-a", "beads-b")
}

func TestDoctorIssueGatesBackendWithActiveEntitiesAndNoWatchedQueryFails(t *testing.T) {
	f := newGateFixture(t)
	f.seedOwningEntity("J-1", true)
	f.caps(`{"sources":[],"backends":[{"backend":"owning-demo","vocabulary":{"owns_changes":true},`+
		`"queries":{"q-mine":{"watched":false},"q-team":{"watched":false}}}]}`, 0)
	wantGateFailure(t, gateNameNoWatchedQuery, "FAIL", gateInst, "no watched query")
}

func TestDoctorIssueGatesNoWatchedQueryIgnoresBackendsWithoutActiveEntities(t *testing.T) {
	f := newGateFixture(t)
	// The instance reports no watched query but holds no active issue entity;
	// a beads entity (another backend) never trips the gate either.
	f.caps(`{"sources":[],"backends":[{"backend":"owning-demo","vocabulary":{"owns_changes":true},`+
		`"queries":{"q-mine":{"watched":false},"q-team":{"watched":false}}}]}`, 0)
	f.seedOwningEntity("J-1", false)
	other := "beads-a"
	at := doctorNow.Format(time.RFC3339)
	if _, err := f.seed.WriteEntityStateWithLogOpts(store.Entity{Repo: "o/r", EntityType: "issue", EntityID: "pg2-1", Facts: `{}`, AsOf: at},
		0, at, true, nil, []string{"added"}, "test", at, store.WriteOptions{Backend: &other}); err != nil {
		t.Fatal(err)
	}
	stdout, _ := runDoctorCmd(t)
	if !strings.Contains(stdout, gateNameNoWatchedQuery+": ok") {
		t.Errorf("the gate tripped without an active entity of the owning instance:\n%s", stdout)
	}
}

func TestDoctorIssueGatesCursorExpiredFailsOnlyWhileYoungerThanADay(t *testing.T) {
	f := newGateFixture(t)
	key := changes.MetaFeedCursorExpiredPrefix + gateInst
	if err := f.seed.SetMeta(key, doctorNow.Add(-23*time.Hour).Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	wantGateFailure(t, gateNameCursorExpired, "FAIL", gateInst, "dropped-key runbook")

	// A record of 24 hours or more passes, and the key is never deleted.
	if err := f.seed.SetMeta(key, doctorNow.Add(-24*time.Hour).Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	stdout, err := runDoctorCmd(t)
	if err != nil {
		t.Fatalf("an expiry older than a day must pass: %v\n%s", err, stdout)
	}
	if _, found, _ := f.seed.GetMeta(key); !found {
		t.Error("doctor deleted the cursor_expired record")
	}
}

func TestDoctorIssueGatesFailedIngestOlderThanItsBoundFails(t *testing.T) {
	f := newGateFixture(t)
	f.failIngest("J-9", doctorNow.Add(-31*time.Minute)) // default bound is 30m
	wantGateFailure(t, gateNameFailedIngestAge, "FAIL", gateInst, "J-9", "30m0s")

	// The configured bound is the one in effect.
	f.cfg.Hydration.IngestFailedMaxAge = "2h"
	stdout, err := runDoctorCmd(t)
	if err != nil {
		t.Fatalf("an entry younger than the configured 2h bound must pass: %v\n%s", err, stdout)
	}
}

func TestDoctorIssueGatesBaselineOpenMoreThanADayFailsNamingQueryAndMembers(t *testing.T) {
	f := newGateFixture(t)
	f.health(`{"sources":[],"backends":[{"backend":"owning-demo","status":{"queries":[`+
		`{"name":"q-mine","baseline_at":"2026-10-01T00:00:00Z"},{"name":"q-team","baseline_open_age_seconds":90000}]}}]}`, 0)
	f.failIngest("J-9", doctorNow.Add(-time.Minute))
	wantGateFailure(t, gateNameBaselineOpen, "FAIL", gateInst, "q-team", "90000", "J-9")
}

func TestDoctorIssueGatesBaselineOpenUnderADayPasses(t *testing.T) {
	f := newGateFixture(t)
	f.health(`{"sources":[],"backends":[{"backend":"owning-demo","status":{"queries":[`+
		`{"name":"q-team","baseline_open_age_seconds":3600}]}}]}`, 0)
	stdout, err := runDoctorCmd(t)
	if err != nil {
		t.Fatalf("a baseline open for 3600s must pass: %v\n%s", err, stdout)
	}
	// The age is read through `issue health`, never the feed wire.
	calls, _ := os.ReadFile(filepath.Join(f.dir, "calls.log"))
	if !strings.Contains(string(calls), "issue health --backend owning-demo") || strings.Contains(string(calls), "changes") {
		t.Errorf("calls:\n%s", calls)
	}
}

func TestDoctorIssueGatesUnavailableDaemonIsUnreachableNotAPass(t *testing.T) {
	f := newGateFixture(t)
	f.caps(gateUnavailable, 1)
	f.health(gateUnavailable, 1)
	stdout, err := runDoctorCmd(t)
	if err == nil {
		t.Fatalf("doctor passed with the daemon unavailable:\n%s", stdout)
	}
	for _, gate := range []string{gateNameDispatch, gateNameNoWatchedQuery, gateNameBaselineOpen} {
		if !strings.Contains(stdout, gate+": UNREACHABLE (") {
			t.Errorf("stdout lacks %q:\n%s", gate+": UNREACHABLE", stdout)
		}
		if !strings.Contains(err.Error(), gate) {
			t.Errorf("failure list %q lacks %q", err, gate)
		}
	}
	if strings.Contains(stdout, "instance_unanswered") {
		t.Errorf("an unavailable daemon was reported as a mismatch, not as unreachable:\n%s", stdout)
	}
}

func TestDoctorIssueGatesWithoutOwningBackendReadsOnlyCapabilitiesOfTheBeadsBackend(t *testing.T) {
	f := newGateFixture(t)
	f.cfg.Watch.Issue.Queries = []string{"bd-a"}
	f.cfg.Watch.Issue.Backends = map[string]config.WatchBackend{"bd-a": {Instance: "beads-a"}}
	stdout, err := runDoctorCmd(t)
	if err != nil {
		t.Fatalf("doctor: %v\n%s", err, stdout)
	}
	calls, _ := os.ReadFile(filepath.Join(f.dir, "calls.log"))
	if !strings.Contains(string(calls), "capabilities --backend beads-a") {
		t.Errorf("the beads backend's capabilities were not read:\n%s", calls)
	}
	if strings.Contains(string(calls), "health") || strings.Contains(string(calls), "owning-demo") {
		t.Errorf("a beads-only configuration asked an owning backend:\n%s", calls)
	}
}

// The first `issue changes` poll of a process runs the dispatch check through
// the real command: the mismatch lands as a warning line and in the meta key.
func TestChangesFirstPollLogsDispatchMismatchAndRecordsIt(t *testing.T) {
	origReader := changesCapabilityReader
	t.Cleanup(func() { changesCapabilityReader = origReader })
	changesCapabilityReader = func(g *gather.Gatherer) changes.Capabilityer { return g }
	changes.ResetStartupDispatchCheck()
	t.Cleanup(changes.ResetStartupDispatchCheck)

	f := owningFeedFixture(t)
	feedConnector(t, f, oneRowFeed)
	f.showIssue("J-1", "2026-09-30T00:00:00Z")
	// feedConnector's script has no capabilities verb: answer it with a
	// backend that reports the owning query unwatched.
	f.write("caps.json", `{"sources":[],"backends":[{"backend":"owning-demo","vocabulary":{"owns_changes":true},"queries":{"jira-q":{"watched":false}}}]}`)
	installFakePGConnector(t, fmt.Sprintf(`D=%q
echo "$*" >> "$D/calls.log"
v="$2"
if [ "$v" = capabilities ]; then cat "$D/caps.json"; exit 0; fi
if [ "$v" = changes ]; then cat "$D/feed.json"; exit 0; fi
if [ "$v" = changes-ack ]; then echo '{"consumer":"pg-desk","acked_seq":6}'; exit 0; fi
if [ "$v" = health ]; then echo '{"sources":[],"backends":[{"backend":"owning-demo","status":{"queries":[]}}]}'; exit 0; fi
if [ "$v" = list ]; then echo '{"entities":[],"present_ids":[],"truncated":false}'; exit 0; fi
if [ "$v" = show ]; then cat "$D/show-$3.json"; exit 0; fi
exit 99`, f.dir))

	_, stderr, rerr := runChangesCmd(t, "issue", "--consumer", "router", "--json")
	if rerr != nil {
		t.Fatalf("changes: %v", rerr)
	}
	if !strings.Contains(stderr, "dispatch check: watched_differs: backend owning-demo query jira-q") {
		t.Errorf("stderr lacks the mismatch warning:\n%s", stderr)
	}
	v, found, merr := f.seed.GetMeta(changes.MetaDispatchCheckPrefix + feedTestInstance)
	if merr != nil || !found || !strings.Contains(v, "watched_differs") || !strings.Contains(v, `"at":"2026-10-01T12:00:00Z"`) {
		t.Errorf("meta = %q %v %v, want the recorded mismatch", v, found, merr)
	}
	// A second poll of the same process does not check again.
	_, stderr, rerr = runChangesCmd(t, "issue", "--consumer", "router", "--json")
	if rerr != nil || strings.Contains(stderr, "dispatch check") {
		t.Errorf("second poll: err = %v, stderr = %q", rerr, stderr)
	}
}
