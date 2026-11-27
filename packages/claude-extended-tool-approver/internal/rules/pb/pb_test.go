package pb

import (
	"encoding/json"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

func TestSurface(t *testing.T) {
	cases := []struct {
		cmd  string
		want bool
	}{
		{"pb actor --role drain --scratchpad /tmp/x", true},
		{"pb drain isolate --bead X --repo /abs/repo --actor A", true},
		{"pb drain isolate --bead X --repo /abs/repo --bogus 1", false},
		{"pb drain isolate --bead X --repo /abs/repo --json", true},
		{"pb drain reclaim --bead X --repo /abs/repo --actor A", true},
		{"pb gate check --dry-run", true},
		{"pb gate check --json", true},
		{"pb gate create --blocks X --repo r --commit abc --reason why", true},
		{"pb gate attach-verified-child --impl X --title t --body-file /tmp/b.md", true},
		{"pb unstick prepare --workdir /tmp/w", true},
		{"pb gate --help", false},
		{"pb gate check --help", true},
		{"pb --version", true},
		// deliberately absent / unmodeled
		{"pb gate check --stale-handler close", false},
		{"pb gate check --strict", false},
		{"pb gate check --last-n 3", false},
		{"pb completion bash", false},
		{"pb help drain", false},
		{"pb frobnicate", false},
		{"pb", false},
		{"pb drain", false},
		{"pb drain isolate", true},
		{"pb drain isolate stray", false},
		{"pb drain isolate --bead", false},
		{"pb drain isolate --bead --repo x", false},
		{"pb drain isolate --bead=X", false},
		{"pb drain isolate -- --bead X", false},
		// shell metacharacters and expansion
		{"pb drain isolate --bead $(id)", false},
		{"pb drain isolate --bead \"$B\"", false},
		{"pb drain isolate --bead X > out", false},
		{"pb drain isolate --bead `id`", false},
		{"pb gate attach-verified-child --impl X --body-file <(echo hi)", false},
		{"/usr/bin/pb gate check --dry-run", false},
		{"other gate check --dry-run", false},
	}
	for _, c := range cases {
		ti, _ := json.Marshal(map[string]string{"command": c.cmd})
		res, err := New().Evaluate(&hookio.HookInput{ToolName: "Bash", ToolInput: ti})
		got := err == nil && res.Decision == hookio.Approve
		if got != c.want {
			t.Errorf("%q approve = %v, want %v", c.cmd, got, c.want)
		}
	}
}
