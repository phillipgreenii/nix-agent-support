//go:build integration

package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Integration tier: the daemon verb runs as a re-executed child of this test
// binary (see runVerbChildIfRequested), a whole process, so it is not a unit
// test. Run through checks.<system>.pg-desk-integration-tests.

// start launches the verb as its own process (a re-execution of this test
// binary, see runVerbChildIfRequested). Only the daemon verb still runs this
// way: its SIGTERM-to-clean-exit contract belongs to a real process.
func (f *verbFixture) start(ctx context.Context, verb string, args []string) (*exec.Cmd, *bytes.Buffer, *bytes.Buffer) {
	full := append(strings.Fields(verb), args...)
	cmd := exec.CommandContext(ctx, os.Args[0])
	cmd.Env = append(append([]string{}, f.env...), verbChildArgsEnv+"="+strings.Join(full, argSep))
	cmd.Stdin = strings.NewReader("")
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	return cmd, &out, &errb
}

// runDaemon starts a long-running verb, waits for its log to say it is
// listening, stops it with SIGTERM and returns its result.
func (f *verbFixture) runDaemon(verb string, args []string) verbResult {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd, out, errb := f.start(ctx, verb, args)
	if err := cmd.Start(); err != nil {
		return verbResult{err: err}
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	deadline := time.After(20 * time.Second)
	listening := false
	for !listening {
		select {
		case err := <-done:
			res := verbResult{stdout: out.String(), stderr: errb.String()}
			if ee, ok := err.(*exec.ExitError); ok {
				res.exit = ee.ExitCode()
			}
			res.err = fmt.Errorf("daemon exited before it listened: %v", err)
			return res
		case <-deadline:
			_ = cmd.Process.Kill()
			<-done
			return verbResult{stdout: out.String(), stderr: errb.String(), err: fmt.Errorf("daemon did not listen within 20s")}
		case <-time.After(50 * time.Millisecond):
			if b, err := os.ReadFile(f.serveLog); err == nil && strings.Contains(string(b), "listening") {
				listening = true
			}
		}
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
	err := <-done
	res := verbResult{stdout: out.String(), stderr: errb.String()}
	if ee, ok := err.(*exec.ExitError); ok {
		res.exit = ee.ExitCode()
	}
	return res
}

// TestDaemonVerbsRunAndStopCleanlyOnASchema2Store starts each safe daemon
// verb (serve) as its own process on the migrated fixture store, waits for it
// to listen, stops it with SIGTERM and requires a clean exit and no schema or
// ledger error.
func TestDaemonVerbsRunAndStopCleanlyOnASchema2Store(t *testing.T) {
	classes := verbClasses()
	ran := 0
	for _, verb := range safeVerbList(classes) {
		c := classes[verb]
		if !c.Daemon {
			continue
		}
		ran++
		t.Run(strings.ReplaceAll(verb, " ", "_"), func(t *testing.T) {
			f := newVerbFixture(t)
			res := f.runDaemon(verb, c.Args)
			t.Logf("%s %v: exit=%d\nstderr: %s", verb, c.Args, res.exit, strings.TrimSpace(res.stderr))
			if res.err != nil {
				t.Fatalf("%s: %v\nstderr: %s", verb, res.err, res.stderr)
			}
			if strings.Contains(res.stderr, "panic:") || strings.Contains(res.stderr, "goroutine ") {
				t.Fatalf("%s crashed:\n%s", verb, res.stderr)
			}
			if m := schemaOrLedgerError(res); m != "" {
				t.Fatalf("%s hit a schema or ledger error on a schema-2 store (%q):\nstdout: %s\nstderr: %s", verb, m, res.stdout, res.stderr)
			}
			if res.exit != 0 {
				t.Fatalf("%s did not stop cleanly on SIGTERM: exit %d\n%s", verb, res.exit, res.stderr)
			}
		})
	}
	if ran == 0 {
		t.Fatal("no daemon verb is classified safe; the test would prove nothing")
	}
}
