//go:build integration

package gate_test

// Guards for hermeticEnviron / goBuildEnviron (gatetest_helpers_test.go), bead
// pg2-rxs4j (tagged integration because the foreign-env guard re-runs
// create_fleetrace_test.go's round trip): no name that selects a git repository or a beads workspace may
// reach a real bd/git/pn/pb child, whatever the parent process exports.

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/x/gitfixture"
	"github.com/phillipgreenii/x/gittest"
)

// hookLeakEnv is a git-hook-shaped environment plus every workspace binding a
// test process could have inherited, all pointing at "another repository".
var hookLeakEnv = []string{
	"GIT_DIR=/outer/repo/.git",
	"GIT_WORK_TREE=/outer/repo",
	"GIT_INDEX_FILE=/outer/repo/.git/index",
	"GIT_COMMON_DIR=/outer/repo/.git",
	"GIT_OBJECT_DIRECTORY=/outer/repo/.git/objects",
	"GIT_PREFIX=packages/pb/",
	"GIT_CEILING_DIRECTORIES=/outer",
	"GIT_ANYTHING_FUTURE=1",
	"BEADS_DIR=/outer/repo/.beads",
	"BEADS_DOLT_SERVER_PORT=25252",
	"BD_ACTOR=outer",
	"WORKSPACE_ROOT=/outer",
	"ZR_MACHINE_SUPPORT_WORKSPACE_ROOT=/outer",
}

// allowedGitNames are the GIT_* names a child legitimately receives: the
// deterministic identity isolateBeadsEnv sets, and the system-config pin.
var allowedGitNames = map[string]bool{
	"GIT_AUTHOR_NAME": true, "GIT_AUTHOR_EMAIL": true,
	"GIT_COMMITTER_NAME": true, "GIT_COMMITTER_EMAIL": true,
	"GIT_CONFIG_NOSYSTEM": true,
}

func envMap(t *testing.T, env []string) map[string]string {
	t.Helper()
	have := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		if _, dup := have[k]; dup {
			t.Errorf("%s appears twice in the child environment", k)
		}
		have[k] = v
	}
	return have
}

// TestHermeticEnvFrom_DropsEverythingOffTheAllowlist is the unit-tier guard: a
// git-hook environment (and any other workspace binding) in the parent MUST NOT
// reach the child, and the names the child does need MUST still arrive.
// Reintroducing a deny-list or a plain pass-through of os.Environ fails it at
// the first GIT_* name.
func TestHermeticEnvFrom_DropsEverythingOffTheAllowlist(t *testing.T) {
	parent := append([]string{
		"PATH=/usr/bin", "HOME=/iso/home", "TMPDIR=/tmp/x", "BD_JSON_ENVELOPE=1",
		"GIT_AUTHOR_NAME=t", "PN_WORKSPACE_ROOT=/ws", "NIX_REMOTE=daemon",
		"LC_ALL=C", "SOMETHING_ELSE=1", "GIT_CONFIG_NOSYSTEM=0", "BEADS_DOLT_AUTO_START=1",
	}, hookLeakEnv...)
	have := envMap(t, hermeticEnvFrom(parent))
	for _, kv := range hookLeakEnv {
		k, _, _ := strings.Cut(kv, "=")
		if v, leaked := have[k]; leaked {
			t.Errorf("%s=%s leaked into the child environment", k, v)
		}
	}
	for k := range have {
		if strings.HasPrefix(k, "GIT_") && !allowedGitNames[k] {
			t.Errorf("unexpected GIT_ variable %s in the child environment", k)
		}
	}
	if _, leaked := have["SOMETHING_ELSE"]; leaked {
		t.Error("a name off the allowlist leaked")
	}
	want := map[string]string{
		"PATH": "/usr/bin", "HOME": "/iso/home", "TMPDIR": "/tmp/x", "BD_JSON_ENVELOPE": "1",
		"GIT_AUTHOR_NAME": "t", "PN_WORKSPACE_ROOT": "/ws", "NIX_REMOTE": "daemon",
		"LC_ALL": "C", "GIT_CONFIG_NOSYSTEM": "1", "BEADS_DOLT_AUTO_START": "0",
	}
	for k, v := range want {
		if have[k] != v {
			t.Errorf("%s = %q, want %q", k, have[k], v)
		}
	}
}

// TestHermeticEnviron_DropsAHookEnvironmentOfTheProcess drives the exported
// helper the tests actually call (not just hermeticEnvFrom) with a hook-shaped
// environment in the real process environment. The old helper removed six
// enumerated GIT_* names; it fails here on every other name (GIT_CEILING_DIRECTORIES,
// GIT_ANYTHING_FUTURE, BD_ACTOR, ...), which is the point of an allowlist.
func TestHermeticEnviron_DropsAHookEnvironmentOfTheProcess(t *testing.T) {
	for _, kv := range hookLeakEnv {
		k, v, _ := strings.Cut(kv, "=")
		t.Setenv(k, v)
	}
	t.Setenv("SOMETHING_ELSE", "1")
	have := envMap(t, hermeticEnviron())
	for _, kv := range hookLeakEnv {
		k, _, _ := strings.Cut(kv, "=")
		if v, leaked := have[k]; leaked {
			t.Errorf("%s=%s leaked into the child environment", k, v)
		}
	}
	if _, leaked := have["SOMETHING_ELSE"]; leaked {
		t.Error("a name off the allowlist leaked")
	}
	if have["PATH"] == "" {
		t.Error("PATH must still reach the child")
	}
}

// TestGoBuildEnviron_CarriesToolchainButNoGit pins the one place the parent's
// own toolchain state is passed through, and that a git selector never is.
func TestGoBuildEnviron_CarriesToolchainButNoGit(t *testing.T) {
	t.Setenv("GOFLAGS", "-mod=mod")
	t.Setenv("NOT_GO", "1")
	for _, kv := range hookLeakEnv {
		k, v, _ := strings.Cut(kv, "=")
		t.Setenv(k, v)
	}
	have := envMap(t, goBuildEnviron())
	if have["GOFLAGS"] != "-mod=mod" || have["PATH"] == "" {
		t.Errorf("go build env lost a toolchain variable: %v", have)
	}
	for k := range have {
		if (strings.HasPrefix(k, "GIT_") && !allowedGitNames[k]) || (strings.HasPrefix(k, "BEADS_") && k != "BEADS_DOLT_AUTO_START") || k == "NOT_GO" || k == "WORKSPACE_ROOT" {
			t.Errorf("%s leaked into the go build environment", k)
		}
	}
}

// foreignRepoWithBeads is another repository with a beads workspace in it.
func foreignRepoWithBeads(t *testing.T) string {
	t.Helper()
	requireBinaries(t, "bd", "git")
	isolateBeadsEnv(t)
	repo := gittest.New(t, gitfixture.RepoOptions{Suite: "pb-gate-foreign"})
	runTool(t, repo.Dir, "bd", "init", "--prefix", "foreign")
	return repo.Dir
}

// foreignGitEnv is what a git hook run inside repo exports.
func foreignGitEnv(repo string) []string {
	return []string{
		"GIT_DIR=" + filepath.Join(repo, ".git"),
		"GIT_WORK_TREE=" + repo,
		"GIT_INDEX_FILE=" + filepath.Join(repo, ".git", "index"),
	}
}

// TestFleetRace_ForeignGitEnv_StillPasses runs the real create/gate/resolve
// round trip with GIT_DIR, GIT_WORK_TREE and GIT_INDEX_FILE exported to ANOTHER
// repository that holds a beads workspace. Restoring any inheritance of those
// names makes the first `bd init` in the throwaway workspace fail here.
func TestFleetRace_ForeignGitEnv_StillPasses(t *testing.T) {
	foreign := foreignRepoWithBeads(t)
	for _, kv := range foreignGitEnv(foreign) {
		k, v, _ := strings.Cut(kv, "=")
		t.Setenv(k, v)
	}
	fleetRaceBeadHeldUntilGateResolves(t)
}

// TestFleetRace_ForeignGitEnv_StopsABdThatInheritsIt is the negative control:
// the SAME environment, handed to a bd that inherits it (hermeticEnvFrom's
// output plus the foreign selectors, i.e. the old deny-list shape), makes
// `bd init` in a fresh directory fail. If bd ever stops following GIT_DIR this
// fails and says so, instead of the guard above passing for no reason.
func TestFleetRace_ForeignGitEnv_StopsABdThatInheritsIt(t *testing.T) {
	foreign := foreignRepoWithBeads(t)
	leaky := append(hermeticEnviron(), foreignGitEnv(foreign)...)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bd", "init", "--prefix", "leak")
	cmd.Dir = t.TempDir()
	cmd.Env = leaky
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("bd init succeeded under a foreign GIT_DIR/GIT_WORK_TREE: the hook environment no longer reaches bd, so the guard above proves nothing (revisit pg2-rxs4j)\n%s", out)
	}
	if !strings.Contains(string(out), "already initialized") {
		t.Fatalf("bd init failed under the foreign git environment, but not with the 'already initialized' refusal pg2-g3204 recorded:\n%s", out)
	}
}
