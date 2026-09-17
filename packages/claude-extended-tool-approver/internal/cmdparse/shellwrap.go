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

// UnwrapShellDashCChain repeatedly applies UnwrapShellDashC — first to pc,
// then to the single resulting leaf, and so on — mirroring the chain-unwrap
// loop internal/rules/nix/nix.go's own innerCommandStructure already runs
// for ITS OWN `nix develop`/`nix shell` `-c`/`--command` inner-command
// resolution (that function's own doc names UnwrapShellDashC as the
// primitive it loops). This is a SECOND, reusable instance of the identical
// loop (pg2-zsv1c) for a caller — internal/rules/envvars' own nested
// PATH/HOME assignment judgement — that needs the fully-unwrapped LEAF SET
// itself, self-contained, rather than nix.go's inner-command-structure
// return shape wired through a rule-specific recursion boundary.
//
// It stops for the identical reason innerCommandStructure's own doc gives:
// either the wrapping ends (leaves is no longer exactly one leaf — the
// script itself split into more than one command, or reduced to zero) or
// the sole leaf is no longer bash/sh -c shaped. leaves/source are always a
// GENUINE cmdparse.Parse(source) pair (I12/I13): source is reassigned
// together with leaves at every step, never built or mutated text.
//
// ok is false only when pc ITSELF is not bash -c / sh -c shaped at all
// (UnwrapShellDashC's own first-step contract) — the ordinary case for any
// leaf that never wraps a nested shell, so a caller may test every leaf in
// a parsed command unconditionally.
func UnwrapShellDashCChain(pc ParsedCommand) (leaves []ParsedCommand, source string, ok bool) {
	script, first := UnwrapShellDashC(pc)
	if !first {
		return nil, "", false
	}
	source = script
	leaves = Parse(source)
	for len(leaves) == 1 {
		inner, innerOK := UnwrapShellDashC(leaves[0])
		if !innerOK {
			break
		}
		source = inner
		leaves = Parse(source)
	}
	return leaves, source, true
}

// NestedShellDashCVars returns the LITERAL shell variables leaf pc's own
// PREFIX assignments establish for a nested bash -c / sh -c payload it
// wraps — e.g. `T=/abs/dir bash -c 'PATH="$T/bin:$PATH" cmd'` — using the
// IDENTICAL literal-value rule InCommandVars applies to a plain assignment
// (literalAssignedValue). A prefix assignment on ANY command becomes
// exactly that command's OWN process environment, and when the command IS
// a shell interpreting `-c`, its own `$NAME` expansions resolve against
// precisely that environment — the one channel (pg2-zsv1c) by which a
// value the OUTER command text establishes genuinely reaches a
// single-quoted nested script's own parameter expansions, without needing
// export: this is PREFIX-assignment scoping to the invoked command, the
// same mechanism `export` relies on, not the OUTER shell's own (never
// exported) variable table, which a spawned `bash -c` process cannot see
// at all — see this file's package doc and InCommandVars' own SCOPE
// section for why a plain, non-prefix `WT=/x` earlier in the OUTER
// expression is deliberately NOT threaded here.
func NestedShellDashCVars(pc ParsedCommand) map[string]string {
	var vars map[string]string
	for _, ev := range pc.EnvVars {
		if ev.Name == "" {
			continue
		}
		value, ok := literalAssignedValue(ev)
		if !ok {
			delete(vars, ev.Name)
			continue
		}
		if vars == nil {
			vars = map[string]string{}
		}
		vars[ev.Name] = value
	}
	return vars
}

// NestedShellDashCTempDirVars is NestedShellDashCVars' sibling for the
// fresh-temp-dir MARKER (IsFreshTempDirAssignment) rather than a literal
// value — InCommandTempDirVars' own identical relationship to
// InCommandVars, applied to the SAME prefix-assignment channel: e.g.
// `T=$(mktemp -d) bash -c 'HOME="$T"'`.
func NestedShellDashCTempDirVars(pc ParsedCommand) map[string]string {
	var vars map[string]string
	for _, ev := range pc.EnvVars {
		if ev.Name == "" {
			continue
		}
		if IsFreshTempDirAssignment(ev) {
			if vars == nil {
				vars = map[string]string{}
			}
			vars[ev.Name] = ""
			continue
		}
		delete(vars, ev.Name)
	}
	return vars
}
