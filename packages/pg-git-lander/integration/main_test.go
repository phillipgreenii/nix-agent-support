//go:build integration

package integration

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Process-wide state built once in TestMain.
var (
	fixtureRoot      string
	globalConfigPath string
	toolBinary       string
	shapes           = map[Shape]*builtShape{}
)

func TestMain(m *testing.M) { os.Exit(runMain(m)) }

func runMain(m *testing.M) int {
	root, err := os.MkdirTemp("", "pgl-integ-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	defer os.RemoveAll(root)
	if r, err := filepath.EvalSymlinks(root); err == nil {
		root = r
	}
	fixtureRoot = root
	globalConfigPath = filepath.Join(root, "gitconfig")
	if err := os.WriteFile(globalConfigPath, []byte(globalConfigText), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	// The real binary: PG_GIT_LANDER_BIN names a prebuilt one, otherwise it is
	// built from this module.
	toolBinary = os.Getenv("PG_GIT_LANDER_BIN")
	if toolBinary == "" {
		toolBinary = filepath.Join(root, "pg-git-lander")
		cmd := exec.Command("go", "build", "-o", toolBinary, "../cmd/pg-git-lander")
		if out, err := cmd.CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "building pg-git-lander: %v\n%s", err, out)
			return 2
		}
	}
	home := filepath.Join(root, "build-home")
	if err := os.MkdirAll(filepath.Join(home, "tmp"), 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	env := gitEnvBase(home, globalConfigPath)
	for _, s := range AllShapes {
		b, err := buildShape(env, root, s)
		if err != nil {
			fmt.Fprintf(os.Stderr, "building fixture %s: %v\n", s, err)
			return 2
		}
		shapes[s] = b
	}
	return m.Run()
}
