package ping

import (
	"encoding/json"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

func TestPingRule(t *testing.T) {
	cases := []struct {
		cmd  string
		want hookio.Decision
	}{
		{"ping -c 2 -W 2 192.168.40.28", hookio.Approve},
		{"ping -c 3 -W 2 pve3", hookio.Approve},
		{"ping -w 5 -n example.test", hookio.Approve},
		{"ping pve3 -c 1", hookio.Approve},
		{"ping -6 -c 1 fe80::1", hookio.Approve},
		{"/usr/bin/ping -c 1 \"pve3\"", hookio.Approve},
		{"ping -c 120 -s 120 -t 64 -q pve3", hookio.Approve},

		{"ping pve3", hookio.NoOpinion},
		{"ping -W 2 pve3", hookio.NoOpinion},
		{"ping -c 0 pve3", hookio.NoOpinion},
		{"ping -c 100000 pve3", hookio.NoOpinion},
		{"ping -c 121 pve3", hookio.NoOpinion},
		{"ping -c 05 pve3", hookio.NoOpinion},
		{"ping -c 2 -s 0 pve3", hookio.NoOpinion},
		{"ping -f -c 5 pve3", hookio.NoOpinion},
		{"ping -l 100 -c 5 pve3", hookio.NoOpinion},
		{"ping -i 0.001 -c 5 pve3", hookio.NoOpinion},
		{"ping -I eth0 -c 1 pve3", hookio.NoOpinion},
		{"ping -c 1 $(cat /etc/hostname).example.test", hookio.NoOpinion},
		{"ping -c 1 \"$H\"", hookio.NoOpinion},
		{"ping -c 1 http://x/y", hookio.NoOpinion},
		{"ping -c 1 a b", hookio.NoOpinion},
		{"ping -c 1", hookio.NoOpinion},
		{"ping -c", hookio.NoOpinion},
		{"ping -c 1 pve3 > out.txt", hookio.NoOpinion},
		{"ping -c 1 -- pve3", hookio.NoOpinion},
		{"git status", hookio.NoOpinion},
	}
	r := New()
	for _, c := range cases {
		ti, _ := json.Marshal(hookio.BashToolInput{Command: c.cmd})
		in := &hookio.HookInput{ToolName: "Bash", ToolInput: ti}
		got := hookio.Verdict(r.Evaluate(in)).Decision
		if got != c.want {
			t.Errorf("%q => %v, want %v", c.cmd, got, c.want)
		}
	}
}
