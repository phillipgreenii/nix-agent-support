package config

import (
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-router/internal/query"
)

// non_blocking_gates is removed everywhere (the gate mechanism is gone): a
// [[query]] carrying it is rejected as an unknown key rather than ignored.
func TestLoad_removedNonBlockingGatesKeyIsRejectedOnAQuery(t *testing.T) {
	absentGlobalConfig(t)
	writeCfg(t, `
[[query]]
name = "q"
type = "timer"
emits = ["e"]
non_blocking_gates = ["LOW_DISK_USAGE"]

[[role]]
name = "r"
handler = "/bin/h"
binds = ["e"]
`)
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "non_blocking_gates") {
		t.Fatalf("Load() = %v, want an unknown-key error naming non_blocking_gates", err)
	}
}

// The gate record event types (gate.set, gate.cleared, gate.expired) are no
// longer emitted by the core: a role bound only to them is an orphan consumer.
func TestValidate_removedGateEventTypesAreOrphanConsumers(t *testing.T) {
	absentGlobalConfig(t)
	writeCfg(t, `
[[query]]
name = "q"
type = "timer"
emits = ["e"]

[[role]]
name = "r"
handler = "/bin/h"
binds = ["e"]

[[role]]
name = "gate-listener"
handler = "/bin/h"
binds = ["gate.set", "gate.cleared", "gate.expired"]
`)
	if _, err := Load(); err == nil {
		t.Fatal("Load() succeeded, want a role bound only to the removed gate.* types rejected")
	}
}

// The timer emitter decodes with no sub-table, is recognized by query.IsTimer
// (the one emitter no level blocks), and a command query is not.
func TestLoad_timerQueryTypeIsTheLevelImmuneEmitter(t *testing.T) {
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
handler = "/bin/h"
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

// The retired file-backed-pause keys are still DECODED (so an existing config
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
handler = "/bin/h"
binds = ["e"]
`)
	if _, err := Load(); err != nil {
		t.Fatalf("Load() = %v, want the retired keys ignored, not rejected", err)
	}
}
