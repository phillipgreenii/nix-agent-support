#!/usr/bin/env bats
#
# Verify pgii-ollama-server:
#   - calls `ollama serve` exactly once
#   - waits until `ollama list` succeeds before pulling
#   - pulls only models that are not already listed
#   - exits 0 when serve exits 0

bats_require_minimum_version 1.5.0

# Replace the shebang on $1 with one that uses an absolute bash path.
# Required for environments where /usr/bin/env doesn't exist (e.g. the
# Nix build sandbox, where only /nix/store paths are visible).
_fix_mock_shebang() {
  sed -i "1s|.*|#!$(command -v bash)|" "$1"
}

setup() {
  # Resolve the wrapper from PATH (testBashScripts puts package/bin on PATH)
  # before we shadow PATH with the mock ollama directory.
  WRAPPER_BIN="${WRAPPER_BIN:-$(command -v pgii-ollama-server)}"
  export WRAPPER_BIN

  TMP="$(mktemp -d)"
  export TMP
  # Force the wrapper to use the mock ollama, not its baked default.
  export OLLAMA_BIN="${TMP}/bin/ollama"
  export PATH="${TMP}/bin:${PATH}"

  mkdir -p "${TMP}/bin"
  : > "${TMP}/list-state"
  : > "${TMP}/calls.log"

  cat > "${TMP}/bin/ollama" <<'EOF'
#!/usr/bin/env bash
echo "$@" >> "${TMP}/calls.log"
case "$1" in
  serve)
    sleep 0.2
    : > "${TMP}/serve.ready"
    sleep 0.2
    exit 0
    ;;
  list)
    if [ ! -f "${TMP}/serve.ready" ]; then
      exit 1
    fi
    echo "NAME            ID              SIZE      MODIFIED"
    while IFS= read -r m; do
      [ -n "$m" ] && printf '%s\tabc123\t9.0 GB\t1 hour ago\n' "$m"
    done < "${TMP}/list-state"
    ;;
  pull)
    echo "$2" >> "${TMP}/list-state"
    ;;
esac
EOF
  _fix_mock_shebang "${TMP}/bin/ollama"
  chmod +x "${TMP}/bin/ollama"
}

teardown() {
  rm -rf "${TMP}"
}

@test "serves and pulls a single missing model" {
  run "${WRAPPER_BIN}" qwen2.5-coder:14b
  [ "$status" -eq 0 ]
  grep -q '^pull qwen2.5-coder:14b$' "${TMP}/calls.log"
  [ "$(grep -c '^serve$' "${TMP}/calls.log")" -eq 1 ]
}

@test "skips pull when model already present" {
  echo "qwen2.5-coder:14b" > "${TMP}/list-state"
  run "${WRAPPER_BIN}" qwen2.5-coder:14b
  [ "$status" -eq 0 ]
  run ! grep -q '^pull qwen2.5-coder:14b$' "${TMP}/calls.log"
}

@test "pulls every missing model, keeps already-present ones" {
  echo "qwen2.5-coder:14b" > "${TMP}/list-state"
  run "${WRAPPER_BIN}" qwen2.5-coder:14b qwen2.5-coder:32b
  [ "$status" -eq 0 ]
  run ! grep -q '^pull qwen2.5-coder:14b$' "${TMP}/calls.log"
  grep -q '^pull qwen2.5-coder:32b$' "${TMP}/calls.log"
}
