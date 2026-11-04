package wire

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"testing"
)

func TestExitForFollowsTheDocumentedMapping(t *testing.T) {
	for name, want := range map[string]int{
		ErrBadRequest: 2, ErrUsage: 2, ErrUnavailable: 3, ErrDaemonOffline: 3, ErrNotFound: 4,
		ErrBusy: 5, ErrBehind: 5, ErrWaitTimeout: 6, ErrQueryTimeout: 6, ErrCursorExpired: 7,
		ErrRemovalDisabled: 8, ErrTargetExists: 8, ErrHeldByLive: 8,
		ErrInternal: 1, "something_new": 1,
	} {
		if got := ExitFor(name); got != want {
			t.Errorf("ExitFor(%s) = %d, want %d", name, got, want)
		}
	}
}

func TestHTTPStatusIsDistinctPerFamily(t *testing.T) {
	if HTTPStatus(ErrNotFound) != 404 || HTTPStatus(ErrBadRequest) != 400 || HTTPStatus(ErrInternal) != 500 ||
		HTTPStatus(ErrUnavailable) != 503 || HTTPStatus(ErrBusy) != 429 || HTTPStatus(ErrCursorExpired) != 410 ||
		HTTPStatus(ErrRemovalDisabled) != 409 || HTTPStatus(ErrQueryTimeout) != 504 {
		t.Fatal("unexpected status mapping")
	}
}

func TestFromErrMapsADeadlineToTheRetryableTimeout(t *testing.T) {
	if e := FromErr(fmt.Errorf("scan: %w", context.DeadlineExceeded)); e.Name != ErrQueryTimeout {
		t.Fatalf("a wrapped deadline mapped to %q, want %q", e.Name, ErrQueryTimeout)
	}
	if e := FromErr(fmt.Errorf("disk on fire")); e.Name != ErrInternal {
		t.Fatalf("another error mapped to %q, want internal", e.Name)
	}
}

var updateWireGolden = flag.Bool("update", false, "rewrite testdata/status.golden.json")

// TestStatusJSONGolden pins the status --json shape with every field set. A
// change to it is reviewed as additive only: no key renamed, retyped or removed.
func TestStatusJSONGolden(t *testing.T) {
	crashed, suspected := 1, 2
	dur, age := int64(412), int64(3)
	exc, fullAge := 5, 90.0
	st := Status{
		Healthy: true, Verdict: "ok", CopyLagS: 1, ParseLagS: 2, Held: 0, FreeBytes: 1000,
		StageCounts:    map[string]int{"live": 2, "quiet": 1, "held": 0, "settled": 5},
		ParserVersions: map[string]int{"1": 8}, SchemaVersion: 3, ProtocolVer: 1,
		Short: "ok", CaughtUp: true, Degraded: true, Backfill: &Backfill{Running: true, Total: 10, Copied: 5, Parsed: 4, EtaS: 30},
		Socket: "/example/r.sock", StatusAgeS: 4, CrashedRecent: &crashed, SuspectedRecent: &suspected,
		LaunchAgent: "loaded", LogPath: "/example/log", StateBytes: 77,
		LastTick:       &LastTick{DurationMS: 412, Examined: 8, AgeS: 3},
		StatExceptions: &exc, LastFullStatAgeS: &fullAge,
		Loops: map[string]LoopStatus{
			"settle_tick":   {LastDurationMS: &dur, LastOutcome: "ok", AgeS: &age, InFlightS: 0},
			"watcher_sweep": {InFlightS: 700, Stalled: true},
		},
	}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	b = append(b, '\n')
	const golden = "testdata/status.golden.json"
	if *updateWireGolden {
		if err := os.WriteFile(golden, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != string(want) {
		t.Fatalf("status JSON differs from %s (rerun with -update and review the diff as additive only):\n%s", golden, b)
	}
}

func TestFromErrMapsCancelledApartFromTimeout(t *testing.T) {
	if e := FromErr(fmt.Errorf("scan: %w", context.Canceled)); e.Name != ErrCancelled {
		t.Fatalf("a wrapped cancel mapped to %q, want %q", e.Name, ErrCancelled)
	}
	if HTTPStatus(ErrCancelled) != 499 {
		t.Fatalf("status %d", HTTPStatus(ErrCancelled))
	}
	if ExitFor(ErrCancelled) == ExitTimeout {
		t.Fatal("cancelled must not exit like a timeout")
	}
}
