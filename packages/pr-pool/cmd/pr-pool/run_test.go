package main

import (
	"context"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/phillipgreenii/pr-pool/internal/activity"
	"github.com/phillipgreenii/pr-pool/internal/config"
	"github.com/phillipgreenii/pr-pool/internal/core"
	"github.com/phillipgreenii/pr-pool/internal/dtest"
	"github.com/phillipgreenii/pr-pool/internal/event"
	"github.com/phillipgreenii/pr-pool/internal/eventqueue"
	"github.com/phillipgreenii/pr-pool/internal/item"
	"github.com/phillipgreenii/pr-pool/internal/orchestrator"
	"github.com/phillipgreenii/pr-pool/internal/query"
	"github.com/phillipgreenii/pr-pool/internal/roles"
)

// declaredBindTypes must still count a role's Binds even when that role is
// disabled BY A RUN-SCOPED SELECTOR (applySelectors flips Enabled, never
// touches Binds): the "declared but inactive this run" half of INV-DISP-3
// depends on this — a selector-excluded role must still count as a DECLARED
// binding, never as if it were never configured at all.
func TestDeclaredBindTypes_selectorDisabledRoleStillCounts(t *testing.T) {
	rs := roles.RoleSet{
		{Name: "r1", Enabled: true, Binds: []string{"t1"}},
		{Name: "r2", Enabled: false, Binds: []string{"t2"}}, // as applySelectors would leave a --disable'd role
	}
	got := declaredBindTypes(rs)
	want := []string{"t1", "t2"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("declaredBindTypes = %v, want %v (a run-scoped exclusion must not drop a type from the declared set)", got, want)
	}
}

// fakeCommander is a query.Commander stand-in that records every argv it is
// asked to run, so a "command"-type role's dispatch is observable without
// shelling out to a real executable. bootCore/queue.Dispatch tests below drive
// the offer phase SEQUENTIALLY (queue.Dispatch's phase 2 is a plain for-loop,
// not concurrent goroutines — internal/eventqueue/queue.go), so no locking is
// needed here.
type fakeCommander struct{ calls [][]string }

func (f *fakeCommander) Run(_ context.Context, argv []string) ([]byte, error) {
	f.calls = append(f.calls, argv)
	return nil, nil
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
	cmd := &fakeCommander{}
	cfg := config.Config{
		LogDir: shortDir(t), // AF_UNIX path length cap; see shortDir's doc (ingest_event_test.go)
		Roles: roles.RoleSet{
			{Name: "r1", Enabled: true, Type: "command", Binds: []string{"t1"}, Command: &roles.CommandConfig{Argv: []string{"r1-cmd"}}},
			{Name: "r2", Enabled: true, Type: "command", Binds: []string{"t2"}, Command: &roles.CommandConfig{Argv: []string{"r2-cmd"}}},
		},
	}
	cfg, err := applySelectors(cfg, runSelectors{Disable: []string{"role:r2"}})
	if err != nil {
		t.Fatalf("applySelectors: %v", err)
	}
	if roleEnabled(cfg.Roles, "r2") {
		t.Fatalf("precondition: r2 must be selector-disabled")
	}
	if !roleEnabled(cfg.Roles, "r1") {
		t.Fatalf("precondition: r1 must remain enabled")
	}

	// BD must be a non-nil beads.Runner: the dispatched role's Offer path calls
	// o.snapshotIDs/o.buildResult (a "created beads" diff + final status read),
	// which shell out through it. Give it a status for the one item ("bd-t1")
	// r1's dispatch will actually reach, so beads.Status finds a real entry
	// rather than indexing an empty per-id sequence.
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"bd-t1": {"open"}}}
	o := &orchestrator.Orchestrator{Cfg: cfg, Cmd: cmd, BD: bd}
	ctx := context.Background()
	svc, q, _, storeClose, err := bootCore(ctx, cfg, o)
	if err != nil {
		t.Fatalf("bootCore: %v", err)
	}
	defer func() { _ = storeClose() }()
	defer func() { _ = svc.Close() }()

	future := time.Now().Add(time.Hour)
	// Payload carries the dispatch item (discover.ItemFromPayload's "item" key)
	// so the dispatched role's Offer resolves a real bead id, matching bd's
	// StatusSeq above, instead of "".
	payloadFor := func(typ string) map[string]any {
		return map[string]any{"item": map[string]any{"id": "bd-" + typ, "type": "task"}}
	}
	for _, typ := range []string{"t1", "t2"} {
		if _, err := q.Enqueue(eventqueue.Event{ID: "ev-" + typ, Type: typ, ExpiresAt: future, Payload: payloadFor(typ)}); err != nil {
			t.Fatalf("enqueue %s: %v", typ, err)
		}
	}
	q.Dispatch()

	if len(cmd.calls) != 1 {
		t.Fatalf("commander calls = %v, want exactly 1 (r1 only; r2 has no registered listener so its event is never offered)", cmd.calls)
	}
	if cmd.calls[0][0] != "r1-cmd" {
		t.Errorf("dispatched argv = %v, want r1's command", cmd.calls[0])
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
	cfg, err := applySelectors(cfg, runSelectors{Disable: []string{"query:q2"}})
	if err != nil {
		t.Fatalf("applySelectors: %v", err)
	}
	if len(cfg.Queries) != 1 || cfg.Queries[0].Name != "q1" {
		t.Fatalf("cfg.Queries = %v, want only q1", queryNames(cfg.Queries))
	}

	o := &orchestrator.Orchestrator{Cfg: cfg}
	queue, err := eventqueue.New(eventqueue.NewMemStore())
	if err != nil {
		t.Fatalf("eventqueue.New: %v", err)
	}
	if err := o.ProduceTick(context.Background(), queue); err != nil {
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

func (*recordingDispatchFailureObserver) OnEnqueue(eventqueue.Event) {}
func (*recordingDispatchFailureObserver) OnAccept(string, string)    {}
func (*recordingDispatchFailureObserver) OnUnconsumedExpired(string) {}
func (*recordingDispatchFailureObserver) OnDeclined(string)          {}
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
// and that an empty set produces nil, not an empty non-nil slice.
func TestSourceReportsFor_oneReportPerActiveSource(t *testing.T) {
	got := sourceReportsFor(query.SourceSet{{Name: "beads-ready"}, {Name: "e2e-source"}})
	want := []core.SourceReport{{Name: "beads-ready"}, {Name: "e2e-source"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sourceReportsFor = %+v, want %+v", got, want)
	}

	if got := sourceReportsFor(nil); got != nil {
		t.Fatalf("sourceReportsFor(nil) = %+v, want nil", got)
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

	present := dir + "/quota-paused"
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
// gateQuotaPaused/gateCICDDown keys svc.ObserveGateFromTick's caller and,
// eventually, Task 3.9's socket verbs must agree on.
func TestCurrentGateFiles_namesBothFileDirectGates(t *testing.T) {
	dir := t.TempDir()
	quota := dir + "/quota-paused"
	if err := os.WriteFile(quota, nil, 0o644); err != nil {
		t.Fatalf("write gate file: %v", err)
	}
	cfg := config.Config{QuotaPaused: quota, CICDDown: dir + "/cicd-down-absent"}

	gates := currentGateFiles(cfg)
	if got := gates[gateQuotaPaused]; !got.Set {
		t.Fatalf("gates[%q] = %+v, want Set=true", gateQuotaPaused, got)
	}
	if got := gates[gateCICDDown]; got.Set {
		t.Fatalf("gates[%q] = %+v, want unset (file absent)", gateCICDDown, got)
	}
}
