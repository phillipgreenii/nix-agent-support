#!/usr/bin/env bash
# probe-pg2-6c85x.sh — verification probe for bead pg2-6c85x: the `GIT_*` variables
# that name a PROGRAM GIT EXECUTES are screened like the pre-subcommand `-c` of
# their twin config key.
#
# THE SIBLING IS scripts/probe-pg2-a12rl.sh, which covers the config-SOURCE family
# (`GIT_CONFIG*` — a config FILE or key/value PAIRS). This one covers the family one
# level down: no config key in between, the variable IS the program. Its section 3
# is where these two variables were first measured, out of scope for that bead.
#
# FOUR HALVES, because four separate claims have to hold.
#
#  1. IS THE ENV ROUTE REAL? A throwaway repo, a marker program, and one run per
#     candidate variable — so "git executes this" is MEASURED on this machine's git
#     rather than read off the documentation. This is the half that justifies gating
#     anything; a variable git ignores must not be gated (see the lowercase controls).
#  2. IS THE READING PRECEDENCE-CLEAN? A marker keyed to a variable that LOSES to an
#     existing config value or to an already-exported variable reads as a FALSE
#     NEGATIVE. pg2-a12rl hit exactly this with `GIT_CONFIG_SYSTEM`; this probe hits
#     it twice (`GIT_EXTERNAL_DIFF` vs this machine's `diff.external=difft`, and
#     `-c core.editor` vs an exported `GIT_EDITOR`), so both are re-measured with the
#     competing source removed.
#  3. DOES THE PAGER NEED A TERMINAL? `git_pager()` takes `isatty(1)` and returns
#     NULL without one, `--paginate` notwithstanding — so a no-sink reading under a
#     pipe says nothing about the VARIABLE. Measured three ways: piped (no sink),
#     under a real pty via expect(1) (SINK), and through `git var GIT_PAGER`, which
#     resolves the program with no tty at all. The `-c core.pager` twin is measured on
#     the same instruments, which is what makes the relation to it sound.
#  4. WHAT DOES THE HOOK ANSWER? The binary is built from the CURRENT worktree and the
#     RAW emitted output is printed, not just a decision word. The raw output is the
#     point: a withdrawn Approve must serialize to the empty object `{}`, and the
#     failure this probe detects is the same command coming back as
#     `permissionDecision: "allow"` because a later rule in the chain re-approved the
#     leaf. A decision-only probe cannot tell `{}` from a missing key.
#
# WHAT `{}` MEANS: claude-code decides — auto-approve mode, then settings
# pre-authorization, then the prompt. It is the SAME verdict the `-c` route has
# reached since before this bead; see docs/adr/0043-ceta-rule-verdict-vocabulary.md's
# Decision, and `gitProgramEnvVars` in internal/rules/git/git.go for the ruling, for
# why the screen is value-blind, and for the two variables deliberately DECLINED.
#
# ISOLATION: the git half runs entirely inside a throwaway repo under a mktemp
# directory — never a real checkout. The hook half points XDG_DATA_HOME at a
# throwaway directory so probe rows land in a scratch asks.db and never reach the
# real corpus.
set -uo pipefail

pkg_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work="$(mktemp -d "${TMPDIR:-/tmp}/pg2-6c85x-probe.XXXXXX")"
trap 'rm -rf "$work"' EXIT

marker="$work/marker.sh"
hits="$work/hits.log"
repo="$work/repo"

cat >"$marker" <<EOF
#!/bin/sh
echo "MARKER-RAN \$*" >> "$hits"
exit 1
EOF
chmod +x "$marker"

# A pager is handed git's output on stdin; a marker that exits without draining it
# would make git die of SIGPIPE instead of recording the run.
pager_marker="$work/pager-marker.sh"
cat >"$pager_marker" <<EOF
#!/bin/sh
echo "MARKER-RAN \$*" >> "$hits"
cat >/dev/null
exit 0
EOF
chmod +x "$pager_marker"

mkdir -p "$repo"
git -C "$repo" init --quiet
git -C "$repo" config user.email probe@invalid
git -C "$repo" config user.name probe
echo hello >"$repo/f.txt"
git -C "$repo" add f.txt
git -C "$repo" commit --quiet -m init
echo two >"$repo/g.txt"
git -C "$repo" add g.txt
git -C "$repo" commit --quiet -m second # a second commit, so `rebase -i HEAD~1` has work
echo changed >"$repo/f.txt"             # an unstaged change, so `git diff` has work to do

echo "git: $(git --version)"
echo "machine config in play: diff.external=$(git config --get diff.external || echo '<unset>')"
echo "ambient env in play:    GIT_EDITOR=${GIT_EDITOR:-<unset>}  PAGER=${PAGER:-<unset>}"
echo

# ran <label> <env assignment>... -- <git args>...
ran() {
  local label="$1" && shift
  local -a envs=()
  while [ "$1" != "--" ]; do
    envs+=("$1")
    shift
  done
  shift
  : >"$hits"
  local err
  err=$(env "${envs[@]}" git -C "$repo" "$@" </dev/null 2>&1 >/dev/null)
  if [ -s "$hits" ]; then
    printf 'SINK-REACHED  %-38s %s\n' "$label" "$(head -1 "$hits" | cut -c1-70)"
  else
    printf 'no-sink       %-38s %s\n' "$label" "$(printf '%s' "${err:0:70}" | tr '\n' ' ')"
  fi
}

echo "=== 1. WHICH PROGRAM-NAMING GIT_* VARIABLES REACH AN EXECUTION SINK ==="
ran 'GIT_EXTERNAL_DIFF   (diff.external)' "GIT_EXTERNAL_DIFF=$marker" -- diff
ran 'GIT_SSH_COMMAND     (core.sshCommand)' "GIT_SSH_COMMAND=$marker" -- ls-remote ssh://invalid.invalid/x
ran 'GIT_SSH             (core.sshCommand)' "GIT_SSH=$marker" -- ls-remote ssh://invalid.invalid/x
ran 'GIT_EDITOR          (core.editor)' "GIT_EDITOR=$marker" -- commit --amend
echo "  GIT_ASKPASS         (core.askPass) — needs a credential prompt, so it is driven"
echo '  through `git credential fill` rather than a network fetch (DNS fails first):'
: >"$hits"
askpass_err=$(printf 'protocol=https\nhost=example.invalid\n\n' |
  env GIT_ASKPASS="$marker" GIT_TERMINAL_PROMPT=1 git -C "$repo" credential fill 2>&1 >/dev/null)
if [ -s "$hits" ]; then
  printf '    SINK-REACHED  %-36s %s\n' 'GIT_ASKPASS' "$(head -1 "$hits" | cut -c1-70)"
else
  printf '    no-sink       %-36s %s\n' 'GIT_ASKPASS' "${askpass_err:0:70}"
fi

echo
echo "=== 2. THE TWO DECLINED VARIABLES — measured anyway, declined for a RECORDED reason ==="
echo "    (see declinedGitProgramEnvVars: the rebase carve-out this rule itself requires,"
echo "     and core.gitProxy's deliberately-deferred alternate-transport ruling)"
ran 'GIT_SEQUENCE_EDITOR (sequence.editor)' "GIT_SEQUENCE_EDITOR=$marker" -- rebase -i HEAD~1
ran 'GIT_PROXY_COMMAND   (core.gitProxy)' "GIT_PROXY_COMMAND=$marker" -- ls-remote git://invalid.invalid/x

echo
echo "=== 3. THE PAGER NEEDS A TERMINAL — one variable, three instruments ==="
echo "    git resolves the pager as git_pager(isatty(1)), which returns NULL with no tty,"
echo "    so the piped reading is about the PROBE's stdout and not about the variable."
ran 'GIT_PAGER, piped stdout' "GIT_PAGER=$pager_marker" -- log
ran 'GIT_PAGER, --paginate, piped stdout' "GIT_PAGER=$pager_marker" -- --paginate log
if command -v expect >/dev/null 2>&1; then
  cat >"$work/pty.exp" <<'EXP'
#!/usr/bin/env expect
set timeout 20
log_user 0
eval spawn -noecho [lrange $argv 0 end]
expect eof
EXP
  chmod +x "$work/pty.exp"
  for pair in "GIT_PAGER=$pager_marker:GIT_PAGER under a real pty" ":-c core.pager under a real pty"; do
    assign="${pair%%:*}"
    label="${pair#*:}"
    : >"$hits"
    if [ -n "$assign" ]; then
      "$work/pty.exp" env "$assign" git -C "$repo" log >/dev/null 2>&1
    else
      "$work/pty.exp" git -C "$repo" -c "core.pager=$pager_marker" log >/dev/null 2>&1
    fi
    if [ -s "$hits" ]; then
      printf 'SINK-REACHED  %-38s %s\n' "$label" "(marker ran)"
    else
      printf 'no-sink       %-38s %s\n' "$label" "(no marker)"
    fi
  done
else
  echo "  expect(1) absent — pty rows SKIPPED, so the pager sink is unproven on this host"
fi
echo "  and with no tty at all, git still RESOLVES the program it would run:"
printf '    git var GIT_PAGER  with GIT_PAGER set   -> %s\n' "$(env GIT_PAGER=/MARK/pager git -C "$repo" var GIT_PAGER)"
printf '    git var GIT_PAGER  with -c core.pager   -> %s\n' "$(env -u GIT_PAGER git -C "$repo" -c core.pager=/MARK/cp var GIT_PAGER)"
printf '    git var GIT_PAGER  with BOTH (env wins) -> %s\n' "$(env GIT_PAGER=/MARK/pager git -C "$repo" -c core.pager=/MARK/cp var GIT_PAGER)"

echo
echo "=== 4. PRECEDENCE TRAPS: two readings that looked decisive and were not ==="
echo "  4a. GIT_EXTERNAL_DIFF vs this machine's configured diff.external=difft."
echo "      It wins BOTH ways, so a configured external differ does not mask the route:"
ran 'with machine config intact' "GIT_EXTERNAL_DIFF=$marker" -- diff
ran 'with global+system nulled' GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null "GIT_EXTERNAL_DIFF=$marker" -- diff
echo '  4b. `-c core.editor` reads NO-SINK in a shell that already exports GIT_EDITOR,'
echo "      because the env variable OUTRANKS the config key. Unset it and the marker runs:"
ran '-c core.editor, GIT_EDITOR ambient' -- -c "core.editor=$marker" commit --amend
: >"$hits"
env -u GIT_EDITOR git -C "$repo" -c "core.editor=$marker" commit --amend </dev/null >/dev/null 2>&1
if [ -s "$hits" ]; then
  printf 'SINK-REACHED  %-38s %s\n' '-c core.editor, GIT_EDITOR unset' "$(head -1 "$hits" | cut -c1-60)"
else
  printf 'no-sink       %-38s %s\n' '-c core.editor, GIT_EDITOR unset' '(still no marker)'
fi
printf '    git var GIT_EDITOR with BOTH (env wins) -> %s\n' "$(env GIT_EDITOR=/MARK/ed git -C "$repo" -c core.editor=/MARK/ce var GIT_EDITOR)"

echo
echo "=== 5. CONTROLS: spellings git does NOT read, so the rule must not gate them ==="
ran 'git_external_diff (lowercase)' "git_external_diff=$marker" -- diff
ran 'git_ssh_command (lowercase)' "git_ssh_command=$marker" -- ls-remote ssh://invalid.invalid/x
ran 'GIT_SSH_VARIANT (names no program)' GIT_SSH_VARIANT=ssh -- ls-remote ssh://invalid.invalid/x
ran 'no assignment at all' PROBE=1 -- diff

echo
echo "=== 6. THE ARGV TWINS, which have been screened since before this bead ==="
ran '-c diff.external' -- -c "diff.external=$marker" diff
ran '-c core.sshCommand' -- -c "core.sshCommand=$marker" ls-remote ssh://invalid.invalid/x
ran '-c sequence.editor' -- -c "sequence.editor=$marker" rebase -i HEAD~1

# ---------------------------------------------------------------------------
bin="$work/ceta"
(cd "$pkg_root" && go build -o "$bin" ./cmd/claude-extended-tool-approver)
export XDG_DATA_HOME="$work/xdg-data"
mkdir -p "$XDG_DATA_HOME"

probe() {
  local cmd="$1" out
  out="$(jq -cn --arg c "$cmd" \
    '{hook_event_name:"PreToolUse",session_id:"pg2-6c85x-probe",cwd:"/tmp",permission_mode:"default",tool_name:"Bash",tool_input:{command:$c}}' |
    "$bin" 2>/dev/null)"
  printf '%-84s -> %s\n' "$cmd" "$out"
}

echo
echo "=== 7. THE FIX: every screened spelling emits {} — the SAME verdict as the -c route ==="
probe 'GIT_EXTERNAL_DIFF=/tmp/evil git diff'
probe 'git -c diff.external=/tmp/evil diff'
probe 'GIT_SSH_COMMAND=/tmp/evil git fetch origin'
probe 'git -c core.sshCommand=/tmp/evil fetch origin'
probe 'GIT_SSH=/tmp/evil git fetch origin'
probe 'GIT_PAGER=/tmp/evil git log'
probe 'GIT_EDITOR=/tmp/evil git commit --amend'
probe 'GIT_ASKPASS=/tmp/evil git fetch origin'
probe 'env GIT_EXTERNAL_DIFF=/tmp/evil git diff'
probe 'GIT_PAGER=/tmp/evil git log && echo done'

echo
echo "=== 8. THE MEASURED PROMPT-VOLUME COST: value-blind, so the benign idioms prompt too ==="
echo "    (the -c route is value-blind as well, so sparing them would make the env"
echo "     spelling WEAKER than the argv one — this bead's defect, mirror-imaged)"
probe 'GIT_PAGER=cat git log'
probe 'GIT_EDITOR=true git commit --amend'
probe 'GIT_EXTERNAL_DIFF= git diff'
probe 'GIT_SSH_COMMAND="ssh -i /tmp/k" git fetch origin'

echo
echo "=== 9. NOT WEAKENED: a decisive verdict survives the prefix (the demotion, not a short-circuit) ==="
probe 'GIT_PAGER=/tmp/evil git tag v1'
probe 'GIT_EXTERNAL_DIFF=/tmp/evil git push --force origin main'
probe 'GIT_EDITOR=/tmp/evil git config core.hooksPath /tmp/h'
echo "  ... and the SAME two shapes on the incumbent -c route, which DOES weaken them"
echo "  (its own defect, filed as pg2-6f4q9 — this bead deliberately does not copy it):"
probe 'git -c diff.external=/tmp/evil tag v1'
probe 'git -c diff.external=/tmp/evil push --force origin main'

echo
echo "=== 10. NOT WIDENED: traffic git treats as naming no caller program keeps its allow ==="
probe 'git diff'
probe 'FOO=bar git status'
probe 'git_external_diff=/tmp/evil git diff'
probe 'GIT_PAGERX=/tmp/evil git log'
probe 'GIT_SSH_VARIANT=ssh git fetch origin'
probe 'GIT_DIR=/other git log'
probe 'git commit -m "screen GIT_EXTERNAL_DIFF/GIT_SSH_COMMAND (pg2-6c85x)"'
echo "  ... and the two DECLINED variables, whose reasons are recorded in-code:"
probe 'GIT_SEQUENCE_EDITOR=: git rebase -i main'
probe 'GIT_PROXY_COMMAND=/tmp/evil git fetch origin'

echo
echo "=== 11. NOT REACHED, recorded: a PERSISTENT export is a different leaf (pg2-xjt1s) ==="
echo "    (hasGitConfigEnvInjection and hasRedirectEnvVar have the identical limit)"
probe 'export GIT_EXTERNAL_DIFF=/tmp/evil; git diff'

echo
echo "asklog isolation: probe rows written under $XDG_DATA_HOME (discarded on exit)"
