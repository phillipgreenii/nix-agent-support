package cmdparse

import "strings"

// IN-COMMAND VARIABLE RESOLUTION (pg2-wq3ki)
//
// A rule that judges a PATH cannot judge `$WT`. cmdparse deliberately does not expand
// parameters, so `git -C "$WT" commit` arrives as the literal token `$WT` and every
// path-judging rule must treat it as UNRESOLVED (the fail-safe verdict
// internal/rules/primarycommit/dirresolve.go's DIRECTORY RESOLUTION comment describes).
// That is correct for a variable whose value the command does not itself establish —
// an inherited export, a `$(…)` nobody can evaluate statically — and it is needlessly
// expensive for the one shape where the value is written down RIGHT THERE:
//
//	WT=/abs/worktree && git -C "$WT" commit -m x
//	WT=/abs/worktree; cd "$WT" && git commit -m x
//
// The value is a property of the COMMAND TEXT in those, not of run time, so refusing to
// read it costs a prompt and buys nothing. This file is the seam that reads it, and it
// reads NOTHING ELSE: the environment it returns holds only values that are literal in
// the text of the SAME expression, so a caller that gets no binding is in exactly the
// state it was in before this file existed.
//
// SCOPE, and every limit is deliberate:
//
//   - ONE EXPRESSION. A hook call sees one Bash command; an `export` in an EARLIER,
//     separate call is invisible to CETA entirely (bead pg2-xjt1s) and no seam here can
//     recover it.
//   - ONLY A LEAF THAT REALLY SETS A SHELL VARIABLE. A PREFIX assignment
//     (`WT=/x git status`) sets the variable in that ONE command's environment and not
//     in the shell, and — the case that matters — bash expands a command's words BEFORE
//     applying its own prefix assignments, so in `WT=/x git -C "$WT" commit` the `$WT`
//     is the OLD value. establishesShellVars refuses both.
//   - ONLY A LITERAL VALUE. See literalAssignedValue: no expansion, no glob, no
//     surviving quote. A `$(…)` value is NOT derived, not even for a git read command
//     whose output looks computable — internal/rules/primarycommit/dirresolve.go's
//     DECLINED section records the measurement that settles it.
//   - A LATER NON-LITERAL ASSIGNMENT REVOKES AN EARLIER LITERAL ONE, so
//     `WT=/x && WT=$(mktemp -d) && git -C "$WT" commit` is unresolved rather than
//     confidently wrong about the first value.
//
// RESIDUAL, recorded rather than modelled: a SUBSHELL scopes its assignments, and a
// leaf carries no subshell identity (the lowering emits a subshell's statements into
// the same flat leaf list — see shellparse.go's `case *syntax.Subshell`), so an
// assignment made inside `( … )` and consumed OUTSIDE it reads here as though it
// persisted. Both halves of the common shape are unaffected: `(WT=/x && cd "$WT" && git
// commit)` assigns and consumes inside the SAME subshell, which is what bash does too.
// The residual needs a command that is already broken — it consumes a variable its own
// subshell scoped away — and closing it needs a subshell SCOPE PATH on the leaf (a
// depth flag is not enough: it cannot tell "the same subshell" from "a sibling one"),
// which is its own change to the lowering and its own replay. A PIPELINE stage is the
// same class and IS excluded here, because the leaf already carries the pipeline
// coordinates that identify one (inMultiStagePipeline).

// InCommandVars returns the shell variables that the leaves BEFORE index `before`
// establish for the rest of the expression, mapped to their LITERAL values. nil when
// nothing qualifies, which is the ordinary case — and the case that leaves every
// caller's verdict exactly as it was.
//
// `leaves` MUST be a single Parse call's output, in source order (the pipeline
// coordinates it consults are per-call). `before` is the index of the leaf about to be
// judged: it is EXCLUSIVE, which is what keeps a leaf's own prefix assignments out of
// its own expansions.
func InCommandVars(leaves []ParsedCommand, before int) map[string]string {
	if before > len(leaves) {
		before = len(leaves)
	}
	var vars map[string]string
	for i := 0; i < before; i++ {
		if !establishesShellVars(leaves, i) {
			continue
		}
		for _, ev := range leaves[i].EnvVars {
			if ev.Name == "" {
				continue
			}
			value, ok := literalAssignedValue(ev)
			if !ok {
				// REVOCATION, not a skip: a name this loop already bound to a literal
				// must not keep that binding after the command reassigns it to something
				// unreadable. `delete` on a nil map is a no-op, so no guard is needed.
				delete(vars, ev.Name)
				continue
			}
			if vars == nil {
				vars = map[string]string{}
			}
			vars[ev.Name] = value
		}
	}
	return vars
}

// establishesShellVars reports whether leaf i's assignments outlive the leaf, i.e.
// whether they are visible to the leaves that follow it in the same expression.
func establishesShellVars(leaves []ParsedCommand, i int) bool {
	pc := leaves[i]
	if len(pc.EnvVars) == 0 {
		return false
	}
	// A leaf with an executable carries PREFIX assignments (`WT=/x git status`, or the
	// `env WT=/x cmd` form cmdparse lifts into the same field): they are scoped to that
	// command's environment and never reach the shell. `export` is the one executable
	// that does set a shell variable — cmdparse lifts each `export NAME=VALUE` argument
	// into EnvVars for exactly this reason (pg2-gkd5e). `declare`/`local`/`readonly`/
	// `typeset` also set one, but the lowering leaves their assignments in Args rather
	// than EnvVars, so there is nothing here to read and they stay unresolved.
	if pc.Executable != "" && pc.Executable != "export" {
		return false
	}
	// A PIPELINE STAGE runs in a subshell, so its assignments die with it:
	// `WT=/x | cat` leaves WT unset in the shell.
	return !inMultiStagePipeline(leaves, i)
}

// inMultiStagePipeline reports whether leaf i is one stage of a MULTI-STAGE pipeline.
// A lone command is a one-stage pipeline and is not one of these.
func inMultiStagePipeline(leaves []ParsedCommand, i int) bool {
	pc := leaves[i]
	if pc.PipelineID < 0 { // a synthesized data leaf stands in no pipeline (tc-vul7)
		return false
	}
	if pc.PipelineIndex > 0 {
		return true
	}
	for j, other := range leaves {
		if j != i && other.PipelineID == pc.PipelineID && other.PipelineIndex > pc.PipelineIndex {
			return true
		}
	}
	return false
}

// literalAssignedValue returns the value an assignment binds, when that value is
// LITERAL — the exact bytes the shell will use, derivable from the command text alone.
//
// cmdparse keeps an assignment token's quoting verbatim (the value of `WT="/a b"` is
// `"/a b"`, quotes included), so ONE wrapping quote pair is stripped and any surviving
// quote or backslash disqualifies the value: mixed quoting (`WT="/a"/b`) is not
// derivable by stripping alone, which is the same reasoning
// internal/rules/envvars/envvars.go's literalValue records.
//
// The expansion markers are then rejected a SECOND time, after Expansion has already
// been consulted. That is not redundant: a SINGLE-quoted value suppresses expansion, so
// `WT='$HOME/x'` is genuinely literal — and the literal it names still contains a `$`,
// which every path consumer must keep treating as unresolvable. The two checks answer
// different questions ("will the shell expand this?" and "is the result a path a rule
// can judge?") and both must pass.
func literalAssignedValue(ev EnvAssignment) (string, bool) {
	if ev.Expansion != ExpansionNone {
		return "", false
	}
	// The bash APPEND form `NAME+=VALUE` binds the CONCATENATION with a value this seam
	// may never have seen (an inherited one), so its Value alone is not what the shell
	// will use. Refused, which revokes any earlier binding via InCommandVars' delete.
	if strings.HasPrefix(ev.Raw, ev.Name+"+=") {
		return "", false
	}
	v := ev.Value
	if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
		v = v[1 : len(v)-1]
	}
	if strings.ContainsAny(v, "\"'\\") {
		return "", false
	}
	if !isLiteralWordText(v) {
		return "", false
	}
	return v, true
}

// isLiteralWordText reports whether s is text a path consumer can take at face value:
// nothing the shell would rewrite, and nothing that would corrupt a user-facing prompt.
// The marker set matches internal/rules/primarycommit/dirresolve.go's unresolvableToken
// — `$` and a backtick for substitutions, `*`/`?` for pathname expansion, a leading `~`
// for tilde expansion — so an expansion this seam clears can never be one that rule
// still calls unresolvable.
func isLiteralWordText(s string) bool {
	if strings.HasPrefix(s, "~") {
		return false
	}
	if strings.ContainsAny(s, "$`*?") {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] == 0x7f {
			return false
		}
	}
	return true
}

// ExpandInCommand expands word against an in-command variable environment and reports
// whether the result is FULLY LITERAL. It is all-or-nothing on purpose: a partially
// expanded path is exactly the "confident wrong answer" the unresolved verdict exists to
// prevent, so anything this function cannot resolve — an unknown name, a `$(…)`, a
// backtick, a glob, a leading `~`, `${NAME:-default}`, `$1`, `$@` — returns ok=false and
// leaves the caller on its existing fail-safe path.
//
// A word with no expansion at all is already literal and comes back unchanged with
// ok=true, so a caller may hand every word to this function without pre-screening.
//
// QUOTING IS NOT OBSERVABLE HERE, and no part of this design depends on it: words arrive
// POST-UNQUOTE, so a quoted literal dollar (`git -C '$WT' commit`, a directory really
// named `$WT`) is indistinguishable from a live expansion (bead pg2-rz9ds). Such a word
// therefore expands here as if it were live. That is the pre-existing ambiguity, and its
// consequence is bounded: the directory a quoted-dollar spelling names almost never
// exists, so the command it belongs to fails on its own.
func ExpandInCommand(word string, vars map[string]string) (string, bool) {
	if !strings.Contains(word, "$") {
		return word, isLiteralWordText(word)
	}
	if len(vars) == 0 {
		return "", false
	}
	var b strings.Builder
	b.Grow(len(word))
	for i := 0; i < len(word); i++ {
		switch c := word[i]; {
		case c == '$':
			name, end, ok := plainVarRef(word, i)
			if !ok {
				return "", false
			}
			value, known := vars[name]
			if !known || !isLiteralWordText(value) {
				return "", false
			}
			b.WriteString(value)
			i = end - 1
		case c == '`' || c == '*' || c == '?':
			return "", false
		case c == '~' && i == 0:
			return "", false
		default:
			b.WriteByte(c)
		}
	}
	return b.String(), true
}

// plainVarRef reads the parameter reference starting at s[at] (which MUST be '$') and
// returns its NAME and the offset just past it. Only the two forms whose value is the
// variable and nothing else are accepted: `$NAME` and `${NAME}`. Every other `$`
// construct — `${NAME:-default}`, `${#NAME}`, `${NAME[0]}`, `$1`, `$@`, `$$`, `$(…)`,
// `$((…))` — reports ok=false, because its value is not a lookup this seam performed.
func plainVarRef(s string, at int) (name string, end int, ok bool) {
	i := at + 1
	braced := false
	if i < len(s) && s[i] == '{' {
		braced = true
		i++
	}
	start := i
	for i < len(s) && isVarNameByte(s[i], i == start) {
		i++
	}
	name = s[start:i]
	if name == "" {
		return "", 0, false
	}
	if braced {
		if i >= len(s) || s[i] != '}' {
			return "", 0, false
		}
		i++
	}
	return name, i, true
}

func isVarNameByte(c byte, first bool) bool {
	if c == '_' || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') {
		return true
	}
	return !first && c >= '0' && c <= '9'
}
