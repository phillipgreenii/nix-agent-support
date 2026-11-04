package effectpolicy

import (
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmddesc"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/evalcontract"
)

// TestPingBoundedProbe pins bead tc-jlgi9 (absorbing the settings rule
// Bash(ping:*)): only a ping bounded by -c or -w, aimed at a plain host/IP
// literal, approves. Unbounded, flood, preload, interval, expansion and
// option-shaped destinations stay conservative.
func TestPingBoundedProbe(t *testing.T) {
	root, _ := fixture(t)
	reg := cmddesc.DefaultRegistry()
	cases := []struct {
		name string
		cmd  string
		want evalcontract.Decision
	}{
		{"count_and_timeout_ip", "ping -c 2 -W 2 192.168.40.28", evalcontract.Approve},
		{"count_hostname", "ping -c 3 -W 2 pve3", evalcontract.Approve},
		{"deadline_only", "ping -w 5 -n example.test", evalcontract.Approve},
		{"count_dest_first", "ping pve3 -c 1", evalcontract.Approve},
		{"ipv6_literal", "ping -6 -c 1 fe80::1", evalcontract.Approve},
		{"unbounded_abstains", "ping pve3", evalcontract.Abstain},
		{"timeout_only_abstains", "ping -W 2 pve3", evalcontract.Abstain},
		{"zero_count_abstains", "ping -c 0 pve3", evalcontract.Abstain},
		{"huge_count_abstains", "ping -c 100000 pve3", evalcontract.Abstain},
		{"flood_abstains", "ping -f -c 5 pve3", evalcontract.Abstain},
		{"preload_abstains", "ping -l 100 -c 5 pve3", evalcontract.Abstain},
		{"interval_abstains", "ping -i 0.001 -c 5 pve3", evalcontract.Abstain},
		{"expansion_destination_abstains", "ping -c 1 $(cat /etc/hostname).example.test", evalcontract.Abstain},
		{"variable_destination_abstains", "ping -c 1 \"$H\"", evalcontract.Abstain},
		{"url_destination_abstains", "ping -c 1 http://x/y", evalcontract.Abstain},
	}
	for _, c := range cases {
		resp := Evaluate(evalcontract.Request{Command: c.cmd, CWD: root, ProjectRoot: root}, reg, DefaultPolicies(), DefaultGraphPolicies())
		if resp.Decision != c.want {
			t.Errorf("%s: %q = %v (%s), want %v", c.name, c.cmd, resp.Decision, resp.Reason, c.want)
		}
	}
}
