package groupsvc

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/registry"
)

// validConfig is a Config New accepts: the rig's, over a fresh store.
func validConfig(t *testing.T, spec registry.Spec) Config {
	t.Helper()
	cfg := newRig(t, spec, nil, nil).svc.cfg
	cfg.Errors = &fakeErrors{}
	return cfg
}

func TestNewRejectsWhatItCannotWire(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Error("New accepted an empty config")
	}
	base := validConfig(t, specAlpha)
	for name, mutate := range map[string]func(*Config){
		"no store":          func(c *Config) { c.Store = nil },
		"no scheduler":      func(c *Config) { c.Scheduler = nil },
		"no describer":      func(c *Config) { c.Describer = nil },
		"no alias table":    func(c *Config) { c.Aliases = nil },
		"no keep-alive":     func(c *Config) { c.KeepAlive = nil },
		"no error policy":   func(c *Config) { c.Errors = nil },
		"no id parser":      func(c *Config) { c.ParseID = nil },
		"no NoSuchEntity":   func(c *Config) { c.NoSuchEntity = nil },
		"no BadRef":         func(c *Config) { c.BadRef = nil },
		"no UnknownEntity":  func(c *Config) { c.UnknownEntity = nil },
		"no rules":          func(c *Config) { c.Rules.Groups = nil },
		"zero expiry":       func(c *Config) { c.Expiry = 0 },
		"negative expiry":   func(c *Config) { c.Expiry = -time.Hour },
		"no entity kind":    func(c *Config) { c.Spec.EntityKind = "" },
		"primary not in it": func(c *Config) { c.Spec.PrimaryGroup = "elsewhere" },
		"duplicate group":   func(c *Config) { c.Spec.Groups = append(append([]string(nil), c.Spec.Groups...), groupSide) },
	} {
		cfg := base
		mutate(&cfg)
		if _, err := New(cfg); err == nil {
			t.Errorf("New accepted a config with %s", name)
		}
	}
}

func TestNewAcceptsASpecWithoutViewsOrRolesAndDefaultsTheClock(t *testing.T) {
	cfg := validConfig(t, specBeta)
	cfg.Clock, cfg.Resolver, cfg.Identity, cfg.Governor = nil, nil, nil, nil
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if s.clock == nil || s.now().IsZero() {
		t.Error("New left the clock unset: it must default to the system clock")
	}
	if s.fetchers == nil || s.commits == nil {
		t.Error("New left the fetcher registry or the commit counts unset")
	}
	if id, _, err := s.Resolve(context.Background(), "ent-x"); err != nil || id != "ent-x" {
		t.Errorf("Resolve of a store id = (%q, %v)", id, err)
	}
}

func TestRegisterRejectsThePrimaryGroupAndANameOutsideTheSpec(t *testing.T) {
	r := newRig(t, specAlpha, nil, nil)
	for _, name := range []string{r.svc.cfg.Spec.PrimaryGroup, "bogus"} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("Register accepted a fetcher for %q", name)
				}
			}()
			r.svc.Register(fakeFetcher{name})
		}()
	}
	f := fakeFetcher{groupSide}
	r.svc.Register(f)
	if got, ok := r.svc.fetcher(groupSide); !ok || got != f {
		t.Error("Register did not install the fetcher")
	}
}

func TestOriginConstantsHaveTheirValues(t *testing.T) {
	for got, want := range map[string]string{
		OriginQuery: "query", OriginRefresh: "refresh", OriginWrite: "write", OriginRead: "read",
	} {
		if got != want {
			t.Errorf("origin constant = %q, want %q", got, want)
		}
	}
}

func TestInvalidateAndQueueNotesWriteAndInvalidateAndQueueAsNotesTheGivenOrigin(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, specAlpha, nil, nil)
	r.seedPrimary(ent1, "p")
	r.seedGroup(freshGroup(r, ent1, groupSide, "d"))
	r.seedGroup(freshGroup(r, ent1, groupPassive, "r"))
	h := &hintLog{}
	r.svc.SetOriginHints(h)

	if err := r.svc.InvalidateAndQueue(ctx, ent1, groupSide); err != nil {
		t.Fatal(err)
	}
	if err := r.svc.InvalidateAndQueueAs(ctx, ent1, groupPassive, OriginRefresh); err != nil {
		t.Fatal(err)
	}
	got := h.all()
	if len(got) != 2 || got[0] != [3]string{ent1, groupSide, "write"} || got[1] != [3]string{ent1, groupPassive, "refresh"} {
		t.Fatalf("notes = %v", got)
	}
	if !r.sched.Queued(ent1, groupSide) || !r.sched.Queued(ent1, groupPassive) {
		t.Error("InvalidateAndQueueAs must still invalidate and queue")
	}
}

func TestNoOriginHintIsNotedWithoutAReceiver(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, specAlpha, nil, nil)
	r.seedPrimary(ent1, "p")
	r.seedGroup(freshGroup(r, ent1, groupSide, "d"))
	h := &hintLog{}
	r.svc.SetOriginHints(h)
	r.svc.SetOriginHints(nil)
	if err := r.svc.InvalidateAndQueue(ctx, ent1, groupSide); err != nil {
		t.Fatal(err)
	}
	if got := h.all(); len(got) != 0 {
		t.Errorf("notes = %v, want none once the receiver is removed", got)
	}
}

// observerSpy records Revalidated calls.
type observerSpy struct {
	mu    sync.Mutex
	reval [][2]string
}

func (o *observerSpy) Served(string, time.Duration) {}
func (o *observerSpy) Revalidated(group, result string) {
	o.mu.Lock()
	o.reval = append(o.reval, [2]string{group, result})
	o.mu.Unlock()
}

func TestRevalidatedReachesTheObserverAndStatusesNameThemselves(t *testing.T) {
	r := newRig(t, specAlpha, nil, nil)
	r.svc.Revalidated(groupSide, "ignored") // no observer: nothing happens
	obs := &observerSpy{}
	r.svc.SetObserver(obs)
	r.svc.Revalidated(groupSide, "unchanged")
	if len(obs.reval) != 1 || obs.reval[0] != [2]string{groupSide, "unchanged"} {
		t.Errorf("Revalidated calls = %v", obs.reval)
	}
	r.svc.SetObserver(nil)
	r.svc.Revalidated(groupSide, "after removal")
	if len(obs.reval) != 1 {
		t.Errorf("Revalidated reached a removed observer: %v", obs.reval)
	}
	for s, want := range map[Status]string{Fresh: "fresh", Stale: "stale", Unavailable: "unavailable", 0: "unknown"} {
		if got := s.String(); got != want || strings.Contains(got, " ") {
			t.Errorf("%d.String() = %q, want %q", int(s), got, want)
		}
	}
}
