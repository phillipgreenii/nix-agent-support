package hookio

import (
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
)

// This file holds the "environment a child will see" model that the git rule
// introduced for pg2-xjt1s and the pg-ccaudit rule reuses (pg2-tqles). It was
// moved here verbatim from internal/rules/git so ONE copy answers "which
// assignments does this leaf's child inherit", rather than a second hand-rolled
// scan that drifts. The doc below still speaks in terms of git because git was
// the first consumer and every measurement was taken against it; the model is
// not git-specific.

// VisibleEnvVars returns the env assignments GIT WILL SEE on leaf `before` of this
// expression: that leaf's own prefix, plus every name an EARLIER leaf EXPORTS.
//
// # THE GAP IT CLOSES (pg2-xjt1s)
//
// All three of this file's env screens read ONE LEAF's own prefix, so the inline spelling
// was screened and the PERSISTENT spelling — the one that outlives the command — was not.
// Measured through the real binary on main @ a064a73e, 2026-08-14
// (scripts/probe-pg2-xjt1s.sh):
//
//	GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=core.fsmonitor GIT_CONFIG_VALUE_0=/tmp/evil git status  -> {}
//	export GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=core.fsmonitor GIT_CONFIG_VALUE_0=/tmp/evil; git status -> allow
//	GIT_PAGER=/tmp/evil git log     -> {}        export GIT_PAGER=/tmp/evil; git log     -> allow
//	GIT_DIR=/other git commit -m x  -> ask       export GIT_DIR=/other; git commit -m x  -> allow
//
// The export form is not the rare one: 111 of the 183 GIT_CONFIG-ASSIGNMENT-bearing Bash
// rows in the 154-day ask log spell it with `export` (read-only replay over an APFS clone,
// 2026-08-14). The bead recorded 97 of 179 on a narrower filter; the ratio is the finding
// and it held.
//
// # THE APPROACH RULING, WHICH THE BEAD REQUIRES BE RECORDED
//
// The two candidates were CROSS-LEAF ORDERING ANALYSIS IN THIS RULE and A NAME SCREEN IN
// THE ENV-VAR RULE. This is the first, for three reasons:
//
//  1. THE ASSIGNMENT LEAF IS NOT THE HAZARD; THE CONJUNCTION IS. `export GIT_PAGER=less`
//     on its own is ordinary, and so is `git log`. A rule that judges the assignment leaf
//     alone must either refuse a harmless export or approve it and lose the fact by the
//     time the git leaf is judged. Only the rule that judges the GIT invocation can see
//     both halves, so this is where the fact has to be read.
//  2. ONE TABLE PER HAZARD. gitProgramEnvVars, the `GIT_CONFIG_` prefix and the redirect
//     pair are the single sources of truth for what is screened, and several landed tests
//     ITERATE them (TestGit_ConfigEnvInjection_EveryGatedKeyIsScreened,
//     TestGit_ProgramEnvVar_TwinIsAConfigSinkInTheRealTable). A second name list in the
//     env-var rule would be a copy that drifts — the exact defect pg2-a12rl, pg2-6c85x
//     and pg2-h1ori each closed one instance of.
//  3. THE VERDICT WOULD BE WRONG THERE. These screens are DEMOTIONS OF AN Approve on the
//     git leaf; the env-var rule's vocabulary for a dangerous name is a decisive Ask or
//     Reject on the assignment. Moving the screen would move the verdict LEVEL too,
//     which no ruling supports.
//
// It does NOT use HookInput.InCommandVars, and that is deliberate rather than an
// oversight: that map holds only values cmdparse could read LITERALLY and DELETES a name
// whose value it cannot (see cmdparse.InCommandVars' revocation). `export
// GIT_CONFIG_KEY_0=$K` would therefore be ABSENT from it, and absent reads as "no
// assignment" — failing OPEN on exactly the shape a value-blind screen must catch. This
// seam wants NAMES, so it reads the leaves.
//
// # WHICH SPELLINGS REACH GIT'S ENVIRONMENT, MEASURED RATHER THAN ASSUMED
//
// The screens are about the environment git INHERITS, so a spelling that never leaves the
// shell is not a hazard and screening it would be a false prompt. bash 5.3.9, 2026-08-14,
// reading the variable in a real child process:
//
//	SPELLING                                REACHES THE CHILD?
//	NAME=v; child                           no   — a shell variable is not an env variable
//	NAME=v && child                         no   — same
//	export NAME=v; child                    YES
//	NAME=v; export NAME; child              YES  — the bare form exports an EXISTING value
//	declare NAME=v; child                   no   — declare does not export
//	declare -x / typeset -x / -gx NAME=v    YES
//	set -a; NAME=v; child                   YES  — allexport turns every later assignment into an export
//	export NAME=v | cat; child              no   — a pipeline stage is a subshell
//	(export NAME=v); child                  no   — a subshell scopes it
//	export NAME=v; unset NAME; child        no   — and export -n likewise un-exports
//
// So the plain `NAME=v;` spelling is NOT screened — that would be a false prompt on
// ordinary traffic — UNLESS an earlier leaf turned on `allexport`, which is a visible
// `set -a` leaf and is modelled. That keeps the fail-closed property without paying for it
// on the common shape.
//
// # THE OVER-APPROXIMATIONS, ALL TOWARD THE PROMPT AND ALL DELIBERATE
//
//  1. A `declare`/`typeset` leaf is read WHATEVER ITS FLAGS SAY, so the unexported
//     `declare NAME=v` is screened too. Deciding the net export state per name means
//     modelling clustered `-gx`, the un-exporting `+x`, and function scope — the same
//     modelling cmdparse.InCommandVars' own SCOPE comment declines for `declare` — and
//     getting it wrong fails OPEN. One prompt on a spelling nobody writes at a hook
//     boundary is the cheaper error.
//  2. VALUES FROM A `declare`-FAMILY LEAF ARE TREATED AS UNKNOWN, never read, because
//     bash's attributes change what the value IS (`declare -i N=5+5` binds `10`). An
//     unknown value can never reach the editor carve-out, which is the fail-closed
//     direction — see hasGitProgramEnvVar's guard 1.
//  3. `unset` AND `export -n` ARE NOT MODELLED, so `export GIT_DIR=/x; unset GIT_DIR; git
//     status` is screened although git sees nothing. Modelling revocation would mean
//     tracking it per name for a verdict that moves one way only.
//  4. A SUBSHELL'S EXPORT IS TREATED AS PERSISTING, IN THIS FUNCTION SPECIFICALLY. A
//     leaf now carries a subshell SCOPE PATH (cmdparse.ParsedCommand.SubshellScope,
//     pg2-4ak2k), and cmdparse.InCommandVars consults it to close the identical
//     residual for shell-variable resolution — but exportedEnvVarsBefore below is a
//     SEPARATE, hand-rolled scan that does not consult SubshellScope at all, so
//     `(export GIT_PAGER=/tmp/evil); git log` still reads here as though the export
//     survived the subshell closing. Closing THIS copy is a matter of exchanging the
//     unconditional scan for the same prefix-of/equal-to test InCommandVars applies,
//     not a new scope-path change — the leaf already carries what it needs.
//  5. A DUPLICATE LEAF TEXT RESOLVES TO ITS LAST OCCURRENCE. `git log; export
//     GIT_PAGER=/tmp/evil; git log` has two byte-identical `git log` leaves, so matching
//     by Raw is ambiguous; taking the LAST match means the FIRST `git log` is judged as if
//     the export had already happened. Over-restrictive for that leaf, and the only
//     direction that does not let the real one slip through.
//
// # SCOPE FALLBACK
//
// Under the engine a rule receives ONE leaf and the whole expression as
// HookInput.RootExpression, so the sibling leaves are recovered by re-parsing that
// and locating this leaf by its Raw text — the same seam internal/rules/gitdir uses, and
// the relation cmdparse.DownstreamStages documents as exact ("the engine builds a leaf's
// synthetic HookInput from that same Raw"). A DIRECT caller (a unit test, any non-engine
// caller) is handed the whole expression already, so its own leaves ARE the expression and
// `before` is already the right index. When RootExpression is empty or nothing matches,
// this degrades to exactly the leaf-local behaviour every screen had before, which is why
// no verdict can regress through this path.
func VisibleEnvVars(input *HookInput, leaves []cmdparse.ParsedCommand, before int) []cmdparse.EnvAssignment {
	if before < 0 || before >= len(leaves) {
		return nil
	}
	scope, at := expressionScope(input, leaves, before)
	own := leaves[before].EnvVars
	prior := exportedEnvVarsBefore(scope, at)
	if len(prior) == 0 {
		return own
	}
	// The leaf's OWN prefix goes last so it wins any duplicate-name comparison a future
	// consumer makes: a prefix assignment is what the child really receives.
	return append(prior, own...)
}

// expressionScope returns the leaves of the EXPRESSION `leaves[before]` belongs to, and
// that leaf's index within them. See VisibleEnvVars' SCOPE FALLBACK for the two cases and
// why the fallback cannot regress a verdict.
func expressionScope(input *HookInput, leaves []cmdparse.ParsedCommand, before int) ([]cmdparse.ParsedCommand, int) {
	if input == nil || input.RootExpression == "" {
		return leaves, before
	}
	// ADR 0039 step 3: the engine already parsed RootExpression once (it is the
	// very text EvaluateExpression split into leaves), and RootLeavesOf reads
	// that threaded result instead of re-parsing the same string here — see
	// HookInput.ParsedRoot's doc.
	root := RootLeavesOf(input)
	// The LAST match, not the first — over-approximation 5 in VisibleEnvVars' list.
	at := -1
	for j := range root {
		if root[j].Raw == leaves[before].Raw {
			at = j
		}
	}
	if at < 0 {
		return leaves, before
	}
	return root, at
}

// exportedEnvVarsBefore collects the assignments the leaves BEFORE index `before` put into
// the environment a later child process inherits. See VisibleEnvVars for the measured
// spelling-by-spelling table this implements and for the over-approximations.
func exportedEnvVarsBefore(leaves []cmdparse.ParsedCommand, before int) []cmdparse.EnvAssignment {
	var out []cmdparse.EnvAssignment
	allExport := false
	for k := 0; k < before && k < len(leaves); k++ {
		pc := leaves[k]
		// A PIPELINE STAGE runs in a subshell, so nothing it exports reaches a later leaf
		// — measured: `export NAME=v | cat` leaves NAME unset in a later child. Same
		// exclusion, and the same reason, as cmdparse's own shellVarWrites.
		if inMultiStagePipelineStage(leaves, k) {
			continue
		}
		switch pc.Executable {
		case "set":
			// `set -a` / `set -o allexport` makes every LATER plain assignment an export.
			// `set +a` is deliberately not modelled: once allexport has been seen this
			// stays on, which over-approximates toward the prompt.
			if setsAllExport(pc.Args) {
				allExport = true
			}
		case "export":
			// cmdparse lifts `export NAME=VALUE` arguments into EnvVars and leaves a BARE
			// `export NAME` in Args, so both halves have to be read — the bare form
			// exports a value set by an earlier Bash call, which is a value nothing here
			// can see and therefore the one that most needs to fail closed.
			out = append(out, pc.EnvVars...)
			out = append(out, namesWithUnknownValues(pc.Args)...)
		case "declare", "typeset":
			// The lowering keeps a declare-family leaf's assignments in Args. Values are
			// never read from here — over-approximation 2 — so every name it mentions
			// arrives with an unknown value.
			out = append(out, namesWithUnknownValues(pc.Args)...)
		case "":
			// A command-less leaf carrying its own assignments. These are SHELL variables
			// and reach no child (measured) unless allexport is on.
			if allExport {
				out = append(out, pc.EnvVars...)
			}
		}
	}
	return out
}

// setsAllExport reports whether a `set` invocation turns on allexport, in either spelling
// bash accepts — the clustered short (`-a`, and `a` inside a cluster such as `-ax`) or the
// long `-o allexport`.
func setsAllExport(args []string) bool {
	for i, a := range args {
		if a == "-o" && i+1 < len(args) && args[i+1] == "allexport" {
			return true
		}
		if strings.HasPrefix(a, "-o") && strings.TrimPrefix(a, "-o") == "allexport" {
			return true
		}
		if strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.ContainsRune(a, 'a') {
			return true
		}
	}
	return false
}

// namesWithUnknownValues turns an assignment-builtin's ARGUMENTS into assignments whose
// VALUE is explicitly unknown. Flags are skipped; every other argument contributes its
// identifier prefix, which covers the bare `NAME`, the `NAME=VALUE` form and bash's
// array-element `m[k]=v` (whose `m` is the name that changes).
//
// cmdparse.ExpansionUnknown is the load-bearing part, not a placeholder: it is what makes
// these assignments unable to reach the editor carve-out, whose first guard requires
// ExpansionNone (hasGitProgramEnvVar). So a name arriving through here is screened
// value-blind even when the text beside it happens to spell an inert literal.
func namesWithUnknownValues(args []string) []cmdparse.EnvAssignment {
	var out []cmdparse.EnvAssignment
	for _, a := range args {
		if strings.HasPrefix(a, "-") || strings.HasPrefix(a, "+") {
			continue
		}
		end := strings.IndexAny(a, "[=")
		if end < 0 {
			end = len(a)
		}
		name := strings.TrimSuffix(a[:end], "+")
		if name == "" {
			continue
		}
		out = append(out, cmdparse.EnvAssignment{Name: name, Raw: a, Expansion: cmdparse.ExpansionUnknown})
	}
	return out
}

// inMultiStagePipelineStage reports whether leaf i is one stage of a MULTI-STAGE pipeline.
// A lone command is a one-stage pipeline and is not one of these.
//
// It is a local reimplementation of cmdparse's unexported `inMultiStagePipeline` rather
// than a shared helper, because exporting one would widen that package's API for a single
// consumer; the pipeline coordinates it reads (PipelineID / PipelineIndex) are already
// exported and are documented as per-Parse-call, which is satisfied here — `leaves` is one
// Parse's output throughout.
func inMultiStagePipelineStage(leaves []cmdparse.ParsedCommand, i int) bool {
	pc := leaves[i]
	if pc.PipelineID < 0 { // a synthesized leaf stands in no pipeline
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
