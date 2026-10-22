package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router/conformance"
	"github.com/phillipgreenii/pg-router/internal/config"
	"github.com/phillipgreenii/pg-router/internal/core"
	"github.com/phillipgreenii/pg-router/internal/eventqueue"
	"github.com/phillipgreenii/pg-router/internal/orchestrator"
	"github.com/phillipgreenii/pg-router/internal/roles"
	"github.com/phillipgreenii/pg-router/internal/wireclient"
)

// The busy state (bead pg2-8ij5o, operator ruling 30 of docket pg2-12gr4) reaches a
// real scrape and a real `status` call of a running core. Each role's handler is a real
// subprocess behind the real wire client that exits 9 with a decline body, so the body's
// kind (and the reason beside it) is decoded by the production path. The reasons are
// deliberately misleading: "busy-role" declines with kind neutral and "calm-role" with a
// reason of "busy" and kind neutral must NOT read busy, while a decline of kind busy
// whose reason says "neutral" must. GOAL-MIN-1: kind decides, never the reason.
func TestBusy_ReachesScrapeAndStatusFromTheDeclineKindAlone(t *testing.T) {
	var ln net.Listener
	mp, shutdown, err := startMetricsServer("127.0.0.1:0", func(l net.Listener) { ln = l })
	if err != nil {
		t.Fatalf("startMetricsServer: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = shutdown(ctx)
	})

	cfg := config.Config{
		LogDir:        shortDir(t),
		MeterProvider: mp,
		Roles: roles.RoleSet{
			{Name: "full", Enabled: true, Binds: []string{"t1"}},
			{Name: "neutral-role", Enabled: true, Binds: []string{"t2"}},
			{Name: "fault-role", Enabled: true, Binds: []string{"t3"}},
			{Name: "idle", Enabled: true, Binds: []string{"t4"}},
		},
	}
	declineBody := map[string]string{
		"full":         `{"schemaVersion":"1","reason":"neutral","kind":"busy"}`,
		"neutral-role": `{"schemaVersion":"1","reason":"busy","kind":"neutral"}`,
		"fault-role":   `{"schemaVersion":"1","reason":"busy","kind":"fault"}`,
	}
	o := &orchestrator.Orchestrator{Cfg: cfg, Handler: wireclient.New(func(r roles.Role, _ string) ([]string, error) {
		return slices.Clone([]string{"/bin/sh", "-c", fmt.Sprintf("printf '%%s' '%s'; exit 9", declineBody[r.Name])}), nil
	})}
	ctx := context.Background()
	svc, q, _, storeClose, err := bootCore(ctx, cfg, o, cfg.Roles, runExclusions{}, core.RunModeDrainAndExit)
	if err != nil {
		t.Fatalf("bootCore: %v", err)
	}
	defer func() { _ = storeClose() }()
	defer func() { _ = svc.Close() }()
	acceptCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- svc.Accept(acceptCtx) }()
	defer func() { cancel(); <-done }()

	for i, typ := range []string{"t1", "t2", "t3"} {
		if _, err := q.Enqueue(eventqueue.Event{ID: fmt.Sprintf("e%d", i), Type: typ, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
			t.Fatalf("Enqueue: %v", err)
		}
	}
	q.Dispatch() // one offer to each of the three roles, each declined

	if err := forceFlush(ctx, mp); err != nil {
		t.Fatal(err)
	}
	body := scrape(t, fmt.Sprintf("http://%s/metrics", ln.Addr().String()))
	for role, want := range map[string]float64{"full": 1, "neutral-role": 0, "fault-role": 0, "idle": 0} {
		got := seriesValue(body, "pg_router_busy", map[string]string{"kind": "role", "name": role})
		if len(got) != 1 || got[0] != want {
			t.Errorf("pg_router_busy{kind=role,name=%q} = %v, want %v:\n%s", role, got, want, body)
		}
	}

	var out, errOut strings.Builder
	if code := status(&out, &errOut, true, svc.Ref()); code != conformance.ExitOK {
		t.Fatalf("status --json exit = %d; stderr=%s", code, errOut.String())
	}
	var reply struct {
		Busy struct {
			Roles   []string `json:"roles"`
			Sources []string `json:"sources"`
		} `json:"busy"`
		Listeners []struct {
			Role string `json:"role"`
			Busy bool   `json:"busy"`
		} `json:"listeners"`
	}
	if err := json.Unmarshal([]byte(out.String()), &reply); err != nil {
		t.Fatalf("status --json: %v", err)
	}
	if !slices.Equal(reply.Busy.Roles, []string{"full"}) || len(reply.Busy.Sources) != 0 {
		t.Errorf("status --json busy = %+v, want roles [full] and no sources", reply.Busy)
	}
	for _, l := range reply.Listeners {
		if l.Busy != (l.Role == "full") {
			t.Errorf("status --json listener %q busy = %v, want %v", l.Role, l.Busy, l.Role == "full")
		}
	}

	out.Reset()
	if code := status(&out, &errOut, false, svc.Ref()); code != conformance.ExitOK {
		t.Fatalf("status exit = %d; stderr=%s", code, errOut.String())
	}
	text := out.String()
	if !strings.Contains(text, "BUSY:\n  role full: 1 busy declines since last progress\n") {
		t.Errorf("status text lacks the busy role under BUSY:\n%s", text)
	}
	for _, other := range []string{"role neutral-role", "role fault-role", "role idle"} {
		if strings.Contains(text, other+":") {
			t.Errorf("status text lists %q as busy:\n%s", other, text)
		}
	}
}
