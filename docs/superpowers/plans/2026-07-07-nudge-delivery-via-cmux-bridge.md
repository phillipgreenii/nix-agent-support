# Nudge Delivery via cmux-bridge — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move all cmux interaction out of the daemon and into the cmux-bridge, so nudges are delivered by the in-tree bridge (which can use cmux's socket) instead of the daemon (which cannot).

**Architecture:** The daemon never calls `cmux`. It routes `Deliver{target_pid}` commands to the owning bridge over a single bridge-initiated gRPC **bidirectional** stream (`BridgeChannel`) that also carries the state snapshots the bridge renders and the bridge's register/heartbeat/acks. The daemon maintains a bridge registry keyed by **cmux server PID** (resolved via socket-free `ps`-ancestry) using a periodic discovery/reaper loop decoupled from the RPC path. Delivery is asynchronous end-to-end.

**Tech Stack:** Go 1.25, gRPC over a unix socket, protobuf (regenerated via `nix run .#pa-monitor-codegen`), existing `internal/signal`, `internal/bridge` registry, `internal/daemon/nudger` dispatcher.

**Authoritative design:** `docs/adr/draft-nudge-delivery-via-cmux-bridge.md`.

## Global Constraints

- The daemon MUST NOT execute `cmux` (delivery or enumeration). `FindCmuxServerAncestor` (ps-based, socket-free) MAY still be used for routing; `Send`/`enumerateSurfaces` MUST NOT be called by the daemon.
- Bridge↔daemon MUST use one bridge-initiated bidi stream; the bridge MUST NOT open a listener.
- Routing MUST key on cmux server PID via ps-ancestry; session env MUST NOT be used for routing.
- **Package placement (avoids an import cycle):** `daemon` imports `nudger`, never the reverse (see `lifecycle.go:23`). So the `Deliverer` **interface MUST be declared in package `nudger`** (like `Signaler`, `dispatcher.go:16`); the concrete `bridgeDeliverer`/`inDaemonDeliverer`/`compositeDeliverer` live in package `daemon` (like `SignalerAdapter`, `nudger_signaler.go:11`).
- **The build MUST stay green after every task.** Where a later task changes a signature, the introducing task adds a back-compatible shim so intervening tasks compile.
- Proto regeneration MUST use `nix run .#pa-monitor-codegen`.
- Every task ends green on `go test -C packages/pa-monitor ./...` and `pre-commit run --all-files`; the final task also on `nix flake check`.
- TDD: no production code without a failing test first. Commit per task.
- The TUI and CLI keep the existing `WatchState`/unary RPCs unchanged.

---

## File Structure

- `internal/proto/pa_monitor.proto` — add `BridgeChannel` bidi RPC + `BridgeMsg`/`DaemonMsg` messages (regen).
- `internal/bridge/registry.go` — hold a **set** of live bridge entries per server PID; retain per-entry `lastSeen` + `StatusForServer`; add stream `send` hook + `Prune`.
- `internal/daemon/discovery.go` _(new)_ — periodic reaper (prune dead PIDs).
- `internal/daemon/bridge_channel.go` _(new)_ — `BridgeChannel` handler: register/heartbeat/ack, bounded per-stream writer, per-stream snapshot ticker, dedup.
- `internal/daemon/delivery.go` _(new)_ — concrete `bridgeDeliverer`, `inDaemonDeliverer`, `compositeDeliverer`, ack `tracker` (all package `daemon`).
- `internal/daemon/nudger/dispatcher.go` — hold a `nudger.Deliverer`; async deliver + drop-after-window.
- `internal/daemon/nudger/deliverer.go` _(new)_ — `Deliverer` interface (package `nudger`).
- `internal/daemon/nudger/nudger.go` — `New` wraps the incoming `Signaler` in a default deliverer (green shim) in Task 6; takes the composite `Deliverer` in Task 8.
- `cmd/pa-monitor/cmux_bridge.go` — replace `WatchState`+unary `RegisterBridge` with `BridgeChannel`; concurrent `Deliver` handler.
- `internal/daemon/server.go` — drop the synchronous `ps` from `RegisterBridge` (keep it as a no-op shim).
- `internal/otel/emitter.go`, `internal/daemon/nudger_runtime.go` — delivery counters incl. `RecordDroppedNoBridge`.
- `docs/adr/` — accept ADR as `0022-…`, update `index.md`.

---

## Task 1: Proto — `BridgeChannel` bidi RPC + messages

**Files:** Modify `internal/proto/pa_monitor.proto`; regen `*.pb.go`; Test `internal/proto/bridge_channel_roundtrip_test.go`.

**Produces:** `rpc BridgeChannel(stream BridgeMsg) returns (stream DaemonMsg)`; `BridgeMsg` oneof `{Register, Heartbeat, DeliverResult}`; `DaemonMsg` oneof `{DaemonState snapshot, Deliver deliver}` (reuses existing `DaemonState`); `Register{bridge_pid, server_pid, workspace_id}`, `Heartbeat{bridge_pid}`, `Deliver{id, target_pid, text}`, `DeliverResult{id, ok, error}`.

- [ ] **Step 1:** Add the RPC + messages (proto snippet below). Reuse `DaemonState` (WatchState's return) inside `DaemonMsg` — legal proto3.

```proto
rpc BridgeChannel(stream BridgeMsg) returns (stream DaemonMsg);
message BridgeMsg { oneof kind { Register register = 1; Heartbeat heartbeat = 2; DeliverResult result = 3; } }
message DaemonMsg { oneof kind { DaemonState snapshot = 1; Deliver deliver = 2; } }
message Register { int32 bridge_pid = 1; int32 server_pid = 2; string workspace_id = 3; }
message Heartbeat { int32 bridge_pid = 1; }
message Deliver { string id = 1; int32 target_pid = 2; string text = 3; }
message DeliverResult { string id = 1; bool ok = 2; string error = 3; }
```

- [ ] **Step 2:** `nix run .#pa-monitor-codegen` → `.pb.go` regenerated (yields `grpc.BidiStreamingServer[BridgeMsg,DaemonMsg]`).
- [ ] **Step 3:** Failing roundtrip test (package `proto`, unaliased `google.golang.org/protobuf/proto`, matching `caffeinate_roundtrip_test.go`):

```go
func TestDaemonMsgDeliverRoundTrip(t *testing.T) {
    in := &DaemonMsg{Kind: &DaemonMsg_Deliver{Deliver: &Deliver{Id: "c1", TargetPid: 4321, Text: "continue"}}}
    b, err := proto.Marshal(in); if err != nil { t.Fatal(err) }
    var out DaemonMsg; if err := proto.Unmarshal(b, &out); err != nil { t.Fatal(err) }
    if d := out.GetDeliver(); d == nil || d.GetId() != "c1" || d.GetTargetPid() != 4321 { t.Fatalf("got %+v", &out) }
}
```

- [ ] **Step 4:** `go test -C packages/pa-monitor ./internal/proto/ -run RoundTrip -v` → PASS.
- [ ] **Step 5:** Commit `feat(proto): add BridgeChannel bidi stream`.

---

## Task 2: Registry — set of live bridges per server PID (build-green + Stale preserved)

**Files:** Modify `internal/bridge/registry.go`; Test `internal/bridge/registry_test.go`.

**Consumes:** current `registry.go` (read first: keys by server PID, overwrites `:53-60`; `StatusForServer` returns Alive/Stale/Unknown from `lastSeen`+`staleAfter` `:39,71-82`; sole external callers: `server.go:353` `Register(serverPID)`, `poller.go:515` `StatusForServer`).

**Produces:**

- **Keep** `Register(serverPID int)` working (back-compat — `server.go` still calls it until Task 8). It records/refreshes a display-only entry.
- Add `AttachStream(serverPID, bridgePID int, send func(*pb.DaemonMsg) error)` — adds/updates a set member carrying the send hook.
- Add `Heartbeat(serverPID, bridgePID int, at time.Time)` — refreshes that member's `lastSeen`.
- Add `LiveBridge(serverPID int) (*BridgeEntry, bool)` — a member with a non-nil `send` and fresh `lastSeen`.
- Add `Deregister(serverPID, bridgePID int)` and `Prune(isAlive func(pid int) bool)`.
- `BridgeEntry{ BridgePID int; send func(*pb.DaemonMsg) error; lastSeen time.Time }`. Internally `map[int]map[int]*BridgeEntry` (serverPID → bridgePID → entry) under the existing mutex. `StatusForServer` keeps its Alive/Stale/Unknown contract, now computed over the freshest member's `lastSeen`.

- [ ] **Step 1:** Failing test — two bridges on one server retained; `StatusForServer` still returns Stale when the freshest `lastSeen` ages past `staleAfter`; `Prune` drops dead PIDs; `LiveBridge` returns a member with a `send`.
- [ ] **Step 2:** Run → FAIL.
- [ ] **Step 3:** Implement the set model; keep `Register`/`StatusForServer` behavior; add the new methods. Verify `server.go:353` and `poller.go:515` still compile unchanged.
- [ ] **Step 4:** `go test -C packages/pa-monitor ./internal/bridge/ -v` → PASS (existing registry tests green).
- [ ] **Step 5:** Commit `refactor(bridge): set of live bridges per server PID; retain Stale status`.

---

## Task 3: Discovery/reaper loop

**Files:** Create `internal/daemon/discovery.go`; Test `internal/daemon/discovery_test.go`.

**Produces:** `reapOnce(reg *bridge.Registry, isAlive func(int) bool)`; `RunReaper(ctx, reg, interval, isAlive)`.

- [ ] **Step 1:** Failing test — register two bridges, `isAlive` reports one dead, `reapOnce` prunes it (assert via `LiveBridge`/`StatusForServer`).
- [ ] **Step 2:** Run → FAIL.
- [ ] **Step 3:** Implement `reapOnce` + `RunReaper` ticking loop honoring `ctx`.
- [ ] **Step 4:** `go test -C packages/pa-monitor ./internal/daemon/ -run Reaper -v` → PASS.
- [ ] **Step 5:** Commit `feat(daemon): periodic bridge reaper`. _(Wired into `RunWith` in Task 8.)_

---

## Task 4: `BridgeChannel` handler (register + bounded push + snapshot ticker + dedup)

**Files:** Create `internal/daemon/bridge_channel.go`; Test `internal/daemon/bridge_channel_test.go`.

**Produces:** `func (s *server) BridgeChannel(stream pb.PaMonitor_BridgeChannelServer) error`. On first `Register`: `AttachStream(serverPID, bridgePID, send)` where `send` enqueues onto a **bounded** `chan *pb.DaemonMsg` drained by a writer goroutine (`stream.Send`). Start a **per-stream ~2s ticker** that enqueues `DaemonMsg{snapshot: s.buildState()}` (this is the roster the bridge renders — replaces the old `WatchState` push cadence, `server.go:99-128`). On `Heartbeat`: `reg.Heartbeat(...)`. On `DeliverResult`: `s.tracker.resolve(id, ok, err)` (Task 5). On stream end/ctx-done: `Deregister` + `s.tracker.failAllForStream(streamID)`. Dedup: replacing `(serverPID,bridgePID)` closes the old writer.

- [ ] **Step 1:** Failing test — fake `pb.PaMonitor_BridgeChannelServer` (embed `grpc.ServerStream`; `Send`/`Recv`/`Context` over channels). Send `Register`; assert (a) registry gains a live bridge, (b) within one tick a `DaemonMsg.snapshot` arrives at the fake's Send side, (c) a `DaemonMsg.deliver` pushed via the entry's `send` reaches Send. Close Recv → assert `Deregister`.
- [ ] **Step 2:** Run → FAIL.
- [ ] **Step 3:** Implement reader loop (dispatch by oneof) + bounded writer goroutine (backpressure: block-with-timeout, drop-oldest for snapshots) + per-stream snapshot ticker + dedup.
- [ ] **Step 4:** `go test -C packages/pa-monitor ./internal/daemon/ -run BridgeChannel -v` → PASS.
- [ ] **Step 5:** Commit `feat(daemon): BridgeChannel handler with snapshot push, bounded writer, dedup`.

---

## Task 5: Deliverer interface (nudger) + concrete deliverers + composite router (daemon)

**Files:** Create `internal/daemon/nudger/deliverer.go` (interface); Create `internal/daemon/delivery.go` (concrete, package `daemon`); Test `internal/daemon/delivery_test.go`.

**Produces:**

- `internal/daemon/nudger/deliverer.go`: `type Deliverer interface { Deliver(ctx context.Context, targetPID int, text string) error }`; exported sentinel `var ErrNoBridge = errors.New("no bridge for target")`.
- `internal/daemon/delivery.go` (package `daemon`): `tracker` (`id→chan result`, `resolve`, `failAllForStream`); `bridgeDeliverer{reg, ancestor func(int)(int,bool), tracker, timeout}`; `inDaemonDeliverer{signal nudger.Signaler}` (wraps the existing sync `Send` behind `Deliver`); and `compositeDeliverer{ancestor, bridge, inDaemon}` whose `Deliver` routes: `if _, ok := ancestor(pid); ok { bridge.Deliver } else { inDaemon.Deliver }`. All three satisfy `nudger.Deliverer`.
- `bridgeDeliverer.Deliver`: `serverPID,ok := ancestor(pid)`; `entry,ok := reg.LiveBridge(serverPID)` else `ErrNoBridge`; new `id`; `entry.send(&pb.DaemonMsg{Deliver{id,pid,text}})`; await `tracker` result or `timeout`.

- [ ] **Step 1:** Failing tests — (a) fake registry+entry whose `send` immediately `tracker.resolve(id,true,"")` → `bridgeDeliverer.Deliver` returns nil, recorded cmd has right pid/text; (b) `LiveBridge` false → `ErrNoBridge`; (c) `send` never resolves → timeout error; (d) `compositeDeliverer` with `ancestor` returning false routes to a fake `inDaemonDeliverer`.
- [ ] **Step 2:** Run → FAIL.
- [ ] **Step 3:** Implement interface + `tracker` + three deliverers.
- [ ] **Step 4:** `go test -C packages/pa-monitor ./internal/daemon/ -run Deliver -v` and `./internal/daemon/nudger/ -v` → PASS.
- [ ] **Step 5:** Commit `feat(daemon): async Deliverer (bridge/in-daemon/composite) + ack tracker`.

---

## Task 6: Dispatcher delivers via `nudger.Deliverer` + drop-after-window

**Files:** Modify `internal/daemon/nudger/dispatcher.go`, `internal/daemon/nudger/nudger.go`, `internal/daemon/nudger/dispatcher_test.go`. (Adds `nudger.go` to keep the build green — the review flagged it.)

**Behavior:**

- `Dispatcher` gains a `Deliverer` field. `nudger.New(signaler, recorder, nudgeRecorder, historyErrLog)` **keeps its current signature**; internally it wraps `signaler` in a trivial in-package `signalerDeliverer{signaler}` (sync adapter) and sets `dispatcher.Deliverer` to it — so `lifecycle.go` compiles unchanged until Task 8.
- At the send site (`dispatcher.go:162-176`): call `d.Deliverer.Deliver(goCtx, view.PID, text)`. `nil` → existing sent path (`RecordSent`+`UpdateWatermarks`+sent history). `ErrNoBridge` → keep the intent queued **unless** `ctx.Now - intent.EmittedAt > dropWindow` (new const, e.g. 60s), in which case remove the keys, call new `Recorder.RecordDroppedNoBridge(sid, sources)`, and record a `dropped` history row. Any other error → existing failed path (`RecordSendFailed` + failed `recordHistory` + `HistoryErrLog` from pg2-dozn). Preserve the disrupt-attempt watermark.
- Add `RecordDroppedNoBridge(sid string, sources []Source)` to the `Recorder` interface + `fakeRecorder` + `WatermarkStore` impl (emits the Task 9 counter).

- [ ] **Step 1:** Failing tests (extend `dispatcher_test.go` fakes with a fake `Deliverer`): nil → `RecordSent`; `ErrNoBridge` with fresh `EmittedAt` → intent retained, no drop; `ErrNoBridge` with `EmittedAt` older than `dropWindow` → keys removed + `RecordDroppedNoBridge` + `dropped` history; generic error → `RecordSendFailed` + failed history.
- [ ] **Step 2:** Run → FAIL.
- [ ] **Step 3:** Implement the `Deliverer` field, the `signalerDeliverer` shim in `nudger.go`, the branch logic, and the `RecordDroppedNoBridge` hook (interface + fake + WatermarkStore).
- [ ] **Step 4:** `go test -C packages/pa-monitor ./internal/daemon/nudger/ ./internal/daemon/ -v` → PASS (all existing dispatcher tests green; `lifecycle.go` still builds).
- [ ] **Step 5:** Commit `refactor(nudger): async Deliverer dispatch + drop-after-window`.

---

## Task 7: Bridge speaks `BridgeChannel` + delivers locally

**Files:** Modify `cmd/pa-monitor/cmux_bridge.go` (replace `streamOnce`'s `WatchState`+`registerBridge`, `:271-345`); Test `cmd/pa-monitor/cmux_bridge_test.go`.

**Behavior:** open `BridgeChannel`; send `Register{bridge_pid, server_pid, workspace_id}` with `server_pid = FindCmuxServerAncestor(os.Getpid())` resolved once at startup **with retry until non-zero** (never freeze 0); periodic `Heartbeat`. Consume `DaemonMsg.snapshot` exactly as the old `WatchState` message drove `reporter.Push(...)`. On `DaemonMsg.deliver`: dispatch to a **handler goroutine** (concurrent, no HOL), resolve the surface for `target_pid` via a local `CmuxSignaler` (in-tree socket), run send/send-key, reply `BridgeMsg{DeliverResult{id, ok, error}}`.

- [ ] **Step 1:** Failing test — injected fake `BridgeChannel` client stream + fake cmux runner (reuse `fakeCmuxRun` pattern from `internal/signal/cmux_test.go`). Feed a `Deliver`; assert (a) `cmux send`+`send-key` run for the target and (b) `DeliverResult{ok:true,id}` sent. Feed a `Deliver` whose resolution fails → `DeliverResult{ok:false}` with error. Assert `snapshot` still drives `reporter.Push`.
- [ ] **Step 2:** Run → FAIL.
- [ ] **Step 3:** Implement the `BridgeChannel` loop + concurrent deliver handler + local send. _(Depends on Task 4's snapshot ticker existing so the pane keeps updating.)_
- [ ] **Step 4:** `go test -C packages/pa-monitor ./cmd/pa-monitor/ -run Bridge -v` → PASS.
- [ ] **Step 5:** Commit `feat(bridge): deliver over BridgeChannel; drop WatchState/RegisterBridge use`.

---

## Task 8: Wire composite Deliverer + reaper; retire daemon cmux delivery

**Files:** Modify `internal/daemon/lifecycle.go` (`RunWith`), `internal/daemon/nudger/nudger.go` (`New` signature), `internal/daemon/server.go` (`RegisterBridge` shim); Tests `server_test.go` + a new non-cmux delivery test.

**Behavior:**

- Change `nudger.New` to accept a `Deliverer` (drop the `signaler` param + the `signalerDeliverer` shim from Task 6). In `RunWith`, build `compositeDeliverer{ancestor: opts.CmuxAncestor, bridge: bridgeDeliverer{reg, tracker}, inDaemon: inDaemonDeliverer{SignalerAdapter for tmux/ghostty/vscode}}` and pass it in. Start `RunReaper(ctx, reg, interval, pidAlive)`.
- The daemon no longer constructs a cmux `Signaler` for delivery (keeps `FindCmuxServerAncestor` for routing via `opts.CmuxAncestor`, already in scope).
- `RegisterBridge` RPC: keep as a **no-op shim** returning `RegisterBridgeResponse{}` (the bridge stopped calling it in Task 7; leaving the RPC avoids a proto-removal codegen pass). Remove its synchronous `ps` walk.

- [ ] **Step 1:** Failing tests — (a) dispatching for a cmux-hosted session (fake `ancestor` resolves) pushes a `Deliver` to the registered in-memory bridge and invokes **no** `cmux` subprocess in the daemon; (b) **regression:** dispatching for a non-cmux session (`ancestor` returns false) routes through `inDaemonDeliverer` and calls the tmux/ghostty/vscode `Signaler.Send`.
- [ ] **Step 2:** Run → FAIL.
- [ ] **Step 3:** Implement wiring; change `New`; add the `RegisterBridge` shim; strip cmux `Signaler` from delivery.
- [ ] **Step 4:** `go test -C packages/pa-monitor ./...` → PASS.
- [ ] **Step 5:** Commit `refactor(daemon): route cmux via bridge, keep non-cmux in-daemon; RegisterBridge shim`.

---

## Task 9: Telemetry, ADR acceptance, docs, full gates

**Files:** Modify `internal/otel/emitter.go` + `internal/daemon/nudger_runtime.go` (counters `nudge.deliver_sent`, `nudge.deliver_failed{reason}`, `nudge.deliver_dropped_no_bridge` — the last emitted from `RecordDroppedNoBridge`, Task 6); rename `docs/adr/draft-…` → `0022-nudge-delivery-via-cmux-bridge.md` (Status: Accepted) + add `index.md` row; update `packages/pa-monitor/README.md`.

- [ ] **Step 1:** TDD the counters via the emitter's existing test pattern; emit from the dispatcher branches (sent/failed/dropped).
- [ ] **Step 2:** Rename ADR to `0022-…`, flip to Accepted, add `index.md` row (2026-07-07).
- [ ] **Step 3:** README delivery-model section.
- [ ] **Step 4:** Full gates: `go test -C packages/pa-monitor ./...`; `pre-commit run --all-files`; `nix flake check` → all pass.
- [ ] **Step 5:** Commit `feat(pa-monitor): delivery telemetry + accept ADR 0022`.

---

## Self-Review notes

- **Import cycle** resolved: `Deliverer` in package `nudger` (Task 5); concrete impls in `daemon`.
- **Build-green invariants:** Task 2 keeps `Register(serverPID)`; Task 6 keeps `nudger.New`'s signature via a shim; Task 8 makes the signature change + retires the shim in one step.
- **Snapshot cadence** owned by Task 4 (per-stream ticker) — the review's dark-pane gap.
- **Net-new logic made explicit:** drop-after-window + `RecordDroppedNoBridge` (Task 6/9); composite router (Task 5); `RegisterBridge` no-op shim (Task 8).
- **Stale display** preserved: `BridgeEntry.lastSeen` + `StatusForServer` retained (Task 2).
- **Non-cmux regression** covered (Task 8 Step 1b). **TUI/CLI `WatchState`** untouched.
- **Out of scope (separate):** pg2-0cmq (manual-nudge matching/UX).
- **Directed (read current signatures at implement time):** the exact `streamOnce` body (Task 7), `registry.go` internals (Task 2), `nudger.New`/`SignalerAdapter` wiring (Tasks 6,8).
