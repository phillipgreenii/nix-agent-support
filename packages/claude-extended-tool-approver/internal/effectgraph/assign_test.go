package effectgraph

import (
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmddesc"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
)

func buildFor(command string) Graph {
	return BuildInterpreted(cmdparse.ParseShell(command), cmddesc.DefaultRegistry(), cmddesc.Context{})
}

// nodesByRaw returns the Command nodes whose leaf Raw equals raw.
func nodesByRaw(g Graph, raw string) []*Node {
	var out []*Node
	for i := range g.Nodes {
		n := &g.Nodes[i]
		if n.Kind == NodeCommand && n.Leaf != nil && n.Leaf.Raw == raw {
			out = append(out, n)
		}
	}
	return out
}

func hasEdge(g Graph, from, to string, kind EdgeKind) bool {
	for _, e := range g.Edges {
		if e.From == from && e.To == to && e.Kind == kind {
			return true
		}
	}
	return false
}

// TestAssignmentValueSubstitutionIsGradedAsACommand (pg2-dbrsg): an
// assignment's `$(cmd)` becomes its own scope whose leaves are interpreted
// like any command and wired to the assignment with a Substitution edge — for
// the bare form AND the prefix form (which cmdparse leaves un-lowered).
func TestAssignmentValueSubstitutionIsGradedAsACommand(t *testing.T) {
	for _, command := range []string{
		`X="$(git rev-parse HEAD)"`,
		`X="$(git rev-parse HEAD)" echo hi`,
	} {
		t.Run(command, func(t *testing.T) {
			g := buildFor(command)
			inner := nodesByRaw(g, "git rev-parse HEAD")
			if len(inner) != 1 {
				t.Fatalf("want one inner git node, got %d in %+v", len(inner), g.Nodes)
			}
			if inner[0].Scope == "" {
				t.Errorf("inner command must live in its own substitution scope")
			}
			if inner[0].Mark == MarkInsufficient {
				t.Errorf("git rev-parse must be interpreted by its schema, got %s", inner[0].MarkReason)
			}
			var outer *Node
			for i := range g.Nodes {
				if n := &g.Nodes[i]; n.Kind == NodeCommand && n.Scope == "" {
					outer = n
				}
			}
			if outer == nil || !hasEdge(g, inner[0].ID, outer.ID, EdgeSubstitution) {
				t.Errorf("missing substitution edge inner -> %+v", outer)
			}
		})
	}
}

// TestAssignmentSubstitutionNotLoweredTwice: `export X=$(pwd)` already carries
// the substitution among its own arguments, so the value must not add a
// second scope for the same body.
func TestAssignmentSubstitutionNotLoweredTwice(t *testing.T) {
	g := buildFor(`export X=$(pwd)`)
	if n := len(nodesByRaw(g, "pwd")); n != 1 {
		t.Errorf("pwd lowered %d times, want 1", n)
	}
}

// TestFreshTempDirIdiomIsNotGraded: `NAME=$(mktemp -d)` is the one value the
// policy layer certifies itself (no mktemp schema exists), so it keeps its
// pre-existing status instead of becoming an unschema'd node.
func TestFreshTempDirIdiomIsNotGraded(t *testing.T) {
	g := buildFor(`HOME=$(mktemp -d) true`)
	if n := len(nodesByRaw(g, "mktemp -d")); n != 0 {
		t.Errorf("mktemp -d was lowered as a node (%d); the idiom must stay certified, not graded", n)
	}
	g = buildFor(`X=$(mktemp -d -p /etc) true`)
	if n := len(nodesByRaw(g, "mktemp -d -p /etc")); n != 1 {
		t.Errorf("a non-idiom mktemp must be graded (got %d nodes)", n)
	}
}

// TestUnmodeledAssignmentValueMakesTheLeafInsufficient: arithmetic and process
// substitution in a value are not understood, for the bare form and for a
// prefix assignment on a command.
func TestUnmodeledAssignmentValueMakesTheLeafInsufficient(t *testing.T) {
	for _, command := range []string{`X=$((1+2))`, `X=$((1+2)) true`, `X=<(cat f) true`, `X=${!Y}`} {
		t.Run(command, func(t *testing.T) {
			g := buildFor(command)
			var any bool
			for i := range g.Nodes {
				n := &g.Nodes[i]
				if n.Kind == NodeCommand && n.Mark == MarkInsufficient && strings.Contains(n.MarkReason, "env X") {
					any = true
				}
			}
			if !any {
				t.Errorf("no node marked insufficient with an env X reason: %+v", g.Nodes)
			}
		})
	}
}

// TestCommandlessLeafShapes pins which command-less leaves the builder models
// and which stay opaque.
func TestCommandlessLeafShapes(t *testing.T) {
	modeled := []string{
		`X=1`,
		`X=$(pwd)`,
		`> /tmp/f`,
		`for f in a b; do true; done`,
		`case "$x" in a) true;; esac`,
	}
	for _, command := range modeled {
		t.Run("modeled/"+command, func(t *testing.T) {
			g := buildFor(command)
			for i := range g.Nodes {
				n := &g.Nodes[i]
				if n.Kind == NodeCommand && n.Mark == MarkInsufficient {
					t.Errorf("node %q unexpectedly insufficient: %s", n.Label, n.MarkReason)
				}
			}
		})
	}
	opaque := []string{
		`[[ -z "$x" ]]`,
		`(( i++ ))`,
		`let a=b`,
		`a[$i]=v`,
		"{ cat; } <<EOF\nx\nEOF",
	}
	for _, command := range opaque {
		t.Run("opaque/"+command, func(t *testing.T) {
			g := buildFor(command)
			var any bool
			for i := range g.Nodes {
				if n := &g.Nodes[i]; n.Kind == NodeCommand && n.MarkReason == "no executable" {
					any = true
				}
			}
			if !any {
				t.Errorf("expected a \"no executable\" node for %q: %+v", command, g.Nodes)
			}
		})
	}
}

// TestPersistentAssignmentMarksItsEffects: only an assignment-only statement
// (and a read/export operand) is a SHELL-variable write; a prefix assignment
// is one command's environment.
func TestPersistentAssignmentMarksItsEffects(t *testing.T) {
	cases := []struct {
		command string
		raw     string
		want    bool
	}{
		{`X=1`, "X=1", true},
		{`X=1 true`, "X=1 true", false},
		{`read -r Y`, "read -r Y", true},
	}
	for _, tc := range cases {
		g := buildFor(tc.command)
		nodes := nodesByRaw(g, tc.raw)
		if len(nodes) != 1 {
			t.Fatalf("%q: want one node %q, got %d", tc.command, tc.raw, len(nodes))
		}
		var seen bool
		for _, e := range nodes[0].Effects {
			if e.Kind == cmddesc.EffectEnv && e.EnvSet {
				seen = true
				if e.EnvPersistent != tc.want {
					t.Errorf("%q: EnvPersistent = %v, want %v", tc.command, e.EnvPersistent, tc.want)
				}
			}
		}
		if !seen {
			t.Errorf("%q: no EffectEnv set", tc.command)
		}
	}
}

// flowsInto reports whether node `from` has a Flow edge into `to`.
func flowsInto(g Graph, from, to *Node) bool { return hasEdge(g, from.ID, to.ID, EdgeFlow) }

// TestCapturedContentTaintsLaterCommands: content captured into a variable
// (or fed to a compound by a redirection) must keep flowing to the commands
// that could consume it, or the content-flow policy loses the data.
func TestCapturedContentTaintsLaterCommands(t *testing.T) {
	t.Run("assignment from a content-emitting substitution", func(t *testing.T) {
		g := buildFor(`T=$(cat README.md); echo "$T"`)
		assign := nodesByRaw(g, "T=$(cat README.md)")
		later := nodesByRaw(g, `echo "$T"`)
		if len(assign) != 1 || len(later) != 1 {
			t.Fatalf("nodes: assign=%d later=%d", len(assign), len(later))
		}
		if !flowsInto(g, assign[0], later[0]) {
			t.Errorf("captured content must flow from the assignment to the later command")
		}
		// the substitution's own command is upstream of the assignment and
		// must not be tainted back by it.
		cat := nodesByRaw(g, "cat README.md")
		if len(cat) != 1 || flowsInto(g, assign[0], cat[0]) {
			t.Errorf("the assignment must not flow back into its own substitution")
		}
	})
	t.Run("metadata output is not content", func(t *testing.T) {
		g := buildFor(`D=$(pwd); echo "$D"`)
		assign := nodesByRaw(g, "D=$(pwd)")
		later := nodesByRaw(g, `echo "$D"`)
		if len(assign) != 1 || len(later) != 1 {
			t.Fatalf("nodes: assign=%d later=%d", len(assign), len(later))
		}
		if flowsInto(g, assign[0], later[0]) {
			t.Errorf("pwd is path metadata; capturing it must not taint later commands")
		}
	})
	t.Run("compound input redirection", func(t *testing.T) {
		g := buildFor(`while read -r l; do echo "$l"; done < README.md`)
		redir := nodesByRaw(g, "< README.md")
		body := nodesByRaw(g, `echo "$l"`)
		if len(redir) != 1 || len(body) != 1 {
			t.Fatalf("nodes: redir=%d body=%d", len(redir), len(body))
		}
		if !flowsInto(g, redir[0], body[0]) {
			t.Errorf("the compound's input file must flow to the commands inside it")
		}
	})
	t.Run("read fed by a pipe", func(t *testing.T) {
		g := buildFor(`cat README.md | read -r L; echo "$L"`)
		rd := nodesByRaw(g, "read -r L")
		later := nodesByRaw(g, `echo "$L"`)
		if len(rd) != 1 || len(later) != 1 {
			t.Fatalf("nodes: read=%d later=%d", len(rd), len(later))
		}
		if !flowsInto(g, rd[0], later[0]) {
			t.Errorf("read of piped content must flow to later commands")
		}
	})
	t.Run("plain assignment taints nothing", func(t *testing.T) {
		g := buildFor(`X=1; echo "$X"`)
		assign := nodesByRaw(g, "X=1")
		later := nodesByRaw(g, `echo "$X"`)
		if len(assign) != 1 || len(later) != 1 {
			t.Fatalf("nodes: assign=%d later=%d", len(assign), len(later))
		}
		if flowsInto(g, assign[0], later[0]) {
			t.Errorf("a literal assignment carries no content")
		}
	})
}
