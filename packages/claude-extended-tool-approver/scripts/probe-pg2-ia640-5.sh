#!/usr/bin/env bash
# probe-pg2-ia640-5.sh — verification probe for bead pg2-ia640.5: a `bd` / `git` /
# `gh` command whose free-text MESSAGE merely MENTIONS a `.ssh/` or `secrets/` path
# stops costing a prompt, while every spelling that could actually REACH the file
# keeps the verdict it had.
#
# It asserts at the HOOK-OUTPUT boundary — a binary built from THIS worktree, fed
# real PreToolUse JSON — because the bead's acceptance criteria are written in terms
# of the decision the hook emits (an `ask` from the `secrets` rule becoming an
# `allow` from `safecmds`, where `bd` is allow-listed), not in terms of one rule's
# return value. The raw emitted object is printed, since a withdrawn Approve
# serializes to the empty object `{}` and a decision-only probe cannot tell `{}`
# from a missing key.
#
# TWO HALVES, matching the bead's two behaviour sections:
#
#  1. WHAT MUST CHANGE. The four observed asklog rows (313634, 325419, 325591,
#     325750), replayed VERBATIM from internal/rules/secrets/testdata/ — the same
#     fixture files the Go regression test reads, so the two cannot drift — plus the
#     `git commit -m` and `gh` shapes of the same class.
#  2. WHAT MUST NOT CHANGE — the anti-bypass guard set. `git commit -F
#     ~/.ssh/id_rsa` is the one to watch: it survives only because `-F` is absent
#     from the message tables, so a naive "drop the token after any listed flag"
#     implementation breaks it SILENTLY. `git checkout -m <pathspec>` is the second
#     (there `-m` is `--merge`, a boolean, so the next token is a real path), and
#     `git commit -- -m ~/.ssh/id_rsa` the third (after `--` a token is an operand).
#
# ON THIS MACHINE the guard rows print `deny`, not `ask`: the operator's sandbox
# config deny-lists `~/.ssh`, and secrets.decide escalates a deny-listed secret to
# Reject. `deny` is MORE restrictive than `ask`, so the guard holds; read this half
# as a RELATION ("not less restrictive than before"), never as a hardcoded word.
#
# ISOLATION: XDG_DATA_HOME points at a throwaway directory, so probe rows land in a
# scratch asks.db and never reach the shared production corpus. NOTHING here runs
# `ceta evaluate` / `baseline` / `compare` — all three open the production asklog
# READ-WRITE (bead pg2-cbihz).
set -uo pipefail

pkg_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work="$(mktemp -d "${TMPDIR:-/tmp}/pg2-ia640-5-probe.XXXXXX")"
trap 'rm -rf "$work"' EXIT

bin="$work/ceta"
(cd "$pkg_root" && go build -o "$bin" ./cmd/claude-extended-tool-approver) || exit 1
export XDG_DATA_HOME="$work/xdg-data"
mkdir -p "$XDG_DATA_HOME"

# The cwd only affects the SYMLINK-RESOLVING pass; every assertion here is decided
# by the lexical pass, which is cwd-independent.
cwd="$pkg_root"

# probe <label> <command>
probe() {
  local label="$1" cmd="$2" out
  out="$(jq -cn --arg c "$cmd" --arg w "$cwd" \
    '{hook_event_name:"PreToolUse",session_id:"pg2-ia640-5-probe",cwd:$w,permission_mode:"default",tool_name:"Bash",tool_input:{command:$c}}' |
    "$bin" 2>/dev/null)"
  printf '  %-48s -> %s\n' "$label" "${out:0:170}"
}

# probe_row <asklog row id> — replays the row VERBATIM from the shared fixture.
probe_row() {
  local id="$1" f="$pkg_root/internal/rules/secrets/testdata/asklog-row-$1.cmd"
  if [[ ! -f $f ]]; then
    printf '  row %-44s -> MISSING FIXTURE %s\n' "$id" "$f"
    return
  fi
  probe "row $id (verbatim)" "$(cat "$f")"
}

echo '=== 1. MUST CHANGE: prose that names a credential path but opens nothing ==='
echo '    (each of these was `ask` from module `secrets` before this bead)'
echo '    RELIEF IS EITHER OUTCOME: `allow` = a later rule claimed it (safecmds'
echo '    allow-lists `bd`); `{}` = no rule holds an opinion at all, which for `gh`'
echo '    and for a `git -C <dir>` whose dir does not exist is the correct answer.'
probe_row 313634 # bd close --reason "<rationale naming ~/.ssh/config>"
probe_row 325419 # bd comment <id> "<~40-line body naming ~/.ssh/agent>"
probe_row 325591 # bd create --title/--description "<prose naming ~/.ssh/authorized_keys>"
probe_row 325750 # ... | bd comment <id> '<prose>' — trailing positional, in a pipeline
probe 'git commit -m <prose naming secrets/>' 'git commit -m "drop the docs/secrets/prod.yaml example"'
probe 'git commit -m <prose naming .ssh/>' 'git commit -m "probe cert via ~/.ssh/agent glob"'
probe 'git -C dir commit -m <prose>' 'git -C /repo commit -m "probe cert via ~/.ssh/agent glob"'
probe 'bd update --append-notes <prose>' 'bd update pg2-x --append-notes "cert probe via ~/.ssh/agent glob"'
probe 'bd close --reason=<prose> (equals form)' 'bd close pg2-x --reason="cert probe via ~/.ssh/agent glob"'
probe 'gh pr comment --body <prose>' 'gh pr comment 1 --body "cert probe via ~/.ssh/agent glob"'
echo

echo '=== 2. MUST NOT CHANGE: the anti-bypass guard set ==='
echo '    (`deny` here is the sandbox deny-list escalation — see the header)'
probe 'cat ~/.ssh/id_rsa' 'cat ~/.ssh/id_rsa'
probe 'cp ~/.ssh/id_rsa /tmp (unlisted executable)' 'cp ~/.ssh/id_rsa /tmp'
probe 'notes-tool --reason <path> (unlisted)' 'notes-tool --reason ~/.ssh/id_rsa'
probe 'git commit -F <path> (THE guard)' 'git commit -F ~/.ssh/id_rsa'
probe 'git commit --file <path>' 'git commit --file ~/.ssh/id_rsa'
probe 'git checkout -m <pathspec> (-m is boolean)' 'git checkout -m ~/.ssh/config'
probe 'git commit -- -m <path> (operand after --)' 'git commit -- -m ~/.ssh/id_rsa'
probe 'gh pr create --body-file <path>' 'gh pr create --body-file ~/.ssh/id_rsa'
probe 'bd comment x --file <path>' 'bd comment x --file secrets/notes.txt'
probe 'bd comment <path> body (the id positional)' 'bd comment ~/.ssh/id_rsa body'
probe 'bd -C <path> comment x body' 'bd -C secrets/wt comment x body'
probe 'bd comment x body < secrets/x (redirection)' 'bd comment x body < secrets/x'
probe 'bd comment x "$(cat ~/.ssh/id_rsa)" (substitution)' 'bd comment x "$(cat ~/.ssh/id_rsa)"'
probe 'bash -lc cat <path> (shellDashC unwrap)' 'bash -lc "cat ~/.ssh/id_rsa"'
probe 'grep .env file.log (pg2-ia640.2 relief)' 'grep .env file.log'
probe 'grep password .env (pg2-ia640.2 regression)' 'grep password .env'
echo

echo "asklog isolation: probe rows written under $XDG_DATA_HOME (discarded on exit)"

# ===========================================================================
# MEASURED 2026-08-13 on branch drain/ceta-ask-relief, before -> after this bead
# (same script, binaries built from the tree with and without the change):
#
#   row 313634                       ask(secrets)  -> allow(engine, "all sub-commands approved")
#   row 325419                       ask(secrets)  -> allow(engine)
#   row 325591                       ask(secrets)  -> allow(engine)
#   row 325750                       ask(secrets)  -> allow(engine)
#   git commit -m <prose>            ask(secrets)  -> allow(engine)
#   git -C /repo commit -m <prose>   ask(secrets)  -> abstain ({} — /repo does not
#                                                    exist, so no rule claims it)
#   bd update --append-notes <prose> ask(secrets)  -> allow(engine)
#   gh pr comment --body <prose>     ask(secrets)  -> abstain ({} — no later rule
#                                                    claims `gh`, so the hook simply
#                                                    stops holding an opinion)
#   gh issue create -t/-b <prose>    ask(secrets)  -> ask(gh, "modifying gh issue
#                                                    command") — same verdict, now
#                                                    from the rule that owns it
#   bd close --reason=<prose>        allow         -> allow (UNCHANGED: the caller
#                                                    already skipped `-`-prefixed
#                                                    tokens, so the equals arm of
#                                                    SkipMessageArgs is explicit
#                                                    rather than load-bearing)
#   every row of section 2           unchanged (deny stayed deny, ask stayed ask)
#
# THE WHOLE-CORPUS REPLAY IS NOT RUN HERE — it needs a SECOND tree to diff against,
# so it cannot live in a one-tree script. The recipe is recorded at the bottom of
# scripts/probe-pg2-wq3ki.sh. What a replay of THIS bead should look for:
#   - transitions toward `allow` ONLY on leaves whose executable basename is bd,
#     git or gh; a transition on any other executable means the skip leaked;
#   - zero transitions away from `ask`/`deny` for any row whose reason names a path
#     that came from a `-F`/`--file`/`--body-file`/`--design-file`/`--graph` value,
#     a redirection target, or a substitution body.
