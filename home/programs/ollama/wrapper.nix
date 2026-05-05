{
  pkgs,
  lib,
  ollamaPackage,
}:
pkgs.writeShellApplication {
  name = "pgii-ollama-server";
  runtimeInputs = [ ];
  text = ''
    set -euo pipefail

    OLLAMA_BIN="''${OLLAMA_BIN:-${lib.getExe ollamaPackage}}"

    "$OLLAMA_BIN" serve &
    serve_pid=$!

    # Wait up to 60s for the server to answer `list`.
    for _ in $(seq 1 120); do
      if "$OLLAMA_BIN" list >/dev/null 2>&1; then
        break
      fi
      sleep 0.5
    done

    # Snapshot present models once; skip the header row (NR>1).
    # `ollama list` emits: NAME ID SIZE MODIFIED — column 1 is NAME:TAG.
    present="$("$OLLAMA_BIN" list | awk 'NR>1 {print $1}')"

    # Pull each requested model exactly once if missing.
    # `ollama pull` is resumable on the daemon side, so launchd restarts
    # mid-pull are safe — the next run re-issues pull and continues.
    for model in "$@"; do
      if printf '%s\n' "$present" | grep -Fxq "$model"; then
        echo "ollama: $model already present"
      else
        echo "ollama: pulling $model"
        "$OLLAMA_BIN" pull "$model"
      fi
    done

    wait "$serve_pid"
  '';
}
