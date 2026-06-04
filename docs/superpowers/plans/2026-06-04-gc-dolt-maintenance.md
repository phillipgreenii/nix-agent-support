# Dolt Maintenance (breaker + self-gating flatten + OTel + dashboard) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. ALSO REQUIRED: the `bash-scripting` skill (mkBashBuilders is the authoritative framework for every script/library here).

**Goal:** Replace today's hand-rolled dolt-maintenance scripts with nix-managed, self-gating maintenance (durable import breaker + hourly stats-purge/GC + gated history flatten), emitting OTLP metrics+logs, plus a Grafana dashboard modeled on the gascity one.

**Architecture:** Two `mkBashScript` commands (`gc-bd-import-breaker`, `gc-dolt-maintenance`) + two `mkBashLibrary` libs (`gc-otlp-emit`, `gc-dolt-maintenance-lib`) in `phillipgreenii-nix-agent-support/packages/`. A **darwin** module registers the hourly LaunchAgent (via `phillipgreenii.system.launchdServices.userAgents`, ADR 0049) and the Grafana dashboard (`phillipgreenii.observability.dashboardProviders`). A **home** module installs the commands and runs the breaker via `home.activation`. Telemetry is best-effort `curl`→OTLP/JSON (no Go SDK in bash). Reference spec: `docs/superpowers/specs/2026-06-04-gc-dolt-maintenance-design.md`.

**Tech Stack:** Nix (mkBashBuilders, nix-darwin, home-manager), bash, bats, jq, curl, dolt, gc, Grafana/OTLP.

**House patterns to mirror (read these first):**

- mkBashScript pkg: `packages/wait-for-agents/wait-for-agents-to-finish/default.nix`
- mkBashLibrary: `packages/agent-activity/lib/default.nix`
- module-style pkg w/ lib+scripts + flake registration: `packages/agent-activity/` + `flake.nix` (the `agent-activity = let result = import ./packages/agent-activity { inherit bashBuilders pkgs …; }; …`)
- launchd + dashboard in darwin: `darwin/modules/pa-monitor/default.nix`
- raw user agent shape: `darwin/services/beads-web/default.nix`
- bash-script HM install + "no launchd.agents in HM" rule: `home/programs/pa-monitor/default.nix`
- dashboard JSON template: `packages/pa-monitor/grafana/pa-monitor-overview.json` and `phillipg-nix-ziprecruiter/darwin/modules/gascity/grafana/gascity-overview.json`

---

## File Structure

```
packages/gc-dolt-maintenance/
├── default.nix                       # imports the 4 sub-packages, returns { breaker, maintenance, libs }
├── otlp-emit/
│   ├── default.nix                   # mkBashLibrary gc-otlp-emit
│   ├── gc-otlp-emit.bash             # otlp_gauge / otlp_log (curl→OTLP/JSON, best-effort)
│   └── tests/test-gc-otlp-emit.bats
├── decision/
│   ├── default.nix                   # mkBashLibrary gc-dolt-maintenance-lib
│   ├── gc-dolt-maintenance-lib.bash  # should_flatten() pure decision fn
│   └── tests/test-gc-dolt-maintenance-lib.bats
├── breaker/
│   ├── default.nix                   # mkBashScript gc-bd-import-breaker
│   ├── gc-bd-import-breaker.sh        # ported from gc repo tools/bd-import-breaker.sh
│   └── tests/test-gc-bd-import-breaker.bats
├── maintenance/
│   ├── default.nix                   # mkBashScript gc-dolt-maintenance (libraries = [otlp-emit, decision])
│   ├── gc-dolt-maintenance.sh
│   └── tests/test-gc-dolt-maintenance.bats
├── test-support/test_helper.bash     # shared bats helper (from bash-scripting assets)
└── grafana/dolt-maintenance-overview.json

darwin/modules/gc-dolt-maintenance/default.nix   # launchd user agent + dashboardProvider
home/programs/gc-dolt-maintenance/default.nix     # install commands + breaker activation
flake.nix                                         # register packages in the overlay
darwin/default.nix, home/default.nix              # import the new modules
```

Cleanup (gc repo `/Users/phillipg/gc`): delete `tools/bd-import-breaker.sh`, `tools/gc-dolt-maintenance.sh`, `tools/com.phillipg.gc-dolt-maintenance.plist`; unload the hand-installed plist; update `HACKS.md` HACK 18.

---

## Phase 1 — `gc-otlp-emit` library (telemetry foundation)

### Task 1: OTLP emit library + unit tests

**Files:**

- Create: `packages/gc-dolt-maintenance/otlp-emit/gc-otlp-emit.bash`
- Create: `packages/gc-dolt-maintenance/otlp-emit/default.nix`
- Create: `packages/gc-dolt-maintenance/otlp-emit/tests/test-gc-otlp-emit.bats`
- Create: `packages/gc-dolt-maintenance/test-support/test_helper.bash` (copy from bash-scripting skill `assets/test_helper.bash`)

- [ ] **Step 1: Write the library.** `gc-otlp-emit.bash` (no shebang, no `set`, starts with the shellcheck directive; library = functions only):

```bash
# shellcheck shell=bash
# gc-otlp-emit: best-effort OTLP/JSON emission via curl. Never blocks/fails the
# caller. No-op when OTEL endpoint is unset or GC_OTEL_DISABLE=1.

_otlp_endpoint() { printf '%s' "${OTEL_EXPORTER_OTLP_ENDPOINT:-}"; }
_otlp_enabled() { [[ -n "$(_otlp_endpoint)" && "${GC_OTEL_DISABLE:-0}" != "1" ]]; }
_otlp_now_ns() { date +%s%N; }   # GNU date (coreutils) on the pinned PATH
_otlp_service() { printf '%s' "${OTEL_SERVICE_NAME:-gc-dolt-maintenance}"; }

# _otlp_attrs k=v k=v ... -> JSON array of OTLP keyValue (string values)
_otlp_attrs() {
  local out="[]" kv k v
  for kv in "$@"; do
    k=${kv%%=*}; v=${kv#*=}
    out=$(jq -c --arg k "$k" --arg v "$v" \
      '. + [{key:$k,value:{stringValue:$v}}]' <<<"$out")
  done
  printf '%s' "$out"
}

# otlp_gauge NAME VALUE [k=v ...]
otlp_gauge() {
  _otlp_enabled || return 0
  local name=$1 value=$2; shift 2 || true
  local attrs; attrs=$(_otlp_attrs "$@")
  local payload
  payload=$(jq -cn --arg svc "$(_otlp_service)" --arg name "$name" \
    --argjson value "$value" --arg ts "$(_otlp_now_ns)" --argjson attrs "$attrs" '
    {resourceMetrics:[{resource:{attributes:[{key:"service.name",value:{stringValue:$svc}}]},
     scopeMetrics:[{metrics:[{name:$name,gauge:{dataPoints:[
       {asDouble:$value,timeUnixNano:$ts,attributes:$attrs}]}}]}]}]}')
  curl -s --max-time 3 -X POST -H 'Content-Type: application/json' \
    --data "$payload" "$(_otlp_endpoint)/v1/metrics" >/dev/null 2>&1 || true
}

# otlp_log SEVERITY BODY [k=v ...]
otlp_log() {
  _otlp_enabled || return 0
  local sev=$1 body=$2; shift 2 || true
  local attrs; attrs=$(_otlp_attrs "$@")
  local payload
  payload=$(jq -cn --arg svc "$(_otlp_service)" --arg sev "$sev" --arg body "$body" \
    --arg ts "$(_otlp_now_ns)" --argjson attrs "$attrs" '
    {resourceLogs:[{resource:{attributes:[{key:"service.name",value:{stringValue:$svc}}]},
     scopeLogs:[{logRecords:[{timeUnixNano:$ts,severityText:$sev,
       body:{stringValue:$body},attributes:$attrs}]}]}]}')
  curl -s --max-time 3 -X POST -H 'Content-Type: application/json' \
    --data "$payload" "$(_otlp_endpoint)/v1/logs" >/dev/null 2>&1 || true
}
```

- [ ] **Step 2: Write `default.nix`** (mirror `packages/agent-activity/lib/default.nix`):

```nix
{ mkBashLibrary, pkgs, testSupport ? null }:
mkBashLibrary {
  name = "gc-otlp-emit";
  src = ./.;
  description = "Best-effort OTLP/JSON metrics+logs emission for bash";
  inherit testSupport;
  testDeps = [ pkgs.jq pkgs.curl pkgs.coreutils ];
}
```

- [ ] **Step 3: Write failing unit test** `tests/test-gc-otlp-emit.bats`:

```bash
setup() {
  load ../../test-support/test_helper
  TEST_DIR="$(mktemp -d)"; export HOME="$TEST_DIR"
  for v in $(env | grep -oE '^(OTEL|GC|BEADS|DOLT)[A-Z_]*' || true); do unset "$v"; done
  # shellcheck source=/dev/null
  source "${LIB_PATH:-$BATS_TEST_DIRNAME/../gc-otlp-emit.bash}"
  # mock curl: record the last payload + url
  mkdir -p "$TEST_DIR/bin"
  cat >"$TEST_DIR/bin/curl" <<'EOF'
#!/usr/bin/env bash
args="$*"; data=""; while [[ $# -gt 0 ]]; do [[ "$1" == "--data" ]] && data="$2"; shift; done
printf '%s' "$data" >"$CURL_PAYLOAD_FILE"; printf '%s' "$args" >"$CURL_ARGS_FILE"
EOF
  chmod +x "$TEST_DIR/bin/curl"; export PATH="$TEST_DIR/bin:$PATH"
  export CURL_PAYLOAD_FILE="$TEST_DIR/payload" CURL_ARGS_FILE="$TEST_DIR/args"
}
teardown() { rm -rf "$TEST_DIR"; }

@test "otlp_gauge no-ops when endpoint unset" {
  run otlp_gauge dolt_maint_commit_count 5 db=hq
  [ "$status" -eq 0 ]; [ ! -f "$CURL_PAYLOAD_FILE" ]
}

@test "otlp_gauge posts well-formed OTLP to /v1/metrics" {
  export OTEL_EXPORTER_OTLP_ENDPOINT="http://127.0.0.1:4318"
  run otlp_gauge dolt_maint_commit_count 21000 db=hq
  [ "$status" -eq 0 ]
  grep -q "/v1/metrics" "$CURL_ARGS_FILE"
  name=$(jq -r '.resourceMetrics[0].scopeMetrics[0].metrics[0].name' "$CURL_PAYLOAD_FILE")
  [ "$name" = "dolt_maint_commit_count" ]
  db=$(jq -r '.resourceMetrics[0].scopeMetrics[0].metrics[0].gauge.dataPoints[0].attributes[0].value.stringValue' "$CURL_PAYLOAD_FILE")
  [ "$db" = "hq" ]
}

@test "GC_OTEL_DISABLE=1 suppresses emission" {
  export OTEL_EXPORTER_OTLP_ENDPOINT="http://127.0.0.1:4318" GC_OTEL_DISABLE=1
  run otlp_log INFO "hi" db=hq; [ "$status" -eq 0 ]; [ ! -f "$CURL_PAYLOAD_FILE" ]
}
```

- [ ] **Step 4: Run, expect FAIL** (lib not yet on `LIB_PATH` / functions missing):
      Run: `cd packages/gc-dolt-maintenance/otlp-emit && bats tests/` → FAIL.
- [ ] **Step 5: Fix source path** in the test helper / `LIB_PATH` so the `source` resolves locally; re-run → PASS (3 tests).
- [ ] **Step 6: Commit.**

```bash
git add packages/gc-dolt-maintenance/otlp-emit packages/gc-dolt-maintenance/test-support
git commit -m "feat(gc-dolt-maintenance): OTLP emit library (curl→OTLP/JSON, best-effort)"
```

---

## Phase 2 — `gc-dolt-maintenance-lib` decision function

### Task 2: `should_flatten` pure function + truth-table unit tests

**Files:**

- Create: `packages/gc-dolt-maintenance/decision/gc-dolt-maintenance-lib.bash`
- Create: `packages/gc-dolt-maintenance/decision/default.nix`
- Create: `packages/gc-dolt-maintenance/decision/tests/test-gc-dolt-maintenance-lib.bats`

- [ ] **Step 1: Write the failing test** (truth table — decision AND reason):

```bash
setup() {
  load ../../test-support/test_helper
  # shellcheck source=/dev/null
  source "${LIB_PATH:-$BATS_TEST_DIRNAME/../gc-dolt-maintenance-lib.bash}"
}
# args: commit busy hours breaker_applied has_remote ; thresholds 5000 4 6 24
flat() { should_flatten "$1" "$2" "$3" "$4" "$5" 5000 4 6 24; }

@test "remote DB never flattens"        { run flat 9000 0 99 1 1; [ "$status" -eq 0 ]; [[ "$output" == no:* ]]; [[ "$output" == *remote* ]]; }
@test "breaker off never flattens"      { run flat 9000 0 99 0 0; [[ "$output" == no:* ]]; [[ "$output" == *breaker* ]]; }
@test "below commit threshold: no"      { run flat 4999 0 99 1 0; [[ "$output" == no:* ]]; [[ "$output" == *threshold* ]]; }
@test "max-age force overrides busy"    { run flat 9000 50 25 1 0; [[ "$output" == yes:* ]]; [[ "$output" == *force* ]]; }
@test "anti-thrash: too soon"           { run flat 9000 0 3 1 0;  [[ "$output" == no:* ]]; [[ "$output" == *recent* ]]; }
@test "busy blocks within window"       { run flat 9000 9 10 1 0; [[ "$output" == no:* ]]; [[ "$output" == *busy* ]]; }
@test "happy path: need+safe+interval"  { run flat 9000 0 10 1 0; [[ "$output" == yes:* ]]; }
```

- [ ] **Step 2: Run, expect FAIL** (`should_flatten` undefined):
      Run: `cd packages/gc-dolt-maintenance/decision && bats tests/` → FAIL.

- [ ] **Step 3: Implement** `gc-dolt-maintenance-lib.bash`:

```bash
# shellcheck shell=bash
# Pure decision: prints "yes:<reason>" or "no:<reason>". No side effects.
should_flatten() {
  local commit=$1 busy=$2 hours=$3 breaker=$4 remote=$5
  local commit_thr=$6 busy_thr=$7 min_h=$8 max_h=$9
  [[ "$remote" == 1 ]]        && { echo "no:remote-connected db";       return 0; }
  [[ "$breaker" != 1 ]]       && { echo "no:breaker not applied";       return 0; }
  (( commit < commit_thr ))   && { echo "no:below commit threshold";    return 0; }
  (( hours >= max_h ))        && { echo "yes:max-age force";            return 0; }
  (( hours < min_h ))         && { echo "no:flattened too recently";    return 0; }
  (( busy >= busy_thr ))      && { echo "no:system busy";               return 0; }
  echo "yes:need met, safe, interval ok"
}
```

- [ ] **Step 4: Run, expect PASS** (7 tests). Run: `bats tests/`.
- [ ] **Step 5: Write `default.nix`** (mkBashLibrary, name `gc-dolt-maintenance-lib`, `testDeps = [ pkgs.coreutils ]`).
- [ ] **Step 6: Commit.**

```bash
git add packages/gc-dolt-maintenance/decision
git commit -m "feat(gc-dolt-maintenance): should_flatten decision function + truth table"
```

---

## Phase 3 — `gc-bd-import-breaker` command

### Task 3: Port the breaker into mkBashScript + emit a span + tests

**Files:**

- Create: `packages/gc-dolt-maintenance/breaker/gc-bd-import-breaker.sh`
- Create: `packages/gc-dolt-maintenance/breaker/default.nix`
- Create: `packages/gc-dolt-maintenance/breaker/tests/test-gc-bd-import-breaker.bats`
- Source to port: `/Users/phillipg/gc/tools/bd-import-breaker.sh` (already written + verified today)

- [ ] **Step 1: Write failing test** `tests/test-gc-bd-import-breaker.bats`:

```bash
setup() {
  load ../../test-support/test_helper
  TEST_DIR="$(mktemp -d)"; export HOME="$TEST_DIR"
  for v in $(env | grep -oE '^(OTEL|GC|BEADS|DOLT)[A-Z_]*' || true); do unset "$v"; done
  CITY="$TEST_DIR/city"; mkdir -p "$CITY/.beads/dolt/hq"   # pretend dolt exists
  printf 'x\n' >"$CITY/.beads/issues.jsonl"                # non-empty -> must be backed up
  BREAKER="${SCRIPT_PATH:-$BATS_TEST_DIRNAME/../gc-bd-import-breaker.sh}"
}
teardown() { chflags nouchg "$CITY/.beads/issues.jsonl" 2>/dev/null || true; rm -rf "$TEST_DIR"; }

@test "apply makes issues.jsonl 0-byte + uchg and backs up" {
  run bash "$BREAKER" apply --city "$CITY"
  [ "$status" -eq 0 ]
  [ "$(/usr/bin/stat -f '%z' "$CITY/.beads/issues.jsonl")" = "0" ]
  /usr/bin/stat -f '%Sf' "$CITY/.beads/issues.jsonl" | grep -q uchg
  ls "$CITY/.beads/"issues.jsonl.breaker-backup-* >/dev/null
}
@test "apply refuses when no .beads/dolt" {
  rm -rf "$CITY/.beads/dolt"
  run bash "$BREAKER" apply --city "$CITY"; [ "$status" -ne 0 ]
}
@test "status reports APPLIED after apply" {
  bash "$BREAKER" apply --city "$CITY"
  run bash "$BREAKER" --status --city "$CITY"; [[ "$output" == *APPLIED* ]]
}
@test "revert clears uchg" {
  bash "$BREAKER" apply --city "$CITY"; run bash "$BREAKER" --revert --city "$CITY"
  [ "$status" -eq 0 ]; /usr/bin/stat -f '%Sf' "$CITY/.beads/issues.jsonl" | grep -qv uchg
}
```

- [ ] **Step 2: Run, expect FAIL** (script missing). Run: `cd packages/gc-dolt-maintenance/breaker && bats tests/`.
- [ ] **Step 3: Port the script.** Copy `/Users/phillipg/gc/tools/bd-import-breaker.sh` to `gc-bd-import-breaker.sh` and make exactly these builder adaptations:
  - Delete the `#!/usr/bin/env bash` shebang line.
  - Delete the `set -euo pipefail` line.
  - Add as the first line: `# shellcheck shell=bash`.
  - After a successful `apply`/`revert`, add: `otlp_log INFO "breaker $ACTION" city="$CITY" backed_up="${bak:+yes}"` (the lib is sourced by the builder via `libraries`).
    Keep all other logic (arg parsing, `chflags uchg`, backup, `.beads/dolt` guard, idempotency) identical.
- [ ] **Step 4: Write `default.nix`** (mirror `wait-for-agents-to-finish/default.nix`):

```nix
{ mkBashScript, pkgs, gc-otlp-emit }:
mkBashScript {
  name = "gc-bd-import-breaker";
  src = ./.;
  description = "Pin <city>/.beads/issues.jsonl as immutable-empty to stop bd's auto-import spiral (HACK 18)";
  libraries = [ gc-otlp-emit ];
  runtimeDeps = [ pkgs.coreutils pkgs.curl pkgs.jq ];
  testDeps = [ pkgs.coreutils pkgs.curl pkgs.jq ];
}
```

- [ ] **Step 5: Run, expect PASS** (4 tests). Run: `bats tests/`.
- [ ] **Step 6: Commit.**

```bash
git add packages/gc-dolt-maintenance/breaker
git commit -m "feat(gc-dolt-maintenance): gc-bd-import-breaker command (port of HACK 18 tool)"
```

---

## Phase 4 — `gc-dolt-maintenance` orchestration

### Task 4: Orchestration script + integration tests (real ephemeral dolt server)

**Files:**

- Create: `packages/gc-dolt-maintenance/maintenance/gc-dolt-maintenance.sh`
- Create: `packages/gc-dolt-maintenance/maintenance/default.nix`
- Create: `packages/gc-dolt-maintenance/maintenance/tests/test-gc-dolt-maintenance.bats`

- [ ] **Step 1: Write the orchestration script** `gc-dolt-maintenance.sh` (no shebang/set; `# shellcheck shell=bash` first line; `should_flatten`, `otlp_gauge`, `otlp_log` come from the sourced libraries). Config vars (`CITY_PATH`, `FLATTEN_COMMIT_THRESHOLD`, `BUSY_PROC_THRESHOLD`, `MIN_FLATTEN_INTERVAL_HOURS`, `MAX_FLATTEN_INTERVAL_HOURS`, `STATE_DIR`) are injected by mkBashScript `config` with env fallbacks:

```bash
# shellcheck shell=bash
CITY="${GC_CITY:-${CITY_PATH:-$PWD}}"
STATE_DIR="${STATE_DIR:-${GC_HOME:-$HOME/.gc}/dolt-maintenance}"
COMMIT_THR="${FLATTEN_COMMIT_THRESHOLD:-5000}"
BUSY_THR="${BUSY_PROC_THRESHOLD:-4}"
MIN_H="${MIN_FLATTEN_INTERVAL_HOURS:-6}"
MAX_H="${MAX_FLATTEN_INTERVAL_HOURS:-24}"
DO_FLATTEN=1
DOLT_ROOT="$CITY/.beads/dolt"

log() { printf '[%s] %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*"; }
dolt_sql() { ( cd "$1" && dolt sql -r csv -q "$2" 2>/dev/null | tail -1 ); }

breaker_applied() {
  local f="$CITY/.beads/issues.jsonl"
  [[ -e "$f" && "$(/usr/bin/stat -f '%z' "$f" 2>/dev/null)" == "0" ]] \
    && /usr/bin/stat -f '%Sf' "$f" 2>/dev/null | grep -q uchg
}

while [[ $# -gt 0 ]]; do case $1 in
  --no-flatten) DO_FLATTEN=0; shift ;;
  --city) CITY=$2; DOLT_ROOT="$CITY/.beads/dolt"; shift 2 ;;
  -h|--help) sed -n '2,20p' "$0"; exit 0 ;;
  *) echo "unknown arg: $1" >&2; exit 1 ;;
esac; done

mkdir -p "$STATE_DIR"
log "=== gc-dolt-maintenance start (city=$CITY) ==="
ba=0; breaker_applied && ba=1
otlp_gauge dolt_maint_breaker_applied "$ba"
otlp_gauge dolt_maint_busy_procs "$(pgrep -fc 'bin/bd ' || echo 0)"

for dbdir in "$DOLT_ROOT"/*/; do
  [[ -d "$dbdir/.dolt" ]] || continue
  db=$(basename "$dbdir")
  before=$(du -sm "$dbdir" 2>/dev/null | cut -f1)

  # cheap tier (no bd -> no import risk)
  for proc in DOLT_STATS_PURGE DOLT_GC; do
    if ( cd "$dbdir" && dolt sql -q "CALL $proc()" ) >/dev/null 2>&1; then
      log "$db: $proc ok"; otlp_log INFO "$proc ok" db="$db"
    else
      log "$db: $proc failed"; otlp_log WARN "$proc failed" db="$db"
    fi
  done
  after=$(du -sm "$dbdir" 2>/dev/null | cut -f1)
  log "$db: ${before:-?}MB -> ${after:-?}MB"

  commit=$(dolt_sql "$dbdir" "SELECT COUNT(*) FROM dolt_log"); commit=${commit:-0}
  size_bytes=$(( ${after:-0} * 1024 * 1024 ))
  remote=0; ( cd "$dbdir" && [[ -n "$(dolt remote -v 2>/dev/null)" ]] ) && remote=1
  busy=$(pgrep -fc 'bin/bd ' || echo 0)
  statef="$STATE_DIR/$db.last-flatten"
  if [[ -f "$statef" ]]; then
    hours=$(( ( $(date +%s) - $(cat "$statef") ) / 3600 ))
  else hours=999999; fi
  ba=0; breaker_applied && ba=1

  otlp_gauge dolt_maint_commit_count "$commit" db="$db"
  otlp_gauge dolt_maint_size_bytes "$size_bytes" db="$db"
  otlp_gauge dolt_maint_hours_since_flatten "$hours" db="$db"
  otlp_gauge dolt_maint_has_remote "$remote" db="$db"

  if [[ "$DO_FLATTEN" == 1 ]]; then
    decision=$(should_flatten "$commit" "$busy" "$hours" "$ba" "$remote" "$COMMIT_THR" "$BUSY_THR" "$MIN_H" "$MAX_H")
    log "$db: flatten decision=$decision (commit=$commit busy=$busy hours=$hours)"
    otlp_log INFO "flatten_decision ${decision%%:*}" db="$db" reason="${decision#*:}" commit_count="$commit"
    if [[ "$decision" == yes:* ]]; then
      if ( cd "$CITY" && gc bd flatten --force ) >/dev/null 2>&1; then
        date +%s >"$statef"; log "$db: flatten ok"; otlp_log INFO "flatten_exec ok" db="$db"
      else
        log "$db: flatten failed (non-fatal)"; otlp_log ERROR "flatten_exec failed" db="$db"
      fi
    fi
  fi
done
otlp_gauge dolt_maint_last_run_timestamp "$(date +%s)"
log "=== gc-dolt-maintenance done ==="
```

- [ ] **Step 2: Write failing integration test** `tests/test-gc-dolt-maintenance.bats` — real ephemeral dolt server, full isolation, teardown kills server even on error:

```bash
setup() {
  load ../../test-support/test_helper
  TEST_DIR="$(mktemp -d)"; export HOME="$TEST_DIR"
  for v in $(env | grep -oE '^(OTEL|GC|BEADS|DOLT)[A-Z_]*' || true); do unset "$v"; done
  export GC_OTEL_DISABLE=1                      # no telemetry escapes
  CITY="$TEST_DIR/city"; DB="$CITY/.beads/dolt/hq"; mkdir -p "$DB"
  ( cd "$DB" && dolt init >/dev/null 2>&1 \
      && dolt sql -q "CREATE TABLE issues (id varchar(64) primary key); INSERT INTO issues VALUES ('sentinel-test-db')" >/dev/null 2>&1 \
      && dolt sql -q "CALL DOLT_COMMIT('-A','-m','seed')" >/dev/null 2>&1 )
  PORT=$(( (RANDOM % 20000) + 30000 ))          # ephemeral, NEVER 24158/24159
  ( cd "$DB" && dolt sql-server --host 127.0.0.1 --port "$PORT" >/dev/null 2>&1 &
    echo $! >"$TEST_DIR/dolt.pid" )
  sleep 2
  # assert it's the TEST db (sentinel present), never a real one
  run bash -c "cd '$DB' && dolt sql -r csv -q \"SELECT id FROM issues\" | tail -1"
  [[ "$output" == "sentinel-test-db" ]] || { echo "WRONG DB"; return 1; }
  # PATH shims for gc/bd/curl so nothing real is touched
  mkdir -p "$TEST_DIR/bin"
  printf '#!/usr/bin/env bash\nexit 0\n' >"$TEST_DIR/bin/gc"
  printf '#!/usr/bin/env bash\nexit 0\n' >"$TEST_DIR/bin/bd"
  chmod +x "$TEST_DIR/bin/"*; export PATH="$TEST_DIR/bin:$PATH"
  SCRIPT="${SCRIPT_PATH:-$BATS_TEST_DIRNAME/../gc-dolt-maintenance.sh}"
}
teardown() {                                    # ALWAYS runs, even on failure
  if [[ -f "$TEST_DIR/dolt.pid" ]]; then kill "$(cat "$TEST_DIR/dolt.pid")" 2>/dev/null || true; fi
  pkill -f "dolt sql-server --port $PORT" 2>/dev/null || true
  rm -rf "$TEST_DIR"
}

@test "cheap tier runs and reports size; no flatten when below threshold" {
  run bash "$SCRIPT" --city "$CITY"
  [ "$status" -eq 0 ]
  [[ "$output" == *"DOLT_STATS_PURGE ok"* ]]
  [[ "$output" == *"flatten decision=no:"* ]]   # 1-row seed << 5000
}
```

- [ ] **Step 3: Run, expect FAIL.** Run: `cd packages/gc-dolt-maintenance/maintenance && bats tests/`.
- [ ] **Step 4: Write `default.nix`:**

```nix
{ mkBashScript, pkgs, gc-otlp-emit, gc-dolt-maintenance-lib, gascity }:
mkBashScript {
  name = "gc-dolt-maintenance";
  src = ./.;
  description = "Hourly self-gating Dolt maintenance: stats-purge/GC always, flatten when worthwhile+safe";
  libraries = [ gc-otlp-emit gc-dolt-maintenance-lib ];
  runtimeDeps = [ pkgs.dolt gascity pkgs.curl pkgs.jq pkgs.coreutils pkgs.procps ];
  testDeps = [ pkgs.dolt pkgs.curl pkgs.jq pkgs.coreutils pkgs.procps ];
}
```

- [ ] **Step 5: Run, expect PASS.** Run: `bats tests/`. (If the local machine lacks `dolt`, run from `nix develop` or skip-guard with `command -v dolt`.)
- [ ] **Step 6: Commit.**

```bash
git add packages/gc-dolt-maintenance/maintenance
git commit -m "feat(gc-dolt-maintenance): hourly self-gating maintenance orchestration"
```

---

## Phase 5 — nix wiring (flake + darwin + home)

### Task 5: Aggregate package + flake registration

**Files:**

- Create: `packages/gc-dolt-maintenance/default.nix`
- Modify: `flake.nix` (overlay, near the `agent-activity` block ~line 116)

- [ ] **Step 1: Aggregate `default.nix`** — takes `bashBuilders` + pkgs, builds the four sub-packages, returns an attrset:

```nix
{ bashBuilders, pkgs, gascity }:
let
  inherit (bashBuilders) mkBashScript mkBashLibrary;
  gc-otlp-emit = pkgs.callPackage ./otlp-emit { inherit mkBashLibrary; };
  gc-dolt-maintenance-lib = pkgs.callPackage ./decision { inherit mkBashLibrary; };
in {
  inherit gc-otlp-emit gc-dolt-maintenance-lib;
  gc-bd-import-breaker = pkgs.callPackage ./breaker { inherit mkBashScript gc-otlp-emit; };
  gc-dolt-maintenance = pkgs.callPackage ./maintenance {
    inherit mkBashScript gc-otlp-emit gc-dolt-maintenance-lib gascity;
  };
}
```

- [ ] **Step 2: Register in `flake.nix`** overlay (mirror the `agent-activity` import block):

```nix
gc-dolt-maintenance-pkgs = import ./packages/gc-dolt-maintenance {
  inherit bashBuilders gascity;
  pkgs = final;
};
gc-bd-import-breaker = gc-dolt-maintenance-pkgs.gc-bd-import-breaker;
gc-dolt-maintenance = gc-dolt-maintenance-pkgs.gc-dolt-maintenance;
```

(Ensure `gascity` is in scope in the overlay — it is if gascity is a flake input feeding the overlay; if not, add the input. Confirm via `grep gascity flake.nix`.)

- [ ] **Step 3: Verify build.** Run: `nix build .#gc-dolt-maintenance .#gc-bd-import-breaker` → both build.
- [ ] **Step 4: Run flake checks.** Run: `nix flake check` → passes (bats checks for all four sub-packages run here).
- [ ] **Step 5: Commit.**

```bash
git add packages/gc-dolt-maintenance/default.nix flake.nix
git commit -m "build(gc-dolt-maintenance): register packages in the flake overlay"
```

### Task 6: Darwin module (LaunchAgent + dashboard provider)

**Files:**

- Create: `darwin/modules/gc-dolt-maintenance/default.nix`
- Modify: `darwin/default.nix` (import the module)

- [ ] **Step 1: Write the darwin module** (mirror `darwin/modules/pa-monitor/default.nix`):

```nix
{ config, lib, pkgs, ... }:
let
  obs = config.phillipgreenii.observability;
  emitterEnv =
    if obs ? mkEmitterEnv then
      obs.mkEmitterEnv { serviceName = "gc-dolt-maintenance"; protocol = "http/protobuf"; }
    else { };
  primaryUser = config.system.primaryUser or "phillipg";
  logDir = "/Users/${primaryUser}/.gc";
in {
  config = lib.mkMerge [
    (lib.mkIf (obs.enable or false) {
      phillipgreenii.observability.dashboardProviders.dolt-maintenance = {
        folder = "Claude Agents";
        dashboards = [ ../../../packages/gc-dolt-maintenance/grafana/dolt-maintenance-overview.json ];
      };
    })
    {
      phillipgreenii.system.launchdServices.userAgents.gc-dolt-maintenance = {
        label = "com.phillipg.gc-dolt-maintenance";
        script = ''exec ${pkgs.gc-dolt-maintenance}/bin/gc-dolt-maintenance --city /Users/${primaryUser}/gc'';
        runAtLoad = false;
        keepAlive = false;
        serviceConfig = {
          StartCalendarInterval = [ { Minute = 0; } ];   # hourly at :00
          StandardOutPath = "${logDir}/dolt-maintenance.log";
          StandardErrorPath = "${logDir}/dolt-maintenance.log";
          EnvironmentVariables = emitterEnv;
        };
      };
    }
  ];
}
```

- [ ] **Step 2: Import** in `darwin/default.nix` (add `./modules/gc-dolt-maintenance` to the imports list, alongside `./modules/pa-monitor`).
- [ ] **Step 3: Build the darwin system.** Run: `nix build .#darwinConfigurations.<host>.system` (use the actual host attr) → builds.
- [ ] **Step 4: Commit.**

```bash
git add darwin/modules/gc-dolt-maintenance darwin/default.nix
git commit -m "feat(gc-dolt-maintenance): hourly LaunchAgent + Grafana dashboard provider (darwin)"
```

### Task 7: Home module (install commands + breaker activation)

**Files:**

- Create: `home/programs/gc-dolt-maintenance/default.nix`
- Modify: `home/default.nix` (import the module)

- [ ] **Step 1: Write the home module:**

```nix
{ config, lib, pkgs, ... }:
let
  cfg = config.phillipgreenii.programs.gc-dolt-maintenance;
  cityPath = cfg.cityPath;
in {
  options.phillipgreenii.programs.gc-dolt-maintenance = {
    enable = lib.mkEnableOption "gc dolt maintenance tools";
    cityPath = lib.mkOption { type = lib.types.str; default = "/Users/phillipg/gc"; };
  };
  config = lib.mkIf cfg.enable {
    home.packages = [ pkgs.gc-bd-import-breaker pkgs.gc-dolt-maintenance ];
    # Ensure the breaker is applied on every switch (idempotent).
    home.activation.gcBdImportBreaker = lib.hm.dag.entryAfter [ "writeBoundary" ] ''
      run ${pkgs.gc-bd-import-breaker}/bin/gc-bd-import-breaker apply --city ${lib.escapeShellArg cityPath} || true
    '';
  };
}
```

- [ ] **Step 2: Import + enable** in `home/default.nix` (`./programs/gc-dolt-maintenance` in imports; set `phillipgreenii.programs.gc-dolt-maintenance.enable = true;`).
- [ ] **Step 3: Build the home config.** Run: `nix build .#homeConfigurations.<user>.activationPackage` → builds.
- [ ] **Step 4: Commit.**

```bash
git add home/programs/gc-dolt-maintenance home/default.nix
git commit -m "feat(gc-dolt-maintenance): install commands + breaker activation (home)"
```

---

## Phase 6 — Grafana dashboard

### Task 8: `dolt-maintenance-overview.json`

**Files:**

- Create: `packages/gc-dolt-maintenance/grafana/dolt-maintenance-overview.json`
- Template to mirror exactly (structure, schemaVersion, datasource uids, gridPos idiom): `phillipg-nix-ziprecruiter/darwin/modules/gascity/grafana/gascity-overview.json` and `packages/pa-monitor/grafana/pa-monitor-overview.json`.

- [ ] **Step 1: Author the JSON** by copying the gascity file's outer structure (title → `"Dolt Maintenance — Overview"`, same `templating`/`time`/`schemaVersion`, datasource uids `"prometheus"` and `"loki"`) and replacing panels with these (exact queries):
  - **Stat — Breaker applied** (prometheus): `max(dolt_maint_breaker_applied)` — thresholds: red 0, green 1.
  - **Stat — DBs over commit threshold** (prometheus): `count(dolt_maint_commit_count > 5000)`.
  - **Stat — hq size (MB)** (prometheus): `dolt_maint_size_bytes{db="hq"} / 1024 / 1024`.
  - **Stat — hours since hq flatten** (prometheus): `dolt_maint_hours_since_flatten{db="hq"}`.
  - **Timeseries — Commit count by db** (prometheus): `dolt_maint_commit_count`, legend `{{db}}`.
  - **Timeseries — Size (bytes) by db** (prometheus): `dolt_maint_size_bytes`, legend `{{db}}`.
  - **Timeseries — Hours since flatten by db** (prometheus): `dolt_maint_hours_since_flatten`, legend `{{db}}`.
  - **Timeseries — Busy bd procs** (prometheus): `dolt_maint_busy_procs`.
  - **Logs — Recent maintenance actions** (loki): `{service_name="gc-dolt-maintenance"}` (the "Recent gascity logs" analog).
- [ ] **Step 2: Validate JSON.** Run: `jq empty packages/gc-dolt-maintenance/grafana/dolt-maintenance-overview.json` → no error.
- [ ] **Step 3: Validate provisioning eval.** Run: `nix build .#darwinConfigurations.<host>.system` → still builds (dashboard path resolves).
- [ ] **Step 4: Commit.**

```bash
git add packages/gc-dolt-maintenance/grafana
git commit -m "feat(gc-dolt-maintenance): Grafana dashboard (gascity-style)"
```

---

## Phase 7 — cleanup / migration

### Task 9: Remove hand-rolled artifacts + update HACKS.md

**Files (gc repo `/Users/phillipg/gc`):**

- Delete: `tools/bd-import-breaker.sh`, `tools/gc-dolt-maintenance.sh`, `tools/com.phillipg.gc-dolt-maintenance.plist`
- Modify: `HACKS.md` (HACK 18 → reference the nix command, not `tools/…`)

- [ ] **Step 1: Unload the hand-installed LaunchAgent.**
      Run: `launchctl unload ~/Library/LaunchAgents/com.phillipg.gc-dolt-maintenance.plist && rm ~/Library/LaunchAgents/com.phillipg.gc-dolt-maintenance.plist`
- [ ] **Step 2: Delete the tools scripts.**
      Run: `cd /Users/phillipg/gc && git rm tools/bd-import-breaker.sh tools/gc-dolt-maintenance.sh tools/com.phillipg.gc-dolt-maintenance.plist`
- [ ] **Step 3: Update HACK 18** in `/Users/phillipg/gc/HACKS.md`: change the **Tool** lines to "`gc-bd-import-breaker` (nix command from `phillipgreenii-nix-agent-support`); breaker also ensured on every `home-manager switch`." Note the standalone-script paragraph is obsolete (the command supersedes it).
- [ ] **Step 4: Commit (gc repo).**

```bash
cd /Users/phillipg/gc && git add -A HACKS.md tools && git commit -m "chore: retire hand-rolled dolt-maintenance tools (superseded by nix gc-dolt-maintenance)"
```

- [ ] **Step 5: Apply for real.** Run `home-manager switch` (or the darwin-rebuild flow that includes HM) → confirms the breaker activation runs, the LaunchAgent registers (`launchctl list | grep gc-dolt-maintenance`), and the dashboard provisions (once obs stack is up — separate agent).

---

## Self-Review

**Spec coverage:** breaker (T3) ✓; self-gating flatten decision (T2) ✓; orchestration cheap-tier + gated flatten + signals (T4) ✓; OTLP metrics+logs (T1, used in T3/T4) ✓; hourly LaunchAgent via launchdServices + mkEmitterEnv (T6) ✓; breaker activation as user (T7) ✓; dashboard gascity-style + provider (T6, T8) ✓; tests with isolation/ephemeral-server/teardown/curl-mock (T1–T4) ✓; cleanup + HACK 18 (T9) ✓; otel-stack-up correctly absent (non-goal) ✓.

**Placeholder scan:** decision fn, OTLP builders, orchestration, breaker adaptation, and all test bodies are complete code; the Grafana JSON is specified by exact queries + an in-repo structural template (not a placeholder).

**Type/name consistency:** `should_flatten` arg order (commit, busy, hours, breaker, remote, commit*thr, busy_thr, min_h, max_h) is identical in lib, its tests, and the orchestration call; metric names (`dolt_maint*\*`) match between orchestration emit and dashboard queries; `otlp_gauge`/`otlp_log` signatures match between lib, tests, and callers.

**Open verification (do at execution):** confirm `gascity` is reachable in the flake overlay (add input if not); confirm the exact host/user attrs for the `nix build` commands; confirm `phillipgreenii.system.launchdServices.userAgents` accepts `serviceConfig.StartCalendarInterval` (pa-monitor passes `serviceConfig` through, so it should).
