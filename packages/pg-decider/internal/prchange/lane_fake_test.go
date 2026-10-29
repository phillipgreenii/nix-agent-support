package prchange

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// The fast run of the lane suite (bead pg2-s4xeju): every lane scenario but the
// one that tests the real bd's own query semantics, over the fake bd of
// internal/fakebd, the independent scenarios in parallel. Everything else is as
// real as in the soak (lane_world_test.go): pg-desk, pg-decider, pg-pr-change,
// pg-pr-backfill, the umbrella pg-connector and the beads backend, which reaches
// the fake through bd's own command line. It takes seconds; its nix check,
// pg-decider-lane-fake, is the suite's automatic run, and asserts a PASS line for
// every scenario named in lane-fake-scenarios.txt. Without the binaries the test
// skips, like the other real-tool tests, unless PG_PR_CHANGE_REQUIRE_BINARIES is
// set.

// laneFakeScenariosFile lists, one name per line, every scenario TestLaneFake
// runs. The test fails when its own scenarios and the file differ, and the check
// asserts a PASS line for each name in it.
const laneFakeScenariosFile = "testdata/lane-fake-scenarios.txt"

// minLaneFakeScenarios is the number of scenarios the fast run must run; keep it
// equal to minScenarios of the nix check pg-decider-lane-fake.
const minLaneFakeScenarios = 24

// laneFakeTools is the lane's tools over the fake bd, built for this test.
func laneFakeTools(t *testing.T) e2eTools {
	t.Helper()
	tools := laneTools(t)
	tools.bd = buildFakeBD(t)
	tools.fake = true
	return tools
}

// buildFakeBD builds internal/fakebd's command as `bd` into a directory of its
// own (the directory goes on the PATH of the tools under test).
func buildFakeBD(t *testing.T) string {
	t.Helper()
	goBin, err := exec.LookPath("go")
	if err != nil {
		goBin = filepath.Join(runtime.GOROOT(), "bin", "go")
	}
	out := filepath.Join(t.TempDir(), "bd")
	cmd := exec.Command(goBin, "build", "-o", out, "github.com/phillipgreenii/pg-decider/internal/fakebd/cmd/fake-bd")
	cmd.Env = os.Environ()
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build the fake bd: %v\n%s", err, b)
	}
	return out
}

func TestLaneFake(t *testing.T) {
	l := &laneRun{t: t, tools: laneFakeTools(t), parallel: true}
	adoptionScenarios(l)
	unhydratedScenarios(l)
	failClosedScenarios(l)
	driftScenarios(l)
	reviewScenarios(l)
	failureScenarios(l)
	rollbackScenarios(l)
	l.scenarioList(laneFakeScenariosFile, minLaneFakeScenarios)
}
