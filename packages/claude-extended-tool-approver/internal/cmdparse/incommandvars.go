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
//     is the OLD value. shellVarWrites refuses both.
//   - ONLY A LITERAL SCALAR VALUE. See literalAssignedValue: no expansion, no glob, no
//     surviving quote, and not bash's ARRAY form (`arr=(/a /b)`, whose `$arr` is the
//     FIRST ELEMENT and not the parenthesised text). A `$(…)` value is NOT derived, not
//     even for a git read command whose output looks computable —
//     internal/rules/primarycommit/dirresolve.go's DECLINED section records the
//     measurement that settles it.
//   - AN ASSIGNMENT BUILTIN IS READ ONLY WHERE ITS OWN SEMANTICS SURVIVE IT.
//     `export`, `declare` and `typeset` genuinely set a shell variable, so an
//     UNFLAGGED literal assignment through one of them reads exactly like the plain
//     spelling (pg2-ft2hl). `local`, `readonly` and `nameref` are DECLINED, and a
//     FLAGGED `declare`/`typeset` is never read, because the value bash stores is then
//     not the text written down. See assignmentBuiltinReads and declWrites for each
//     recorded reason.
//   - A LATER NON-LITERAL ASSIGNMENT REVOKES AN EARLIER LITERAL ONE, so
//     `WT=/x && WT=$(mktemp -d) && git -C "$WT" commit` is unresolved rather than
//     confidently wrong about the first value. REVOCATION IS A DIFFERENT QUESTION FROM
//     BINDING and the two are answered separately (shellVarWrites returns both): a leaf
//     that writes a name whose value cannot be read must revoke it, while a leaf that
//     does not reach the shell at all — a prefix assignment, a pipeline stage — must
//     leave every earlier binding exactly as it was.
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
		writes, readValues := shellVarWrites(leaves, i)
		for _, ev := range writes {
			if ev.Name == "" {
				continue
			}
			value, ok := "", false
			if readValues {
				value, ok = literalAssignedValue(ev)
			}
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

// assignmentBuiltinReads lists every ASSIGNMENT BUILTIN the lowering can put in a
// leaf's Executable (shellparse.go's lowerDecl: the `*syntax.DeclClause` variants),
// mapped to whether this seam may READ the values it assigns.
//
// A builtin ABSENT from this map is not an assignment builtin at all, so a leaf naming
// it carries PREFIX assignments and writes nothing to the shell. A builtin present with
// `false` DOES write the shell — so its names are REVOKED — but its values are never
// read. Each `false` is a recorded refusal, not an oversight:
//
//   - `local` (OPERATOR RULING, pg2-ft2hl, 2026-08-13): outside a function `local` is a
//     bash ERROR, so reading it as a plain assignment models a command that cannot run —
//     and inside one it binds a value that dies at the function's end, a scope this seam
//     cannot see (it has no more function identity than the subshell identity the file
//     comment's RESIDUAL paragraph records it lacking).
//     To implement it later, a leaf would first have to carry the FUNCTION BODY it was
//     lowered from, which is the same scope-path change pg2-4ak2k needs for subshells.
//   - `readonly` (OPERATOR RULING, pg2-ft2hl, 2026-08-13): a readonly name CANNOT be
//     reassigned, so the SCOPE comment's revoke rule does not hold for it unchanged — a
//     later assignment to the name FAILS and leaves the ORIGINAL value in place, and the
//     `&&` that usually follows then short-circuits. Reading it would therefore need a
//     second, inverted rule ("a later assignment to a readonly name is the one that is
//     revoked"), and getting that backwards is worse than the prompt it saves. To
//     implement it later, this seam would have to track READONLY-ness per name, not just
//     the value.
//   - `nameref` (the ksh spelling of `declare -n`): the name is an ALIAS, so `$r` after
//     `nameref r=t` expands to the value of `t` and never to `t`. That is the same
//     refusal the `-n` flag gets in declWrites.
var assignmentBuiltinReads = map[string]bool{
	// `export` is the pg2-gkd5e case: unwrapCommand's liftAssignmentArgs has already
	// moved each `export NAME=VALUE` argument into EnvVars, and none of export's own
	// flags (`-n`, `-p`, `-f`) rewrites a VALUE, so there is nothing extra to guard.
	"export": true,
	// `declare` / `typeset` are the same builtin under two names, and the unflagged form
	// is a plain shell-variable assignment. This is the relief pg2-ft2hl authorizes.
	"declare":  true,
	"typeset":  true,
	"local":    false,
	"readonly": false,
	"nameref":  false,
}

// shellVarWrites reports what leaf i writes to the SHELL's variables — the assignments
// whose effect outlives the leaf — and whether this seam may read their VALUES.
//
// The two answers are deliberately separate. "This leaf changes the name" and "I can
// tell what to" are different facts, and collapsing them is how a stale binding
// survives a reassignment that should have taken it away: before pg2-ft2hl a
// `declare`-family leaf was simply skipped, so `WT=/x && declare -i WT=5+5 && git -C
// "$WT" commit` kept the binding `/x` while bash had already made it `10`. A leaf that
// writes a name whose value is unreadable therefore returns that name with
// readValues=false (InCommandVars revokes it), while a leaf that never reaches the
// shell returns NO writes at all so every earlier binding stands.
func shellVarWrites(leaves []ParsedCommand, i int) (writes []EnvAssignment, readValues bool) {
	pc := leaves[i]
	// A PIPELINE STAGE runs in a subshell, so nothing it assigns reaches the shell:
	// `WT=/x | cat` leaves WT exactly as it was. That is a NO-WRITE, not a revoke.
	if inMultiStagePipeline(leaves, i) {
		return nil, false
	}
	if pc.Executable == "" {
		// The plain spelling: a command-less leaf carrying its own assignments.
		return pc.EnvVars, true
	}
	reads, isAssignmentBuiltin := assignmentBuiltinReads[pc.Executable]
	if !isAssignmentBuiltin {
		// A leaf with any OTHER executable carries PREFIX assignments (`WT=/x git
		// status`, or the `env WT=/x cmd` form cmdparse lifts into the same field): they
		// are scoped to that one command's environment and never reach the shell, so
		// they neither bind nor revoke. The match is on the EXACT executable rather than
		// its filepath.Base because lowerDecl emits the builtin's own bare word; a
		// path-spelled `/bin/declare` (which does not exist) would fail closed here.
		return nil, false
	}
	if pc.Executable == "export" {
		return pc.EnvVars, reads
	}
	return declWrites(pc, reads)
}

// declWrites reads a `declare`-family leaf's ARGUMENTS as assignments. The lowering
// keeps them there rather than in EnvVars (shellparse.go's lowerDecl rebuilds the
// builtin's own shape so a bare `export`/`export NAME` stays a read-only query), and
// pg2-ft2hl chose to read them HERE rather than to widen that lowering: lifting them
// into EnvVars would change what EVERY rule sees for a `declare` leaf, whereas this
// seam's output reaches only the callers that already asked for it.
//
// reads is the builtin's own permission from assignmentBuiltinReads; this function can
// only ever REVOKE it, never grant it.
func declWrites(pc ParsedCommand, reads bool) (writes []EnvAssignment, readValues bool) {
	readValues = reads
	// A PREFIX ASSIGNMENT on a `declare` leaf makes the builtin's own assignment
	// EPHEMERAL, so the value written down is NOT the one that survives the leaf.
	// Measured against bash 5.3.9 on 2026-08-13:
	//
	//	WT=/first; WT=/x declare WT=/y; echo "$WT"     # /first  — fully discarded
	//	WT=/first; OTHER=/x declare WT=/y; echo "$WT"  # /y      — a different name persists
	//	WT=/first; WT=/x export WT=/y; echo "$WT"      # /y      — `export` is a POSIX
	//	                                               #           SPECIAL builtin, so
	//	                                               #           assignments before it
	//	                                               #           persist; that is why the
	//	                                               #           export path is unaffected
	//
	// Which of the first two applies depends on whether the prefix names the SAME
	// variable, and getting that per-name distinction wrong binds a value bash threw
	// away. Refusing to read the whole leaf is fail-safe under BOTH: the same-name shape
	// loses a binding it could have kept, and the different-name shape loses one it never
	// had. The PREFIX names themselves are deliberately NOT revoked — being ephemeral is
	// exactly what makes an earlier binding of the same name still correct.
	if len(pc.EnvVars) > 0 {
		readValues = false
	}
	// PASS 1 — is this leaf readable AT ALL? Any single unreadable argument disqualifies
	// the whole leaf, because a flag applies to the names that follow it and this seam
	// does not model which.
	for _, arg := range pc.Args {
		if isDeclFlag(arg) || (!isEnvAssign(arg) && !isValidEnvName(arg)) {
			readValues = false
		}
	}
	// PASS 2 — collect the names written, now that readValues is known.
	for _, arg := range pc.Args {
		if isDeclFlag(arg) {
			continue
		}
		if isEnvAssign(arg) {
			writes = append(writes, newEnvAssignment(arg))
			continue
		}
		if !readValues {
			// The leaf is unreadable, so every name it so much as MENTIONS is revoked.
			// A flagged NAKED name is the case that needs it: `declare -i WT` re-reads
			// WT's existing value as ARITHMETIC and `declare -u WT` case-folds the next
			// assignment to it, so a value already bound here may no longer be the one
			// bash holds. The array-element forms (`m[a]=1`, `m[$k]=$(…)`) land here too
			// — isEnvAssign rejects their bracketed name — and revoking `m` is right for
			// the same reason.
			if name, ok := declMentionedName(arg); ok {
				writes = append(writes, EnvAssignment{Name: name, Raw: arg})
			}
			continue
		}
		// An UNFLAGGED naked name — `declare WT` — is a NO-OP for the value: bash
		// declares the name and leaves any existing value alone (`WT=/x; declare WT`
		// still expands to `/x`). So it must neither bind (an empty string would be a
		// wrong answer) nor revoke.
	}
	return writes, readValues
}

// isDeclFlag reports whether a `declare`-family argument is an OPTION rather than a
// name or an assignment. Both polarities count: bash's `+x` REMOVES the attribute `-x`
// adds, and `--` ends the option list.
//
// A flag is never read, because bash's attributes change what a value IS: `-i` makes
// the assignment an ARITHMETIC evaluation (`declare -i N=5+5` binds `10`, not `5+5`),
// `-l`/`-u` case-fold it, `-n` makes the name an ALIAS whose expansion is a DIFFERENT
// variable's value, `-a`/`-A` make it a list or a map whose `$NAME` is one element, and
// `-r` makes it readonly (assignmentBuiltinReads records why that is its own problem).
// Refusing the whole flagged form — including the harmless `-x`, `-g` and `--` — is the
// deliberate choice: an allowlist of safe flags would have to be re-audited against
// bash's attribute set on every read of this file, and the cost of refusing is one
// prompt on a spelling nobody writes at a hook boundary.
func isDeclFlag(arg string) bool {
	return strings.HasPrefix(arg, "-") || strings.HasPrefix(arg, "+")
}

// declMentionedName returns the shell variable an unreadable `declare`-family argument
// WRITES, so InCommandVars can revoke it. It is the identifier prefix: the whole of a
// naked `WT`, and the `m` of an array-element `m[a]=1`. Anything with no valid
// identifier prefix names nothing this seam could have bound, so there is nothing to
// revoke.
func declMentionedName(arg string) (string, bool) {
	end := strings.IndexAny(arg, "[=")
	if end < 0 {
		end = len(arg)
	}
	name := arg[:end]
	if !isValidEnvName(name) {
		return "", false
	}
	return strings.TrimSuffix(name, "+"), true
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
	// The bash ARRAY form `NAME=(a b)` binds a LIST, and `$NAME` expands to its FIRST
	// ELEMENT — never to the parenthesised text. Reading the text would be a confidently
	// WRONG value rather than a missing one, which is the exact failure this file exists
	// to avoid, so the shape is refused (and any earlier binding of the name revoked).
	// classifyExpansion cannot filter it: it parses the value in ASSIGNMENT POSITION
	// precisely so `(a b)` reads as an ArrayExpr rather than a subshell, but a list whose
	// elements hold no `$` and no backtick still classifies as ExpansionNone. The test is
	// on the UNSTRIPPED value, so a genuinely quoted `WT="(a b)"` — a scalar whose value
	// really is that text — keeps its binding.
	if strings.HasPrefix(ev.Value, "(") {
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
