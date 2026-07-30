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
