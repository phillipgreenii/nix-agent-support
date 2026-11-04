#!/usr/bin/env bats
# host-forensics: retained crash evidence (new panic reports copied after boot, a daily
# snapshot of disk, swap, load and wired memory). Every case runs the REAL script against a
# fixture diagnostics directory and a fixture state directory, with stub df/sysctl/vm_stat
# first on PATH, so nothing on the real host is read or written.
#   HF_SCRIPT  the script under test (defaults to the one beside the module)

setup() {
  HF_SCRIPT="${HF_SCRIPT:-$BATS_TEST_DIRNAME/../home/programs/host-forensics/host-forensics.bash}"
  VOL="$(mktemp -d)"
  export HF_DIAG_DIR="$VOL/diag" HF_STATE_DIR="$VOL/state"
  mkdir -p "$HF_DIAG_DIR" "$VOL/bin"
  cat >"$VOL/bin/df" <<'STUB'
#!/bin/sh
printf 'Filesystem 1024-blocks Used Available Capacity Mounted\n/dev/x 1000 900 12345 99%% /\n'
STUB
  cat >"$VOL/bin/sysctl" <<'STUB'
#!/bin/sh
case "$*" in
*swapusage*) echo 'total = 2048.00M  used = 1024.00M  free = 1024.00M  (encrypted)' ;;
*loadavg*) echo '{ 1.50 2.50 3.50 }' ;;
esac
STUB
  cat >"$VOL/bin/vm_stat" <<'STUB'
#!/bin/sh
echo 'Mach Virtual Memory Statistics: (page size of 16384 bytes)'
echo 'Pages free:                               100.'
echo 'Pages wired down:                         1000.'
STUB
  chmod +x "$VOL/bin/"*
  export PATH="$VOL/bin:$PATH"
}

teardown() { rm -rf "$VOL"; }

@test "a new panic report is copied to the retained directory" {
  echo crash-1 >"$HF_DIAG_DIR/Kernel-2026-11-03-033000.panic"
  echo ignored >"$HF_DIAG_DIR/other.ips"
  run bash "$HF_SCRIPT" panics
  [ "$status" -eq 0 ]
  [ "$(cat "$HF_STATE_DIR/panics/Kernel-2026-11-03-033000.panic")" = crash-1 ]
  [ ! -e "$HF_STATE_DIR/panics/other.ips" ]
  [[ $output == *"copied panic report Kernel-2026-11-03-033000.panic"* ]]
}

@test "a retained report is never overwritten, a later new one is added" {
  echo crash-1 >"$HF_DIAG_DIR/a.panic"
  bash "$HF_SCRIPT" panics
  echo changed >"$HF_DIAG_DIR/a.panic"
  echo crash-2 >"$HF_DIAG_DIR/b.panic"
  run bash "$HF_SCRIPT" panics
  [ "$status" -eq 0 ]
  [ "$(cat "$HF_STATE_DIR/panics/a.panic")" = crash-1 ]
  [ "$(cat "$HF_STATE_DIR/panics/b.panic")" = crash-2 ]
  [[ $output == *"panics: 1 new"* ]]
}

@test "a missing diagnostics directory is not an error" {
  rmdir "$HF_DIAG_DIR"
  run bash "$HF_SCRIPT" panics
  [ "$status" -eq 0 ]
  [[ $output == *"nothing to copy"* ]]
}

@test "the snapshot records free disk, swap, load and wired memory" {
  run bash "$HF_SCRIPT" snapshot
  [ "$status" -eq 0 ]
  row="$(tail -n 1 "$HF_STATE_DIR/snapshots.tsv")"
  IFS=$'\t' read -r _t disk swap load wired <<<"$row"
  [ "$disk" = 12345 ]
  [[ $swap == *"used = 1024.00M"* ]]
  [ "$load" = "1.50 2.50 3.50" ]
  [ "$wired" -eq $((1000 * 16384)) ]
}

@test "the snapshot is taken once per day" {
  bash "$HF_SCRIPT" snapshot
  run bash "$HF_SCRIPT" snapshot
  [ "$status" -eq 0 ]
  [[ $output == *"already taken"* ]]
  [ "$(grep -vc '^#' "$HF_STATE_DIR/snapshots.tsv")" -eq 1 ]
}

@test "an older day's line does not block today's snapshot" {
  mkdir -p "$HF_STATE_DIR"
  printf '2020-01-01T03:00:00+0000\t1\t\t\t\n' >"$HF_STATE_DIR/snapshots.tsv"
  bash "$HF_SCRIPT" snapshot
  [ "$(wc -l <"$HF_STATE_DIR/snapshots.tsv")" -eq 3 ] || [ "$(wc -l <"$HF_STATE_DIR/snapshots.tsv")" -eq 2 ]
  tail -n 1 "$HF_STATE_DIR/snapshots.tsv" | grep -q "^$(date +%Y-%m-%d)"
}

@test "run does both, and every log line is timestamped" {
  echo c >"$HF_DIAG_DIR/a.panic"
  run bash "$HF_SCRIPT" run
  [ "$status" -eq 0 ]
  [ -f "$HF_STATE_DIR/panics/a.panic" ] && [ -f "$HF_STATE_DIR/snapshots.tsv" ]
  while IFS= read -r l; do
    [[ $l =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9:]{8}Z\ host-forensics: ]] || { echo "bad: $l" >&2; return 1; }
  done <<<"$output"
}

@test "an unknown subcommand exits 2" {
  run bash "$HF_SCRIPT" bogus
  [ "$status" -eq 2 ]
}
