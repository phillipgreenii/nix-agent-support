#!/usr/bin/env bash
# probe-pg2-a12rl.sh — verification probe for bead pg2-a12rl: the `GIT_CONFIG_*`
# env spelling of git config injection is screened like the pre-subcommand `-c`.
#
# TWO HALVES, because the bead's claim has two halves.
#
#  1. IS THE ENV ROUTE REAL? A throwaway repo, a marker program, and one run per
#     candidate variable — so "git honours this variable" is MEASURED on this
#     machine's git rather than read off the documentation. This is the half that
#     justifies gating anything at all; a variable git ignores must not be gated
#     (see the lowercase control, which git really does ignore).
#  2. WHAT DOES THE HOOK ANSWER? The binary is built from the CURRENT worktree and
#     the RAW emitted output is printed, not just a decision word. The raw output is
#     the point: a withdrawn Approve must serialize to the empty object `{}`, and the
#     failure this probe detects is the same command coming back as
#     `permissionDecision: "allow"` because a later rule in the chain re-approved the
#     leaf. A decision-only probe cannot tell `{}` from a missing key.
#
# WHAT `{}` MEANS: claude-code decides — auto-approve mode, then settings
# pre-authorization, then the prompt. It is the SAME verdict the `-c` route has
# reached since before this bead; see docs/adr/0043-ceta-rule-verdict-vocabulary.md's
# Decision, and `hasGitConfigEnvInjection` in internal/rules/git/git.go for the
# ruling and for why the screen is key-blind.
#
# ONE READING THAT LOOKED DECISIVE AND WAS NOT, reproduced here on purpose:
# `GIT_CONFIG_SYSTEM` shows NO SINK for `core.fsmonitor` and `diff.external` on this
# machine — not because git ignores the variable, but because `~/.gitconfig` sets
# both keys and GLOBAL outranks SYSTEM. The `core.sshCommand` row is the same
# variable winning, with a key set nowhere else. A no-sink reading against ONE key is
# not evidence about the VARIABLE.
#
# ISOLATION: the git half runs entirely inside a throwaway repo under a mktemp
# directory — never a real checkout. The hook half points XDG_DATA_HOME at a
# throwaway directory so probe rows land in a scratch asks.db and never reach the
# real corpus.
set -uo pipefail

pkg_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work="$(mktemp -d "${TMPDIR:-/tmp}/pg2-a12rl-probe.XXXXXX")"
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

mk_cfg() { # mk_cfg <file> <section> <key> — a config file naming the marker
  printf '[%s]\n\t%s = %s\n' "$2" "$3" "$marker" >"$1"
}
mk_cfg "$work/fsmonitor.cfg" core fsmonitor
mk_cfg "$work/sshcommand.cfg" core sshCommand

mkdir -p "$repo"
git -C "$repo" init --quiet
git -C "$repo" config user.email probe@invalid
git -C "$repo" config user.name probe
echo hello >"$repo/f.txt"
git -C "$repo" add f.txt
git -C "$repo" commit --quiet -m init
echo changed >"$repo/f.txt" # an unstaged change, so `git diff` has work to do

echo "git: $(git --version)"
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
  err=$(env "${envs[@]}" git -C "$repo" "$@" 2>&1 >/dev/null)
  if [ -s "$hits" ]; then
    printf 'SINK-REACHED  %-30s\n' "$label"
  else
    printf 'no-sink       %-30s %s\n' "$label" "${err:0:80}"
  fi
}

echo "=== 1. WHICH GIT_CONFIG_* VARIABLES REACH AN EXECUTION SINK ==="
ran 'COUNT/KEY_0/VALUE_0' \
  GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=core.fsmonitor "GIT_CONFIG_VALUE_0=$marker" -- status --porcelain
ran 'COUNT triple, diff.external' \
  GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=diff.external "GIT_CONFIG_VALUE_0=$marker" -- diff
ran 'KEY case-insensitive' \
  GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=CORE.FSMonitor "GIT_CONFIG_VALUE_0=$marker" -- status --porcelain
ran 'GIT_CONFIG_GLOBAL' "GIT_CONFIG_GLOBAL=$work/fsmonitor.cfg" -- status --porcelain
ran 'GIT_CONFIG_SYSTEM (fsmonitor)' "GIT_CONFIG_SYSTEM=$work/fsmonitor.cfg" -- status --porcelain
ran 'GIT_CONFIG_SYSTEM (sshCommand)' "GIT_CONFIG_SYSTEM=$work/sshcommand.cfg" -- ls-remote ssh://invalid.invalid/x
ran 'GIT_CONFIG_PARAMETERS' "GIT_CONFIG_PARAMETERS='core.fsmonitor=$marker'" -- status --porcelain
ran 'GIT_CONFIG (legacy)' "GIT_CONFIG=$work/fsmonitor.cfg" -- status --porcelain
echo "  GIT_CONFIG (legacy), read back through \`git config\` — it IS that command's own file:"
printf '    %s\n' "$(env GIT_CONFIG="$work/fsmonitor.cfg" git -C "$repo" config --get core.fsmonitor)"

echo
echo "=== 2. CONTROLS: spellings git does NOT read as config, so the rule must not gate them ==="
ran 'lowercase names' \
  git_config_count=1 git_config_key_0=core.fsmonitor "git_config_value_0=$marker" -- status --porcelain
ran 'NOSYSTEM (suppresses only)' GIT_CONFIG_NOSYSTEM=1 -- status --porcelain
ran 'COUNT=2, one pair (partial)' \
  GIT_CONFIG_COUNT=2 GIT_CONFIG_KEY_0=core.fsmonitor "GIT_CONFIG_VALUE_0=$marker" -- status --porcelain
ran 'pair with no COUNT' \
  GIT_CONFIG_KEY_0=core.fsmonitor "GIT_CONFIG_VALUE_0=$marker" -- status --porcelain
ran 'no injection at all' PROBE=1 -- status --porcelain

echo
echo "=== 3. OUT OF SCOPE, MEASURED ANYWAY: the GIT_* env twins of gated config sinks ==="
echo "    (diff.external and core.sshCommand by another name — a follow-up bead, not this one)"
ran 'GIT_EXTERNAL_DIFF' "GIT_EXTERNAL_DIFF=$marker" -- diff
ran 'GIT_SSH_COMMAND' "GIT_SSH_COMMAND=$marker" -- ls-remote ssh://invalid.invalid/x

# ---------------------------------------------------------------------------
bin="$work/ceta"
(cd "$pkg_root" && go build -o "$bin" ./cmd/claude-extended-tool-approver)
export XDG_DATA_HOME="$work/xdg-data"
mkdir -p "$XDG_DATA_HOME"

probe() {
  local cmd="$1" out
  out="$(jq -cn --arg c "$cmd" \
    '{hook_event_name:"PreToolUse",session_id:"pg2-a12rl-probe",cwd:"/tmp",permission_mode:"default",tool_name:"Bash",tool_input:{command:$c}}' |
    "$bin" 2>/dev/null)"
  printf '%-104s -> %s\n' "$cmd" "$out"
}

echo
echo "=== 4. THE FIX: every screened spelling emits {} — the SAME verdict as the -c route ==="
probe 'GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=core.fsmonitor GIT_CONFIG_VALUE_0=/tmp/evil git status'
probe 'git -c core.fsmonitor=/tmp/evil status'
probe 'GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=core.pager GIT_CONFIG_VALUE_0=/tmp/evil git log'
probe 'GIT_CONFIG_GLOBAL=/tmp/evil.cfg git status'
probe 'GIT_CONFIG_SYSTEM=/tmp/evil.cfg git status'
probe "GIT_CONFIG_PARAMETERS='core.fsmonitor=/tmp/evil' git status"
probe 'GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=core.fsmonitor git status'
probe 'GIT_CONFIG_NOSYSTEM=1 git status'
probe 'GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=core.fsmonitor GIT_CONFIG_VALUE_0=/tmp/evil git status && echo done'

echo
echo "=== 5. THE MEASURED PROMPT-VOLUME COST: the in-corpus merge-driver idiom now prompts ==="
probe 'GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=merge.mergiraf.driver GIT_CONFIG_VALUE_0= git rebase --autostash origin/main'

echo
echo "=== 6. NOT WEAKENED: a decisive verdict survives the prefix (the demotion, not a short-circuit) ==="
probe 'GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=user.name GIT_CONFIG_VALUE_0=x git tag v1'
probe 'GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=user.name GIT_CONFIG_VALUE_0=x git push --force origin main'
probe 'GIT_CONFIG_GLOBAL=/tmp/evil.cfg git config core.hooksPath /tmp/h'
echo "  ... and the SAME two shapes on the incumbent -c route, which DOES weaken them"
echo "  (its own defect, recorded for a follow-up bead — this bead deliberately does not copy it):"
probe 'git -c user.name=x tag v1'
probe 'git -c user.name=x push --force origin main'

echo
echo "=== 7. NOT WIDENED: traffic git treats as carrying no caller config keeps its allow ==="
probe 'git status'
probe 'FOO=bar git status'
probe 'git_config_count=1 git_config_key_0=core.fsmonitor git_config_value_0=/tmp/evil git status'
probe 'GIT_CONFIGURATION=1 git status'
probe 'GIT_DIR=/other git log'
probe 'git commit -m "screen GIT_CONFIG_COUNT/KEY_0/VALUE_0 (pg2-a12rl)"'
echo
echo "=== 8. NOT REACHED, recorded: a PERSISTENT export is a different leaf ==="
echo "    (hasRedirectEnvVar has the identical limit for GIT_DIR; its own bead)"
probe 'export GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=core.fsmonitor GIT_CONFIG_VALUE_0=/tmp/evil; git status'

echo
echo "asklog isolation: probe rows written under $XDG_DATA_HOME (discarded on exit)"
