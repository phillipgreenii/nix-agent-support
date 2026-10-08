package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
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

// reachFixtureFanOut lists one anchor and, when withChild, one open review
// request for the same PR.
func reachFixtureFanOut(withChild bool) string {
	anchor := `{"id":"bd-1","title":"o/r#5: t","state":"open","metadata":{"repo":"o/r","pr_number":"5"}}`
	if !withChild {
		return `{"entities":[` + anchor + `]}`
	}
	return `{"entities":[` + anchor + `,{"id":"bd-1.2","title":"review-pr: o/r#5","state":"open","parent":"bd-1","metadata":{"repo":"o/r","pr_number":"5"}}]}`
}

func seedChildLedgerRow(t *testing.T, seed *store.Store) {
	t.Helper()
	if err := seed.UpsertLedger(store.LedgerEntry{
		Repo: "o/r", EntityType: entityTypePR, EntityID: "o/r#5",
		Kind: "review-request", BeadID: "bd-1.2",
	}); err != nil {
		t.Fatalf("seed ledger: %v", err)
	}
}

// pg2-6w396: a work-beads listing that carries anchors but no child, while
// the ledger holds a child row, means the deployed query excludes type task.
// doctor warns and still exits 0 (an observability line, not a gate).
func TestDoctorWorkBeadsReachWarnsWhenListingHasNoChildren(t *testing.T) {
	seed, openFresh := openTestStore(t)
	withOpenSeams(t, openTestConfig("o/r"), openFresh)
	stubDoctorSeams(t, nil, nil, nil)
	doctorFanOutIssueList = func(ctx context.Context, cfg *config.Config) ([]byte, error) {
		return []byte(reachFixtureFanOut(false)), nil
	}
	seedChildLedgerRow(t, seed)

	stdout, err := runDoctorCmd(t)
	if err != nil {
		t.Fatalf("doctor: %v, want a warning only", err)
	}
	if !strings.Contains(stdout, "work-beads reach: WARN (1 anchors") || !strings.Contains(stdout, "ledger holds 1 child rows") {
		t.Errorf("stdout does not warn about the unreachable children: %s", stdout)
	}
}

func TestDoctorWorkBeadsReachOkWhenListingHasChildren(t *testing.T) {
	seed, openFresh := openTestStore(t)
	withOpenSeams(t, openTestConfig("o/r"), openFresh)
	stubDoctorSeams(t, nil, nil, nil)
	doctorFanOutIssueList = func(ctx context.Context, cfg *config.Config) ([]byte, error) {
		return []byte(reachFixtureFanOut(true)), nil
	}
	seedChildLedgerRow(t, seed)

	stdout, err := runDoctorCmd(t)
	if err != nil {
		t.Fatalf("doctor: %v", err)
	}
	if !strings.Contains(stdout, "work-beads reach: ok (1 anchors, 1 child beads listed)") {
		t.Errorf("stdout does not report the reachable children: %s", stdout)
	}
}

// With no child row in the ledger there is nothing to contradict the
// listing, so an anchor-only listing is not a warning.
func TestDoctorWorkBeadsReachOkWhenLedgerHasNoChildren(t *testing.T) {
	_, openFresh := openTestStore(t)
	withOpenSeams(t, openTestConfig("o/r"), openFresh)
	stubDoctorSeams(t, nil, nil, nil)
	doctorFanOutIssueList = func(ctx context.Context, cfg *config.Config) ([]byte, error) {
		return []byte(reachFixtureFanOut(false)), nil
	}

	stdout, err := runDoctorCmd(t)
	if err != nil {
		t.Fatalf("doctor: %v", err)
	}
	if !strings.Contains(stdout, "work-beads reach: ok (1 anchors, 0 child beads listed)") {
		t.Errorf("stdout: %s", stdout)
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
