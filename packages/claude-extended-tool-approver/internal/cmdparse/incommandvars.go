package cmdparse

import (
	"path/filepath"
	"strings"
	"sync"
)

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
// SUBSHELL SCOPING (pg2-4ak2k, closing the residual pg2-wq3ki recorded here). A
// SUBSHELL scopes its assignments — bash forks a child for `( … )`, and a child can
// never write its parent's variables — so `( WT=/x ); git -C "$WT" …` leaves `$WT`
// EMPTY once the subshell closes, even though an assignment with that exact text ran.
// A leaf now carries that scoping as SubshellScope (parser.go), the chain of subshell
// IDs — outermost to innermost — enclosing it; shellparse.go's `case *syntax.Subshell`
// pushes a fresh ID onto the walk's scope path before lowering the body and pops it
// back off after, so every leaf lowered from inside the subshell carries one extra
// path segment that leaves lowered before or after it do not.
//
// The VISIBILITY RULE this file applies (scopeVisible): leaf i's write is visible to
// leaf `before` iff leaf i's SubshellScope is a PREFIX of (or equal to) leaf
// `before`'s — i.e. leaf i's subshell, if any, is STILL OPEN at leaf `before`'s
// position. That covers all three shapes:
//
//   - `(WT=/x && cd "$WT" && git commit)` — assignment and consumption share the SAME
//     scope path (equal, the trivial "prefix"), so the write is visible, exactly as
//     before this bead.
//   - `WT=/x; (git -C "$WT" commit)` — the assignment's EMPTY (top-level) path is a
//     prefix of the consuming leaf's one-element path: an enclosing scope that is
//     still open, so the write is visible, exactly as before this bead.
//   - `( WT=/x ) ; git -C "$WT" commit` — the assignment's one-element path is NOT a
//     prefix of the consuming leaf's EMPTY path (it is longer): the subshell already
//     CLOSED by the time the consuming leaf runs, so the write is invisible —
//     COMPLETELY invisible, not merely un-bound: it must not even revoke an outer
//     binding of the same name that `before` should still see (see InCommandVars'
//     loop, which `continue`s past such a write before shellVarWrites is even
//     called). The same non-prefix test also catches SIBLING subshells at the same
//     nesting depth, which a bare depth counter could not distinguish from "the same
//     subshell" — the reason a counter was rejected as the fix.
//
// A PIPELINE stage remains its own, already-solved case and is untouched by any of
// this: the leaf already carries the pipeline coordinates that identify one
// (inMultiStagePipeline), a pipeline stage's subshell never writes the parent shell
// AT ALL (a no-write, not a revoke — shellVarWrites' own comment), and that check runs
// before the scope-visibility test ever sees such a leaf.

// OverlayVars merges `local` (the NEARER scope) onto `base` (the FARTHER one), local
// winning on a name both define. It is the one merge rule every InCommandVars/
// InCommandTempDirVars overlay in this tree uses — originally written twice, byte for
// byte, as primarycommit.LeafVars and primarycommit.LeafTempDirVars (pg2-eqacu,
// pg2-d71my), and pulled up here (bead tc-5h6e) so the engine's own
// substitution-recursion overlay (internal/engine/engine.go's evaluateParsed) is a
// THIRD caller of the identical rule rather than a third hand-rolled copy. Neither map
// is mutated: a fresh map is allocated for the merge, so a caller holding `base` (an
// outer scope another leaf may still read) never observes a nearer leaf's write.
//
// Returns base unchanged when local is empty (the ordinary case: nothing nearer to
// overlay), and local unchanged when base is empty — both zero-allocation fast paths a
// leaf with no enclosing scope or no local assignments takes on every call.
func OverlayVars(base, local map[string]string) map[string]string {
	if len(local) == 0 {
		return base
	}
	if len(base) == 0 {
		return local
	}
	merged := make(map[string]string, len(base)+len(local))
	for k, v := range base {
		merged[k] = v
	}
	for k, v := range local { // the nearer assignment wins
		merged[k] = v
	}
	return merged
}

// InCommandVars returns the shell variables that the leaves BEFORE index `before`
// establish for the rest of the expression, mapped to their LITERAL values. nil when
// nothing qualifies, which is the ordinary case — and the case that leaves every
// caller's verdict exactly as it was.
//
// `leaves` MUST be a single Parse call's output, in source order (the pipeline and
// subshell-scope coordinates it consults are per-call). `before` is the index of the
// leaf about to be judged: it is EXCLUSIVE, which is what keeps a leaf's own prefix
// assignments out of its own expansions.
func InCommandVars(leaves []ParsedCommand, before int) map[string]string {
	if before > len(leaves) {
		before = len(leaves)
	}
	// targetScope is leaf `before`'s own subshell scope path, against which every
	// earlier write is tested (see this file's SUBSHELL SCOPING comment). `before`
	// is a valid index at every real call site (InCommandVars' own doc comment), but
	// the defensive clamp above can still leave it equal to len(leaves) for an
	// out-of-range caller — there is no leaf to read a path from then, so
	// knownScope is false and every write is treated as visible, exactly the
	// pre-pg2-4ak2k behaviour for that degenerate, unexercised case (a
	// prefix-of-everything fallback, the conservative direction).
	var targetScope []int
	knownScope := before >= 0 && before < len(leaves)
	if knownScope {
		targetScope = leaves[before].SubshellScope
	}
	var vars map[string]string
	for i := 0; i < before; i++ {
		if knownScope && !scopeVisible(leaves[i].SubshellScope, targetScope) {
			// The writer's subshell has already CLOSED by the time leaf `before` runs
			// (its scope path is longer than, and not a prefix of, `before`'s), or the
			// two are SIBLING subshells (the paths diverge). Either way bash never
			// applied this write to the scope leaf `before` runs in, so it is
			// COMPLETELY INVISIBLE here — not even a revoke: it must leave an outer
			// binding of the same name exactly as an earlier, visible leaf left it.
			continue
		}
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

// scopeVisible reports whether a write from subshell scope `writer` is visible to a
// leaf whose own scope is `at` — i.e. whether writer is a PREFIX of, or equal to, at.
// That is exactly "writer's subshell (if any) is still open at at's position": every
// element writer names is an enclosing `( … )` that has not yet closed by the time a
// leaf at `at` runs.
//
//   - len(writer) > len(at): writer nests MORE subshells than at does, so at least
//     the innermost of writer's has already closed (control returned to an
//     enclosing scope) before at runs — invisible.
//   - the two share every index up to len(writer): writer names the same chain of
//     subshells as (a prefix of) at's — visible.
//   - they diverge at some index < len(writer): SIBLING subshells — same or
//     different depth, but different identities — never share scope — invisible.
//
// A bare depth comparison (len(writer) <= len(at)) cannot make this distinction: two
// subshells at the same depth can still be siblings, which is exactly the case a
// counter was rejected for (this bead's own acceptance criteria).
func scopeVisible(writer, at []int) bool {
	if len(writer) > len(at) {
		return false
	}
	for i, id := range writer {
		if at[i] != id {
			return false
		}
	}
	return true
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
//     still cannot see: pg2-4ak2k gave a leaf SUBSHELL scope (SubshellScope, this
//     file's SUBSHELL SCOPING comment), but that is a DIFFERENT scope from a
//     function body's, and `local`'s still has no representation on a leaf at all.
//     To implement it later, a leaf would first have to carry the FUNCTION BODY it
//     was lowered from — the same KIND of scope-path change pg2-4ak2k made for
//     subshells, but for function bodies instead, and still its own separate,
//     still-open bead.
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
	//
	// TRUE HERE ONLY MEANS "this narrow in-command-resolution seam may read the
	// value" — it MUST NOT be read as "declare/typeset are safe commands", and it
	// is deliberately NOT enough to add them to internal/rules/safecmds' alwaysSafe
	// allowlist (pg2-c2non, decision: DECLINE, permanently for now, doc comment at
	// that allowlist's `export` entry has the full reasoning and the measurement).
	// The short version: this seam reads a decl leaf's assignments out of its Args
	// (declWrites, below), because the lowering that would LIFT them into the
	// leaf's EnvVars field was deliberately not widened — so
	// internal/rules/envvars' guard, which only ever inspects EnvVars, cannot see
	// a `declare -x LD_PRELOAD=/tmp/evil` leaf's assignment at all (MEASURED:
	// abstain, not reject, on this tree). Safe-listing declare/typeset in
	// safecmds before that lowering lands would turn that abstain into an
	// auto-approve — a live env-var-guard bypass, the same class pg2-a12rl and
	// pg2-6c85x closed for git.
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
// derivable by stripping alone, which is the same reasoning this file's sibling
// `LiteralAssignmentValueText` (shellparse.go) — the structural replacement for
// `internal/rules/envvars/envvars.go`'s former `literalValue` (pg2-30wro) — now
// derives from the parse tree instead of scanning characters. This function is a
// separate, deliberately unmigrated instance: it resolves a DIFFERENT question (an
// EARLIER leaf's binding for InCommandVars) and was not in that bead's scope.
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

// PlainVarRefWhole reports whether token is EXACTLY ONE `$NAME`/`${NAME}` parameter
// reference — the WHOLE string, with no literal prefix or suffix glued alongside it —
// returning NAME. It is plainVarRef's contract (only the two forms whose value IS the
// variable and nothing else) applied to a whole token instead of a scan position, for
// a caller that has already isolated ONE path component (dirresolve.go's
// unresolvableToken, whose own doc names token as the offending component "as
// written") and needs to know whether that entire component is a bare variable
// reference before treating the variable's BINDING — rather than its (unknowable)
// literal value — as safety-relevant (primarycommit's fresh-temp-dir recognition,
// pg2-70g51). A glued literal (`${d}extra`, `x$d`) reports ok=false: `x$d` is not
// even a reference to "d" at all (bash reads the longest valid identifier, so it
// names variable "dx"), and `${d}extra` genuinely does reference "d" but with a
// literal suffix this function's caller must not silently ignore.
func PlainVarRefWhole(token string) (name string, ok bool) {
	if token == "" || token[0] != '$' {
		return "", false
	}
	name, end, ok := plainVarRef(token, 0)
	if !ok || end != len(token) {
		return "", false
	}
	return name, true
}

func isVarNameByte(c byte, first bool) bool {
	if c == '_' || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') {
		return true
	}
	return !first && c >= '0' && c <= '9'
}

// FRESH TEMP DIR RESOLUTION (pg2-d71my)
//
// A HOME REPLACEMENT is ordinarily indistinguishable from a PATH-hijack-shaped
// hazard (envvars' askVars doc comment) — dropping the caller's HOME for a
// value nobody vouches for is exactly what an attacker's replacement would also
// look like. `mktemp -d`'s output is the one replacement value that IS
// distinguishable: it names a freshly created, session-unique directory that
// did not exist before this command ran, so nothing — attacker or otherwise —
// could have pre-staged content there in advance. OPERATOR RULING 2026-08-17
// (via `/unblock-human-beads`, decided together with pg2-qhhil): this is
// authorized relief, narrower than preservesCallerValue's EXTEND shape and
// gated on recognizing the SAME shape this file's InCommandVars already reads
// for — a value established by THIS command's own earlier text, not by
// anything ambient.
//
// InCommandTempDirVars is InCommandVars' sibling for this marker rather than
// for a literal value, and deliberately reuses shellVarWrites/scopeVisible
// rather than re-deriving the leaf-order / assignment-builtin /
// subshell-scoping rules those encode (InCommandVars' own doc comment records
// why each rule exists). `T=$(mktemp -d)` is exactly the shape
// literalAssignedValue refuses — a command substitution is never literal — so
// InCommandVars itself never binds T; a caller wanting "is this name grounded
// in a directory nothing could have pre-staged" needs this scan instead.

// isMktempDirSubstitution reports whether body — a substitution BODY as
// EnumerateSubstitutions returns it, e.g. "mktemp -d" — is a `mktemp`
// invocation carrying a directory-creating flag. Plain `mktemp` (no flag)
// creates a FILE, not a directory, and is deliberately excluded: a file path
// is not a hermetic HOME. The executable is matched by filepath.Base, the same
// convention assignmentIsWholeLeaf and this package's other executable
// switches use, so a path-spelled `/usr/bin/mktemp` still matches.
func isMktempDirSubstitution(body string) bool {
	leaves := Parse(body)
	if len(leaves) != 1 {
		return false
	}
	leaf := leaves[0]
	if filepath.Base(leaf.Executable) != "mktemp" {
		return false
	}
	for _, a := range leaf.Args {
		if a == "-d" || a == "--directory" {
			return true
		}
	}
	return false
}

// IsFreshTempDirAssignment reports whether ev's VALUE is DIRECTLY, and ONLY,
// the output of a `mktemp -d` / `mktemp --directory` command substitution —
// `$(mktemp -d)` or “ `mktemp -d` “ — with no literal prefix or suffix
// alongside it. That is the narrow shape a variable InCommandTempDirVars marks
// must itself satisfy, and it is also the shape an assignment's OWN value may
// satisfy directly (`HOME=$(mktemp -d)`), so it is exported for both call
// sites — internal/rules/envvars uses it for the latter.
//
// A trailing literal suffix (`HOME="$T/h"`) is a DIFFERENT, one-level-removed
// shape — a variable REFERENCES a tempdir and the suffix is inert literal text
// — and is composed by the caller via ExpandInCommand against
// InCommandTempDirVars' result, not by widening this predicate to parse a
// prefix/suffix split itself.
//
// MEMOIZED (I7, pg2-x9452, ADR 0039 step 5's final bead). InCommandTempDirVars
// calls this once per (leaf, EARLIER leaf) pair — O(n^2) calls for an
// n-leaf expression, since engine.go's own loop re-derives the "vars visible
// so far" set from scratch for EVERY leaf rather than threading it forward —
// and this function is a PURE function of ev.Expansion/ev.Value, so the SAME
// earlier assignment gets this predicate, and the cmdparse.Parse call inside
// isMktempDirSubstitution, recomputed once per LATER leaf in the same
// expression. Measured on a real corpus snapshot (2026-08-21): commands with
// several leaves after a `$(mktemp -d)`-shaped assignment (or after any
// OTHER ExpansionSafeCmd value; a plain `TARGET=$(readlink f)` pays the exact
// same cost to be told no) reparsed the identical substitution body up to
// 23 times in one hook evaluation. The cache below is keyed on the two
// fields this function actually reads, is safe for concurrent use (the hook
// may evaluate substitution bodies from more than one goroutine, same
// reasoning as shellparse.go's parserPool), and is unbounded only for the
// lifetime of one process — this binary is a short-lived per-hook-call CLI,
// never a long-running daemon, so that is not a growth concern; a replay
// harness driving 150k+ distinct rows through one process still bounds the
// cache by the number of DISTINCT (Expansion, Value) pairs ever seen, not by
// row count.
func IsFreshTempDirAssignment(ev EnvAssignment) bool {
	key := freshTempDirCacheKey{expansion: ev.Expansion, value: ev.Value}
	if v, ok := freshTempDirCache.Load(key); ok {
		return v.(bool)
	}
	result := computeIsFreshTempDirAssignment(ev)
	freshTempDirCache.Store(key, result)
	return result
}

type freshTempDirCacheKey struct {
	expansion ExpansionKind
	value     string
}

var freshTempDirCache sync.Map // freshTempDirCacheKey -> bool

func computeIsFreshTempDirAssignment(ev EnvAssignment) bool {
	if ev.Expansion != ExpansionSafeCmd {
		return false
	}
	value := ev.Value
	if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[len(value)-1] == value[0] {
		value = value[1 : len(value)-1]
	}
	if strings.ContainsAny(value, "\"'\\") {
		return false
	}
	subs := EnumerateSubstitutions(value)
	if len(subs) != 1 || !subs[0].IsCommandSubstitution() {
		return false
	}
	var wrapped string
	switch subs[0].Kind {
	case SubstCommand:
		wrapped = "$(" + subs[0].Body + ")"
	case SubstBacktick:
		wrapped = "`" + subs[0].Body + "`"
	default:
		return false
	}
	if value != wrapped {
		// A literal prefix/suffix survived alongside the substitution — not the
		// narrow "value is nothing but the mktemp call" shape this predicate
		// covers.
		return false
	}
	return isMktempDirSubstitution(subs[0].Body)
}

// InCommandTempDirVars returns the shell variables that leaves before index
// `before` bind, in THIS SAME expression, to IsFreshTempDirAssignment's shape
// — mapped to the empty-string SENTINEL value ExpandInCommand needs to treat
// the name as literal-and-known without asserting any particular literal text
// (the whole point of `mktemp -d` is that nothing here can know what path it
// produced). `leaves`/`before` follow InCommandVars' own contract exactly: a
// single Parse call's leaves, in source order, `before` exclusive. nil when
// nothing qualifies.
func InCommandTempDirVars(leaves []ParsedCommand, before int) map[string]string {
	if before > len(leaves) {
		before = len(leaves)
	}
	var targetScope []int
	knownScope := before >= 0 && before < len(leaves)
	if knownScope {
		targetScope = leaves[before].SubshellScope
	}
	var vars map[string]string
	for i := 0; i < before; i++ {
		if knownScope && !scopeVisible(leaves[i].SubshellScope, targetScope) {
			continue
		}
		writes, readValues := shellVarWrites(leaves, i)
		for _, ev := range writes {
			if ev.Name == "" {
				continue
			}
			if readValues && IsFreshTempDirAssignment(ev) {
				if vars == nil {
					vars = map[string]string{}
				}
				vars[ev.Name] = ""
				continue
			}
			// REVOCATION, mirroring InCommandVars: a name this loop already marked
			// must not keep that marker after the command reassigns it to something
			// that is not itself a fresh temp dir. `delete` on a nil map is a no-op.
			delete(vars, ev.Name)
		}
	}
	return vars
}
