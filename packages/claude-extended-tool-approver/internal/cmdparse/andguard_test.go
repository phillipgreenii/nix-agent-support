package cmdparse

import (
	"reflect"
	"testing"
)

// TestAndGuardChainsAndInLoop (bead pg2-xj975) pins the two leaf facts the
// effective-cwd tracking reads: AndGuardChains (the chain ids of every enclosing
// `X && Y` right-hand side, so a pipeline stage or a nested body inherits the
// guarantee that X ran) and InLoop.
func TestAndGuardChainsAndInLoop(t *testing.T) {
	leaf := func(t *testing.T, cmd, raw string) ParsedCommand {
		t.Helper()
		sp := ParseShell(cmd)
		for _, l := range sp.Leaves {
			if l.Raw == raw {
				return l
			}
		}
		t.Fatalf("no leaf %q in %q", raw, cmd)
		return ParsedCommand{}
	}
	t.Run("plain chain: shared chain id, right side guarded", func(t *testing.T) {
		cd, y := leaf(t, "cd d && go test", "cd d"), leaf(t, "cd d && go test", "go test")
		if cd.AndChainID == 0 || cd.AndChainID != y.AndChainID {
			t.Errorf("chain ids %d / %d, want equal and nonzero", cd.AndChainID, y.AndChainID)
		}
		// y is the right-hand side of the cd's &&, so it is guarded by that
		// same chain (which is also its own AndChainID here).
		if !reflect.DeepEqual(y.AndGuardChains, []int{cd.AndChainID}) || y.InLoop {
			t.Errorf("guards=%v inLoop=%v, want [%d] and false", y.AndGuardChains, y.InLoop, cd.AndChainID)
		}
		if len(cd.AndGuardChains) != 0 {
			t.Errorf("the left-hand side is not guarded by its own chain: %v", cd.AndGuardChains)
		}
	})
	for _, c := range []struct{ name, cmd, raw string }{
		{"pipeline stage", "cd d && go test | tail", "go test"},
		{"subshell body", "cd d && (go test)", "go test"},
		{"block body", "cd d && { go test; }", "go test"},
		{"if body", "cd d && if true; then go test; fi", "go test"},
	} {
		t.Run(c.name+" is guarded by the cd's chain", func(t *testing.T) {
			cd, y := leaf(t, c.cmd, "cd d"), leaf(t, c.cmd, c.raw)
			if !reflect.DeepEqual(y.AndGuardChains, []int{cd.AndChainID}) {
				t.Errorf("AndGuardChains = %v, want [%d]", y.AndGuardChains, cd.AndChainID)
			}
		})
	}
	for _, c := range []struct{ name, cmd, raw string }{
		{"left of &&", "go test && cd d", "go test"},
		{"after ;", "cd d; go test", "go test"},
		{"right of ||", "cd d || go test", "go test"},
		{"|| anywhere disables the whole tree", "cd d && go test || true", "go test"},
		{"pipeline's left stage", "go test | tail && cd d", "go test"},
	} {
		t.Run(c.name+" is unguarded", func(t *testing.T) {
			if y := leaf(t, c.cmd, c.raw); len(y.AndGuardChains) != 0 {
				t.Errorf("AndGuardChains = %v, want none", y.AndGuardChains)
			}
		})
	}
	for cmd, want := range map[string]bool{
		"for i in 1 2; do cd sub; done":     true,
		"while true; do cd sub; done":       true,
		"until false; do cd sub; done":      true,
		"cd sub; for i in 1; do true; done": false,
		"for i in 1; do true; done; cd sub": false,
		"if true; then cd sub; fi":          false,
	} {
		if got := leaf(t, cmd, "cd sub").InLoop; got != want {
			t.Errorf("%q: InLoop = %v, want %v", cmd, got, want)
		}
	}
}
