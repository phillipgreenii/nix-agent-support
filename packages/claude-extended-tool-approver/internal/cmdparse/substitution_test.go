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
