#!/usr/bin/env bats
# bats file_tags=type:unit
#
# The wtaudit CLI as a subprocess (the strict-mode runner that mimics the built
# script), against REAL git (the shared hermetic fixture harness), real lsof
# and a real bd in a throwaway embedded database (bd_fixture.bash). Each
# fake used here says why in its test:
#   - a fake `pn` for discovery (a nonexistent discovered path cannot come from
#     the real pn, and the real workspace must not be scanned by a unit test);
#   - a fake `pnwf` for set parity (a real pnwf needs a whole fixture workspace);
#   - a fake `wtdone` through WTDONE_BIN for the compare-and-swap and
#     teardown-refused tests (the point is a wtdone that misbehaves);
#   - a `git` shim that wraps the REAL git, to move a branch tip in the narrow
#     window between classification and the compare-and-swap delete of a
#     dangling branch (no other hook point exists for that race).
#
# Exit-code matrix (every code of the interface has a named test below):
#   scan   0 "exit-code matrix: scan 0"       2 "...: scan 2"   3 "...: scan 3"
#   remove 0 "...: remove 0"   2 "...: remove 2 (malformed tip / unreadable repo)"
#          10 tip-moved / verdict-changed (became-dirty, anchored, claimed,
#          canonical-checkout, unknown branch)   11 branch-refused, tip-moved-late
#          (worktree candidate and dangling branch)   12 set member
#          13 teardown-refused

load wtaudit_helper

setup_file() { wtaudit_file_setup; }
teardown_file() { wtaudit_file_teardown; }

setup() {
  wtaudit_test_setup
  REPO="$(mk_repo repo)"
  commit_in "$REPO" base.txt "second commit on main"
  git -C "$REPO" worktree list >/dev/null
}

teardown() { wtaudit_test_teardown; }

tip_of() { git -C "$1" rev-parse "refs/heads/$2"; }

# do_remove <repo> <branch> [<tip>]: run remove, capturing status and stdout.
do_remove() {
  run --separate-stderr "$WTAUDIT" remove --repo "$1" --branch "$2" --expect-tip "${3:-$(tip_of "$1" "$2")}"
}

# fake_pnwf <exit> [stdout]
fake_pnwf() {
  mkdir -p "$FX/pnwfbin"
  printf '#!/usr/bin/env bash\nprintf "%%s" "%s"\nexit %s\n' "${2:-}" "${1}" >"$FX/pnwfbin/pnwf"
  chmod +x "$FX/pnwfbin/pnwf"
  PATH="$FX/pnwfbin:$PATH"
  export PATH
}

# ---- scan -------------------------------------------------------------------

@test "scan: JSON validates against the schema and every candidate appears exactly once" {
  fake_pnwf 0 ""
  # one of each candidate kind: worktree, dangling branch, detached, locked,
  # set member
  add_wt "$REPO" scratch >/dev/null
  git -C "$REPO" branch dangler
  git -C "$REPO" worktree add -q --detach "$FX/wts/det" >/dev/null
  locked="$(add_wt "$REPO" lockme)"
  git -C "$REPO" worktree lock "$locked"
  mkdir -p "$FX/ws/.workforests"
  mkdir -p "$FX/ws/.workforests/setA"
  git -C "$REPO" worktree add -q -b setA "$FX/ws/.workforests/setA/repo" >/dev/null

  run --separate-stderr cli_scan "$REPO"
  [ "$status" -eq 0 ]
  SCAN="$output"
  jq -e '.schema_version == 1' <<<"$SCAN"
  jq -e '.generated_at | test("^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9:]{8}Z$")' <<<"$SCAN"
  jq -e '(.candidates | length) == 5' <<<"$SCAN"
  jq -e '[.candidates[].id] | (length == (unique | length))' <<<"$SCAN"
  jq -e '.candidates | all(
      has("id","repo","repo_path","kind","set","branch","worktree","tip","primary",
          "ahead","behind","ancestor","plus_commits","merge_commits","patch_equivalent",
          "dirty","anchored","bead","bead_lookup","bead_match","last_commit_date",
          "subjects","action","reason","evidence"))' <<<"$SCAN"
  jq -e '.candidates | all(.kind | IN("worktree","dangling-branch","set-member","repo"))' <<<"$SCAN"
  jq -e '.candidates | all(.action | IN("remove","reclaim","keep","agent"))' <<<"$SCAN"
  jq -e '.candidates | all(.bead_lookup | IN("ok","none","failed"))' <<<"$SCAN"
  jq -e '.candidates | all(.reason | IN(
      "repo-unreadable","detached-worktree","anchored-process","canonical-checkout",
      "bead-lookup-failed","claimed","worktree-unreadable","dirty","landed-ancestor",
      "landed-patch-equivalent","bead-match-weak","patch-equivalent-no-bead",
      "patch-equivalent-bead-open","idle-clean-unworked","isolated-not-worked","unlanded-work"))' <<<"$SCAN"
  jq -e '.candidates | all(.anchored | all(has("pid","command","blocking")))' <<<"$SCAN"
  jq -e '.sets | all(has("set","members","action","reason","evidence"))' <<<"$SCAN"
  jq -e '.sets | all(.action | IN("keep","agent","remove-via-pnwf"))' <<<"$SCAN"
  # the kinds and the specific rows
  [ "$(row scratch | jq -r '.kind')" = worktree ]
  [ "$(row dangler | jq -r '.kind')" = dangling-branch ]
  [ "$(row setA | jq -r '"\(.kind) \(.set)"')" = "set-member setA" ]
  [ "$(row lockme | jq -r '.reason')" = worktree-unreadable ]
  jq -e '.candidates | any(.branch == null and .reason == "detached-worktree")' <<<"$SCAN"
  # the dangling branch sits at the main tip, so it is an ancestor with no bead
  [ "$(row dangler | jq -r '"\(.action) \(.dirty)"')" = "remove null" ]
  [ "$(jq -r '.sets[0] | "\(.set) \(.action)"' <<<"$SCAN")" = "setA remove-via-pnwf" ]
}

@test "scan: --table renders the same set of rows as the JSON" {
  fake_pnwf 0 ""
  add_wt "$REPO" scratch >/dev/null
  git -C "$REPO" branch dangler
  run --separate-stderr cli_scan "$REPO"
  json="$output"
  run --separate-stderr "$WTAUDIT" scan --repo "$REPO" --table
  [ "$status" -eq 0 ]
  table="$output"
  [ "$(jq -r '.candidates | length' <<<"$json")" -eq 2 ]
  for id in $(jq -r '.candidates[].id' <<<"$json"); do
    [[ $table == *"$id"* ]]
  done
  [[ $table == *"TYPE"* ]]
}

@test "scan: a real bd lookup of a nonexistent id is none, and the verdict is not a lookup failure" {
  add_wt "$REPO" drain/tst-nosuchbead >/dev/null
  run --separate-stderr cli_scan "$REPO"
  SCAN="$output"
  [ "$(row drain/tst-nosuchbead | jq -r '"\(.bead_lookup) \(.reason)"')" = "none landed-ancestor" ]
}

@test "scan: an unrunnable or garbage-printing bd yields failed and keep bead-lookup-failed, never an abort (mock: the failure under test)" {
  add_wt "$REPO" drain/tst-q1 >/dev/null
  mock_bd 'echo "not json at all"; exit 0'
  run --separate-stderr cli_scan "$REPO"
  [ "$status" -eq 0 ]
  SCAN="$output"
  [ "$(row drain/tst-q1 | jq -r '"\(.bead_lookup) \(.action) \(.reason)"')" = "failed keep bead-lookup-failed" ]
  mock_bd 'exit 127'
  run --separate-stderr cli_scan "$REPO"
  [ "$status" -eq 0 ]
  SCAN="$output"
  [ "$(row drain/tst-q1 | jq -r '.reason')" = bead-lookup-failed ]
}

@test "scan: a real bd claim is reported claimed and carries the bead facts" {
  id="$(pool_bead claimed)"
  add_wt "$REPO" "drain/$id" >/dev/null
  run --separate-stderr cli_scan "$REPO"
  SCAN="$output"
  [ "$(row "drain/$id" | jq -r '"\(.reason) \(.bead.status) \(.bead.assignee) \(.bead_match)"')" = "claimed in_progress wtaudit-test branch-id" ]
  jq -e --arg id "$id" '.candidates[] | select(.branch == "drain/\($id)") | .bead.lease_expires_at != ""' <<<"$SCAN"
}

@test "scan: discovery uses pn workspace discover and an unreadable discovered path becomes a repo-unreadable row (fake pn: a nonexistent discovered path cannot come from the real pn)" {
  mkdir -p "$FX/pnbin"
  cat >"$FX/pnbin/pn" <<PN
#!/usr/bin/env bash
printf 'goodrepo\tgit@example.invalid:good.git\t%s\n' "$REPO"
printf 'ghostrepo\tgit@example.invalid:ghost.git\t%s\n' "$FX/does-not-exist"
PN
  chmod +x "$FX/pnbin/pn"
  PATH="$FX/pnbin:$PATH"
  export PATH
  git -C "$REPO" branch dangler
  run --separate-stderr "$WTAUDIT" scan
  [ "$status" -eq 0 ]
  SCAN="$output"
  jq -e '.candidates | any(.repo == "ghostrepo" and .kind == "repo" and .reason == "repo-unreadable" and .action == "agent")' <<<"$SCAN"
  jq -e '.candidates | any(.repo == "goodrepo" and .branch == "dangler")' <<<"$SCAN"
}

@test "scan: pn workspace discover failing is a usage-class error (exit 2)" {
  mkdir -p "$FX/pnbin"
  printf '#!/usr/bin/env bash\nexit 1\n' >"$FX/pnbin/pn"
  chmod +x "$FX/pnbin/pn"
  PATH="$FX/pnbin:$PATH"
  export PATH
  run --separate-stderr "$WTAUDIT" scan
  [ "$status" -eq 2 ]
}

@test "anchored process: a real anchored bash blocks, a real anchored sleep is reported blocking false" {
  wt="$(add_wt "$REPO" feat)"
  start_anchor "$wt"
  run --separate-stderr cli_scan "$REPO"
  SCAN="$output"
  [ "$(row feat | jq -r '"\(.action) \(.reason)"')" = "keep anchored-process" ]
  jq -e '.candidates[] | select(.branch == "feat") | .anchored | (any(.blocking == true) and any(.blocking == false))' <<<"$SCAN"
}

@test "anchored process: the WTDONE_BLOCKING_COMMANDS override is honored" {
  wt="$(add_wt "$REPO" feat)"
  start_anchor "$wt"
  WTDONE_BLOCKING_COMMANDS="somethingelse" run --separate-stderr cli_scan "$REPO"
  SCAN="$output"
  [ "$(row feat | jq -r '.action')" = remove ]
  jq -e '.candidates[] | select(.branch == "feat") | .anchored | all(.blocking == false)' <<<"$SCAN"
}

@test "scan: works from a minimal PATH without grep, sed or sort (the fallback runtimeDeps are enough)" {
  mkdir -p "$FX/minbin"
  local t
  for t in git jq lsof awk date basename cat tr bd env bash dirname mktemp; do
    ln -sf "$(command -v "$t")" "$FX/minbin/$t"
  done
  [ ! -e "$FX/minbin/grep" ]
  wt="$(add_wt "$REPO" unlanded)"
  commit_in "$wt" f.txt "unlanded work"
  PATH="$FX/minbin" run --separate-stderr "$WTAUDIT" scan --repo "$REPO"
  [ "$status" -eq 0 ]
  [ -z "$stderr" ]
  SCAN="$output"
  [ "$(row unlanded | jq -r '"\(.plus_commits) \(.reason)"')" = "1 unlanded-work" ]
}

# ---- help, completions, tldr ------------------------------------------------

@test "--help lists the verdict enum, the set and remove reasons and the exit codes" {
  run "$WTAUDIT" --help
  [ "$status" -eq 0 ]
  for reason in repo-unreadable detached-worktree anchored-process canonical-checkout \
    bead-lookup-failed claimed worktree-unreadable dirty landed-ancestor \
    landed-patch-equivalent bead-match-weak patch-equivalent-no-bead \
    patch-equivalent-bead-open idle-clean-unworked isolated-not-worked unlanded-work \
    pnwf-disagrees pnwf-unavailable branch-refused tip-moved-late teardown-refused \
    tip-moved verdict-changed set-member usage; do
    [[ $output == *"$reason"* ]] || { echo "help lacks $reason" >&2; return 1; }
  done
  for code in "0 success" "3 a --repo" "10 refused" "11 the branch remains" "12 the candidate is a set member" "13 wtdone failed"; do
    [[ $output == *"$code"* ]] || { echo "help lacks exit code text: $code" >&2; return 1; }
  done
}

@test "completions and the tldr page mention every subcommand and flag" {
  for f in "$SCRIPTS_DIR/completions/wtaudit.bash" "$SCRIPTS_DIR/completions/_wtaudit" "$SCRIPTS_DIR/wtaudit.md"; do
    for word in scan remove; do
      grep -q -- "$word" "$f" || { echo "$f lacks $word" >&2; return 1; }
    done
    for flag in --repo --json --table --branch --expect-tip --keep-branch; do
      grep -q -- "$flag" "$f" || { echo "$f lacks $flag" >&2; return 1; }
    done
  done
  for f in "$SCRIPTS_DIR/completions/wtaudit.bash" "$SCRIPTS_DIR/completions/_wtaudit"; do
    grep -q -- "--help" "$f" || { echo "$f lacks --help" >&2; return 1; }
    grep -q -- "--version" "$f" || { echo "$f lacks --version" >&2; return 1; }
  done
}

@test "the tldr page carries the agent instructions" {
  for phrase in "wtaudit scan" "never re-derive" "action: keep" "remove\` or \`reclaim" "do NOT retry" "remove-via-pnwf" "isolated, not yet worked" "schema_version == 1"; do
    grep -q -- "$phrase" "$SCRIPTS_DIR/wtaudit.md" || { echo "tldr lacks: $phrase" >&2; return 1; }
  done
}

# ---- remove: happy paths ----------------------------------------------------

@test "exit-code matrix: remove 0 - an ancestor worktree and its branch are removed" {
  wt="$(add_wt "$REPO" scratch)"
  do_remove "$REPO" scratch
  [ "$status" -eq 0 ]
  [ "$(jq -r '"\(.removed_worktree) \(.removed_branch) \(.exit) \(.reason)"' <<<"$output")" = "true true 0 landed-ancestor" ]
  [ ! -e "$wt" ]
  run git -C "$REPO" rev-parse --verify -q refs/heads/scratch
  [ "$status" -ne 0 ]
}

@test "remove: a rebased branch (patch-equivalent, closed bead) loses its worktree to wtdone and its branch to the compare-and-swap" {
  id="$(pool_bead closed)"
  wt="$(add_wt "$REPO" "drain/$id")"
  commit_in "$wt" feature.txt "feature work"
  commit_in "$REPO" other.txt "unrelated main work"
  git -C "$REPO" cherry-pick "$(tip_of "$REPO" "drain/$id")" >/dev/null
  do_remove "$REPO" "drain/$id"
  [ "$status" -eq 0 ]
  [ "$(jq -r '"\(.removed_worktree) \(.removed_branch) \(.reason)"' <<<"$output")" = "true true landed-patch-equivalent" ]
  [ ! -e "$wt" ]
  run git -C "$REPO" rev-parse --verify -q "refs/heads/drain/$id"
  [ "$status" -ne 0 ]
}

@test "remove: reclaim of a clean zero-commit worktree for an open unclaimed bead (canonical HEAD on the primary) exits 0" {
  id="$(pool_bead open)"
  wt="$(add_wt "$REPO" "drain/$id")"
  [ "$(git -C "$REPO" rev-parse --abbrev-ref HEAD)" = main ]
  do_remove "$REPO" "drain/$id"
  [ "$status" -eq 0 ]
  [ "$(jq -r '"\(.removed_worktree) \(.removed_branch) \(.reason)"' <<<"$output")" = "true true idle-clean-unworked" ]
  [ ! -e "$wt" ]
  # the bead is untouched
  [ "$(bd_fixture_show_json "$id" | jq -r '.status')" = open ]
}

@test "remove: a dangling ancestor branch is deleted by the compare-and-swap (no worktree)" {
  git -C "$REPO" branch dangler
  do_remove "$REPO" dangler
  [ "$status" -eq 0 ]
  [ "$(jq -r '"\(.removed_worktree) \(.removed_branch)"' <<<"$output")" = "false true" ]
  run git -C "$REPO" rev-parse --verify -q refs/heads/dangler
  [ "$status" -ne 0 ]
}

@test "remove: the branch is never deleted with branch -D (an unmerged branch with a stale tip claim is refused)" {
  wt="$(add_wt "$REPO" unlanded)"
  commit_in "$wt" f.txt "unlanded work"
  do_remove "$REPO" unlanded
  [ "$status" -eq 10 ]
  [ "$(jq -r '.reason' <<<"$output")" = verdict-changed ]
  [ -e "$wt" ]
  run git -C "$REPO" rev-parse --verify -q refs/heads/unlanded
  [ "$status" -eq 0 ]
}

# ---- remove: the compare-and-swap and refusals ------------------------------

@test "remove: the compare-and-swap loses to a tip moved by a misbehaving wtdone, exit 11 tip-moved-late, branch intact (fake wtdone: the misbehavior is the point)" {
  wt="$(add_wt "$REPO" scratch)"
  tip="$(tip_of "$REPO" scratch)"
  cat >"$FX/bin/wtdone-fake" <<FAKE
#!/usr/bin/env bash
git -C "$REPO" worktree remove --force "$wt"
new="\$(git -C "$REPO" commit-tree "refs/heads/scratch^{tree}" -p refs/heads/scratch -m moved)"
git -C "$REPO" update-ref refs/heads/scratch "\$new"
exit 1
FAKE
  chmod +x "$FX/bin/wtdone-fake"
  WTDONE_BIN="$FX/bin/wtdone-fake" do_remove "$REPO" scratch "$tip"
  [ "$status" -eq 11 ]
  [ "$(jq -r '"\(.reason) \(.removed_worktree) \(.removed_branch)"' <<<"$output")" = "tip-moved-late true false" ]
  [ "$(tip_of "$REPO" scratch)" != "$tip" ]
}

@test "exit-code matrix: remove 11 tip-moved-late for a dangling branch (git shim wrapping the real git moves the tip just before the delete)" {
  git -C "$REPO" branch dangler
  tip="$(tip_of "$REPO" dangler)"
  newtip="$(git -C "$REPO" commit-tree "refs/heads/dangler^{tree}" -p refs/heads/dangler -m moved)"
  real_git="$(command -v git)"
  cat >"$FX/bin/git" <<SHIM
#!/usr/bin/env bash
if [[ " \$* " == *" update-ref -d "* ]]; then
  "$real_git" -C "$REPO" update-ref refs/heads/dangler "$newtip"
fi
exec "$real_git" "\$@"
SHIM
  chmod +x "$FX/bin/git"
  PATH="$FX/bin:$PATH" do_remove "$REPO" dangler "$tip"
  [ "$status" -eq 11 ]
  [ "$(jq -r '"\(.reason) \(.removed_worktree) \(.removed_branch)"' <<<"$output")" = "tip-moved-late false false" ]
  [ "$(tip_of "$REPO" dangler)" = "$newtip" ]
}

@test "exit-code matrix: remove 11 branch-refused - a reclaim whose canonical HEAD does not contain the zero-commit branch" {
  id="$(pool_bead open)"
  wt="$(add_wt "$REPO" "drain/$id")"
  tip="$(tip_of "$REPO" "drain/$id")"
  # canonical HEAD moves to a branch at the PARENT of main, so the zero-commit
  # branch (at main's tip) is not merged into HEAD and `branch -d` refuses it
  git -C "$REPO" checkout -q -b old "main~1"
  do_remove "$REPO" "drain/$id" "$tip"
  [ "$status" -eq 11 ]
  [ "$(jq -r '"\(.reason) \(.removed_worktree) \(.removed_branch)"' <<<"$output")" = "branch-refused true false" ]
  [ ! -e "$wt" ]
  run git -C "$REPO" rev-parse --verify -q "refs/heads/drain/$id"
  [ "$status" -eq 0 ]
}

@test "exit-code matrix: remove 13 teardown-refused - a wtdone that exits 1 without removing the worktree (fake wtdone: the refusal is the point)" {
  wt="$(add_wt "$REPO" scratch)"
  printf '#!/usr/bin/env bash\necho "wtdone: refusing: live process" >&2\nexit 1\n' >"$FX/bin/wtdone-refuse"
  chmod +x "$FX/bin/wtdone-refuse"
  WTDONE_BIN="$FX/bin/wtdone-refuse" do_remove "$REPO" scratch
  [ "$status" -eq 13 ]
  [ "$(jq -r '.reason' <<<"$output")" = teardown-refused ]
  [[ "$(jq -r '.message' <<<"$output")" == *"refusing: live process"* ]]
  [ -e "$wt" ]
}

@test "exit-code matrix: remove 12 - a set member is refused with set-member" {
  mkdir -p "$FX/ws/.workforests/setA"
  git -C "$REPO" worktree add -q -b setA "$FX/ws/.workforests/setA/repo" >/dev/null
  do_remove "$REPO" setA
  [ "$status" -eq 12 ]
  [ "$(jq -r '"\(.reason) \(.removed_worktree) \(.removed_branch)"' <<<"$output")" = "set-member false false" ]
  [ -e "$FX/ws/.workforests/setA/repo" ]
}

@test "exit-code matrix: remove 10 tip-moved - the tip moved between scan and remove, still removable" {
  git -C "$REPO" branch dangler
  scanned="$(tip_of "$REPO" dangler)"
  git -C "$REPO" branch -f dangler "main~1"
  do_remove "$REPO" dangler "$scanned"
  [ "$status" -eq 10 ]
  [ "$(jq -r '.reason' <<<"$output")" = tip-moved ]
  run git -C "$REPO" rev-parse --verify -q refs/heads/dangler
  [ "$status" -eq 0 ]
}

@test "exit-code matrix: remove 10 verdict-changed - the worktree became dirty after the scan" {
  wt="$(add_wt "$REPO" scratch)"
  tip="$(tip_of "$REPO" scratch)"
  echo x >"$wt/untracked.txt"
  do_remove "$REPO" scratch "$tip"
  [ "$status" -eq 10 ]
  [ "$(jq -r '.reason' <<<"$output")" = verdict-changed ]
  [[ "$(jq -r '.message' <<<"$output")" == *dirty* ]]
  [ -e "$wt" ]
}

@test "exit-code matrix: remove 10 verdict-changed - a blocking process became anchored after the scan" {
  wt="$(add_wt "$REPO" scratch)"
  tip="$(tip_of "$REPO" scratch)"
  start_anchor "$wt"
  do_remove "$REPO" scratch "$tip"
  [ "$status" -eq 10 ]
  [[ "$(jq -r '.message' <<<"$output")" == *anchored-process* ]]
  [ -e "$wt" ]
}

@test "exit-code matrix: remove 10 verdict-changed - the bead became claimed after the scan" {
  id="$(bd -C "$TDIR" create "late claim" -p 2 --json | jq -r '.id')"
  git -C "$REPO" branch "drain/$id"
  git -C "$REPO" worktree add -q "$FX/wts/late" "drain/$id"
  tip="$(tip_of "$REPO" "drain/$id")"
  bd -C "$TDIR" update "$id" --claim --actor wtaudit-test >/dev/null 2>&1
  do_remove "$REPO" "drain/$id" "$tip"
  [ "$status" -eq 10 ]
  [[ "$(jq -r '.message' <<<"$output")" == *claimed* ]]
}

@test "exit-code matrix: remove 10 verdict-changed - the branch became the canonical checkout after the scan" {
  git -C "$REPO" branch scratch
  tip="$(tip_of "$REPO" scratch)"
  git -C "$REPO" checkout -q scratch
  do_remove "$REPO" scratch "$tip"
  [ "$status" -eq 10 ]
  [[ "$(jq -r '.message' <<<"$output")" == *canonical-checkout* ]]
  [ "$(git -C "$REPO" rev-parse --abbrev-ref HEAD)" = scratch ]
}

@test "exit-code matrix: remove 10 verdict-changed - an unknown branch" {
  do_remove "$REPO" nosuchbranch "$(git -C "$REPO" rev-parse HEAD)"
  [ "$status" -eq 10 ]
  [ "$(jq -r '.reason' <<<"$output")" = verdict-changed ]
}

@test "exit-code matrix: remove 10 verdict-changed - a branch that vanished after the scan" {
  git -C "$REPO" branch dangler
  tip="$(tip_of "$REPO" dangler)"
  git -C "$REPO" branch -D dangler >/dev/null
  do_remove "$REPO" dangler "$tip"
  [ "$status" -eq 10 ]
  [ "$(jq -r '.reason' <<<"$output")" = verdict-changed ]
}

# ---- usage errors and scan exits --------------------------------------------

@test "exit-code matrix: remove 2 - a malformed --expect-tip" {
  git -C "$REPO" branch dangler
  run --separate-stderr "$WTAUDIT" remove --repo "$REPO" --branch dangler --expect-tip not-a-sha
  [ "$status" -eq 2 ]
  [ "$(jq -r '.reason' <<<"$output")" = usage ]
}

@test "exit-code matrix: remove 2 - an unreadable --repo" {
  run --separate-stderr "$WTAUDIT" remove --repo "$FX/not-a-repo" --branch x --expect-tip "$(git -C "$REPO" rev-parse HEAD)"
  [ "$status" -eq 2 ]
  [ "$(jq -r '.reason' <<<"$output")" = usage ]
}

@test "exit-code matrix: scan 0 - success, even with an unreadable discovered repo (a row, not an error)" {
  run --separate-stderr cli_scan "$REPO"
  [ "$status" -eq 0 ]
  jq -e '.schema_version == 1' <<<"$output"
}

@test "exit-code matrix: scan 2 - a usage error" {
  run --separate-stderr "$WTAUDIT" scan --bogus
  [ "$status" -eq 2 ]
  run --separate-stderr "$WTAUDIT" frobnicate
  [ "$status" -eq 2 ]
}

@test "exit-code matrix: scan 3 - a --repo argument that is not a readable git repository" {
  mkdir -p "$FX/plain"
  run --separate-stderr "$WTAUDIT" scan --repo "$FX/plain"
  [ "$status" -eq 3 ]
}

# ---- remove --keep-branch: reclaim an abandoned agent row's worktree ---------

# do_keep <repo> <branch> [<tip>]: run remove --keep-branch.
do_keep() {
  run --separate-stderr "$WTAUDIT" remove --repo "$1" --branch "$2" --expect-tip "${3:-$(tip_of "$1" "$2")}" --keep-branch
}

# unlanded_wt <branch>: a clean worktree whose branch has a commit not on main.
unlanded_wt() {
  local wt
  wt="$(add_wt "$REPO" "$1")"
  commit_in "$wt" f.txt "unlanded work"
  printf '%s\n' "$wt"
}

@test "keep-branch: exit 0 removes the worktree of an unlanded agent row and keeps the branch at its tip" {
  wt="$(unlanded_wt abandoned)"
  tip="$(tip_of "$REPO" abandoned)"
  do_keep "$REPO" abandoned "$tip"
  [ "$status" -eq 0 ]
  [ "$(jq -r '"\(.removed_worktree) \(.removed_branch) \(.exit) \(.reason)"' <<<"$output")" = "true false 0 unlanded-work" ]
  [ ! -e "$wt" ]
  [ "$(tip_of "$REPO" abandoned)" = "$tip" ]
  [ -z "$(git -C "$REPO" worktree list --porcelain | grep -F "$wt" || true)" ]
}

@test "keep-branch: plain remove still refuses the same row (the new mode is opt-in)" {
  wt="$(unlanded_wt abandoned)"
  do_remove "$REPO" abandoned
  [ "$status" -eq 10 ]
  [ -e "$wt" ]
}

@test "keep-branch: exit 10 tip-moved when the tip differs from --expect-tip" {
  wt="$(unlanded_wt abandoned)"
  stale="$(git -C "$REPO" rev-parse main)"
  do_keep "$REPO" abandoned "$stale"
  [ "$status" -eq 10 ]
  [ "$(jq -r '.reason' <<<"$output")" = tip-moved ]
  [ -e "$wt" ]
}

@test "keep-branch: exit 10 verdict-changed when the worktree is dirty" {
  wt="$(unlanded_wt abandoned)"
  tip="$(tip_of "$REPO" abandoned)"
  echo x >"$wt/untracked.txt"
  do_keep "$REPO" abandoned "$tip"
  [ "$status" -eq 10 ]
  [ "$(jq -r '.reason' <<<"$output")" = verdict-changed ]
  [[ "$(jq -r '.message' <<<"$output")" == *dirty* ]]
  [ -e "$wt" ]
}

@test "keep-branch: exit 10 verdict-changed when a blocking process is anchored in the worktree" {
  wt="$(unlanded_wt abandoned)"
  tip="$(tip_of "$REPO" abandoned)"
  start_anchor "$wt"
  do_keep "$REPO" abandoned "$tip"
  [ "$status" -eq 10 ]
  [[ "$(jq -r '.message' <<<"$output")" == *anchored-process* ]]
  [ -e "$wt" ]
}

@test "keep-branch: exit 10 verdict-changed when the bead is claimed" {
  id="$(bd -C "$TDIR" create "abandoned but claimed" -p 2 --json | jq -r '.id')"
  wt="$(add_wt "$REPO" "drain/$id")"
  commit_in "$wt" f.txt "unlanded work"
  tip="$(tip_of "$REPO" "drain/$id")"
  bd -C "$TDIR" update "$id" --claim --actor wtaudit-test >/dev/null 2>&1
  do_keep "$REPO" "drain/$id" "$tip"
  [ "$status" -eq 10 ]
  [[ "$(jq -r '.message' <<<"$output")" == *claimed* ]]
  [ -e "$wt" ]
}

@test "keep-branch: exit 10 verdict-changed for a row that is not an agent row (a landed ancestor must use plain remove)" {
  wt="$(add_wt "$REPO" scratch)"
  do_keep "$REPO" scratch
  [ "$status" -eq 10 ]
  [ "$(jq -r '.reason' <<<"$output")" = verdict-changed ]
  [ -e "$wt" ]
}

@test "keep-branch: exit 10 verdict-changed for a dangling branch with no worktree to reclaim" {
  git -C "$REPO" branch dangler main~1
  commit_in "$REPO" other.txt "advance main"
  git -C "$REPO" branch -f dangler HEAD~1
  do_keep "$REPO" dangler
  [ "$status" -eq 10 ]
  [ "$(jq -r '.reason' <<<"$output")" = verdict-changed ]
}

@test "keep-branch: exit 12 set-member for a coordinated set member" {
  mkdir -p "$FX/ws/.workforests/setA"
  git -C "$REPO" worktree add -q -b setA "$FX/ws/.workforests/setA/repo" >/dev/null
  commit_in "$FX/ws/.workforests/setA/repo" f.txt "unlanded work"
  do_keep "$REPO" setA
  [ "$status" -eq 12 ]
  [ "$(jq -r '.reason' <<<"$output")" = set-member ]
  [ -e "$FX/ws/.workforests/setA/repo" ]
}

@test "keep-branch: exit 13 teardown-refused when wtdone leaves the worktree (fake wtdone: the refusal is the point)" {
  wt="$(unlanded_wt abandoned)"
  printf '#!/usr/bin/env bash\necho "wtdone: refusing" >&2\nexit 1\n' >"$FX/bin/wtdone-refuse"
  chmod +x "$FX/bin/wtdone-refuse"
  WTDONE_BIN="$FX/bin/wtdone-refuse" do_keep "$REPO" abandoned
  [ "$status" -eq 13 ]
  [ "$(jq -r '.reason' <<<"$output")" = teardown-refused ]
  [ -e "$wt" ]
}

@test "keep-branch: exit 11 when the branch is missing after the teardown (fake wtdone deletes it)" {
  wt="$(unlanded_wt abandoned)"
  tip="$(tip_of "$REPO" abandoned)"
  cat >"$FX/bin/wtdone-eager" <<EOF
#!/usr/bin/env bash
git -C "$REPO" worktree remove --force "$wt"
git -C "$REPO" branch -D abandoned >/dev/null
EOF
  chmod +x "$FX/bin/wtdone-eager"
  WTDONE_BIN="$FX/bin/wtdone-eager" do_keep "$REPO" abandoned "$tip"
  [ "$status" -eq 11 ]
  [ "$(jq -r '"\(.reason) \(.removed_worktree) \(.removed_branch)"' <<<"$output")" = "tip-moved-late true false" ]
}

@test "keep-branch: --keep-branch is rejected for scan (exit 2)" {
  run --separate-stderr "$WTAUDIT" scan --repo "$REPO" --keep-branch
  [ "$status" -eq 2 ]
}

# ---- checkout-pool slots ----------------------------------------------------

@test "scan: a locked detached pool slot and an unlocked slot under a pg-checkout-pool root are never candidates" {
  fake_pnwf 0 ""
  # slot locked the way pg-checkout-pool locks it (detached, reason "pool slot")
  git -C "$REPO" worktree add -q --detach "$FX/pool-a" >/dev/null
  git -C "$REPO" worktree lock --reason "pool slot (pg-checkout-pool)" "$FX/pool-a"
  # a leased slot: on a lease branch, locked
  git -C "$REPO" worktree add -q -b lease/s1 "$FX/pool-b" >/dev/null
  git -C "$REPO" worktree lock --reason "pool slot (pg-checkout-pool)" "$FX/pool-b"
  # a slot whose lock was lost, identified by its path under the pool layout
  git -C "$REPO" worktree add -q --detach "$FX/state/pg-checkout-pool/slots/slot-03/tree" >/dev/null
  # an ordinary locked worktree is still reported (kept), not skipped
  git -C "$REPO" worktree add -q -b plain "$FX/plain" >/dev/null
  git -C "$REPO" worktree lock --reason "operator hold" "$FX/plain"

  run --separate-stderr cli_scan "$REPO"
  [ "$status" -eq 0 ]
  jq -e '(.candidates | length) == 1' <<<"$output"
  jq -e '.candidates[0] | .branch == "plain" and .reason == "worktree-unreadable"' <<<"$output"
  # the lease branch is checked out in a slot, so it is not a dangling branch either
  ! jq -e '.candidates | any(.branch == "lease/s1")' <<<"$output"
}

@test "remove: a pool slot's lease branch is not a removal candidate" {
  git -C "$REPO" worktree add -q -b lease/s2 "$FX/pool-c" >/dev/null
  git -C "$REPO" worktree lock --reason "pool slot (pg-checkout-pool)" "$FX/pool-c"
  do_remove "$REPO" lease/s2
  [ "$status" -eq 10 ]
  [ -d "$FX/pool-c" ]
}
