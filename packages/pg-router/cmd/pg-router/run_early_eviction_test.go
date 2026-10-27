package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router/internal/config"
	"github.com/phillipgreenii/pg-router/internal/core"
	"github.com/phillipgreenii/pg-router/internal/eventqueue"
	"github.com/phillipgreenii/pg-router/internal/orchestrator"
	"github.com/phillipgreenii/pg-router/internal/roles"
)

// acceptAllListener accepts every offer of the types it binds.
type acceptAllListener struct {
	id    string
	types map[string]bool
}

func (l *acceptAllListener) ID() string                      { return l.id }
func (l *acceptAllListener) Matches(e eventqueue.Event) bool { return l.types[e.Type] }
func (l *acceptAllListener) Offer(eventqueue.Offering) eventqueue.OfferResult {
	return eventqueue.OfferResult{Accepted: true}
}

// bootCore threads cfg.EarlyEvictionTypes ([pool].early_eviction_types, bead
// pg2-u0p2d, DEC-EVENT-2) into the queue: an accepted event of a listed type
// (escalated.pg2) leaves the queue and the depth gauge source at ACCEPT time,
// with an evict record of reason all-accepted, although its expiresAt is an hour
// away; an accepted event of a type that is not listed keeps its retention.
func TestBootCore_EarlyEvictionTypesEvictsAtAcceptOnlyForListedType(t *testing.T) {
	dir := shortDir(t)
	cfg := config.Config{
		LogDir:             dir,
		EarlyEvictionTypes: []string{"escalated.pg2"},
		Roles:              roles.RoleSet{{Name: "r1", Enabled: true, Binds: []string{"unrelated"}}}, // the test's own listener below is the only binder of the two types
	}
	o := &orchestrator.Orchestrator{Cfg: cfg}
	svc, q, _, storeClose, err := bootCore(context.Background(), cfg, o, cfg.Roles, runExclusions{}, core.RunModeDrainAndExit)
	if err != nil {
		t.Fatalf("bootCore: %v", err)
	}
	defer func() { _ = storeClose() }()
	defer func() { _ = svc.Close() }()

	q.Register(&acceptAllListener{id: "h", types: map[string]bool{"escalated.pg2": true, "other": true}})
	future := time.Now().Add(time.Hour)
	for _, e := range []eventqueue.Event{
		{ID: "esc-1", Type: "escalated.pg2", ExpiresAt: future},
		{ID: "oth-1", Type: "other", ExpiresAt: future},
	} {
		if _, err := q.Enqueue(e); err != nil {
			t.Fatalf("enqueue %s: %v", e.ID, err)
		}
	}
	for range 3 {
		q.Dispatch()
	}

	depth := q.DepthByType()
	if depth["escalated.pg2"] != 0 {
		t.Fatalf("depth[escalated.pg2] = %d after accept (expiresAt an hour out), want 0: %v", depth["escalated.pg2"], depth)
	}
	if depth["other"] != 1 {
		t.Fatalf("depth[other] = %d, want 1 (type not listed keeps retention): %v", depth["other"], depth)
	}

	log, err := os.ReadFile(filepath.Join(dir, "queue.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var evictedEsc, evictedOther bool
	for _, line := range strings.Split(string(log), "\n") {
		if !strings.Contains(line, `"op":"evict"`) {
			continue
		}
		if strings.Contains(line, `"eventId":"esc-1"`) && strings.Contains(line, `"reason":"`+eventqueue.EvictReasonAllAccepted+`"`) {
			evictedEsc = true
		}
		if strings.Contains(line, `"eventId":"oth-1"`) {
			evictedOther = true
		}
	}
	if !evictedEsc {
		t.Fatalf("queue.jsonl has no all-accepted evict record for esc-1:\n%s", log)
	}
	if evictedOther {
		t.Fatalf("queue.jsonl evicted oth-1 although its type is not listed:\n%s", log)
	}
}
