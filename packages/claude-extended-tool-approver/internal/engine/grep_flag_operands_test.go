// grep_flag_operands_test.go — pg2-ygjs5 asserted END TO END, through the production
// rule chain, as a RELATION between two ways of naming one file to grep/rg.
//
// THE RELATION: for any path, naming it through a grep/rg FLAG OPERAND is never LESS
// gated than passing it as a grep/rg POSITIONAL.
//
// It is the sibling of jq_flag_operands_test.go's relation (pg2-wrxg6) and it exists for
// the same reason: the defect was invisible because the two spellings of one read were
// never compared. `grep -f ~/.ssh/id_rsa x.log` measured `approve` on main @974d0276
// while `grep pat ~/.ssh/id_rsa` — the SAME FILE, named positionally — measured
// `reject`. No verdict table could see that, because each row was individually
// plausible; only the COMPARISON is a defect.
//
// WHY IT NEEDS THE WHOLE CHAIN. The path was deleted from the candidate set in
// internal/cmdparse (SkipGrepPattern) while the screening that would have caught it
// lives in internal/rules/secrets and internal/rules/safecmds. Every component was
// self-consistent; the COMPOSITION lost the path.
//
// FOUR MECHANISMS ARE PINNED HERE, and they fail differently — which is the argument for
// a relation over a flag matrix rather than a list of known-bad rows. Each was measured
// on main @974d0276 as `approve` against the positional control's `reject`:
//
//   - AN OPERAND THE TOOL OPENS, deleted by a skip table: `-f` / `--file` (the PATTERN
//     FILE, whose contents become the patterns) and rg's `--ignore-file`.
//   - AN OPERAND THE TOOL OPENS, eaten by the PATTERN HEURISTIC. ugrep's
//     `--exclude-from`, `--include-from`, `--from` and `--config` were in no table at
//     all, so the flag NAME was skipped as a `-` token and the operand landed in the
//     first positional slot, where it was discarded as "the pattern". Being absent from
//     the skip tables was NOT sufficient — which is why grepFileFlags EMITS operands
//     rather than merely declining to strip them.
//   - AN OPTIONAL-VALUE FLAG WRONGLY LISTED AS TAKING A VALUE, which swallows the
//     FOLLOWING token: `--color` (`--color[=WHEN]` consumes nothing in the space
//     spelling). The pg2-wrxg6 boolean class, in the grep table.
//   - THE GLUED `--flag=value` SPELLING, discarded whole by the blanket `-`-prefix skip
//     before firstSecretRef's GluedFlagValue arm could see it (pg2-cu3ro's defect, on a
//     route that fix did not reach). Short and long, glued and spaced, are ALL listed
//     below because a skip table is keyed on the exact token.
//
// A PROGRAM OPERAND IS A SEPARATE CLAIM and has its own test at the bottom: screening
// `rg --pre CMD` as a path is necessary but NOT sufficient, because `--pre evilcmd`
// names a program on PATH that looks like nothing at all.
package engine_test

import (
	"strings"
	"testing"
)

// grepFileFlagForms renders a grep/rg invocation that names path p through a flag whose
// operand the tool OPENS. Each must end up at least as gated as `grep pat <p>`.
//
// BOTH SPELLINGS OF EACH FLAG APPEAR DELIBERATELY. The glued form is one argv token, so
// a fix confined to the space form leaves the other half open — which is exactly how
// this family of defects has propagated (pg2-cu3ro, then pg2-wrxg6, then this).
var grepFileFlagForms = []func(p string) string{
	// The pattern file: grep opens it and its contents become the patterns.
	func(p string) string { return "grep -f " + p + " x.log" },
	func(p string) string { return "grep --file " + p + " x.log" },
	func(p string) string { return "grep --file=" + p + " x.log" },
	func(p string) string { return "rg -f " + p + " x.log" },
	func(p string) string { return "rg --file " + p + " x.log" },
	func(p string) string { return "rg --file=" + p + " x.log" },
	// rg reads this one for ignore rules.
	func(p string) string { return "rg --ignore-file " + p + " pat /tmp" },
	func(p string) string { return "rg --ignore-file=" + p + " pat /tmp" },
	// ugrep 7.5.0 reads each of these. They were in NO table, and the pattern heuristic
	// ate the operand — see this file's header.
	func(p string) string { return "grep --exclude-from " + p + " pat /tmp" },
	func(p string) string { return "grep --exclude-from=" + p + " pat /tmp" },
	func(p string) string { return "grep --include-from " + p + " pat /tmp" },
	func(p string) string { return "grep --include-from=" + p + " pat /tmp" },
	func(p string) string { return "grep --from " + p + " pat /tmp" },
	func(p string) string { return "grep --from=" + p + " pat /tmp" },
	// ugrep's OPTIONAL-value file flags, GLUED SPELLING ONLY. `--config[=FILE]` names a
	// file only when the value is attached by `=`; the space form consumes nothing (see
	// the boolean forms below, where those spellings are asserted instead).
	// `--save-config` WRITES its file, which is at least as gated as reading it.
	func(p string) string { return "grep --config=" + p + " pat /tmp" },
	func(p string) string { return "grep --ignore-files=" + p + " pat /tmp" },
	func(p string) string { return "grep --save-config=" + p + " pat /tmp" },
}

// grepBooleanFlagForms renders invocations whose flag takes NO value in the space
// spelling, so the path stays a positional FILE. A flag wrongly listed as value-taking
// swallows the pattern and leaves the path in the pattern's slot, where it stops being
// screened. Short and long spellings are both listed because a table is keyed on the
// exact token.
// It is ALSO where an OPTIONAL-value flag belongs, and those rows are the ones that
// caught a regression this very fix introduced: `--config[=FILE]` names a file, so
// putting it in the consuming file-flag table looked obviously right — and it swallowed
// the pattern, taking `grep --config pat ~/.ssh/id_rsa` from `reject` to `approve`. The
// whole-corpus replay could NOT catch that (no logged invocation uses the flag at all),
// so this list is the only thing standing between an optional-value flag and a
// less-restrictive transition.
var grepBooleanFlagForms = []func(p string) string{
	// --color[=WHEN] / --colour[=WHEN]: optional value, so neither consumes a token here.
	func(p string) string { return "grep --color pat " + p },
	func(p string) string { return "grep --colour pat " + p },
	func(p string) string { return "rg --color pat " + p },
	// ugrep's optional-value FILE flags: in the space spelling they take no operand, so
	// the trailing positional is a FILE and must stay screened.
	func(p string) string { return "grep --config pat " + p },
	func(p string) string { return "grep --ignore-files pat " + p },
	func(p string) string { return "grep --save-config pat " + p },
	// ...and the optional-value PROGRAM flags, for the same reason.
	func(p string) string { return "grep --pager pat " + p },
	func(p string) string { return "grep --view pat " + p },
	// Plain booleans, as controls on the same shape.
	func(p string) string { return "grep -i pat " + p },
	func(p string) string { return "grep -n pat " + p },
	func(p string) string { return "grep -v pat " + p },
	func(p string) string { return "grep -H pat " + p },
	func(p string) string { return "grep -w pat " + p },
	func(p string) string { return "grep -c pat " + p },
	func(p string) string { return "grep -l pat " + p },
	func(p string) string { return "grep -o pat " + p },
	func(p string) string { return "grep -a pat " + p },
	func(p string) string { return "grep -s pat " + p },
	func(p string) string { return "grep --ignore-case pat " + p },
	func(p string) string { return "grep --line-number pat " + p },
	func(p string) string { return "grep --invert-match pat " + p },
	func(p string) string { return "grep --recursive pat " + p },
	func(p string) string { return "rg -i pat " + p },
	func(p string) string { return "rg -n pat " + p },
	func(p string) string { return "rg -L pat " + p },
	func(p string) string { return "rg -uu pat " + p },
	func(p string) string { return "rg --hidden pat " + p },
	func(p string) string { return "rg --no-ignore pat " + p },
	func(p string) string { return "rg --files-with-matches pat " + p },
	func(p string) string { return "rg --multiline pat " + p },
}

// relationPaths spans the deny-listed, the merely out-of-zone and the benign, because
// the four mechanisms are not all visible on one kind of path: a skip table hides a
// deny-listed key from the secrets rule, while the pattern heuristic loses an
// out-of-zone path from safecmds' zone model (the pg2-wxbr9 distinction).
var relationPaths = []string{
	"/Users/testuser/.ssh/id_rsa", "/Users/testuser/.aws/credentials",
	"~/.ssh/config", "secrets/db.yaml", ".env",
	"/etc/shadow", "/etc/passwd", "/var/log/system.log",
	"./data.json",
}

// TestGrep_FlagOperandIsNeverLooserThanAPositional asserts the relation over a matrix of
// grep/rg flags x paths rather than over hand-picked rows, because the defect's shape is
// "some flag nobody wrote down" — and this fix found four such flags in ugrep alone
// after the bead had named two.
func TestGrep_FlagOperandIsNeverLooserThanAPositional(t *testing.T) {
	t.Setenv("WORKSPACE_ROOT", "/Users/testuser/workspace")
	projectRoot := "/Users/testuser/workspace/my-project"
	eng := buildFullEngine(projectRoot, projectRoot)

	forms := append(append([]func(string) string{}, grepFileFlagForms...), grepBooleanFlagForms...)
	for _, p := range relationPaths {
		positional := "grep pat " + p
		want := eng.EvaluateHook(provenanceInput(projectRoot, positional))
		for _, form := range forms {
			cmd := form(p)
			got := eng.EvaluateHook(provenanceInput(projectRoot, cmd))
			if got.Decision < want.Decision {
				t.Errorf("GREP FLAG-OPERAND HOLE: %q is %s but the positional %q is %s — naming the path through a flag made it LESS gated\n  flag reason:       %s\n  positional reason: %s",
					cmd, got.Decision, positional, want.Decision, got.Reason, want.Reason)
			}
		}
	}
}

// TestGrep_NonPathFlagValuesAreStillSkipped is the OPPOSITE-DIRECTION half, and it is a
// different claim from the relation above.
//
// The relation says a file operand is never under-gated. This says a NON-path operand is
// still not treated as a filename, because the cheap way to satisfy the relation would
// have been to stop skipping anything — which would re-break pg2-ia640.2, whose whole
// subject is that a grep PATTERN, an rg GLOB and a context NUMBER are not files.
//
// The rows named in pg2-ygjs5's acceptance criteria are the first three, and they pass
// UNMODIFIED from pg2-ia640.2. One pg2-ia640.2 row did change verdict and is deliberately
// absent: `grep -f .env file.log` now Asks, because `-f`'s operand IS a file grep opens.
// See the note on that row in internal/rules/secrets/secrets_test.go.
func TestGrep_NonPathFlagValuesAreStillSkipped(t *testing.T) {
	t.Setenv("WORKSPACE_ROOT", "/Users/testuser/workspace")
	projectRoot := "/Users/testuser/workspace/my-project"
	eng := buildFullEngine(projectRoot, projectRoot)

	for _, cmd := range []string{
		// pg2-ia640.2's own rows: a pattern, a flag-supplied pattern, a glob.
		`grep .env file.log`,
		`rg .env somefile.log`,
		`grep -e .env file.log`,
		`rg -g *.env pattern file.log`,
		`grep --regexp=.env file.log`,
		// A non-path value must not become a candidate path merely because it is glued.
		// `--path-separator=/` is the sharp case: emitted, `/` is out of every zone.
		`rg --path-separator=/ pat /tmp`,
		`rg --context-separator=-- pat /tmp`,
		`rg --field-match-separator=: pat /tmp`,
		`rg --max-filesize=1M pat /tmp`,
		`rg --threads=4 pat /tmp`,
		`grep --binary-files=text pat file.log`,
		`grep --devices=skip pat file.log`,
		`grep --colors=ms=01 pat file.log`,
		`grep -C 3 pat file.log`,
		`grep -m 5 pat file.log`,
	} {
		got := eng.EvaluateHook(provenanceInput(projectRoot, cmd))
		if got.Decision.String() != "approve" {
			t.Errorf("NON-PATH FLAG VALUE REGRESSED: %q is %s — a pattern, glob, number, enum or separator is not a file read, and gating it is the false positive pg2-ia640.2 fixed\n  reason: %s",
				cmd, got.Decision, got.Reason)
		}
	}
}

// TestGrep_PatternSourceFlagsKeepTheirPositionalsAsFiles pins the detection that made
// this fix non-obvious, and it guards the direction the fix could most easily have
// broken.
//
// When the pattern comes from a flag there is NO positional pattern, so EVERY positional
// is a file. Removing `-f`/`--file` from the value table is only safe because that
// detection is separate and was kept; had it been removed alongside the table entries,
// `grep -f pats.txt real.log` would have started discarding `real.log` as "the pattern".
//
// The glued spelling is here for a reason beyond symmetry: the detection originally
// keyed on the exact tokens `-f`/`--file`, so `grep --file=pats.txt <path>` did NOT set
// it and the path was eaten. That is a REAL path lost from screening, introduced by the
// glued arm if the pre-scan is not taught the same spelling.
func TestGrep_PatternSourceFlagsKeepTheirPositionalsAsFiles(t *testing.T) {
	t.Setenv("WORKSPACE_ROOT", "/Users/testuser/workspace")
	projectRoot := "/Users/testuser/workspace/my-project"
	eng := buildFullEngine(projectRoot, projectRoot)

	const denyListed = "/Users/testuser/.ssh/id_rsa"

	// With the pattern supplied by a flag, the trailing positional is a FILE and must be
	// screened as one.
	for _, cmd := range []string{
		"grep -f pats.txt " + denyListed,
		"grep --file pats.txt " + denyListed,
		"grep --file=pats.txt " + denyListed,
		"grep -e pat " + denyListed,
		"grep --regexp pat " + denyListed,
		"grep --regexp=pat " + denyListed,
		"rg -f pats.txt " + denyListed,
		"rg --file=pats.txt " + denyListed,
	} {
		got := eng.EvaluateHook(provenanceInput(projectRoot, cmd))
		if got.Decision.String() == "approve" {
			t.Errorf("POSITIONAL FILE EATEN AS THE PATTERN: %q is %s — the pattern comes from a flag, so the trailing positional is a FILE and must be screened\n  reason: %s",
				cmd, got.Decision, got.Reason)
		}
	}

	// And the converse: with NO pattern-source flag the first positional IS the pattern
	// and must not be screened, or every `grep .env x.log` becomes a prompt.
	for _, cmd := range []string{
		"grep .env x.log",
		"grep -i .env x.log",
		"rg .env x.log",
	} {
		got := eng.EvaluateHook(provenanceInput(projectRoot, cmd))
		if got.Decision.String() != "approve" {
			t.Errorf("POSITIONAL PATTERN SCREENED AS A FILE: %q is %s — with no pattern-source flag the first positional is the PATTERN\n  reason: %s",
				cmd, got.Decision, got.Reason)
		}
	}
}

// TestGrep_ProgramOperandFlagsAreNotReadOnly is the claim the relation cannot make.
//
// The relation compares a flag operand against a POSITIONAL FILE, so it can only ever
// assert that an operand is screened as well as a path is. `rg --pre CMD` runs CMD per
// file and searches its OUTPUT, and ugrep's `--filter`/`--pager`/`--view` likewise name
// programs — so the operand being a screened path is necessary and NOT sufficient:
// `rg --pre evilcmd` names a program on PATH and is path-shaped like nothing at all.
//
// grep and rg are approvable BECAUSE they only read. These flags make the invocation an
// execution primitive, so the disqualification is of the whole command: it must not be
// APPROVED, whatever its operand looks like.
func TestGrep_ProgramOperandFlagsAreNotReadOnly(t *testing.T) {
	t.Setenv("WORKSPACE_ROOT", "/Users/testuser/workspace")
	projectRoot := "/Users/testuser/workspace/my-project"
	eng := buildFullEngine(projectRoot, projectRoot)

	// A BENIGN, IN-ZONE operand is the load-bearing case. An out-of-zone operand like
	// /tmp/evil would be caught by the zone model even with no notion of execution, so it
	// would pass this test for the wrong reason and prove nothing.
	for _, cmd := range []string{
		"rg --pre catz pat " + projectRoot,
		"rg --pre=catz pat " + projectRoot,
		"rg --pre " + projectRoot + "/bin/pre pat " + projectRoot,
		"rg --hostname-bin hn pat " + projectRoot,
		"rg --hostname-bin=hn pat " + projectRoot,
		"grep --filter=pdf:pdftotext pat " + projectRoot,
		"grep --pager=less pat " + projectRoot,
		"grep --pager pat " + projectRoot,
		"grep --view=vi pat " + projectRoot,
		// Through xargs, which reaches the same command by a different route in
		// safecmds. A guard on only the direct spelling is the one-spelling coverage
		// this family of defects is made of.
		"git ls-files | xargs rg --pre catz pat",
		"git ls-files | xargs grep --filter=pdf:pdftotext pat",
	} {
		got := eng.EvaluateHook(provenanceInput(projectRoot, cmd))
		if got.Decision.String() == "approve" {
			t.Errorf("PROGRAM-RUNNING FLAG APPROVED AS A SAFE READ: %q is %s — the flag names a program the tool RUNS, so the invocation is not read-only and screening its operand as a path cannot make it so\n  reason: %s",
				cmd, got.Decision, got.Reason)
		}
	}

	// The `--` boundary is load-bearing in the OTHER direction: after it a token is an
	// operand, so this is a search for the literal string `--pre` and must stay approved.
	for _, cmd := range []string{
		"rg -- --pre " + projectRoot,
		"grep -- --filter=x " + projectRoot,
	} {
		got := eng.EvaluateHook(provenanceInput(projectRoot, cmd))
		if got.Decision.String() != "approve" {
			t.Errorf("LITERAL SEARCH TERM READ AS A FLAG: %q is %s — after a bare `--` the token is an operand, not a program-running flag\n  reason: %s",
				cmd, got.Decision, got.Reason)
		}
		if !strings.Contains(cmd, "--") {
			t.Fatalf("test bug: %q lost its -- boundary", cmd)
		}
	}
}
