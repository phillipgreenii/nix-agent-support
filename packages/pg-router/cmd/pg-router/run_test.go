package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router/conformance"
	"github.com/phillipgreenii/pg-router/internal/activity"
	"github.com/phillipgreenii/pg-router/internal/backoff"
	"github.com/phillipgreenii/pg-router/internal/config"
	"github.com/phillipgreenii/pg-router/internal/core"
	"github.com/phillipgreenii/pg-router/internal/discover"
	"github.com/phillipgreenii/pg-router/internal/event"
	"github.com/phillipgreenii/pg-router/internal/eventqueue"
	"github.com/phillipgreenii/pg-router/internal/item"
	"github.com/phillipgreenii/pg-router/internal/metrics"
	"github.com/phillipgreenii/pg-router/internal/orchestrator"
	"github.com/phillipgreenii/pg-router/internal/query"
	"github.com/phillipgreenii/pg-router/internal/roles"
	"github.com/phillipgreenii/pg-router/internal/wireclient"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// fakeHandlerClient is a local wireclient.HandlerClient test double for
// cmd/pg-router's own tests (package orchestrator's own fakeHandler is
// unexported and cannot be imported from package main). It records every
// role name Dispatch/PostStartup/PreShutdown was called for.
type fakeHandlerClient struct {
	mu          sync.Mutex
	dispatched  []string
	postStartup []string
	preShutdown []string
}

func (f *fakeHandlerClient) Dispatch(_ context.Context, role roles.Role, _ eventqueue.Event) (wireclient.Reply, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dispatched = append(f.dispatched, role.Name)
	return wireclient.Reply{Outcome: "delivered"}, nil
}

func (f *fakeHandlerClient) PostStartup(_ context.Context, role roles.Role) (wireclient.Reply, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.postStartup = append(f.postStartup, role.Name)
	return wireclient.Reply{Outcome: "ok"}, nil
}

func (f *fakeHandlerClient) PreShutdown(_ context.Context, role roles.Role) (wireclient.Reply, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.preShutdown = append(f.preShutdown, role.Name)
	return wireclient.Reply{Outcome: "ok"}, nil
}

func (f *fakeHandlerClient) dispatchedCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.dispatched...)
}

func (f *fakeHandlerClient) postStartupCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.postStartup...)
}

func (f *fakeHandlerClient) preShutdownCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.preShutdown...)
}

// RoleSet.DeclaredBindTypes (moved from this package's own declaredBindTypes to
// a shared home, Task 1.1) must still count a role's Binds even when that role
// is disabled BY A RUN-SCOPED SELECTOR (applySelectors flips Enabled, never
// touches Binds): the "declared but inactive this run" half of INV-DISP-3
// depends on this — a selector-excluded role must still count as a DECLARED
// binding, never as if it were never configured at all.
func TestDeclaredBindTypes_selectorDisabledRoleStillCounts(t *testing.T) {
	rs := roles.RoleSet{
		{Name: "r1", Enabled: true, Binds: []string{"t1"}},
		{Name: "r2", Enabled: false, Binds: []string{"t2"}}, // as applySelectors would leave a --disable'd role
	}
	got := rs.DeclaredBindTypes()
	want := []string{"t1", "t2"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DeclaredBindTypes = %v, want %v (a run-scoped exclusion must not drop a type from the declared set)", got, want)
	}
}

// TestBootCore_selectorExcludedRoleNotRegisteredAsListener proves the
// acceptance criterion end to end: a role a run-scoped --disable excludes
// (applySelectors flips its Enabled to false) never gets a Listener
// registered by bootCore (run.go), so an event of its bound type is never
// offered to it — while an INCLUDED role's sibling event IS dispatched in the
// same pass. This is the composition of applySelectors' new Enabled-flip with
// bootCore's PRE-EXISTING role.Enabled skip (run.go) — nothing in bootCore
// itself needed to change.
func TestBootCore_selectorExcludedRoleNotRegisteredAsListener(t *testing.T) {
	fh := &fakeHandlerClient{}
	cfg := config.Config{
		LogDir: shortDir(t), // AF_UNIX path length cap; see shortDir's doc (ingest_event_test.go)
		Roles: roles.RoleSet{
			{Name: "r1", Enabled: true, Binds: []string{"t1"}},
			{Name: "r2", Enabled: true, Binds: []string{"t2"}},
		},
	}
	declaredRoles := cfg.Roles
	cfg, excluded, err := applySelectors(cfg, runSelectors{Disable: []string{"role:r2"}})
	if err != nil {
		t.Fatalf("applySelectors: %v", err)
	}
	if roleEnabled(cfg.Roles, "r2") {
		t.Fatalf("precondition: r2 must be selector-disabled")
	}
	if !roleEnabled(cfg.Roles, "r1") {
		t.Fatalf("precondition: r1 must remain enabled")
	}

	// Role dispatch always goes through Handler now (pg2-oju6w.15 / Task
	// 5.4) regardless of what used to be role.Type — never through o.Cmd,
	// which is a query-source-only seam.
	o := &orchestrator.Orchestrator{Cfg: cfg, Handler: fh}
	ctx := context.Background()
	svc, q, _, storeClose, err := bootCore(ctx, cfg, o, declaredRoles, excluded, core.RunModeDrainAndExit)
	if err != nil {
		t.Fatalf("bootCore: %v", err)
	}
	defer func() { _ = storeClose() }()
	defer func() { _ = svc.Close() }()

	future := time.Now().Add(time.Hour)
	payloadFor := func(typ string) map[string]any {
		return map[string]any{"id": "bd-" + typ, "type": "task"}
	}
	for _, typ := range []string{"t1", "t2"} {
		if _, err := q.Enqueue(eventqueue.Event{ID: "ev-" + typ, Type: typ, ExpiresAt: future, Payload: payloadFor(typ)}); err != nil {
			t.Fatalf("enqueue %s: %v", typ, err)
		}
	}
	q.Dispatch()

	if got := fh.dispatchedCalls(); len(got) != 1 || got[0] != "r1" {
		t.Fatalf("dispatched roles = %v, want exactly [r1] (r2 has no registered listener so its event is never offered)", got)
	}
}

// TestBootCore_InProcessParticipantAvailableImmediately proves Task 2.1's own
// named acceptance test: a registered in-process handler (a role's
// roleListener, registered onto the queue AND the registry by bootCore's own
// loop) is Available IMMEDIATELY after bootCore returns — before Accept ever
// runs, before any register verb crosses the wire at all. RED against the
// pre-fix code: bootCore never touched svc.Registry() (only q.Register), so
// Available was false for every role.
func TestBootCore_InProcessParticipantAvailableImmediately(t *testing.T) {
	cfg := config.Config{
		LogDir: shortDir(t),
		Roles: roles.RoleSet{
			{Name: "r1", Enabled: true, Binds: []string{"t1"}},
		},
	}
	o := &orchestrator.Orchestrator{Cfg: cfg}
	svc, _, _, storeClose, err := bootCore(context.Background(), cfg, o, cfg.Roles, runExclusions{}, core.RunModeDrainAndExit)
	if err != nil {
		t.Fatalf("bootCore: %v", err)
	}
	defer func() { _ = storeClose() }()
	defer func() { _ = svc.Close() }()

	if !svc.Registry().Available("r1") {
		t.Fatal("registered in-process handler r1 must be Available immediately after bootCore")
	}
}

// TestHandlerCommandFor_unconfiguredIsAnError proves handlerCommandFor names
// PG_ROUTER_HANDLER_COMMAND (not a hardcoded participant) and the role that
// needed it, when cfg.HandlerCommand is unset — GOAL-MIN-1's Floor (this
// bead, pg2-g068j) means this seam has no baked-in default to fall back to.
func TestHandlerCommandFor_unconfiguredIsAnError(t *testing.T) {
	commandFor := handlerCommandFor(config.Config{})
	_, err := commandFor(roles.Role{Name: "r1"}, "dispatch")
	if err == nil {
		t.Fatal("commandFor with no HandlerCommand configured must error, not silently resolve a command")
	}
	if !strings.Contains(err.Error(), "r1") || !strings.Contains(err.Error(), "PG_ROUTER_HANDLER_COMMAND") {
		t.Errorf("err = %q, want it to name the role and PG_ROUTER_HANDLER_COMMAND", err)
	}
}

// TestHandlerCommandFor_configuredReturnsArgv proves a configured
// HandlerCommand (with no HandlerCommandDir) resolves to [command,
// subcommand] — every enabled role shares the SAME command (DEC-WIRE-3's
// "shared process backing multiple roles" is an accepted shape) — with
// subcommand now placed by handlerCommandFor itself (pg2-ymb3v), not
// appended later by wireclient.Client.Dispatch.
func TestHandlerCommandFor_configuredReturnsArgv(t *testing.T) {
	commandFor := handlerCommandFor(config.Config{HandlerCommand: "pg-router-ccpool-handler"})
	got, err := commandFor(roles.Role{Name: "r1"}, "dispatch")
	if err != nil {
		t.Fatalf("commandFor: %v", err)
	}
	want := []string{"pg-router-ccpool-handler", "dispatch"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("commandFor = %v, want %v", got, want)
	}
}

// TestHandlerCommandFor_dirConfiguredDifferentiatesRoles proves the core fix
// (this bead, pg2-ymb3v, acceptance criterion a): with HandlerCommandDir
// set, two DIFFERENT roles resolve to two DISTINCT argvs — each carrying its
// own --role-config path — rather than the single shared command every role
// got before this bead. subcommand lands as argv[1] (right after the
// command, before the flags), matching pg-router-ccpool-handler's own CLI
// (main.go parses args[0] as the subcommand).
func TestHandlerCommandFor_dirConfiguredDifferentiatesRoles(t *testing.T) {
	commandFor := handlerCommandFor(config.Config{
		HandlerCommand:    "pg-router-ccpool-handler",
		HandlerCommandDir: "/etc/pg-router/roles",
	})

	feedback, err := commandFor(roles.Role{Name: "feedback"}, "dispatch")
	if err != nil {
		t.Fatalf("commandFor(feedback): %v", err)
	}
	worker, err := commandFor(roles.Role{Name: "worker"}, "dispatch")
	if err != nil {
		t.Fatalf("commandFor(worker): %v", err)
	}

	wantFeedback := []string{"pg-router-ccpool-handler", "dispatch", "--role-config", "/etc/pg-router/roles/feedback.json"}
	wantWorker := []string{"pg-router-ccpool-handler", "dispatch", "--role-config", "/etc/pg-router/roles/worker.json"}
	if !reflect.DeepEqual(feedback, wantFeedback) {
		t.Errorf("commandFor(feedback) = %v, want %v", feedback, wantFeedback)
	}
	if !reflect.DeepEqual(worker, wantWorker) {
		t.Errorf("commandFor(worker) = %v, want %v", worker, wantWorker)
	}
	if reflect.DeepEqual(feedback, worker) {
		t.Fatal("feedback and worker must resolve to DISTINCT argvs when HandlerCommandDir is set")
	}
}

// TestHandlerCommandFor_dirConfiguredPlacesSubcommandBeforeFlags proves the
// exact reason the seam had to widen: the subcommand MUST be argv[1], never
// pushed past the --role-config flag pair, or pg-router-ccpool-handler's own
// CLI would reject "--role-config" as an unknown subcommand.
func TestHandlerCommandFor_dirConfiguredPlacesSubcommandBeforeFlags(t *testing.T) {
	commandFor := handlerCommandFor(config.Config{
		HandlerCommand:    "pg-router-ccpool-handler",
		HandlerCommandDir: "/etc/pg-router/roles",
	})
	got, err := commandFor(roles.Role{Name: "worker"}, "postStartup")
	if err != nil {
		t.Fatalf("commandFor: %v", err)
	}
	if len(got) < 2 || got[1] != "postStartup" {
		t.Fatalf("argv = %v, want argv[1] == %q (the participant CLI's own first-argument subcommand parse)", got, "postStartup")
	}
}

// TestBootCore_wiresRealHandlerWhenUnset proves this bead's own acceptance
// criterion at the bootCore seam: bootCore now sets o.Handler to a real
// wireclient.Client (never leaves it nil, which used to fall through to
// orchestrator's unconfiguredHandler and "no Handler configured") whenever
// the caller has not already injected one.
func TestBootCore_wiresRealHandlerWhenUnset(t *testing.T) {
	cfg := config.Config{LogDir: shortDir(t)}
	o := &orchestrator.Orchestrator{Cfg: cfg}
	svc, _, _, storeClose, err := bootCore(context.Background(), cfg, o, nil, runExclusions{}, core.RunModeDrainAndExit)
	if err != nil {
		t.Fatalf("bootCore: %v", err)
	}
	defer func() { _ = storeClose() }()
	defer func() { _ = svc.Close() }()

	if o.Handler == nil {
		t.Fatal("bootCore must wire a real Handler, never leave it nil")
	}
	if _, ok := o.Handler.(*wireclient.Client); !ok {
		t.Fatalf("o.Handler = %T, want *wireclient.Client", o.Handler)
	}
	// cfg.HandlerCommand is unset here, so the wired client's own CommandFor
	// errors per-call rather than resolving — proving it is a REAL
	// wireclient.Client attempting real resolution (a different, more
	// specific error than "no Handler configured"), not a disguised
	// unconfiguredHandler.
	if _, err := o.Handler.Dispatch(context.Background(), roles.Role{Name: "r1"}, eventqueue.Event{}); err == nil {
		t.Fatal("Dispatch with no HandlerCommand configured must still error")
	} else if strings.Contains(err.Error(), "orchestrator: no Handler configured") {
		t.Errorf("err = %q, want the wireclient-level CommandFor error, not orchestrator's own unconfiguredHandler message", err)
	}
}

// --- warnHandlerCommandAmbiguity (this bead, pg2-ymb3v, acceptance criterion b) ---

// TestWarnHandlerCommandAmbiguity_firesForMultiRoleSingleCommand proves the
// boot-time WARN fires for exactly the silent-misconfiguration shape the
// bead's design names: more than one ENABLED role, only HandlerCommand set
// (no HandlerCommandDir).
func TestWarnHandlerCommandAmbiguity_firesForMultiRoleSingleCommand(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	cfg := config.Config{
		HandlerCommand: "pg-router-ccpool-handler",
		Roles: roles.RoleSet{
			{Name: "feedback", Enabled: true},
			{Name: "worker", Enabled: true},
		},
	}
	warnHandlerCommandAmbiguity(cfg)

	if !strings.Contains(buf.String(), "PG_ROUTER_HANDLER_COMMAND_DIR") {
		t.Fatalf("expected a WARN naming PG_ROUTER_HANDLER_COMMAND_DIR; got:\n%s", buf.String())
	}
}

// TestWarnHandlerCommandAmbiguity_silentWhenDirSet proves the WARN does NOT
// fire once the operator has actually configured HandlerCommandDir — the
// same multi-role shape is no longer a misconfiguration.
func TestWarnHandlerCommandAmbiguity_silentWhenDirSet(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	cfg := config.Config{
		HandlerCommand:    "pg-router-ccpool-handler",
		HandlerCommandDir: "/etc/pg-router/roles",
		Roles: roles.RoleSet{
			{Name: "feedback", Enabled: true},
			{Name: "worker", Enabled: true},
		},
	}
	warnHandlerCommandAmbiguity(cfg)

	if buf.Len() != 0 {
		t.Fatalf("expected no WARN once HandlerCommandDir is set; got:\n%s", buf.String())
	}
}

// TestWarnHandlerCommandAmbiguity_silentWhenOneRoleEnabled proves a
// single-enabled-role deployment (today's ordinary shape) is unaffected —
// sharing one command is not a misconfiguration when there is no sibling
// role to confuse it with, even if a second role is DECLARED but disabled.
func TestWarnHandlerCommandAmbiguity_silentWhenOneRoleEnabled(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	cfg := config.Config{
		HandlerCommand: "pg-router-ccpool-handler",
		Roles: roles.RoleSet{
			{Name: "feedback", Enabled: true},
			{Name: "worker", Enabled: false},
		},
	}
	warnHandlerCommandAmbiguity(cfg)

	if buf.Len() != 0 {
		t.Fatalf("expected no WARN with only one role enabled; got:\n%s", buf.String())
	}
}

// TestWarnHandlerCommandAmbiguity_silentWhenHandlerCommandUnset proves an
// unconfigured HandlerCommand is not this WARN's concern — that case
// already errors loudly per-dispatch (handlerCommandFor's own "no handler
// command configured" branch), so it must not ALSO warn at boot.
func TestWarnHandlerCommandAmbiguity_silentWhenHandlerCommandUnset(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	cfg := config.Config{
		Roles: roles.RoleSet{
			{Name: "feedback", Enabled: true},
			{Name: "worker", Enabled: true},
		},
	}
	warnHandlerCommandAmbiguity(cfg)

	if buf.Len() != 0 {
		t.Fatalf("expected no WARN with HandlerCommand unset; got:\n%s", buf.String())
	}
}

// TestBootCore_warnsHandlerCommandAmbiguity proves warnHandlerCommandAmbiguity
// is actually wired into the real boot path (bootCore), not just callable in
// isolation — alongside its per-role registration loop, per the bead's own
// design.
func TestBootCore_warnsHandlerCommandAmbiguity(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	cfg := config.Config{
		LogDir:         shortDir(t),
		HandlerCommand: "pg-router-ccpool-handler",
		Roles: roles.RoleSet{
			{Name: "feedback", Enabled: true, Binds: []string{"e"}},
			{Name: "worker", Enabled: true, Binds: []string{"e"}},
		},
	}
	o := &orchestrator.Orchestrator{Cfg: cfg}
	svc, _, _, storeClose, err := bootCore(context.Background(), cfg, o, nil, runExclusions{}, core.RunModeDrainAndExit)
	if err != nil {
		t.Fatalf("bootCore: %v", err)
	}
	defer func() { _ = storeClose() }()
	defer func() { _ = svc.Close() }()

	if !strings.Contains(buf.String(), "PG_ROUTER_HANDLER_COMMAND_DIR") {
		t.Fatalf("bootCore must warn about the multi-role-single-HandlerCommand misconfiguration; log:\n%s", buf.String())
	}
}

// TestBootCore_preservesCallerInjectedHandler proves bootCore's own "caller
// wins" pattern (the same seam-override shape o.Cmd/o.Log already use): a
// test double the caller sets BEFORE calling bootCore (this package's own
// fakeHandlerClient, used throughout this file) survives bootCore untouched
// — bootCore must not clobber it with a real wireclient.Client.
func TestBootCore_preservesCallerInjectedHandler(t *testing.T) {
	fh := &fakeHandlerClient{}
	cfg := config.Config{LogDir: shortDir(t)}
	o := &orchestrator.Orchestrator{Cfg: cfg, Handler: fh}
	svc, _, _, storeClose, err := bootCore(context.Background(), cfg, o, nil, runExclusions{}, core.RunModeDrainAndExit)
	if err != nil {
		t.Fatalf("bootCore: %v", err)
	}
	defer func() { _ = storeClose() }()
	defer func() { _ = svc.Close() }()

	if o.Handler != fh {
		t.Fatalf("bootCore replaced the caller-injected Handler; got %T, want the original *fakeHandlerClient", o.Handler)
	}
}

// selTestQuery is a minimal query.Query stand-in (mirrors internal/discover's
// own unexported fakeQuery, copied here since that one is package-private):
// it records whether Run was ever called and returns one canned event of its
// configured emit type.
type selTestQuery struct {
	query.Meta
	ran *bool
	typ string
}

func (q selTestQuery) Validate() error        { return nil }
func (q selTestQuery) BackingCommand() string { return "" }
func (q selTestQuery) Run(context.Context, query.Env) ([]event.Event, error) {
	*q.ran = true
	return []event.Event{event.NewItemEvent(q.typ, "", item.Item{ID: "x-" + q.typ, Type: "task"})}, nil
}

// TestApplySelectors_queryExcludedNeverProduces proves the second acceptance
// criterion: a query a run-scoped selector excludes (applySelectors drops it
// from cfg.Queries entirely) never has its Run called by ProduceTick, so no
// event from it ever reaches the queue — while a sibling INCLUDED query's
// event does.
func TestApplySelectors_queryExcludedNeverProduces(t *testing.T) {
	var q1Ran, q2Ran bool
	cfg := config.Config{
		Queries: query.SourceSet{
			{Name: "q1", Query: selTestQuery{Meta: query.Meta{EmitTypes: []string{"t1"}}, ran: &q1Ran, typ: "t1"}},
			{Name: "q2", Query: selTestQuery{Meta: query.Meta{EmitTypes: []string{"t2"}}, ran: &q2Ran, typ: "t2"}},
		},
	}
	cfg, _, err := applySelectors(cfg, runSelectors{Disable: []string{"query:q2"}})
	if err != nil {
		t.Fatalf("applySelectors: %v", err)
	}
	if len(cfg.Queries) != 1 || cfg.Queries[0].Name != "q1" {
		t.Fatalf("cfg.Queries = %v, want only q1", queryNames(cfg.Queries))
	}

	// t1/t2 declared directly (this test wires no Roles): a real run derives this
	// set from cfg.Roles.DeclaredBindTypes() (bootCore), but this test's own
	// concern is selector exclusion, not the undeclared-type rejection Task 1.1
	// added — so declare both query types to keep that orthogonal.
	o := &orchestrator.Orchestrator{Cfg: cfg, Bindings: core.NewBindings("t1", "t2")}
	queue, err := eventqueue.New(eventqueue.NewMemStore())
	if err != nil {
		t.Fatalf("eventqueue.New: %v", err)
	}
	if _, err := o.ProduceTick(context.Background(), queue); err != nil {
		t.Fatalf("ProduceTick: %v", err)
	}

	if !q1Ran {
		t.Error("q1 (not excluded) should have run")
	}
	if q2Ran {
		t.Error("q2 (selector-excluded) must never run")
	}
	depth := queue.DepthByType()
	if depth["t1"] != 1 {
		t.Errorf("depth[t1] = %d, want 1 (q1's event)", depth["t1"])
	}
	if depth["t2"] != 0 {
		t.Errorf("depth[t2] = %d, want 0 (q2 excluded, never produced)", depth["t2"])
	}
}

// recordingDispatchFailureObserver is a minimal eventqueue.Observer that only
// records OnDispatchFailure calls — enough to prove fanOutObserver.
// OnDispatchFailure (bead pg2-icm3u) reaches both fanned-out observers.
type recordingDispatchFailureObserver struct{ dispatchFailed []string }

func (*recordingDispatchFailureObserver) OnEnqueue(eventqueue.Event)        {}
func (*recordingDispatchFailureObserver) OnAccept(string, string)           {}
func (*recordingDispatchFailureObserver) OnUnconsumedExpired(string)        {}
func (*recordingDispatchFailureObserver) OnDeclined(string, string, string) {}
func (*recordingDispatchFailureObserver) OnDeduped(string)                  {}
func (r *recordingDispatchFailureObserver) OnDispatchFailure(t string) {
	r.dispatchFailed = append(r.dispatchFailed, t)
}

// fanOutObserver.OnDispatchFailure must call BOTH fanned-out observers, in
// order — exactly like its siblings OnEnqueue/OnAccept/OnUnconsumedExpired/
// OnDeclined already do (bootCore's one construction site relies on this to
// feed the metrics.Emitter and the activity.Ring from the same queue hook).
func TestFanOutObserver_OnDispatchFailureCallsBoth(t *testing.T) {
	a := &recordingDispatchFailureObserver{}
	b := &recordingDispatchFailureObserver{}
	f := fanOutObserver{a, b}

	f.OnDispatchFailure("review-requested")

	if !reflect.DeepEqual(a.dispatchFailed, []string{"review-requested"}) {
		t.Fatalf("a.dispatchFailed = %v, want [review-requested]", a.dispatchFailed)
	}
	if !reflect.DeepEqual(b.dispatchFailed, []string{"review-requested"}) {
		t.Fatalf("b.dispatchFailed = %v, want [review-requested]", b.dispatchFailed)
	}
}

// flakySourceQuery is a minimal pull-source query.Query stand-in (mirrors
// internal/discover's own unexported flakyQuery, copied here since that one is
// package-private, the same convention selTestQuery above already follows):
// it fails its first failTimes Run calls, then succeeds. calls counts every
// Run invocation via a shared pointer so it survives Source.Query's by-value
// interface storage.
type flakySourceQuery struct {
	query.Meta
	failTimes int
	calls     *int
}

func (f flakySourceQuery) Validate() error        { return nil }
func (f flakySourceQuery) BackingCommand() string { return "" }
func (f flakySourceQuery) Run(context.Context, query.Env) ([]event.Event, error) {
	*f.calls++
	if *f.calls <= f.failTimes {
		return nil, errors.New("source unavailable")
	}
	return nil, nil
}

// TestBootCore_wiresMetricsEmitterAsProduceTickSourceFailureObserver proves
// the production wiring bootCore now performs (INV-FAIL-3, register gap R21 /
// bead pg2-00jpn): a pull-source query that fails and retries drives the SAME
// metrics.Emitter bootCore constructs, via o.SourceFailureObserver threaded
// through Orchestrator.ProduceTick's discover.Produce call — so
// MetricSourceFailures actually increments in the running binary, closing the
// gap where source failures were recorded to logs only (discover.go's
// runAndEnqueue Warn line existed; nothing fed its metrics half).
func TestBootCore_wiresMetricsEmitterAsProduceTickSourceFailureObserver(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	calls := 0
	cfg := config.Config{
		LogDir:        shortDir(t), // AF_UNIX path length cap; see shortDir's doc (ingest_event_test.go)
		MeterProvider: mp,
		Queries: query.SourceSet{
			{Name: "flaky-src", Query: flakySourceQuery{
				Meta: query.Meta{
					EmitTypes: []string{"t1"},
					FB: query.FailureBackoff{
						Policy:  backoff.Policy{Initial: time.Millisecond, Factor: 2, Max: time.Millisecond},
						Retries: 1,
					},
				},
				failTimes: 1, // fails once, succeeds on the 2nd Run
				calls:     &calls,
			}},
		},
	}
	o := &orchestrator.Orchestrator{Cfg: cfg}
	ctx := context.Background()
	svc, q, _, storeClose, err := bootCore(ctx, cfg, o, cfg.Roles, runExclusions{}, core.RunModeDrainAndExit)
	if err != nil {
		t.Fatalf("bootCore: %v", err)
	}
	defer func() { _ = storeClose() }()
	defer func() { _ = svc.Close() }()

	if _, err := o.ProduceTick(ctx, q); err != nil {
		t.Fatalf("ProduceTick: %v", err)
	}
	if calls != 2 {
		t.Fatalf("Run was called %d times, want 2 (1 failure + 1 success)", calls)
	}

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(ctx, &rm); err != nil {
		t.Fatalf("collect: %v", err)
	}
	got := int64(-1)
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != metrics.MetricSourceFailures {
				continue
			}
			s, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				continue
			}
			for _, dp := range s.DataPoints {
				if v, present := dp.Attributes.Value(attribute.Key("source")); present && v.AsString() == "flaky-src" {
					got = dp.Value
				}
			}
		}
	}
	if got != 1 {
		t.Fatalf("%s{source=flaky-src} = %d, want 1 (bootCore must wire the emitter as o.SourceFailureObserver so ProduceTick's retry reaches it)", metrics.MetricSourceFailures, got)
	}
}

// TestBootCore_DefaultMeterProviderWiresReadableMetricsReader proves the
// Task 3.6-prereq value-read-back acceptance criterion end to end at the
// production wiring site: when Config.MeterProvider is unset (the default —
// nothing sets it in production today), bootCore must give the returned
// core.Service a NON-NIL MetricsReader whose Snapshot actually reflects a
// live queue mutation, not the plain no-op provider Config.Meter() itself
// still defaults to (that provider can never be read back — see
// resolveMeterProvider's doc for why bootCore stopped calling cfg.Meter()
// directly).
func TestBootCore_DefaultMeterProviderWiresReadableMetricsReader(t *testing.T) {
	cfg := config.Config{LogDir: shortDir(t)}
	o := &orchestrator.Orchestrator{Cfg: cfg}
	ctx := context.Background()
	svc, q, _, storeClose, err := bootCore(ctx, cfg, o, cfg.Roles, runExclusions{}, core.RunModeDrainAndExit)
	if err != nil {
		t.Fatalf("bootCore: %v", err)
	}
	defer func() { _ = storeClose() }()
	defer func() { _ = svc.Close() }()

	reader := svc.MetricsReader()
	if reader == nil {
		t.Fatal("MetricsReader() = nil, want a wired read-back handle when Config.MeterProvider is unset")
	}

	future := time.Now().Add(time.Hour)
	if _, err := q.Enqueue(eventqueue.Event{ID: "ev-1", Type: "review-requested", ExpiresAt: future}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	rm, err := reader.Snapshot(ctx)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	got := int64(-1)
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != metrics.MetricQueueDepth {
				continue
			}
			g, ok := m.Data.(metricdata.Gauge[int64])
			if !ok {
				continue
			}
			for _, dp := range g.DataPoints {
				if v, present := dp.Attributes.Value(attribute.Key("type")); present && v.AsString() == "review-requested" {
					got = dp.Value
				}
			}
		}
	}
	if got != 1 {
		t.Fatalf("%s{type=review-requested} via svc.MetricsReader().Snapshot() = %d, want 1 (bootCore's default MeterProvider must be read-back-capable)", metrics.MetricQueueDepth, got)
	}
}

// TestBootCore_ExternalMeterProviderLeavesMetricsReaderNil proves the
// documented degradation: when a deployment binds its OWN external
// MeterProvider (Config.MeterProvider, Task 3.3's binding decision), bootCore
// must use it as-is (unchanged from Task 3.3) and MUST NOT report a
// MetricsReader — the OTel SDK provides no way to retrofit a second reader
// onto an already-constructed provider this function does not own.
func TestBootCore_ExternalMeterProviderLeavesMetricsReaderNil(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	cfg := config.Config{LogDir: shortDir(t), MeterProvider: mp}
	o := &orchestrator.Orchestrator{Cfg: cfg}
	ctx := context.Background()
	svc, _, gotMP, storeClose, err := bootCore(ctx, cfg, o, cfg.Roles, runExclusions{}, core.RunModeDrainAndExit)
	if err != nil {
		t.Fatalf("bootCore: %v", err)
	}
	defer func() { _ = storeClose() }()
	defer func() { _ = svc.Close() }()

	if gotMP != mp {
		t.Fatalf("bootCore's returned MeterProvider changed identity; want the exact configured one back unmodified")
	}
	if got := svc.MetricsReader(); got != nil {
		t.Fatalf("MetricsReader() = %v, want nil when Config.MeterProvider is externally set", got)
	}
}

// TestBootCore_LivenessRegisteredInDaemonMode proves this bead's (pg2-tp13g)
// own wiring: passing core.RunModeLongRunning — runRun's own call — makes
// bootCore register MetricLiveness reporting 1, the operator's DECIDED
// process-health semantics ("report 1 as long as the pg-router daemon
// process is up and its metrics endpoint responds"). internal/metrics'
// own TestLivenessReflectsIsLive already proves WithLiveness's mechanics in
// isolation; this proves bootCore actually SUPPLIES the option in daemon
// mode, at the real production wiring site.
func TestBootCore_LivenessRegisteredInDaemonMode(t *testing.T) {
	cfg := config.Config{LogDir: shortDir(t)}
	o := &orchestrator.Orchestrator{Cfg: cfg}
	ctx := context.Background()
	svc, _, _, storeClose, err := bootCore(ctx, cfg, o, cfg.Roles, runExclusions{}, core.RunModeLongRunning)
	if err != nil {
		t.Fatalf("bootCore: %v", err)
	}
	defer func() { _ = storeClose() }()
	defer func() { _ = svc.Close() }()

	reader := svc.MetricsReader()
	if reader == nil {
		t.Fatal("MetricsReader() = nil, want a wired read-back handle")
	}
	rm, err := reader.Snapshot(ctx)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	found := false
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != metrics.MetricLiveness {
				continue
			}
			g, ok := m.Data.(metricdata.Gauge[int64])
			if !ok || len(g.DataPoints) == 0 {
				continue
			}
			found = true
			if got := g.DataPoints[0].Value; got != 1 {
				t.Fatalf("%s = %d, want 1 in daemon mode (core.RunModeLongRunning)", metrics.MetricLiveness, got)
			}
		}
	}
	if !found {
		t.Fatalf("%s not registered; bootCore(core.RunModeLongRunning) must pass metrics.WithLiveness", metrics.MetricLiveness)
	}
}

// TestBootCore_LivenessNotRegisteredInDrainAndExitMode proves the other half
// of this bead's (pg2-tp13g) binding decision: core.RunModeDrainAndExit
// (runUntilIdleGated/runRunUntilIdle's own call) MUST NOT register
// MetricLiveness at all — Task 3.3's binding decision that drain-and-exit
// never registers this observable, not merely never observes it true.
func TestBootCore_LivenessNotRegisteredInDrainAndExitMode(t *testing.T) {
	cfg := config.Config{LogDir: shortDir(t)}
	o := &orchestrator.Orchestrator{Cfg: cfg}
	ctx := context.Background()
	svc, _, _, storeClose, err := bootCore(ctx, cfg, o, cfg.Roles, runExclusions{}, core.RunModeDrainAndExit)
	if err != nil {
		t.Fatalf("bootCore: %v", err)
	}
	defer func() { _ = storeClose() }()
	defer func() { _ = svc.Close() }()

	reader := svc.MetricsReader()
	if reader == nil {
		t.Fatal("MetricsReader() = nil, want a wired read-back handle")
	}
	rm, err := reader.Snapshot(ctx)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name == metrics.MetricLiveness {
				t.Fatalf("%s registered in core.RunModeDrainAndExit; want it absent entirely", metrics.MetricLiveness)
			}
		}
	}
}

// TestResolveMetricsAddr_precedence proves runRun's --metrics-addr
// resolution: the flag occurrence wins over PG_ROUTER_METRICS_ADDR
// (cfg.MetricsAddr), which wins over "" (disabled) — the same
// CLI-flag-over-env-over-default precedence PG_ROUTER_TUI_INTERVAL already
// documents.
func TestResolveMetricsAddr_precedence(t *testing.T) {
	cases := []struct {
		name     string
		flagAddr string
		cfgAddr  string
		wantAddr string
	}{
		{"flag wins over env", "127.0.0.1:9001", "127.0.0.1:9820", "127.0.0.1:9001"},
		{"env used when flag empty", "", "127.0.0.1:9820", "127.0.0.1:9820"},
		{"disabled when both empty", "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveMetricsAddr(tc.flagAddr, config.Config{MetricsAddr: tc.cfgAddr})
			if got != tc.wantAddr {
				t.Errorf("resolveMetricsAddr(%q, {MetricsAddr: %q}) = %q, want %q", tc.flagAddr, tc.cfgAddr, got, tc.wantAddr)
			}
		})
	}
}

// TestBootCore_ThreadsMonitorSubsetsIntoCoreOptions proves Task 3.6-prereq's
// second acceptance criterion end to end: Config.MonitorSubsets (resolved
// from config, BEFORE any mon.read caller ever calls register) actually
// reaches core.Service.Register's resolution, through bootCore's
// monitorSubsetResolverFrom adaptation — the full path Task 3.6's mon.read
// handler will rely on.
func TestBootCore_ThreadsMonitorSubsetsIntoCoreOptions(t *testing.T) {
	cfg := config.Config{
		LogDir:         shortDir(t),
		MonitorSubsets: map[string][]string{"mon-1": {"queue_depth", "unconsumed_expired"}},
	}
	o := &orchestrator.Orchestrator{Cfg: cfg}
	ctx := context.Background()
	svc, _, _, storeClose, err := bootCore(ctx, cfg, o, cfg.Roles, runExclusions{}, core.RunModeDrainAndExit)
	if err != nil {
		t.Fatalf("bootCore: %v", err)
	}
	defer func() { _ = storeClose() }()
	defer func() { _ = svc.Close() }()

	reg, err := svc.Register("mon-1", core.KindMonitor)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if !reflect.DeepEqual(reg.Subset, []string{"queue_depth", "unconsumed_expired"}) {
		t.Fatalf("Subset = %v, want [queue_depth unconsumed_expired] (from Config.MonitorSubsets)", reg.Subset)
	}

	unconfigured, err := svc.Register("mon-2", core.KindMonitor)
	if err != nil {
		t.Fatalf("Register mon-2: %v", err)
	}
	if unconfigured.Subset != nil {
		t.Fatalf("Subset = %v, want nil for an id absent from Config.MonitorSubsets", unconfigured.Subset)
	}
}

// activityObserver.OnDispatchFailure (bead pg2-icm3u) must append a
// "dispatch_failed" Entry to the ring — the fourth outcome its own doc
// comment now enumerates, alongside delivered/missed/declined.
func TestActivityObserver_OnDispatchFailureAppendsEntry(t *testing.T) {
	ring := activity.New(4)
	a := newActivityObserver(ring)

	a.OnDispatchFailure("review-requested")

	buf := make([]activity.Entry, 4)
	n, _ := ring.Read(0, buf)
	if n != 1 {
		t.Fatalf("ring entries = %d, want 1", n)
	}
	if buf[0].Type != "review-requested" || buf[0].Outcome != "dispatch_failed" {
		t.Fatalf("entry = %+v, want {Type: review-requested, Outcome: dispatch_failed}", buf[0])
	}
}

// TestActivityObserver_OnResourceLimitThenOnAcceptRendersBudgetEscalation is
// this bead's (pg2-fm2gw) required RED test for acceptance criterion 2: the
// Activity Ring records a "budget_escalation" event sourced from
// roleListener.Offer's new hook. It replays the SAME call ordering
// production sees for one accepted-but-resource-limited dispatch —
// OnEnqueue (queued), then OnResourceLimit (fired inline from Offer, before
// Offer returns), then OnAccept (fired moments later by the queue's own
// Dispatch phase 3 for the identical eventID) — and asserts exactly ONE
// ring entry results, carrying "budget_escalation", never the default
// "delivered" OnAccept alone would produce.
func TestActivityObserver_OnResourceLimitThenOnAcceptRendersBudgetEscalation(t *testing.T) {
	ring := activity.New(4)
	a := newActivityObserver(ring)

	a.OnEnqueue(eventqueue.Event{ID: "evt-1", Type: "review-requested"})
	a.OnResourceLimit("evt-1", "review-requested")
	a.OnAccept("evt-1", "worker")

	buf := make([]activity.Entry, 4)
	n, _ := ring.Read(0, buf)
	if n != 1 {
		t.Fatalf("ring entries = %d, want exactly 1 (OnResourceLimit must SUPPRESS the default delivered entry, not add a second one); entries=%+v", n, buf[:n])
	}
	if buf[0].Type != "review-requested" || buf[0].Outcome != "budget_escalation" {
		t.Fatalf("entry = %+v, want {Type: review-requested, Outcome: budget_escalation}", buf[0])
	}
}

// TestActivityObserver_OnResourceLimitWithoutPriorOnEnqueueStillRecovers
// proves OnResourceLimit's own doc's upsert claim: even when the pending
// entry was never seeded by OnEnqueue (an edge case the doc calls out —
// e.g. a pending entry the activityPendingTypesCap FIFO already evicted),
// OnResourceLimit still records the right Type (it carries its own evtType
// argument, unlike OnAccept) and the subsequent OnAccept still renders
// "budget_escalation".
func TestActivityObserver_OnResourceLimitWithoutPriorOnEnqueueStillRecovers(t *testing.T) {
	ring := activity.New(4)
	a := newActivityObserver(ring)

	a.OnResourceLimit("evt-2", "worker-ready") // no OnEnqueue("evt-2", ...) at all
	a.OnAccept("evt-2", "worker")

	buf := make([]activity.Entry, 4)
	n, _ := ring.Read(0, buf)
	if n != 1 {
		t.Fatalf("ring entries = %d, want 1", n)
	}
	if buf[0].Type != "worker-ready" || buf[0].Outcome != "budget_escalation" {
		t.Fatalf("entry = %+v, want {Type: worker-ready, Outcome: budget_escalation}", buf[0])
	}
}

// TestActivityObserver_OnAcceptWithoutOnResourceLimitStillRendersDelivered
// is the negative control: an ordinary accept with NO OnResourceLimit call
// at all must render "delivered" exactly as before this bead — the new
// suppression path must never fire uninvited.
func TestActivityObserver_OnAcceptWithoutOnResourceLimitStillRendersDelivered(t *testing.T) {
	ring := activity.New(4)
	a := newActivityObserver(ring)

	a.OnEnqueue(eventqueue.Event{ID: "evt-3", Type: "review-requested"})
	a.OnAccept("evt-3", "worker")

	buf := make([]activity.Entry, 4)
	n, _ := ring.Read(0, buf)
	if n != 1 {
		t.Fatalf("ring entries = %d, want 1", n)
	}
	if buf[0].Type != "review-requested" || buf[0].Outcome != "delivered" {
		t.Fatalf("entry = %+v, want {Type: review-requested, Outcome: delivered}", buf[0])
	}
}

// TestResolvedConfigFor_drainAndExitOmitsPollInterval is the run-mode gating
// test [design: Task 3.5 Step 7]: "drain-and-exit" omits PollInterval
// (Task 3.8's eventual tickIntervalMs) from the composed view entirely — a
// nil pointer, not a zero duration — while "long-running" carries it.
func TestResolvedConfigFor_drainAndExitOmitsPollInterval(t *testing.T) {
	cfg := config.Config{PollInterval: 7 * time.Second}

	drain := resolvedConfigFor(cfg, core.RunModeDrainAndExit)
	if drain.PollInterval != nil {
		t.Fatalf("PollInterval = %v, want nil (omitted) in drain-and-exit mode", *drain.PollInterval)
	}

	long := resolvedConfigFor(cfg, core.RunModeLongRunning)
	if long.PollInterval == nil || *long.PollInterval != cfg.PollInterval {
		t.Fatalf("PollInterval = %v, want %v in long-running mode", long.PollInterval, cfg.PollInterval)
	}
}

// TestResolvedConfigFor_countsActiveRolesAndQueries proves the other
// ResolvedConfig fields reflect the post-selector active set, not the
// configuration's full declared set.
func TestResolvedConfigFor_countsActiveRolesAndQueries(t *testing.T) {
	cfg := config.Config{
		RepoRoot:    "/repo",
		BeadsPrefix: "pfx",
		Roles: roles.RoleSet{
			{Name: "r1", Enabled: true},
			{Name: "r2", Enabled: false}, // as applySelectors would leave a --disable'd role
		},
		Queries: query.SourceSet{{Name: "q1"}},
	}

	rc := resolvedConfigFor(cfg, core.RunModeLongRunning)
	if rc.RepoRoot != "/repo" || rc.BeadsPrefix != "pfx" {
		t.Fatalf("RepoRoot/BeadsPrefix = %q/%q, want /repo / pfx", rc.RepoRoot, rc.BeadsPrefix)
	}
	if rc.ActiveRoles != 1 {
		t.Fatalf("ActiveRoles = %d, want 1 (only the enabled role)", rc.ActiveRoles)
	}
	if rc.ActiveQueries != 1 {
		t.Fatalf("ActiveQueries = %d, want 1", rc.ActiveQueries)
	}
}

// TestSourceReportsFor_oneReportPerActiveSource proves sourceReportsFor
// reflects cfg.Queries verbatim — the already-post-selector active subset —
// and that an empty set produces nil, not an empty non-nil slice. Task 4.1
// widens the assertion to cover Type (always "pull") and the LastTick/
// Failure threading: LastTick comes from the caller's own merged-forward
// lastTick map (Orchestrator.LastTick, pg2-bzb8i), Failure from this pass's
// own discover.ProduceReport.
func TestSourceReportsFor_oneReportPerActiveSource(t *testing.T) {
	now := time.Date(2026, 9, 1, 0, 5, 0, 0, time.UTC)
	rpt := discover.ProduceReport{
		Failure: map[string]discover.FailureInfo{"e2e-source": {Count: 2, NextEligible: now}},
	}
	lastTick := map[string]time.Time{"beads-ready": now}
	got := sourceReportsFor(query.SourceSet{{Name: "beads-ready"}, {Name: "e2e-source"}}, rpt, lastTick)
	want := []core.SourceReport{
		{Name: "beads-ready", Type: "pull", LastTick: now},
		{Name: "e2e-source", Type: "pull", Failure: &core.FailureInfo{Count: 2, NextEligible: now}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sourceReportsFor = %+v, want %+v", got, want)
	}

	if got := sourceReportsFor(nil, discover.ProduceReport{}, nil); got != nil {
		t.Fatalf("sourceReportsFor(nil) = %+v, want nil", got)
	}
}

// TestSourceReportsFor_lastTickSurvivesAPassThatSkippedTheSource is
// pg2-bzb8i's own regression test: a source whose cadence gated it off THIS
// pass (so rpt itself carries no LastTick at all — Task 4.1 already dropped
// that field from ProduceReport's own per-pass relevance here) still
// reports the real time it last fired, read from the caller's persisted
// lastTick map, instead of reverting to the zero value ("-"/idle in the
// TUI) the way a per-pass-only view previously did on nearly every poll.
func TestSourceReportsFor_lastTickSurvivesAPassThatSkippedTheSource(t *testing.T) {
	firedAt := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	rpt := discover.ProduceReport{} // this pass fired nothing for beads-ready
	lastTick := map[string]time.Time{"beads-ready": firedAt}

	got := sourceReportsFor(query.SourceSet{{Name: "beads-ready"}}, rpt, lastTick)
	want := []core.SourceReport{{Name: "beads-ready", Type: "pull", LastTick: firedAt}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sourceReportsFor = %+v, want %+v (LastTick must survive a pass that skipped this source)", got, want)
	}
}

// TestGateFileInfo_unsetWhenPathEmptyOrAbsent matches
// orchestrator.gated()'s own "" ⇒ never-gated short-circuit, and reports the
// file's mtime when it does exist.
func TestGateFileInfo_unsetWhenPathEmptyOrAbsent(t *testing.T) {
	if got := gateFileInfo(""); got.Set {
		t.Fatalf("gateFileInfo(\"\") = %+v, want unset", got)
	}

	dir := t.TempDir()
	missing := dir + "/no-such-gate"
	if got := gateFileInfo(missing); got.Set {
		t.Fatalf("gateFileInfo(%q) = %+v, want unset (file absent)", missing, got)
	}

	present := dir + "/operator-paused"
	if err := os.WriteFile(present, nil, 0o644); err != nil {
		t.Fatalf("write gate file: %v", err)
	}
	got := gateFileInfo(present)
	if !got.Set {
		t.Fatalf("gateFileInfo(%q) = %+v, want Set=true", present, got)
	}
	fi, err := os.Stat(present)
	if err != nil {
		t.Fatalf("stat gate file: %v", err)
	}
	if !got.Mtime.Equal(fi.ModTime()) {
		t.Fatalf("Mtime = %v, want %v", got.Mtime, fi.ModTime())
	}
}

// TestCurrentGateFiles_namesBothFileDirectGates proves currentGateFiles
// reports both file-direct gates (Task 1.2b, ADR 0036) under the fixed
// gateTickKeyOperatorPaused/gateTickKeyCICDDown keys svc.ObserveGateFromTick's caller and,
// eventually, Task 3.9's socket verbs must agree on.
func TestCurrentGateFiles_namesBothFileDirectGates(t *testing.T) {
	dir := t.TempDir()
	operatorPaused := dir + "/operator-paused"
	if err := os.WriteFile(operatorPaused, nil, 0o644); err != nil {
		t.Fatalf("write gate file: %v", err)
	}
	cfg := config.Config{OperatorPaused: operatorPaused, CICDDown: dir + "/cicd-down-absent"}

	gates := currentGateFiles(cfg)
	if got := gates[gateTickKeyOperatorPaused]; !got.Set {
		t.Fatalf("gates[%q] = %+v, want Set=true", gateTickKeyOperatorPaused, got)
	}
	if got := gates[gateTickKeyCICDDown]; got.Set {
		t.Fatalf("gates[%q] = %+v, want unset (file absent)", gateTickKeyCICDDown, got)
	}
}

// writeGateFile creates a gate sentinel file under its OWN fresh t.TempDir()
// (deliberately never cfg.LogDir — a gate sentinel is not core state, and
// mixing the two would leave a stray gates-shaped file in a LogDir fixture)
// and returns its path.
func writeGateFile(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "operator-paused")
	if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRunUntilIdleGated_reachableAnswersIngestNoDispatch(t *testing.T) {
	fh := &fakeHandlerClient{}
	logDir := shortDir(t)
	cfg := config.Config{
		LogDir:         logDir,
		OperatorPaused: writeGateFile(t),
		Roles: roles.RoleSet{
			{Name: "r1", Enabled: true, Binds: []string{"t1"}},
			{Name: "r2", Enabled: false, Binds: []string{"t2"}}, // disabled: must never get a lifecycle hook
		},
	}
	o := &orchestrator.Orchestrator{Cfg: cfg, Handler: fh}
	if !o.Gated() {
		t.Fatal("precondition: cfg must be gated (OperatorPaused sentinel present)")
	}

	done := make(chan int, 1)
	go func() { done <- runUntilIdleGated(context.Background(), cfg, o, cfg.Roles, runExclusions{}) }()

	var ref core.Ref
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if r, err := core.Discover(logDir); err == nil {
			ref = r
			break
		}
		time.Sleep(time.Millisecond)
	}
	if ref == (core.Ref{}) {
		t.Fatal("gated run-until-idle never became discoverable; it must still boot the core (INV-LIFE-1)")
	}

	// postStartupAll (pg2-oju6w.15) fires right after bootCore succeeds,
	// before the gated drain loop even starts — by the time the core is
	// discoverable it must already have fired, once, for the ENABLED role
	// only (never declaredRoles' disabled r2).
	if got := fh.postStartupCalls(); len(got) != 1 || got[0] != "r1" {
		t.Fatalf("postStartup calls = %v, want exactly [r1] (disabled r2 must never get the hook)", got)
	}

	var stdout, stderr strings.Builder
	code := callCore(&stdout, &stderr, ref, core.SubcommandIngestEvent,
		[]byte(`{"schemaVersion":"1","id":"trk-1","events":[{"id":"e1","type":"t1"}]}`))
	if code != conformance.ExitOK {
		t.Fatalf("ingest-event while gated: exit = %d, want 0; stderr=%s", code, stderr.String())
	}
	var reply map[string]any
	if err := json.Unmarshal([]byte(stdout.String()), &reply); err != nil {
		t.Fatalf("stdout %q is not JSON: %v", stdout.String(), err)
	}
	if reply["accepted"] != float64(1) {
		t.Fatalf("accepted = %v, want 1 — a gated core must still answer ingest-event and durably enqueue", reply["accepted"])
	}

	exitCode := <-done
	if exitCode != exitOK {
		t.Fatalf("runUntilIdleGated exit = %d, want %d", exitCode, exitOK)
	}
	if len(fh.dispatchedCalls()) != 0 {
		t.Fatalf("gated run-until-idle must dispatch nothing; dispatched = %v", fh.dispatchedCalls())
	}
	// preShutdownAll fires at the same point TeardownAll used to, exactly
	// once per shutdown (pg2-asr8z dedup) — with only one enabled role here,
	// that single call must land on it, never on disabled r2.
	if got := fh.preShutdownCalls(); len(got) != 1 || got[0] != "r1" {
		t.Fatalf("preShutdown calls = %v, want exactly [r1] (disabled r2 must never get the hook)", got)
	}
}

// TestPostStartupAll_onlyEnabledRoles and TestPreShutdownAll_* are
// pg2-oju6w.15's direct unit coverage for the two helpers run.go's three
// entry points (runRun/runRunUntilIdle/runUntilIdleGated) share, and skip
// silently (no panic) when Handler is nil — the known, out-of-scope
// CommandFor/bootCore-wiring gap (pg2-oju6w.15's own plan, Section 0).
//
// The two helpers now differ in per-role fan-out (pg2-asr8z, superseding
// pg2-oju6w.15's original "both dispatch once per enabled role" shape):
// postStartupAll still calls Handler.PostStartup exactly once per role with
// r.Enabled == true (mirroring bootCore's own registration loop — never
// declaredRoles, the full pre-selector superset). preShutdownAll instead
// calls Handler.PreShutdown exactly ONCE per shutdown, for the first
// enabled role only — see preShutdownAll's own doc (run.go) for why
// dispatching it once per enabled role was redundant, not merely harmless.
func TestPostStartupAll_onlyEnabledRoles(t *testing.T) {
	fh := &fakeHandlerClient{}
	o := &orchestrator.Orchestrator{Handler: fh}
	cfg := config.Config{Roles: roles.RoleSet{
		{Name: "enabled-1", Enabled: true},
		{Name: "disabled-1", Enabled: false},
		{Name: "enabled-2", Enabled: true},
	}}
	postStartupAll(context.Background(), o, cfg)
	got := fh.postStartupCalls()
	want := []string{"enabled-1", "enabled-2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("postStartup calls = %v, want %v", got, want)
	}
}

func TestPostStartupAll_nilHandlerDoesNotPanic(t *testing.T) {
	o := &orchestrator.Orchestrator{}
	cfg := config.Config{Roles: roles.RoleSet{{Name: "r1", Enabled: true}}}
	postStartupAll(context.Background(), o, cfg) // must not panic
}

// TestPreShutdownAll_dedupesToOneCallForFirstEnabledRole is pg2-asr8z's
// direct RED/GREEN coverage for the dedup fix: pre-fix, this asserted
// exactly the OPPOSITE (want == []string{"enabled-1", "enabled-2"}, one call
// per enabled role) — that shape is what let launchd's 5s ExitTimeOut
// SIGKILL the daemon before preShutdownAll's sweep(s) could ever finish (see
// preShutdownAll's own doc, run.go). A leading disabled role is included
// specifically to prove the dedup call lands on the first ENABLED role, not
// literally cfg.Roles[0].
func TestPreShutdownAll_dedupesToOneCallForFirstEnabledRole(t *testing.T) {
	fh := &fakeHandlerClient{}
	o := &orchestrator.Orchestrator{Handler: fh}
	cfg := config.Config{Roles: roles.RoleSet{
		{Name: "disabled-1", Enabled: false},
		{Name: "enabled-1", Enabled: true},
		{Name: "enabled-2", Enabled: true},
	}}
	preShutdownAll(context.Background(), o, cfg)
	got := fh.preShutdownCalls()
	want := []string{"enabled-1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("preShutdown calls = %v, want %v (pg2-asr8z: exactly one call per shutdown, never one per enabled role)", got, want)
	}
}

// TestPreShutdownAll_noEnabledRolesDispatchesNothing proves the dedup fix's
// loop-with-early-return shape still degrades to a no-op (never panics or
// dispatches) when no role is enabled — the same case the pre-fix loop
// handled by simply never entering its body.
func TestPreShutdownAll_noEnabledRolesDispatchesNothing(t *testing.T) {
	fh := &fakeHandlerClient{}
	o := &orchestrator.Orchestrator{Handler: fh}
	cfg := config.Config{Roles: roles.RoleSet{
		{Name: "disabled-1", Enabled: false},
		{Name: "disabled-2", Enabled: false},
	}}
	preShutdownAll(context.Background(), o, cfg)
	if got := fh.preShutdownCalls(); len(got) != 0 {
		t.Fatalf("preShutdown calls = %v, want none (no enabled role)", got)
	}
}

func TestPreShutdownAll_nilHandlerDoesNotPanic(t *testing.T) {
	o := &orchestrator.Orchestrator{}
	cfg := config.Config{Roles: roles.RoleSet{{Name: "r1", Enabled: true}}}
	preShutdownAll(context.Background(), o, cfg) // must not panic
}

// TestRunOneTick_gatedStillExpiresDueEvent proves INV-LIFE-2's "Expiry MUST
// continue while gated": a gated tick must still run q.Expire(), even though
// it skips ProduceTick/Dispatch entirely. An orphan event (no listener bound
// to its type) past its ExpiresAt is retained only until SOME Expire() call
// evicts it (eventqueue.Queue.Expire's retainedLocked: an unmatched type is
// vacuously not owed an attempt), so this needs no dispatch/listener setup at
// all to prove the point. RED against the pre-fix code: the gated branch
// never called q.Expire() (or anything else) at all.
func TestRunOneTick_gatedStillExpiresDueEvent(t *testing.T) {
	svc := &core.Service{}
	q, err := eventqueue.New(eventqueue.NewMemStore())
	if err != nil {
		t.Fatalf("eventqueue.New: %v", err)
	}
	past := time.Now().Add(-time.Hour)
	if _, err := q.Enqueue(eventqueue.Event{ID: "ev1", Type: "orphan", ExpiresAt: past}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if depth := q.DepthByType()["orphan"]; depth != 1 {
		t.Fatalf("precondition: depth = %d, want 1", depth)
	}

	cfg := config.Config{OperatorPaused: writeGateFile(t)}
	o := &orchestrator.Orchestrator{Cfg: cfg}
	if !o.Gated() {
		t.Fatal("precondition: must be gated")
	}

	var stderr strings.Builder
	runOneTick(context.Background(), cfg, o, svc, q, false, &stderr)

	if depth := q.DepthByType()["orphan"]; depth != 0 {
		t.Fatalf("gated tick must still run q.Expire(): depth = %d, want 0 (due event expired)", depth)
	}
}

// TestRunOneTick_gateNoticeOncePerTransition proves the stderr notice fires
// exactly on the gate-state TRANSITION — including startup-while-gated,
// since the very first call passes wasGated=false regardless of history —
// stays silent across repeat ticks in the SAME gated state, and fires again
// after a clear-and-reset (an ungated tick, then re-gated). RED against the
// pre-fix code: runOneTick/gateNotice did not exist (the old gated branch
// logged an unconditional per-tick slog.Info, never a stderr notice).
func TestRunOneTick_gateNoticeOncePerTransition(t *testing.T) {
	svc := &core.Service{}
	q, err := eventqueue.New(eventqueue.NewMemStore())
	if err != nil {
		t.Fatalf("eventqueue.New: %v", err)
	}
	gatedCfg := config.Config{OperatorPaused: writeGateFile(t)}
	gatedO := &orchestrator.Orchestrator{Cfg: gatedCfg}
	ungatedO := &orchestrator.Orchestrator{}

	const noticeMarker = "pg-router: gated by"
	var buf strings.Builder
	wasGated := false
	for i := 0; i < 3; i++ {
		wasGated = runOneTick(context.Background(), gatedCfg, gatedO, svc, q, wasGated, &buf)
	}
	if !wasGated {
		t.Fatal("after 3 gated ticks wasGated must be true")
	}
	if n := strings.Count(buf.String(), noticeMarker); n != 1 {
		t.Fatalf("notice count across 3 gated ticks = %d, want exactly 1; output=%q", n, buf.String())
	}

	// clear: one ungated tick resets the transition edge.
	wasGated = runOneTick(context.Background(), config.Config{}, ungatedO, svc, q, wasGated, &buf)
	if wasGated {
		t.Fatal("an ungated tick must report wasGated=false")
	}

	// reset: gate again — a FRESH transition, must notice again.
	wasGated = runOneTick(context.Background(), gatedCfg, gatedO, svc, q, wasGated, &buf)
	if !wasGated {
		t.Fatal("re-gated tick must report wasGated=true")
	}
	if n := strings.Count(buf.String(), noticeMarker); n != 2 {
		t.Fatalf("notice count after clear-and-reset = %d, want exactly 2 total; output=%q", n, buf.String())
	}
}
