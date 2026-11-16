#!/usr/bin/env bats
# bats file_tags=type:unit
# Spool metrics: OTLP-JSON gauge files in pn's exact spool file-name pattern, for
# reclaim and auto runs. The clock and the free-space reads are scripted through
# PGDR_NOW_CMD and PGDR_FREE_KB_CMD.
bats_require_minimum_version 1.5.0
load 'hermetic'

GB=1048576

setup() {
  if [[ -z ${SCRIPTS_DIR:-} ]]; then
    SCRIPTS_DIR="$(cd "$(dirname "$BATS_TEST_FILENAME")/.." && pwd)"
  fi
  TEST_DIR="$(mktemp -d)"
  export TEST_DIR
  pgdr_test_isolate "$TEST_DIR"
  pgdr_test_assert_hermetic "$TEST_DIR"
  source "$SCRIPTS_DIR/pg-disk-reclaimer.bash"
  mkdir -p "$TEST_DIR/area" "$HOME/.config/pg-disk-reclaimer"
  SPOOL="$TEST_DIR/spool" # does not exist yet
  export PGDR_OTEL_SPOOL_DIR="$SPOOL"

  # clock: every call advances 0.25 s
  echo 0 >"$TEST_DIR/now.n"
  cat >"$TEST_DIR/now" <<STUB
#!/usr/bin/env bash
n=\$(cat "$TEST_DIR/now.n"); echo \$((n + 1)) >"$TEST_DIR/now.n"
us=\$((1700000000000000 + n * 250000))
printf '%d.%06d\n' \$((us / 1000000)) \$((us % 1000000))
STUB
  # free space: pops the first line of the sequence file, repeats the last
  cat >"$TEST_DIR/free" <<STUB
#!/usr/bin/env bash
f="$TEST_DIR/free.seq"; v=\$(head -n1 "\$f")
if [ "\$(wc -l <"\$f")" -gt 1 ]; then tail -n +2 "\$f" >"\$f.n"; mv "\$f.n" "\$f"; fi
echo "\$v"
STUB
  chmod +x "$TEST_DIR/now" "$TEST_DIR/free"
  export PGDR_NOW_CMD="$TEST_DIR/now" PGDR_FREE_KB_CMD="$TEST_DIR/free"
  printf '1000\n' >"$TEST_DIR/free.seq"
}

teardown() {
  rm -rf "$TEST_DIR"
}

# install <id:dry-exit:rm-exit>...: one level-1 item per argument.
install() {
  local spec id d r
  local -a items=()
  for spec in "$@"; do
    IFS=: read -r id d r <<<"$spec"
    items+=("$(jq -n --arg id "$id" --arg path "$TEST_DIR/area" --arg dry "exit $d" --arg rm "exit $r" \
      '{id: $id, description: "d", path: $path, displayCommand: "true", sizeCommand: "echo 10",
        variants: [{aggressiveness: 1, variantDescription: "v", dryRunCommand: $dry, removeCommand: $rm}]}')")
  done
  printf '%s\n' "${items[@]}" | jq -s . >"$HOME/.config/pg-disk-reclaimer/registry.json"
}

# spool_files: the .jsonl files; one_file: the single one or fail.
spool_files() {
  local f
  for f in "$SPOOL"/metrics/*; do
    [[ -e $f ]] && basename "$f"
  done
  return 0
}
one_file() {
  local n
  n=$(spool_files | wc -l)
  [ "$n" -eq 1 ]
  printf '%s/metrics/%s\n' "$SPOOL" "$(spool_files)"
}

# flat <file>: "<metric> <k=v,...> <value>" per data point.
flat() {
  jq -r '.resourceMetrics[].scopeMetrics[].metrics[] | .name as $n | .gauge.dataPoints[]
    | [$n, (.attributes | map("\(.key)=\(.value.stringValue)") | join(",")), (.asInt // .asDouble | tostring)]
    | join(" ")' "$1"
}

has() { flat "$FILE" | grep -Fxq -- "$1"; }

@test "a reclaim run creates metrics/, writes one pn-pattern file, no leftover tmp" {
  install a:0:0
  [ ! -e "$SPOOL" ]
  run cmd_reclaim --aggressiveness 1
  [ "$status" -eq 0 ]
  FILE=$(one_file)
  [[ $(basename "$FILE") =~ ^pg_disk_reclaimer-[0-9]{8}T[0-9]{6}Z-[0-9]+-[0-9a-f]{8}-[0-9]+\.jsonl$ ]]
  run compgen -G "$SPOOL/metrics/.*.tmp"
  [ "$status" -ne 0 ]
  [ "$(wc -l <"$FILE")" -eq 1 ]
  [ "$(jq -r '.resourceMetrics[0].resource.attributes[0].key' "$FILE")" = pn.spool.file_id ]
  [ "$(jq -r '.resourceMetrics[0].resource.attributes[1] | "\(.key)=\(.value.stringValue)"' "$FILE")" = service.name=pg-disk-reclaimer ]
  [[ $(jq -r '.resourceMetrics[0].resource.attributes[0].value.stringValue' "$FILE") =~ ^[0-9a-f]{32}$ ]]
  [ "$(jq -r '[.. | .timeUnixNano? // empty] | map(select(test("^[1-9][0-9]*$"))) | length' "$FILE")" -ge 1 ]
}

@test "two runs write files with different pn.spool.file_id" {
  install a:0:0
  run cmd_reclaim --aggressiveness 1
  run cmd_reclaim --aggressiveness 1
  [ "$(spool_files | wc -l)" -eq 2 ]
  local ids
  ids=$(for f in $(spool_files); do jq -r '.resourceMetrics[0].resource.attributes[0].value.stringValue' "$SPOOL/metrics/$f"; done | sort -u | wc -l)
  [ "$ids" -eq 2 ]
}

@test "dry run: outcomes ok, failed and skipped, applied=false, no freed points" {
  install ok1:0:0 bad1:1:0 skip1:75:0
  run cmd_reclaim --aggressiveness 1
  [ "$status" -eq 1 ]
  FILE=$(one_file)
  has "pg_disk_reclaimer_item_duration_seconds item=ok1,level=1,outcome=ok,mode=manual,applied=false 0.25"
  has "pg_disk_reclaimer_item_duration_seconds item=bad1,level=1,outcome=failed,mode=manual,applied=false 0.25"
  has "pg_disk_reclaimer_item_duration_seconds item=skip1,level=1,outcome=skipped,mode=manual,applied=false 0.25"
  [ "$(flat "$FILE" | grep -c freed_bytes || true)" -eq 0 ]
}

@test "apply: freed bytes match the scripted delta and clamp at 0" {
  install ok1:0:0 neg1:0:0 bad1:0:1 skip1:0:75
  # before/after per item: +2000 KiB, -1000 KiB (clamps), then flat, flat
  printf '1000\n3000\n5000\n4000\n6000\n6000\n7000\n7000\n' >"$TEST_DIR/free.seq"
  run cmd_reclaim --aggressiveness 1 --apply
  [ "$status" -eq 1 ]
  FILE=$(one_file)
  has "pg_disk_reclaimer_item_freed_bytes item=ok1,level=1,mode=manual,applied=true 2048000"
  has "pg_disk_reclaimer_item_freed_bytes item=neg1,level=1,mode=manual,applied=true 0"
  has "pg_disk_reclaimer_item_duration_seconds item=bad1,level=1,outcome=failed,mode=manual,applied=true 0.25"
  has "pg_disk_reclaimer_item_duration_seconds item=skip1,level=1,outcome=skipped,mode=manual,applied=true 0.25"
}

@test "golden file for one representative document" {
  install ok1:0:0
  export PGDR_OTEL_FILE_ID=0123456789abcdef0123456789abcdef
  run cmd_reclaim --aggressiveness 1
  FILE=$(one_file)
  [ "$(jq -S . "$FILE")" = "$(jq -S . "$BATS_TEST_DIRNAME/fixtures/spool-golden.json")" ]
}

@test "OTEL_SDK_DISABLED=true writes nothing" {
  install a:0:0
  OTEL_SDK_DISABLED=true run cmd_reclaim --aggressiveness 1
  [ "$status" -eq 0 ]
  [ ! -e "$SPOOL" ]
}

@test "telemetry.toml enabled=false writes nothing; enabled=true uses its spool_dir" {
  install a:0:0
  unset PGDR_OTEL_SPOOL_DIR
  printf 'enabled = false\nspool_dir = "%s"\n' "$SPOOL" >"$HOME/.config/pg-disk-reclaimer/telemetry.toml"
  run cmd_reclaim --aggressiveness 1
  [ ! -e "$SPOOL" ]
  printf 'enabled = true\nspool_dir = "%s"\n' "$SPOOL" >"$HOME/.config/pg-disk-reclaimer/telemetry.toml"
  run cmd_reclaim --aggressiveness 1
  [ "$(spool_files | wc -l)" -eq 1 ]
}

@test "telemetry.toml enabled=true without spool_dir uses XDG_STATE_HOME/otel-spool; no toml is off" {
  install a:0:0
  unset PGDR_OTEL_SPOOL_DIR
  run cmd_reclaim --aggressiveness 1
  [ ! -e "$XDG_STATE_HOME/otel-spool" ]
  printf 'enabled = true\n' >"$HOME/.config/pg-disk-reclaimer/telemetry.toml"
  run cmd_reclaim --aggressiveness 1
  [ "$(ls "$XDG_STATE_HOME/otel-spool/metrics" | wc -l)" -eq 1 ]
}

@test "an unwritable spool prints one stderr line and keeps the exit status" {
  install a:0:0 bad1:1:0
  : >"$TEST_DIR/blocker"
  export PGDR_OTEL_SPOOL_DIR="$TEST_DIR/blocker"
  run --separate-stderr cmd_reclaim --aggressiveness 1
  [ "$status" -eq 1 ]
  [ "$(grep -c 'telemetry: cannot write metrics' <<<"$stderr")" -eq 1 ]
  install a:0:0
  run --separate-stderr cmd_reclaim --aggressiveness 1
  [ "$status" -eq 0 ]
}

# --- auto ---------------------------------------------------------------------

# auto <free-gib> [args]: an applied auto run at the selected level.
auto() {
  local gib="$1"
  shift
  PGDR_AUTO_FREE_KB=$((gib * GB)) run --separate-stderr cmd_auto "$@" --below1-gb 60 --below2-gb 45 --below3-gb 30
}

@test "auto ok: run points with level, outcome, free before/after and thresholds" {
  install a:0:0
  # item before/after, then the run's after read (+1 GiB over the 50 GiB before)
  printf '10\n20\n%s\n' $((51 * GB)) >"$TEST_DIR/free.seq"
  auto 50 --apply
  [ "$status" -eq 0 ]
  FILE=$(one_file)
  flat "$FILE" | grep -Eq '^pg_disk_reclaimer_auto_run_duration_seconds level=1,outcome=ok,applied=true [0-9]+(\.[0-9]+)?$'
  has "pg_disk_reclaimer_auto_run_freed_bytes level=1,applied=true 1073741824"
  has "pg_disk_reclaimer_auto_free_bytes when=before,applied=true 53687091200"
  has "pg_disk_reclaimer_auto_free_bytes when=after,applied=true 54760833024"
  has "pg_disk_reclaimer_auto_threshold_bytes level=1,applied=true 64424509440"
  has "pg_disk_reclaimer_auto_threshold_bytes level=2,applied=true 48318382080"
  has "pg_disk_reclaimer_auto_threshold_bytes level=3,applied=true 32212254720"
  has "pg_disk_reclaimer_item_duration_seconds item=a,level=1,outcome=ok,mode=auto,applied=true 0.25"
  # one file for the whole run, JSONL line carries duration and items
  jq -e '.duration_s >= 0 and (.items | length == 1) and .items[0].id == "a" and .items[0].outcome == "ok"' <<<"$output"
}

@test "auto outcome precedence: alert beats failed beats ok" {
  install a:0:1
  printf '10\n20\n%s\n' $((20 * GB)) >"$TEST_DIR/free.seq"
  auto 20 --apply
  FILE=$(one_file)
  flat "$FILE" | grep -Eq '^pg_disk_reclaimer_auto_run_duration_seconds level=3,outcome=alert,'
  rm -rf "$SPOOL"
  printf '10\n20\n%s\n' $((50 * GB)) >"$TEST_DIR/free.seq"
  auto 50 --apply
  FILE=$(one_file)
  flat "$FILE" | grep -Eq '^pg_disk_reclaimer_auto_run_duration_seconds level=1,outcome=failed,'
}

@test "auto nothing-to-do: level none" {
  install a:0:0
  auto 100 --apply
  [ "$status" -eq 0 ]
  FILE=$(one_file)
  flat "$FILE" | grep -Eq '^pg_disk_reclaimer_auto_run_duration_seconds level=none,outcome=nothing-to-do,'
}

@test "auto read-error and config-error keep their exit status and write an outcome" {
  install a:0:0
  unset PGDR_FREE_KB_CMD
  run --separate-stderr cmd_auto --apply --volume "$TEST_DIR/no-such-volume"
  [ "$status" -eq 1 ]
  FILE=$(one_file)
  flat "$FILE" | grep -Eq '^pg_disk_reclaimer_auto_run_duration_seconds level=none,outcome=read-error,'
  rm -rf "$SPOOL"
  PGDR_AUTO_FREE_KB=abc run --separate-stderr cmd_auto --apply
  [ "$status" -eq 1 ]
  FILE=$(one_file)
  flat "$FILE" | grep -Eq '^pg_disk_reclaimer_auto_run_duration_seconds level=none,outcome=config-error,'
}

@test "an unapplied auto run writes item points but no run points" {
  install a:0:0
  auto 50
  [ "$status" -eq 0 ]
  FILE=$(one_file)
  [ "$(flat "$FILE" | grep -c auto_ || true)" -eq 0 ]
  has "pg_disk_reclaimer_item_duration_seconds item=a,level=1,outcome=ok,mode=auto,applied=false 0.25"
}

@test "an argument-parse failure in auto emits nothing" {
  install a:0:0
  run --separate-stderr cmd_auto --bogus
  [ "$status" -eq 1 ]
  [ ! -e "$SPOOL" ]
}
