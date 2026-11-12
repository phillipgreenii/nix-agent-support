//go:build integration

package main

// Smoke test of a level-source query against the REAL pg-router binary: the
// router is built, run (run-until-idle) with a config that registers two level
// sources, one whose command prints one valid level answer and one that hangs.
// The answer must reach the durable level record, no event record must exist
// for it (the answer never reaches the queue), and the hung sensor must not
// delay the other: the run ends at the start wait, not at the hung source's
// deadline.

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSmoke_LevelSource_AnswerLandsAsLevelAndNeverAsEvent(t *testing.T) {
	if testing.Short() {
		t.Skip("smoke test builds the router binary")
	}
	bin := buildPgRouterBinary(t)
	repoRoot := t.TempDir()
	logDir := shortDir(t)
	cfgDir := filepath.Join(repoRoot, ".pg-router")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `[levels]
start_wait = "2s"

[[resource]]
name = "disk"
scale = ["PLENTY", "OK", "TIGHT", "EXHAUSTED"]
sensor = "host"

[[resource]]
name = "network"
scale = ["AVAILABLE", "UNAVAILABLE"]
sensor = "slow"
unknown = "worst"

[[query]]
name = "host-probe"
type = "command"
level_source = "host"
[query.command]
argv = ["/bin/sh", "-c", "printf '{\"schemaVersion\":\"1\",\"resource\":\"disk\",\"level\":\"OK\",\"observed_at\":\"%s\",\"valid_for_s\":180,\"owner\":\"host\"}\n' \"$(date -u +%Y-%m-%dT%H:%M:%SZ)\""]
format = "jsonl"

[[query]]
name = "hung-probe"
type = "command"
level_source = "slow"
deadline = "30s"
[query.command]
argv = ["/bin/sh", "-c", "sleep 60"]
format = "jsonl"

[[query]]
name = "work"
type = "command"
emits = ["e"]
[query.command]
argv = ["/usr/bin/true"]
format = "jsonl"

[[role]]
name = "worker"
handler = "/usr/bin/true"
binds = ["e"]
`
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PG_ROUTER_REPO_ROOT", repoRoot)
	t.Setenv("PG_ROUTER_LOG_DIR", logDir)
	t.Setenv("PG_ROUTER_GLOBAL_CONFIG", filepath.Join(t.TempDir(), "no-such-global-config.toml"))

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "run-until-idle")
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	begin := time.Now()
	runErr := cmd.Run()
	elapsed := time.Since(begin)
	if runErr != nil {
		t.Fatalf("run-until-idle: %v\n%s", runErr, out.String())
	}
	if elapsed > 25*time.Second {
		t.Fatalf("run took %s: the hung sensor delayed the run (it has a 30s deadline)\n%s", elapsed, out.String())
	}

	data, err := os.ReadFile(filepath.Join(logDir, "queue.jsonl"))
	if err != nil {
		t.Fatalf("read the durable log: %v\n%s", err, out.String())
	}
	var levelSets, enqueues int
	for _, line := range strings.Split(string(data), "\n") {
		switch {
		case strings.Contains(line, `"op":"level_set"`) && strings.Contains(line, `"resource":"disk"`) && strings.Contains(line, `"level":"OK"`):
			levelSets++
		case strings.Contains(line, `"op":"enqueue"`):
			enqueues++
		}
	}
	if levelSets != 1 {
		t.Fatalf("level_set records for disk = %d, want 1\nlog:\n%s\noutput:\n%s", levelSets, data, out.String())
	}
	if enqueues != 0 {
		t.Fatalf("%d enqueue record(s): a level answer must never reach the queue\nlog:\n%s", enqueues, data)
	}
	fmt.Fprintf(os.Stderr, "smoke: run took %s\n", elapsed)
}
