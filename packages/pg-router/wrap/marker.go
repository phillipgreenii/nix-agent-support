package wrap

import "os"

// ReadyMarkerEnv names the environment variable that holds the ready-marker path.
const ReadyMarkerEnv = "PG_WRAP_READY_MARKER"

// markerScript fails (before the payload starts) when the marker variable is
// unset or empty or the marker cannot be created; otherwise it creates the
// marker empty and execs the payload. It writes only to stderr (WC-3).
const markerScript = `if [ -z "${PG_WRAP_READY_MARKER:-}" ]; then
  echo "pg-wrap[marker]: PG_WRAP_READY_MARKER is unset or empty" >&2
  exit 97
fi
if ! : > "$PG_WRAP_READY_MARKER"; then
  echo "pg-wrap[marker]: cannot create ready marker $PG_WRAP_READY_MARKER" >&2
  exit 97
fi
exec "$@"`

// MarkerStage returns the ready-marker stage as an array entry
// ["/bin/sh", "-c", <script>, "sh"]. The adapter appends it as the LAST entry
// before Compose, even for an empty wrap list, so a failure before the stage
// runs is told apart from the payload's own failure.
func MarkerStage() Entry {
	return Entry{Argv: []string{"/bin/sh", "-c", markerScript, "sh"}}
}

// MarkerExists reports whether the marker file exists. No marker means the wrap
// chain failed before the payload started.
func MarkerExists(path string) bool {
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}
