# Nudge delivery routed through the cmux-bridge

**Status**: Draft (decisions confirmed; pending implementation plan)
**Date**: 2026-07-07
**Deciders**: Phillip

## Context

The nudger delivers auto-resume and manual nudges by having the **daemon** shell
out to `cmux send` / `cmux send-key`, after enumerating surfaces with
`cmux --json top --processes` (`packages/pa-monitor/internal/signal/cmux.go`).
This cannot work: cmux is **configured to deny socket control to processes
outside the cmux process tree** — a deliberate operator setting; outside
processes MUST NOT control cmux. The daemon runs as a LaunchAgent, not a cmux
descendant (`darwin/modules/pa-monitor/default.nix`, `runAtLoad` + `keepAlive`),
so every `cmux` invocation from the daemon is rejected with `exit status 1`.

Evidence (live, 2026-07-07): `pa_monitor_signal_send_failures_total{reason="enumerate"}`
≈ 6372 — read from the local otel-stack's Prometheus, which is up and receiving
(the daemon's OTLP export was separately confirmed healthy this session) — with
error `cmux enumerate: cmux --json top --processes: exit status 1`; auto-resume
nudges recorded attempted-but-never-delivered (pg2-il6j). The clean
`exit status 1` (not the `signal: killed` timeout signature) is consistent with
a socket-access rejection rather than a hang.

The cmux-bridge exists because of this constraint: it runs inside each cmux
workspace (a cmux descendant), relays status to the pane, and registers with the
daemon (ADR 0016) — but plays no part in delivery. Separately, the daemon
resolves each bridge's cmux-server PID by running `ps` ancestry **synchronously
inside `RegisterBridge` on every 10s heartbeat**, exceeding the bridge's 2s
client deadline under load (`DeadlineExceeded` ×540; pg2-5ytu).

## Decision

The **daemon MUST NOT talk to cmux at all**; all cmux interaction moves into the
bridge, which becomes a per-cmux-server _delivery agent_. This is a deliberate
architectural boundary, not merely a bug workaround: cmux is locked to in-tree
processes by policy, so the "give the daemon the socket password" escape hatch is
explicitly rejected.

### 1. Route, don't execute

The daemon resolves the owning bridge and sends a `Deliver{target_pid, text}`
command over that bridge's connection; the bridge — a cmux descendant — resolves
the surface hosting that PID and emits `cmux send` / `send-key` locally, then
returns an ack/error. A **PID is the entire delivery payload**.

```mermaid
sequenceDiagram
    participant N as Nudger (daemon)
    participant R as Registry (daemon)
    participant B as cmux-bridge (cmux child)
    participant C as cmux server
    N->>R: bridge for target PID? (via server PID)
    alt bridge registered
        R-->>N: bridge stream
        N->>B: DaemonMsg{Deliver, id, target_pid, text}
        B->>C: cmux send / send-key (in-tree socket)
        C-->>B: ok
        B-->>N: BridgeMsg{DeliverResult, id, ok}
    else no bridge for that server
        N->>N: hold in short-lived queue; drop after retry window
    end
```

### 2. One bidirectional stream for all bridge↔daemon traffic

The bridge opens a single long-lived bidi stream that **subsumes its current use
of `WatchState` and the unary `RegisterBridge` heartbeat**:

```proto
rpc BridgeChannel(stream BridgeMsg) returns (stream DaemonMsg);
```

- `BridgeMsg` (bridge→daemon): `Register`, `Heartbeat`, `DeliverResult`.
- `DaemonMsg` (daemon→bridge): `StateSnapshot` (the roster/status the bridge
  renders on the pane), `Deliver` commands.

Both directions are correlated by an `id`. The **TUI and CLI keep the existing
`WatchState`/unary RPCs** (they receive no commands) — only the bridge needs the
bidi channel. The bridge MUST NOT expose its own gRPC listener; the reverse
command channel over the client-initiated stream subsumes it. (gRPC servers
cannot dial clients; a bidi stream is the sound way to push commands to a
client.)

### 3. The bridge owns session→surface mapping and all cmux calls

`cmux --json top --processes` enumerates the **entire cmux instance** (every
workspace), so this is the _same_ enumeration **relocated to where cmux permits
it**, not a smaller scan. Its value is correctness (it runs in-tree, where the
socket is allowed) and isolating cmux failures to the bridge, observable per
server.

### 4. Daemon registry via periodic discovery/reaper, routed by cmux server PID

```mermaid
flowchart LR
    subgraph daemon
      D["discovery/reaper loop<br/>(periodic; ps-ancestry, socket-free)"] -->|upsert / prune| REG[(bridge registry<br/>serverPID → {bridge streams}, cached)]
      H["BridgeChannel handler"] -->|register / heartbeat| REG
      RT["nudge router<br/>(targetPID → serverPID)"] -->|lookup| REG
    end
```

- A background loop (**not on any RPC path**) maintains the registry using `ps`
  ancestry (`FindCmuxServerAncestor` — socket-free, already wired via
  `CmuxAncestor`, `lifecycle.go:206-209`) and prunes dead bridges/servers;
  results cached. `RegisterBridge`'s synchronous `ps` is removed.
- The **routing key is the cmux server PID** (`target PID → server PID` via
  ps-ancestry → registered bridge). The session-env/workspace path is NOT used —
  it is unreliable (`sv.Env["CMUX_WORKSPACE_ID"]` is often unpopulated; the cause
  of pg2-0cmq #3). Any bridge on a given cmux server can deliver to any surface
  on that server, so **one live bridge per server suffices** for delivery.
- The registry today overwrites by server PID (`internal/bridge/registry.go:56-60`);
  with multiple per-workspace bridges under one server it MUST keep a **set** (or
  select a live member) rather than clobbering, so a live delivery target always
  exists.
- No live bridge for the target server → hold in the existing short-lived pending
  queue → drop after the retry window (record the drop).

### 5. Delivery is asynchronous end-to-end

The dispatcher's synchronous `Signaler.Send(pid, text)` (`dispatcher.go:170`)
becomes an async command/await-ack model for **all** signalers — cmux via the
bridge, tmux/ghostty/vscode via an in-daemon executor — behind one delivery
abstraction. This is a deliberate trade for a single code path over a
synchronous special case.

### Policies (normative)

- The daemon MUST NOT execute `cmux` commands (neither delivery nor enumeration).
- Delivery commands MUST be addressed by target PID; the bridge MUST resolve the
  surface locally and return a per-command ack/error.
- Bridge↔daemon MUST use one bridge-initiated bidi stream; the bridge MUST NOT
  open a listener.
- Routing MUST key on cmux server PID via ps-ancestry; session env MUST NOT be
  used for routing.
- On stream drop the daemon MUST treat unacked `Deliver` commands as failed and
  requeue them.
- The bridge MUST handle commands concurrently and ack out-of-order by `id` (no
  head-of-line blocking); the daemon MUST bound its per-bridge send queue
  (backpressure).
- The daemon MUST dedup/replace duplicate streams for the same server PID.
- On no available bridge the daemon MUST enqueue briefly, then drop.
- Non-cmux signalers MUST remain executed in the daemon and SHOULD adopt the same
  async model.

## Consequences

### Positive

- Dissolves the root cause of pg2-il6j: delivery runs in-tree, where cmux permits
  the socket; the daemon's `cmux` calls disappear entirely.
- Removes the per-heartbeat `ps` from `RegisterBridge` (pg2-5ytu): the registry is
  maintained out-of-band, and the long-lived stream removes the 2s unary deadline.
- Enforces the operator's cmux lockdown — no outside process ever touches cmux.
- Clean separation: daemon orchestrates/routes; bridge performs cmux mechanics.

### Negative

- Large change: a new bidi proto (folding in the bridge's `WatchState` +
  `RegisterBridge`), delivery logic moved into the bridge, an async nudger, and
  the registry/reaper rework.
- Delivery becomes best-effort/asynchronous for all signalers; the nudger's
  result handling changes.
- A cmux server with no live bridge cannot be nudged (acceptable — direct never
  worked, and bridges are being restarted onto the new binary).

### Neutral

- The bridge's `WatchState` + `RegisterBridge` collapse into `BridgeChannel`; the
  TUI and CLI are unchanged.
- The daemon retains `ps`-ancestry (for routing) and its non-cmux signal
  machinery, so it holds **less** cmux knowledge, not zero.
- Migration is handled operationally: the operator will restart all bridges onto
  the new binary, so no in-code delivery fallback is required.

## Alternatives Considered

### Password-direct (give the daemon `CMUX_SOCKET_PASSWORD` + `CMUX_SOCKET_PATH`)

Rejected **by policy**: cmux is deliberately configured to forbid control from
processes outside its tree. Even where technically possible, it violates the
intended security boundary. This is the decisive reason delivery must move to the
bridge rather than authorizing the daemon.

### Bridge exposes its own gRPC server the daemon dials

Rejected: two listeners, a daemon-reachable bridge socket, and endpoint
discovery. The bidi stream over the existing bridge→daemon connection provides
server-push with none of that.

### Daemon-side `ps` cache / async `RegisterBridge` (the narrow pg2-5ytu fix)

Insufficient: it fixes the `RegisterBridge` timeout, but the daemon still cannot
talk to cmux, so pg2-il6j remains. Subsumed by this decision.

## Related Decisions

- Extends `docs/adr/0016-pa-monitor-config-sourced-otel-and-connection-alert.md`
  (same subsystem): makes the bridge a delivery agent and folds its `WatchState`
  - `RegisterBridge` into one bidi `BridgeChannel`.
- Builds on `docs/adr/0011-pa-monitor-daemon-otel-split.md`.
- Resolves pg2-il6j (enumerate `exit status 1`) and pg2-5ytu (`RegisterBridge`
  `ps` timeout). Does **not** resolve pg2-0cmq (manual-nudge no-op = session-id
  churn + working-state suppression + unreliable env selector), which is a
  separate matching/UX issue and remains open.
