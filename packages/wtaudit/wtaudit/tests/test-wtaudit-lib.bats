#!/usr/bin/env bats
# bats file_tags=type:unit
#
# Library tests for wtaudit.bash against REAL tools: real git (the shared
# hermetic fixture harness), real lsof (a real anchored bash blocks, a real
# anchored sleep does not) and a real bd in a throwaway embedded database
# (bd_fixture.bash), because claim-guard correctness depends on bd's actual
# field semantics (status, assignee, lease_expires_at, closed-but-assigned).
#
# A MOCK bd on PATH is used only where design allows it, each test saying why:
#   - the two envelope-shape tests (bare array vs {schema_version,data});
#   - the unrunnable / garbage-printing bd tests (lookup "failed");
#   - the lease-only claim test: real `bd update --claim` always also sets
#     status in_progress, so a bead with a lease but status open and no
#     assignee cannot be produced with the real tool.
# A fake `pnwf` on PATH is used for the set parity tests: building a real
# pnwf fixture workspace (pn-workspace.lock.json, `pn workspace info`) is not
# cheap, so the real pnwf is covered by the sibling package's own suite.
#
# Parity fixture (the landed test), shared by name with the pnwf suite:
#   ancestor            branch merged by fast-forward         landed
#   rebased             branch commit re-applied on main      landed (patch-equivalent)
#   cherry-picked dup   branch commit cherry-picked to main   landed (patch-equivalent)
#   squash-merged       two commits squashed on main          NOT landed (git cherry shows +)
#   merge-commit        landed commit plus a merge of main    NOT patch-equivalent
#   plus-and-minus      one landed and one unlanded commit    NOT landed

load wtaudit_helper

setup_file() { wtaudit_file_setup; }
teardown_file() { wtaudit_file_teardown; }

setup() {
  wtaudit_test_setup
  source_lib
  REPO="$(mk_repo repo)"
}

teardown() { wtaudit_test_teardown; }

# ---- scenario builders ------------------------------------------------------

# mk_patch_equivalent <branch>: the branch has one commit and main carries a
# cherry-picked copy of it (not an ancestor, patch-equivalent). Sets WT.
mk_patch_equivalent() {
  WT="$(add_wt "$REPO" "$1")"
  commit_in "$WT" feature.txt "feature work"
  commit_in "$REPO" other.txt "unrelated main work"
  git -C "$REPO" cherry-pick "$(git -C "$REPO" rev-parse "$1")" >/dev/null
}

# mk_unlanded <branch>: one commit only on the branch. Sets WT.
mk_unlanded() {
  WT="$(add_wt "$REPO" "$1")"
  commit_in "$WT" feature.txt "unlanded work"
}

# ---- the landed test --------------------------------------------------------

@test "landed test: ancestor (fast-forward merged) is ancestor and patch-equivalent" {
  WT="$(add_wt "$REPO" feat)"
  commit_in "$WT" f.txt work
  git -C "$REPO" merge -q --ff-only feat
  row="$(classify "$REPO" feat)"
  [ "$(jq -r '.ancestor' <<<"$row")" = true ]
  [ "$(jq -r '.patch_equivalent' <<<"$row")" = true ]
  [ "$(jq -r '.plus_commits' <<<"$row")" = 0 ]
}

@test "landed test: rebased branch (copy of its commit on main) is patch-equivalent, not ancestor" {
  mk_patch_equivalent feat
  row="$(classify "$REPO" feat)"
  [ "$(jq -r '.ancestor' <<<"$row")" = false ]
  [ "$(jq -r '.patch_equivalent' <<<"$row")" = true ]
  [ "$(jq -r '.ahead' <<<"$row")" = 1 ]
}

@test "landed test: cherry-picked duplicate already on main is patch-equivalent" {
  WT="$(add_wt "$REPO" feat)"
  commit_in "$WT" f.txt first
  commit_in "$WT" g.txt second
  commit_in "$REPO" other.txt "main moved first"
  git -C "$REPO" cherry-pick "feat~1" "feat" >/dev/null
  row="$(classify "$REPO" feat)"
  [ "$(jq -r '.patch_equivalent' <<<"$row")" = true ]
  [ "$(jq -r '.ancestor' <<<"$row")" = false ]
}

@test "landed test: squash-merged is NOT landed (git cherry shows +)" {
  WT="$(add_wt "$REPO" feat)"
  commit_in "$WT" f.txt first
  commit_in "$WT" g.txt second
  git -C "$REPO" merge -q --squash feat
  git -C "$REPO" commit -q -m squashed
  row="$(classify "$REPO" feat)"
  [ "$(jq -r '.patch_equivalent' <<<"$row")" = false ]
  [ "$(jq -r '.plus_commits' <<<"$row")" -ge 1 ]
  [ "$(verdict "$REPO" feat)" = "agent unlanded-work" ]
}

@test "landed test: a branch with a merge commit is NOT patch-equivalent even when cherry shows no +" {
  WT="$(add_wt "$REPO" feat)"
  commit_in "$REPO" other.txt "main moved"
  git -C "$WT" merge -q --no-ff --no-edit main
  row="$(classify "$REPO" feat)"
  [ "$(jq -r '.plus_commits' <<<"$row")" = 0 ]
  [ "$(jq -r '.merge_commits' <<<"$row")" -ge 1 ]
  [ "$(jq -r '.patch_equivalent' <<<"$row")" = false ]
}

@test "landed test: zero commits of its own is an ancestor with ahead 0" {
  add_wt "$REPO" feat >/dev/null
  row="$(classify "$REPO" feat)"
  [ "$(jq -r '.ahead' <<<"$row")" = 0 ]
  [ "$(jq -r '.ancestor' <<<"$row")" = true ]
}

@test "landed test: one landed and one unlanded commit has one + and is not landed" {
  WT="$(add_wt "$REPO" feat)"
  commit_in "$WT" f.txt landed
  commit_in "$WT" g.txt unlanded
  git -C "$REPO" cherry-pick "feat~1" >/dev/null
  row="$(classify "$REPO" feat)"
  [ "$(jq -r '.plus_commits' <<<"$row")" = 1 ]
  [ "$(jq -r '.patch_equivalent' <<<"$row")" = false ]
}

@test "landed test: a branch that merged main into itself with its own unlanded commit is unlanded-work" {
  mk_unlanded feat
  commit_in "$REPO" other.txt "main moved"
  git -C "$WT" merge -q --no-edit main
  [ "$(verdict "$REPO" feat)" = "agent unlanded-work" ]
}

# ---- primary resolution -----------------------------------------------------

@test "primary: git config pgii-integrate-branch.primaryBranch wins" {
  git -C "$REPO" config pgii-integrate-branch.primaryBranch trunk
  git -C "$REPO" symbolic-ref refs/remotes/origin/HEAD refs/remotes/origin/other
  [ "$(wtaudit_primary "$REPO")" = trunk ]
}

@test "primary: origin/HEAD when no config" {
  git -C "$REPO" symbolic-ref refs/remotes/origin/HEAD refs/remotes/origin/develop
  [ "$(wtaudit_primary "$REPO")" = develop ]
}

@test "primary: defaults to main" {
  [ "$(wtaudit_primary "$REPO")" = main ]
}

# ---- bead lookup ------------------------------------------------------------

@test "bead tokens: drain/<id> gives branch-id only" {
  [ "$(wtaudit_bead_tokens drain/pg2-abc.1)" = $'branch-id\tpg2-abc.1' ]
}

@test "bead tokens: a name that is not id-shaped gives only the longest suffix" {
  run wtaudit_bead_tokens feature/pg2-abc12
  [ "${#lines[@]}" -eq 1 ]
  [ "${lines[0]}" = $'suffix\tpg2-abc12' ]
}

@test "bead tokens: a name with no id-shaped part gives no token (no bd call)" {
  run wtaudit_bead_tokens scratch
  [ -z "$output" ]
}

@test "bead tokens: a name that is itself an id has no suffix line" {
  run wtaudit_bead_tokens pg2-abc12
  [ "${#lines[@]}" -eq 1 ]
  [ "${lines[0]}" = $'whole-name\tpg2-abc12' ]
}

@test "bead lookup: real bd finds an existing bead (ok) and reports branch-id" {
  id="$(pool_bead open)"
  res="$(wtaudit_bead_lookup "$REPO" "drain/$id")"
  [ "$(jq -r '.lookup' <<<"$res")" = ok ]
  [ "$(jq -r '.match' <<<"$res")" = branch-id ]
  [ "$(jq -r '.bead.id' <<<"$res")" = "$id" ]
}

@test "bead lookup: real bd answers a nonexistent id with exit 1 and a JSON object, which is none" {
  res="$(wtaudit_bead_lookup "$REPO" drain/tst-nosuch)"
  [ "$(jq -r '.lookup' <<<"$res")" = none ]
  # the real reply really is an exit-1-with-JSON-object (guards the premise)
  run bd -C "$REPO" show tst-nosuch --json
  [ "$status" -ne 0 ]
}

@test "bead lookup: a whole-name hit reports whole-name" {
  id="$(pool_bead open)"
  res="$(wtaudit_bead_lookup "$REPO" "$id")"
  [ "$(jq -r '.match' <<<"$res")" = whole-name ]
}

@test "bead lookup: a suffix-only hit reports suffix" {
  id="$(pool_bead open)"
  res="$(wtaudit_bead_lookup "$REPO" "feature/$id")"
  [ "$(jq -r '.lookup' <<<"$res")" = ok ]
  [ "$(jq -r '.match' <<<"$res")" = suffix ]
}

@test "bead lookup: mock bd, bare-array envelope parses (mock: the shape under test)" {
  mock_bd_bead open "" "" array
  res="$(wtaudit_bead_show "$REPO" tst-x1)"
  [ "$(jq -r '.lookup' <<<"$res")" = ok ]
  [ "$(jq -r '.bead.status' <<<"$res")" = open ]
}

@test "bead lookup: mock bd, {schema_version,data} envelope parses (mock: the shape under test)" {
  mock_bd_bead open "" "" data
  res="$(wtaudit_bead_show "$REPO" tst-x1)"
  [ "$(jq -r '.lookup' <<<"$res")" = ok ]
  [ "$(jq -r '.bead.id' <<<"$res")" = tst-x1 ]
}

@test "bead lookup: mock bd printing {data:{error}} is none (mock: the error-envelope shape)" {
  mock_bd 'printf "%s\n" "{\"data\":{\"error\":\"no issues found\"},\"schema_version\":1}"; exit 1'
  [ "$(wtaudit_bead_show "$REPO" tst-x1 | jq -r '.lookup')" = none ]
}

@test "bead lookup: mock bd printing garbage is failed (mock: unparsable output)" {
  mock_bd 'echo "this is not json"; exit 0'
  [ "$(wtaudit_bead_show "$REPO" tst-x1 | jq -r '.lookup')" = failed ]
}

@test "bead lookup: mock bd that prints nothing and exits 127 is failed (mock: unrunnable bd)" {
  mock_bd 'exit 127'
  [ "$(wtaudit_bead_show "$REPO" tst-x1 | jq -r '.lookup')" = failed ]
}

@test "bead lookup: no bd on PATH at all is failed" {
  PATH="$FX/emptybin" run wtaudit_bead_show "$REPO" tst-x1
  [ "$(jq -r '.lookup' <<<"$output")" = failed ]
}

@test "bead lookup: a bd answer for a DIFFERENT id is not an exact hit (none)" {
  mock_bd 'printf "%s\n" "[{\"id\":\"tst-other\",\"status\":\"open\"}]"'
  [ "$(wtaudit_bead_show "$REPO" tst-x1 | jq -r '.lookup')" = none ]
}

# ---- the drain-claim guard (through classification) -------------------------

# claim_verdict <pool-bead>: a zero-commit worktree for that pooled bead, then its verdict.
claim_verdict() {
  id="$(pool_bead "$1")"
  add_wt "$REPO" "drain/$id" >/dev/null
  verdict "$REPO" "drain/$id"
}

@test "claim guard: in_progress (real claim) is claimed" {
  [ "$(claim_verdict claimed)" = "keep claimed" ]
}

@test "claim guard: assignee set with status open (stranded) is claimed" {
  [ "$(claim_verdict stranded)" = "keep claimed" ]
}

@test "claim guard: a lease alone is claimed (mock: real bd cannot make a lease with status open and no assignee)" {
  mock_bd_bead open "" "2099-01-01T00:00:00Z" array
  add_wt "$REPO" drain/tst-lease >/dev/null
  [ "$(verdict "$REPO" drain/tst-lease)" = "keep claimed" ]
}

@test "claim guard: a closed bead that still carries an assignee is NOT a claim" {
  id="$(pool_bead closed_assigned)"
  [ "$(bd_fixture_show_json "$id" | jq -r '.status')" = closed ]
  add_wt "$REPO" "drain/$id" >/dev/null
  [ "$(verdict "$REPO" "drain/$id")" = "remove landed-ancestor" ]
}

@test "claim guard: no bead is not claimed" {
  add_wt "$REPO" scratch >/dev/null
  [ "$(verdict "$REPO" scratch)" = "remove landed-ancestor" ]
}

@test "claim guard: a lookup failure is keep, never a removal (mock: unrunnable bd)" {
  mock_bd 'exit 127'
  add_wt "$REPO" drain/tst-zz >/dev/null
  [ "$(verdict "$REPO" drain/tst-zz)" = "keep bead-lookup-failed" ]
}

# ---- the decision table: one test per row -----------------------------------

@test "row 0: a missing repo path is agent repo-unreadable" {
  row="$(wtaudit_scan_repo ghost "$FX/ghost")"
  [ "$(jq -r '"\(.action) \(.reason) \(.kind)"' <<<"$row")" = "agent repo-unreadable repo" ]
}

@test "row 0: a repo whose primary branch does not resolve is repo-unreadable" {
  git -C "$REPO" branch -m main trunk
  row="$(wtaudit_scan_repo repo "$REPO")"
  [ "$(jq -r '.reason' <<<"$row")" = repo-unreadable ]
}

@test "row 1a: a non-primary branch checked out in the canonical clone is keep canonical-checkout, and scan emits it" {
  git -C "$REPO" checkout -q -b feat
  row="$(classify "$REPO" feat)"
  [ "$(jq -r '"\(.action) \(.reason) \(.kind) \(.worktree)"' <<<"$row")" = "keep canonical-checkout worktree $REPO" ]
}

@test "row 1a: no lsof probe runs for the canonical checkout (anchored stays empty)" {
  git -C "$REPO" checkout -q -b feat
  start_anchor "$REPO"
  row="$(classify "$REPO" feat)"
  [ "$(jq -r '.reason' <<<"$row")" = canonical-checkout ]
  [ "$(jq -c '.anchored' <<<"$row")" = "[]" ]
}

@test "row 1: a real anchored bash blocks (anchored-process)" {
  WT="$(add_wt "$REPO" feat)"
  start_anchor "$WT"
  row="$(classify "$REPO" feat)"
  [ "$(jq -r '"\(.action) \(.reason)"' <<<"$row")" = "keep anchored-process" ]
  [ "$(jq -r '[.anchored[] | select(.blocking)] | length' <<<"$row")" -ge 1 ]
}

@test "row 1: a real anchored sleep alone does not block and is reported blocking false" {
  WT="$(add_wt "$REPO" feat)"
  (cd "$WT" && exec sleep 60) 3>&- &
  # shellcheck disable=SC2034  # read by wtaudit_test_teardown in the helper
  ANCHOR_PID=$!
  for _ in $(seq 1 25); do
    [ -n "$(lsof -a -d cwd +D "$WT" -F p 2>/dev/null)" ] && break
    sleep 0.2
  done
  row="$(classify "$REPO" feat)"
  [ "$(jq -r '.anchored | length' <<<"$row")" -ge 1 ]
  [ "$(jq -r '[.anchored[] | select(.blocking)] | length' <<<"$row")" = 0 ]
  [ "$(jq -r '.action' <<<"$row")" = remove ]
}

@test "row 1: WTDONE_BLOCKING_COMMANDS replaces the allow-list" {
  WT="$(add_wt "$REPO" feat)"
  start_anchor "$WT"
  WTDONE_BLOCKING_COMMANDS="nothing-matches" run classify "$REPO" feat
  [ "$(jq -r '.action' <<<"$output")" = remove ]
}

@test "row 2: a bead id token with a failed lookup is keep bead-lookup-failed" {
  mock_bd 'echo garbage'
  add_wt "$REPO" drain/tst-q >/dev/null
  [ "$(verdict "$REPO" drain/tst-q)" = "keep bead-lookup-failed" ]
}

@test "row 2: a failed lookup on a patch-equivalent branch is still keep bead-lookup-failed" {
  mk_patch_equivalent drain/tst-pe
  mock_bd 'echo garbage'
  [ "$(verdict "$REPO" drain/tst-pe)" = "keep bead-lookup-failed" ]
}

@test "row 3: a claimed bead is keep claimed" {
  id="$(pool_bead claimed)"
  add_wt "$REPO" "drain/$id" >/dev/null
  [ "$(verdict "$REPO" "drain/$id")" = "keep claimed" ]
}

@test "row 4: a registered worktree whose path is missing is keep worktree-unreadable" {
  WT="$(add_wt "$REPO" feat)"
  rm -rf "$WT"
  [ "$(verdict "$REPO" feat)" = "keep worktree-unreadable" ]
}

@test "row 4: a locked worktree is keep worktree-unreadable" {
  WT="$(add_wt "$REPO" feat)"
  git -C "$REPO" worktree lock "$WT"
  [ "$(verdict "$REPO" feat)" = "keep worktree-unreadable" ]
}

@test "row 4: a worktree whose git status cannot be read is keep worktree-unreadable" {
  WT="$(add_wt "$REPO" feat)"
  printf 'gitdir: %s/nowhere\n' "$FX" >"$WT/.git"
  [ "$(verdict "$REPO" feat)" = "keep worktree-unreadable" ]
}

@test "row 4b: an untracked file makes the worktree dirty" {
  WT="$(add_wt "$REPO" feat)"
  echo x >"$WT/untracked.txt"
  [ "$(verdict "$REPO" feat)" = "keep dirty" ]
}

@test "row 4b: a tracked change makes the worktree dirty" {
  WT="$(add_wt "$REPO" feat)"
  commit_in "$WT" tracked.txt one
  echo changed >>"$WT/tracked.txt"
  [ "$(verdict "$REPO" feat)" = "keep dirty" ]
}

@test "row 4c: a clean idle detached worktree is agent detached-worktree with dirty and anchored filled" {
  git -C "$REPO" worktree add -q --detach "$FX/wts/det" >/dev/null
  rows="$(wtaudit_scan_repo repo "$REPO")"
  row="$(jq -c 'select(.worktree == "'"$FX/wts/det"'")' <<<"$rows")"
  [ "$(jq -r '"\(.action) \(.reason) \(.branch) \(.dirty)"' <<<"$row")" = "agent detached-worktree null false" ]
  [ "$(jq -c '.anchored' <<<"$row")" = "[]" ]
}

@test "row 4c precedence: a detached worktree with an anchored bash reports anchored-process, not detached-worktree" {
  git -C "$REPO" worktree add -q --detach "$FX/wts/det" >/dev/null
  start_anchor "$FX/wts/det"
  rows="$(wtaudit_scan_repo repo "$REPO")"
  [ "$(jq -r 'select(.worktree == "'"$FX/wts/det"'") | .reason' <<<"$rows")" = anchored-process ]
}

@test "row 5: an ancestor with no bead is remove landed-ancestor" {
  add_wt "$REPO" scratch >/dev/null
  [ "$(verdict "$REPO" scratch)" = "remove landed-ancestor" ]
}

@test "row 5: an ancestor whose branch-id bead is closed is remove landed-ancestor" {
  id="$(pool_bead closed)"
  add_wt "$REPO" "drain/$id" >/dev/null
  [ "$(verdict "$REPO" "drain/$id")" = "remove landed-ancestor" ]
}

@test "row 5b: an ancestor whose closed bead matched only by suffix is agent bead-match-weak" {
  id="$(pool_bead closed)"
  add_wt "$REPO" "feature/$id" >/dev/null
  [ "$(verdict "$REPO" "feature/$id")" = "agent bead-match-weak" ]
}

@test "row 6: patch-equivalent with a closed branch-id bead is remove landed-patch-equivalent" {
  id="$(pool_bead closed)"
  mk_patch_equivalent "drain/$id"
  [ "$(verdict "$REPO" "drain/$id")" = "remove landed-patch-equivalent" ]
}

@test "row 6b: patch-equivalent with a closed bead matched only by suffix is agent bead-match-weak" {
  id="$(pool_bead closed)"
  mk_patch_equivalent "feature/$id"
  [ "$(verdict "$REPO" "feature/$id")" = "agent bead-match-weak" ]
}

@test "row 7: patch-equivalent with no bead is keep patch-equivalent-no-bead" {
  mk_patch_equivalent scratch
  [ "$(verdict "$REPO" scratch)" = "keep patch-equivalent-no-bead" ]
}

@test "row 8: patch-equivalent with an open bead is agent patch-equivalent-bead-open" {
  id="$(pool_bead open)"
  mk_patch_equivalent "drain/$id"
  [ "$(verdict "$REPO" "drain/$id")" = "agent patch-equivalent-bead-open" ]
}

@test "row 9: an ancestor with an open unclaimed branch-id bead and a worktree is reclaim idle-clean-unworked" {
  id="$(pool_bead open)"
  add_wt "$REPO" "drain/$id" >/dev/null
  [ "$(verdict "$REPO" "drain/$id")" = "reclaim idle-clean-unworked" ]
}

@test "row 9b: an ancestor with an open bead matched only by suffix is agent bead-match-weak" {
  id="$(pool_bead open)"
  add_wt "$REPO" "feature/$id" >/dev/null
  [ "$(verdict "$REPO" "feature/$id")" = "agent bead-match-weak" ]
}

@test "row 10: a dangling ancestor branch with an open unclaimed bead is keep isolated-not-worked" {
  id="$(pool_bead open)"
  git -C "$REPO" branch "drain/$id"
  row="$(classify "$REPO" "drain/$id")"
  [ "$(jq -r '"\(.kind) \(.action) \(.reason) \(.dirty)"' <<<"$row")" = "dangling-branch keep isolated-not-worked null" ]
}

@test "row 11: unlanded commits are agent unlanded-work" {
  mk_unlanded scratch
  [ "$(verdict "$REPO" scratch)" = "agent unlanded-work" ]
}

@test "row 11: a dangling unlanded branch carries facts for the agent (ahead, subjects, last_commit_date)" {
  git -C "$REPO" checkout -q -b dang
  commit_in "$REPO" d.txt "dangling commit"
  git -C "$REPO" checkout -q main
  row="$(classify "$REPO" dang)"
  [ "$(jq -r '.ahead' <<<"$row")" = 1 ]
  [ "$(jq -r '.subjects[0]' <<<"$row")" = "dangling commit" ]
  [ "$(jq -r '.last_commit_date' <<<"$row")" != null ]
}

@test "precedence: anchored, dirty and claimed together report anchored-process" {
  id="$(pool_bead claimed)"
  WT="$(add_wt "$REPO" "drain/$id")"
  echo x >"$WT/untracked.txt"
  start_anchor "$WT"
  [ "$(verdict "$REPO" "drain/$id")" = "keep anchored-process" ]
}

@test "precedence: claimed and dirty together report claimed (guards in table order)" {
  id="$(pool_bead claimed)"
  WT="$(add_wt "$REPO" "drain/$id")"
  echo x >"$WT/untracked.txt"
  [ "$(verdict "$REPO" "drain/$id")" = "keep claimed" ]
}

# ---- set classification -----------------------------------------------------

# mk_set <set> <repo-name>...: create the repos under $FX/ws, each with a
# worktree on branch <set> at $FX/ws/.workforests/<set>/<repo>. Sets SET_ROOT.
mk_set() {
  local set="$1" name r
  shift
  SET_ROOT="$FX/ws"
  mkdir -p "$SET_ROOT/.workforests/$set"
  for name in "$@"; do
    gfh_init_repo "$SET_ROOT/$name" wtaudit
    git -C "$SET_ROOT/$name" commit -q --allow-empty -m root
    r="$SET_ROOT/$name"
    git -C "$r" worktree add -q -b "$set" "$SET_ROOT/.workforests/$set/$name" >/dev/null
  done
}

# fake_pnwf <exit> [stdout]: a pnwf on PATH that records its cwd and args.
fake_pnwf() {
  mkdir -p "$FX/pnwfbin"
  cat >"$FX/pnwfbin/pnwf" <<FAKE
#!/usr/bin/env bash
pwd -P >"$FX/pnwf.cwd"
printf '%s\n' "\$*" >"$FX/pnwf.args"
printf '%s' "${2:-}"
exit ${1}
FAKE
  chmod +x "$FX/pnwfbin/pnwf"
  PATH="$FX/pnwfbin:$PATH"
  export PATH
}

scan_sets() {
  {
    wtaudit_scan_repo r1 "$SET_ROOT/r1"
    wtaudit_scan_repo r2 "$SET_ROOT/r2"
  } | wtaudit_scan_document
}

@test "sets: every member removable and pnwf land-plan prints nothing is remove-via-pnwf (pnwf run from the workspace root)" {
  mk_set setA r1 r2
  fake_pnwf 0 ""
  doc="$(scan_sets)"
  [ "$(jq -r '.sets[0] | "\(.set) \(.action)"' <<<"$doc")" = "setA remove-via-pnwf" ]
  [ "$(jq -r '.candidates[0].kind' <<<"$doc")" = set-member ]
  [ "$(cat "$FX/pnwf.args")" = "land-plan setA" ]
  [ "$(cat "$FX/pnwf.cwd")" = "$(cd "$SET_ROOT" && pwd -P)" ]
}

@test "sets: a keep member keeps the whole set and names the member" {
  mk_set setA r1 r2
  echo x >"$SET_ROOT/.workforests/setA/r2/untracked.txt"
  fake_pnwf 0 ""
  doc="$(scan_sets)"
  [ "$(jq -r '.sets[0] | "\(.action) \(.reason)"' <<<"$doc")" = "keep dirty" ]
  [[ "$(jq -r '.sets[0].evidence' <<<"$doc")" == *"r2:setA"* ]]
  [ ! -e "$FX/pnwf.args" ]
}

@test "sets: pnwf printing output is agent pnwf-disagrees with the output in evidence" {
  mk_set setA r1 r2
  fake_pnwf 0 $'r2\n'
  doc="$(scan_sets)"
  [ "$(jq -r '.sets[0] | "\(.action) \(.reason)"' <<<"$doc")" = "agent pnwf-disagrees" ]
  [[ "$(jq -r '.sets[0].evidence' <<<"$doc")" == *r2* ]]
}

@test "sets: pnwf absent from PATH is agent pnwf-unavailable" {
  mk_set setA r1 r2
  mkdir -p "$FX/minbin"
  local t
  for t in git jq lsof awk date basename cat tr grep sort head env bash dirname mktemp bd sed uname; do
    ln -sf "$(command -v "$t")" "$FX/minbin/$t"
  done
  PATH="$FX/minbin" run scan_sets
  [ "$(jq -r '.sets[0] | "\(.action) \(.reason)"' <<<"$output")" = "agent pnwf-unavailable" ]
}

@test "sets: pnwf exiting non-zero is agent pnwf-unavailable" {
  mk_set setA r1 r2
  fake_pnwf 3 ""
  doc="$(scan_sets)"
  [ "$(jq -r '.sets[0] | "\(.action) \(.reason)"' <<<"$doc")" = "agent pnwf-unavailable" ]
}

@test "sets: a not-yet-worked set (open bead, zero commits) is an agent set carrying the member reason" {
  id="$(pool_bead open)"
  mk_set "$id" r1 r2
  fake_pnwf 0 ""
  doc="$(scan_sets)"
  [ "$(jq -r '.sets[0] | "\(.action) \(.reason)"' <<<"$doc")" = "agent idle-clean-unworked" ]
}
