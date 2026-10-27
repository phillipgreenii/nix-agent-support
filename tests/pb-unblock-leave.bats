#!/usr/bin/env bats
# bats file_tags=type:integration
# Guard for the LEAVE terminal action and the narrowed DEFER in /pb:unblock-human-beads
# (bead pg2-8gvdfi). Operator rulings (Phillip, 2026-10-27): DEFER is only for a live external
# event (at +1d) or the operator's explicitly stated window; operator unavailability selects
# LEAVE ("do nothing for now"), which keeps the bead in `bd ready --label human`.
#
# Two layers:
#   1. TEXT: assertions 1-7 over the command file. Each negative control mutates a temp copy and
#      MUST fail exactly ONE assertion, so every assertion is shown to bite on its own.
#   2. CONTRACT: the facts the tracking key `(updated_at, comment_count)` depends on, checked
#      against the REAL bd over a throwaway embedded-Dolt workspace (`bd init` in a temp dir with
#      an `env -i` environment: no shared dolt server, no `bd dolt start`). The flake check pins
#      the flake's llm-agents beads, not the machine's bd.
#
# Environment (set by the flake check, or by hand for a local run):
#   PBUL_CMD  path to claude-marketplace/pb/commands/unblock-human-beads.md
#   bd, jq, git (bd init uses it), awk, grep, sed on PATH

PRELUDE='(if type=="object" and has("data") then .data else . end)'
PHRASE='Operator unavailability MUST NOT select DEFER'

# --- text assertions ------------------------------------------------------------------------------

joined() { tr '\n' ' ' <"$1" | tr -s ' '; }

# The lines inside the single mermaid fence.
mermaid() {
  awk '!on && /^```mermaid[ \t]*$/ { on = 1; next } on && /^```[ \t]*$/ { exit } on' "$1"
}

# 1. "four terminal actions" and a LEAVE terminal-action heading exist.
a1() {
  joined "$1" | grep -qF 'four terminal actions' && grep -qE '^- \*\*LEAVE\*\*' "$1"
}

# 2. Every value-like `--defer` (followed by +, < or a digit) is exactly `--defer +1d`.
a2() {
  local bad
  bad="$(grep -oE -- "--defer[ =]+[+<0-9][^ \`\"',.;)|]*" "$1" | grep -vxF -- '--defer +1d' || true)"
  [ -z "$bad" ]
}

# 3. The unavailability phrase is present.
a3() { joined "$1" | grep -qF "$PHRASE"; }

# 4. `--no-engage` is in the frontmatter argument-hint and in "Optional scope arguments".
a4() {
  grep -E '^argument-hint:' "$1" | grep -qF -- '--no-engage' &&
    awk '/^## Optional scope arguments/ { on = 1; next } on && /^## / { exit } on' "$1" |
    grep -qF -- '--no-engage'
}

# 5. The tracking mechanics are present.
a5() {
  local j
  j="$(joined "$1")"
  for s in 'left/<id>.json' '(updated_at, comment_count)' '24 h' 'engage_skipped' \
    'skip tracking for that bead'; do
    printf '%s' "$j" | grep -qF -- "$s" || return 1
  done
}

# 6(i). In bash fences, no `bd create` command (its line plus backslash continuations) has --defer.
a6i() {
  awk '
    !on && /^[ \t]*```bash[ \t]*$/ { on = 1; next }
    on && /^[ \t]*```[ \t]*$/ { on = 0; cont = 0; next }
    on {
      if (cont || $0 ~ /(^|[^A-Za-z])bd create( |$)/) {
        if ($0 ~ /--defer/) bad = 1
        cont = ($0 ~ /\\[ \t]*$/)
      }
    }
    END { exit bad }' "$1"
}

# 6(ii). In the mermaid fence, no quoted label naming `bd create` also names --defer.
a6ii() {
  mermaid "$1" | awk '
    {
      s = $0
      while (match(s, /"[^"]*"/)) {
        lab = substr(s, RSTART, RLENGTH)
        if (lab ~ /bd create/ && lab ~ /--defer/) bad = 1
        s = substr(s, RSTART + RLENGTH)
      }
    }
    END { exit bad }'
}

# 7. The DEFER recognition rule exists, and the flowchart defines DEF and LEAVE exactly once.
a7() {
  local j
  j="$(joined "$1")"
  printf '%s' "$j" | grep -qF 'DEFER recognition rule' &&
    printf '%s' "$j" | grep -qF 'live external event' &&
    [ "$(mermaid "$1" | grep -oE '(^|[^A-Za-z])DEF\[' | wc -l)" -eq 1 ] &&
    [ "$(mermaid "$1" | grep -oE '(^|[^A-Za-z])LEAVE\[' | wc -l)" -eq 1 ]
}

# Print the space-separated ids of the assertions FILE fails (empty when all pass).
failing() {
  local n out=""
  for n in 1 2 3 4 5 6i 6ii 7; do
    "a$n" "$1" >/dev/null 2>&1 || out="$out $n"
  done
  printf '%s\n' "${out# }"
}

# Literal replace of the first occurrence of $2 by $3 in file $1; fails when $2 is absent so a
# stale mutant cannot silently become a no-op.
mutate() {
  awk -v pat="$2" -v rep="$3" '
    !done && (i = index($0, pat)) { $0 = substr($0, 1, i - 1) rep substr($0, i + length(pat)); done = 1 }
    { print }
    END { exit done ? 0 : 3 }
  ' "$1" >"$1.new"
  mv "$1.new" "$1"
}

copy() {
  M="$(mktemp -d "${BATS_TEST_TMPDIR:-${TMPDIR:-/tmp}}/pbul.XXXXXX")/cmd.md"
  cp "$PBUL_CMD" "$M"
  chmod u+w "$M"
}

# --- hermetic bd fixture --------------------------------------------------------------------------

# Nothing inherited, so no BEADS_DIR / dolt-server settings / actor can leak in.
bdenv() {
  env -i PATH="$PATH" HOME="$FIX/hm" TMPDIR="$FIX" BEADS_DOLT_AUTO_START=0 \
    BD_NON_INTERACTIVE=1 BD_BACKUP_ENABLED=0 "$@"
}

inws() { (cd "$FIX/ws" && bdenv "$@"); }

setup_file() {
  : "${PBUL_CMD:?}"
  FIX="$(mktemp -d "${TMPDIR:-/tmp}/pbul.XXXXXX")"
  export FIX
  mkdir -p "$FIX/hm" "$FIX/ws"
  inws bd init -p tst --non-interactive --quiet >/dev/null 2>&1
}

teardown_file() {
  rm -rf "$FIX"
}

# The documented baseline read (from the command file, not retyped), for bead $1.
key_show() {
  local cmd
  cmd="$(grep -F -m1 'bd show <id> --json | jq -c' "$PBUL_CMD" | sed -E 's/^[[:space:]]+//')"
  [ -n "$cmd" ] || return 1
  inws bash -c "${cmd//<id>/$1}"
}

key_ready() {
  inws bd ready --json -n 0 | jq -c --arg id "$1" "${PRELUDE}[] | select(.id == \$id) | {updated_at, comment_count}"
}

field() { jq -r ".$1" <<<"$2"; }

# The LEAVE release, as the command file documents it, for bead $1.
leave() {
  grep -qF 'bd update <id> --status open --assignee "" --actor "ID"' "$PBUL_CMD"
  inws bd update "$1" --status open --assignee "" --actor tst-unblock >/dev/null
}

# --- text tests -----------------------------------------------------------------------------------

@test "the command file passes every text assertion" {
  run failing "$PBUL_CMD"
  [ "$status" -eq 0 ]
  echo "failing: [$output]"
  [ -z "$output" ]
}

@test "the worktree-review follow-up bd create carries no --defer in its bash fence" {
  grep -qF 'bd create "worktree-review: reconcile leftover isolation for <id>" \' "$PBUL_CMD"
  a6i "$PBUL_CMD"
}

@test "control: --defer +7d on a non-create bash command fails assertion 2 only" {
  copy
  mutate "$M" '--add-label planning-session-required --status open' \
    '--add-label planning-session-required --defer +7d --status open'
  run failing "$M"
  [ "$output" = "2" ]
}

@test "control: --defer +1d on a bash bd create line fails assertion 6(i) only" {
  copy
  mutate "$M" 'bd create "worktree-review: reconcile leftover isolation for <id>" \' \
    'bd create "worktree-review: reconcile leftover isolation for <id>" --defer +1d \'
  run failing "$M"
  [ "$output" = "6i" ]
}

@test "control: --defer in a mermaid bd create label fails assertion 6(ii) only" {
  copy
  mutate "$M" '--labels human,worktree-review, if a worktree is left' \
    '--labels human,worktree-review --defer, if a worktree is left'
  run failing "$M"
  [ "$output" = "6ii" ]
}

@test "control: deleting every line with the unavailability phrase fails assertion 3 only" {
  copy
  sed "/$PHRASE/d" "$PBUL_CMD" >"$M"
  run cmp -s "$PBUL_CMD" "$M"
  [ "$status" -ne 0 ]
  run failing "$M"
  [ "$output" = "3" ]
}

@test "control: dropping --no-engage from argument-hint fails assertion 4 only" {
  copy
  sed '/^argument-hint:/s/--no-engage//g' "$PBUL_CMD" >"$M"
  run cmp -s "$PBUL_CMD" "$M"
  [ "$status" -ne 0 ]
  run failing "$M"
  [ "$output" = "4" ]
}

@test "control: deleting every line with engage_skipped fails assertion 5 only" {
  copy
  sed '/engage_skipped/d' "$PBUL_CMD" >"$M"
  run cmp -s "$PBUL_CMD" "$M"
  [ "$status" -ne 0 ]
  run failing "$M"
  [ "$output" = "5" ]
}

# --- contract tests (real bd) ---------------------------------------------------------------------

@test "fixture is the throwaway database, not the shared tracker" {
  run inws bd list --status all -n 0 --json
  [ "$status" -eq 0 ]
  [ "$(jq -r "$PRELUDE | length" <<<"$output")" -eq 0 ] ||
    [ "$(jq -r "$PRELUDE | map(.id) | all(startswith(\"tst-\"))" <<<"$output")" = "true" ]
}

@test "contract (a): bd ready and bd show rows carry updated_at and comment_count" {
  inws bd create "ready row" --id tst-a --type task --labels human >/dev/null
  run key_show tst-a
  [ "$status" -eq 0 ]
  [ "$(jq -r 'has("updated_at") and has("comment_count")' <<<"$output")" = "true" ]
  [ "$(field updated_at "$output")" != "null" ]
  run key_ready tst-a
  [ "$status" -eq 0 ]
  [ -n "$output" ]
  [ "$(jq -r 'has("updated_at") and has("comment_count")' <<<"$output")" = "true" ]
  [ "$(field comment_count "$output")" != "null" ]
}

@test "contract (b): adding a comment raises comment_count" {
  inws bd create "commented" --id tst-b --type task --labels human >/dev/null
  before="$(key_show tst-b)"
  sleep 2 # bd timestamps are second-granularity
  inws bd comment tst-b "probe comment" --actor tst-unblock >/dev/null
  after="$(key_show tst-b)"
  [ "$(field comment_count "$after")" -eq "$(($(field comment_count "$before") + 1))" ]
  # Informational only: the key changes either way.
  if [ "$(field updated_at "$after")" = "$(field updated_at "$before")" ]; then
    echo "# info: a comment does NOT bump updated_at" >&3
  else
    echo "# info: a comment bumps updated_at" >&3
  fi
}

@test "contract (c): a LEAVE release of a claimed bead changes updated_at" {
  inws bd create "claimed" --id tst-c --type task --labels human >/dev/null
  inws bd update tst-c --claim --actor tst-unblock >/dev/null
  claimed="$(key_show tst-c)"
  sleep 2 # bd timestamps are second-granularity
  leave tst-c
  released="$(key_show tst-c)"
  [ "$(field updated_at "$claimed")" != "$(field updated_at "$released")" ]
}

@test "contract (d): after a release the bd ready row and bd show agree on the key" {
  inws bd create "agreement" --id tst-d --type task --labels human >/dev/null
  inws bd update tst-d --claim --actor tst-unblock >/dev/null
  leave tst-d
  shown="$(key_show tst-d)"
  ready="$(key_ready tst-d)"
  [ -n "$ready" ]
  [ "$(jq -cS . <<<"$shown")" = "$(jq -cS . <<<"$ready")" ]
}
