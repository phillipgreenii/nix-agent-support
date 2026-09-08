package effectgraph

import (
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmddesc"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
)

// remotePathEffects returns every EffectPath among a node's effects.
func remotePathEffects(n *Node) []cmddesc.Effect {
	var out []cmddesc.Effect
	for _, e := range n.Effects {
		if e.Kind == cmddesc.EffectPath {
			out = append(out, e)
		}
	}
	return out
}

func scopeByLabel(g Graph, label string) (Scope, bool) {
	for _, s := range g.Scopes {
		if s.Label == label {
			return s, true
		}
	}
	return Scope{}, false
}

// TestRemoteScopeTagsPathEffects: an ssh child's own scope is tagged with
// the host (slice 3aa, tc-lc8f item 4g; tc-vn5z item 4), and the remote
// leaf's own EffectPath is stamped Remote with that host — proving the
// path-policy guard (effectpolicy.remotePathGuard) can tell "this is not
// this process's local filesystem" from the effect alone.
func TestRemoteScopeTagsPathEffects(t *testing.T) {
	reg := cmddesc.DefaultRegistry()
	g := BuildInterpreted(cmdparse.ParseShell("ssh host cat /etc/passwd"), reg, cmddesc.Context{})

	scope, ok := scopeByLabel(g, "ssh host")
	if !ok {
		t.Fatalf("no scope labelled \"ssh host\": %+v", g.Scopes)
	}
	if scope.Remote != "host" {
		t.Errorf("scope.Remote = %q, want %q", scope.Remote, "host")
	}

	var found bool
	for i := range g.Nodes {
		n := &g.Nodes[i]
		if n.Kind != NodeCommand || n.Scope != scope.ID {
			continue
		}
		for _, e := range remotePathEffects(n) {
			if e.Path == "/etc/passwd" {
				found = true
				if e.Remote != "host" {
					t.Errorf("effect.Remote = %q, want %q", e.Remote, "host")
				}
			}
		}
	}
	if !found {
		t.Fatalf("no PathRead of /etc/passwd found in the remote scope: %+v", g.Nodes)
	}
}

// TestRemoteScopeInheritsThroughLocalDialect: a `bash -c` the REMOTE command
// itself runs is a local-dialect child (its own ChildInvocation.Remote is
// "" — ordinary bash -c semantics), but it is created INSIDE the ssh scope,
// so it inherits "host" via newScope's parent lookup — transitively, so its
// own nested `cat` leaf's path effect is ALSO tagged, without bash's own
// schema/interpreter knowing anything about remoteness.
func TestRemoteScopeInheritsThroughLocalDialect(t *testing.T) {
	reg := cmddesc.DefaultRegistry()
	g := BuildInterpreted(cmdparse.ParseShell(`ssh host 'bash -c "cat /etc/passwd"'`), reg, cmddesc.Context{})

	var found bool
	for i := range g.Nodes {
		n := &g.Nodes[i]
		if n.Kind != NodeCommand {
			continue
		}
		for _, e := range remotePathEffects(n) {
			if e.Path == "/etc/passwd" {
				found = true
				if e.Remote != "host" {
					t.Errorf("effect.Remote = %q, want %q (inherited through a local bash -c dialect)", e.Remote, "host")
				}
			}
		}
	}
	if !found {
		t.Fatalf("no PathRead of /etc/passwd found: %+v", g.Nodes)
	}
}

// TestLocalScopeStaysUntagged: an ordinary local `bash -c` (no ssh anywhere)
// leaves every scope's Remote "" and every path effect's Remote "" —
// regression coverage for the pre-existing, non-remote graph shape.
func TestLocalScopeStaysUntagged(t *testing.T) {
	reg := cmddesc.DefaultRegistry()
	g := BuildInterpreted(cmdparse.ParseShell(`bash -c "cat README.md"`), reg, cmddesc.Context{})

	for _, s := range g.Scopes {
		if s.Remote != "" {
			t.Errorf("scope %+v unexpectedly remote", s)
		}
	}
	var found bool
	for i := range g.Nodes {
		n := &g.Nodes[i]
		if n.Kind != NodeCommand {
			continue
		}
		for _, e := range remotePathEffects(n) {
			if e.Path == "README.md" {
				found = true
				if e.Remote != "" {
					t.Errorf("effect.Remote = %q, want \"\" (local)", e.Remote)
				}
			}
		}
	}
	if !found {
		t.Fatalf("no PathRead of README.md found: %+v", g.Nodes)
	}
}

// TestNestedRemoteScopeOverridesHost: an ssh invocation running AS the
// remote command of an outer ssh (a jump-style double hop) tags its OWN
// nested scope with its OWN host, not the outer one's — a child invocation
// naming a remote host always OVERRIDES whatever it inherited, per
// newScope/child's own contract.
func TestNestedRemoteScopeOverridesHost(t *testing.T) {
	reg := cmddesc.DefaultRegistry()
	g := BuildInterpreted(cmdparse.ParseShell(`ssh host1 "ssh host2 'cat /etc/passwd'"`), reg, cmddesc.Context{})

	var found bool
	for i := range g.Nodes {
		n := &g.Nodes[i]
		if n.Kind != NodeCommand {
			continue
		}
		for _, e := range remotePathEffects(n) {
			if e.Path == "/etc/passwd" {
				found = true
				if e.Remote != "host2" {
					t.Errorf("effect.Remote = %q, want %q (the INNER ssh's own host, not host1)", e.Remote, "host2")
				}
			}
		}
	}
	if !found {
		t.Fatalf("no PathRead of /etc/passwd found: %+v", g.Nodes)
	}
}
