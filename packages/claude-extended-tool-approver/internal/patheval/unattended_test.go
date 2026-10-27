package patheval

import (
	"testing"
)

// unattendedEvaluator builds an evaluator with HOME pinned (the zone table depends
// on it) and the unattended roots/TMPDIR environment set, then extends it.
func unattendedEvaluator(t *testing.T, roots, tmpdir string) *PathEvaluator {
	t.Helper()
	t.Setenv("HOME", "/home/testuser")
	t.Setenv("CETA_EXTRA_READONLY_ROOTS", "")
	t.Setenv("CETA_EXTRA_READWRITE_ROOTS", "")
	t.Setenv(UnattendedExtraReadOnlyRootsEnv, roots)
	t.Setenv("TMPDIR", tmpdir)
	pe := NewWithCWD("/home/testuser/project", "/home/testuser/project")
	pe.ExtendForUnattended()
	return pe
}

func TestExtendForUnattended_ReadOnlyRoots(t *testing.T) {
	pe := unattendedEvaluator(t, "/var/log:/Library/LaunchDaemons:/usr/local/libexec/pg-launchd", "")
	for _, p := range []string{"/var/log/system.log", "/Library/LaunchDaemons/x.plist", "/usr/local/libexec/pg-launchd/y"} {
		got := pe.Evaluate(p)
		if !got.CanRead() || got.CanWrite() {
			t.Errorf("%s: read=%v write=%v, want read-only", p, got.CanRead(), got.CanWrite())
		}
	}
	if pe.Evaluate("/Library/Preferences/x").CanRead() {
		t.Errorf("a sibling of a root must stay unknown")
	}
}

// TestExtendForUnattended_NotExtended proves an evaluator that never calls
// ExtendForUnattended (an attended session) does not see the roots at all.
func TestExtendForUnattended_NotExtended(t *testing.T) {
	t.Setenv("HOME", "/home/testuser")
	t.Setenv(UnattendedExtraReadOnlyRootsEnv, "/var/log")
	pe := NewWithCWD("/home/testuser/project", "/home/testuser/project")
	if pe.Evaluate("/var/log/system.log").CanRead() {
		t.Errorf("an attended evaluator must not read /var/log through the unattended roots")
	}
}

// TestExtendForUnattended_RefusesBroadAndSecretRoots: a root at or above $HOME, or
// one that is, contains or sits under ~/.claude (transcripts can hold secrets), is
// ignored even when configured.
func TestExtendForUnattended_RefusesBroadAndSecretRoots(t *testing.T) {
	pe := unattendedEvaluator(t, "/:/home:/home/testuser:/home/testuser/.claude:/home/testuser/.claude/projects", "")
	if len(pe.ExtraReadOnlyRoots()) != 0 {
		t.Errorf("roots %v survived, want none", pe.ExtraReadOnlyRoots())
	}
}

func TestExtendForUnattended_Tmpdir(t *testing.T) {
	const bg = "/private/var/folders/zz/fake/T/pg-bg-user/job.log"
	pe := unattendedEvaluator(t, "", "/private/var/folders/zz/fake/T/")
	if got := pe.Evaluate(bg); !got.CanRead() || !got.CanWrite() {
		t.Errorf("%s: read=%v write=%v, want read-write through the session TMPDIR", bg, got.CanRead(), got.CanWrite())
	}
	if pe.Evaluate("/private/var/folders/zz/other/T/x").CanRead() {
		t.Errorf("another user's temp directory must stay unknown")
	}
	for _, bad := range []string{"/", "/etc", "/home/testuser", "/private/var/folders", "/private/var/folders/", "relative/tmp", ""} {
		pe := unattendedEvaluator(t, "", bad)
		if len(pe.ExtraReadWriteRoots()) != 0 {
			t.Errorf("TMPDIR=%q was granted: %v", bad, pe.ExtraReadWriteRoots())
		}
	}
}
