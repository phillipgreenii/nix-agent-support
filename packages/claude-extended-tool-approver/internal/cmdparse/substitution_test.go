package cmdparse

import (
	"reflect"
	"testing"
)

// bodies extracts just the Body strings from a Substitution slice for concise
// assertions.
func bodies(subs []Substitution) []string {
	out := make([]string, 0, len(subs))
	for _, s := range subs {
		out = append(out, s.Body)
	}
	return out
}

func TestEnumerateSubstitutions(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string // top-level substitution bodies, in order
	}{
		{"none", "echo hi", nil},
		{"single cmd sub", "echo $(git rev-parse HEAD)", []string{"git rev-parse HEAD"}},
		{"multiple cmd subs", "echo $(a) $(b)", []string{"a", "b"}},
		{"backtick", "echo `git status`", []string{"git status"}},
		{"process sub in", "diff <(sort a) <(sort b)", []string{"sort a", "sort b"}},
		{"process sub out", "tee >(wc -l) > /tmp/out", []string{"wc -l"}},
		// Top-level only: the nested inner is contained in the outer body and
		// surfaces on engine re-evaluation, not as a separate top-level entry.
		{"nested cmd sub returns outer body", "echo $(cat $(malicious))", []string{"cat $(malicious)"}},
		{"backtick nested in cmd sub", "echo $(cat `malicious`)", []string{"cat `malicious`"}},
		// pg2-1q5i3 depth-counter gotcha: the <( inside $() must NOT truncate the
		// body. The buggy classifier truncated to "cat <(rm -rf ~". The enumerator
		// must return the FULL body including the closing paren of the process sub.
		{"process sub nested in cmd sub not truncated", "echo $(cat <(rm -rf ~))", []string{"cat <(rm -rf ~)"}},
		{"grep with process sub nested", "echo $(grep x <(dangerous))", []string{"grep x <(dangerous)"}},
		{"out process sub nested in cmd sub", "echo $(cat >(dangerous))", []string{"cat >(dangerous)"}},
		{"deeply nested returns outer", "$(cat $(cat $(malicious)))", []string{"cat $(cat $(malicious))"}},
		// Single quotes are literal — no substitution happens, so nothing is enumerated.
		{"single quoted literal skipped", "echo '$(rm -rf ~)'", nil},
		{"single quoted backtick skipped", "echo '`rm -rf ~`'", nil},
		// Double quotes DO allow command substitution in bash — must be extracted.
		{"double quoted cmd sub extracted", `echo "$(nix run .#x -- --version)"`, []string{"nix run .#x -- --version"}},
		{"double quoted backtick extracted", "echo \"`git status`\"", []string{"git status"}},
		// Arithmetic $((...)) is NOT a command substitution.
		{"arithmetic skipped", "echo $((1+2))", nil},
		{"arithmetic then cmd sub", "echo $((1+2)) $(id)", []string{"id"}},
		// Escaped $ inside double quotes is literal.
		{"escaped dollar in double quotes", `echo "\$(rm -rf ~)"`, nil},
		// Parens inside single quotes inside a body must not truncate matching.
		{"literal paren in body", "echo $(echo ')')", []string{"echo ')'"}},
		// Unterminated substitution is skipped (best-effort safe default).
		{"unterminated cmd sub", "echo $(oops", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := bodies(EnumerateSubstitutions(tt.in))
			if len(got) == 0 && len(tt.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("EnumerateSubstitutions(%q) bodies = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

// TestScanSubstitutions_Unparseable pins the pg2-wguam contract: the scan REPORTS
// when it lost track of the text, instead of handing back a short list that reads
// as "nothing here".
//
// The distinction is the whole fix. EnumerateSubstitutions returns []Substitution,
// and the engine's fold is Approve iff nothing objects — so an empty list from a
// DESYNCED scan was an auto-approve of everything the scan skipped. One apostrophe
// of prose is enough to desync it: matchParen tracks quotes, so an unbalanced one
// inside a `$( )` means its closing paren is never found and the extent (with
// whatever it contains) is never enumerated.
func TestScanSubstitutions_Unparseable(t *testing.T) {
	tests := []struct {
		name            string
		in              string
		wantUnparseable bool
		wantBodies      []string
	}{
		// --- clean text: parseable, complete ---
		{"no substitutions", "echo hi", false, nil},
		{"balanced quotes around a sub", `echo "$(date)"`, false, []string{"date"}},
		{"apostrophe safely inside double quotes", `echo "the agent's note"`, false, nil},
		{"single-quoted region containing a double quote", `echo 'it"s'`, false, nil},
		{"escaped quote is not a quote", `echo \'`, false, nil},
		{"jq filter with parens inside single quotes", `echo "$(jq -r 'select(.a)' f)"`, false, []string{"jq -r 'select(.a)' f"}},

		// --- the reproduction: an apostrophe inside a $( ) body ---
		{"apostrophe inside a command substitution", "echo \"$(echo the agent's note)\"", true, nil},
		{"stray double quote inside a command substitution", "echo \"$(echo he said \"hi)\"", true, nil},
		// The killer detail: the SECOND, genuinely dangerous substitution is
		// discarded too, because the scan stops at the first desync.
		{"desync discards a later substitution", "echo \"$(echo don't)\" \"$(rm -rf .git/objects)\"", true, nil},

		// --- unterminated forms ---
		{"unterminated command substitution", "echo $(oops", true, nil},
		{"unterminated backtick", "echo `oops", true, nil},
		{"unterminated backtick discards a later sub", "echo `oops $(rm -rf ~)", true, nil},

		// --- a quote left open at end of input ---
		{"top-level unbalanced single quote", "echo don't ; rm -rf .git/objects", true, nil},
		{"top-level unbalanced double quote", `echo "hi`, true, nil},
		// A complete substitution BEFORE the desync is still reported; the flag is
		// what tells the caller the list is a prefix.
		{"substitution before the desync is kept", `echo "$(date)" 'oops`, true, []string{"date"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ScanSubstitutions(tt.in)
			if got.Unparseable != tt.wantUnparseable {
				t.Errorf("ScanSubstitutions(%q).Unparseable = %v, want %v (reason %q)",
					tt.in, got.Unparseable, tt.wantUnparseable, got.Reason)
			}
			if got.Unparseable && got.Reason == "" {
				t.Errorf("ScanSubstitutions(%q) is Unparseable with an empty Reason", tt.in)
			}
			if gotBodies := bodies(got.Substitutions); len(gotBodies) > 0 || len(tt.wantBodies) > 0 {
				if !reflect.DeepEqual(gotBodies, tt.wantBodies) {
					t.Errorf("ScanSubstitutions(%q) bodies = %v, want %v", tt.in, gotBodies, tt.wantBodies)
				}
			}
			// EnumerateSubstitutions keeps its old signature and its old bodies; it
			// simply drops the flag. Callers whose "no substitutions" branch is an
			// approval must use ScanSubstitutions instead.
			if !reflect.DeepEqual(EnumerateSubstitutions(tt.in), got.Substitutions) {
				t.Errorf("EnumerateSubstitutions(%q) diverged from ScanSubstitutions(%q).Substitutions", tt.in, tt.in)
			}
		})
	}
}

// TestScanSubstitutionsInHeredocBody_QuotesAreData pins the BODY expansion model
// (pg2-wguam): inside an unquoted heredoc body bash performs expansion but no
// quote removal, so a `'` is one literal apostrophe and must not open a quoted
// region that hides the rest of the body.
//
// Before this, the shell-text scan was used on bodies too, which made the verdict
// depend on where in the body the apostrophe sat — the order-dependent-verdict
// class heredocFloor's fold exists to eliminate.
func TestScanSubstitutionsInHeredocBody_QuotesAreData(t *testing.T) {
	tests := []struct {
		name            string
		body            string
		wantBodies      []string
		wantUnparseable bool
	}{
		{"apostrophe before the substitution", "don't\n$(rm -rf .git/objects)\n", []string{"rm -rf .git/objects"}, false},
		{"apostrophe after the substitution", "$(rm -rf .git/objects)\ndon't\n", []string{"rm -rf .git/objects"}, false},
		{"stray double quote before the substitution", "he said \"hi\n$(rm -rf .git/objects)\n", []string{"rm -rf .git/objects"}, false},
		// Quotes are data, so a "single-quoted" span in a body does NOT make its
		// substitution inert — bash still expands it.
		{"apparent single-quoted span still expands", "'$(rm -rf .git/objects)'\n", []string{"rm -rf .git/objects"}, false},
		{"no substitution, prose only", "the agent's note\n", nil, false},
		// Backslash escaping IS honored in a body: `\$(x)` is literal text.
		{"escaped dollar is literal", "\\$(rm -rf .git/objects)\n", nil, false},
		// An unterminated `$(` or backtick still leaves the extent unknown.
		{"unterminated substitution in a body", "$(oops\n", nil, true},
		{"unterminated backtick in a body", "`oops\n", nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ScanSubstitutionsInHeredocBody(tt.body)
			if got.Unparseable != tt.wantUnparseable {
				t.Errorf("ScanSubstitutionsInHeredocBody(%q).Unparseable = %v, want %v",
					tt.body, got.Unparseable, tt.wantUnparseable)
			}
			if gotBodies := bodies(got.Substitutions); len(gotBodies) > 0 || len(tt.wantBodies) > 0 {
				if !reflect.DeepEqual(gotBodies, tt.wantBodies) {
					t.Errorf("ScanSubstitutionsInHeredocBody(%q) bodies = %v, want %v", tt.body, gotBodies, tt.wantBodies)
				}
			}
		})
	}
}

// TestScanSubstitutions_UnparseableStillEnumeratesItsPrefix is the ANTI-FORFEITURE
// guard for ADR 0039 step 2a (pg2-zeqa5).
//
// The seam's strict parser yields NO tree for text it rejects, so the naive
// migration would have returned zero bodies wherever the old byte loop returned the
// ones it had already found. Any Reject one of those bodies would have earned would
// then be replaced by the unparseable NoOpinion floor — a transition in the
// LESS-RESTRICTIVE direction under `Approve < NoOpinion < Ask < Reject`, which ADR
// 0039's Enforcement gate forbids.
//
// The heredoc-stripped leaf Raw is the shape that makes this load-bearing rather
// than theoretical. On the engine's path a heredoc-bearing leaf's Raw has its BODY
// removed, so the text reaching ScanSubstitutions ends at an unclosed
// here-document and cannot parse — while the `$( )` on the command line is a real
// substitution with an exact extent that MUST still be recursed.
//
// Both halves are asserted together on purpose: the flag MUST stay set (that is
// I1a's floor, and the recovering parser must never be allowed to clear it) AND the
// body MUST still be enumerated.
func TestScanSubstitutions_UnparseableStillEnumeratesItsPrefix(t *testing.T) {
	tests := []struct {
		name       string
		in         string
		wantBodies []string
	}{
		{
			// The engine's actual heredoc-bearing leaf Raw, post-strip.
			name:       "heredoc-stripped raw keeps its command-line substitution",
			in:         "cmd $(rm -rf /) <<EOF",
			wantBodies: []string{"rm -rf /"},
		},
		{
			name:       "heredoc-stripped raw with no substitution",
			in:         "cat <<EOF",
			wantBodies: nil,
		},
		{
			name:       "a complete substitution before an unbalanced quote is kept",
			in:         `echo "$(date)" 'oops`,
			wantBodies: []string{"date"},
		},
		{
			// The closing delimiter is MISSING, so the extent is unknown and nothing
			// inside it has been enumerated. Salvaging a body here would be inventing
			// one — this is the pg2-wguam rule, expressed as a recovered-position check.
			name:       "an unterminated substitution contributes no body",
			in:         "echo $(oops",
			wantBodies: nil,
		},
		{
			name:       "an unterminated backtick contributes no body",
			in:         "echo `oops $(rm -rf ~)",
			wantBodies: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ScanSubstitutions(tt.in)
			if !got.Unparseable {
				t.Fatalf("ScanSubstitutions(%q).Unparseable = false, want true: the recovering parser must never clear the I1a floor", tt.in)
			}
			if got.Reason == "" {
				t.Errorf("ScanSubstitutions(%q) is Unparseable with an empty Reason", tt.in)
			}
			if gotBodies := bodies(got.Substitutions); len(gotBodies) > 0 || len(tt.wantBodies) > 0 {
				if !reflect.DeepEqual(gotBodies, tt.wantBodies) {
					t.Errorf("ScanSubstitutions(%q) bodies = %v, want %v", tt.in, gotBodies, tt.wantBodies)
				}
			}
		})
	}
}

// TestScanSubstitutions_DoesNotScanHeredocBodiesAsShellText pins the seam's
// division of labour between the two expansion models.
//
// A heredoc body's quotes are DATA, not syntax, so scanning it as shell text is
// exactly the pg2-wguam mis-model. The body therefore belongs to
// ScanSubstitutionsInHeredocBody, and on the engine's path to
// evaluateHeredocBodies — which is also what keeps the same body from being
// recursed twice under two different models.
func TestScanSubstitutions_DoesNotScanHeredocBodiesAsShellText(t *testing.T) {
	const cmd = "cat <<EOF\n$(rm -rf .git/objects)\nEOF\n"
	scan := ScanSubstitutions(cmd)
	if scan.Unparseable {
		t.Fatalf("ScanSubstitutions(%q).Unparseable = true, want false (the text is valid bash)", cmd)
	}
	if len(scan.Substitutions) != 0 {
		t.Errorf("ScanSubstitutions(%q) = %v, want no substitutions: a heredoc BODY is not shell text", cmd, bodies(scan.Substitutions))
	}
	// The body model DOES see it — that is the entry point that owns it.
	body := ScanSubstitutionsInHeredocBody("$(rm -rf .git/objects)\n")
	if len(body.Substitutions) != 1 || body.Substitutions[0].Body != "rm -rf .git/objects" {
		t.Errorf("ScanSubstitutionsInHeredocBody = %v, want one body %q", bodies(body.Substitutions), "rm -rf .git/objects")
	}
	// A redirection TARGET, unlike a heredoc body, IS shell text and is still scanned.
	if got := bodies(EnumerateSubstitutions("cmd > $(mktemp)")); !reflect.DeepEqual(got, []string{"mktemp"}) {
		t.Errorf("EnumerateSubstitutions(%q) = %v, want [mktemp]", "cmd > $(mktemp)", got)
	}
}

// TestIsSafeSubstitutionBody_OnlyASoleSimpleCommand pins the shape TIGHTENING that
// ADR 0039 step 2a applied to the static allowlist floor.
//
// The outgoing test was "Parse yields exactly one leaf", and its quote-awareness was
// a SIDE EFFECT of splitCompound happening to split top-level compound operators. A
// real grammar makes the same leaf count admit shapes the floor was never meant to
// clear: `(cat VERSION)`, `{ cat VERSION; }` and `if true; then cat VERSION; fi` all
// reduce to ONE command leaf. Clearing those would be a move in the LESS-RESTRICTIVE
// direction, so the seam requires the sole statement to BE a simple command.
//
// Every row here is a body whose EXECUTABLE is on the static allowlist, so the only
// thing that can reject it is the shape test.
func TestIsSafeSubstitutionBody_OnlyASoleSimpleCommand(t *testing.T) {
	tests := []struct {
		name string
		body string
		safe bool
	}{
		{"a bare simple command is safe", "cat VERSION", true},
		{"a subshell is not a simple command", "(cat VERSION)", false},
		{"a brace group is not a simple command", "{ cat VERSION; }", false},
		{"a conditional is not a simple command", "if true; then cat VERSION; fi", false},
		{"a loop is not a simple command", "for f in a; do cat VERSION; done", false},
		{"a pipeline is not a simple command", "cat VERSION | cat", false},
		{"an && list is not a simple command", "cat VERSION && cat VERSION", false},
		{"two statements are not one command", "cat VERSION; cat VERSION", false},
		{"a negated command is refused", "! cat VERSION", false},
		{"a backgrounded command is refused", "cat VERSION &", false},
		{"a redirected command is refused", "cat VERSION > /tmp/x", false},
		{"a leading assignment is refused", "LC_ALL=C cat VERSION", false},
		{"an arithmetic command is not a simple command", "(( 1 + 1 ))", false},
		{"a test clause is not a simple command", "[[ -f VERSION ]]", false},
		// The `env`/`command` exec prefix is UNWRAPPED by the shared lowering, so it
		// stays a simple command and the allowlist judges the real executable.
		{"an env exec prefix still resolves to the real command", "command cat VERSION", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsSafeSubstitutionBody(tt.body); got != tt.safe {
				t.Errorf("IsSafeSubstitutionBody(%q) = %v, want %v", tt.body, got, tt.safe)
			}
		})
	}
}

func TestEnumerateSubstitutions_Kinds(t *testing.T) {
	got := EnumerateSubstitutions("a $(cmd) `bt` <(pin) >(pout)")
	want := []Substitution{
		{Kind: SubstCommand, Body: "cmd"},
		{Kind: SubstBacktick, Body: "bt"},
		{Kind: SubstProcessIn, Body: "pin"},
		{Kind: SubstProcessOut, Body: "pout"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("EnumerateSubstitutions kinds = %+v, want %+v", got, want)
	}
	// Command substitutions ($()/backtick) are the ones governed by the static
	// allowlist floor; process substitutions are not.
	for _, s := range got {
		wantCmd := s.Kind == SubstCommand || s.Kind == SubstBacktick
		if s.IsCommandSubstitution() != wantCmd {
			t.Errorf("%+v IsCommandSubstitution = %v, want %v", s, s.IsCommandSubstitution(), wantCmd)
		}
	}
}

// TestProcessSubstitutionsFieldMissesTruncationCase is the guard the AC requires:
// a fix that relies SOLELY on leaf.ProcessSubstitutions misses `$(cat <(rm -rf ~))`,
// because the parser's depth counter (which ignores the '(' in '<(') never
// populates ProcessSubstitutions for a process sub nested inside a command sub.
// The raw-text enumerator is what catches it.
func TestProcessSubstitutionsFieldMissesTruncationCase(t *testing.T) {
	const cmd = "$(cat <(rm -rf ~))"
	parsed := Parse(cmd)
	if len(parsed) != 1 {
		t.Fatalf("Parse(%q): got %d leaves, want 1", cmd, len(parsed))
	}
	// Relying solely on this field WOULD miss the dangerous inner process sub.
	if len(parsed[0].ProcessSubstitutions) != 0 {
		t.Fatalf("precondition: ProcessSubstitutions = %v, want empty (proves the field is unpopulated for the nested case)", parsed[0].ProcessSubstitutions)
	}
	// The enumerator DOES catch it: the outer $() body is returned in full
	// (not truncated to "cat <(rm -rf ~"), so re-evaluation exposes the <(rm -rf ~).
	subs := EnumerateSubstitutions(cmd)
	if len(subs) != 1 || subs[0].Body != "cat <(rm -rf ~)" {
		t.Fatalf("EnumerateSubstitutions(%q) = %v, want one body %q", cmd, bodies(subs), "cat <(rm -rf ~)")
	}
	// And the static classifier now flags the whole thing unsafe (was false before the fix).
	if !HasUnsafeCommandSubstitution(cmd) {
		t.Errorf("HasUnsafeCommandSubstitution(%q) = false, want true (truncation no longer hides the process sub)", cmd)
	}
}

func TestIsSafeSubstitutionBody_NestedRejected(t *testing.T) {
	tests := []struct {
		body string
		safe bool
	}{
		{"cat VERSION", true},
		{"git rev-parse HEAD", true},
		{"mktemp", true},
		{"date +%F", true},
		{"grep -E 'a|b' file", true},
		// Bodies containing a nested substitution are never statically safe.
		{"cat $(malicious)", false},
		{"cat `malicious`", false},
		{"cat <(rm -rf ~)", false},
		{"grep x <(dangerous)", false},
		// git show/diff/log excluded from the static allowlist (RCE floor).
		{"git show HEAD", false},
		{"git diff", false},
		{"git log", false},
		// rm is not a safe substitution command.
		{"rm -rf ~/x", false},
		// An UNPARSEABLE body is never statically safe (pg2-wguam). Reachable via a
		// backtick body, whose extent scan is not quote-aware: the body below still
		// reduces to one safe-looking `echo` leaf, so without the unparseable check
		// the static-allowlist floor would clear text nobody enumerated.
		{"echo don't", false},
		{`echo "unterminated`, false},
		{"echo `oops", false},
	}
	for _, tt := range tests {
		if got := IsSafeSubstitutionBody(tt.body); got != tt.safe {
			t.Errorf("IsSafeSubstitutionBody(%q) = %v, want %v", tt.body, got, tt.safe)
		}
	}
}

func TestStripLeadingEnvAssignments(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"echo hi", "echo hi"},
		{"FOO=bar echo hi", "echo hi"},
		{"FOO=1 BAR=2 cmd arg", "cmd arg"},
		// Env value containing a command substitution (with an inner space) is one token.
		{"FOO=$(curl evil) echo hi", "echo hi"},
		{"PATH=/x:$PATH make", "make"},
		// A NAME=VALUE that is an ARGUMENT (after the exec) is not stripped.
		{"cmd FOO=bar", "cmd FOO=bar"},
		{"echo a=b", "echo a=b"},
		// No command (all env) → empty command portion.
		{"FOO=bar", ""},
	}
	for _, tt := range tests {
		if got := StripLeadingEnvAssignments(tt.in); got != tt.want {
			t.Errorf("StripLeadingEnvAssignments(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// TestClassifyExpansion_NestedUnknown is the env-assignment-path AC: a nested
// substitution inside an env value must classify ExpansionUnknown (the naive
// classifier wrongly returned ExpansionSafeCmd).
func TestClassifyExpansion_NestedUnknown(t *testing.T) {
	tests := []struct {
		value string
		want  ExpansionKind
	}{
		{"$(cat $(malicious))", ExpansionUnknown},
		{"$(cat <(rm -rf ~))", ExpansionUnknown},
		{"$(cat `malicious`)", ExpansionUnknown},
		{"$(cat $(mktemp))", ExpansionUnknown},
		// Non-nested safe forms still classify SafeCmd.
		{"$(mktemp)", ExpansionSafeCmd},
		{"$(git rev-parse HEAD)", ExpansionSafeCmd},
		{"$(cat VERSION)", ExpansionSafeCmd},
	}
	for _, tt := range tests {
		if got := classifyExpansion(tt.value); got != tt.want {
			t.Errorf("classifyExpansion(%q) = %d, want %d", tt.value, got, tt.want)
		}
	}
}
