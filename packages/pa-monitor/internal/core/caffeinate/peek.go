package caffeinate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// The in-process keeper reading. Launching `pg-keep-awake status --json
// --check` every daemon tick costs one process launch (which endpoint security
// software inspects) per tick even when nothing is held. pg-keep-awake's keeper
// rewrites status.json and touches keeper.heartbeat every heartbeat, so an idle
// reading can come straight from those files. Anything that needs the binary's
// own judgement (a live lease, a held assertion, a pause, a linger, unexplained
// ticks, an unreadable file) still goes to the binary: peekKeeper reports
// ok=false and the caller launches it. Pid liveness and the degraded rule are
// deliberately NOT reimplemented here.

const (
	// peekSchemaVersion is the status.json schema_version this reader knows. A
	// newer file is handed to the binary.
	peekSchemaVersion = 1
	// peekDefaultHeartbeatS is the heartbeat interval used when status.json
	// does not carry one (the binary's own fallback).
	peekDefaultHeartbeatS = 10
	keeperStateDirName    = "pg-keep-awake"
	statusFileName        = "status.json"
	heartbeatFileName     = "keeper.heartbeat"
)

// peekStatus is the part of status.json the in-process reader looks at.
type peekStatus struct {
	SchemaVersion int `json:"schema_version"`
	Assertion     struct {
		Held bool `json:"held"`
	} `json:"assertion"`
	Paused  json.RawMessage `json:"paused"`
	Battery struct {
		Percent   int    `json:"percent"`
		OnBattery bool   `json:"on_battery"`
		Gate      string `json:"gate"`
		ReadOK    bool   `json:"read_ok"`
	} `json:"battery"`
	Leases []struct {
		Live bool `json:"live"`
	} `json:"leases"`
	HeartbeatIntervalS int             `json:"heartbeat_interval_s"`
	UnexplainedTicks   int             `json:"unexplained_ticks"`
	LingerUntil        json.RawMessage `json:"linger_until"`
}

// keeperStateDir resolves the pg-keep-awake state directory as the binary does:
// ${XDG_STATE_HOME:-~/.local/state}/pg-keep-awake, ignoring a relative
// XDG_STATE_HOME. ok is false when it cannot be resolved.
func keeperStateDir() (string, bool) {
	if x := os.Getenv("XDG_STATE_HOME"); x != "" && filepath.IsAbs(x) {
		return filepath.Join(x, keeperStateDirName), true
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", false
	}
	return filepath.Join(home, ".local", "state", keeperStateDirName), true
}

func isJSONNull(raw json.RawMessage) bool {
	s := string(raw)
	return len(raw) == 0 || s == "null"
}

// peekKeeper reads the keeper's files and returns an Observation without
// launching anything. ok is false when the binary must be asked instead.
func peekKeeper(now time.Time) (obs Observation, ok bool) {
	dir, resolved := keeperStateDir()
	if !resolved {
		return Observation{}, false
	}
	data, statErr := os.ReadFile(filepath.Join(dir, statusFileName))
	fi, hbErr := os.Lstat(filepath.Join(dir, heartbeatFileName))
	haveHB := hbErr == nil && fi.Mode().IsRegular()

	if statErr != nil {
		if os.IsNotExist(statErr) && os.IsNotExist(hbErr) {
			// Never started, or both files removed: the keeper is down.
			return Observation{Health: HealthDown, StatusKnown: true, Gate: GateUnknown}, true
		}
		return Observation{}, false
	}
	var s peekStatus
	if err := json.Unmarshal(data, &s); err != nil || s.SchemaVersion > peekSchemaVersion {
		return Observation{}, false
	}
	if !haveHB {
		return Observation{}, false
	}
	interval := peekDefaultHeartbeatS
	if s.HeartbeatIntervalS > 0 {
		interval = s.HeartbeatIntervalS
	}
	age := now.Sub(fi.ModTime())
	if age < 0 {
		age = 0
	}
	down := age > 2*time.Duration(interval)*time.Second

	obs = Observation{
		StatusKnown:   true,
		Held:          s.Assertion.Held,
		Gate:          ParseBatteryGate(s.Battery.Gate),
		OnBattery:     s.Battery.OnBattery,
		Percent:       s.Battery.Percent,
		BatteryReadOK: s.Battery.ReadOK,
	}
	if down {
		obs.Health = HealthDown
		return obs, true
	}
	if s.Assertion.Held || !isJSONNull(s.Paused) || !isJSONNull(s.LingerUntil) || s.UnexplainedTicks > 0 {
		return Observation{}, false
	}
	for _, l := range s.Leases {
		if l.Live {
			return Observation{}, false
		}
	}
	obs.Health = HealthOK
	return obs, true
}
