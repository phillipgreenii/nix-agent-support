package pggwm

import (
	"encoding/json"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

func TestReadOnlySurface(t *testing.T) {
	cases := map[string]bool{
		"pg-gwm list":                         true,
		"pg-gwm list --include-unmanaged":     true,
		"pg-gwm list --format=json":           true,
		"pg-gwm list --format json":           true,
		"pg-gwm config check":                 true,
		"pg-gwm info --name x":                true,
		"pg-gwm preserved list":               true,
		"pg-gwm reap --dry-run":               true,
		"pg-gwm reap --dry-run --all-repos":   true,
		"pg-gwm version":                      true,
		"pg-gwm completion zsh":               true,
		"pg-gwm reap":                         false,
		"pg-gwm reap --all-repos":             false,
		"pg-gwm reap --dry-run=false":         false,
		"pg-gwm preserved prune":              false,
		"pg-gwm preserved":                    false,
		"pg-gwm serve":                        false,
		"pg-gwm lease --name x":               false,
		"pg-gwm list --repo /etc":             false,
		"pg-gwm config check --config /etc/x": false,
		"pg-gwm list extra":                   false,
		"pg-gwm list --bogus":                 false,
		"pg-gwm list --format $X":             false,
		"pg-gwm list > out":                   false,
		"pg-gwm":                              false,
	}
	for cmd, want := range cases {
		ti, _ := json.Marshal(map[string]string{"command": cmd})
		res, err := New().Evaluate(&hookio.HookInput{ToolName: "Bash", ToolInput: ti})
		got := err == nil && res.Decision == hookio.Approve
		if got != want {
			t.Errorf("%q approve = %v, want %v", cmd, got, want)
		}
	}
}
