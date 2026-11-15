//go:build integration

package main

// Slow shim scenarios (V-4 integration tier): each waits real seconds for a
// timed lease or a watched process. Run by the pg-keep-awake-shim-integration-tests
// check; the unit gate never builds this file.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-keep-awake/internal/lease"
)

func TestTimedLeaseIsWrittenExpiresAndIsRemoved(t *testing.T) {
	requireDarwin(t)
	xdg := t.TempDir()
	before := time.Now()
	p := startShim(t, xdg, "-t", "3", "-i")
	l := waitForLease(t, xdg, p)
	seen := time.Now()

	if l.Owner != "shim" || l.Kind != "shim" || l.SchemaVersion != 1 {
		t.Errorf("owner/kind/schema = %q/%q/%d", l.Owner, l.Kind, l.SchemaVersion)
	}
	pid := p.cmd.Process.Pid
	ref, err := lease.Ref(pid)
	if err != nil {
		t.Fatal(err)
	}
	if want := fmt.Sprintf("%d-%d", pid, ref.StartUS); l.Key != want || l.ID != "shim."+want {
		t.Errorf("key/id = %q/%q, want %q", l.Key, l.ID, want)
	}
	if l.Client == nil || *l.Client != ref {
		t.Errorf("client = %+v, want %+v", l.Client, ref)
	}
	if l.Watch != nil {
		t.Errorf("watch = %+v, want nil", l.Watch)
	}
	if got := strings.Join(l.Flags, ""); got != "i" {
		t.Errorf("flags = %q", got)
	}
	if len(l.Argv) != 4 || l.Argv[0] != "caffeinate" || l.Argv[1] != "-t" {
		t.Errorf("argv = %q", l.Argv)
	}
	if l.ExpiresAt == nil {
		t.Fatal("expires_at is nil for -t 3")
	}
	if got := l.ExpiresAt.Sub(l.CreatedAt); got != 3*time.Second {
		t.Errorf("expires_at - created_at = %v, want exactly 3s", got)
	}
	// Wall clock: expires_at is now + N, taken between the test's two clocks.
	if l.ExpiresAt.Before(before.Add(3*time.Second)) || l.ExpiresAt.After(seen.Add(3*time.Second)) {
		t.Errorf("expires_at %v not within [%v, %v]", l.ExpiresAt, before.Add(3*time.Second), seen.Add(3*time.Second))
	}

	if ws := p.wait(t, 10*time.Second); !exitedWith(ws, 0) {
		t.Fatalf("timed shim: %s, stderr %q", describe(ws), p.stderr.String())
	}
	if elapsed := time.Since(before); elapsed < 2500*time.Millisecond {
		t.Errorf("exited after %v, want about 3s", elapsed)
	}
	noLeases(t, xdg)
}

func TestWatchRecordsPidAndEndsWhenProcessExits(t *testing.T) {
	requireDarwin(t)
	cases := []struct {
		name   string
		args   []string // {HELPER} stands for the helper's pid
		helper string   // seconds the helper sleeps
		within time.Duration
	}{
		{"-w", []string{"-w", "{HELPER}"}, "2", 8 * time.Second},
		{"-t and -w, watch fires first", []string{"-t", "30", "-w", "{HELPER}"}, "2", 8 * time.Second},
		{"-t and -w, timer fires first", []string{"-t", "1", "-w", "{HELPER}"}, "60", 5 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			xdg := t.TempDir()
			helper := start(t, exec.CommandContext(context.Background(), "sleep", tc.helper))
			hpid := helper.cmd.Process.Pid
			href, err := lease.Ref(hpid)
			if err != nil {
				t.Fatal(err)
			}
			var args []string
			for _, a := range tc.args {
				args = append(args, strings.ReplaceAll(a, "{HELPER}", fmt.Sprint(hpid)))
			}
			began := time.Now()
			p := startShim(t, xdg, args...)
			l := waitForLease(t, xdg, p)
			if l.Watch == nil || *l.Watch != href {
				t.Errorf("watch = %+v, want %+v", l.Watch, href)
			}
			if ws := p.wait(t, tc.within); !exitedWith(ws, 0) {
				t.Fatalf("shim: %s, stderr %q", describe(ws), p.stderr.String())
			}
			if tc.helper == "60" && helper.running() == false {
				t.Error("the timer case must end while the watched process still runs")
			}
			if tc.helper == "2" && time.Since(began) > 6*time.Second {
				t.Errorf("took %v to notice the watched process exit", time.Since(began))
			}
			noLeases(t, xdg)
		})
	}
}

func TestUtilityFormIgnoresTimeoutAndWatch(t *testing.T) {
	requireDarwin(t)
	xdg := t.TempDir()
	// The lease is bound to the command, so -t 1 must not cut it short.
	p := startShim(t, xdg, "-t", "1", "sh", "-c", "sleep 2; echo finished")
	waitForLease(t, xdg, p)
	if ws := p.wait(t, 10*time.Second); !exitedWith(ws, 0) || p.stdout.String() != "finished\n" {
		t.Errorf("%s, stdout %q", describe(ws), p.stdout.String())
	}
}

func TestShimNeedsNoKeeperAndWritesNoKeeperState(t *testing.T) {
	requireDarwin(t)
	xdg := t.TempDir()
	p := startShim(t, xdg, "-t", "1")
	waitForLease(t, xdg, p)
	if ws := p.wait(t, 10*time.Second); !exitedWith(ws, 0) {
		t.Fatalf("%s", describe(ws))
	}
	for _, name := range []string{lease.FileStatus, lease.FileAssertion, lease.FilePause} {
		if _, err := os.Stat(filepath.Join(xdg, lease.DirName, name)); err == nil {
			t.Errorf("the shim wrote keeper state %s", name)
		}
	}
}
