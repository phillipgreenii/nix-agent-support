package cmdparse

import (
	"reflect"
	"testing"
)

func TestHasShortFlag(t *testing.T) {
	tests := []struct {
		name string
		args []string
		flag byte
		want bool
	}{
		// Bare and clustered shorts — the forms the git rule cannot see today.
		{"bare short", []string{"-f", "origin", "main"}, 'f', true},
		{"cluster leading", []string{"-fd"}, 'f', true},
		{"cluster leading of three", []string{"-fdx"}, 'f', true},
		{"cluster trailing", []string{"-xdf"}, 'f', true},
		{"cluster push -fu", []string{"push", "-fu", "origin", "main"}, 'f', true},
		{"cluster non-first member", []string{"-fdx"}, 'd', true},
		{"short later in args", []string{"origin", "main", "-f"}, 'f', true},
		// Long flags are never short clusters.
		{"long force is not a cluster", []string{"--force"}, 'f', false},
		{"long --f is not a cluster", []string{"--f"}, 'f', false},
		{"long with glued value", []string{"--force-with-lease=main"}, 'f', false},
		// End-of-options terminator stops the scan.
		{"after terminator", []string{"--", "-f"}, 'f', false},
		{"before terminator still seen", []string{"-f", "--", "x"}, 'f', true},
		{"terminator then cluster", []string{"push", "--", "-fd"}, 'd', false},
		// Non-flag shapes.
		{"absent", []string{"push", "origin", "main"}, 'f', false},
		{"lone dash is an operand", []string{"-"}, 'f', false},
		{"empty args", nil, 'f', false},
		{"operand containing the letter", []string{"push", "origin", "feature"}, 'f', false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := HasShortFlag(tt.args, tt.flag); got != tt.want {
				t.Errorf("HasShortFlag(%v, %q) = %v, want %v", tt.args, string(tt.flag), got, tt.want)
			}
		})
	}
}

func TestHasLongFlag(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		flag      string
		wantValue string
		wantOK    bool
	}{
		{"bare long", []string{"--force-with-lease", "origin"}, "force-with-lease", "", true},
		{"glued long", []string{"--force-with-lease=main:abc123", "origin"}, "force-with-lease", "main:abc123", true},
		{"glued long other branch", []string{"push", "--force-with-lease=other", "origin", "main:other"}, "force-with-lease", "other", true},
		{"name may carry its dashes", []string{"--force"}, "--force", "", true},
		{"glued empty value", []string{"--force-with-lease="}, "force-with-lease", "", true},
		{"absent", []string{"push", "origin", "main"}, "force", "", false},
		{"prefix must be exact", []string{"--force-with-lease"}, "force", "", false},
		{"after terminator not matched", []string{"--", "--force"}, "force", "", false},
		{"before terminator matched", []string{"--force", "--", "x"}, "force", "", true},
		{"empty name", []string{"--force"}, "", "", false},
		{"empty args", nil, "force", "", false},
		// Separated value form: present, but the value is NOT returned (no arity table).
		{"separated value not returned", []string{"--repo", "origin"}, "repo", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			value, ok := HasLongFlag(tt.args, tt.flag)
			if value != tt.wantValue || ok != tt.wantOK {
				t.Errorf("HasLongFlag(%v, %q) = (%q, %v), want (%q, %v)", tt.args, tt.flag, value, ok, tt.wantValue, tt.wantOK)
			}
		})
	}
}

// TestHasLongFlagPrefix pins the abbreviation matcher pg2-os1kq added. The rows
// marked MEASURED are spellings verified against real git 2.54.0 on 2026-07-30; the
// rows marked OVER-MATCH are spellings git itself refuses as ambiguous and this
// matcher deliberately still reports, because for a dangerous-flag boolean test that
// error is in the fail-safe direction (see the function's doc).
func TestHasLongFlagPrefix(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		canonical string
		want      bool
	}{
		// MEASURED: real git performs the hard reset for all four of these.
		{"full name", []string{"--hard", "HEAD~1"}, "hard", true},
		{"one char short", []string{"--har", "HEAD~1"}, "hard", true},
		{"two chars short", []string{"--ha", "HEAD~1"}, "hard", true},
		{"single char", []string{"--h", "HEAD~1"}, "hard", true},
		// MEASURED: git parses these as --interactive; `--i` is ambiguous to git.
		{"interactive abbreviated", []string{"--interactiv"}, "interactive", true},
		{"interactive two chars", []string{"--in"}, "interactive", true},
		// OVER-MATCH, fail-safe: git answers `ambiguous option: i` for this one.
		{"interactive single char (git: ambiguous)", []string{"--i"}, "interactive", true},
		// MEASURED: git parses --force-with-lease down to --force-w.
		{"force-with-lease abbreviated", []string{"--force-w", "origin"}, "force-with-lease", true},
		// A LONGER token never matches a shorter canonical — this is what keeps the
		// separate `--force` and `--force-with-lease` gates from collapsing into one.
		{"longer token vs shorter canonical", []string{"--force-with-lease"}, "force", false},
		{"unrelated longer token", []string{"--dry-run"}, "delete", false},
		{"different first letter", []string{"--soft"}, "hard", false},
		// The `=`-glued form matches on the part BEFORE the '='; no value is returned.
		{"glued value on an abbreviation", []string{"--force-with-lea=main:abc123"}, "force-with-lease", true},
		{"glued value on the full name", []string{"--hard=x"}, "hard", true},
		{"glued with empty flag name", []string{"--=x"}, "hard", false},
		// A negation is not the flag.
		{"no- negation", []string{"--no-hard"}, "hard", false},
		{"no- negation abbreviated", []string{"--no-h"}, "hard", false},
		// The `--` end-of-options terminator: after it every token is an operand.
		{"after terminator not matched", []string{"--", "--hard"}, "hard", false},
		{"after terminator abbreviated", []string{"--", "--har"}, "hard", false},
		{"before terminator matched", []string{"--har", "--", "x"}, "hard", true},
		{"bare terminator alone", []string{"--"}, "hard", false},
		// Shorts, a lone `-`, and operands are never long flags.
		{"short flag ignored", []string{"-h"}, "hard", false},
		{"cluster ignored", []string{"-fh"}, "hard", false},
		{"lone dash ignored", []string{"-"}, "hard", false},
		{"operand ignored", []string{"hard"}, "hard", false},
		// canonical MAY carry its own dashes; an empty canonical never matches.
		{"canonical with dashes", []string{"--har"}, "--hard", true},
		{"empty canonical", []string{"--hard"}, "", false},
		{"empty canonical spelled as dashes", []string{"--hard"}, "--", false},
		{"empty args", nil, "hard", false},
		// Case is significant, as it is for git's long options.
		{"case sensitive", []string{"--HARD"}, "hard", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := HasLongFlagPrefix(tt.args, tt.canonical); got != tt.want {
				t.Errorf("HasLongFlagPrefix(%v, %q) = %v, want %v", tt.args, tt.canonical, got, tt.want)
			}
		})
	}
}

// TestHasLongFlagPrefix_MatchesEveryPrefixOfCanonical is the property the callers
// rely on and the reason the primitive exists at all: no abbreviation of a gated flag
// can be missed, so no enumeration has to be kept in step with git's option table.
func TestHasLongFlagPrefix_MatchesEveryPrefixOfCanonical(t *testing.T) {
	for _, canonical := range []string{"hard", "interactive", "force", "mirror", "delete", "force-with-lease"} {
		for n := 1; n <= len(canonical); n++ {
			tok := "--" + canonical[:n]
			if !HasLongFlagPrefix([]string{tok, "origin", "main"}, canonical) {
				t.Errorf("HasLongFlagPrefix([%q …], %q) = false, want true — every prefix must match", tok, canonical)
			}
		}
	}
}

// TestHasAbbrevLongFlag pins the measured-minimum matcher promoted for
// pg2-1xq3m (safecmds' `cp --target-directory`, whose glued value is
// load-bearing and so needs the BOUNDED matcher rather than HasLongFlagPrefix's
// open one).
func TestHasAbbrevLongFlag(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		flag      string
		minLen    int
		wantValue string
		wantOK    bool
	}{
		{"full name", []string{"--target-directory", "/tmp"}, "target-directory", 1, "", true},
		{"one char short", []string{"--target-director", "/tmp"}, "target-directory", 1, "", true},
		{"down to minLen", []string{"--t", "/tmp"}, "target-directory", 1, "", true},
		{"glued value on abbreviation", []string{"--target-d=/tmp"}, "target-directory", 1, "/tmp", true},
		{"glued value on minimal spelling", []string{"--t=/tmp"}, "target-directory", 1, "/tmp", true},
		{"bare terminator is not a flag at any n", []string{"--"}, "target-directory", 1, "", false},
		// "--tar" IS a genuine prefix of "target-directory" (3 chars), but
		// minLen=4 means n never drops below 4, so this 3-char spelling is
		// correctly never tried and must not match.
		{"below minLen never matches even though it is a real prefix", []string{"--tar"}, "target-directory", 4, "", false},
		{"absent", []string{"push", "origin"}, "target-directory", 1, "", false},
		{"longer token vs shorter canonical", []string{"--target-directoryx"}, "target-directory", 1, "", false},
		// LONGEST FIRST: when two candidate prefixes are both technically present
		// as literal args, the longer (more specific) spelling's value wins.
		{"longest spelling wins", []string{"--t=short", "--target-directory=long"}, "target-directory", 1, "long", true},
		{"empty args", nil, "target-directory", 1, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			value, ok := HasAbbrevLongFlag(tt.args, tt.flag, tt.minLen)
			if value != tt.wantValue || ok != tt.wantOK {
				t.Errorf("HasAbbrevLongFlag(%v, %q, %d) = (%q, %v), want (%q, %v)", tt.args, tt.flag, tt.minLen, value, ok, tt.wantValue, tt.wantOK)
			}
		})
	}
}

func TestFirstOperand(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		wantTok   string
		wantIndex int
	}{
		// The pinned `git remote -v add upstream <url>` case: a leading flag must
		// NOT displace the subcommand, and must NOT be treated as consuming it.
		{"leading short flag", []string{"-v", "add", "upstream", "https://x/y.git"}, "add", 1},
		{"no flags", []string{"add", "upstream"}, "add", 0},
		{"leading long flag", []string{"--global", "clean.requireForce", "false"}, "clean.requireForce", 1},
		{"leading glued long flag", []string{"--file=/tmp/x", "key", "value"}, "key", 1},
		{"leading cluster", []string{"-fu", "origin", "main"}, "origin", 1},
		{"only flags", []string{"-f", "--force"}, "", -1},
		{"empty args", nil, "", -1},
		{"lone dash is an operand", []string{"-"}, "-", 0},
		{"after terminator", []string{"--force", "--", "-weird"}, "-weird", 2},
		{"terminator with nothing after", []string{"-f", "--"}, "", -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tok, idx := FirstOperand(tt.args)
			if tok != tt.wantTok || idx != tt.wantIndex {
				t.Errorf("FirstOperand(%v) = (%q, %d), want (%q, %d)", tt.args, tok, idx, tt.wantTok, tt.wantIndex)
			}
		})
	}
}

// TestOperands pins the whole-list operand walk pg2-szadj's `git config` gate
// needs. The `separated flag value` rows are the load-bearing ones: Operands
// documents that it does NOT model flag arity, so a separated value is returned as
// an extra operand. That over-collection is what makes the returned slice a
// SUPERSET of the real operands and therefore safe for a gate to scan — a caller
// cannot lose the token it is looking for to an arity trick. A "fix" that skipped
// separated values would break that property and these rows would catch it.
func TestOperands(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{"no flags", []string{"key", "value"}, []string{"key", "value"}},
		{"leading long flag", []string{"--global", "clean.requireForce", "false"}, []string{"clean.requireForce", "false"}},
		{"glued long flag", []string{"--type=bool", "clean.requireForce", "false"}, []string{"clean.requireForce", "false"}},
		{"flags interleaved", []string{"--global", "set", "--local", "core.hooksPath", "/tmp/h"}, []string{"set", "core.hooksPath", "/tmp/h"}},
		{"separated short flag value is an operand", []string{"-f", "cfg", "core.hooksPath", "/tmp/h"}, []string{"cfg", "core.hooksPath", "/tmp/h"}},
		{"separated long flag value is an operand", []string{"--type", "bool", "clean.requireForce", "false"}, []string{"bool", "clean.requireForce", "false"}},
		{"only flags", []string{"--list", "-z"}, nil},
		{"empty args", nil, nil},
		{"lone dash is an operand", []string{"-l", "-"}, []string{"-"}},
		{"after terminator every token is an operand", []string{"--unset", "--", "-weird", "-x"}, []string{"-weird", "-x"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Operands(tt.args)
			if len(got) != len(tt.want) {
				t.Fatalf("Operands(%v) = %v, want %v", tt.args, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("Operands(%v) = %v, want %v", tt.args, got, tt.want)
				}
			}
		})
	}
}

// TestOperandsAgreesWithFirstOperand pins the shared-walk property: Operands and
// FirstOperand run the same operandIndexes scan, so the first element of one MUST
// be the other's answer. Two independent walks would be free to disagree about
// what counts as a flag, which is the drift this guards.
func TestOperandsAgreesWithFirstOperand(t *testing.T) {
	cases := [][]string{
		{"key", "value"},
		{"--global", "clean.requireForce", "false"},
		{"-v", "add", "upstream", "https://x/y.git"},
		{"-f", "cfg", "core.hooksPath", "/tmp/h"},
		{"--force", "--", "-weird"},
		{"-f", "--force"},
		nil,
	}
	for _, args := range cases {
		first, _ := FirstOperand(args)
		ops := Operands(args)
		var wantFirst string
		if len(ops) > 0 {
			wantFirst = ops[0]
		}
		if first != wantFirst {
			t.Errorf("args %v: FirstOperand = %q but Operands[0] = %q — the two walks disagree", args, first, wantFirst)
		}
	}
}

func TestClassifyPushRefspecs(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []Refspec
	}{
		{"no operands at all", []string{}, nil},
		{"flags only", []string{"--force"}, nil},
		{"remote only is not a refspec", []string{"origin"}, nil},
		{
			"force via plus prefix",
			[]string{"origin", "+main"},
			[]Refspec{{Raw: "+main", Force: true, Src: "main"}},
		},
		{
			"delete via empty source",
			[]string{"origin", ":main"},
			[]Refspec{{Raw: ":main", Delete: true, Src: "", Dst: "main", HasDst: true}},
		},
		{
			"cross-branch",
			[]string{"origin", "main:other"},
			[]Refspec{{Raw: "main:other", Src: "main", Dst: "other", HasDst: true}},
		},
		{
			"same-branch bare src",
			[]string{"origin", "main"},
			[]Refspec{{Raw: "main", Src: "main"}},
		},
		{
			"same-branch explicit",
			[]string{"origin", "main:main"},
			[]Refspec{{Raw: "main:main", Src: "main", Dst: "main", HasDst: true}},
		},
		{
			"HEAD source",
			[]string{"origin", "HEAD:main"},
			[]Refspec{{Raw: "HEAD:main", Src: "HEAD", Dst: "main", HasDst: true}},
		},
		{
			"force cross-branch",
			[]string{"origin", "+src:dst"},
			[]Refspec{{Raw: "+src:dst", Force: true, Src: "src", Dst: "dst", HasDst: true}},
		},
		{
			"force delete",
			[]string{"origin", "+:main"},
			[]Refspec{{Raw: "+:main", Force: true, Delete: true, Src: "", Dst: "main", HasDst: true}},
		},
		{
			"leading flags do not displace the remote",
			[]string{"-fu", "origin", "+main"},
			[]Refspec{{Raw: "+main", Force: true, Src: "main"}},
		},
		{
			"multiple refspecs",
			[]string{"origin", "main", ":stale", "+hot:hot"},
			[]Refspec{
				{Raw: "main", Src: "main"},
				{Raw: ":stale", Delete: true, Dst: "stale", HasDst: true},
				{Raw: "+hot:hot", Force: true, Src: "hot", Dst: "hot", HasDst: true},
			},
		},
		{
			"fully qualified refs split on first colon only",
			[]string{"origin", "refs/heads/main:refs/heads/other"},
			[]Refspec{{Raw: "refs/heads/main:refs/heads/other", Src: "refs/heads/main", Dst: "refs/heads/other", HasDst: true}},
		},
		{
			"empty dst is not a delete",
			[]string{"origin", "main:"},
			[]Refspec{{Raw: "main:", Src: "main", Dst: "", HasDst: true}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifyPushRefspecs(tt.args)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ClassifyPushRefspecs(%v) = %+v, want %+v", tt.args, got, tt.want)
			}
		})
	}
}

// TestRefspecSameRef pins the "no refspec given" vs "refspec present and
// same-branch" distinction a consumer must be able to draw, plus the deliberate
// HEAD over-approximation.
func TestRefspecSameRef(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []bool // one entry per returned refspec
	}{
		{"no refspec given", []string{"origin"}, nil},
		{"same-branch bare", []string{"origin", "main"}, []bool{true}},
		{"same-branch explicit", []string{"origin", "main:main"}, []bool{true}},
		{"cross-branch", []string{"origin", "main:other"}, []bool{false}},
		{"HEAD is over-approximated as cross-branch", []string{"origin", "HEAD:main"}, []bool{false}},
		{"delete is not same-ref", []string{"origin", ":main"}, []bool{false}},
		{"force same-branch", []string{"origin", "+main"}, []bool{true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			specs := ClassifyPushRefspecs(tt.args)
			if len(specs) != len(tt.want) {
				t.Fatalf("ClassifyPushRefspecs(%v) returned %d refspecs, want %d", tt.args, len(specs), len(tt.want))
			}
			for i, spec := range specs {
				if got := spec.SameRef(); got != tt.want[i] {
					t.Errorf("Refspec(%q).SameRef() = %v, want %v", spec.Raw, got, tt.want[i])
				}
			}
		})
	}
}

// TestGitHelpersOnMeasuredHoles pins the primitive against the exact invocations
// bead pg2-si0bp measured as auto-approved on main. It asserts only that the
// PRIMITIVE now SEES each form — the verdict change is each consumer bead's job
// (pg2-bohpm, pg2-8imjo, pg2-szadj), so nothing here touches internal/rules/git.
func TestGitHelpersOnMeasuredHoles(t *testing.T) {
	// `git push -fu origin main` — clustered short flag.
	_, sub, rest := GitInvocation([]string{"push", "-fu", "origin", "main"})
	if sub != "push" || !HasShortFlag(rest, 'f') {
		t.Errorf("clustered -fu: sub=%q HasShortFlag(f)=%v, want push/true", sub, HasShortFlag(rest, 'f'))
	}

	// `git push --force-with-lease=other origin main:other` — =-glued long flag
	// whose value names a DIFFERENT branch, plus a cross-branch refspec.
	_, sub, rest = GitInvocation([]string{"push", "--force-with-lease=other", "origin", "main:other"})
	value, ok := HasLongFlag(rest, "force-with-lease")
	if sub != "push" || !ok || value != "other" {
		t.Errorf("glued --force-with-lease: sub=%q value=%q ok=%v, want push/\"other\"/true", sub, value, ok)
	}
	specs := ClassifyPushRefspecs(rest)
	if len(specs) != 1 || specs[0].SameRef() {
		t.Errorf("glued --force-with-lease refspecs = %+v, want one cross-branch refspec", specs)
	}

	// `git push origin +main` — force via refspec prefix, no flag at all.
	_, _, rest = GitInvocation([]string{"push", "origin", "+main"})
	specs = ClassifyPushRefspecs(rest)
	if len(specs) != 1 || !specs[0].Force {
		t.Errorf("+main refspecs = %+v, want one forced refspec", specs)
	}

	// `git push origin :main` — remote-ref delete, no flag at all.
	_, _, rest = GitInvocation([]string{"push", "origin", ":main"})
	specs = ClassifyPushRefspecs(rest)
	if len(specs) != 1 || !specs[0].Delete {
		t.Errorf(":main refspecs = %+v, want one delete refspec", specs)
	}

	// `git remote -v add upstream <url>` — leading flag displaces rest[0].
	_, sub, rest = GitInvocation([]string{"remote", "-v", "add", "upstream", "https://x/y.git"})
	if op, idx := FirstOperand(rest); sub != "remote" || op != "add" || idx != 1 {
		t.Errorf("remote -v add: sub=%q FirstOperand=(%q,%d), want remote/(\"add\",1)", sub, op, idx)
	}

	// `git config --global clean.requireForce false` — leading flag displaces the key.
	_, sub, rest = GitInvocation([]string{"config", "--global", "clean.requireForce", "false"})
	if op, idx := FirstOperand(rest); sub != "config" || op != "clean.requireForce" || idx != 1 {
		t.Errorf("config --global: sub=%q FirstOperand=(%q,%d), want config/(\"clean.requireForce\",1)", sub, op, idx)
	}
}

func TestGitInvocation(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantChdirs []string
		wantSub    string
		wantRest   []string
	}{
		{"plain commit", []string{"commit", "-m", "x"}, nil, "commit", []string{"-m", "x"}},
		{"dash-C", []string{"-C", "/repo", "commit"}, []string{"/repo"}, "commit", []string{}},
		{"chained dash-C", []string{"-C", "a", "-C", "b", "status"}, []string{"a", "b"}, "status", []string{}},
		{"config-injection then commit", []string{"-c", "k=v", "commit"}, nil, "commit", []string{}},
		{"commit with -c flag after subcmd", []string{"commit", "-c", "HEAD~1"}, nil, "commit", []string{"-c", "HEAD~1"}},
		{"no subcommand", []string{"-C", "/repo"}, []string{"/repo"}, "", nil},
		{"commit-tree not commit", []string{"commit-tree", "abc"}, nil, "commit-tree", []string{"abc"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ch, sub, rest := GitInvocation(tt.args)
			if !reflect.DeepEqual(ch, tt.wantChdirs) || sub != tt.wantSub || !reflect.DeepEqual(rest, tt.wantRest) {
				t.Errorf("GitInvocation(%v) = (%v,%q,%v), want (%v,%q,%v)", tt.args, ch, sub, rest, tt.wantChdirs, tt.wantSub, tt.wantRest)
			}
		})
	}
}
