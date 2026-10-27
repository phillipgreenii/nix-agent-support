// xargs_arg_file_operands_test.go — pg2-b0qcjc asserted END TO END, through the
// production rule chain, as a RELATION between two ways of naming one file to read.
//
// THE RELATION: for any path, naming it as the FILE xargs reads its items from
// (`xargs -a P CMD`, `xargs --arg-file P CMD`) is never LESS gated than reading
// it directly (`cat P`).
//
// The defect: pg2-imjrv (e2fdc3267) taught safecmds' xargs parser to CONSUME the
// value of -a/--arg-file so it is never mistaken for the inner command, but the
// consumed value was then dropped, so no rule judged it. `xargs -a /etc/shadow
// cat` measured `approve` while `cat /etc/shadow` measured `abstain`. Like the
// grep flag-operand family (grep_flag_operands_test.go), no verdict table could
// see it: each row was plausible on its own and only the COMPARISON is a defect.
//
// Hermetic: HOME and TMPDIR are fresh directories and nothing is read from or
// written to the real home. The paths are judged by the path model, not opened.
package engine_test

import "testing"

// xargsArgFileForms renders every spelling that makes xargs open path p itself,
// over inner commands from the strict-read, browsing and grep families.
var xargsArgFileForms = []func(p string) string{
	func(p string) string { return "xargs -a " + p + " cat" },
	func(p string) string { return "xargs --arg-file " + p + " cat" },
	func(p string) string { return "xargs --arg-file=" + p + " cat" },
	func(p string) string { return "xargs -a" + p + " cat" },
	func(p string) string { return "xargs -0 -a " + p + " cat" },
	func(p string) string { return "xargs -0a " + p + " cat" },
	func(p string) string { return "xargs -n1 -a " + p + " ls" },
	func(p string) string { return "xargs -a " + p + " rg --files" },
	func(p string) string { return "xargs -a " + p + " grep -l pat" },
	func(p string) string { return "xargs -a " + p + " sh -c 'echo \"$1\"' _" },
}

func TestXargs_ArgFileIsNeverLooserThanADirectRead(t *testing.T) {
	// The fresh HOME and TMPDIR keep the zone model off the operator's real
	// home. No declared temp root is needed: paths are judged by the path
	// model and never opened, and /tmp is a built-in temp root.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("WORKSPACE_ROOT", "/Users/testuser/workspace")
	projectRoot := "/Users/testuser/workspace/my-project"
	eng := buildFullEngine(projectRoot, projectRoot)

	paths := []string{
		// In zone: the relation must hold AND these must stay approved (below).
		"./data.json",
		projectRoot + "/data.json",
		"/tmp/globs.txt",
		// Out of every zone, and deny-listed.
		"/etc/shadow", "/etc/passwd", "/var/log/system.log",
		"/Users/testuser/notes.txt",
		"/Users/testuser/.ssh/id_rsa", "/Users/testuser/.aws/credentials",
		"~/.ssh/config", ".env",
	}
	for _, p := range paths {
		direct := "cat " + p
		want := eng.EvaluateHook(provenanceInput(projectRoot, direct))
		for _, form := range xargsArgFileForms {
			cmd := form(p)
			got := eng.EvaluateHook(provenanceInput(projectRoot, cmd))
			if got.Decision < want.Decision {
				t.Errorf("XARGS ARG-FILE HOLE: %q is %s but the direct read %q is %s — naming the path through xargs -a/--arg-file made it LESS gated\n  xargs reason:  %s\n  direct reason: %s",
					cmd, got.Decision, direct, want.Decision, got.Reason, want.Reason)
			}
		}
	}
}

// TestXargs_ArgFileOutOfZoneIsNotApproved is the absolute half of the relation:
// the replay rows from pg2-b0qcjc, which measured `approve` before the fix.
func TestXargs_ArgFileOutOfZoneIsNotApproved(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("WORKSPACE_ROOT", "/Users/testuser/workspace")
	projectRoot := "/Users/testuser/workspace/my-project"
	eng := buildFullEngine(projectRoot, projectRoot)

	for _, cmd := range []string{
		"xargs -a /etc/shadow cat",
		"xargs --arg-file /etc/shadow cat",
		"xargs --arg-file=/etc/shadow cat",
		"xargs -a/etc/shadow cat",
		"xargs -a /var/log/system.log ls",
		"xargs -a /Users/testuser/notes.txt rg --files",
		"xargs -a /etc/passwd sh -c 'echo \"$1\"' _",
	} {
		got := eng.EvaluateHook(provenanceInput(projectRoot, cmd))
		if got.Decision.String() == "approve" {
			t.Errorf("OUT-OF-ZONE ARG-FILE APPROVED: %q is approve; the file's contents are read by xargs, so it must be gated like `cat` (reason: %s)", cmd, got.Reason)
		}
	}
}

// TestXargs_ArgFileInZoneStillApproved is the opposite-direction half: the fix
// must only narrow, so the in-zone shapes (the originating row was
// `xargs -a /tmp/... rg --files`) keep their approval.
func TestXargs_ArgFileInZoneStillApproved(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("WORKSPACE_ROOT", "/Users/testuser/workspace")
	projectRoot := "/Users/testuser/workspace/my-project"
	eng := buildFullEngine(projectRoot, projectRoot)

	list := "/tmp/globs.txt"
	for _, cmd := range []string{
		"xargs -a ./data.json cat",
		"xargs --arg-file ./data.json cat",
		"xargs -a " + projectRoot + "/data.json rg --files",
		"xargs -a " + list + " rg --files",
		"xargs --arg-file=" + list + " cat",
	} {
		got := eng.EvaluateHook(provenanceInput(projectRoot, cmd))
		if got.Decision.String() != "approve" {
			t.Errorf("IN-ZONE ARG-FILE REGRESSED: %q is %s, want approve (reason: %s)", cmd, got.Decision, got.Reason)
		}
	}
}
