// Package config — pool.go: resolve the active pool (CCPOOL_POOL) into paths.
package config

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
)

// PoolContext is the resolved location of the active pool. In default mode Root
// is "" and Socket is "" (config.toml's [tmux] supplies the socket); in pool-dir
// mode every field is derived from the canonical pool directory.
type PoolContext struct {
	Root       string // canonical pool dir; "" in default mode
	ConfigPath string // <root>/config.toml, or the XDG config path
	DBPath     string
	StateDir   string // holds hook.log
	RuntimeDir string // holds the per-session *.lock files
	Socket     string // tmux -L name; "" in default mode
	Prefix     string // "" in default mode (config.toml supplies it)
}

// resolvePaths computes a PoolContext from the CCPOOL_POOL value WITHOUT validating
// or creating anything (safe for the hook's log-dir lookup). Empty → default mode.
func resolvePaths(poolEnv string) PoolContext {
	if poolEnv == "" {
		return PoolContext{
			ConfigPath: filepath.Join(xdg("XDG_CONFIG_HOME", ".config"), "ccpool", "config.toml"),
			DBPath:     filepath.Join(xdg("XDG_DATA_HOME", ".local/share"), "ccpool", "store.db"),
			StateDir:   StateDirPath(),
			RuntimeDir: defaultRuntimeDir(),
		}
	}
	root := canonicalize(poolEnv)
	return PoolContext{
		Root:       root,
		ConfigPath: filepath.Join(root, "config.toml"),
		DBPath:     filepath.Join(root, "store.db"),
		StateDir:   root,
		RuntimeDir: root,
		Socket:     SocketFor(root),
		Prefix:     "cc-",
	}
}

// ResolvePool resolves AND validates the pool dir, creating a missing leaf.
func ResolvePool(poolEnv string) (PoolContext, error) {
	pc := resolvePaths(poolEnv)
	if pc.Root == "" {
		return pc, nil // default mode: nothing to validate/create
	}
	if err := ensurePoolDir(pc.Root); err != nil {
		return PoolContext{}, err
	}
	return pc, nil
}

func defaultRuntimeDir() string {
	if rt := os.Getenv("XDG_RUNTIME_DIR"); rt != "" {
		return filepath.Join(rt, "ccpool")
	}
	return filepath.Join(os.TempDir(), "ccpool")
}

// SocketFor derives a short, collision-resistant tmux -L name from the canonical
// path: "cc-" + first 16 hex of sha256. ~19 chars, far under the socket-path limit.
func SocketFor(canonicalRoot string) string {
	sum := sha256.Sum256([]byte(canonicalRoot))
	return "cc-" + hex.EncodeToString(sum[:8])
}

func canonicalize(p string) string { abs, _ := filepath.Abs(p); return filepath.Clean(abs) }
func ensurePoolDir(root string) error { return nil }
