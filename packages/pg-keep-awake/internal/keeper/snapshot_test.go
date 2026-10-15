package keeper

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-keep-awake/internal/lease"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-keep-awake/internal/policy"
)

var snapNow = time.Date(2026, 10, 14, 20, 0, 10, 0, time.UTC)

func acGate() policy.Status {
	return policy.Status{Gate: policy.GateOK, Reading: policy.Reading{OnBattery: false, Percent: 64}}
}

func heldResult() ReconcileResult {
	return ReconcileResult{Held: true, Flags: []string{"i", "s"}, AnchorPID: 100, CaffeinatePID: 101, WantHold: true}
}

func liveOne() []LiveLease {
	return []LiveLease{{ID: "pa-monitor.default", Owner: "pa-monitor", Key: "default", Kind: lease.KindAcquire, Flags: []string{"i"}}}
}

func baseInputs() SnapshotInputs {
	return SnapshotInputs{
		Now:               snapNow,
		KeeperPID:         4242,
		KeeperVersion:     "v0.1.0",
		HeartbeatMTime:    snapNow.Add(-4 * time.Second),
		HeartbeatInterval: 10 * time.Second,
		Gate:              acGate(),
		Live:              liveOne(),
		Reconcile:         heldResult(),
	}
}

func rawStatus(t *testing.T, d *lease.Dir) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(d.Path(), lease.FileStatus))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("status.json is not JSON: %v\n%s", err, data)
	}
	return m
}

// TestSnapshotRoundTripsEveryField builds a snapshot with every field populated,
// writes it, and reads it back through the status.json type.
func TestSnapshotRoundTripsEveryField(t *testing.T) {
	d := newDir(t)
	exp := snapNow.Add(5 * time.Minute)
	until := snapNow.Add(time.Hour)
	linger := snapNow.Add(30 * time.Second)

	in := baseInputs()
	in.ConfigError = "line 3: bad value"
	in.Pause = &lease.PauseFile{Until: &until, CreatedAt: snapNow.Add(-time.Minute), ByPID: 4412}
	in.Gate = policy.Status{Gate: policy.GateNoSpawn, Reading: policy.Reading{OnBattery: true, Percent: 27}}
	in.Live = []LiveLease{
		{ID: "pa-monitor.default", Owner: "pa-monitor", Key: "default", Kind: lease.KindAcquire, Flags: []string{"i"}},
		{ID: "shim.4412-1", Owner: "shim", Key: "4412-1", Kind: lease.KindShim, Flags: []string{"s"}, ExpiresAt: &exp},
	}
	in.Reconcile = ReconcileResult{
		Held: true, Flags: []string{"i", "s"}, AnchorPID: 100, CaffeinatePID: 101, WantHold: true,
		LingerActive: true, LingerUntil: &linger,
	}
	in.PrevLastRelease = &lease.StatusRelease{At: snapNow.Add(-time.Hour), Reason: lease.ReasonWatchdog}
	in.PrevUnexplainedTicks = 1
	in.External = []lease.StatusExternal{{PID: 419, Process: "powerd", Type: "PreventUserIdleSystemSleep", AgeS: 95}}

	snap := BuildSnapshot(in)
	if err := WriteSnapshot(d, snap); err != nil {
		t.Fatalf("WriteSnapshot: %v", err)
	}
	got, err := d.ReadStatus()
	if err != nil {
		t.Fatalf("ReadStatus: %v", err)
	}

	want := &lease.StatusFile{
		SchemaVersion: lease.SchemaVersion,
		WrittenAt:     snapNow,
		Keeper:        lease.StatusKeeper{PID: 4242, Version: "v0.1.0", HeartbeatAgeS: 4, ConfigError: ptr("line 3: bad value")},
		Assertion:     lease.StatusAssertion{Held: true, Flags: []string{"i", "s"}, PID: 101},
		Paused:        &lease.PauseFile{SchemaVersion: lease.SchemaVersion, Until: &until, CreatedAt: snapNow.Add(-time.Minute), ByPID: 4412},
		Battery:       lease.StatusBattery{Percent: 27, OnBattery: true, Gate: "no_spawn", ReadOK: true},
		Leases: []lease.StatusLease{
			{ID: "pa-monitor.default", Owner: "pa-monitor", Key: "default", Kind: lease.KindAcquire, Live: true},
			{ID: "shim.4412-1", Owner: "shim", Key: "4412-1", Kind: lease.KindShim, ExpiresAt: &exp, Live: true},
		},
		LastRelease:        &lease.StatusRelease{At: snapNow.Add(-time.Hour), Reason: lease.ReasonWatchdog},
		External:           []lease.StatusExternal{{PID: 419, Process: "powerd", Type: "PreventUserIdleSystemSleep", AgeS: 95}},
		HeartbeatIntervalS: 10,
		UnexplainedTicks:   0,
		LingerUntil:        &linger,
	}
	if !reflect.DeepEqual(got, want) {
		g, _ := json.MarshalIndent(got, "", " ")
		w, _ := json.MarshalIndent(want, "", " ")
		t.Fatalf("round trip mismatch\n got: %s\nwant: %s", g, w)
	}

	raw := rawStatus(t, d)
	for _, k := range []string{"schema_version", "written_at", "keeper", "assertion", "paused", "battery", "leases", "last_release", "external"} {
		if _, ok := raw[k]; !ok {
			t.Errorf("status.json lacks key %q", k)
		}
	}
	kb := raw["keeper"].(map[string]any)
	for _, k := range []string{"pid", "version", "heartbeat_age_s", "config_error"} {
		if _, ok := kb[k]; !ok {
			t.Errorf("keeper block lacks %q", k)
		}
	}
}

func ptr[T any](v T) *T { return &v }

// TestSnapshotQuietTickIsNullAndEmpty: no pause, no config error and no
// release are JSON null, and the lists are [] and never null.
func TestSnapshotQuietTickIsNullAndEmpty(t *testing.T) {
	d := newDir(t)
	in := baseInputs()
	in.Live = nil
	in.Reconcile = ReconcileResult{}
	if err := WriteSnapshot(d, BuildSnapshot(in)); err != nil {
		t.Fatal(err)
	}
	raw := rawStatus(t, d)
	if raw["paused"] != nil || raw["last_release"] != nil {
		t.Errorf("paused=%v last_release=%v, want null", raw["paused"], raw["last_release"])
	}
	if raw["keeper"].(map[string]any)["config_error"] != nil {
		t.Errorf("config_error = %v, want null", raw["keeper"].(map[string]any)["config_error"])
	}
	for _, k := range []string{"leases", "external"} {
		if l, ok := raw[k].([]any); !ok || len(l) != 0 {
			t.Errorf("%s = %#v, want []", k, raw[k])
		}
	}
	if f, ok := raw["assertion"].(map[string]any)["flags"].([]any); !ok || len(f) != 0 {
		t.Errorf("assertion.flags = %#v, want []", raw["assertion"].(map[string]any)["flags"])
	}
}

func TestSnapshotBatteryBlock(t *testing.T) {
	cases := []struct {
		name         string
		gate         policy.Status
		wantGate     string
		wantReadOK   bool
		wantPct      any
		wantOnBatter any
	}{
		{"ok on AC", policy.Status{Gate: policy.GateOK, Reading: policy.Reading{Percent: 64}}, "ok", true, 64.0, false},
		{"no_spawn on battery", policy.Status{Gate: policy.GateNoSpawn, Reading: policy.Reading{OnBattery: true, Percent: 25}}, "no_spawn", true, 25.0, true},
		{"released", policy.Status{Gate: policy.GateReleased, Reading: policy.Reading{OnBattery: true, Percent: 19}}, "released", true, 19.0, true},
		{"unknown after a failed read", policy.Status{Gate: policy.GateUnknown, Reading: policy.Reading{Percent: policy.NoPercent}, ReadFailures: 1}, "unknown", false, nil, nil},
		{"blind release has no reading", policy.Status{Gate: policy.GateReleased, Blind: true, Reading: policy.Reading{Percent: policy.NoPercent}, ReadFailures: 3}, "released", false, nil, nil},
		{"disabled makes no read", policy.Status{Gate: policy.GateDisabled, Reading: policy.Reading{Percent: policy.NoPercent}}, "disabled", false, nil, nil},
		{"AC with no charge reading (desktop)", policy.Status{Gate: policy.GateOK, Reading: policy.Reading{Percent: policy.NoPercent}}, "ok", true, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := newDir(t)
			in := baseInputs()
			in.Gate = tc.gate
			if err := WriteSnapshot(d, BuildSnapshot(in)); err != nil {
				t.Fatal(err)
			}
			b := rawStatus(t, d)["battery"].(map[string]any)
			if b["gate"] != tc.wantGate || b["read_ok"] != tc.wantReadOK {
				t.Errorf("gate=%v read_ok=%v, want %v %v", b["gate"], b["read_ok"], tc.wantGate, tc.wantReadOK)
			}
			if b["percent"] != tc.wantPct || b["on_battery"] != tc.wantOnBatter {
				t.Errorf("percent=%v on_battery=%v, want %v %v", b["percent"], b["on_battery"], tc.wantPct, tc.wantOnBatter)
			}
			if _, err := d.ReadStatus(); err != nil {
				t.Errorf("a null battery field must still read back: %v", err)
			}
		})
	}
}

func TestSnapshotGateNamesAreTheStatusWireNames(t *testing.T) {
	allowed := map[string]bool{"ok": true, "no_spawn": true, "released": true, "unknown": true, "disabled": true}
	for _, g := range []policy.Gate{policy.GateDisabled, policy.GateOK, policy.GateNoSpawn, policy.GateReleased, policy.GateUnknown} {
		in := baseInputs()
		in.Gate = policy.Status{Gate: g}
		got := BuildSnapshot(in).Battery.Gate
		if !allowed[got] {
			t.Errorf("gate %d rendered as %q", g, got)
		}
	}
}

func TestSnapshotLastRelease(t *testing.T) {
	prev := &lease.StatusRelease{At: snapNow.Add(-2 * time.Hour), Reason: lease.ReasonNoLeases}
	released := func(blind bool) policy.Status {
		return policy.Status{Gate: policy.GateReleased, Blind: blind, Reading: policy.Reading{OnBattery: true, Percent: 19}}
	}
	cases := []struct {
		name string
		mod  func(*SnapshotInputs)
		want string // "" means last_release carried from prev
	}{
		{"no release this tick carries the previous one", func(in *SnapshotInputs) {}, ""},
		{"anchor exit release_threshold", func(in *SnapshotInputs) {
			in.AnchorExit = &lease.AnchorExitFile{Reason: lease.ReasonReleaseThreshold}
			in.Reconcile = ReconcileResult{PairLost: true}
		}, lease.ReasonReleaseThreshold},
		{"anchor exit blind_threshold", func(in *SnapshotInputs) {
			in.AnchorExit = &lease.AnchorExitFile{Reason: lease.ReasonBlindThreshold}
			in.Reconcile = ReconcileResult{PairLost: true}
		}, lease.ReasonBlindThreshold},
		{"anchor exit watchdog", func(in *SnapshotInputs) {
			in.AnchorExit = &lease.AnchorExitFile{Reason: lease.ReasonWatchdog}
			in.Reconcile = ReconcileResult{PairLost: true}
		}, lease.ReasonWatchdog},
		{"anchor exit wins over the keeper's own stop", func(in *SnapshotInputs) {
			in.AnchorExit = &lease.AnchorExitFile{Reason: lease.ReasonWatchdog}
			in.Reconcile = ReconcileResult{Stopped: true, Release: lease.ReasonPaused}
		}, lease.ReasonWatchdog},
		{"garbled anchor exit reason is unknown", func(in *SnapshotInputs) {
			in.AnchorExit = &lease.AnchorExitFile{Reason: "spawn_blocked"}
		}, lease.ReasonUnknown},
		{"keeper stop from the release level", func(in *SnapshotInputs) {
			in.Gate = released(false)
			in.Reconcile = ReconcileResult{Stopped: true, Release: lease.ReasonReleaseThreshold}
		}, lease.ReasonReleaseThreshold},
		{"keeper stop from the blind rule", func(in *SnapshotInputs) {
			in.Gate = released(true)
			in.Reconcile = ReconcileResult{Stopped: true, Release: lease.ReasonBlindThreshold}
		}, lease.ReasonBlindThreshold},
		{"linger window dropped the assertion", func(in *SnapshotInputs) {
			in.Live = nil
			in.Reconcile = ReconcileResult{Stopped: true, Release: lease.ReasonNoLeases}
		}, lease.ReasonNoLeases},
		{"a pause dropped the assertion", func(in *SnapshotInputs) {
			in.Reconcile = ReconcileResult{Stopped: true, Release: lease.ReasonPaused}
		}, lease.ReasonPaused},
		{"graceful shutdown", func(in *SnapshotInputs) {
			in.KeeperStop = true
		}, lease.ReasonKeeperStop},
		{"pair lost with nothing to explain it", func(in *SnapshotInputs) {
			in.Reconcile = ReconcileResult{PairLost: true}
		}, lease.ReasonUnknown},
		{"pair lost under a release gate with no exit record", func(in *SnapshotInputs) {
			in.Gate = released(false)
			in.Reconcile = ReconcileResult{PairLost: true}
		}, lease.ReasonReleaseThreshold},
		{"pair lost under the blind rule with no exit record", func(in *SnapshotInputs) {
			in.Gate = released(true)
			in.Reconcile = ReconcileResult{PairLost: true}
		}, lease.ReasonBlindThreshold},
		{"a lone caffeinate restarted under its anchor is not a release", func(in *SnapshotInputs) {
			in.Reconcile = heldResult()
			in.Reconcile.PairLost, in.Reconcile.Started = true, true
		}, ""},
		{"spawn_blocked is not a release", func(in *SnapshotInputs) {
			in.Reconcile = ReconcileResult{SpawnBlocked: true, BlockedBy: BlockNoSpawn, WantHold: true}
		}, ""},
		{"a stop whose reason is spawn_blocked is mapped to unknown", func(in *SnapshotInputs) {
			in.Reconcile = ReconcileResult{Stopped: true, Release: "spawn_blocked"}
		}, lease.ReasonUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := baseInputs()
			in.PrevLastRelease = prev
			tc.mod(&in)
			got := BuildSnapshot(in).LastRelease
			if tc.want == "" {
				if got == nil || *got != *prev {
					t.Fatalf("last_release = %+v, want the carried %+v", got, *prev)
				}
				if got == prev {
					t.Error("the carried last_release must be a copy")
				}
				return
			}
			if got == nil || got.Reason != tc.want {
				t.Fatalf("last_release = %+v, want reason %q", got, tc.want)
			}
			if !got.At.Equal(snapNow) {
				t.Errorf("last_release.at = %v, want the keeper's clock %v", got.At, snapNow)
			}
		})
	}
}

func TestSnapshotLastReleaseNilWithoutHistory(t *testing.T) {
	in := baseInputs()
	if got := BuildSnapshot(in).LastRelease; got != nil {
		t.Errorf("last_release = %+v, want nil", got)
	}
}

func TestSnapshotPause(t *testing.T) {
	future, past := snapNow.Add(time.Hour), snapNow.Add(-time.Minute)
	t.Run("active pause is reported verbatim", func(t *testing.T) {
		in := baseInputs()
		in.Pause = &lease.PauseFile{Until: &future, CreatedAt: snapNow, ByPID: 7}
		p := BuildSnapshot(in).Paused
		if p == nil || p.Until == nil || !p.Until.Equal(future) || p.ByPID != 7 {
			t.Fatalf("paused = %+v", p)
		}
	})
	t.Run("pause with no until never expires", func(t *testing.T) {
		in := baseInputs()
		in.Pause = &lease.PauseFile{CreatedAt: snapNow, ByPID: 7}
		if BuildSnapshot(in).Paused == nil {
			t.Fatal("an open-ended pause must be reported")
		}
	})
	t.Run("a past until is no pause", func(t *testing.T) {
		in := baseInputs()
		in.Pause = &lease.PauseFile{Until: &past, CreatedAt: snapNow, ByPID: 7}
		if p := BuildSnapshot(in).Paused; p != nil {
			t.Fatalf("paused = %+v, want nil", p)
		}
	})
}

func TestSnapshotUnexplainedTicksEvidence(t *testing.T) {
	future := snapNow.Add(time.Hour)
	linger := snapNow.Add(20 * time.Second)
	cases := []struct {
		name     string
		mod      func(*SnapshotInputs)
		want     int
		wantLing bool
	}{
		{"live leases, wanted, not held: counts", func(in *SnapshotInputs) {
			in.Reconcile = ReconcileResult{WantHold: true}
		}, 3, false},
		{"held: resets", func(in *SnapshotInputs) {}, 0, false},
		{"no live leases: resets", func(in *SnapshotInputs) {
			in.Live = nil
			in.Reconcile = ReconcileResult{WantHold: true}
		}, 0, false},
		{"explained by the no_spawn gate", func(in *SnapshotInputs) {
			in.Gate = policy.Status{Gate: policy.GateNoSpawn, Reading: policy.Reading{OnBattery: true, Percent: 25}}
			in.Reconcile = ReconcileResult{WantHold: true, SpawnBlocked: true, BlockedBy: BlockNoSpawn}
		}, 0, false},
		{"explained by the released gate", func(in *SnapshotInputs) {
			in.Gate = policy.Status{Gate: policy.GateReleased}
			in.Reconcile = ReconcileResult{}
		}, 0, false},
		{"explained by a pause", func(in *SnapshotInputs) {
			in.Pause = &lease.PauseFile{Until: &future}
			in.Reconcile = ReconcileResult{}
		}, 0, false},
		{"explained by the linger window", func(in *SnapshotInputs) {
			in.Reconcile = ReconcileResult{WantHold: true, LingerActive: true, LingerUntil: &linger}
		}, 0, true},
		{"churn backoff is not explained", func(in *SnapshotInputs) {
			in.Reconcile = ReconcileResult{WantHold: true, SpawnBlocked: true, BlockedBy: BlockChurn}
		}, 3, false},
		{"desired state is not to hold: resets", func(in *SnapshotInputs) {
			in.Reconcile = ReconcileResult{}
		}, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := baseInputs()
			in.PrevUnexplainedTicks = 2
			tc.mod(&in)
			s := BuildSnapshot(in)
			if s.UnexplainedTicks != tc.want {
				t.Errorf("unexplained_ticks = %d, want %d", s.UnexplainedTicks, tc.want)
			}
			if (s.LingerUntil != nil) != tc.wantLing {
				t.Errorf("linger_until = %v, want set=%v", s.LingerUntil, tc.wantLing)
			}
		})
	}
}

func TestSnapshotKeeperBlockUsesTheInjectedClock(t *testing.T) {
	in := baseInputs()
	in.HeartbeatMTime = snapNow.Add(-90*time.Second - 500*time.Millisecond)
	in.HeartbeatInterval = 2500 * time.Millisecond
	s := BuildSnapshot(in)
	if s.Keeper.HeartbeatAgeS != 90.5 {
		t.Errorf("heartbeat_age_s = %v, want 90.5", s.Keeper.HeartbeatAgeS)
	}
	if s.HeartbeatIntervalS != 3 {
		t.Errorf("heartbeat_interval_s = %d, want 3 (2.5s rounded up)", s.HeartbeatIntervalS)
	}
	if !s.WrittenAt.Equal(snapNow) {
		t.Errorf("written_at = %v, want %v", s.WrittenAt, snapNow)
	}
	if s.Keeper.ConfigError != nil {
		t.Errorf("config_error = %q, want nil", *s.Keeper.ConfigError)
	}

	in.HeartbeatMTime = snapNow.Add(time.Minute) // a clock step backwards
	if got := BuildSnapshot(in).Keeper.HeartbeatAgeS; got != 0 {
		t.Errorf("a future mtime gives age %v, want 0", got)
	}
	in.HeartbeatMTime = time.Time{}
	if got := BuildSnapshot(in).Keeper.HeartbeatAgeS; got != 0 {
		t.Errorf("no heartbeat file gives age %v, want 0", got)
	}
}

func TestSnapshotWriteIsAtomicAndPrivate(t *testing.T) {
	d := newDir(t)
	path := filepath.Join(d.Path(), lease.FileStatus)

	in := baseInputs()
	if err := WriteSnapshot(d, BuildSnapshot(in)); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("status.json mode = %o, want 600", fi.Mode().Perm())
	}

	// A second write replaces the file in place and leaves no temp file.
	in.KeeperPID = 9999
	if err := WriteSnapshot(d, BuildSnapshot(in)); err != nil {
		t.Fatal(err)
	}
	got, err := d.ReadStatus()
	if err != nil || got.Keeper.PID != 9999 {
		t.Fatalf("second write: %+v, %v", got, err)
	}
	entries, err := os.ReadDir(d.Path())
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("leftover temp file %s", e.Name())
		}
	}
}

// TestSnapshotReadersNeverSeeAPartialFile writes repeatedly while a reader
// parses the file; every read must be a complete, valid status.json.
func TestSnapshotReadersNeverSeeAPartialFile(t *testing.T) {
	d := newDir(t)
	in := baseInputs()
	for i := 0; i < 5; i++ {
		in.Live = append(in.Live, LiveLease{ID: "o.k" + string(rune('a'+i)), Owner: "o", Key: "k" + string(rune('a'+i)), Kind: lease.KindAcquire})
	}
	if err := WriteSnapshot(d, BuildSnapshot(in)); err != nil {
		t.Fatal(err)
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for pid := 1; ; pid++ {
			select {
			case <-stop:
				return
			default:
			}
			in.KeeperPID = pid
			if err := WriteSnapshot(d, BuildSnapshot(in)); err != nil {
				t.Errorf("WriteSnapshot: %v", err)
				return
			}
		}
	}()
	for i := 0; i < 300; i++ {
		if _, err := d.ReadStatus(); err != nil {
			close(stop)
			wg.Wait()
			t.Fatalf("read %d saw a bad status.json: %v", i, err)
		}
	}
	close(stop)
	wg.Wait()
}

func TestSnapshotWriteFailureIsReturned(t *testing.T) {
	d := newDir(t)
	if err := os.RemoveAll(d.Path()); err != nil {
		t.Fatal(err)
	}
	if err := WriteSnapshot(d, BuildSnapshot(baseInputs())); err == nil {
		t.Fatal("writing into a missing state directory must fail")
	}
}
