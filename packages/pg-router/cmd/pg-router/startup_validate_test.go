package main

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router/conformance"
	"github.com/phillipgreenii/pg-router/internal/config"
	"github.com/phillipgreenii/pg-router/internal/core"
	"github.com/phillipgreenii/pg-router/internal/eventqueue"
	"github.com/phillipgreenii/pg-router/internal/metrics"
	"github.com/phillipgreenii/pg-router/internal/orchestrator"
	"github.com/phillipgreenii/pg-router/internal/roles"
	"github.com/phillipgreenii/pg-router/internal/tui"
	"github.com/phillipgreenii/pg-router/internal/wireclient"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// validatingHandler is fakeHandlerClient plus the optional wireclient.Validator,
// scripted per role.
type validatingHandler struct {
	*fakeHandlerClient
	validate func(ctx context.Context, role roles.Role) (wireclient.ValidateResult, error)
}

func (h validatingHandler) Validate(ctx context.Context, role roles.Role) (wireclient.ValidateResult, error) {
	return h.validate(ctx, role)
}

// startupValidateFixture boots a core with roles "bad" (t1) and "good" (t2), runs the
// startup validate step and postStartupAll in the order both startup paths do, and
// returns the pieces a test asserts on.
type startupValidateFixture struct {
	svc    *core.Service
	q      *eventqueue.Queue
	fh     *fakeHandlerClient
	reader sdkmetric.Reader
}

func bootWithValidate(t *testing.T, validate func(context.Context, roles.Role) (wireclient.ValidateResult, error)) startupValidateFixture {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	cfg := config.Config{
		LogDir:        shortDir(t),
		MeterProvider: sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)),
		Roles: roles.RoleSet{
			{Name: "bad", Enabled: true, Binds: []string{"t1"}},
			{Name: "good", Enabled: true, Binds: []string{"t2"}},
			{Name: "off", Enabled: false, Binds: []string{"t3"}},
		},
	}
	fh := &fakeHandlerClient{}
	o := &orchestrator.Orchestrator{Cfg: cfg, Handler: validatingHandler{fakeHandlerClient: fh, validate: validate}}
	svc, q, _, storeClose, err := bootCore(context.Background(), cfg, o, cfg.Roles, runExclusions{}, core.RunModeDrainAndExit)
	if err != nil {
		t.Fatalf("bootCore: %v", err)
	}
	t.Cleanup(func() { _ = storeClose() })
	t.Cleanup(func() { _ = svc.Close() })
	validateRolesAtStartup(context.Background(), o, cfg)
	postStartupAll(context.Background(), o, cfg)
	return startupValidateFixture{svc: svc, q: q, fh: fh, reader: reader}
}

func failsRole(bad string) func(context.Context, roles.Role) (wireclient.ValidateResult, error) {
	return func(_ context.Context, r roles.Role) (wireclient.ValidateResult, error) {
		if r.Name == bad {
			return wireclient.ValidateResult{Supported: true, Problems: []string{"pool dir missing", "unknown key"}, Warnings: []string{"key TOKEN looks secret"}}, nil
		}
		return wireclient.ValidateResult{Supported: true}, nil
	}
}

// TestStartupValidate_FailedValidateLeavesRoleRegisteredAndDeclining is design
// section 10's core test (V-3): a failed startup validate leaves the role
// registered and declining with DeclineUnavailable, unexpired events kept,
// postStartup run, and the role shown in the metric and status.
func TestStartupValidate_FailedValidateLeavesRoleRegisteredAndDeclining(t *testing.T) {
	f := bootWithValidate(t, failsRole("bad"))
	reg := f.svc.Registry()

	got, registered := reg.Get("bad")
	if !registered {
		t.Fatal("a role whose validation failed must stay registered")
	}
	if got.Self != core.SelfUnavailable || got.UnavailableReason != "pool dir missing; unknown key" {
		t.Fatalf("bad: Self=%q reason=%q, want unavailable with the problems joined", got.Self, got.UnavailableReason)
	}
	if reg.Available("bad") || !reg.Available("good") {
		t.Fatalf("Available(bad)=%v Available(good)=%v, want false/true", reg.Available("bad"), reg.Available("good"))
	}

	// postStartup still runs for the unavailable role (sessions outlive restarts).
	if got := f.fh.postStartupCalls(); !reflect.DeepEqual(got, []string{"bad", "good"}) {
		t.Fatalf("postStartup calls = %v, want [bad good]", got)
	}

	// Offer declines the unavailable role; its unexpired event is kept, the good role's is delivered.
	future := time.Now().Add(time.Hour)
	for _, typ := range []string{"t1", "t2"} {
		if _, err := f.q.Enqueue(eventqueue.Event{ID: "ev-" + typ, Type: typ, ExpiresAt: future, Payload: map[string]any{"id": typ}}); err != nil {
			t.Fatalf("enqueue %s: %v", typ, err)
		}
	}
	f.q.Dispatch()
	if got := f.fh.dispatchedCalls(); !reflect.DeepEqual(got, []string{"good"}) {
		t.Fatalf("dispatched = %v, want only [good]: the unavailable role must be declined", got)
	}
	if depth := f.q.DepthByType()["t1"]; depth != 1 {
		t.Fatalf("depth(t1) = %d, want 1: the declined role's unexpired event is kept", depth)
	}

	// The metric: 1 for the available role, 0 for the unavailable one, none for the disabled role.
	var rm metricdata.ResourceMetrics
	if err := f.reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect: %v", err)
	}
	if v, ok := int64GaugePoint(rm, metrics.MetricRoleAvailable, "role", "bad"); !ok || v != 0 {
		t.Errorf("%s{role=bad} = %d (present %v), want 0", metrics.MetricRoleAvailable, v, ok)
	}
	if v, ok := int64GaugePoint(rm, metrics.MetricRoleAvailable, "role", "good"); !ok || v != 1 {
		t.Errorf("%s{role=good} = %d (present %v), want 1", metrics.MetricRoleAvailable, v, ok)
	}
	if _, ok := int64GaugePoint(rm, metrics.MetricRoleAvailable, "role", "off"); ok {
		t.Errorf("%s has a series for the disabled role", metrics.MetricRoleAvailable)
	}

	// status (the wire reply, decoded by the TUI's own types).
	reply := statusReplyOf(t, f.svc)
	byRole := map[string]tui.Listener{}
	for _, l := range reply.Listeners {
		byRole[l.Role] = l
	}
	bad := byRole["bad"]
	if bad.SelfReportState != "unavailable" || bad.UnavailableReason != "pool dir missing; unknown key" {
		t.Errorf("status listener bad: self=%q reason=%q", bad.SelfReportState, bad.UnavailableReason)
	}
	if bad.DeclinedByReason["unavailable"] != 1 {
		t.Errorf("status listener bad declinedByReason = %v, want unavailable=1", bad.DeclinedByReason)
	}
	if byRole["good"].UnavailableReason != "" || byRole["good"].SelfReportState != "healthy" {
		t.Errorf("status listener good: self=%q reason=%q, want healthy and no reason", byRole["good"].SelfReportState, byRole["good"].UnavailableReason)
	}
}

// statusReplyOf serves the core's status verb in-process and decodes it.
func statusReplyOf(t *testing.T, svc *core.Service) tui.StatusReply {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	accepted := make(chan error, 1)
	go func() { accepted <- svc.Accept(ctx) }()
	t.Cleanup(func() { cancel(); <-accepted })
	deadline := time.Now().Add(2 * time.Second)
	for svc.State() != conformance.Started && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	var out strings.Builder
	if code := svc.Serve(core.SubcommandStatus, strings.NewReader(`{"schemaVersion":"1"}`), &out); code != conformance.ExitOK {
		t.Fatalf("status exit = %d: %s", code, out.String())
	}
	if err := conformance.CheckBytes(core.StatusReplySchema, []byte(out.String())); err != nil {
		t.Fatalf("status reply violates cli.status-reply: %v\n%s", err, out.String())
	}
	var reply tui.StatusReply
	if err := json.Unmarshal([]byte(out.String()), &reply); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return reply
}

// A handler without validate (usage exit), a validate that could not run, and a
// valid role all leave the role available; warnings alone never fail it.
func TestStartupValidate_NonVerdictsLeaveTheRoleAvailable(t *testing.T) {
	cases := map[string]func(context.Context, roles.Role) (wireclient.ValidateResult, error){
		"not supported": func(context.Context, roles.Role) (wireclient.ValidateResult, error) {
			return wireclient.ValidateResult{Supported: false}, nil
		},
		"transport error": func(context.Context, roles.Role) (wireclient.ValidateResult, error) {
			return wireclient.ValidateResult{}, errors.New("exited 1")
		},
		"warnings only": func(context.Context, roles.Role) (wireclient.ValidateResult, error) {
			return wireclient.ValidateResult{Supported: true, Warnings: []string{"w"}}, nil
		},
	}
	for name, v := range cases {
		t.Run(name, func(t *testing.T) {
			f := bootWithValidate(t, v)
			if !f.svc.Registry().Available("bad") || !f.svc.Registry().Available("good") {
				t.Fatal("every role must stay available")
			}
		})
	}
}

// A handler that does not answer is bounded by startupValidateTimeout, the roles are
// validated concurrently (so the step waits ONE timeout, not one per role), and the
// role stays available.
func TestStartupValidate_HangingHandlerIsBoundedAndConcurrent(t *testing.T) {
	old := startupValidateTimeout
	startupValidateTimeout = 300 * time.Millisecond
	t.Cleanup(func() { startupValidateTimeout = old })

	var mu sync.Mutex
	started := 0
	hang := func(ctx context.Context, _ roles.Role) (wireclient.ValidateResult, error) {
		mu.Lock()
		started++
		mu.Unlock()
		<-ctx.Done()
		return wireclient.ValidateResult{}, ctx.Err()
	}
	begin := time.Now()
	f := bootWithValidate(t, hang)
	if elapsed := time.Since(begin); elapsed > 5*time.Second {
		t.Fatalf("startup took %v with hanging validates; want about one timeout", elapsed)
	}
	if started != 2 {
		t.Fatalf("validate started for %d roles, want 2 (the enabled ones)", started)
	}
	if !f.svc.Registry().Available("bad") || !f.svc.Registry().Available("good") {
		t.Fatal("a validate timeout is not a verdict: roles stay available")
	}
}

// A client that cannot validate at all (a plain HandlerClient) is skipped.
func TestStartupValidate_ClientWithoutValidatorIsSkipped(t *testing.T) {
	cfg := config.Config{LogDir: shortDir(t), Roles: roles.RoleSet{{Name: "r1", Enabled: true, Binds: []string{"t1"}}}}
	o := &orchestrator.Orchestrator{Cfg: cfg, Handler: &fakeHandlerClient{}}
	svc, _, _, storeClose, err := bootCore(context.Background(), cfg, o, cfg.Roles, runExclusions{}, core.RunModeDrainAndExit)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = storeClose() }()
	defer func() { _ = svc.Close() }()
	validateRolesAtStartup(context.Background(), o, cfg)
	if !svc.Registry().Available("r1") {
		t.Fatal("role must stay available")
	}
	validateRolesAtStartup(context.Background(), &orchestrator.Orchestrator{}, cfg) // nil Handler: no panic
}
