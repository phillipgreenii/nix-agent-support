//go:build integration && unix

package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A real-process proof of ADR 0130's diagnosis: a daemon stop must end a command
// role's payload process group, whatever the payload does with SIGTERM.
//
// The router under test is the real run() entry point (TestHelperRouterProcess),
// the handler is the REAL pg-router-handler-command binary built from this
// module, and the payload is a shell script that records its own pid. The test
// signals only the router child it started; pids it reads from the payload's
// pid files are only probed with signal 0 and, in cleanup, killed as a group
// when they are still alive (a leaked payload sleeps at most payloadSleepSeconds).

const (
	payloadSleepSeconds = 90
	payloadRole         = "repo-verify-like"
	// payloadGraceMargin is added to drain plus the adapter's 5 s payload grace
	// for process-exit scheduling noise on a loaded host.
	payloadGraceMargin = 4 * time.Second
)

func buildCommandAdapter(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "pg-router-handler-command")
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "build", "-o", bin, "../pg-router-handler-command")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build ./cmd/pg-router-handler-command: %v\n%s", err, out)
	}
	return bin
}

func groupGone(pgid int) bool {
	return errors.Is(syscall.Kill(-pgid, 0), syscall.ESRCH)
}

// readPidFile waits for a newline-terminated pid in path.
func readPidFile(t *testing.T, h *reofferHarness, path, msg string) int {
	t.Helper()
	var pid int
	h.waitFor(30*time.Second, msg, func() bool {
		b, err := os.ReadFile(path)
		if err != nil || !strings.HasSuffix(string(b), "\n") {
			return false
		}
		n, err := strconv.Atoi(strings.TrimSpace(string(b)))
		pid = n
		return err == nil && n > 1
	})
	return pid
}

// TestShutdown_PayloadGroupEndsWithinDrainPlusGrace stops a real router while a
// command role's payload runs and asserts the payload's whole process group is
// gone within the drain budget plus the adapter's 5 s payload grace.
func TestShutdown_PayloadGroupEndsWithinDrainPlusGrace(t *testing.T) {
	if _, set := os.LookupEnv(routerSentinelEnv); set {
		t.Fatalf("recursion guard: %s is already set in the test process's own environment", routerSentinelEnv)
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not on PATH")
	}
	adapterBin := buildCommandAdapter(t)

	cases := []struct {
		name string
		// body runs under /bin/sh as the payload leader; $1 is the leader pid file,
		// $2 a descendant pid file. A payload leader is the process group leader.
		body string
	}{
		{
			// The plain case: the leader is the long-running process.
			name: "leader-sleeps",
			body: `echo $$ > "$1.tmp"; mv "$1.tmp" "$1"; exec sleep ` + strconv.Itoa(payloadSleepSeconds),
		},
		{
			// The leader exits on SIGTERM (the shell's default), but a descendant in
			// the same group ignores SIGTERM and holds no inherited descriptor, as a
			// helper that daemonises its output would. Only a group SIGKILL ends it.
			name: "descendant-ignores-term",
			body: `echo $$ > "$1.tmp"; mv "$1.tmp" "$1"; ` +
				`(trap '' TERM; exec sleep ` + strconv.Itoa(payloadSleepSeconds) + ` </dev/null >/dev/null 2>&1) & ` +
				`echo $! > "$2.tmp"; mv "$2.tmp" "$2"; wait`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newReofferHarness(t)
			leaderPid := filepath.Join(h.markers, "leader.pid")
			descPid := filepath.Join(h.markers, "descendant.pid")
			payload := filepath.Join(h.base, "payload.sh")
			if err := os.WriteFile(payload, []byte("#!/bin/sh\n"+tc.body+"\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			cfg := `[[query]]
name = "idle-source"
emits = ["` + reofferEventType + `"]
type = "command"
[query.command]
argv = ["true"]
format = "jsonl"

[[role]]
name = "` + payloadRole + `"
enabled = true
binds = ["` + reofferEventType + `"]
handler = "` + adapterBin + `"
[role.config.command]
argv = ["/bin/sh", "` + payload + `", "` + leaderPid + `", "` + descPid + `"]
`
			if err := os.WriteFile(h.configPath, []byte(cfg), 0o644); err != nil {
				t.Fatal(err)
			}
			r := h.startRouter()
			h.waitCore(r)
			h.inject()
			pgid := readPidFile(t, h, leaderPid, "the payload leader to record its pid\nstderr:\n"+r.stderr.String())
			t.Cleanup(func() { _ = syscall.Kill(-pgid, syscall.SIGKILL) })
			if g, err := syscall.Getpgid(pgid); err != nil || g != pgid {
				t.Fatalf("payload leader pid %d is not its own group leader (pgid %d, err %v)", pgid, g, err)
			}
			if tc.name == "descendant-ignores-term" {
				readPidFile(t, h, descPid, "the payload descendant to record its pid")
			}

			budget := newShutdownBudget(defaultShutdownDeadline)
			limit := budget.Drain + 5*time.Second + payloadGraceMargin
			begun := time.Now()
			if err := r.cmd.Process.Signal(syscall.SIGTERM); err != nil {
				t.Fatal(err)
			}
			for !groupGone(pgid) {
				if time.Since(begun) > limit {
					t.Fatalf("payload process group %d still alive %v after SIGTERM to the router (limit: drain %v + 5s grace + margin)\nrouter stderr:\n%s",
						pgid, time.Since(begun), budget.Drain, r.stderr.String())
				}
				time.Sleep(100 * time.Millisecond)
			}
			select {
			case err := <-r.done:
				r.done <- err
			case <-time.After(40 * time.Second):
				t.Fatalf("router did not exit after the payload group ended\nstderr:\n%s", r.stderr.String())
			}
			if tc.name == "leader-sleeps" && !strings.Contains(r.stderr.String(), "interrupted_by=daemon_shutdown") {
				t.Errorf("router log lacks interrupted_by=daemon_shutdown on the cut-short dispatch\nstderr:\n%s", r.stderr.String())
			}
			t.Logf("payload group gone %v after SIGTERM to the router (drain %v)", time.Since(begun), budget.Drain)
		})
	}
}
