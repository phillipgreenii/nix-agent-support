//go:build contract

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
)

// builtBin is the binary under test, built once. CCPOOL_BIN overrides it with an
// already-built (e.g. installed) binary so the suite can test the shipped ccpool.
var (
	builtBin  string
	buildOnce sync.Once
	buildErr  error
)

func TestMain(m *testing.M) {
	// Hard requirements; without them every scenario is meaningless.
	for _, tool := range []string{"tmux", "claude", "sqlite3"} {
		if _, err := exec.LookPath(tool); err != nil {
			fmt.Fprintf(os.Stderr, "contract suite skipped: %q not on PATH\n", tool)
			os.Exit(0) // skip cleanly, not a failure
		}
	}
	os.Exit(m.Run())
}

// ccpoolBin returns the path to the binary under test, building it once.
func ccpoolBin(t *testing.T) string {
	t.Helper()
	if env := os.Getenv("CCPOOL_BIN"); env != "" {
		return env
	}
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "ccpool-contract-bin-")
		if err != nil {
			buildErr = err
			return
		}
		bin := filepath.Join(dir, "ccpool")
		if out, e := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); e != nil {
			buildErr = fmt.Errorf("build ccpool: %v\n%s", e, out)
			return
		}
		builtBin = bin
	})
	if buildErr != nil {
		t.Fatalf("%v", buildErr)
	}
	return builtBin
}

func TestContract_GuardAndBuild(t *testing.T) {
	bin := ccpoolBin(t)
	out, err := exec.Command(bin, "version").CombinedOutput()
	if err != nil {
		t.Fatalf("ccpool version: %v\n%s", err, out)
	}
	t.Logf("binary under test: %s (version %s)", bin, out)
}
