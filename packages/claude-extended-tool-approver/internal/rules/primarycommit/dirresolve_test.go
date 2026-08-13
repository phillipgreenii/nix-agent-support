package primarycommit

import (
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
)

// TestResolveDir is the seam's own table (pg2-wq3ki, and the shape primary-push will
// consume for pg2-eqacu). Dir is asserted for the resolved rows; Token/Source for the
// unresolved ones, because those two ARE the reason text an agent has to act on.
func TestResolveDir(t *testing.T) {
	vars := map[string]string{"WT": "/abs/worktree", "ROOT": "/abs/root", "DOLLAR": "$X"}
	const cwd = "/session/cwd"

	tests := []struct {
		name      string
		cwd       string
		chdirs    []string
		wantDir   string
		wantToken string
	}{
		{name: "no -C", cwd: cwd, wantDir: cwd},
		{name: "literal absolute -C", cwd: cwd, chdirs: []string{"/abs/worktree"}, wantDir: "/abs/worktree"},
		{name: "literal relative -C joins the cwd", cwd: cwd, chdirs: []string{"sub"}, wantDir: "/session/cwd/sub"},
		{name: "two -C options apply in order", cwd: cwd, chdirs: []string{"/a", "b"}, wantDir: "/a/b"},

		// The pg2-wq3ki resolution.
		{name: "variable -C", cwd: cwd, chdirs: []string{"$WT"}, wantDir: "/abs/worktree"},
		{name: "braced variable -C", cwd: cwd, chdirs: []string{"${WT}"}, wantDir: "/abs/worktree"},
		{name: "variable with a segment", cwd: cwd, chdirs: []string{"$ROOT/.worktrees/feat"}, wantDir: "/abs/root/.worktrees/feat"},
		{name: "variable then a literal -C", cwd: cwd, chdirs: []string{"$ROOT", "sub"}, wantDir: "/abs/root/sub"},

		// Unresolved: the token is reported as WRITTEN, which is the text to fix.
		{name: "unknown name", cwd: cwd, chdirs: []string{"$OTHER"}, wantToken: "$OTHER"},
		// The token is the first offending path COMPONENT, so a `/` inside the
		// construct truncates it — unresolvableToken's long-standing shape. The Source
		// still names the whole argument, which is what makes the reason actionable.
		{name: "default-value form", cwd: cwd, chdirs: []string{"${WT:-/tmp}"}, wantToken: "${WT:-"},
		{name: "command substitution", cwd: cwd, chdirs: []string{"$(pwd)"}, wantToken: "$(pwd)"},
		{name: "glob", cwd: cwd, chdirs: []string{"/abs/work*"}, wantToken: "work*"},
		{name: "tilde", cwd: cwd, chdirs: []string{"~/repo"}, wantToken: "~"},
		{
			// A binding whose VALUE is not usable as a path must not launder the token:
			// the expansion is refused and the original token is reported.
			name: "binding whose value still holds an expansion", cwd: cwd,
			chdirs: []string{"$DOLLAR"}, wantToken: "$DOLLAR",
		},
		{
			// The FIRST unresolvable input is reported, and `-C` is checked before the
			// cwd because a `-C` is what decides the directory when present.
			name: "the first unresolvable -C wins", cwd: cwd,
			chdirs: []string{"$OTHER", "$(pwd)"}, wantToken: "$OTHER",
		},
		{
			name: "an unresolved cwd is reported when every -C is literal",
			cwd:  "/session/$WT", chdirs: []string{"sub"}, wantToken: "$WT",
		},
		{
			// The cwd is NOT expanded here: the engine expands a `cd` target before its
			// verbatim join, so a token surviving into the cwd is one nothing resolved.
			name: "a token in the cwd is not resolved by the environment",
			cwd:  "/session/cwd/$WT", wantToken: "$WT",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveDir(tt.cwd, tt.chdirs, vars)
			if tt.wantToken != "" {
				if !got.Unresolved() {
					t.Fatalf("ResolveDir(%q, %v) = resolved %q, want unresolved token %q", tt.cwd, tt.chdirs, got.Dir, tt.wantToken)
				}
				if got.Token != tt.wantToken {
					t.Errorf("token = %q, want %q", got.Token, tt.wantToken)
				}
				if got.Source == "" {
					t.Error("Source is empty: the reason text would name no cause")
				}
				// Dir stays populated for a best-effort side lookup (git aliases).
				if got.Dir == "" {
					t.Error("Dir is empty on an unresolved resolution; the alias lookup relies on it")
				}
				return
			}
			if got.Unresolved() {
				t.Fatalf("ResolveDir(%q, %v) = unresolved (%s), want dir %q", tt.cwd, tt.chdirs, got.Token, tt.wantDir)
			}
			if got.Dir != tt.wantDir {
				t.Errorf("Dir = %q, want %q", got.Dir, tt.wantDir)
			}
			if got.Chosen == "" {
				t.Error("Chosen is empty: the reason text would not say how the directory was picked")
			}
		})
	}
}

// TestResolveDir_NoEnvironmentIsThePriorBehaviour is the "nothing else moved" assertion
// stated locally: with no bindings, every variable spelling is unresolved exactly as it
// was before pg2-wq3ki, and every literal spelling resolves exactly as it did.
func TestResolveDir_NoEnvironmentIsThePriorBehaviour(t *testing.T) {
	for _, chdir := range []string{"$WT", "${WT}", "$WT/x", "$(pwd)", "`pwd`", "~/repo", "/abs/w*"} {
		if got := ResolveDir("/session/cwd", []string{chdir}, nil); !got.Unresolved() {
			t.Errorf("ResolveDir(-C %q, no vars) = resolved %q, want unresolved", chdir, got.Dir)
		}
	}
	if got := ResolveDir("/session/cwd", []string{"/abs/worktree"}, nil); got.Unresolved() || got.Dir != "/abs/worktree" {
		t.Errorf("ResolveDir(-C literal, no vars) = %+v, want /abs/worktree", got)
	}
}

// TestResolveDir_ChosenNamesTheResolution pins that the provenance string carries BOTH
// the token as written and the value it resolved to (pg2-h2npt's cause-naming quality).
func TestResolveDir_ChosenNamesTheResolution(t *testing.T) {
	got := ResolveDir("/session/cwd", []string{"$WT"}, map[string]string{"WT": "/abs/worktree"})
	for _, want := range []string{"`git -C $WT`", "resolved to /abs/worktree from an assignment earlier in the same command"} {
		if !strings.Contains(got.Chosen, want) {
			t.Errorf("Chosen = %q, does not mention %q", got.Chosen, want)
		}
	}
	// A literal `-C` gets no note, so the ordinary prompt is unchanged.
	if plain := ResolveDir("/session/cwd", []string{"/abs/worktree"}, nil); plain.Chosen != "the `git -C /abs/worktree` option" {
		t.Errorf("Chosen for a literal -C = %q, want the unchanged wording", plain.Chosen)
	}
}

// TestLeafVars covers the fallback that lets this rule be correct at whatever scope it is
// handed: the engine supplies the environment for ONE leaf, a direct caller supplies the
// whole expression and the leaves before `i` are the assignments.
func TestLeafVars(t *testing.T) {
	// Direct-caller scope: the whole expression, no engine-supplied base.
	leaves := cmdparse.Parse(`WT=/abs/worktree && git -C "$WT" commit`)
	if len(leaves) != 2 {
		t.Fatalf("expected 2 leaves, got %d", len(leaves))
	}
	if got := LeafVars(nil, leaves, 1)["WT"]; got != "/abs/worktree" {
		t.Errorf("LeafVars(nil, whole expression, 1)[WT] = %q, want /abs/worktree", got)
	}
	// Engine scope: one leaf, so the overlay is empty and the base passes through.
	base := map[string]string{"WT": "/abs/worktree"}
	oneLeaf := cmdparse.Parse(`git -C "$WT" commit`)
	if got := LeafVars(base, oneLeaf, 0)["WT"]; got != "/abs/worktree" {
		t.Errorf("LeafVars(base, one leaf, 0)[WT] = %q, want the base binding", got)
	}
	// The NEARER assignment wins over the engine-supplied one.
	shadow := cmdparse.Parse(`WT=/nearer && git -C "$WT" commit`)
	if got := LeafVars(base, shadow, 1)["WT"]; got != "/nearer" {
		t.Errorf("LeafVars(base, shadowing expression, 1)[WT] = %q, want /nearer", got)
	}
	// Neither source: no bindings, which is the ordinary case.
	if got := LeafVars(nil, oneLeaf, 0); len(got) != 0 {
		t.Errorf("LeafVars(nil, one leaf, 0) = %v, want no bindings", got)
	}
}
