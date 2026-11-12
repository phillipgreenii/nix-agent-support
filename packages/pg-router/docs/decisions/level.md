# Level — pg-router decision docs

Realization decisions about **levels and sensors**: why levels replace gates, the band edges,
hysteresis and dwell, the answer schema, the sensors module, the metric mapping and what happens to
a legacy gate at upgrade. The behavior side — resources, levels, sensors, requirements, admission,
staleness and gate removal — is `INV-LIFE-2`, `INV-LVL-1` to `INV-LVL-6`, `INV-SEN-1` to
`INV-SEN-7`, `INV-ADM-1` to `INV-ADM-8`, `INV-REQ-1`, `INV-STALE-1`, `INV-GATE-1` and `INV-ALR-1` in
pg-router's [behavior docs](../behavior/invariants.md). The record of the choice is
[ADR 0122](../../../../docs/adr/0122-pg-router-resources-have-levels-and-requirements-replace-gates.md).

### `DEC-LEVEL-1` — levels replace gates <!-- uuid: 629be3b2-5e0f-43ed-a67e-03227020d48d -->

**Decided** (operator, Phillip, 2026-11-09 to 2026-11-10; design under delegation 2026-11-10, which
the operator MAY veto).

**Why.** A gate was a named, ALL-CAPS, TYPE-keyed on/off record that a registration either blocked on
or listed as exempt. It had no scale, so "disk is tight" and "disk is exhausted" were two gates that
a watchdog had to sequence by hand; it had no staleness, so a dead setter held work forever until a
person cleared it; and a registration's exemption list named the conditions it did
NOT care about, which grows with every new gate. A **resource** has an ordered scale, a **sensor**
reports where it stands and owns its thresholds, and a registration says what it **requires**; an
unmentioned resource means "does not care". Enforcement starts at the first deploy (no observe
mode), and the levels and the readings behind them are exposed as metrics so the thresholds can be
tuned (design rulings R-1 to R-4).

**What is removed.** The Gate Registry, the gate TYPE, the external gate, the gate CLI verbs and
socket verbs, the gate schemas, metrics and records, the per-registration exemption list (its place is taken by the
absence of a `requires` clause for the resources a registration does not name), and the disk
watchdog with its timer. Nothing is kept as an alias and no period has gates and levels side by side:
the whole conversion lands at once (R-13, R-14).

**Term mapping** used to convert the gate wording of these docs: the retired set, clear and
list verbs become `level set`, `level clear` and `level list`; the global pause gate becomes the
`router` resource at level `PAUSED`; the disk-pressure gate becomes the `disk`, `cpu` and `ram`
resources; the tracker-server-down gate becomes `beads_<tracker>` at level `DOWN`.

**Rejected:** keeping gates and adding levels beside them (two mechanisms, two vocabularies, and a
period in which a consumer could name either); a compatibility alias for the retired verbs.

### `DEC-LEVEL-2` — band edges, hysteresis and dwell <!-- uuid: 8d413675-7193-40aa-96c3-00d4f726a014 -->

**Decided** (delegated, 2026-11-10; the operator approved the looser CPU and RAM bands on 2026-11-09
to 2026-11-10). The values are strawmen to tune after the first deploy, all configurable per sensor.

Scales: graded `PLENTY > OK > TIGHT > EXHAUSTED`; two-level `router` `RUNNING > PAUSED`,
`beads_<tracker>` `UP > DOWN`, reachability and credential resources `AVAILABLE > UNAVAILABLE`.

Each band INCLUDES its lower numeric bound and EXCLUDES its upper one, and a band written with a
strict `>` is read as `>=`, so a value exactly on a boundary belongs to the band that states it as its
lower bound:

| Resource                   | PLENTY     | OK                   | TIGHT                 | EXHAUSTED    |
| -------------------------- | ---------- | -------------------- | --------------------- | ------------ |
| `disk` (free GiB)          | `>= 40`    | `>= 25` and `< 40`   | `>= 12` and `< 25`    | `< 12`       |
| `cpu` (1-minute load/core) | `< 4`      | `>= 4` and `< 8`     | `>= 8` and `< 14`     | `>= 14`      |
| `ram` swap (GiB)           | `< 2`      | `>= 2` and `< 10`    | `>= 10` and `< 20`    | `>= 20`      |
| `ram` pressure             | normal (1) | warn (2)             | none                  | critical (4) |
| Claude pace                | `< 0.8`    | `>= 0.8` and `< 1.0` | `>= 1.0` and `< 1.25` | `>= 1.25`    |

The `ram` level is the WORST of the swap level and the pressure level; an unreadable pressure value is
no reading for that component, and with no readable component the sensor emits no `ram` answer. When
swap used is `>= 10 GiB` and the lowest free disk over the configured paths is `< 25 GiB`, `ram` is
lowered one more step (never below `EXHAUSTED`); the `ram` sensor reads the disk itself and does not
read the router's `disk` level (`INV-SEN-4`).

**Basis.** Load per core was 0.6 to 6.3 on an ordinary day and 7 to 26 during the 2026-11-03
incident, so routine load stays at `OK` or better and the incident range is `TIGHT` or `EXHAUSTED`;
the platform's memory-pressure "warn" occurs routinely, so it is `OK`, not `TIGHT`; swap cannot
exceed free disk, so pressure critical is the practical `EXHAUSTED` signal. The retired watchdog
tripped at 30 GiB and recovered at 37.5; this is a deliberate change. `>= 20` rather than `> 20`
because a half-open rule leaves exactly 20 in the more restrictive band.

**Hysteresis and dwell** (`INV-SEN-3`). A fall to a worse level is immediate when the reading enters
the worse band. A rise needs the reading to clear the boundary with margin: for higher-is-better
readings (disk) `>= boundary x 1.25`, for lower-is-better readings (load per core, swap) `<= boundary
x 0.8` (the factors the retired watchdog used), and for Claude pace a 0.05 recovery band. After the
margin is met, the rise still moves ONE level per dwell, 60 s by default, for credentials and
two-level scales too. A lost or unreadable dwell-state file starts the sensor at the WORST level of
the scale, rising one level per dwell. The router's own defaults: pull period 60 s, hard deadline
20 s, `valid_for_s` 180, grace factor 2, skew bound 5 minutes, start wait 10 s, start stagger 30 s,
refresh record every `valid_for_s / 2`.

### `DEC-LEVEL-3` — the answer schema and the push envelope <!-- uuid: 073d7647-42f3-463b-84e8-e0c5015f7a60 -->

**Decided** (delegated, 2026-11-10). One schema serves a pull answer and a push: `resource`,
`level`, optional `reading` (`{ value: number, unit: string }`), `observed_at`, `valid_for_s` and
`owner`; ONE JSON Schema file defines it, and the push request schema $refs it, so one schema validates both. The owner length 1 to 64 is the pattern `^.{1,64}$` (the schema validator has no minLength). Field names are snake_case on this wire although the other schemas are camelCase; that is accepted. A pull answer is JSONL, one record per resource, diverted
to level ingest (`INV-LVL-4`); a push is the `level-ingest`socket verb carrying the same record in
the usual`schemaVersion`envelope. The outcomes are`accepted`, `idempotent`(same owner, resource,
level and`observed_at`) and a rejection with a reason (`conflict`: same owner, resource and
`observed_at`with a different level;`older_than_held`; skew ahead of now; unknown resource or
level; owner not the resource's sensor or `operator`; malformed), each counted
(`pg_router_level_events_rejected`). `level-ingest`rejects an owner of`operator`with reason`not_registered_sensor`; any custom owner given to `level-set` counts as an operator source for precedence and compaction. The receipt sequence is the append order in the durable log; no
second counter is added. Staleness is strict (`INV-STALE-1`): stale when the evaluation time is
strictly after `expires_at`, or when `received_at - observed_at`is strictly greater than`valid_for_s`; a held level past `expires_at`stays in force until`expires_at`plus the grace
(factor x`valid_for_s`), then reads `unknown`. The answer schema version and the default period and
deadline per sensor remain tunable (open item).

### `DEC-LEVEL-4` — the sensors module layout and invocation <!-- uuid: 62f7f98c-4f73-424b-b48b-300ddec7d6e7 -->

**Decided** (delegated, 2026-11-10). The six sensors are ONE generic binary with subcommands (hosts,
remotes, tracker names and certificate margins are configuration, so the public code carries no
deployment name). The module directory is `packages/pg-router-sensors`, the Go module path
`github.com/phillipgreenii/pg-router-sensors`, the binary `pg-router-sensors` and the check
`pg-router-sensors-go-tests`; it mirrors the handlers and heartbeat modules.

Invocation: `pg-router-sensors <subcommand> [--config FILE] [--state-dir DIR] [--resource RESOURCE]`,
the subcommand one of `host-sensor` (resources `cpu`, `disk`, `ram`, `network`), `vpn-sensor` (the configured reachability and trust resources),
`git-auth-sensor`, `beads-sensor`, `claude-sensor`, `aws-sso-sensor` (resource `aws_sso_zr`); an unknown or missing subcommand prints usage to
stderr and exits 2, and a configuration file that cannot be loaded exits 2 with the cause on stderr. One invocation answers all the resources of that sensor, one JSONL line per
resource on stdout and diagnostics on stderr; `--resource` limits it to one resource (not `--only`, which clashes with pg-router's own `--only <kind>:<name>`). `--config` is a
TOML file (absent means built-in defaults); `--state-dir` defaults to
`$XDG_STATE_HOME/pg-router-sensors`, and the dwell state is `<state-dir>/<subcommand>.state.json`.
Exit 0 when at least one resource was answered, 1 when none could be measured (a pull failure for the
router, `INV-SEN-5`).

Probes: the network sensors send an HTTPS `HEAD` (5 s timeout per target, redirects not followed so a
3xx counts as reachable, and in the verifying mode (`host-sensor`'s `network`, and `vpn-sensor`'s trust resource) a DNS or TLS failure counts as unreachable, targets in parallel) and report
`UNAVAILABLE` only when ALL targets fail on 3 consecutive passes, the counter kept in the state file.
`vpn-sensor` answers TWO resources per invocation over the same hosts: the reachability resource (config
key `resource`) counts a target reachable when the connect, the TLS handshake and an HTTP response
complete even if the certificate does not verify (a private CA the machine does not trust is not the
VPN being down), and the trust resource (config key `trust_resource`, default `network_zr_trust`)
repeats the `HEAD` with normal verification against the system roots. Each has its own consecutive-failure
counter and dwell state; `--resource` limits the run to one. The skip-verify mode is an option of the
shared prober used only by the reachability resource: `host-sensor`'s `network` resource always verifies.
Accepted trade-off: the skip-verify reachability resource (`network_zr`) reads `AVAILABLE` for any HTTP
response below 500 from any TLS endpoint answering at the target name, a captive portal included; its
only guard is that the private hostname resolves. `network_zr_trust` is the trust signal. The two probes
of one invocation run concurrently, so a black-holed host costs one timeout, not two.
The credential sensor parses the validity of the ssh certificate selected by a configured key id or
principal (the latest expiry when several match), with a configurable margin (default 30 minutes) and
an optional bounded remote listing; a missing agent socket, no identity or an expired certificate is
`UNAVAILABLE`. The AWS SSO sensor reads only the expiry and refresh-token presence fields of the AWS CLI SSO cache JSON for a configured session name (`sso_session`, default `zip`; `cache_dir`, default `/.aws/sso/cache`), never a token value: it is `AVAILABLE` when the access token is unexpired, or a refresh token is present and `registrationExpiresAt` (when present) is in the future, with the same configurable margin; a missing cache or no matching file is `UNAVAILABLE`, not an error. The tracker sensor reads the exporter's up series per configured tracker and emits no
line for a tracker with no series. The Claude sensor reads the 5-hour block and the week from the
usage monitor's API and treats a capture older than 15 minutes as unknown. The sensors replace the
disk watchdog role, its timer and its package.

### `DEC-LEVEL-5` — the metric catalog mapping <!-- uuid: f4bb9b73-e0ff-4c0a-876f-d6b026e0cc9a -->

**Decided** (delegated, 2026-11-10). The gate metrics are replaced by the resource and sensor family
listed in `INV-OBS-1`: the active-gates gauge becomes `resource_level` (0 = best, with
`resource_scale_size`, so an alert can tell "below best" from "worst" on any scale) and `role_paused`;
the gate-blocked counter becomes `requirement_blocked` (labels role, resource and kind, with the
64-value fold); the gate-duration counter becomes `resource_level_seconds`; the counters of gates placed, cleared and
expired become `resource_changes`; the gate-drops counter becomes `requirement_drops`. New:
the age, lease, stale and override gauges, `resource_level_source` (sensor, operator or
`grace_fallback`), the numeric `resource_reading`, the sensor pull counters and the rejected-event
counter. Log lines carry the key `resource_level` (never `level`). Every alert, rule test, dashboard
panel and runbook that named a gate metric is converted in the same landing and a check enforces it
(`INV-ALR-1`): global-pause suppression keys only on the `router` resource at `PAUSED`, per-role
suppression on the role-paused metric. The `kind` label of the `requirement_blocked` series changes meaning from `dispatch`, `poll` or `pull` to `level`, `unknown` or `stale`; a dashboard or alert that groups or filters on it breaks and must be rewritten in the same landing. `sensor_pull_failures` keeps labels `sensor` and `reason` (`deadline`, `exit`, `malformed`, `rejected`; this is the pull-failure list, not the ingest-reject list). Gauge and counter constants omit the unit and `_total`; the exporter adds `_seconds` and `_total`.

### `DEC-LEVEL-6` — legacy gates at upgrade <!-- uuid: 86ef6b4c-0487-405a-abef-6b3dcbfcd094 -->

**Decided** (operator, Phillip, 2026-11-10, "approved as recommended"). The records the retired Gate
Registry wrote are ignored on replay and dropped by compaction; startup compaction runs before
replay, so replay never sees a legacy gate record; the compaction pass (or a scan immediately before
it) logs `legacy_gate_dropped` once per dropped ACTIVE gate (a latest record that is a set with no
expiry or an unlapsed expiry), carrying its type, owner and expiry, tied to the drop itself because
compaction on start is optional. There is NO translation code (R-14): the landing runbook
requires the list of active gates (read with the retired listing verb before it goes) to be empty, or recorded and re-applied as a `level set`, BEFORE the apply.
**Rejected:** translating a legacy gate into a level at replay, because the gate TYPEs have no
resource to map to and a compatibility path is exactly what the one-landing rule forbids.
