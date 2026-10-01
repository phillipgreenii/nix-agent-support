package config

import (
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-router/internal/query"
)

// --- Gate Registry config surface (bead pg2-h63eu) ---

// A role and a query each declare, at registration, which gate TYPEs they do
// NOT block on; the lists ride through to roles.Role / query.Source.
func TestLoad_nonBlockingGatesOnRoleAndQuery(t *testing.T) {
	absentGlobalConfig(t)
	writeCfg(t, `
[[query]]
name = "watchdog-tick"
type = "timer"
emits = ["timer.5m"]
non_blocking_gates = ["LOW_DISK_USAGE"]

[[role]]
name = "disk-watchdog"
binds = ["timer.5m"]
non_blocking_gates = ["LOW_DISK_USAGE", "SYSTEM_PAUSE"]

[[query]]
name = "plain"
type = "command"
emits = ["e"]
[query.command]
argv = ["x"]
format = "jsonl"

[[role]]
name = "plain-role"
binds = ["e"]
`)
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := c.Roles[0].NonBlockingGates; len(got) != 2 || got[0] != "LOW_DISK_USAGE" || got[1] != "SYSTEM_PAUSE" {
		t.Errorf("role non_blocking_gates = %v", got)
	}
	if len(c.Roles[1].NonBlockingGates) != 0 {
		t.Errorf("a role that declares none must block on every TYPE; got %v", c.Roles[1].NonBlockingGates)
	}
	if got := c.Queries[0].NonBlockingGates; len(got) != 1 || got[0] != "LOW_DISK_USAGE" {
		t.Errorf("query non_blocking_gates = %v", got)
	}
	if len(c.Queries[1].NonBlockingGates) != 0 {
		t.Errorf("a query that declares none must block on every TYPE; got %v", c.Queries[1].NonBlockingGates)
	}
}

// A mistyped (not ALL CAPS) exemption is rejected rather than silently
// exempting nothing.
func TestLoad_nonBlockingGatesMustBeAllCaps(t *testing.T) {
	absentGlobalConfig(t)
	for name, body := range map[string]string{
		"role": `
[[query]]
name = "q"
type = "timer"
emits = ["e"]
[[role]]
name = "r"
binds = ["e"]
non_blocking_gates = ["low_disk"]
`,
		"query": `
[[query]]
name = "q"
type = "timer"
emits = ["e"]
non_blocking_gates = ["low_disk"]
[[role]]
name = "r"
binds = ["e"]
`,
	} {
		writeCfg(t, body)
		_, err := Load()
		if err == nil || !strings.Contains(err.Error(), "non_blocking_gates") {
			t.Errorf("%s: Load() = %v, want a non_blocking_gates ALL CAPS error", name, err)
		}
	}
}

// The timer emitter decodes with no sub-table, is recognized by query.IsTimer
// (the one emitter no gate blocks), and a command query is not.
func TestLoad_timerQueryTypeIsTheGateImmuneEmitter(t *testing.T) {
	absentGlobalConfig(t)
	writeCfg(t, `
[[query]]
name = "tick"
type = "timer"
emits = ["timer.5m"]
trigger = { kind = "period", every = "5m" }

[[query]]
name = "cmd"
type = "command"
emits = ["e"]
[query.command]
argv = ["x"]
format = "jsonl"

[[role]]
name = "r"
binds = ["timer.5m", "e"]
`)
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !query.IsTimer(c.Queries[0].Query) {
		t.Error("type = \"timer\" must decode to the timer emitter")
	}
	if query.IsTimer(c.Queries[1].Query) {
		t.Error("a command query must NOT be treated as the timer emitter")
	}
}

// Gate records are emitted by the core itself, so a role may bind them without
// any [[query]] declaring them (no orphan-consumer error).
func TestValidate_gateEventTypesAreNotOrphanConsumers(t *testing.T) {
	absentGlobalConfig(t)
	writeCfg(t, `
[[query]]
name = "q"
type = "timer"
emits = ["e"]

[[role]]
name = "r"
binds = ["e"]

[[role]]
name = "gate-listener"
binds = ["gate.set", "gate.cleared", "gate.expired"]
`)
	if _, err := Load(); err != nil {
		t.Fatalf("Load() = %v, want a role bound only to gate.* types to be valid", err)
	}
}

// The retired file-backed-gate keys are still DECODED (so an existing config
// keeps loading) but have no effect.
func TestLoad_retiredGatePathKeysAreIgnored(t *testing.T) {
	absentGlobalConfig(t)
	writeCfg(t, `
[pool]
operator_paused_path = "/x/op"
cicd_down_path = "/x/ci"
disk_space_low_path = "/x/disk"

[[query]]
name = "q"
type = "timer"
emits = ["e"]

[[role]]
name = "r"
binds = ["e"]
`)
	if _, err := Load(); err != nil {
		t.Fatalf("Load() = %v, want the retired keys ignored, not rejected", err)
	}
}
