package keeper

import (
	"encoding/json"
	"slices"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-keep-awake/internal/lease"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-keep-awake/internal/policy"
)

// SnapshotInputs are one tick's inputs to BuildSnapshot. The builder computes
// none of the keeper's runtime values: the keeper reads the heartbeat file's
// mtime, the config loader's error, pause.json and anchor-exit.json and hands
// them in, together with the tick's sweep, gate and reconcile results.
type SnapshotInputs struct {
	// Now is the tick's time (the keeper's injectable clock). It is
	// written_at, the base of heartbeat_age_s and of last_release.at.
	Now time.Time

	// KeeperPID and KeeperVersion fill the keeper block.
	KeeperPID     int
	KeeperVersion string
	// HeartbeatMTime is the keeper.heartbeat file's modification time. The
	// zero time (no heartbeat file yet) gives heartbeat_age_s 0.
	HeartbeatMTime time.Time
	// HeartbeatInterval is the keeper's effective heartbeat interval; it fills
	// the additive heartbeat_interval_s that `status` judges staleness by.
	HeartbeatInterval time.Duration
	// ConfigError is the config loader's error text; empty means no error
	// (keeper.config_error is null).
	ConfigError string

	// Pause is the contents of pause.json, or nil. A past Until is reported as
	// no pause (the keeper removes the file).
	Pause *lease.PauseFile
	// Gate is the battery rule's verdict for the tick.
	Gate policy.Status
	// Live are the sweep's live leases (Result.Live).
	Live []LiveLease
	// Reconcile is the tick's Reconcile result.
	Reconcile ReconcileResult

	// AnchorExit is the anchor-exit.json read this tick, or nil.
	AnchorExit *lease.AnchorExitFile
	// KeeperStop is true when the keeper itself stopped the held pair at
	// shutdown (Reconciler.Stop returned true).
	KeeperStop bool
	// PrevLastRelease is the previous snapshot's last_release, carried forward
	// unless a release happened this tick.
	PrevLastRelease *lease.StatusRelease
	// PrevUnexplainedTicks is the consecutive-degraded-tick counter after the
	// previous tick (the previous snapshot's unexplained_ticks).
	PrevUnexplainedTicks int

	// External are the non-keeper assertion holders (ExternalProbe.Probe).
	External []lease.StatusExternal
}

// BuildSnapshot builds status.json for one tick. Leases lists the live leases
// only: a lease the sweep reaped is already gone from disk, and `status`
// treats a dead entry here as authoritative for a lease file of the same id,
// so listing a reaped lease would mislabel a lease re-acquired in the next
// moments. A dead lease that is still on disk (the sweep was skipped) is judged
// by `status` itself.
func BuildSnapshot(in SnapshotInputs) *lease.StatusFile {
	now := in.Now
	rr := in.Reconcile

	s := &lease.StatusFile{
		SchemaVersion: lease.SchemaVersion,
		WrittenAt:     now.UTC(),
		Keeper: lease.StatusKeeper{
			PID:           in.KeeperPID,
			Version:       in.KeeperVersion,
			HeartbeatAgeS: heartbeatAgeS(now, in.HeartbeatMTime),
		},
		Assertion: lease.StatusAssertion{
			Held:  rr.Held,
			Flags: slices.Clone(rr.Flags),
			PID:   rr.CaffeinatePID,
		},
		Battery:            batteryBlock(in.Gate),
		Leases:             leaseBlock(in.Live),
		LastRelease:        resolveLastRelease(in),
		External:           slices.Clone(in.External),
		HeartbeatIntervalS: ceilSeconds(in.HeartbeatInterval),
	}
	if in.ConfigError != "" {
		msg := in.ConfigError
		s.Keeper.ConfigError = &msg
	}
	if s.Assertion.Flags == nil {
		s.Assertion.Flags = []string{}
	}
	if s.External == nil {
		s.External = []lease.StatusExternal{}
	}

	paused := in.Pause != nil && !in.Pause.Expired(now)
	if paused {
		p := *in.Pause
		p.SchemaVersion = lease.SchemaVersion
		s.Paused = &p
	}

	// Exit-6 evidence: the tick is "unexplained" when live leases want a held
	// assertion that is not held and neither the gate, a pause nor the linger
	// window accounts for it.
	if rr.LingerActive && rr.LingerUntil != nil {
		t := rr.LingerUntil.UTC()
		s.LingerUntil = &t
	}
	if len(in.Live) > 0 && rr.WantHold && !rr.Held && !paused && !rr.LingerActive && !in.Gate.Gate.Restricts() {
		s.UnexplainedTicks = in.PrevUnexplainedTicks + 1
	}
	return s
}

// heartbeatAgeS is the age of the heartbeat file at now in seconds, never
// negative; a zero mtime (no file) is 0.
func heartbeatAgeS(now, mtime time.Time) float64 {
	if mtime.IsZero() {
		return 0
	}
	return max(0, now.Sub(mtime).Seconds())
}

// batteryBlock maps the gate verdict onto the status battery block. read_ok is
// false when the rule is disabled (no read was made) or any consecutive read
// failed; the writer renders percent and on_battery as null then.
func batteryBlock(g policy.Status) lease.StatusBattery {
	b := lease.StatusBattery{
		Percent:   g.Reading.Percent,
		OnBattery: g.Reading.OnBattery,
		Gate:      g.Gate.String(),
		ReadOK:    g.ReadFailures == 0 && g.Gate != policy.GateDisabled,
	}
	if !b.ReadOK {
		b.Percent, b.OnBattery = 0, false
	}
	// A good read with no charge (a desktop on AC) keeps policy.NoPercent in
	// memory; MarshalSnapshot renders it as null.
	return b
}

func leaseBlock(live []LiveLease) []lease.StatusLease {
	out := make([]lease.StatusLease, 0, len(live))
	for _, l := range live {
		out = append(out, lease.StatusLease{
			ID: l.ID, Owner: l.Owner, Key: l.Key, Kind: l.Kind,
			ExpiresAt: l.ExpiresAt, Live: true,
		})
	}
	return out
}

// resolveLastRelease carries the previous last_release forward and replaces it
// only when a release happened this tick. A release is an anchor exit record
// read this tick, a stop the reconciler performed, a stop at shutdown, or a
// pair found gone with no assertion held after the tick. The reason comes from,
// in order: the anchor's exit record; the reconciler's own release reason
// (release_threshold, blind_threshold, no_leases or paused, which it derives
// from the gate and the pause); keeper_stop at shutdown; for a lost pair the
// gate (released: release_threshold, or blind_threshold under the blind rule),
// else unknown. at is the keeper's clock.
func resolveLastRelease(in SnapshotInputs) *lease.StatusRelease {
	rr := in.Reconcile
	var reason string
	switch {
	case in.AnchorExit != nil:
		reason = knownReleaseReason(in.AnchorExit.Reason)
	case rr.Stopped:
		reason = knownReleaseReason(rr.Release)
	case in.KeeperStop:
		reason = lease.ReasonKeeperStop
	case rr.PairLost && !rr.Held:
		switch {
		case in.Gate.Gate != policy.GateReleased:
			reason = lease.ReasonUnknown
		case in.Gate.Blind:
			reason = lease.ReasonBlindThreshold
		default:
			reason = lease.ReasonReleaseThreshold
		}
	default:
		return carryRelease(in.PrevLastRelease)
	}
	return &lease.StatusRelease{At: in.Now.UTC(), Reason: reason}
}

func carryRelease(prev *lease.StatusRelease) *lease.StatusRelease {
	if prev == nil {
		return nil
	}
	c := *prev
	return &c
}

// knownReleaseReason returns r when it is one of the last_release reasons and
// unknown otherwise, so spawn_blocked (a metric reason only) or a garbled exit
// record can never reach status.json.
func knownReleaseReason(r string) string {
	switch r {
	case lease.ReasonReleaseThreshold, lease.ReasonBlindThreshold, lease.ReasonWatchdog,
		lease.ReasonNoLeases, lease.ReasonPaused, lease.ReasonKeeperStop:
		return r
	}
	return lease.ReasonUnknown
}

// statusWire is status.json as written: lease.StatusFile with the battery block
// shadowed so percent and on_battery are JSON null when the read failed (the
// lease type holds them as plain values). Fields of the shallower struct win in
// encoding/json, and a null decodes into the lease type as the zero value.
type statusWire struct {
	lease.StatusFile
	Battery batteryWire `json:"battery"`
}

type batteryWire struct {
	Percent   *int   `json:"percent"`
	OnBattery *bool  `json:"on_battery"`
	Gate      string `json:"gate"`
	ReadOK    bool   `json:"read_ok"`
}

// MarshalSnapshot renders s as status.json bytes (indented, newline-ended).
func MarshalSnapshot(s *lease.StatusFile) ([]byte, error) {
	w := statusWire{StatusFile: *s, Battery: batteryWire{Gate: s.Battery.Gate, ReadOK: s.Battery.ReadOK}}
	w.SchemaVersion = lease.SchemaVersion
	if s.Battery.ReadOK {
		on := s.Battery.OnBattery
		w.Battery.OnBattery = &on
		if s.Battery.Percent >= 0 {
			pct := s.Battery.Percent
			w.Battery.Percent = &pct
		}
	}
	data, err := json.MarshalIndent(&w, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// WriteSnapshot writes s as status.json atomically: a mode 0600 temp file in
// the state directory renamed into place (lease.Dir.WriteAtomic), so a reader
// never sees a partial file. The keeper calls it once per tick, before it
// touches the heartbeat, so `status` never sees a heartbeat newer than the
// snapshot.
func WriteSnapshot(dir *lease.Dir, s *lease.StatusFile) error {
	data, err := MarshalSnapshot(s)
	if err != nil {
		return err
	}
	return dir.WriteAtomic(lease.FileStatus, data)
}
