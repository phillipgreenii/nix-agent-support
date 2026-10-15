package effectgraph

import (
	"reflect"
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmddesc"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
)

// TestExecWorkingDirectories (bead pg2-xj975) pins the set of directories the
// builder stamps on an EffectExec (Effect.CWDs): one entry when every cd
// before the leaf provably ran, the old AND the new directory when a cd may
// not have taken effect, nothing at all when the position is unknown. Each
// row is one shape from execTracker's doc comment. The probe leaf is the
// `go test` node.
func TestExecWorkingDirectories(t *testing.T) {
	const base = "/base"
	reg := cmddesc.DefaultRegistry()
	cases := []struct {
		name, cmd string
		want      []string // the go test node's possible directories
		unknown   bool
	}{
		{"no cd", "go test ./...", []string{"/base"}, false},
		{"cd && leaf", "cd /a && go test ./...", []string{"/a"}, false},
		{"relative cd && leaf", "cd sub && go test ./...", []string{"/base/sub"}, false},
		{"two cds in one chain", "cd /a && cd b && go test ./...", []string{"/a/b"}, false},
		{"cd && leaf | pipe: stage guarded by the chain", "cd /a && go test ./... | tail", []string{"/a"}, false},
		{"cd && (leaf): subshell body guarded", "cd /a && (go test ./...)", []string{"/a"}, false},
		{"cd && { leaf; }: block body guarded", "cd /a && { go test ./...; }", []string{"/a"}, false},
		{"cd && if: body guarded", "cd /a && if true; then go test ./...; fi", []string{"/a"}, false},
		{"cd; leaf may have failed", "cd /a; go test ./...", []string{"/base", "/a"}, false},
		{"cd || leaf", "cd /a || go test ./...", []string{"/base", "/a"}, false},
		{"cd && true; leaf: chain broken by ;", "cd /a && true; go test ./...", []string{"/base", "/a"}, false},
		{"cd in if body", "if true; then cd /a; fi; go test ./...", []string{"/base", "/a"}, false},
		{"cd in block", "{ cd /a; }; go test ./...", []string{"/base", "/a"}, false},
		{"cd; cd && leaf: second cd guaranteed, first not", "cd /a; cd b && go test ./...", []string{"/base/b", "/a/b"}, false},
		{"subshell cd confined", "(cd /a); go test ./...", []string{"/base"}, false},
		{"cd inside subshell reaches its own later leaf", "(cd /a && go test ./...)", []string{"/a"}, false},
		{"cd; (leaf): outer cd reaches the subshell", "cd /a; (go test ./...)", []string{"/base", "/a"}, false},
		{"pipeline stage cd confined", "cd /a | cat; go test ./...", []string{"/base"}, false},
		{"cd after the leaf", "go test ./... && cd /a", []string{"/base"}, false},
		{"substitution sees the cd before its parent leaf", "cd /a && echo $(go test ./...)", []string{"/a"}, false},
		{"substitution does not see a later cd", "echo $(go test ./...) && cd /a", []string{"/base"}, false},
		{"substitution of an unguarded cd", "cd /a; echo $(go test ./...)", []string{"/base", "/a"}, false},
		{"bash -c child starts at the parent's set", "cd /a && bash -c 'go test ./...'", []string{"/a"}, false},
		{"bash -c child of an unguarded cd", "cd /a; bash -c 'go test ./...'", []string{"/base", "/a"}, false},
		{"absolute cd in a loop is idempotent", "for i in 1 2; do cd /a && go test ./...; done", []string{"/a"}, false},
		{"relative cd in a loop is unknown", "for i in 1 2; do cd sub && go test ./...; done", nil, true},
		{"dynamic cd is unknown", `cd "$D" && go test ./...`, nil, true},
		{"cd - is unknown", "cd - && go test ./...", nil, true},
		{"unknown stays unknown after a later literal cd", `cd "$D" && cd /a && go test ./...`, nil, true},
		{"bare cd goes home", "cd && go test ./...", []string{"~"}, false},
		{"cd ~/x", "cd ~/x && go test ./...", []string{"~/x"}, false},
		{"too many alternatives is unknown", "cd /a; cd /b; cd /c; cd /d; cd /e; cd /f; cd /g; cd /h; cd /i; go test ./...", nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sp := cmdparse.ParseShell(tc.cmd)
			if sp.Unparseable {
				t.Fatalf("unparseable: %s", tc.cmd)
			}
			g := BuildInterpreted(sp, reg, cmddesc.Context{CWD: base})
			found := false
			for _, n := range g.Nodes {
				if n.Kind != NodeCommand || !strings.Contains(n.Label, "go test") {
					continue
				}
				for _, e := range n.Effects {
					if e.Kind != cmddesc.EffectExec {
						continue
					}
					found = true
					if got := e.CWDList(); !reflect.DeepEqual(got, tc.want) {
						t.Errorf("CWDs = %v, want %v", got, tc.want)
					}
					if e.CWDUnknown != tc.unknown {
						t.Errorf("CWDUnknown = %v, want %v", e.CWDUnknown, tc.unknown)
					}
				}
			}
			if !found {
				t.Fatal("no Exec effect on a go test node")
			}
		})
	}
}

// TestExecWorkingDirectories_EmptyBase: with no request cwd a literal absolute
// cd still yields an absolute directory, a relative cd yields a relative one
// (which the policy refuses), and no cd yields the empty "unstamped" set.
func TestExecWorkingDirectories_EmptyBase(t *testing.T) {
	reg := cmddesc.DefaultRegistry()
	for cmd, want := range map[string][]string{
		"cd /a && go test ./...":  {"/a"},
		"cd sub && go test ./...": {"sub"},
		"go test ./...":           nil,
	} {
		g := BuildInterpreted(cmdparse.ParseShell(cmd), reg, cmddesc.Context{})
		for _, n := range g.Nodes {
			for _, e := range n.Effects {
				if e.Kind == cmddesc.EffectExec && !reflect.DeepEqual(e.CWDList(), want) {
					t.Errorf("%q: CWDs = %v, want %v", cmd, e.CWDList(), want)
				}
			}
		}
	}
}
