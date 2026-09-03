package cmdparse

import "path/filepath"

// UnwrapShellDashC reports whether pc is a `bash -c <script>` / `sh -c
// <script>` invocation and, if so, returns <script> — a REAL element of
// pc.Args, never text built or mutated by this function (I12/I13).
//
// This is docker.go's `scriptArg` check (pg2-lwwwk), GENERALIZED out of that
// rule package (pg2-ipn7w) so a second caller with the identical need —
// nix.go's `nix develop -c`/`nix shell -c` inner-command resolution, which
// hands argv to execve directly and can therefore hand a SECOND, nested
// shell invocation to `-c`/`--command` just as easily as a plain command —
// does not have to re-implement the same check a third time. docker.go
// itself is deliberately NOT migrated onto this in the same bead that adds
// it: its own scriptArg is exercised by an extensive, already-passing test
// suite (pg2-lwwwk's own acceptance criteria), and swapping the
// implementation out from under it is a separate, purely-mechanical
// refactor with no behaviour change to justify touching that surface here.
//
// Only the LITERAL `-c` flag is recognised (matching docker.go's own scope):
// a combined/short-flag spelling such as `sh -ec` is not.
func UnwrapShellDashC(pc ParsedCommand) (script string, ok bool) {
	base := filepath.Base(pc.Executable)
	if base != "bash" && base != "sh" {
		return "", false
	}
	if len(pc.Args) < 2 || pc.Args[0] != "-c" {
		return "", false
	}
	return pc.Args[1], true
}
