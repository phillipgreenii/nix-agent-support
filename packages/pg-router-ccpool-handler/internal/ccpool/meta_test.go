package ccpool

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestDispatchMeta_buildsPgrouterNamespacedMap(t *testing.T) {
	got := DispatchMeta("zr-1", "worker", time.Time{}, 0, "")
	want := map[string]string{
		MetaKeyBead: "zr-1",
		MetaKeyRole: "worker",
		MetaKeyPool: PoolName,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DispatchMeta = %v, want %v", got, want)
	}
}

// The initial lease covers the whole `ccpool new` wait plus one TTL, and the
// launch time is stamped alongside it (INV-CCH-18).
func TestDispatchMeta_stampsLaunchTimeAndInitialLease(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	got := DispatchMeta("zr-1", "worker", now, 2*time.Minute, "")
	if got[MetaKeyLaunchedAt] != "2026-10-06T12:00:00Z" {
		t.Errorf("launched_at = %q", got[MetaKeyLaunchedAt])
	}
	wantLease := now.Add(EnsureTimeout + 2*time.Minute).Format(time.RFC3339)
	if got[MetaKeyLeaseUntil] != wantLease {
		t.Errorf("lease_until = %q, want %q", got[MetaKeyLeaseUntil], wantLease)
	}
	// 12m ensure timeout + 2m TTL after launch.
	if got[MetaKeyLeaseUntil] != "2026-10-06T12:14:00Z" {
		t.Errorf("lease_until = %q, want 12:14:00Z", got[MetaKeyLeaseUntil])
	}
}

func TestDispatchMeta_nonPositiveTTLOmitsLease(t *testing.T) {
	got := DispatchMeta("zr-1", "worker", time.Now(), 0, "")
	if _, ok := got[MetaKeyLeaseUntil]; ok {
		t.Errorf("a zero TTL must omit the lease: %v", got)
	}
	if _, ok := got[MetaKeyLaunchedAt]; ok {
		t.Errorf("a zero TTL must omit launched_at too: %v", got)
	}
}

// The dispatching event's id is stamped so a later dispatch can tell a
// redelivery of the same event from a re-dispatch (pg2-uprw5); an empty id
// omits the key.
func TestDispatchMeta_stampsEventID(t *testing.T) {
	got := DispatchMeta("zr-1", "worker", time.Time{}, 0, "evt-7")
	if got[MetaKeyEventID] != "evt-7" {
		t.Errorf("event_id = %q, want evt-7", got[MetaKeyEventID])
	}
	if _, ok := DispatchMeta("zr-1", "worker", time.Time{}, 0, "")[MetaKeyEventID]; ok {
		t.Errorf("an empty event id must omit the key")
	}
}

func TestSession_LeaseUntilAndLaunchedAt(t *testing.T) {
	s := Session{Meta: map[string]string{
		MetaKeyLeaseUntil: "2026-10-06T12:14:00Z",
		MetaKeyLaunchedAt: "2026-10-06T12:00:00Z",
	}}
	if got, ok := s.LeaseUntil(); !ok || got.Format(time.RFC3339) != "2026-10-06T12:14:00Z" {
		t.Errorf("LeaseUntil = %v, %v", got, ok)
	}
	if got, ok := s.LaunchedAt(); !ok || got.Format(time.RFC3339) != "2026-10-06T12:00:00Z" {
		t.Errorf("LaunchedAt = %v, %v", got, ok)
	}
	for _, bad := range []Session{{}, {Meta: map[string]string{MetaKeyLeaseUntil: "not-a-time", MetaKeyLaunchedAt: ""}}} {
		if _, ok := bad.LeaseUntil(); ok {
			t.Errorf("LeaseUntil on %v must be absent", bad.Meta)
		}
		if _, ok := bad.LaunchedAt(); ok {
			t.Errorf("LaunchedAt on %v must be absent", bad.Meta)
		}
	}
}

// Neither lease key may ever be passed as a ccpool --label: a per-poll value
// would churn telemetry (bead pg2-g2u9m).
func TestEnsure_neverLabelsLeaseKeys(t *testing.T) {
	cli, got, _ := newSpy()
	meta := DispatchMeta("zr-1", "worker", time.Now(), 2*time.Minute, "")
	if err := cli.Ensure(t.Context(), "s", "", "/r", nil, meta); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	joined := strings.Join((*got)[0], " ")
	for _, k := range []string{MetaKeyLeaseUntil, MetaKeyLaunchedAt} {
		if !strings.Contains(joined, "--meta "+k+"=") {
			t.Errorf("argv must carry --meta %s=...; got %v", k, (*got)[0])
		}
		if strings.Contains(joined, "--label "+k) {
			t.Errorf("%s must never be a --label; got %v", k, (*got)[0])
		}
	}
}
