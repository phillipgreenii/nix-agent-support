//go:build integration

package web

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// The client logic under a real `node --test` process is an integration test:
// checks.<system>.pg-task-focus-integration-tests runs it.

// ---- the client logic, under node ----

// requireNode is set by the Nix check, which supplies node; elsewhere a host
// without node skips the logic tests, with the reason, and says so.
const requireNode = "PG_TASK_FOCUS_REQUIRE_NODE"

func nodePath(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("node")
	if err != nil {
		if os.Getenv(requireNode) == "1" {
			t.Fatalf("node is not on PATH and %s=1: the client logic cannot go untested here", requireNode)
		}
		t.Skipf("node is not on PATH; the client logic tests need it (%s=1 makes this a failure)", requireNode)
	}
	return p
}

// NodeUnitTests runs every web/test/*.test.mjs under node's own test runner.
// There are no dependencies: the tests import the page's modules and a small
// fake DOM from the same tree.
func TestClientLogicUnderNode(t *testing.T) {
	node := nodePath(t)
	files, err := filepath.Glob(filepath.Join("test", "*.test.mjs"))
	if err != nil || len(files) < 5 {
		t.Fatalf("the client logic tests are missing: %v %v", files, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, append([]string{"--test"}, files...)...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("node --test failed: %v\n%s", err, out.String())
	}
	if !regexp.MustCompile(`(?m)^ℹ fail 0$`).MatchString(out.String()) {
		t.Fatalf("node reported a failure or no summary:\n%s", out.String())
	}
	t.Log(lastLines(out.String(), 8))
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
