package core

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/pr-pool/conformance"
)

func fixedClock(t time.Time) func() time.Time { return func() time.Time { return t } }

// A fresh registration starts in `starting` / `healthy` — the core must not route
// to it before it says it is started (INV-INTF-1).
func TestRegistry_RegisterStartsUnroutable(t *testing.T) {
	at := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	r := NewRegistry(fixedClock(at))

	reg, err := r.Register("src-beads", KindSource, "pr-pool ingest-event --socket s --token t")
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if reg.State != conformance.Starting {
		t.Fatalf("state = %s, want starting", reg.State)
	}
	if reg.Self != SelfHealthy {
		t.Fatalf("self = %s, want healthy", reg.Self)
	}
	if !reg.RegisteredAt.Equal(at) || !reg.UpdatedAt.Equal(at) {
		t.Fatalf("timestamps = %v/%v, want the injected clock %v", reg.RegisteredAt, reg.UpdatedAt, at)
	}
	if r.Available("src-beads") {
		t.Fatal("Available = true while starting; the core must not route yet")
	}
	if r.Len() != 1 {
		t.Fatalf("Len = %d, want 1", r.Len())
	}
}

func TestRegistry_RejectsInvalidRegistrations(t *testing.T) {
	r := NewRegistry(nil)
	if _, err := r.Register("", KindSource, ""); !errors.Is(err, ErrInvalidRegistration) {
		t.Fatalf("empty id err = %v, want ErrInvalidRegistration", err)
	}
	if _, err := r.Register("x", Kind("wat"), ""); !errors.Is(err, ErrInvalidRegistration) {
		t.Fatalf("unknown kind err = %v, want ErrInvalidRegistration", err)
	}
}

// A participant that crashed and came back re-registers under the same id; the
// fresh registration must replace the stale one rather than be refused.
func TestRegistry_ReRegisterReplaces(t *testing.T) {
	r := NewRegistry(nil)
	if _, err := r.Register("h1", KindHandler, "cb-old"); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := r.SetLifecycle("h1", conformance.Started); err != nil {
		t.Fatalf("SetLifecycle: %v", err)
	}
	if _, err := r.Register("h1", KindHandler, "cb-new"); err != nil {
		t.Fatalf("re-Register: %v", err)
	}
	got, ok := r.Get("h1")
	if !ok {
		t.Fatal("registration vanished")
	}
	if got.State != conformance.Starting {
		t.Fatalf("state = %s, want the re-registration to reset to starting", got.State)
	}
	if got.Callback != "cb-new" {
		t.Fatalf("callback = %q, want the new one", got.Callback)
	}
	if r.Len() != 1 {
		t.Fatalf("Len = %d, want 1 (replace, not append)", r.Len())
	}
}

func TestRegistry_LifecycleAndAvailability(t *testing.T) {
	r := NewRegistry(nil)
	if _, err := r.Register("h1", KindHandler, ""); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := r.SetLifecycle("h1", conformance.Started); err != nil {
		t.Fatalf("SetLifecycle: %v", err)
	}
	if !r.Available("h1") {
		t.Fatal("Available = false for a started, healthy participant")
	}
	// `degraded` is a quality warning, not a refusal — still routable.
	if err := r.SetSelfStatus("h1", SelfDegraded); err != nil {
		t.Fatalf("SetSelfStatus: %v", err)
	}
	if !r.Available("h1") {
		t.Fatal("Available = false for a degraded participant; degraded is still routable")
	}
	// `unavailable` is a PRE-ACCEPT decline: the core re-offers within ttl.
	if err := r.SetSelfStatus("h1", SelfUnavailable); err != nil {
		t.Fatalf("SetSelfStatus: %v", err)
	}
	if r.Available("h1") {
		t.Fatal("Available = true for an unavailable participant (INV-FAIL-1 pre-accept decline)")
	}
	// Leaving `started` also makes it unroutable.
	if err := r.SetSelfStatus("h1", SelfHealthy); err != nil {
		t.Fatalf("SetSelfStatus: %v", err)
	}
	if err := r.SetLifecycle("h1", conformance.Stopping); err != nil {
		t.Fatalf("SetLifecycle: %v", err)
	}
	if r.Available("h1") {
		t.Fatal("Available = true while stopping (INV-INTF-1)")
	}
}

func TestRegistry_UnknownParticipant(t *testing.T) {
	r := NewRegistry(nil)
	if err := r.SetLifecycle("nope", conformance.Started); !errors.Is(err, ErrUnknownParticipant) {
		t.Fatalf("SetLifecycle err = %v, want ErrUnknownParticipant", err)
	}
	if err := r.SetSelfStatus("nope", SelfHealthy); !errors.Is(err, ErrUnknownParticipant) {
		t.Fatalf("SetSelfStatus err = %v, want ErrUnknownParticipant", err)
	}
	if _, ok := r.Get("nope"); ok {
		t.Fatal("Get returned an entry for an unregistered id")
	}
	if r.Available("nope") {
		t.Fatal("Available = true for an unregistered id")
	}
}

func TestRegistry_SetSelfStatusRejectsUnknownValue(t *testing.T) {
	r := NewRegistry(nil)
	if _, err := r.Register("h1", KindHandler, ""); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := r.SetSelfStatus("h1", SelfStatus("mostly-fine")); !errors.Is(err, ErrInvalidRegistration) {
		t.Fatalf("err = %v, want ErrInvalidRegistration (report, do not guess)", err)
	}
}

// Deregistering is idempotent: `stopped` and `crashing` can both reach it.
func TestRegistry_Deregister(t *testing.T) {
	r := NewRegistry(nil)
	if _, err := r.Register("s1", KindSource, ""); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if !r.Deregister("s1") {
		t.Fatal("Deregister = false for a registered participant")
	}
	if r.Deregister("s1") {
		t.Fatal("second Deregister = true, want a no-op false")
	}
	if r.Len() != 0 {
		t.Fatalf("Len = %d, want 0", r.Len())
	}
}

// List is sorted so any view built on it is deterministic.
func TestRegistry_ListIsSorted(t *testing.T) {
	r := NewRegistry(nil)
	for _, id := range []string{"zeta", "alpha", "mid"} {
		if _, err := r.Register(id, KindSource, ""); err != nil {
			t.Fatalf("Register %s: %v", id, err)
		}
	}
	got := r.List()
	ids := make([]string, len(got))
	for i, g := range got {
		ids[i] = g.ID
	}
	if strings.Join(ids, ",") != "alpha,mid,zeta" {
		t.Fatalf("List order = %v, want sorted by id", ids)
	}
}

// The registry is touched from many goroutines (the accept loop), so it must be
// race-free under -race.
func TestRegistry_ConcurrentUse(t *testing.T) {
	r := NewRegistry(nil)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := string(rune('a' + i%5))
			if _, err := r.Register(id, KindSource, ""); err != nil {
				t.Errorf("Register: %v", err)
				return
			}
			_ = r.SetLifecycle(id, conformance.Started)
			_ = r.SetSelfStatus(id, SelfDegraded)
			_ = r.Available(id)
			_ = r.List()
			_ = r.Len()
		}(i)
	}
	wg.Wait()
}

func TestParseSelfStatusAndKind(t *testing.T) {
	for _, s := range []string{"healthy", "degraded", "unavailable"} {
		if got, err := ParseSelfStatus(s); err != nil || string(got) != s {
			t.Fatalf("ParseSelfStatus(%q) = %q, %v", s, got, err)
		}
	}
	if _, err := ParseSelfStatus("busy"); err == nil {
		t.Fatal("ParseSelfStatus accepted an undeclared value")
	}
	for _, s := range []string{"source", "handler", "monitor", "storage"} {
		if got, err := ParseKind(s); err != nil || string(got) != s {
			t.Fatalf("ParseKind(%q) = %q, %v", s, got, err)
		}
	}
	if _, err := ParseKind("operator"); err == nil {
		t.Fatal("ParseKind accepted an undeclared kind")
	}
}

// Registering through the SERVICE hands a source its ingest-event callback with
// the socket and token baked in; a handler gets none, because session-status is
// dropped and acceptance arrives in the dispatch reply instead.
func TestService_RegisterHandsOutTheCallback(t *testing.T) {
	svc := &Service{
		state:   conformance.Started,
		reg:     NewRegistry(nil),
		command: "pr-pool",
		ref:     Ref{Socket: "/s/core.sock", Token: "tok"},
	}
	src, err := svc.Register("src-1", KindSource)
	if err != nil {
		t.Fatalf("Register source: %v", err)
	}
	want := `pr-pool ingest-event --socket '/s/core.sock' --token 'tok'`
	if src.Callback != want {
		t.Fatalf("source callback = %q, want %q", src.Callback, want)
	}
	for _, kind := range []Kind{KindHandler, KindMonitor, KindStorage} {
		reg, err := svc.Register("p-"+string(kind), kind)
		if err != nil {
			t.Fatalf("Register %s: %v", kind, err)
		}
		if reg.Callback != "" {
			t.Fatalf("%s callback = %q, want empty (no callback target)", kind, reg.Callback)
		}
	}
	if svc.Registry().Len() != 4 {
		t.Fatalf("registry len = %d, want 4", svc.Registry().Len())
	}
}
