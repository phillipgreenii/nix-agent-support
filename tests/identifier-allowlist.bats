#!/usr/bin/env bats
# bats file_tags=type:unit
# Regression guard for scripts/check-identifier-allowlist.sh, the body of the
# `identifier-allowlist` hook (bead pg2-34s486), which replaced six drifting per-module Go
# copies of the identifier allowlist guard (pg-connector, pg-decider, pg-desk,
# pg-router-source-pg-desk and work-report; pg-desk-shadow was a sixth until it was
# deleted, PR lane refactor packet P9a-3).
#
# Deterministic logic against the REAL script, the REAL ripgrep (rg -P) and a REAL git, in
# a hermetic fixture from the shared git fixture harness (global rule V-3). The fixture
# mirrors the five guarded modules with just enough files to satisfy the script's liveness
# canaries and carries a copy of the REAL scripts/identifier-allowlists/, so the shipped
# allowlist entries (including each module's own extras) are under test, not a stand-in.
# Every identifier planted here is an obviously fake placeholder.

# GFH_LIB is the one required file (tc-4ehow): no filesystem search.
if [[ -z ${GFH_LIB:-} ]]; then
  echo "GFH_LIB is not set: commit via the run-unit-tests hook, run under the workspace .envrc (direnv, or direnv exec <workspace-root> ...), or export GFH_LIB=<path to git-fixture-harness.bash>." >&2
  exit 1
fi
GFH_HARNESS_FILE="$GFH_LIB"
# shellcheck disable=SC1090
source "$GFH_HARNESS_FILE"

SCRIPT="$BATS_TEST_DIRNAME/../scripts/check-identifier-allowlist.sh"
REAL_ALLOWLISTS="$BATS_TEST_DIRNAME/../scripts/identifier-allowlists"
SUITE="identifier-allowlist"

# fill <dir> <count> <name-prefix> <ext> -- <count> clean files in <dir>.
fill() {
  local dir="$1" count="$2" prefix="$3" ext="$4" i paths=()
  mkdir -p "$ROOT/$dir"
  for i in $(seq 1 "$count"); do
    paths+=("$ROOT/$dir/$prefix$i.$ext")
  done
  printf '{"login": "teammate"}\n' | tee "${paths[@]}" >/dev/null
}

setup() {
  command -v rg >/dev/null || {
    echo "rg (ripgrep with PCRE2) is required on PATH" >&2
    return 1
  }
  gfh_setup "$SUITE"
  ROOT="$GFH_REPO"

  mkdir -p "$ROOT/scripts"
  cp -R "$REAL_ALLOWLISTS" "$ROOT/scripts/identifier-allowlists"

  # pg-connector: the two required goldens plus filler to reach the minimum.
  mkdir -p "$ROOT/packages/pg-connector/pkg/scriptout/conformance/testdata/golden"
  printf '{"ok": true}\n' >"$ROOT/packages/pg-connector/pkg/scriptout/conformance/testdata/golden/response-success.json"
  printf '{"ok": false}\n' >"$ROOT/packages/pg-connector/pkg/scriptout/conformance/testdata/golden/error.json"
  fill packages/pg-connector/cmd/pg-connector/testdata 70 f json
  # pg-decider.
  fill packages/pg-decider/cmd/pg-decider/testdata 210 f json
  mkdir -p "$ROOT/packages/pg-decider/testdata"
  printf '{"ok": true}\n' >"$ROOT/packages/pg-decider/testdata/pr_view_minimal.json"
  # pg-desk: the example config plus 30 focus fixtures plus filler.
  mkdir -p "$ROOT/packages/pg-desk/internal/config/testdata"
  printf 'self_login: phillipgreenii\n' >"$ROOT/packages/pg-desk/internal/config/testdata/config.example.yaml"
  fill packages/pg-desk/internal/focus/testdata 30 g json
  fill packages/pg-desk/cmd/pg-desk/testdata 45 f json
  # pg-router-source-pg-desk.
  mkdir -p "$ROOT/packages/pg-router-source-pg-desk/cmd/pg-router-source-pg-desk/testdata"
  fill packages/pg-router-source-pg-desk/cmd/pg-router-source-pg-desk/testdata 6 f json
  printf '{"ok": true}\n' >"$ROOT/packages/pg-router-source-pg-desk/cmd/pg-router-source-pg-desk/testdata/envelope_degraded.json"
  # work-report: .go files, testdata files, docs files and docs/behavior files.
  fill packages/work-report/internal/x 45 f go
  fill packages/work-report/internal/x/testdata 6 t json
  fill packages/work-report/docs/behavior 5 b md
  fill packages/work-report/docs/design 2 d md

  # Out of scope on purpose.
  fill packages/other/testdata 3 f json
  printf 'package other\n' >"$ROOT/packages/other/o.go"

  git -C "$ROOT" add -A
  git -C "$ROOT" commit -q -m "fixture"
}

teardown() {
  gfh_teardown
}

# plant <relpath> <content...> -- writes the lines to <relpath> (replacing any file) and stages it.
plant() {
  local rel="$1"
  shift
  mkdir -p "$ROOT/$(dirname "$rel")"
  printf '%s\n' "$@" >"$ROOT/$rel"
  git -C "$ROOT" add -A
}

run_guard() {
  run bash "$SCRIPT" --root "$ROOT" "$@"
}

CONN=packages/pg-connector/cmd/pg-connector/testdata
DESK=packages/pg-desk/cmd/pg-desk/testdata

@test "clean fixture passes and reports what it scanned" {
  run_guard
  [ "$status" -eq 0 ]
  [[ $output == *"OK:"*"5 module(s)"* ]]
}

@test "json shape: each identity key with a non-allowlisted value is flagged with file:line" {
  local key
  for key in login author reviewer user; do
    plant "$CONN/planted.json" '{' "  \"$key\": \"zzfakehandle\"" '}'
    run_guard
    [ "$status" -eq 1 ]
    [[ $output == *"pg-connector: $CONN/planted.json:2: json: \"zzfakehandle\""* ]]
  done
}

@test "json shape: a nested author login and a spaced colon are flagged" {
  plant "$CONN/planted.json" '{"author": {"login"  :   "zzfakehandle"}}'
  run_guard
  [ "$status" -eq 1 ]
  [[ $output == *"planted.json:1: json: \"zzfakehandle\""* ]]
}

@test "branch shape: a name.name.TICKET-NNN component is flagged" {
  plant "$CONN/planted.json" '{"headRefName": "zzfake.handle.PROJ-9.fix"}'
  run_guard
  [ "$status" -eq 1 ]
  [[ $output == *"planted.json:1: branch: \"zzfake.handle\""* ]]
}

@test "trailer shape: Author and Committer lines are flagged" {
  plant "$CONN/planted.json" 'commit abc' 'Author: Zz Fake <zz@example.test>' 'Committer: Zz Other <zo@example.test>'
  run_guard
  [ "$status" -eq 1 ]
  [[ $output == *"planted.json:2: trailer: \"Zz Fake\""* ]]
  [[ $output == *"planted.json:3: trailer: \"Zz Other\""* ]]
}

@test "allowlisted tokens pass, whatever the case and surrounding whitespace" {
  plant "$CONN/safe.json" \
    '{"author": {"login": "phillipgreenii"}}' \
    '{"reviewer": "phillipg@ziprecruiter.com"}' \
    '{"user": {"login": "REVIEW-BOT"}}' \
    '{"login": "  Teammate   "}' \
    '{"login":     "teammate"}' \
    'Author: phillipgreenii <phillipg@ziprecruiter.com>' \
    'Committer:    TEAMMATE   <t@example.test>' \
    '{"branch": "teammate.PROJ-42.fix"}'
  run_guard
  [ "$status" -eq 0 ]
}

@test "an allowlisted name that merely starts or ends a longer token is flagged" {
  plant "$CONN/planted.json" '{"login": "teammate-two"}' '{"user": "xteammate"}' 'Author: Teammate Smith <t@example.test>'
  run_guard
  [ "$status" -eq 1 ]
  [[ $output == *"planted.json:1: json: \"teammate-two\""* ]]
  [[ $output == *"planted.json:2: json: \"xteammate\""* ]]
  [[ $output == *"planted.json:3: trailer: \"Teammate Smith\""* ]]
}

@test "empty and blank values are not flagged; unrelated keys are not flagged" {
  plant "$CONN/safe.json" '{"login": ""}' '{"user": "   "}' '{"name": "zzfakehandle"}' 'Author: <x@example.test>'
  run_guard
  [ "$status" -eq 0 ]
}

@test "the same token twice in one file is reported once, in another file again" {
  plant "$CONN/a.json" '{"login": "zzfakehandle"}' '{"user": "zzfakehandle"}'
  plant "$CONN/b.json" '{"login": "zzfakehandle"}'
  run_guard
  [ "$status" -eq 1 ]
  [ "$(grep -c 'a.json' <<<"$output")" -eq 1 ]
  [[ $output == *"b.json:1: json"* ]]
}

@test "every planted file is listed, not just the first" {
  plant "$CONN/a.json" '{"login": "zzfakeone"}'
  plant "$DESK/b.json" '{"login": "zzfaketwo"}'
  run_guard
  [ "$status" -eq 1 ]
  [[ $output == *"pg-connector: $CONN/a.json:1"* ]]
  [[ $output == *"pg-desk: $DESK/b.json:1"* ]]
}

@test "allowlists are per module: alice-dev passes only in pg-desk" {
  plant "$DESK/ok.json" '{"login": "alice-dev"}'
  run_guard
  [ "$status" -eq 0 ]
  plant packages/pg-decider/cmd/pg-decider/testdata/bad.json '{"login": "alice-dev"}'
  run_guard
  [ "$status" -eq 1 ]
  [[ $output == *"pg-decider: packages/pg-decider/cmd/pg-decider/testdata/bad.json:1: json: \"alice-dev\""* ]]
}

@test "allowlists are per module: work-report's bots and names pass only there" {
  local who
  for who in dependabot policy-bot alice bob; do
    plant "packages/work-report/internal/x/testdata/ok-$who.json" "{\"login\": \"$who\"}"
  done
  run_guard
  [ "$status" -eq 0 ]
  plant "$DESK/bad.json" '{"login": "dependabot"}'
  run_guard
  [ "$status" -eq 1 ]
  [[ $output == *"pg-desk: "*"bad.json:1: json: \"dependabot\""* ]]
}

@test "the operator email is not on work-report's allowlist (carried over exactly)" {
  plant packages/work-report/internal/x/testdata/mail.json '{"reviewer": "phillipg@ziprecruiter.com"}'
  run_guard
  [ "$status" -eq 1 ]
  [[ $output == *"work-report: "*"mail.json:1: json: \"phillipg@ziprecruiter.com\""* ]]
}

@test "scope: a testdata module ignores files outside testdata/ and outside the module" {
  plant packages/pg-connector/cmd/pg-connector/not_fixture.go '{"login": "zzfakehandle"}'
  plant packages/pg-connector/README.md '{"login": "zzfakehandle"}'
  plant packages/other/testdata/x.json '{"login": "zzfakehandle"}'
  plant packages/other/o.go '{"login": "zzfakehandle"}'
  run_guard
  [ "$status" -eq 0 ]
}

@test "scope: work-report is wider, covering .go, testdata and docs files" {
  plant packages/work-report/internal/x/planted.go '/*' '{"login": "zzfakehandle"}' '*/'
  plant packages/work-report/internal/x/testdata/planted.json '{"login": "zzfakehandle"}'
  plant packages/work-report/docs/design/planted.md '{"login": "zzfakehandle"}'
  plant packages/work-report/docs/behavior/planted.md '{"login": "zzfakehandle"}'
  run_guard
  [ "$status" -eq 1 ]
  [[ $output == *"packages/work-report/internal/x/planted.go:2: json"* ]]
  [[ $output == *"packages/work-report/internal/x/testdata/planted.json:1: json"* ]]
  [[ $output == *"packages/work-report/docs/design/planted.md:1: json"* ]]
  [[ $output == *"packages/work-report/docs/behavior/planted.md:1: json"* ]]
}

@test "scope: work-report ignores a non-go file outside testdata and docs" {
  plant packages/work-report/README.md '{"login": "zzfakehandle"}'
  plant packages/work-report/notes.txt '{"login": "zzfakehandle"}'
  run_guard
  [ "$status" -eq 0 ]
}

@test "scope: pg-desk's focus testdata is guarded" {
  plant packages/pg-desk/internal/focus/testdata/snapshots/github-pr/planted.json '{"author": {"login": "zzfakehandle"}}'
  run_guard
  [ "$status" -eq 1 ]
  [[ $output == *"pg-desk: packages/pg-desk/internal/focus/testdata/snapshots/github-pr/planted.json:1: json"* ]]
}

@test "scope: every module is guarded (one planted token per module)" {
  plant packages/pg-connector/pkg/scriptout/conformance/testdata/golden/p.json '{"login": "zzfakehandle"}'
  plant packages/pg-decider/testdata/p.json '{"login": "zzfakehandle"}'
  plant packages/pg-desk/internal/config/testdata/p.json '{"login": "zzfakehandle"}'
  plant packages/pg-router-source-pg-desk/cmd/pg-router-source-pg-desk/testdata/p.json '{"login": "zzfakehandle"}'
  plant packages/work-report/internal/x/testdata/p.json '{"login": "zzfakehandle"}'
  run_guard
  [ "$status" -eq 1 ]
  local m
  for m in pg-connector pg-decider pg-desk pg-router-source-pg-desk work-report; do
    [[ $output == *"check-identifier-allowlist: $m: packages/$m/"* ]]
  done
}

@test "an untracked planted file is not scanned (hooks only see git-known files)" {
  printf '{"login": "zzfakehandle"}\n' >"$ROOT/$CONN/untracked.json"
  run_guard
  [ "$status" -eq 0 ]
}

@test "a binary file in scope is skipped" {
  printf '{"login": "zz\0fake"}\n' >"$ROOT/$CONN/blob.bin"
  git -C "$ROOT" add -A
  run_guard
  [ "$status" -eq 0 ]
}

@test "changed files outside every module cost no scan, even when a module is dirty" {
  plant "$CONN/planted.json" '{"login": "zzfakehandle"}'
  run_guard README.md packages/other/o.go
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "a changed file inside a module scans that module only" {
  plant "$CONN/planted.json" '{"login": "zzfakehandle"}'
  plant "$DESK/planted.json" '{"login": "zzfaketwo"}'
  run_guard "$CONN/planted.json"
  [ "$status" -eq 1 ]
  [[ $output == *"pg-connector:"* ]]
  [[ $output != *"pg-desk:"* ]]
}

@test "a changed allowlist or the script itself triggers every module" {
  plant "$DESK/planted.json" '{"login": "zzfaketwo"}'
  run_guard scripts/identifier-allowlists/pg-connector.txt
  [ "$status" -eq 1 ]
  [[ $output == *"pg-desk:"* ]]
  run_guard scripts/check-identifier-allowlist.sh
  [ "$status" -eq 1 ]
}

@test "canary: a module emptied of files fails loudly instead of passing" {
  git -C "$ROOT" rm -rq packages/pg-decider
  run_guard
  [ "$status" -eq 1 ]
  [[ $output == *"canary: pg-decider: never scanned packages/pg-decider/testdata/pr_view_minimal.json"* ]]
  [[ $output == *"canary: pg-decider: scanned only 0 file(s), expected at least 200"* ]]
}

@test "canary: a missing required fixture fails" {
  git -C "$ROOT" rm -q packages/pg-connector/pkg/scriptout/conformance/testdata/golden/error.json
  run_guard
  [ "$status" -eq 1 ]
  [[ $output == *"canary: pg-connector: never scanned packages/pg-connector/pkg/scriptout/conformance/testdata/golden/error.json"* ]]
}

@test "canary: a module scanned below its minimum file count fails" {
  local d=packages/pg-router-source-pg-desk/cmd/pg-router-source-pg-desk/testdata
  git -C "$ROOT" rm -q "$d/f1.json" "$d/f2.json" "$d/f3.json"
  run_guard
  [ "$status" -eq 1 ]
  [[ $output == *"canary: pg-router-source-pg-desk: scanned only 4 file(s), expected at least 5"* ]]
}

@test "canary: pg-desk losing its focus testdata fails" {
  git -C "$ROOT" rm -rq packages/pg-desk/internal/focus
  run_guard
  [ "$status" -eq 1 ]
  [[ $output == *"canary: pg-desk: scanned only 0 file(s) for internal/focus/testdata/, expected at least 25"* ]]
}

@test "canary: work-report losing its docs/behavior files fails" {
  git -C "$ROOT" rm -rq packages/work-report/docs/behavior
  run_guard
  [ "$status" -eq 1 ]
  [[ $output == *"canary: work-report: scanned only 0 file(s) for docs/behavior/, expected at least 4"* ]]
}

@test "canary: work-report with too few .go files fails" {
  git -C "$ROOT" rm -rq packages/work-report/internal/x/f1*.go packages/work-report/internal/x/f2*.go packages/work-report/internal/x/f3*.go
  run_guard
  [ "$status" -eq 1 ]
  [[ $output == *"canary: work-report: scanned only"*"kind:go"* ]]
}

@test "config: a missing allowlist file fails" {
  git -C "$ROOT" rm -q scripts/identifier-allowlists/pg-desk.txt
  run_guard
  [ "$status" -eq 1 ]
  [[ $output == *"pg-desk: allowlist file scripts/identifier-allowlists/pg-desk.txt is missing"* ]]
}

@test "config: an allowlist with no entries fails instead of allowing everything" {
  plant scripts/identifier-allowlists/pg-connector.txt '# only a comment' ''
  run_guard
  [ "$status" -eq 1 ]
  [[ $output == *"pg-connector: allowlist scripts/identifier-allowlists/pg-connector.txt has no entries"* ]]
}

@test "config: regex metacharacters in an allowlist entry are matched literally" {
  plant scripts/identifier-allowlists/pg-connector.txt 'teammate' 'a.b+c'
  plant "$CONN/ok.json" '{"login": "a.b+c"}'
  run_guard
  [ "$status" -eq 0 ]
  plant "$CONN/ok.json" '{"login": "aXb+c"}'
  run_guard
  [ "$status" -eq 1 ]
}

@test "usage: an unknown option exits 2" {
  run bash "$SCRIPT" --nope
  [ "$status" -eq 2 ]
}
