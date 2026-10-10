#!/usr/bin/env bats
# bats file_tags=type:integration
# Guard for rule J-1 (beads-lifecycle skill) in the pb plugin's skills and commands (pg2-dyztr).
#
# `bd ... --json` emits {"schema_version":..,"data":[..]} only when BD_JSON_ENVELOPE=1; a session
# that does not inherit the variable (pg-router-dispatched workers, launchd, nix sandboxes) gets a
# bare array. A jq filter that indexes `.data` without the J-1 prelude therefore errors there.
#
# Two layers:
#   1. LINT: no runnable jq filter under claude-marketplace/pb indexes `.data` without the J-1
#      prelude, and every prose `.data` instruction names the bare-array shape too. Mutants prove
#      the lint bites.
#   2. REAL bd: the documented commands are EXTRACTED FROM THE MARKDOWN (not retyped here) and run
#      against the real `bd` over a throwaway embedded-Dolt workspace (`bd init` in a temp dir with
#      an `env -i` environment: no shared dolt server, no port 25252, no `bd dolt start`), once with
#      BD_JSON_ENVELOPE unset (bare array) and once with BD_JSON_ENVELOPE=1 (envelope).
#
# Environment (set by the flake check, or by hand for a local run):
#   PBJQ_PB_DIR  path to claude-marketplace/pb
#   bd, jq, rg, git, awk on PATH

PRELUDE='(if type=="object" and has("data") then .data else . end)'

# Hermetic bd environment: nothing inherited, so no BEADS_DIR / dolt-server settings can leak in.
# $BD_SHAPE: "bare" (BD_JSON_ENVELOPE unset) or "envelope" (BD_JSON_ENVELOPE=1).
bdenv() {
  local shape=()
  [ "${BD_SHAPE:-bare}" = envelope ] && shape=(BD_JSON_ENVELOPE=1)
  env -i PATH="$PATH" HOME="$FIX/home" TMPDIR="$FIX" BEADS_DOLT_AUTO_START=0 \
    BD_NON_INTERACTIVE=1 BD_BACKUP_ENABLED=0 "${shape[@]}" "$@"
}

setup_file() {
  : "${PBJQ_PB_DIR:?}"
  FIX="$(mktemp -d "${TMPDIR:-/tmp}/pbjq.XXXXXX")"
  export FIX
  mkdir -p "$FIX/home" "$FIX/ws"
  git -C "$FIX/ws" init -q
  cd "$FIX/ws" || return 1
  bdenv bd init -p tst --non-interactive --quiet >/dev/null 2>&1
  bdenv bd create "curated packet" --id tst-cur --type task --description "needle in the body" \
    --metadata '{"pd_curated_rev":3}' >/dev/null
  bdenv bd create "uncurated bead" --id tst-plain --type task >/dev/null
  bdenv bd create "parked bead" --id tst-park --type task --notes "Promoted P2->P1" >/dev/null
  bdenv bd comments add tst-park "PRECONDITION-KEY: nb-opens-gradle-root" >/dev/null
  bdenv bd create "finished sibling" --id tst-done --type task >/dev/null
  bdenv bd close tst-done >/dev/null
}

teardown_file() {
  rm -rf "$FIX"
}

# Print the documented command found at the first line of $1 matching fixed string $2, plus the
# $3-1 lines after it (default 1). When that text carries an inline `bd ...` code span (prose),
# only the span is returned; otherwise the line(s) are returned verbatim, left-trimmed.
grab() {
  local file=$1 pat=$2 n=${3:-1} text span
  text="$(grep -F -m1 -A "$((n - 1))" -- "$pat" "$PBJQ_PB_DIR/$file" | sed -E 's/^[[:space:]]+//')"
  [ -n "$text" ] || {
    echo "grab: pattern not found in $file: $pat" >&2
    return 1
  }
  span="$(printf '%s\n' "$text" | rg -o '`bd [^`]+`' | head -1 | tr -d '`' || true)"
  if [ -n "$span" ]; then printf '%s\n' "$span"; else printf '%s\n' "$text"; fi
}

# Run documented command $1 in the fixture workspace under the current $BD_SHAPE, substituting
# the <placeholders> from the remaining "name=value" args.
runcmd() {
  local cmd=$1 kv
  shift
  for kv in "$@"; do cmd="${cmd//"<${kv%%=*}>"/${kv#*=}}"; done
  cd "$FIX/ws" || return 1
  run bdenv bash -c "$cmd"
}

# <command> [name=value ...]: exit 0 and identical output in the bare-array and envelope shapes.
both_shapes() {
  local out
  BD_SHAPE=bare runcmd "$@"
  [ "$status" -eq 0 ] || {
    echo "bare-array shape failed ($status): $output" >&2
    return 1
  }
  out="$output"
  BD_SHAPE=envelope runcmd "$@"
  [ "$status" -eq 0 ] || {
    echo "envelope shape failed ($status): $output" >&2
    return 1
  }
  [ "$out" = "$output" ] || {
    echo "shape mismatch: bare=[$out] envelope=[$output]" >&2
    return 1
  }
}

# --- lint ---------------------------------------------------------------------------------------

# Print every offending line under directory $1; exit 1 when any. POSIX awk (BSD awk safe).
lint() {
  local f rc=0
  while IFS= read -r f; do
    awk -v prelude='\\(if type=="object" and has\\("data"\\) then \\.data else \\. end\\)' '
      { raw[NR] = $0 }
      END {
        for (i = 1; i <= NR; i++) {
          line = raw[i]
          gsub(prelude, "", line)
          if (line !~ /\.data([^A-Za-z0-9_]|$)/) continue
          if (line ~ /jq/) {
            printf "%s:%d: jq filter indexes .data without the J-1 prelude: %s\n", FILENAME, i, raw[i]; bad = 1; continue
          }
          ok = 0
          for (j = i - 2; j <= i + 2; j++) if (j >= 1 && j <= NR && raw[j] ~ /J-1|bare/) ok = 1
          if (!ok) { printf "%s:%d: prose names .data but not the bare-array shape: %s\n", FILENAME, i, raw[i]; bad = 1 }
        }
        exit bad
      }' "$f" || rc=1
  done < <(find "$1" -name '*.md' | sort)
  return $rc
}

# Literal replace of first occurrence of $2 by $3 in file $1; fails when $2 is absent.
mutate() {
  awk -v pat="$2" -v rep="$3" '
    !done && (i = index($0, pat)) { $0 = substr($0, 1, i - 1) rep substr($0, i + length(pat)); done = 1 }
    { print }
    END { exit done ? 0 : 3 }
  ' "$1" >"$1.new"
  mv "$1.new" "$1"
}

mutant_dir() {
  M="$(mktemp -d "$FIX/mut.XXXXXX")"
  cp -R "$PBJQ_PB_DIR/." "$M/"
  chmod -R u+w "$M"
}

@test "lint: real pb files carry the J-1 prelude on every runnable filter" {
  run lint "$PBJQ_PB_DIR"
  [ "$status" -eq 0 ] || echo "$output" >&2
  [ "$status" -eq 0 ]
}

@test "lint mutant: bare .data[0] in the curated-packet check is caught" {
  mutant_dir
  mutate "$M/skills/drain-one/references/delegate.md" "${PRELUDE}[0].metadata.pd_curated_rev" ".data[0].metadata.pd_curated_rev"
  run lint "$M"
  [ "$status" -eq 1 ]
  [[ "$output" == *"delegate.md"* ]]
}

@test "lint mutant: bare .data in the step 5 repeat-key probe is caught" {
  mutant_dir
  mutate "$M/skills/drain-stuck/SKILL.md" "${PRELUDE}[0] | (.comments" "(.data[0].comments"
  run lint "$M"
  [ "$status" -eq 1 ]
  [[ "$output" == *"drain-stuck/SKILL.md"* ]]
}

@test "lint mutant: bare .data in the gate absence-proof recipe is caught" {
  mutant_dir
  mutate "$M/skills/pb-gate-lifecycle/SKILL.md" "$PRELUDE | length > 0" "(.data // []) | length > 0"
  run lint "$M"
  [ "$status" -eq 1 ]
}

@test "lint mutant: prose .data[] without the bare-array shape is caught" {
  mutant_dir
  mutate "$M/commands/drain-beads.md" 'walk the result array (`.data[]` under the `{"data":[…]}` envelope, the bare top-level `[]` when `BD_JSON_ENVELOPE` is unset — J-1)' 'walk `.data[]`'
  run lint "$M"
  [ "$status" -eq 1 ]
  [[ "$output" == *"drain-beads.md"* ]]
}

# --- real bd, hermetic ----------------------------------------------------------------------------

@test "fixture is the throwaway database, not the shared tracker" {
  cd "$FIX/ws" || return 1
  BD_SHAPE=bare run bdenv bash -c "bd list --status all -n 0 --json | jq -r '$PRELUDE | map(.id) | sort | join(\",\")'"
  [ "$status" -eq 0 ]
  [ "$output" = "tst-cur,tst-done,tst-park,tst-plain" ]
}

@test "shape control: the legacy .data[0] form errors on a bare array and works under the envelope" {
  cd "$FIX/ws" || return 1
  BD_SHAPE=bare run bdenv bash -c "bd show tst-plain --json | jq -r '.data[0].id'"
  [ "$status" -ne 0 ]
  BD_SHAPE=envelope run bdenv bash -c "bd show tst-plain --json | jq -r '.data[0].id'"
  [ "$status" -eq 0 ]
  [ "$output" = "tst-plain" ]
}

@test "delegate.md curated-packet check: non-null for a packet, null otherwise, same in both shapes" {
  cmd="$(grab skills/drain-one/references/delegate.md "Curated-packet check" 2)"
  [ -n "$cmd" ]
  both_shapes "$cmd" id=tst-cur
  [ "$output" = "3" ]
  both_shapes "$cmd" id=tst-plain
  [ "$output" = "null" ]
}

@test "delegate.md stamp-refusal metadata read agrees in both shapes" {
  cmd="$(grab skills/drain-one/references/delegate.md "{parent, metadata}")"
  both_shapes "$cmd" id=tst-cur
  [ "$(jq -r '.metadata.pd_curated_rev' <<<"$output")" = "3" ]
}

@test "drain-stuck sibling-open? probe reads the status in both shapes" {
  cmd="$(grab skills/drain-stuck/SKILL.md 'check: `bd show <sib>')"
  both_shapes "$cmd" sib=tst-done
  [ "$output" = "closed" ]
  both_shapes "$cmd" sib=tst-plain
  [ "$output" = "open" ]
}

@test "drain-stuck STUCK step 5 probe prints the prior PRECONDITION-KEY in both shapes" {
  cmd="$(grab skills/drain-stuck/SKILL.md "PRECONDITION-KEY: .*'")"
  [[ "$cmd" == *"--include-comments"* ]]
  both_shapes "$cmd" id=tst-park
  [ "$output" = "PRECONDITION-KEY: nb-opens-gradle-root" ]
}

@test "drain-stuck STUCK step 5 probe finds nothing (rg exit 1) on a bead with no key, in both shapes" {
  cmd="$(grab skills/drain-stuck/SKILL.md "PRECONDITION-KEY: .*'")"
  for shape in bare envelope; do
    BD_SHAPE=$shape runcmd "$cmd" id=tst-plain
    [ "$status" -eq 1 ]
    [ -z "$output" ]
  done
}

@test "step 5 mutant: dropping --include-comments blinds the probe (the test would notice)" {
  cmd="$(grab skills/drain-stuck/SKILL.md "PRECONDITION-KEY: .*'")"
  cmd="${cmd/ --include-comments/}"
  [[ "$cmd" != *"--include-comments"* ]]
  for shape in bare envelope; do
    BD_SHAPE=$shape runcmd "$cmd" id=tst-park
    [ "$status" -eq 1 ]
    [ -z "$output" ]
  done
}

@test "unblock-human-beads comment-text read prints the comment in both shapes" {
  cmd="$(grab commands/unblock-human-beads.md "bd comments <id> --json")"
  both_shapes "$cmd" id=tst-park
  [ "$output" = "PRECONDITION-KEY: nb-opens-gradle-root" ]
}

@test "unblock-human-beads desc-contains read lists the matching bead in both shapes" {
  cmd="$(grab commands/unblock-human-beads.md 'bd list --desc-contains "<SEARCH TERM>"' 2)"
  cmd="${cmd//\\$'\n'/ }"
  cmd="${cmd//$'\n'/ }"
  both_shapes "$cmd" "SEARCH TERM=needle"
  [ "$output" = "tst-cur open curated packet" ]
}

@test "unblock-human-beads promotion-record read prints the note in both shapes" {
  cmd="$(grab commands/unblock-human-beads.md "bd show <id> --json | jq -r '(if type" 1)"
  [[ "$cmd" == *"Promoted"* ]]
  both_shapes "$cmd" id=tst-park
  [ "$output" = "Promoted P2->P1" ]
}

@test "pb-gate-lifecycle absence proof: OK for a bead not in bd ready, FAIL for a workable one, both shapes" {
  recipe="$(awk '/^ *READY="\$\(bd ready --json -n 0\)"/{p=1} p{print} p&&/echo "OK:/{exit}' "$PBJQ_PB_DIR/skills/pb-gate-lifecycle/SKILL.md" | sed -E 's/^[[:space:]]+//')"
  [ -n "$recipe" ]
  for shape in bare envelope; do
    BD_SHAPE=$shape runcmd "$recipe" BEAD=tst-done
    [ "$status" -eq 0 ]
    [[ "$output" == "OK:"* ]]
    BD_SHAPE=$shape runcmd "$recipe" BEAD=tst-plain
    [[ "$output" == "FAIL:"* ]]
  done
}
