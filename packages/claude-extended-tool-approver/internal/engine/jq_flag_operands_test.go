// jq_flag_operands_test.go — pg2-wrxg6 asserted END TO END, through the
// production rule chain, as a RELATION between two spellings of one jq read.
//
// THE RELATION: for any path, naming it through a jq FLAG OPERAND is never LESS gated than
// passing it as a jq POSITIONAL.
//
// That is the invariant that was missing, and its absence is exactly why the defect was
// invisible: `--argfile name file` SATISFIED it while its two siblings `--rawfile` and
// `--slurpfile` violated it, so three flags of one family disagreed and no test compared
// them. Stating it as a relation rather than as verdict rows is what makes a future flag
// inherit the guard instead of needing a row of its own.
//
// WHY IT NEEDS THE WHOLE CHAIN. The path was deleted from the candidate set in
// `internal/cmdparse` (SkipJqValueFlags) and the screening that would have caught it lives
// in `internal/rules/secrets` and `internal/rules/safecmds`. Each component was
// self-consistent; the COMPOSITION lost the path. Only an assembled chain evaluates that.
//
// TWO CLASSES OF DEFECT ARE PINNED HERE, and they failed differently:
//
//   - AN OPERAND JQ OPENS, deleted by a skip table: `-f`, `--from-file`, `--rawfile`,
//     `--slurpfile`. Measured `approve` on main @6737a0ea against `reject` for the same
//     file as a positional.
//   - A BOOLEAN FLAG WRONGLY LISTED AS TAKING A VALUE, which swallows the FOLLOWING token:
//     `--tab`, `--join-output`, `--jsonargs`. `--join-output` is the cleanest evidence —
//     it measured `abstain` while its OWN SHORT FORM `-j` measured `reject`, the same flag
//     in two spellings differing only by table membership.
//
// The short/long-form pairs below are load-bearing for the second class: a table is keyed
// on the exact spelling, so the pair is what detects a one-spelling entry.
package engine_test

import (
	"strings"
	"testing"
)

// TestJq_FlagOperandIsNeverLooserThanAPositional asserts the relation over a matrix of
// jq flags x paths rather than over hand-picked rows, because the defect's shape is "some
// flag nobody wrote down".
func TestJq_FlagOperandIsNeverLooserThanAPositional(t *testing.T) {
	t.Setenv("WORKSPACE_ROOT", "/Users/testuser/workspace")
	projectRoot := "/Users/testuser/workspace/my-project"
	eng := buildFullEngine(projectRoot, projectRoot)

	// Flags whose operand jq OPENS. Each entry renders the command for a given path; the
	// path must end up at least as gated as `jq . <path>`.
	//
	// The two-operand forms keep their NAME operand, which is what pg2-ia640.2's fix is
	// about — the relation is about the FILE operand only.
	fileFlagForms := []func(p string) string{
		func(p string) string { return "jq -f " + p + " ." },
		func(p string) string { return "jq --from-file " + p + " ." },
		func(p string) string { return "jq --rawfile n " + p + " ." },
		func(p string) string { return "jq --slurpfile n " + p + " ." },
		func(p string) string { return "jq --argfile n " + p + " ." },
	}

	// Boolean flags that take NO operand. The path follows the filter, so a flag that
	// wrongly consumes a token would eat the filter and leave the path in the filter's
	// place — where it stops being screened. Short and long spellings are BOTH listed
	// because a skip table is keyed on the exact token.
	booleanFlagForms := []func(p string) string{
		func(p string) string { return "jq --tab . " + p },
		func(p string) string { return "jq --join-output . " + p },
		func(p string) string { return "jq -j . " + p },
		func(p string) string { return "jq --jsonargs . " + p },
		func(p string) string { return "jq --args . " + p },
		func(p string) string { return "jq -S . " + p },
		func(p string) string { return "jq -r . " + p },
		func(p string) string { return "jq --raw-output . " + p },
		func(p string) string { return "jq -s . " + p },
		func(p string) string { return "jq --slurp . " + p },
		func(p string) string { return "jq -c . " + p },
		func(p string) string { return "jq --compact-output . " + p },
		func(p string) string { return "jq -n . " + p },
		func(p string) string { return "jq --null-input . " + p },
		func(p string) string { return "jq -e . " + p },
		func(p string) string { return "jq --exit-status . " + p },
		func(p string) string { return "jq -a . " + p },
		func(p string) string { return "jq --ascii-output . " + p },
		func(p string) string { return "jq -R . " + p },
		func(p string) string { return "jq --raw-input . " + p },
		func(p string) string { return "jq --seq . " + p },
		func(p string) string { return "jq --stream . " + p },
		func(p string) string { return "jq --unbuffered . " + p },
		func(p string) string { return "jq -C . " + p },
		func(p string) string { return "jq -M . " + p },
	}

	// Flags whose operand is a LITERAL jq never opens. They must keep skipping it — that is
	// pg2-ia640.2's false-positive fix — so they are checked in the OPPOSITE direction only
	// (see the literal-operand test below), never as part of this relation.
	paths := []string{
		"/Users/testuser/.ssh/id_rsa", "/Users/testuser/.aws/credentials",
		"~/.ssh/config", "secrets/db.yaml", ".env",
		"/etc/shadow", "/etc/passwd", "/var/log/system.log",
		projectRoot + "/go.mod", "./data.json",
	}

	for _, p := range paths {
		positional := "jq . " + p
		want := eng.EvaluateHook(provenanceInput(projectRoot, positional))
		for _, form := range append(append([]func(string) string{}, fileFlagForms...), booleanFlagForms...) {
			cmd := form(p)
			got := eng.EvaluateHook(provenanceInput(projectRoot, cmd))
			if got.Decision < want.Decision {
				t.Errorf("JQ FLAG-OPERAND HOLE: %q is %s but the positional %q is %s — naming the path through a flag made it LESS gated\n  flag reason:       %s\n  positional reason: %s",
					cmd, got.Decision, positional, want.Decision, got.Reason, want.Reason)
			}
		}
	}
}

// TestJq_LiteralOperandFlagsStillSkipTheirValue is the OPPOSITE-DIRECTION half, and it is
// a different claim from the relation above.
//
// The relation says a file operand is never under-gated. This says the LITERAL operands are
// still skipped, because the cheap way to satisfy the relation would have been to stop
// skipping everything — which would re-break pg2-ia640.2 (a `--arg dir "/app/src"` binding
// read as a filename) and pg2-gkd5e's `action_meta=$(jq -nc --arg a b '{a:$a}')`.
//
// `--arg` / `--argjson` / `--indent` are the ONLY jq flags in jq 1.8.2 whose operands are
// literals jq never opens, so this table is complete rather than a sample.
func TestJq_LiteralOperandFlagsStillSkipTheirValue(t *testing.T) {
	t.Setenv("WORKSPACE_ROOT", "/Users/testuser/workspace")
	projectRoot := "/Users/testuser/workspace/my-project"
	eng := buildFullEngine(projectRoot, projectRoot)

	// A credential-shaped STRING passed as a jq variable binding is not a read of it, so it
	// must NOT be gated. These are the rows pg2-ia640.2 exists for.
	for _, cmd := range []string{
		`jq --arg p /Users/testuser/.ssh/id_rsa .`,
		`jq --arg dir /app/src .`,
		`jq --argjson n 1 . ` + projectRoot + `/data.json`,
		`jq --indent 2 . ` + projectRoot + `/data.json`,
	} {
		got := eng.EvaluateHook(provenanceInput(projectRoot, cmd))
		if got.Decision.String() == "reject" {
			t.Errorf("LITERAL OPERAND REGRESSED: %q is %s — a jq variable binding is not a file read, and gating it is the false positive pg2-ia640.2 fixed\n  reason: %s",
				cmd, got.Decision, got.Reason)
		}
	}
}

// TestJq_SkipTablesHoldNoFileTakingFlag is the STRUCTURAL guard, and it is the one that
// makes a future edit safe rather than merely making today's tables correct.
//
// The rule it enforces is the one messageFlags' doc already states for its own table: A FLAG
// WHOSE OPERAND THE COMMAND OPENS NEVER BELONGS IN A SKIP TABLE. Both defects pg2-wrxg6
// closed were additions to those tables that violated it, and a verdict test cannot prevent
// the next one — it can only notice it for the paths someone thought to list.
//
// It reads the tables through the behaviour rather than reaching into cmdparse's unexported
// maps: for every flag jq documents as taking a file or directory operand, the operand must
// survive into the args path screening sees.
func TestJq_SkipTablesHoldNoFileTakingFlag(t *testing.T) {
	t.Setenv("WORKSPACE_ROOT", "/Users/testuser/workspace")
	projectRoot := "/Users/testuser/workspace/my-project"
	eng := buildFullEngine(projectRoot, projectRoot)

	const denyListed = "/Users/testuser/.ssh/id_rsa"

	// Every jq flag documented (jq 1.8.2 --help) as taking a FILE or DIRECTORY operand.
	// `-L`/`--library-path` is listed and EXPECTED TO FAIL THIS today — it escapes by a
	// different route (its operand becomes jq's apparent filter) and is tracked as
	// pg2-mu8zg — so it is asserted only NOT to reach `approve`, the weaker claim that
	// holds now, with the stronger one left to that bead.
	for _, tc := range []struct {
		cmd       string
		mustNotBe string
		bead      string
	}{
		{"jq -f " + denyListed + " .", "approve", "pg2-wrxg6"},
		{"jq --from-file " + denyListed + " .", "approve", "pg2-wrxg6"},
		{"jq --rawfile n " + denyListed + " .", "approve", "pg2-wrxg6"},
		{"jq --slurpfile n " + denyListed + " .", "approve", "pg2-wrxg6"},
		{"jq --argfile n " + denyListed + " .", "approve", "pg2-wrxg6"},
		{"jq -L /Users/testuser/.ssh . " + projectRoot + "/data.json", "approve", "pg2-mu8zg"},
		{"jq --library-path /Users/testuser/.ssh . " + projectRoot + "/data.json", "approve", "pg2-mu8zg"},
	} {
		got := eng.EvaluateHook(provenanceInput(projectRoot, tc.cmd))
		if got.Decision.String() == tc.mustNotBe {
			t.Errorf("FILE-TAKING FLAG IS IN A SKIP TABLE: %q is %s — its operand is a path jq opens, so it must not be deleted from the candidate set (%s)\n  reason: %s",
				tc.cmd, got.Decision, tc.bead, got.Reason)
		}
	}

	// The captured-substitution spelling, which is how pg2-wrxg6 was originally reported.
	// It reaches the approval through the RECURSION rather than the static seam, so a fix
	// confined to one of the two path models would leave it open (the pg2-zpct4 shape).
	for _, cmd := range []string{
		"X=$(jq -f " + denyListed + " .) echo hi",
		"X=$(jq --rawfile n " + denyListed + " .) echo hi",
	} {
		got := eng.EvaluateHook(provenanceInput(projectRoot, cmd))
		if got.Decision.String() == "approve" {
			t.Errorf("CAPTURED SPELLING STILL APPROVES: %q is %s — the approval arrives via the substitution recursion, so both path models must screen the operand\n  reason: %s",
				cmd, got.Decision, got.Reason)
		}
		if !strings.Contains(cmd, "jq") {
			t.Fatalf("test bug: %q lost its jq invocation", cmd)
		}
	}
}
