#!/usr/bin/env bats
# bats file_tags=type:unit
#
# Wiring tests for pg-keep-awake's user-facing surface: --help on the command
# and every subcommand, the bash and zsh completions, the tldr page, and the
# package's install of those files. The symlink and PATH wiring of the
# caffeinate shim is a separate suite (the home-manager wiring).
#
# Binary under test, in order: $PG_KEEP_AWAKE (an explicit path), then
# $PKG_OUT/bin/pg-keep-awake (a nix build output, for example ./result), then a
# `go build` of this package into the file's temp dir (a local `bats tests/`).
# $PKG_OUT also enables the check that the build output installs the shell
# completions and the tldr page.

bats_require_minimum_version 1.5.0

PKG_DIR="$(cd "$(dirname "${BATS_TEST_FILENAME}")/.." && pwd)"

# Every command the CLI exposes, as the words typed before --help.
COMMANDS=("acquire" "release" "run" "status" "pause" "resume" "config check" "keeper" "anchor")

setup_file() {
  if [[ -n ${PG_KEEP_AWAKE:-} ]]; then
    BIN="$PG_KEEP_AWAKE"
  elif [[ -n ${PKG_OUT:-} ]]; then
    BIN="$PKG_OUT/bin/pg-keep-awake"
  else
    command -v go >/dev/null 2>&1 || skip "no PG_KEEP_AWAKE or PKG_OUT, and go is not on PATH"
    BIN="$BATS_FILE_TMPDIR/pg-keep-awake"
    (cd "$PKG_DIR" && go build -o "$BIN" ./cmd/pg-keep-awake)
  fi
  export BIN
}

setup() {
  # The commands under test must never touch the real state or config.
  export HOME="$BATS_TEST_TMPDIR/home"
  export XDG_STATE_HOME="$BATS_TEST_TMPDIR/state"
  export XDG_CONFIG_HOME="$BATS_TEST_TMPDIR/config"
  mkdir -p "$HOME"
}

# --- --help --------------------------------------------------------------

@test "--help prints usage on stdout, nothing on stderr, and exits 0" {
  run --separate-stderr "$BIN" --help
  [ "$status" -eq 0 ]
  [[ $output == *"usage: pg-keep-awake"* ]]
  [ -z "$stderr" ]
}

@test "top-level --help lists every command" {
  run "$BIN" --help
  [ "$status" -eq 0 ]
  local c
  for c in "${COMMANDS[@]}"; do
    [[ $output == *"$c"* ]] || {
      echo "top-level help does not mention '$c'"
      return 1
    }
  done
}

@test "every subcommand's --help prints usage on stdout and exits 0" {
  local c
  for c in "${COMMANDS[@]}"; do
    # shellcheck disable=SC2086 # $c is one or two words by design
    run --separate-stderr "$BIN" $c --help
    [ "$status" -eq 0 ] || {
      echo "'$c --help' exited $status"
      return 1
    }
    [[ $output == *"usage: pg-keep-awake"* ]] || {
      echo "'$c --help' printed no usage line: $output"
      return 1
    }
    [ -z "$stderr" ] || {
      echo "'$c --help' wrote to stderr: $stderr"
      return 1
    }
  done
}

@test "-h works like --help" {
  run "$BIN" acquire -h
  [ "$status" -eq 0 ]
  [[ $output == *"usage: pg-keep-awake acquire"* ]]
}

@test "help <command> is the same as <command> --help" {
  run "$BIN" run --help
  [ "$status" -eq 0 ]
  local expected="$output"
  run "$BIN" help run
  [ "$status" -eq 0 ]
  [ "$output" = "$expected" ]
}

@test "keeper and anchor are labelled as long-running modes not for interactive use" {
  local c
  for c in keeper anchor; do
    run "$BIN" "$c" --help
    [ "$status" -eq 0 ]
    [[ $output == *"not for interactive use"* ]] || {
      echo "'$c --help' does not say it is not for interactive use"
      return 1
    }
  done
  run "$BIN" --help
  [[ $output == *"not for interactive use"* ]]
}

@test "a usage mistake still exits 2 with the usage line on stderr" {
  run --separate-stderr "$BIN" acquire --bogus
  [ "$status" -eq 2 ]
  [ -z "$output" ]
  [[ $stderr == *"usage: pg-keep-awake acquire"* ]]
}

# --- completions ----------------------------------------------------------

@test "bash completion is valid and offers every client and operator command" {
  local file="$PKG_DIR/completions/pg-keep-awake.bash"
  [ -f "$file" ]
  bash -n "$file"
  run bash -c '
    source "$1"
    complete -p pg-keep-awake | grep -q -- "-F _pg_keep_awake" || { echo "not registered"; exit 1; }
    COMP_WORDS=(pg-keep-awake ""); COMP_CWORD=1; COMPREPLY=()
    _pg_keep_awake
    printf "%s\n" "${COMPREPLY[@]}"
  ' _ "$file"
  [ "$status" -eq 0 ]
  local c
  for c in acquire release run status pause resume config; do
    [[ $output == *"$c"* ]] || {
      echo "bash completion does not offer '$c'"
      return 1
    }
  done
}

@test "bash completion completes flags per command" {
  local file="$PKG_DIR/completions/pg-keep-awake.bash"
  run bash -c '
    source "$1"
    COMP_WORDS=(pg-keep-awake config check --c); COMP_CWORD=3; COMPREPLY=()
    _pg_keep_awake
    printf "%s\n" "${COMPREPLY[@]}"
  ' _ "$file"
  [ "$status" -eq 0 ]
  [ "$output" = "--config" ]
  run bash -c '
    source "$1"
    COMP_WORDS=(pg-keep-awake acquire --o); COMP_CWORD=2; COMPREPLY=()
    _pg_keep_awake
    printf "%s\n" "${COMPREPLY[@]}"
  ' _ "$file"
  [ "$output" = "--owner" ]
}

@test "zsh completion is valid and names every client and operator command" {
  local file="$PKG_DIR/completions/_pg-keep-awake"
  [ -f "$file" ]
  [ "$(head -n 1 "$file")" = "#compdef pg-keep-awake" ]
  if command -v zsh >/dev/null 2>&1; then
    zsh -n "$file"
  fi
  local c
  for c in acquire release run status pause resume config; do
    grep -q "'$c:" "$file" || {
      echo "zsh completion does not describe '$c'"
      return 1
    }
  done
}

# --- tldr page ------------------------------------------------------------

@test "the tldr page exists, is titled and has an example per client command" {
  local page="$PKG_DIR/share/tldr/pages.common/pg-keep-awake.md"
  [ -f "$page" ]
  [ "$(head -n 1 "$page")" = "# pg-keep-awake" ]
  local c
  for c in acquire release run status pause resume "config check"; do
    grep -q "^\`pg-keep-awake $c" "$page" || {
      echo "tldr page has no example for '$c'"
      return 1
    }
  done
}

@test "every command in a tldr example is a real command" {
  local page="$PKG_DIR/share/tldr/pages.common/pg-keep-awake.md"
  local line sub
  while IFS= read -r line; do
    sub="$(sed -E 's/^`pg-keep-awake ([a-z]+( check)?).*/\1/' <<<"$line")"
    # shellcheck disable=SC2086 # $sub is one or two words by design
    "$BIN" $sub --help >/dev/null 2>&1 || {
      echo "tldr example '$line' names '$sub', which has no --help"
      return 1
    }
  done < <(grep '^`pg-keep-awake ' "$page")
}

@test "the tldr page does not present the state files as an interface" {
  local page="$PKG_DIR/share/tldr/pages.common/pg-keep-awake.md"
  ! grep -Eiq 'leases/|\.json|state file' "$page" ||
    grep -iq 'private' "$page"
}

# --- packaging ------------------------------------------------------------

@test "default.nix installs the completions and the tldr page from the package" {
  local nix="$PKG_DIR/default.nix"
  grep -q 'completions/pg-keep-awake.bash' "$nix"
  grep -q 'share/bash-completion/completions/pg-keep-awake' "$nix"
  grep -q 'completions/_pg-keep-awake' "$nix"
  grep -q 'share/zsh/site-functions/_pg-keep-awake' "$nix"
  grep -q 'share/tldr/pages.common/pg-keep-awake.md' "$nix"
}

@test "the package output contains the completions and the tldr page" {
  [[ -n ${PKG_OUT:-} ]] || skip "PKG_OUT (a nix build output) is not set"
  [ -f "$PKG_OUT/share/bash-completion/completions/pg-keep-awake" ]
  [ -f "$PKG_OUT/share/zsh/site-functions/_pg-keep-awake" ]
  [ -f "$PKG_OUT/share/tldr/pages.common/pg-keep-awake.md" ]
}

@test "the README documents the CLI as the only interface and the state as private" {
  local readme="$PKG_DIR/README.md"
  [ -f "$readme" ]
  grep -qi 'only supported interface' "$readme"
  grep -qi 'private' "$readme"
}
