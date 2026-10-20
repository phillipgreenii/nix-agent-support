package lease

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// SchemaVersion is the schema_version every writer stamps and the newest one
// a reader understands.
const SchemaVersion = 1

// State-file names inside the state directory.
const (
	FileAssertion  = "assertion.json"
	FileStatus     = "status.json"
	FilePause      = "pause.json"
	FileAnchorExit = "anchor-exit.json"
	FileLastGood   = "config.last-good.toml"
)

// Lease kinds.
const (
	KindShim    = "shim"
	KindRun     = "run"
	KindAcquire = "acquire"
)

// ShimOwner is the owner every shim lease carries.
const ShimOwner = "shim"

// Release and anchor-exit reasons.
const (
	ReasonReleaseThreshold = "release_threshold"
	ReasonBlindThreshold   = "blind_threshold"
	ReasonWatchdog         = "watchdog"
	ReasonNoLeases         = "no_leases"
	ReasonPaused           = "paused"
	ReasonKeeperStop       = "keeper_stop"
	ReasonUnknown          = "unknown"
)

// ErrUnreadable matches (via errors.Is) every *UnreadableError.
var ErrUnreadable = errors.New("state file unreadable: schema_version newer than this reader")

// UnreadableError reports a file whose schema_version is greater than
// SchemaVersion. The file MUST be skipped, shown by `status` as unreadable,
// and MUST NOT be deleted.
type UnreadableError struct {
	Path    string
	Version int
}

func (e *UnreadableError) Error() string {
	return fmt.Sprintf("%s: schema_version %d is newer than supported %d", e.Path, e.Version, SchemaVersion)
}

// Is reports that every UnreadableError matches ErrUnreadable.
func (e *UnreadableError) Is(target error) bool { return target == ErrUnreadable }

var idPartRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// ValidIDPart reports whether s is a legal lease owner or key.
func ValidIDPart(s string) bool { return idPartRE.MatchString(s) }

// ProcRef identifies a process by pid and kernel start time (microseconds
// since the epoch), so a recycled pid is not mistaken for the original.
type ProcRef struct {
	PID     int   `json:"pid"`
	StartUS int64 `json:"start_us"`
}

// Lease is leases/<id>.json: one request to keep the Mac awake.
type Lease struct {
	SchemaVersion int      `json:"schema_version"`
	ID            string   `json:"id"`
	Owner         string   `json:"owner"`
	Key           string   `json:"key"`
	Kind          string   `json:"kind"`
	Client        *ProcRef `json:"client"`
	// ExpiresAt is an RFC 3339 wall-clock time, or nil for no expiry.
	ExpiresAt *time.Time `json:"expires_at"`
	Watch     *ProcRef   `json:"watch"`
	// Flags is a subset of d i m s.
	Flags []string `json:"flags"`
	// Argv is for display only and MUST NOT be executed.
	Argv      []string  `json:"argv"`
	CreatedAt time.Time `json:"created_at"`
}

// MakeID builds the lease id "<owner>.<key>".
func MakeID(owner, key string) string { return owner + "." + key }

// Validate checks the creation rules and fills ID and SchemaVersion. A lease
// with none of client, expires_at, watch is refused, so a lease can never be
// unbounded and unowned.
func (l *Lease) Validate() error {
	if !ValidIDPart(l.Owner) {
		return fmt.Errorf("lease owner %q must match %s", l.Owner, idPartRE)
	}
	if !ValidIDPart(l.Key) {
		return fmt.Errorf("lease key %q must match %s", l.Key, idPartRE)
	}
	id := MakeID(l.Owner, l.Key)
	if l.ID != "" && l.ID != id {
		return fmt.Errorf("lease id %q does not equal owner.key %q", l.ID, id)
	}
	switch l.Kind {
	case KindShim:
		if l.Owner != ShimOwner {
			return fmt.Errorf("a shim lease must have owner %q, got %q", ShimOwner, l.Owner)
		}
	case KindRun, KindAcquire:
	default:
		return fmt.Errorf("lease kind %q must be shim, run or acquire", l.Kind)
	}
	if l.Client == nil && l.ExpiresAt == nil && l.Watch == nil {
		return errors.New("a lease must have at least one of client, expires_at, watch")
	}
	for name, p := range map[string]*ProcRef{"client": l.Client, "watch": l.Watch} {
		if p != nil && (p.PID <= 0 || p.StartUS <= 0) {
			return fmt.Errorf("lease %s needs a positive pid and start_us", name)
		}
	}
	for _, f := range l.Flags {
		if len(f) != 1 || !strings.Contains("dims", f) {
			return fmt.Errorf("lease flag %q must be one of d i m s", f)
		}
	}
	l.ID = id
	l.SchemaVersion = SchemaVersion
	return nil
}

// Live reports whether the lease still holds its claim at now. It is live iff
// every one of these holds: client is nil, or the client pid exists with a
// matching start_us and is not a zombie; expires_at is nil or in the future;
// watch is nil or the watched pid exists with a matching start time.
// A nil prober means the real sysctl-backed SysProber.
func (l *Lease) Live(now time.Time, p Prober) bool {
	if p == nil {
		p = SysProber{}
	}
	if l.Client != nil {
		st := p.Probe(l.Client.PID)
		if !st.Exists || st.StartUS != l.Client.StartUS || st.Zombie {
			return false
		}
	}
	if l.ExpiresAt != nil && !l.ExpiresAt.After(now) {
		return false
	}
	if l.Watch != nil {
		st := p.Probe(l.Watch.PID)
		if !st.Exists || st.StartUS != l.Watch.StartUS {
			return false
		}
	}
	return true
}

// ProcDesc is a process the keeper started, with its argv for display.
type ProcDesc struct {
	PID     int      `json:"pid"`
	StartUS int64    `json:"start_us"`
	Argv    []string `json:"argv"`
}

// AssertionParams records the parameters the assertion was started with.
type AssertionParams struct {
	ReleasePct int `json:"release_pct"`
	BlindPct   int `json:"blind_pct"`
	// WatchdogSeconds and HeartbeatSeconds are fractional seconds: a sub-second
	// configured value (heartbeat_interval = "200ms" records 0.2) is kept as is,
	// and whole seconds still encode as integers (10, not 10.0).
	WatchdogSeconds  float64 `json:"watchdog_seconds"`
	HeartbeatSeconds float64 `json:"heartbeat_seconds"`
	StateDir         string  `json:"state_dir"`
}

// AssertionFile is assertion.json: the anchor and caffeinate the keeper started.
type AssertionFile struct {
	SchemaVersion int             `json:"schema_version"`
	Anchor        ProcDesc        `json:"anchor"`
	Caffeinate    ProcDesc        `json:"caffeinate"`
	Flags         []string        `json:"flags"`
	Params        AssertionParams `json:"params"`
	StartedAt     time.Time       `json:"started_at"`
	KeeperVersion string          `json:"keeper_version"`
}

// StatusKeeper is the keeper's own block of status.json.
type StatusKeeper struct {
	PID           int     `json:"pid"`
	Version       string  `json:"version"`
	HeartbeatAgeS float64 `json:"heartbeat_age_s"`
	ConfigError   *string `json:"config_error"`
}

// StatusAssertion says whether the keeper currently holds an assertion.
type StatusAssertion struct {
	Held  bool     `json:"held"`
	Flags []string `json:"flags"`
	PID   int      `json:"pid"`
}

// StatusBattery is the keeper's last battery reading and gate. Gate is one of
// ok, no_spawn, released, unknown, disabled (pa-monitor battery.go names).
type StatusBattery struct {
	Percent   int    `json:"percent"`
	OnBattery bool   `json:"on_battery"`
	Gate      string `json:"gate"`
	ReadOK    bool   `json:"read_ok"`
}

// StatusLease is one lease as the keeper judged it on its last tick.
type StatusLease struct {
	ID        string     `json:"id"`
	Owner     string     `json:"owner"`
	Key       string     `json:"key"`
	Kind      string     `json:"kind"`
	ExpiresAt *time.Time `json:"expires_at"`
	Live      bool       `json:"live"`
	Reason    string     `json:"reason"`
}

// StatusRelease records what released the assertion last. Reason is one of the
// Reason* constants.
type StatusRelease struct {
	At     time.Time `json:"at"`
	Reason string    `json:"reason"`
}

// StatusExternal is a non-keeper process holding a power assertion.
type StatusExternal struct {
	PID     int     `json:"pid"`
	Process string  `json:"process"`
	Type    string  `json:"type"`
	AgeS    float64 `json:"age_s"`
}

// StatusFile is status.json: the snapshot the keeper writes each tick and
// `status` reads. `status` MUST NOT read the configuration, so everything it
// needs to answer `--check` travels in this file.
//
// Additive fields beyond the design's example (readers ignore unknown fields,
// and both default to their zero value when absent):
//
//   - HeartbeatIntervalS is the keeper's heartbeat interval in seconds.
//     `status` judges heartbeat staleness against it and falls back to 10 only
//     when the field is absent or zero. It is needed because assertion.json
//     does not exist while the keeper has never held an assertion.
//   - UnexplainedTicks and LingerUntil are the exit-6 evidence. `status
//     --check` exits 6 when leases are live, no assertion is held, and none of
//     the gate (Battery.Gate), a pause (Paused) or the linger window
//     (LingerUntil in the future) explains it, for more than one tick.
//     UnexplainedTicks is the number of consecutive keeper ticks on which that
//     condition held (reset to 0 on any tick it did not); exit 6 means
//     UnexplainedTicks > 1. LingerUntil is when the post-release linger window
//     ends, nil when no linger is running.
type StatusFile struct {
	SchemaVersion      int              `json:"schema_version"`
	WrittenAt          time.Time        `json:"written_at"`
	Keeper             StatusKeeper     `json:"keeper"`
	Assertion          StatusAssertion  `json:"assertion"`
	Paused             *PauseFile       `json:"paused"`
	Battery            StatusBattery    `json:"battery"`
	Leases             []StatusLease    `json:"leases"`
	LastRelease        *StatusRelease   `json:"last_release"`
	External           []StatusExternal `json:"external"`
	HeartbeatIntervalS int              `json:"heartbeat_interval_s,omitempty"`
	UnexplainedTicks   int              `json:"unexplained_ticks,omitempty"`
	LingerUntil        *time.Time       `json:"linger_until,omitempty"`
}

// PauseFile is pause.json: an active pause. Until is nil for a pause with no
// --for. The keeper treats a past Until as no pause and removes the file.
type PauseFile struct {
	SchemaVersion int        `json:"schema_version"`
	Until         *time.Time `json:"until"`
	CreatedAt     time.Time  `json:"created_at"`
	ByPID         int        `json:"by_pid"`
}

// Expired reports whether the pause has ended at now.
func (p *PauseFile) Expired(now time.Time) bool {
	return p.Until != nil && !p.Until.After(now)
}

// AnchorExitFile is anchor-exit.json: why the anchor last exited. Reason is
// release_threshold, blind_threshold or watchdog; an anchor killed by SIGKILL
// leaves no file, so its reason is unknown.
type AnchorExitFile struct {
	SchemaVersion int       `json:"schema_version"`
	At            time.Time `json:"at"`
	Reason        string    `json:"reason"`
	Percent       int       `json:"percent"`
	PID           int       `json:"pid"`
}
