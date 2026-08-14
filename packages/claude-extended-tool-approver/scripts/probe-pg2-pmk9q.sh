#!/usr/bin/env bash
# probe-pg2-pmk9q.sh — verification probe for bead pg2-pmk9q: the `secrets` rule
# stops prompting on READ-ONLY inspection of its own source directory, because the
# bare `secrets` path component is now SKIPPED INSIDE A GIT REPOSITORY.
#
# THE DEFECT: secretpath's `secretDirs` matches the whole component `secrets`, so
# every path with a `secrets/` component matched — including this repository's own
# packages/claude-extended-tool-approver/internal/rules/secrets/. Four confirmed
# asklog rows (327201, 327344, 327371, 327471), all `ask`, all read-only inspection
# issued by a review agent with NO file-edit tools. Anyone working the CETA bead
# queue paid that prompt repeatedly.
#
# THE FIX, per the OPERATOR RULING of 2026-08-13: the arm stays LEXICAL but is
# skipped for a READ of a path inside a git repository. Operator rationale, verbatim:
# "anything that is in a git repo is not secret. if someone does have secrets in a
# repo, then they can explicitly set those paths in the config" — and the escape
# hatch is real, because patheval.LoadSandboxFilesystemConfig already merges the
# PROJECT-level .claude/settings.json. This SUPERSEDES the bead's own sanctioned
# option 1 (narrow by role and direction) and is strictly broader: it covers ANY
# project with an `internal/.../secrets/` package, which the bead's option 3 could
# not.
#
# THE THREE PINNED GUARDS, and their status under the ruling:
#
#   guard 1  ~/secrets/prod.env still Asks     STILL HOLDS (not inside a repo)
#   guard 2  deploy/secrets/token still Asks   DELIBERATELY OVERRIDDEN by the
#                                              operator, with the guard's text in
#                                              front of them. A deploy/ tree is
#                                              inside a repo, so covering it becomes
#                                              a project-level denyRead entry. This
#                                              is the ONE coverage reduction in the
#                                              ruling; the row below asserts the
#                                              OVERRIDDEN behaviour on purpose.
#   guard 3  a WRITE under secrets/ is not     STILL REQUIRED, and now MORE
#            loosened                          important — the read relaxation is
#                                              broader than option 1's, so keeping
#                                              the directions distinguished is what
#                                              bounds it.
#
# Section 3 states guard 3 as a RELATION — "the write spelling is never less
# restrictive than the read spelling of the same path" — because that is the property
# the ruling requires, and a table of hardcoded verdicts cannot keep it: a later
# retuning that relaxed writes to match reads would leave every hardcoded row passing.
#
# NOT IN SCOPE and still forbidden: widening pg2-ia640.5's prose/message-arg skip to
# cover this case. That skip drops real PATH arguments only by POSITION; reaching
# this case through it would start skipping genuine path operands, which is a
# security regression. `git diff` for this bead must not touch it.
#
# ISOLATION: XDG_DATA_HOME points at a throwaway directory on EVERY invocation, so
# probe rows land in a scratch asks.db and never reach the shared production corpus.
# NOTHING here runs `ceta evaluate` / `baseline` / `compare` — all three open the
# production asklog READ-WRITE (bead pg2-cbihz).
set -uo pipefail

pkg_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work="$(mktemp -d "${TMPDIR:-/tmp}/pg2-pmk9q-probe.XXXXXX")"
trap 'rm -rf "$work"' EXIT

bin="$work/ceta"
(cd "$pkg_root" && go build -o "$bin" ./cmd/claude-extended-tool-approver) || exit 1
export XDG_DATA_HOME="$work/xdg-data"
mkdir -p "$XDG_DATA_HOME"

decide() { # decide <cwd> <tool> <json tool_input> -> "<decision>"
  jq -cn --arg w "$1" --arg t "$2" --argjson ti "$3" \
    '{hook_event_name:"PreToolUse",session_id:"pg2-pmk9q-probe",cwd:$w,permission_mode:"default",tool_name:$t,tool_input:$ti}' |
    "$bin" 2>/dev/null |
    jq -r '.hookSpecificOutput.permissionDecision // .permissionDecision // "abstain"' 2>/dev/null
}

probe() { # probe <label> <cwd> <bash command>
  printf '  %-52s -> %s\n' "$1" "$(decide "$2" Bash "$(jq -cn --arg c "$3" '{command:$c}')")"
}

probe_file() { # probe_file <label> <cwd> <tool> <path>
  printf '  %-52s -> %s\n' "$1" "$(decide "$2" "$3" "$(jq -cn --arg p "$4" '{file_path:$p}')")"
}

# ---------------------------------------------------------------------------
# 1. THE REPORTED DEFECT, against the REAL path it was reported for.
# ---------------------------------------------------------------------------
self="$pkg_root/internal/rules/secrets/secrets.go"
repo_root="$(cd "$pkg_root" && git rev-parse --show-toplevel 2>/dev/null || echo '')"
echo '=== 1. MUST CHANGE: read-only inspection of the secrets rule OWN source ==='
if [[ -z $repo_root ]]; then
  echo '    SKIPPED — this checkout is not a git working tree (nix build sandbox),'
  echo '    so the premise of the relaxation does not hold here. Section 2 carries'
  echo '    the guarantee against a synthetic repo instead.'
else
  echo "    repo root: $repo_root"
  probe 'cat <abs>/internal/rules/secrets/secrets.go' "$repo_root" "cat $self"
  probe 'cat <rel>/internal/rules/secrets/secrets.go' "$pkg_root" 'cat internal/rules/secrets/secrets.go'
  probe 'git show HEAD:.../rules/secrets/secrets.go' "$pkg_root" 'git show HEAD:internal/rules/secrets/secrets.go'
  probe 'git grep -n func .../rules/secrets/' "$pkg_root" "git grep -n -E 'func ' -- internal/rules/secrets/"
  probe 'rg -n Evaluate .../rules/secrets/secrets.go' "$pkg_root" 'rg -n Evaluate internal/rules/secrets/secrets.go'
  probe_file 'Read <abs>/internal/rules/secrets/secrets.go' "$repo_root" Read "$self"
  echo
  echo '    ...and guard 3: a WRITE to the SAME file is NOT relaxed.'
  probe_file 'Write <abs>/internal/rules/secrets/secrets.go' "$repo_root" Write "$self"
fi
echo

# ---------------------------------------------------------------------------
# 2. THE THREE GUARDS, against a synthetic tree that holds every shape.
# ---------------------------------------------------------------------------
root="$work/fixture"
mkdir -p "$root/secrets" "$root/deploy/secrets" \
  "$root/repo/.git" "$root/repo/internal/rules/secrets" "$root/repo/deploy/secrets" \
  "$root/repo/secrets/.ssh" "$root/repo/config" "$root/wt/secrets"
: >"$root/secrets/prod.env"
: >"$root/deploy/secrets/token"
: >"$root/repo/internal/rules/secrets/secrets.go"
: >"$root/repo/deploy/secrets/token"
: >"$root/repo/.env"
: >"$root/repo/secrets/.ssh/id_rsa"
: >"$root/repo/config/api-token.json"
: >"$root/repo/README.md"
: >"$root/wt/secrets/token"
# A WORKTREE's `.git` is a FILE, not a directory. Agents here work almost entirely
# in `.worktrees/<name>` checkouts, so a dir-only repo test would miss them all.
printf 'gitdir: %s\n' "$root/repo/.git/worktrees/wt" >"$root/wt/.git"

echo '=== 2. Synthetic tree: RELAXED vs NOT RELAXED, adjacent ==='
echo '    RELAXED (inside a git repo, READ):'
probe 'in-repo internal/rules/secrets/secrets.go' "$root" "cat $root/repo/internal/rules/secrets/secrets.go"
probe 'in-repo deploy/secrets/token (guard 2 OVERRIDDEN)' "$root" "cat $root/repo/deploy/secrets/token"
probe 'in-WORKTREE secrets/token (.git is a FILE)' "$root" "cat $root/wt/secrets/token"
echo
echo '    NOT RELAXED — guard 1: outside any repo the arm still fires.'
probe 'guard 1: <root>/secrets/prod.env' "$root" "cat $root/secrets/prod.env"
probe 'guard 2 spelling OUTSIDE a repo: deploy/secrets/token' "$root" "cat $root/deploy/secrets/token"
echo
echo '    NOT RELAXED — the repo-BLIND arms. `.env` inside a repo is the most common'
echo '    real credential file an agent reads, and `.ssh` / `*token*.json` name a'
echo '    specific credential store, so none of them is scoped by the repo test.'
probe 'in-repo .env (ruling decision 2)' "$root" "cat $root/repo/.env"
probe 'in-repo secrets/.ssh/id_rsa (stronger arm wins)' "$root" "cat $root/repo/secrets/.ssh/id_rsa"
probe 'in-repo config/api-token.json' "$root" "cat $root/repo/config/api-token.json"
echo
echo '    UNCHANGED — a bare word is still not a path (pg2-ia640.2).'
probe 'kubectl get secrets' "$root/repo" 'kubectl get secrets'
echo

# ---------------------------------------------------------------------------
# 3. GUARD 3 AS A RELATION.
# ---------------------------------------------------------------------------
rank() { case "$1" in allow) echo 0 ;; abstain) echo 1 ;; ask) echo 2 ;; deny) echo 3 ;; *) echo 1 ;; esac }
echo '=== 3. GUARD 3 as a RELATION: write is never less restrictive than read ==='
for p in \
  "$root/repo/internal/rules/secrets/secrets.go" \
  "$root/repo/deploy/secrets/token" \
  "$root/wt/secrets/token" \
  "$root/secrets/prod.env" \
  "$root/repo/.env" \
  "$root/repo/secrets/.ssh/id_rsa" \
  "$root/repo/README.md"; do
  rd="$(decide "$root" Read "$(jq -cn --arg p "$p" '{file_path:$p}')")"
  wr="$(decide "$root" Write "$(jq -cn --arg p "$p" '{file_path:$p}')")"
  verdict=OK
  [[ $(rank "$wr") -lt $(rank "$rd") ]] && verdict='*** VIOLATION ***'
  printf '  %-46s read=%-8s write=%-8s %s\n' "${p#"$root/"}" "$rd" "$wr" "$verdict"
done
echo

echo "asklog isolation: probe rows written under $XDG_DATA_HOME (discarded on exit)"

# ===========================================================================
# MEASURED 2026-08-14 on branch drain/ceta-wave2. `base` is the same worktree with
# ONLY this bead's three files reverted to HEAD (other agents' concurrent wave-2
# changes present in both), so the delta below is this bead's alone.
#
#   SECTION 1 (the real reported path)                base -> patched
#     cat <abs>/internal/rules/secrets/secrets.go     ask  -> allow
#     cat <rel>/internal/rules/secrets/secrets.go     ask  -> allow
#     git show HEAD:.../rules/secrets/secrets.go      ask  -> allow
#     git grep -n func -- internal/rules/secrets/     ask  -> allow
#     rg -n Evaluate .../secrets/secrets.go           ask  -> allow
#     Read <abs>/.../secrets/secrets.go               ask  -> allow
#     Write <abs>/.../secrets/secrets.go (guard 3)    ask  -> ask
#
#   SECTION 2 (synthetic tree)
#     in-repo internal/rules/secrets/secrets.go       ask  -> allow
#     in-repo deploy/secrets/token (guard 2)          ask  -> allow
#     in-WORKTREE secrets/token                       ask  -> allow
#     guard 1: <root>/secrets/prod.env                ask  -> ask
#     deploy/secrets/token OUTSIDE a repo             ask  -> ask
#     in-repo .env                                    ask  -> ask
#     in-repo secrets/.ssh/id_rsa                     ask  -> ask
#     in-repo config/api-token.json                   ask  -> ask
#     kubectl get secrets                             allow-> allow
#
#   SECTION 3 (read vs write, both binaries): every row OK, no VIOLATION. The three
#   relaxed paths read ask -> allow while their WRITES stay ask -> ask, so the
#   relation holds strictly and the base binary satisfied it trivially (ask == ask).
#
# WHY THE RELIEVED ROWS READ `allow` AND NOT `abstain`: this rule returning
# NOT-APPLICABLE lets the chain CONTINUE, and the next rule that claims a read of a
# file inside the projectRoot read-write zone is safecmds, which approves it. So
# `allow` is what relief looks like whenever the path is in-zone — which, for a path
# inside a git repo, it usually is. A cwd that leaves the path out of every zone
# yields `{}` (abstain) instead. BOTH are relief; neither is a prompt. Read this
# half as a RELATION ("no longer ask"), never as a hardcoded word.
#
# THE WHOLE-CORPUS REPLAY IS NOT RUN HERE — it needs a SECOND tree to diff against,
# so it cannot live in a one-tree script. The recipe is at the bottom of
# scripts/probe-pg2-wq3ki.sh. What a replay of THIS bead should look for:
#   - transitions toward `allow`/abstain ONLY on rows whose reason named a path with
#     a bare `secrets` component that resolves INSIDE a git working tree, and only
#     on READ-shaped tool calls. A moved row whose path has a `.ssh`/`.env`/
#     `*token*.json` component means the relaxation leaked past GenericSecretsDir;
#   - ZERO movement on any Write/Edit/MultiEdit/Delete row (guard 3);
#   - ZERO movement on rows whose `secrets` component is outside a repo (guard 1);
#   - ZERO rows in the toward-allow direction that pg2-ia640.5's message skip could
#     explain — this bead must not have touched it.
#
# ASKLOG ROWS THIS PROBING CREATES: none in the production corpus. Every invocation
# in this script runs under a throwaway XDG_DATA_HOME, so nothing needs
# `mark-excluded`. Any interactive re-measurement done WITHOUT that isolation would
# write rows naming credential paths and IS pg2-60aon corpus pollution — isolate it.
