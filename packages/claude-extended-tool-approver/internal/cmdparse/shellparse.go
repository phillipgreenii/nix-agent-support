package cmdparse

// THE SEAM (ADR 0039's Decision, item "One seam").
//
// This file is the ONLY file in this Go module permitted to import
// `mvdan.cc/sh/v3` — any package within it, not merely `.../syntax`. That is
// invariant I6, and `TestSeamIsTheOnlyParserImporter` in shellparse_test.go walks
// the module's import graph and fails if any other file imports it.
//
// It is a Facade over the parser (one entry point, ParseShell) and an Adapter from
// the parser's *syntax.File to CETA's own ParsedCommand (ADR 0039's Decision,
// item "Lower to the existing type").
//
// STATUS: this file is the CANDIDATE front end running in SHADOW. The outgoing
// front end — `StripCommentsPreservingHeredocs` then `Parse`, in that order — is
// still authoritative for every verdict. See shadow.go for the comparison and
// LOWERING.md in this directory for the per-construct coverage record.
//
// The lowering deliberately REUSES the outgoing post-processing that already
// operates on ParsedCommand rather than on text: `unwrapCommand` (and through it
// `unwrapExecPrefix`, `unwrapCommandRunner`, `liftAssignmentArgs`), `unquote`,
// `newEnvAssignment` and `isEnvAssign`. Reusing them is not a shortcut; it is what
// makes exact parity provable for the constructs they own. In particular `unquote`
// is applied to each word's EXACT SOURCE SLICE, so mixed quoting such as `a'b'c`
// keeps the outgoing's (stricter, non-expanding) reading rather than the parser's
// true literal expansion — which ADR 0039's Consequences names as the change that
// would newly clear the very predicate I4 exists to fence.

import (
	"strings"
	"sync"

	"mvdan.cc/sh/v3/syntax"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

// ShellParse is the seam's result. It is a FIRST-CLASS value rather than an
// absent result: `Unparseable` is the fail-safe parse floor of I1b, and a caller
// making a security decision MUST fold it to a non-approving verdict rather than
// read `Leaves` as an inventory (it is empty, not "no commands").
type ShellParse struct {
	// Leaves is the lowered leaf set. It is EMPTY when Unparseable is set.
	Leaves []ParsedCommand
	// Unparseable reports that the bash parser could not parse the command. Per
	// I1b the whole command MUST then fold to a non-approving verdict with NO leaf
	// examined, and per I10 CETA MUST NOT Approve it.
	Unparseable bool
	// Reason names the parse failure, for the deferring caller's reason string.
	Reason string
	// Dialect names the shell variants that DO support the construct that failed,
	// when the parser attributed the failure to a dialect (I10: the reason SHOULD
	// name the dialect where the parser attributes it, and MUST NOT guess where it
	// does not). Empty when the parser made no attribution.
	Dialect string
}

// parserPool reuses parsers across calls. A *syntax.Parser is reusable but not
// safe for concurrent use, so it is pooled rather than kept in a package
// variable: `go test` runs package tests in parallel goroutines and the hook
// itself may evaluate substitution bodies from more than one goroutine later.
var parserPool = sync.Pool{
	New: func() any {
		// BOTH options are part of ADR 0039's decision. Variant(LangBash) is what
		// makes a zsh-only construct a parse ERROR rather than a silent mis-parse, so
		// every dialect figure and the whole of I10 depend on it. KeepComments(true)
		// is what makes comment handling a parser FACT, which is the entire basis for
		// retiring the per-line comment pass by construction instead of replacing it.
		return syntax.NewParser(syntax.Variant(syntax.LangBash), syntax.KeepComments(true))
	},
}

// ParseShell parses command with the real bash parser and lowers the resulting
// AST to CETA's ParsedCommand leaves.
//
// It takes TEXT because the OUTERMOST input is text: the hook receives a command
// string and nothing upstream has parsed it (I7's permanent text entry point).
// Comments are NOT pre-stripped — under KeepComments they are parser facts and
// simply never appear in any CallExpr, so no comment pass runs here at all.
func ParseShell(command string) ShellParse {
	p, _ := parserPool.Get().(*syntax.Parser)
	file, err := p.Parse(strings.NewReader(command), "command")
	parserPool.Put(p)
	if err != nil {
		return unparseable(err)
	}
	lw := &lowering{src: command, pipeSeq: -1}
	lw.lowerStmtsFresh(file.Stmts)
	// Word-list and other DATA leaves are appended LAST, mirroring Parse: the
	// heredoc leftover net there attaches an unclaimed extent to the LAST leaf, and
	// a data leaf must not become that leaf. Leaf order is otherwise immaterial —
	// verdicts fold through MostRestrictive.
	return ShellParse{Leaves: append(lw.leaves, lw.dataLeaves...)}
}

// unparseable maps a parser error onto the I1b/I10 floor value, attributing the
// dialect only when the parser itself did.
func unparseable(err error) ShellParse {
	switch e := err.(type) {
	case syntax.LangError:
		var langs []string
		for _, l := range e.Langs {
			langs = append(langs, l.String())
		}
		return ShellParse{
			Unparseable: true,
			Reason:      "shell parse failed: " + e.Feature + " is not valid bash",
			Dialect:     strings.Join(langs, ","),
		}
	case syntax.ParseError:
		return ShellParse{Unparseable: true, Reason: "shell parse failed: " + e.Text}
	default:
		return ShellParse{Unparseable: true, Reason: "shell parse failed: " + err.Error()}
	}
}

// lowering carries the source string alongside the walk (I12: the seam MUST
// retain the source string, and every identity key MUST be derived from an exact
// source slice of it rather than produced by printing the AST).
type lowering struct {
	src string
	// leaves are the COMMAND leaves, in walk order.
	leaves []ParsedCommand
	// dataLeaves are command-less leaves whose only content is Raw text that may
	// hold a live substitution: a `for` word list, a `case` subject, an arithmetic
	// or test expression. They are DATA — never judged as a command — and carry
	// PipelineID -1 because they stand in no pipeline.
	dataLeaves []ParsedCommand
	pipeSeq    int
}

func (lw *lowering) nextPipelineID() int {
	lw.pipeSeq++
	return lw.pipeSeq
}

// slice returns the exact source slice a node spans, clamped to the source. Every
// identity key the lowering produces goes through here (I12).
func (lw *lowering) slice(from, to syntax.Pos) string {
	a, b := int(from.Offset()), int(to.Offset())
	if a < 0 || a > len(lw.src) || b < a {
		return ""
	}
	if b > len(lw.src) {
		b = len(lw.src)
	}
	return lw.src[a:b]
}

func (lw *lowering) node(n syntax.Node) string { return lw.slice(n.Pos(), n.End()) }

// stmtRaw is a leaf's Raw: the exact source slice spanning the owning statement,
// with the trailing separator the statement's extent includes (`;`, `&`, `|&`)
// and surrounding whitespace removed.
//
// The trim is why this is "derived from" an exact slice rather than one verbatim.
// It is deliberate: the outgoing front end's Raw never carried the separator, the
// atomicity contract the engine relies on (re-parsing a leaf's Raw must not reveal
// further commands) is unaffected by it, and DownstreamStages matches leaves by
// Raw EQUALITY against a re-parse of the root expression, so a separator present
// in one and absent in the other would silently break the pipeline relation.
func (lw *lowering) stmtRaw(st *syntax.Stmt) string {
	return strings.TrimRight(strings.TrimSpace(lw.node(st)), "&;| \t\n")
}

// lowerStmtsFresh lowers a top-level statement list, giving each statement its own
// pipeline.
func (lw *lowering) lowerStmtsFresh(stmts []*syntax.Stmt) {
	for _, st := range stmts {
		lw.lowerStmt(st, lw.nextPipelineID(), 0)
	}
}

// lowerStmtList lowers a COMPOUND BODY's statement list. The FIRST statement
// inherits (pid, idx) so a pipe INTO the compound relates to the stage that
// actually reads it; the rest get fresh pipelines.
//
// RESIDUE, carried over from the outgoing front end unchanged: a compound's LATER
// stages also read that stdin (`a | (b; c)` feeds c too) and are not related here,
// so a pipe into a multi-stage compound is an under-approximation. It is the
// outgoing answer for those stages, never a new one.
func (lw *lowering) lowerStmtList(stmts []*syntax.Stmt, pid, idx int) {
	for i, st := range stmts {
		if i == 0 {
			lw.lowerStmt(st, pid, idx)
			continue
		}
		lw.lowerStmt(st, lw.nextPipelineID(), 0)
	}
}

// flattenPipe collects the stages of a pipeline in SOURCE order. `a | b | c`
// parses left-nested as BinaryCmd(Pipe, BinaryCmd(Pipe, a, b), c), so the
// recursion yields a, b, c. A stage carrying its own redirections is a stage, not
// a nesting point, so the guard stops there.
//
// TERMINATION: the caller MUST descend into the BinaryCmd's X and Y rather than
// re-entering on the statement that owns it. A pipeline statement that also carried
// redirections would otherwise flatten to ITSELF — the redirs guard stops the
// descent — and lowering that single "stage" would re-enter this same branch
// forever. bash attaches a pipeline's redirections to its LAST STAGE, so the
// parser is not observed to produce that shape; a hook that runs on every tool call
// must not depend on an unobserved shape staying unobserved, and its fail-safe is
// Abstain (I1a/I1b), never a stack overflow.
func flattenPipe(st *syntax.Stmt, out *[]*syntax.Stmt) {
	if bc, ok := st.Cmd.(*syntax.BinaryCmd); ok && len(st.Redirs) == 0 &&
		(bc.Op == syntax.Pipe || bc.Op == syntax.PipeAll) {
		flattenPipe(bc.X, out)
		flattenPipe(bc.Y, out)
		return
	}
	*out = append(*out, st)
}

// lowerStmt lowers one statement. Every branch is a COVERAGE obligation: I14
// requires every *syntax.CallExpr in the parsed file, plus every statement
// carrying redirections or a heredoc, to be covered by at least one leaf source
// span — INCLUDING nodes in untaken branches, because CETA cannot know which
// branch runs and MUST judge every branch that could.
func (lw *lowering) lowerStmt(st *syntax.Stmt, pid, idx int) {
	switch cmd := st.Cmd.(type) {
	case nil:
		// A statement with no command at all — `> file` on its own. Its
		// redirections MUST still be evaluated, so it becomes a command-less leaf
		// exactly as the outgoing front end's redirection-only segment did.
		lw.emitRedirOnly(st, pid, idx)

	case *syntax.CallExpr:
		lw.lowerCall(st, cmd, pid, idx)

	case *syntax.BinaryCmd:
		switch cmd.Op {
		case syntax.Pipe, syntax.PipeAll:
			// Descend into X and Y, NOT into st — see flattenPipe's TERMINATION note.
			var stages []*syntax.Stmt
			flattenPipe(cmd.X, &stages)
			flattenPipe(cmd.Y, &stages)
			for i, stage := range stages {
				lw.lowerStmt(stage, pid, idx+i)
			}
			// A pipeline that itself carries redirections (only reachable when the
			// whole pipeline is a redirected compound) still owes its redirection leaf.
			lw.emitCompoundRedirs(st, pid, idx)
		default: // && and ||
			lw.lowerStmt(cmd.X, pid, idx)
			lw.lowerStmt(cmd.Y, lw.nextPipelineID(), 0)
			lw.emitCompoundRedirs(st, pid, idx)
		}

	case *syntax.Subshell:
		lw.lowerStmtList(cmd.Stmts, pid, idx)
		lw.emitCompoundRedirs(st, pid, idx)

	case *syntax.Block:
		lw.lowerStmtList(cmd.Stmts, pid, idx)
		lw.emitCompoundRedirs(st, pid, idx)

	case *syntax.IfClause:
		// Both branches are lowered. `if false; then rm -rf /; fi` executes nothing,
		// but executedness is a RUNTIME property and I14's binding form is the static
		// surrogate, so the untaken branch is judged too. That is the conservative
		// direction and the correct one.
		lw.lowerStmtList(cmd.Cond, pid, idx)
		lw.lowerStmtsFresh(cmd.Then)
		for els := cmd.Else; els != nil; els = els.Else {
			lw.lowerStmtsFresh(els.Cond)
			lw.lowerStmtsFresh(els.Then)
		}
		lw.emitCompoundRedirs(st, pid, idx)

	case *syntax.WhileClause:
		// The condition keeps the header's pipeline coordinates: in
		// `cat .git/config | while read l; do …; done` the `read` IS the stage
		// downstream of the cat, and losing that would lose the relation.
		lw.lowerStmtList(cmd.Cond, pid, idx)
		lw.lowerStmtsFresh(cmd.Do)
		lw.emitCompoundRedirs(st, pid, idx)

	case *syntax.ForClause:
		lw.lowerLoop(cmd.Loop)
		lw.lowerStmtList(cmd.Do, pid, idx)
		lw.emitCompoundRedirs(st, pid, idx)

	case *syntax.CaseClause:
		// The subject word is DATA — `case $(curl|sh) in` executes the substitution
		// but the word itself is never a command — so it gets the same command-less
		// data leaf a `for` word list gets.
		lw.emitData(cmd.Word)
		for _, item := range cmd.Items {
			for _, pat := range item.Patterns {
				lw.emitData(pat)
			}
			lw.lowerStmtsFresh(item.Stmts)
		}
		lw.emitCompoundRedirs(st, pid, idx)

	case *syntax.FuncDecl:
		// A function BODY is not executed at declaration time, but by I14's static
		// surrogate it is judged anyway: nothing here can know whether it is called.
		if cmd.Body != nil {
			lw.lowerStmt(cmd.Body, lw.nextPipelineID(), 0)
		}
		lw.emitCompoundRedirs(st, pid, idx)

	case *syntax.TimeClause:
		if cmd.Stmt != nil {
			lw.lowerStmt(cmd.Stmt, pid, idx)
		}
		lw.emitCompoundRedirs(st, pid, idx)

	case *syntax.CoprocClause:
		if cmd.Stmt != nil {
			lw.lowerStmt(cmd.Stmt, lw.nextPipelineID(), 0)
		}
		lw.emitCompoundRedirs(st, pid, idx)

	case *syntax.DeclClause:
		lw.lowerDecl(st, cmd, pid, idx)

	case *syntax.ArithmCmd, *syntax.TestClause, *syntax.LetClause:
		// `(( i++ ))`, `[[ -f $x ]]`, `let a=b`. None of these EXECUTES a command,
		// but each can embed a live `$( )`, so each reaches a data leaf whose Raw the
		// engine's substitution recursion walks. Judging them as commands (which the
		// outgoing front end did, yielding executables `[[` and `((`) is what this
		// deliberately stops.
		lw.emitDataSpan(lw.node(st.Cmd))
		lw.emitCompoundRedirs(st, pid, idx)

	default:
		// An unmodelled command type MUST NOT vanish (root cause 4: a pass may
		// DELETE a segment, so the leaf set stops being a cover). Emitting its source
		// span as a data leaf keeps I14's coverage while judging nothing.
		lw.emitDataSpan(lw.node(st.Cmd))
		lw.emitCompoundRedirs(st, pid, idx)
	}
}

// lowerLoop lowers a for/select loop's iteration clause.
func (lw *lowering) lowerLoop(loop syntax.Loop) {
	wi, ok := loop.(*syntax.WordIter)
	if !ok {
		// *CStyleLoop — `for ((i=0;i<10;i++))`. It has no word list, exactly as the
		// outgoing forWordList returned "" for the C-style header.
		return
	}
	if !wi.InPos.IsValid() || len(wi.Items) == 0 {
		// `for x; do …` iterates "$@" and has no word list either.
		return
	}
	// The word list reaches a leaf of its own (pg2-qkecz hole B). It carries ONLY
	// Raw: it is data, so it has no executable and must never be judged as a
	// command, but its text can hold a live `$( )` that genuinely executes.
	lw.emitDataSpan(lw.slice(wi.Items[0].Pos(), wi.Items[len(wi.Items)-1].End()))
}

// lowerDecl lowers an assignment builtin — `export`, `declare`, `local`,
// `readonly`, `typeset`, `nameref`.
//
// It rebuilds the outgoing shape exactly: Executable is the builtin's own name and
// each argument is its verbatim source slice, so `unwrapCommand` routes `export`
// through `liftAssignmentArgs` and the env-var guard sees `export VAR=VALUE`
// exactly like the leading `VAR=VALUE` form (pg2-gkd5e), while a bare
// `export`/`export NAME` stays a read-only query the safe-commands rule can
// approve.
func (lw *lowering) lowerDecl(st *syntax.Stmt, cmd *syntax.DeclClause, pid, idx int) {
	leaf := ParsedCommand{
		Executable:    cmd.Variant.Value,
		Raw:           lw.stmtRaw(st),
		PipelineID:    pid,
		PipelineIndex: idx,
	}
	for _, a := range cmd.Args {
		leaf.Args = append(leaf.Args, unquote(lw.assignRaw(a)))
	}
	lw.attachRedirs(st, &leaf)
	lw.leaves = append(lw.leaves, unwrapCommand(leaf))
}

// assignRaw returns an assignment's verbatim source text. A NAKED assignment
// (`export B`, `declare -x`) has no Name, so its extent is its Value's.
func (lw *lowering) assignRaw(a *syntax.Assign) string {
	if a.Name != nil {
		return lw.slice(a.Name.Pos(), a.End())
	}
	if a.Value != nil {
		return lw.node(a.Value)
	}
	return ""
}

// lowerCall lowers a simple command.
func (lw *lowering) lowerCall(st *syntax.Stmt, cmd *syntax.CallExpr, pid, idx int) {
	leaf := ParsedCommand{
		Raw:           lw.stmtRaw(st),
		PipelineID:    pid,
		PipelineIndex: idx,
	}
	// LEADING ASSIGNMENTS land in CallExpr.Assigns; the `env FOO=1 cmd` form lands
	// in Args and is consumed by unwrapExecPrefix below. The lowering MUST NOT
	// conflate them — that is pg2-gkd5e's position-independence invariant, and
	// TestShellParse_PositionIndependentAssignments pins both forms reaching the
	// same EnvVars.
	for _, a := range cmd.Assigns {
		raw := lw.assignRaw(a)
		if isEnvAssign(raw) {
			leaf.EnvVars = append(leaf.EnvVars, newEnvAssignment(raw))
			continue
		}
		// An assignment whose NAME is not a valid shell identifier — the INDEXED and
		// associative-array element forms, `BEAD_IDS[85591]="zr-8pl"` and
		// `m[$k]=$(curl|sh)` — cannot become an EnvAssignment, because isEnvAssign
		// (and the isValidEnvName guard behind it) deliberately rejects the bracket.
		// It MUST NOT therefore be dropped: its VALUE can hold a live substitution,
		// and a statement that is nothing but such assignments would otherwise lower
		// to no leaf at all. That is root cause 4 — a pass DELETING a segment — which
		// the corpus census caught in this lowering (5 rows, -71 leaves) before the
		// shadow comparison was believed.
		//
		// It reaches a DATA leaf, the same shape a `for` word list gets: no executable,
		// so it is never judged as a command, but its Raw is walked for substitutions.
		lw.emitDataSpan(raw)
	}
	for i, w := range cmd.Args {
		tok, procSubs := lw.wordToken(w)
		leaf.ProcessSubstitutions = append(leaf.ProcessSubstitutions, procSubs...)
		if i == 0 {
			leaf.Executable = tok
			continue
		}
		leaf.Args = append(leaf.Args, tok)
	}
	lw.attachRedirs(st, &leaf)
	if leaf.Executable == "" {
		// Assignment-only statement (`LD_PRELOAD=/evil.so && cmd`, or a whole command
		// that is nothing but assignments), or a redirection-only one. Keep it as a
		// command-less leaf carrying its EnvVars — the shape the engine's
		// command-less-leaf branch evaluates. Dropping it was a live auto-approve
		// BYPASS (pg2-mtnmb). There is no executable to unwrapCommand.
		if len(leaf.EnvVars) > 0 || len(leaf.Redirections) > 0 || leaf.HasHeredoc {
			lw.leaves = append(lw.leaves, leaf)
		}
		return
	}
	lw.leaves = append(lw.leaves, unwrapCommand(leaf))
}

// emitRedirOnly emits a command-less leaf for a statement that is nothing but
// redirections.
func (lw *lowering) emitRedirOnly(st *syntax.Stmt, pid, idx int) {
	leaf := ParsedCommand{Raw: lw.stmtRaw(st), PipelineID: pid, PipelineIndex: idx}
	lw.attachRedirs(st, &leaf)
	if len(leaf.Redirections) > 0 || leaf.HasHeredoc {
		lw.leaves = append(lw.leaves, leaf)
	}
}

// emitCompoundRedirs emits the redirection leaf a redirected COMPOUND owes.
//
// This is pg2-qkecz hole A, expressed structurally. The outgoing front end
// discarded a loop's terminator segment and its redirections with it, so
// `for f in a b; do echo hi; done > /etc/passwd` was APPROVED — evaluateRedirections
// never ran. In the AST those redirections sit on the compound's own *Stmt, so ONE
// uniform rule covers every compound that can carry them: the loop terminator's
// `done > /etc/passwd`, the subshell form `(cmd) > /etc/passwd`, a redirected
// `{ …; } > f`, `if …; fi > f`, and `case … esac > f` alike. There is no residue
// text-prefix match and no leftover net.
//
// Raw is the residue: the exact source slice from the first redirection operator to
// the end of the statement, which is the same text the outgoing doneResidue
// produced for the loop case.
func (lw *lowering) emitCompoundRedirs(st *syntax.Stmt, pid, idx int) {
	if len(st.Redirs) == 0 {
		return
	}
	leaf := ParsedCommand{
		Raw:           strings.TrimSpace(lw.slice(st.Redirs[0].OpPos, st.End())),
		PipelineID:    pid,
		PipelineIndex: idx,
	}
	lw.attachRedirs(st, &leaf)
	if len(leaf.Redirections) > 0 || leaf.HasHeredoc {
		lw.leaves = append(lw.leaves, leaf)
	}
}

// emitData records a command-less DATA leaf for a word whose text may hold a live
// substitution.
func (lw *lowering) emitData(w *syntax.Word) {
	if w == nil {
		return
	}
	lw.emitDataSpan(lw.node(w))
}

func (lw *lowering) emitDataSpan(raw string) {
	if strings.TrimSpace(raw) == "" {
		return
	}
	// PipelineID -1: a data leaf stands in no pipeline, so it must never be
	// reported as a stage (tc-vul7).
	lw.dataLeaves = append(lw.dataLeaves, ParsedCommand{Raw: raw, PipelineID: -1, PipelineIndex: -1})
}

// wordToken lowers one *syntax.Word to the token text the outgoing tokenize
// produced, plus any process-substitution bodies lifted out of it.
//
// The token is `unquote` applied to the word's EXACT SOURCE SLICE. That is what
// buys exact unquote parity: the outgoing unquote strips quoting only when the
// WHOLE token is wrapped, so `a'b'c` survives verbatim, and a true literal
// expansion — which would yield `abc` — is deliberately NOT used (ADR 0039's
// Consequences: it is stricter than the outgoing unquoting and would newly clear
// the predicate I4 exists to fence).
//
// A process substitution is replaced by the fabricated `/dev/fd/63` operand and
// its body is lifted, matching tokenize exactly. Both halves are load-bearing:
// emitting the substitution's source text instead causes mass new abstains from
// the redirect-target check, while emitting nothing loses the operand.
func (lw *lowering) wordToken(w *syntax.Word) (string, []string) {
	if !hasProcSubst(w) {
		return unquote(lw.node(w)), nil
	}
	var b strings.Builder
	var procSubs []string
	for _, part := range w.Parts {
		ps, ok := part.(*syntax.ProcSubst)
		if !ok {
			b.WriteString(lw.node(part))
			continue
		}
		// The body is the text BETWEEN the operator and the closing paren, verbatim,
		// exactly as tokenize's `s[start:j-1]`.
		procSubs = append(procSubs, lw.slice(bodyStart(ps), ps.Rparen))
		b.WriteString("/dev/fd/63")
	}
	return unquote(b.String()), procSubs
}

// bodyStart is the position just past a process substitution's two-byte opening
// operator (`<(`, `>(`, `=(`).
func bodyStart(ps *syntax.ProcSubst) syntax.Pos {
	return syntax.NewPos(ps.OpPos.Offset()+2, ps.OpPos.Line(), ps.OpPos.Col()+2)
}

func hasProcSubst(w *syntax.Word) bool {
	for _, part := range w.Parts {
		if _, ok := part.(*syntax.ProcSubst); ok {
			return true
		}
	}
	return false
}

// attachRedirs lowers a statement's redirections onto a leaf.
func (lw *lowering) attachRedirs(st *syntax.Stmt, leaf *ParsedCommand) {
	for _, r := range st.Redirs {
		lw.attachRedir(r, leaf)
	}
}

func (lw *lowering) attachRedir(r *syntax.Redirect, leaf *ParsedCommand) {
	switch r.Op {
	case syntax.Hdoc, syntax.DashHdoc:
		// HasHeredoc keys off the OPERATOR, never off a non-empty body: keying it off
		// the body would drop the Abstain floor for `<<EOF` with an empty body and,
		// below, for every herestring (ADR 0039's Consequences).
		leaf.HasHeredoc = true
		leaf.Heredocs = append(leaf.Heredocs, lw.heredoc(r))
		return
	case syntax.WordHdoc:
		// A herestring `<<<word` carries its word inline and has no body, so it
		// records NO extent — but I2 requires the heredoc floor to keep firing for
		// every heredoc-OR-HERESTRING-bearing leaf, so HasHeredoc is still set. This
		// matches the outgoing extractRedirections exactly.
		leaf.HasHeredoc = true
		return
	case syntax.DplIn, syntax.DplOut:
		// `N>&M` DUPLICATES a descriptor and `N>&-` CLOSES one; `<&M` duplicates for
		// input. None names a path and none creates a file, so all are dropped rather
		// than recorded — this is the branch that keeps `2>&1` off the write path.
		if r.Op == syntax.DplOut && r.Word != nil {
			t := unquote(lw.node(r.Word))
			if t != "-" && !isAllDigits(t) {
				break // `>& FILE` is bash's both-streams form: a real write.
			}
		}
		return
	}
	if r.Word == nil {
		return // dangling operator with nothing after it — nothing to path-check
	}
	target := unquote(lw.node(r.Word))
	if target == "" {
		return
	}
	fd := ""
	if r.N != nil {
		fd = r.N.Value
	}
	core := redirCore(r.Op)
	if core == "" {
		return
	}
	leaf.Redirections = append(leaf.Redirections, hookio.Redirection{
		Operator: fd + core,
		Path:     target,
		Kind:     redirectionKind(fd, core),
	})
}

// redirCore maps a parser redirection operator onto the operator TEXT the
// outgoing redirectionCore produced, so hookio.Redirection.Operator and the Kind
// derived from it are unchanged.
func redirCore(op syntax.RedirOperator) string {
	switch op {
	case syntax.RdrOut:
		return ">"
	case syntax.AppOut:
		return ">>"
	case syntax.RdrIn:
		return "<"
	case syntax.RdrInOut:
		return "<>"
	case syntax.RdrClob:
		return ">|"
	case syntax.DplOut:
		return ">&"
	case syntax.RdrAll:
		return "&>"
	case syntax.AppAll:
		return "&>>"
	}
	return ""
}

// heredoc lowers a `<<`/`<<-` redirection to a Heredoc extent.
//
// Terminated is always true: an unterminated heredoc is a PARSE FAILURE, so it
// never reaches the lowering at all — it folds to the I1b floor instead of
// swallowing the rest of the input as body.
func (lw *lowering) heredoc(r *syntax.Redirect) Heredoc {
	hd := Heredoc{StripTabs: r.Op == syntax.DashHdoc, Terminated: true}
	hd.Delimiter, hd.Quoted = lw.delimiter(r.Word)
	hd.Body = lw.heredocBody(r)
	return hd
}

// delimiter returns a heredoc delimiter word's text with its quoting REMOVED, and
// whether ANY part of the word carried quoting.
//
// The quoting discriminator is not cosmetic and MUST survive (I3): identical bytes
// under `<<EOF` deny while `<<'EOF'` abstains, because only an unquoted delimiter
// makes the body expand. Any quoting of any part — `<<'EOF'`, `<<"EOF"`, `<<\EOF`,
// `<<'E'OF` — makes the whole body literal.
func (lw *lowering) delimiter(w *syntax.Word) (string, bool) {
	if w == nil {
		return "", false
	}
	var b strings.Builder
	quoted := false
	for _, part := range w.Parts {
		switch p := part.(type) {
		case *syntax.Lit:
			// A BACKSLASH survives into Lit.Value for a delimiter word (`<<\EOF` has
			// Value `\EOF`, `<<E\OF` has `E\OF`), so it must be removed here rather
			// than detected by a slice/value mismatch. It is also the quoting signal:
			// ANY escaping of ANY part of the delimiter makes the whole body literal,
			// exactly as the outgoing parseHeredocOperator recorded it.
			for i := 0; i < len(p.Value); i++ {
				if p.Value[i] == '\\' && i+1 < len(p.Value) {
					quoted = true
					i++
					b.WriteByte(p.Value[i])
					continue
				}
				b.WriteByte(p.Value[i])
			}
		case *syntax.SglQuoted:
			quoted = true
			b.WriteString(p.Value)
		case *syntax.DblQuoted:
			quoted = true
			for _, inner := range p.Parts {
				if lit, ok := inner.(*syntax.Lit); ok {
					b.WriteString(lit.Value)
				}
			}
		default:
			b.WriteString(lw.node(part))
		}
	}
	return b.String(), quoted
}

// heredocBody returns the body text verbatim, the TERMINATOR LINE EXCLUDED.
//
// The AST's Hdoc extent runs to the END of the terminator line, so the terminator
// is trimmed by cutting at the last newline in the span. That reproduces the
// outgoing readHeredocBody byte for byte, including the `<<-` case: the outgoing
// returned `s[from:lineStart]`, which keeps every body line's trailing newline and
// stops before the (possibly tab-indented) terminator.
func (lw *lowering) heredocBody(r *syntax.Redirect) string {
	if r.Hdoc == nil {
		return ""
	}
	span := lw.node(r.Hdoc)
	nl := strings.LastIndexByte(span, '\n')
	if nl < 0 {
		return "" // the terminator line is the only line: the body is empty
	}
	return span[:nl+1]
}

// NormalizeCommandShell is NormalizeCommand computed through the seam.
//
// It exists because NormalizeCommand is the persisted grouping key for the
// hook-miss taxonomy, so ANY leaf-set change re-keys historical analysis buckets
// (ADR 0039's Consequences). Keeping both spellings lets that re-keying be
// measured before it is adopted rather than discovered afterwards. An unparseable
// command falls back to the trimmed source, which is what NormalizeCommand
// already does for a command that yields no executable leaf.
func NormalizeCommandShell(command, projectRoot, cwd string) string {
	sp := ParseShell(command)
	if sp.Unparseable {
		return strings.TrimSpace(command)
	}
	parts := make([]string, 0, len(sp.Leaves))
	for _, lc := range sp.Leaves {
		if lc.Executable == "" {
			continue
		}
		exec := NormalizeExecutable(lc.Executable, projectRoot, cwd)
		if len(lc.Args) > 0 {
			parts = append(parts, exec+" "+strings.Join(lc.Args, " "))
		} else {
			parts = append(parts, exec)
		}
	}
	if len(parts) == 0 {
		return strings.TrimSpace(command)
	}
	return strings.Join(parts, " && ")
}
