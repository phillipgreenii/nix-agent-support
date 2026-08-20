package cmdparse

// The tests ADR 0039 step 2 (pg2-fez3d) owes.
//
// Three beads are superseded by this step, and ADR 0039's Enforcement is explicit
// that superseding a defect bead is valid ONLY if its defect has a test that would
// catch a regression, written against its ORIGINAL REPRODUCER rather than a weaker
// restatement. Those three are pg2-s26v5, pg2-4h7ee and pg2-14vjq.
//
// The rest of this file pins the constructs ADR 0039's Consequences names as
// "silently change verdict if lowered naively" — each of them in the LESS
// restrictive direction, which is the direction the replay gate exists to catch.

import (
	"strings"
	"testing"
)

// TestFlip_BareSubshellKeepsEveryCommand is the test owed to pg2-s26v5, written
// against its original reproducer.
//
// The reproducer: the outgoing `splitCompound` scanned a bare `(` group by counting
// parens WITHOUT tracking quotes, so the `)` inside `")"` closed the group early and
// `ls` was TRUNCATED off the end — a command that a real shell runs, judged by
// nobody. It is inventory site 1's shape (an extent hand-rolled while holding the
// shared scanner).
//
// pg2-s26v5's acceptance criteria name three things, and all three are asserted:
// two leaves with `ls` intact, `Raw` an exact source slice, and FuzzParse's
// IDEMPOTENCE invariant holding against that Raw — which is the half that was
// VACUOUS before the `Raw` decision (I12), because a post-strip Raw could not be
// expected to re-parse to anything in particular.
func TestFlip_BareSubshellKeepsEveryCommand(t *testing.T) {
	for _, src := range []string{`(echo ")"; ls)`, `(echo ')'; ls)`} {
		t.Run(src, func(t *testing.T) {
			sp := ParseShell(src)
			if sp.Unparseable {
				t.Fatalf("ParseShell(%q) failed to parse: %s", src, sp.Reason)
			}
			var execs []string
			for _, leaf := range sp.Leaves {
				execs = append(execs, leaf.Executable)
			}
			if len(execs) != 2 || execs[0] != "echo" || execs[1] != "ls" {
				t.Fatalf("executables = %v, want [echo ls] — the subshell was truncated at the quoted paren", execs)
			}
			for i, leaf := range sp.Leaves {
				// I12: Raw is an exact source slice, so it appears verbatim in the source.
				if !strings.Contains(src, leaf.Raw) {
					t.Errorf("leaf %d Raw %q is not a slice of %q (I12)", i, leaf.Raw, src)
				}
				// FuzzParse's idempotence invariant, now meaningful: the engine re-feeds
				// leaf.Raw as a synthetic command, so re-parsing it must reproduce the same
				// executable or a rule judges a different command than was folded.
				re := ParseShell(leaf.Raw)
				if re.Unparseable {
					t.Fatalf("leaf %d Raw %q does not re-parse: %s", i, leaf.Raw, re.Reason)
				}
				if len(re.Leaves) != 1 || re.Leaves[0].Executable != leaf.Executable {
					t.Errorf("leaf %d Raw %q re-parses to %s, want the single %q leaf",
						i, leaf.Raw, dumpLeaves(re.Leaves), leaf.Executable)
				}
			}
		})
	}

	// `echo ")"` must keep its argument too: the quoted paren is DATA, so it stays in
	// the token rather than being consumed as grouping syntax.
	sp := ParseShell(`(echo ")"; ls)`)
	if got := sp.Leaves[0].Args; len(got) != 1 || got[0] != ")" {
		t.Errorf("args = %v, want [)] — the quoted paren must survive as an operand", got)
	}
}

// TestFlip_HashInsideAQuotedArgumentIsNotAComment is the test owed to pg2-4h7ee,
// written against its original reproducer.
//
// The reproducer: the engine ran a PER-LINE comment strip before parsing, so a '#'
// inside a MULTI-LINE quoted argument was treated as the start of a comment and the
// rest of that line was DELETED from the command. 41 corpus rows were mangled by it
// — the reason those rows were deliberately left unannotated.
//
// The defect is now impossible by CONSTRUCTION rather than fixed: there is no
// pre-strip pass, and under KeepComments(true) a '#' inside a quoted word is part of
// a *syntax.Lit. So the test asserts what a mangled parse would destroy — the
// argument arrives WHOLE, with every line and every '#' still in it.
//
// THIS IS THE PARSEABILITY HALF ONLY, and the distinction is load-bearing: an
// argument can arrive whole and still have a substitution inside it never ENUMERATED
// (the pg2-wguam shape). The SECURITY half — that a live `$( )` in such a span reaches
// the text the rules see, and that the quoted spelling's verdict is never more
// permissive than the unquoted one — is pg2-ekplq's
// TestIntegration_HashInAMultiLineQuotedSpanNeverHidesASubstitution in
// internal/engine, where the composed rule chain is reachable.
func TestFlip_HashInsideAQuotedArgumentIsNotAComment(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "multi-line double-quoted argument",
			src:  "bd update x --notes \"line one # not a comment\nline two # nor this\"",
			want: "line one # not a comment\nline two # nor this",
		},
		{
			name: "multi-line single-quoted argument",
			src:  "git commit -m 'fix #123\nrefs #456'",
			want: "fix #123\nrefs #456",
		},
		{
			name: "a '#' at the start of a continued quoted line",
			src:  "echo \"a\n# b\nc\"",
			want: "a\n# b\nc",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sp := ParseShell(tc.src)
			if sp.Unparseable {
				t.Fatalf("ParseShell(%q) failed: %s", tc.src, sp.Reason)
			}
			if len(sp.Leaves) != 1 {
				t.Fatalf("want 1 leaf, got %s", dumpLeaves(sp.Leaves))
			}
			args := sp.Leaves[0].Args
			last := args[len(args)-1]
			if last != tc.want {
				t.Errorf("last arg = %q, want %q — the comment pass mangled the quoted argument", last, tc.want)
			}
			// And the comment must be reported as ABSENT: there is no comment here, so
			// CommandComment must not invent one out of a quoted '#'.
			if c := CommandComment(tc.src); c != "" {
				t.Errorf("CommandComment = %q, want empty (the '#' is inside a quoted word)", c)
			}
		})
	}

	// The CONTRAST, so the test cannot pass by never seeing a comment at all: an
	// UNQUOTED trailing '#' IS a comment, and it must still be recognised.
	t.Run("an unquoted trailing # is still a comment", func(t *testing.T) {
		if got := CommandComment("echo hi # real comment"); got != "real comment" {
			t.Errorf("CommandComment = %q, want %q", got, "real comment")
		}
		leaves := Parse("echo hi # real comment")
		if len(leaves) != 1 || len(leaves[0].Args) != 1 || leaves[0].Args[0] != "hi" {
			t.Errorf("the comment leaked into the args: %s", dumpLeaves(leaves))
		}
		if leaves[0].Comment != "real comment" {
			t.Errorf("leaf Comment = %q, want %q", leaves[0].Comment, "real comment")
		}
	})
}

// TestFlip_LoopFedByAHeredocDropsNoSegment is the test owed to pg2-14vjq's FIRST
// half, written against its original reproducer.
//
// The reproducer: `while read c; do …; done <<EOF` puts the heredoc operator on the
// `done` line, and the outgoing `resolveLoops` DISCARDED the terminator segment — so
// no surviving segment claimed the extent, the body's lines were shredded into real
// leaves, and prose was judged as commands. Three corpus rows had their
// probe-harness DATA denied by it.
//
// Two things must hold: the heredoc is ATTRIBUTED to some leaf (so the I2 floor
// fires and an expanding body's substitutions are recursed), and NO SEGMENT IS
// DROPPED — every command of the loop still reaches a leaf.
func TestFlip_LoopFedByAHeredocDropsNoSegment(t *testing.T) {
	src := "while read c; do echo \"$c\"; done <<EOF\n$(rm -rf /etc)\nEOF\necho after"
	sp := ParseShell(src)
	if sp.Unparseable {
		t.Fatalf("ParseShell failed: %s", sp.Reason)
	}
	var execs []string
	extents, floored := 0, false
	for _, leaf := range sp.Leaves {
		execs = append(execs, leaf.Executable)
		extents += len(leaf.Heredocs)
		if leaf.HasHeredoc {
			floored = true
		}
		for _, a := range leaf.Args {
			if strings.Contains(a, "rm -rf") {
				t.Errorf("a heredoc BODY word became an operand: %q", a)
			}
		}
	}
	// `read` (the condition), `echo` (the body), the command-less terminator leaf
	// carrying the extent, and `echo after`. The empty executable is the terminator's
	// own redirection leaf — the shape `(cmd) > /etc/passwd` already produced — NOT a
	// body line.
	want := []string{"read", "echo", "", "echo"}
	if len(execs) != len(want) {
		t.Fatalf("executables = %v, want %v: %s", execs, want, dumpLeaves(sp.Leaves))
	}
	for i := range want {
		if execs[i] != want[i] {
			t.Fatalf("executables = %v, want %v", execs, want)
		}
	}
	if !floored || extents != 1 {
		t.Fatalf("extent lost: floored=%v extents=%d — the I2 floor and the body recursion both key on it", floored, extents)
	}
	// The expanding body's substitution must be reachable, or the injection is never
	// judged.
	var bodies []string
	for _, leaf := range sp.Leaves {
		bodies = append(bodies, leaf.UnquotedHeredocBodies()...)
	}
	if len(bodies) != 1 || !strings.Contains(bodies[0], "rm -rf /etc") {
		t.Errorf("exposed bodies = %v, want the expanding body holding the substitution", bodies)
	}
}

// TestFlip_FdPrefixedHeredocIsNotAPhantomOperand is the test owed to pg2-14vjq's
// SECOND half, and ADR 0039's Enforcement is explicit about what it must assert:
// that `2<<EOF` does NOT leak into the ARGUMENT LIST, **not** merely that the leaf is
// heredoc-bearing — which already passed before this migration and so could not
// catch a regression.
//
// The reproducer: `extractRedirections` matched heredoc operators by TOKEN PREFIX
// (`strings.HasPrefix(tok, "<<")`), which a descriptor defeats. `2<<EOF` therefore
// matched nothing, stayed an ordinary token, and reached the rule chain as an operand
// — a path-bearing rule would read `2<<EOF` as a real operand of the command.
func TestFlip_FdPrefixedHeredocIsNotAPhantomOperand(t *testing.T) {
	for _, src := range []string{
		"cat 2<<EOF\nbody\nEOF",
		"cat 9<<-EOF\n\tbody\n\tEOF",
		"cat 2<<'EOF'\nbody\nEOF",
	} {
		t.Run(src, func(t *testing.T) {
			sp := ParseShell(src)
			if sp.Unparseable {
				t.Fatalf("ParseShell failed: %s", sp.Reason)
			}
			if len(sp.Leaves) != 1 {
				t.Fatalf("want 1 leaf, got %s", dumpLeaves(sp.Leaves))
			}
			leaf := sp.Leaves[0]
			// THE OWED ASSERTION: no phantom operand.
			if len(leaf.Args) != 0 {
				t.Errorf("args = %v, want none — the fd-prefixed heredoc operator leaked into the argument list", leaf.Args)
			}
			for _, a := range leaf.Args {
				if strings.Contains(a, "<<") {
					t.Errorf("arg %q is a heredoc operator", a)
				}
			}
			// The pre-existing half, kept as a guard against the test passing because
			// nothing was recorded at all.
			if !leaf.HasHeredoc || len(leaf.Heredocs) != 1 {
				t.Errorf("the extent was not recorded: %+v", leaf.Heredocs)
			}
		})
	}
}

// TestShellParse_UnquoteParity_MixedQuoting is the NEW test ADR 0039's Consequences
// requires: "that last one needs a new test — the existing one covers
// replace-versus-preserve, not mixed quoting".
//
// THE HAZARD. The outgoing `unquote` strips quoting only when the WHOLE token is
// wrapped in ONE quote character, so `a"b"c` KEEPS its quotes. Both
// `LiteralAssignmentValueText` (envvars' structural replacement for its former
// hand-rolled `literalValue`, pg2-30wro) and `envvars.isStaticAbsolutePath` REJECT
// any value with a surviving quote, which is what fences the I4 assignment
// approval gate. The parser's own
// literal expansion is STRICTER — it would yield `abc` — and a value that newly
// becomes quote-free newly CLEARS that predicate. That is a move in the
// LESS-RESTRICTIVE direction, reached by making the lowering more correct, which is
// exactly why the seam applies the RETAINED `unquote` to each word's exact source
// slice instead of asking the parser for the literal.
func TestShellParse_UnquoteParity_MixedQuoting(t *testing.T) {
	cases := []struct {
		src  string
		want string
	}{
		// MIXED: quoting is NOT removed, so the token still carries a quote and the
		// I4 predicate still refuses it.
		{`echo a"b"c`, `a"b"c`},
		{`echo a'b'c`, `a'b'c`},
		{`echo /pre"fix"/path`, `/pre"fix"/path`},
		// The outgoing rule is FIRST-BYTE-AND-LAST-BYTE, not "is wholly one quoted
		// span", so a token that merely BEGINS and ENDS with the same quote character
		// does get stripped — and the INNER quotes survive, which is what still trips
		// the predicate. These two rows pin that exact (slightly odd) rule, because a
		// true literal expansion would remove the inner quotes too and clear it.
		{`echo 'a'b'c'`, `a'b'c`},
		{`echo ""x""`, `"x"`},
		// WHOLLY wrapped: quoting IS removed, exactly as before.
		{`echo 'abc'`, `abc`},
		{`echo "abc"`, `abc`},
	}
	for _, tc := range cases {
		t.Run(tc.src, func(t *testing.T) {
			sp := ParseShell(tc.src)
			if len(sp.Leaves) != 1 || len(sp.Leaves[0].Args) != 1 {
				t.Fatalf("want 1 leaf with 1 arg, got %s", dumpLeaves(sp.Leaves))
			}
			if got := sp.Leaves[0].Args[0]; got != tc.want {
				t.Errorf("arg = %q, want %q — a TRUE literal expansion here would clear the predicate I4 fences", got, tc.want)
			}
		})
	}

	// The same rule in ASSIGNMENT position, which is where I4 actually reads it: a
	// mixed-quoted VALUE must keep its quotes, so `LiteralAssignmentValueText`
	// keeps refusing it (see TestLiteralAssignmentValueText, parser_test.go, for
	// the dedicated coverage of that function).
	t.Run("a mixed-quoted assignment value keeps its quotes", func(t *testing.T) {
		sp := ParseShell(`PATH=/pre"fix"/bin cmd`)
		if len(sp.Leaves) != 1 || len(sp.Leaves[0].EnvVars) != 1 {
			t.Fatalf("want 1 leaf with 1 assignment, got %s", dumpLeaves(sp.Leaves))
		}
		if got := sp.Leaves[0].EnvVars[0].Value; got != `/pre"fix"/bin` {
			t.Errorf("value = %q, want %q", got, `/pre"fix"/bin`)
		}
	})
}

// TestFlip_HerestringKeepsTheHeredocFloor pins the herestring, which ADR 0039's
// Consequences names first among the constructs that "silently change verdict if
// lowered naively".
//
// THE HAZARD. `HasHeredoc` must key off the OPERATOR, never off a non-empty BODY. A
// herestring `<<<word` carries its word INLINE and records NO extent, so keying the
// flag off the body would drop the I2 Abstain floor for EVERY herestring — silently,
// and for a construct whose word can be an interpreter's program.
//
// The two pins ADR 0039 step 2 must not break are in heredoc_test.go
// (`{"cat <<<\"word\"", true}`) and parser_test.go (`{"herestring", "cmd <<<'input'",
// true}`); both pass UNEDITED. This adds the structural statement they imply.
func TestFlip_HerestringKeepsTheHeredocFloor(t *testing.T) {
	for _, src := range []string{
		`cat <<<"word"`,
		`cmd <<<'input'`,
		`cat <<<$VAR`,
		`cat <<<""`, // an EMPTY word is the case a body-keyed flag gets wrong twice over
	} {
		t.Run(src, func(t *testing.T) {
			sp := ParseShell(src)
			if sp.Unparseable {
				t.Fatalf("ParseShell failed: %s", sp.Reason)
			}
			floored := false
			for _, leaf := range sp.Leaves {
				if leaf.HasHeredoc {
					floored = true
				}
				if len(leaf.Heredocs) != 0 {
					t.Errorf("a herestring recorded an extent %+v; it has no body", leaf.Heredocs)
				}
			}
			if !floored {
				t.Fatalf("the I2 Abstain floor does not fire for %q: %s", src, dumpLeaves(sp.Leaves))
			}
		})
	}
}

// TestFlip_ArithmeticSubshellFallbackIsUnparseable pins a SEMANTIC GAP the parser
// does not cover, so it stops being emergent.
//
// Verified against bash 5.3.9: when the ARITHMETIC parse of `$((` fails, bash falls
// back to reading it as `$( (list) )` — a command substitution around a subshell. So
// `$((cmd) | cmd)` and `$((cmd) )` REALLY EXECUTE `cmd`. `$((cmd; cmd))` does NOT:
// `;` is not a valid arithmetic operator and bash reports an error, so that spelling
// is not a bypass.
//
// The upstream parser does not implement that fallback, so all three are Unparseable.
// Post
// pg2-zeqa5 that lands them on the I1a floor and ABSTAINs, which is the right
// DIRECTION — but the body is never ENUMERATED, so a sibling `Reject` inside one is
// FORFEITED. That is the I1b forfeiture class, and three corpus rows carry the shape.
// Pinning the verdict here is what keeps a future change from quietly turning
// "abstain because we could not read it" into "approve because we found nothing".
func TestFlip_ArithmeticSubshellFallbackIsUnparseable(t *testing.T) {
	for _, src := range []string{
		"echo $((cd /x && ls) | jq .)",
		"echo $((printf A) )",
		"AGENT_BEAD=$((cd ~/gt && bd list --json) | jq -r .x)",
	} {
		t.Run(src, func(t *testing.T) {
			sp := ParseShell(src)
			if !sp.Unparseable {
				t.Fatalf("ParseShell(%q) parsed; the bash `$( (list) )` fallback is not modelled, so this MUST be a parse failure whose verdict is the floor: %s",
					src, dumpLeaves(sp.Leaves))
			}
			if len(sp.Leaves) != 0 {
				t.Errorf("I1b: unparseable must carry NO leaves, got %d", len(sp.Leaves))
			}
			if sp.Reason == "" {
				t.Errorf("I10: a parse failure must report a reason")
			}
			// I10's other half: the parser did NOT attribute this to a dialect, so the
			// reason MUST NOT guess at one.
			if sp.Dialect != "" {
				t.Errorf("Dialect = %q; the parser attributed no dialect here, so none may be reported", sp.Dialect)
			}
		})
	}

	// The NON-bypass, kept so the test cannot pass by declaring every `$((` shape
	// unparseable and calling it done: `$((cmd; cmd))` is a bash arithmetic ERROR, so
	// nothing executes and no verdict is forfeited by refusing it.
	t.Run("$((cmd; cmd)) is a bash error, not a bypass", func(t *testing.T) {
		if sp := ParseShell("echo $((printf A; printf B))"); !sp.Unparseable {
			t.Errorf("want unparseable, got %s", dumpLeaves(sp.Leaves))
		}
	})

	// And a REAL arithmetic expansion must still parse, or the pin above would be
	// describing a much wider regression than it claims.
	t.Run("ordinary arithmetic still parses", func(t *testing.T) {
		for _, src := range []string{"echo $((1+2))", "echo $(( $(date +%s) - start ))", "echo $((i++))"} {
			if sp := ParseShell(src); sp.Unparseable {
				t.Errorf("ParseShell(%q) failed: %s", src, sp.Reason)
			}
		}
	})
}

// TestFlip_ProcessSubstitutionOperandChoice records WHICH of the two options ADR
// 0039 names was taken for the fabricated process-substitution operand, and why.
//
// ADR 0039's Consequences: "the fabricated `/dev/fd/63` operand for a process
// substitution is what stops the redirect-target check demoting the leaf, so emitting
// the substitution's source text instead causes mass new abstains while emitting
// nothing loses the operand".
//
// CHOICE: keep the fabricated `/dev/fd/63`, exactly as `tokenize` produced it.
// Reason: it is the only option that is neither a behaviour change nor a loss. The
// source text is not a path, so `hookio.IsSafeRedirectTarget` fails and
// patheval collapses it — mass new abstains on `diff <(a) <(b)`, a benign and very
// common idiom. Emitting nothing changes the ARGUMENT COUNT, which several rules key
// on (a leaf's operand position decides what a path-bearing rule inspects). The
// fabrication is confined to the seam, is the ONE fabricated token in the whole
// lowering, and the real body is carried separately in ProcessSubstitutions where the
// engine recurses it — so nothing is hidden, only named.
func TestFlip_ProcessSubstitutionOperandChoice(t *testing.T) {
	sp := ParseShell("diff -u <(cat x | jq .) <(cat y | jq .)")
	if sp.Unparseable {
		t.Fatalf("ParseShell failed: %s", sp.Reason)
	}
	if len(sp.Leaves) != 1 {
		t.Fatalf("want 1 leaf, got %s", dumpLeaves(sp.Leaves))
	}
	leaf := sp.Leaves[0]
	want := []string{"-u", "/dev/fd/63", "/dev/fd/63"}
	if len(leaf.Args) != len(want) {
		t.Fatalf("args = %v, want %v", leaf.Args, want)
	}
	for i := range want {
		if leaf.Args[i] != want[i] {
			t.Fatalf("args = %v, want %v", leaf.Args, want)
		}
	}
	// The BODIES are carried, verbatim and unsplit — the outgoing splitCompound cut
	// these at the inner `|` and lifted neither (LOWERING.md's third recorded
	// outgoing-front-end defect, 123 corpus rows).
	if len(leaf.ProcessSubstitutions) != 2 ||
		leaf.ProcessSubstitutions[0] != "cat x | jq ." ||
		leaf.ProcessSubstitutions[1] != "cat y | jq ." {
		t.Errorf("process substitutions = %q, want the two whole bodies", leaf.ProcessSubstitutions)
	}
}
