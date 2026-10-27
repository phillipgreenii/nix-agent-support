//go:build integration

// Hook-mode tests, through the COMPILED binary, for what a pool (dontAsk) session may
// read and write that an interactive one may not, and for the host-scalar and scratch
// commands approved for every session: bead pg2-b1lir W3, W4 and W6, operator ruling
// pg2-r9kv7 (2026-10-27). See hook_unattended_integration_test.go for the harness.

package main

import "testing"

const poolRootsEnv = "CETA_UNATTENDED_EXTRA_READONLY_ROOTS=/var/log:/Library/LaunchDaemons:/usr/local/libexec/pg-launchd"

// TestHook_UnattendedRoots_OnlyAnUnattendedSessionReadsThem: the roots in
// CETA_UNATTENDED_EXTRA_READONLY_ROOTS are read-only for a dontAsk session and
// invisible to an attended one.
func TestHook_UnattendedRoots_OnlyAnUnattendedSessionReadsThem(t *testing.T) {
	reads := []string{
		"ls -l /usr/local/libexec/pg-launchd/",
		"cat /var/log/pg-router.log",
		"ls /Library/LaunchDaemons",
		"timeout 60 tail -f /var/log/pg-router.log | grep -m1 DONE",
	}
	for _, cmd := range reads {
		if got := runPreToolUse(t, bashPayload(secretCWD, "dontAsk", cmd), poolRootsEnv).decision(); got != "allow" {
			t.Errorf("dontAsk %q: decision = %q, want allow", cmd, got)
		}
		if got := runPreToolUse(t, bashPayload(secretCWD, "default", cmd), poolRootsEnv).decision(); got == "allow" {
			t.Errorf("attended %q: decision = allow, want the roots invisible to an attended session", cmd)
		}
		if got := runPreToolUse(t, bashPayload(secretCWD, "dontAsk", cmd)).decision(); got == "allow" {
			t.Errorf("dontAsk without the roots %q: decision = allow, want not allowed", cmd)
		}
	}
	// A root grants a READ: a write into it is never approved.
	for _, cmd := range []string{"mkdir -p /var/log/x", "touch /Library/LaunchDaemons/x.plist", "rm /var/log/pg-router.log"} {
		if got := runPreToolUse(t, bashPayload(secretCWD, "dontAsk", cmd), poolRootsEnv).decision(); got == "allow" {
			t.Errorf("dontAsk %q: decision = allow, want a read-only root to stay read-only", cmd)
		}
	}
}

// TestHook_UnattendedRoots_RefuseBroadRoots: a configured root that is $HOME, above
// it, or `/` is ignored, so a mistaken list can never grant a whole tree. (A root
// that names ~/.claude is dropped too, pinned by patheval's own unit test: CETA's
// built-in zones already let a read command see ~/.claude, which no root here can
// change in either direction.)
func TestHook_UnattendedRoots_RefuseBroadRoots(t *testing.T) {
	// A synthetic HOME outside every zone the evaluator knows: t.TempDir() lands under the
	// machine's temp roots, which are readable on their own and would mask a refused root.
	const home = "/srv/ceta-test-home"
	roots := "CETA_UNATTENDED_EXTRA_READONLY_ROOTS=" + home + ":/srv:/"
	for _, cmd := range []string{"ls " + home, "cat " + home + "/notes.txt", "cat /srv/other/x.txt", "ls /srv"} {
		if got := runPreToolUse(t, bashPayload(secretCWD, "dontAsk", cmd), "HOME="+home, roots).decision(); got == "allow" {
			t.Errorf("dontAsk %q: decision = allow, want a root at or above $HOME refused", cmd)
		}
	}
}

// TestHook_UnattendedRoots_SessionTmpdir: a dontAsk session may read and write under
// its own $TMPDIR (the bgrun state and a scratch directory), nobody else's.
func TestHook_UnattendedRoots_SessionTmpdir(t *testing.T) {
	const tmp = "/private/var/folders/zz/fake/T"
	env := "TMPDIR=" + tmp
	for _, cmd := range []string{
		"ls " + tmp + "/pg-bg-user",
		"cat " + tmp + "/pg-bg-user/job.log",
		"mkdir -p " + tmp + "/pg2-b1lir-scratch",
	} {
		if got := runPreToolUse(t, bashPayload(secretCWD, "dontAsk", cmd), env).decision(); got != "allow" {
			t.Errorf("dontAsk %q: decision = %q, want allow", cmd, got)
		}
		if got := runPreToolUse(t, bashPayload(secretCWD, "default", cmd), env).decision(); got == "allow" {
			t.Errorf("attended %q: decision = allow, want the TMPDIR grant to be unattended-only", cmd)
		}
	}
	for _, cmd := range []string{"ls /private/var/folders/zz/other/T", "ls /private/var/folders"} {
		if got := runPreToolUse(t, bashPayload(secretCWD, "dontAsk", cmd), env).decision(); got == "allow" {
			t.Errorf("dontAsk %q: decision = allow, want only the session's own TMPDIR", cmd)
		}
	}
}

// TestHook_HostScalarsAndScratch: W4 and W6 for every session. sysctl -n, pmset -g
// and uuidgen read; mktemp and tar -x write only inside a temp root.
func TestHook_HostScalarsAndScratch(t *testing.T) {
	allow := []string{
		"sysctl -n hw.ncpu hw.memsize",
		"pmset -g",
		"pmset -g assertions",
		"uuidgen",
		"mktemp -d /tmp/pg2-b1lir-XXXXXX",
		"tar -xf /tmp/pg2-b1lir/a.tar -C /tmp/pg2-b1lir/out",
		"tar xzf /tmp/pg2-b1lir/a.tgz -C /tmp/pg2-b1lir/out",
	}
	deny := []string{
		"sysctl -w kern.maxfiles=1",
		"sysctl kern.maxfiles=1",
		"pmset -a sleep 0",
		"pmset sleepnow",
		"mktemp -d /etc/x.XXXXXX",
		"mktemp -d " + secretCWD + "/x.XXXXXX",
		"tar -xf /tmp/a.tar -C " + secretCWD + "/out",
		"tar -xPf /tmp/a.tar -C /tmp/out",
		"tar -xf /tmp/a.tar -C /tmp/out --to-command=sh",
		"tar -cf /tmp/a.tar /tmp/x",
	}
	for _, mode := range []string{"default", "dontAsk"} {
		for _, cmd := range allow {
			if got := runPreToolUse(t, bashPayload(secretCWD, mode, cmd)).decision(); got != "allow" {
				t.Errorf("%s %q: decision = %q, want allow", mode, cmd, got)
			}
		}
		for _, cmd := range deny {
			if got := runPreToolUse(t, bashPayload(secretCWD, mode, cmd)).decision(); got == "allow" {
				t.Errorf("%s %q: decision = allow, want not allowed", mode, cmd)
			}
		}
	}
}

// TestHook_GitPoolReads: W5 through the binary.
func TestHook_GitPoolReads(t *testing.T) {
	for _, cmd := range []string{
		"git patch-id --stable",
		"git -c core.fsmonitor=false status --short",
		"git for-each-ref refs/heads",
		"git ls-files -- '*.go'",
		"git grep -n foo",
	} {
		if got := runPreToolUse(t, bashPayload(secretCWD, "dontAsk", cmd)).decision(); got != "allow" {
			t.Errorf("dontAsk %q: decision = %q, want allow", cmd, got)
		}
	}
	for _, cmd := range []string{"git reset --hard", "git clean -fd"} {
		if got := runPreToolUse(t, bashPayload(secretCWD, "dontAsk", cmd)).decision(); got == "allow" {
			t.Errorf("dontAsk %q: decision = allow, want it kept out", cmd)
		}
	}
}
