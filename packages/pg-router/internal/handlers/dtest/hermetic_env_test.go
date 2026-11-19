package dtest

import (
	"strings"
	"testing"
)

// HermeticBDEnv must hand the real bd a private world: a git hook's GIT_DIR /
// GIT_WORK_TREE / GIT_INDEX_FILE (which point at the OUTER repository) and any
// workspace binding must not reach it, or `bd init` in the throwaway directory
// resolves the outer repo's beads workspace and refuses with "already
// initialized". Unrelated variables (PATH) must still arrive.
func TestHermeticBDEnv_DropsGitAndWorkspaceBindings(t *testing.T) {
	leaks := map[string]string{
		"GIT_DIR":                "/outer/repo/.git",
		"GIT_WORK_TREE":          "/outer/repo",
		"GIT_INDEX_FILE":         "/outer/repo/.git/index",
		"GIT_COMMON_DIR":         "/outer/repo/.git",
		"BEADS_DIR":              "/outer/repo/.beads",
		"BEADS_DOLT_SERVER_PORT": "25252",
		"BD_ACTOR":               "outer",
		"WORKSPACE_ROOT":         "/outer",
	}
	for k, v := range leaks {
		t.Setenv(k, v)
	}
	t.Setenv("PATH", "/usr/bin:/bin")

	have := map[string]string{}
	for _, kv := range HermeticBDEnv() {
		k, v, _ := strings.Cut(kv, "=")
		have[k] = v
	}

	for k := range leaks {
		if v, leaked := have[k]; leaked {
			t.Errorf("%s=%s leaked into the hermetic bd environment", k, v)
		}
	}
	for k := range have {
		if strings.HasPrefix(k, "GIT_") {
			t.Errorf("unexpected %s in the hermetic bd environment", k)
		}
	}
	if have["BEADS_DOLT_AUTO_START"] != "0" {
		t.Errorf("BEADS_DOLT_AUTO_START = %q, want it pinned to 0", have["BEADS_DOLT_AUTO_START"])
	}
	if have["PATH"] != "/usr/bin:/bin" {
		t.Errorf("PATH = %q, want the unrelated variable kept", have["PATH"])
	}
}
