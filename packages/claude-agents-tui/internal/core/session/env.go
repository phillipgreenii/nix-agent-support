package session

// ReadProcessEnv returns the environment of pid as a map. Implementations
// live in per-OS files (env_linux.go, env_darwin.go, env_other.go). All
// implementations return an empty map (never nil) on failure. Empty
// values are dropped — callers only see populated KEY=VALUE pairs.
//
// Errors are returned for observability only; callers treat any error as
// "env unavailable" and use the empty map.
func ReadProcessEnv(pid int) (map[string]string, error) {
	return readProcessEnv(pid)
}
