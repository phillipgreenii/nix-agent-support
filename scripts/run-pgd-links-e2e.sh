#!/usr/bin/env bash
# Opt-in end-to-end test of the pgd transcript links with the real Claude Code binary.
#
# Opt-in (operator standard V-4): NOT a commit or pre-land hook; it has its own named nix check,
# test-pgd-links-e2e. It runs only when PGD_E2E=1 is set; without it the script prints a SKIP line
# and exits 0. Run it by hand:
#
#   PGD_E2E=1 scripts/run-pgd-links-e2e.sh
#
# What it does: starts a stub Messages API server on 127.0.0.1 (a small node program embedded
# below, answering every model request with one fixed reply), starts the real `claude` in a
# uniquely named tmux session on a private tmux socket with the pgd mod loaded through
# --plugin-dir and a rule set (github-pr, jira, file-ref and regex), submits one prompt, then
# reads the pane with `tmux capture-pane -p -e -J` and asserts the OSC 8 hyperlinks the pane holds,
# as an exact list of "url => text" pairs. The fixed reply holds each matched form, the same
# forms inside code spans (which must carry NO hyperlink) and a markdown link the assistant wrote
# (which must keep its own url and never become a pgd link).
#
# Spike result (Claude Code 2.1.318, tmux 3.7b, 2026-11-10), how the binary accepts the stub:
#  - Base url: ANTHROPIC_BASE_URL=http://127.0.0.1:<port>. The binary sends HEAD /api/hello and
#    POST /v1/messages?beta=true (streaming); both are answered locally. No other host is reached.
#  - Authentication: no account login and no real credential is involved. The run gets a throwaway
#    ANTHROPIC_API_KEY (a made-up string) which the stub ignores. A fresh config dir would ask
#    "Do you want to use this API key?", so the scratch CLAUDE_CONFIG_DIR is pre-seeded with
#    .claude.json holding hasCompletedOnboarding, theme, the last 20 characters of the key under
#    customApiKeyResponses.approved, and hasTrustDialogAccepted for the scratch project directory
#    (keyed by its REAL path: /tmp is /private/tmp on macOS). Headless `claude -p` needs none of
#    that and printed the stub reply.
#  - Isolation: env -i, so no inherited credential or setting; HOME and CLAUDE_CONFIG_DIR are a
#    scratch directory removed on every exit path.
#  - Hyperlinks: Claude Code draws a link as OSC 8 only when it believes the terminal supports it;
#    under tmux with TERM=xterm-256color it falls back to "text (url)". FORCE_HYPERLINK=1 selects
#    OSC 8. The sequence is ESC ] 8 ; id=<id> ; <url> ESC \ <text> ESC ] 8 ; ; ESC \ (the string
#    terminator is ESC \, not BEL).
#  - No outside traffic: without CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC and
#    CLAUDE_CODE_DISABLE_OFFICIAL_MARKETPLACE_AUTOINSTALL the binary starts cloning its official
#    plugin marketplace into the scratch config dir in the background; the run sets both (plus
#    DISABLE_AUTOUPDATER and DISABLE_TELEMETRY) so the only host it talks to is the stub.
#  - Options: the pgd mod reads its options from the PG_CLAUDE_DISPLAY_OPTIONS variable.
#
# What it does not prove: that a click opens the link, how any terminal other than tmux draws it,
# or the account-authenticated path (the stub ignores the key).
#
# Requires: bash, tmux, node and a `claude` binary on PATH (override: PGD_E2E_CLAUDE).
# Environment: PGD_E2E=1 (required), PGD_E2E_CLAUDE (binary), PGD_E2E_PLUGIN_DIR (the pgd plugin
# directory, default: claude-marketplace/pgd of this repo), PGD_E2E_KEEP=1 (keep the scratch
# directory for inspection; the tmux session and the stub are still removed).
set -u

if [[ ${PGD_E2E:-} != "1" ]]; then
  echo "SKIP: pgd links e2e is opt-in; set PGD_E2E=1 to run it"
  exit 0
fi

here="$(cd -P "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
plugin_dir="${PGD_E2E_PLUGIN_DIR:-$here/../claude-marketplace/pgd}"
claude_bin="${PGD_E2E_CLAUDE:-$(command -v claude || true)}"
for need in tmux node; do
  command -v "$need" >/dev/null || {
    echo "FAIL: $need not on PATH" >&2
    exit 2
  }
done
[[ -n $claude_bin && -x $claude_bin ]] || {
  echo "FAIL: no claude binary (set PGD_E2E_CLAUDE)" >&2
  exit 2
}
[[ -f "$plugin_dir/.claude-plugin/plugin.json" ]] || {
  echo "FAIL: not a plugin dir: $plugin_dir" >&2
  exit 2
}
plugin_dir="$(cd -P "$plugin_dir" && pwd)"

scratch="$(cd -P "$(mktemp -d "${TMPDIR:-/tmp}/pgd-e2e.XXXXXX")" && pwd -P)"
sess="pgd-e2e-$$-$RANDOM"
stub_pid=""

cleanup() {
  tmux -L "$sess" kill-server >/dev/null 2>&1 || true
  [[ -n $stub_pid ]] && kill "$stub_pid" >/dev/null 2>&1 || true
  if [[ ${PGD_E2E_KEEP:-} == "1" ]]; then
    echo "kept scratch: $scratch" >&2
  else
    # claude flushes its config on hangup: wait (bounded) for processes still using the scratch dir
    for _ in $(seq 1 30); do
      pgrep -f "$scratch" >/dev/null 2>&1 || break
      sleep 0.2
    done
    rm -rf "$scratch"
  fi
}
trap cleanup EXIT
trap 'exit 130' INT TERM

mkdir -p "$scratch/home" "$scratch/cfg" "$scratch/work"
# The engine writes a tsconfig.json into a plugin directory it loads, so load a scratch copy and
# leave the repository untouched.
cp -R "$plugin_dir" "$scratch/plugin"
chmod -R u+w "$scratch/plugin" # a copy of read-only (nix store) files must stay removable
plugin_dir="$scratch/plugin"

# The stub: every POST /v1/messages answers the fixed reply (streamed or not).
#   (the server lives in scripts/pgd-stub-messages-server.mjs, shared with the multi-process check)
cp "$here/pgd-stub-messages-server.mjs" "$scratch/stub.mjs"

cat >"$scratch/reply.txt" <<'TXT'
Matched: acme/widgets#5 and ABC-123 and src/main.go:42 and REQ-77.

Code: `acme/widgets#5` `ABC-123` `src/main.go:42` `REQ-77`.

Markdown: [ABC-123](https://other.example.invalid/x) done.
TXT

node "$scratch/stub.mjs" "$scratch/reply.txt" >"$scratch/stub.out" 2>"$scratch/stub.err" &
stub_pid=$!
port=""
for _ in $(seq 1 50); do
  port="$(sed -n 's/^PORT //p' "$scratch/stub.out")"
  [[ -n $port ]] && break
  sleep 0.2
done
[[ -n $port ]] || {
  echo "FAIL: the stub did not start" >&2
  cat "$scratch/stub.err" >&2
  exit 1
}

key="sk-ant-pgd-e2e-stub-key-0001"
cat >"$scratch/cfg/.claude.json" <<JSON
{"hasCompletedOnboarding":true,"theme":"dark","numStartups":5,
 "customApiKeyResponses":{"approved":["${key: -20}"],"rejected":[]},
 "projects":{"$scratch/work":{"hasTrustDialogAccepted":true,"allowedTools":[]}}}
JSON
echo '{}' >"$scratch/cfg/settings.json"

options='{"enable":true,"components":{"beads":false,"assume":false,"sessionMode":false,"prStatus":false,"sessionName":false,"links":true,"modelEffort":false,"focus":false},"links":[{"kind":"github-pr","name":"pr"},{"kind":"jira","name":"jira","options":{"baseUrl":"https://jira.example.invalid","keys":["ABC"]}},{"kind":"file-ref","name":"file"},{"name":"req","pattern":"\\bREQ-\\d+\\b","url":"https://tracker.example.invalid/{0}"}],"github":{"host":"github.com","repos":["acme/widgets"]}}'

tmux -L "$sess" new-session -d -s "$sess" -x 200 -y 50 -c "$scratch/work" \
  env -i "PATH=$PATH" "HOME=$scratch/home" "CLAUDE_CONFIG_DIR=$scratch/cfg" \
  "ANTHROPIC_BASE_URL=http://127.0.0.1:$port" "ANTHROPIC_API_KEY=$key" \
  TERM=xterm-256color FORCE_HYPERLINK=1 CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1 \
  CLAUDE_CODE_DISABLE_OFFICIAL_MARKETPLACE_AUTOINSTALL=1 DISABLE_AUTOUPDATER=1 DISABLE_TELEMETRY=1 "PG_CLAUDE_DISPLAY_OPTIONS=$options" \
  "$claude_bin" --plugin-dir "$plugin_dir" || {
  echo "FAIL: tmux did not start" >&2
  exit 1
}

capture() { tmux -L "$sess" capture-pane -p -e -J -t "$sess" 2>/dev/null; }
capture_plain() { tmux -L "$sess" capture-pane -p -J -t "$sess" 2>/dev/null; }

# Wait for the prompt box, submit one prompt, wait for the reply to be drawn.
ready=""
for _ in $(seq 1 100); do
  if capture_plain | grep -aq '^❯'; then
    ready=1
    break
  fi
  sleep 0.3
done
[[ -n $ready ]] || {
  echo "FAIL: no prompt box" >&2
  capture >&2
  exit 1
}
sleep 1
tmux -L "$sess" send-keys -t "$sess" 'go' Enter
done_=""
for _ in $(seq 1 100); do
  if capture_plain | grep -aq 'Markdown:.*done'; then
    done_=1
    break
  fi
  sleep 0.3
done
pane="$(capture)"
[[ -n $done_ ]] || {
  echo "FAIL: the reply never appeared" >&2
  printf '%s\n' "$pane" | cat -v >&2
  exit 1
}

esc=$'\033'
# every OSC 8 opener with its text, as "url => text"
# only the reply itself (the footer holds a hyperlink of its own)
reply="$(printf '%s\n' "$pane" | awk '/Matched:/ { on = 1 } on { print } /Markdown:/ { on = 0 }')"
found="$(printf '%s\n' "$reply" | grep -aoE "${esc}\\]8;id=[^;]+;[^${esc}]+${esc}\\\\[^${esc}]+" |
  sed -E "s/^${esc}\\]8;id=[^;]+;//; s/${esc}\\\\/ => /")"
expected="https://github.com/acme/widgets/pull/5 => acme/widgets#5
https://jira.example.invalid/ABC-123 => ABC-123
file://$scratch/work/src/main.go:42 => src/main.go:42
https://tracker.example.invalid/REQ-77 => REQ-77
https://other.example.invalid/x => ABC-123"

rc=0
if [[ $found == "$expected" ]]; then
  echo "PASS: the pane holds exactly the five expected hyperlinks (github-pr, jira, file-ref, regex, the author's markdown link)"
else
  echo "FAIL: hyperlinks differ" >&2
  echo "--- expected" >&2
  printf '%s\n' "$expected" >&2
  echo "--- found" >&2
  printf '%s\n' "$found" >&2
  rc=1
fi
code_line="$(printf '%s\n' "$reply" | grep -a 'Code:')"
if [[ -z $code_line ]]; then
  echo "FAIL: the code-span line is not on the pane" >&2
  rc=1
elif [[ $code_line == *"${esc}]8;"* ]]; then
  echo "FAIL: a code span carries a hyperlink" >&2
  printf '%s\n' "$code_line" | cat -v >&2
  rc=1
else
  echo "PASS: the code-span forms carry no hyperlink"
fi
exit "$rc"
