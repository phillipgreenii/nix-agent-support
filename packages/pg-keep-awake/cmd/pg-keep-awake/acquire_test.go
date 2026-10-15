package main

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-keep-awake/internal/lease"
)

func TestAcquireForCreatesPureTimerLease(t *testing.T) {
	sd := useStateDir(t)
	before := time.Now()
	code, stdout, stderr := runCLI(t, "acquire", "--owner", "t", "--for", "30s")
	if code != 0 {
		t.Fatalf("exit = %d, stderr %q", code, stderr)
	}
	if stdout != "t.default\n" {
		t.Fatalf("stdout = %q, want the lease id", stdout)
	}
	fi, err := os.Stat(leasePath(sd, "t.default"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("lease mode = %v, want 0600", fi.Mode().Perm())
	}
	if di, _ := os.Stat(sd); di.Mode().Perm() != 0o700 {
		t.Errorf("state dir mode = %v, want 0700", di.Mode().Perm())
	}
	l := readLeaseFile(t, sd, "t.default")
	if l.Kind != lease.KindAcquire || l.Owner != "t" || l.Key != "default" || l.ID != "t.default" {
		t.Errorf("lease identity = %+v", l)
	}
	if l.Client != nil {
		t.Errorf("--for alone must make a pure timer with no client, got %+v", l.Client)
	}
	if l.Watch != nil {
		t.Errorf("acquire has no watch, got %+v", l.Watch)
	}
	if l.ExpiresAt == nil || l.ExpiresAt.Before(before.Add(29*time.Second)) || l.ExpiresAt.After(time.Now().Add(31*time.Second)) {
		t.Errorf("expires_at = %v, want about now+30s", l.ExpiresAt)
	}
	if l.ExpiresAt != nil && l.ExpiresAt.Location() != time.UTC {
		t.Errorf("expires_at must be UTC, got %v", l.ExpiresAt.Location())
	}
	if strings.Join(l.Flags, "") != "i" {
		t.Errorf("flags = %v, want the default [i]", l.Flags)
	}
}

func TestAcquireIsIdempotentAndRefreshes(t *testing.T) {
	sd := useStateDir(t)
	for i := 0; i < 2; i++ {
		code, stdout, stderr := runCLI(t, "acquire", "--owner", "t", "--for", "30s")
		if code != 0 || stdout != "t.default\n" {
			t.Fatalf("run %d: exit %d, stdout %q, stderr %q", i, code, stdout, stderr)
		}
	}
	first := readLeaseFile(t, sd, "t.default")
	if code, _, stderr := runCLI(t, "acquire", "--owner", "t", "--for", "2h", "-s"); code != 0 {
		t.Fatalf("refresh exit = %d, stderr %q", code, stderr)
	}
	second := readLeaseFile(t, sd, "t.default")
	if !second.ExpiresAt.After(first.ExpiresAt.Add(time.Hour)) {
		t.Errorf("refresh kept the old expiry: %v -> %v", first.ExpiresAt, second.ExpiresAt)
	}
	if strings.Join(second.Flags, "") != "s" {
		t.Errorf("refresh flags = %v, want [s]", second.Flags)
	}
	entries, err := os.ReadDir(sd + "/leases")
	if err != nil {
		t.Fatal(err)
	}
	var jsons int
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".json") {
			jsons++
		}
	}
	if jsons != 1 {
		t.Errorf("%d lease files after refresh, want 1", jsons)
	}
}

func TestAcquireFlagsAndKey(t *testing.T) {
	sd := useStateDir(t)
	code, stdout, stderr := runCLI(t, "acquire", "--owner", "pa-monitor", "--key", "k_1", "--for", "1m", "-s", "-d", "-m")
	if code != 0 || stdout != "pa-monitor.k_1\n" {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	l := readLeaseFile(t, sd, "pa-monitor.k_1")
	if got := strings.Join(l.Flags, ""); got != "dms" {
		t.Errorf("flags = %q, want dms in d i m s order", got)
	}
}

func TestAcquirePidDefaultsToParent(t *testing.T) {
	requireProcInfo(t)
	sd := useStateDir(t)
	code, _, stderr := runCLI(t, "acquire", "--owner", "t")
	if code != 0 {
		t.Fatalf("exit = %d, stderr %q", code, stderr)
	}
	l := readLeaseFile(t, sd, "t.default")
	want, err := lease.Ref(os.Getppid())
	if err != nil {
		t.Fatal(err)
	}
	if l.Client == nil || *l.Client != want {
		t.Errorf("client = %+v, want the parent %+v", l.Client, want)
	}
	if l.ExpiresAt != nil {
		t.Errorf("expires_at = %v, want null with no --for", l.ExpiresAt)
	}
}

func TestAcquireExplicitPidAndFor(t *testing.T) {
	requireProcInfo(t)
	sd := useStateDir(t)
	pid := os.Getpid()
	code, _, stderr := runCLI(t, "acquire", "--owner", "t", "--pid", itoa(pid), "--for", "10m")
	if code != 0 {
		t.Fatalf("exit = %d, stderr %q", code, stderr)
	}
	l := readLeaseFile(t, sd, "t.default")
	want, _ := lease.Ref(pid)
	if l.Client == nil || *l.Client != want || l.ExpiresAt == nil {
		t.Errorf("--pid with --for must set both: client %+v expires %v", l.Client, l.ExpiresAt)
	}
}

func TestAcquireDeadPidCreatesNoLease(t *testing.T) {
	requireProcInfo(t)
	sd := useStateDir(t)
	code, stdout, stderr := runCLI(t, "acquire", "--owner", "t", "--pid", itoa(deadPID(t)))
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr %q)", code, stderr)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "not running") {
		t.Errorf("no warning on stderr: %q", stderr)
	}
	if fileExists(leasePath(sd, "t.default")) {
		t.Error("a dead --pid created a lease")
	}
}

func TestAcquireUsageErrors(t *testing.T) {
	useStateDir(t)
	cases := map[string][]string{
		"no owner":            {"acquire", "--for", "1m"},
		"owner with dot":      {"acquire", "--owner", "a.b", "--for", "1m"},
		"owner too long":      {"acquire", "--owner", strings.Repeat("a", 65), "--for", "1m"},
		"key with slash":      {"acquire", "--owner", "t", "--key", "a/b", "--for", "1m"},
		"unparsable duration": {"acquire", "--owner", "t", "--for", "soon"},
		"zero duration":       {"acquire", "--owner", "t", "--for", "0s"},
		"negative duration":   {"acquire", "--owner", "t", "--for", "-5s"},
		"zero pid":            {"acquire", "--owner", "t", "--pid", "0"},
		"stray argument":      {"acquire", "--owner", "t", "--for", "1m", "extra"},
		"unknown flag":        {"acquire", "--owner", "t", "--bogus"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			code, stdout, stderr := runCLI(t, args...)
			if code != 2 {
				t.Fatalf("exit = %d, want 2 (stderr %q)", code, stderr)
			}
			if stdout != "" {
				t.Errorf("stdout = %q, want empty on a usage error", stdout)
			}
			if stderr == "" {
				t.Error("no diagnostic on stderr")
			}
		})
	}
}
