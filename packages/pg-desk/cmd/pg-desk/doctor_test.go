package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

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
