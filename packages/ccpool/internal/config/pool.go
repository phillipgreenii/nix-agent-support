// pool.go: resolve the active pool (CCPOOL_POOL) into paths.
package config

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/phillipgreenii/ccpool/internal/registry"
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
			StateDir:   filepath.Join(xdg("XDG_STATE_HOME", ".local/state"), "ccpool"),
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

// canonicalize resolves symlinks + makes absolute + cleans. EvalSymlinks errors on
// a non-existent leaf, so fall back to canonicalizing the PARENT and rejoining the
// leaf (the dir may not exist yet — it is created by ensurePoolDir).
func canonicalize(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return filepath.Clean(p)
	}
	abs = filepath.Clean(abs)
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved
	}
	parent, leaf := filepath.Split(abs)
	if rp, err := filepath.EvalSymlinks(filepath.Clean(parent)); err == nil {
		return filepath.Join(rp, leaf)
	}
	return abs
}

// poolFileOK reports whether a dir entry name is one ccpool writes.
func poolFileOK(name string) bool {
	switch {
	case name == "config.toml", name == "hook.log":
		return true
	case name == "store.db" || name == "store.db-wal" || name == "store.db-shm" || name == "store.db-journal":
		return true
	case strings.HasSuffix(name, ".lock"): // per-session <name>.lock
		return true
	}
	return false
}

// ValidatePoolDir checks an EXISTING dir's contents against the allowlist WITHOUT
// creating anything. A missing dir (ReadDir → ENOENT) is reported as an error, which
// is exactly what reap-all's GC wants: a dangling registry symlink or a dir gone
// foreign both fail this check and mark the link for removal.
func ValidatePoolDir(root string) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return fmt.Errorf("read pool dir %s: %w", root, err)
	}
	for _, e := range entries {
		if e.IsDir() || !poolFileOK(e.Name()) {
			return fmt.Errorf("not a ccpool pool dir: %s contains %s", root, e.Name())
		}
	}
	return nil
}

// ensurePoolDir validates an existing pool dir's contents against the allowlist, or
// creates a missing leaf (parent must exist; mode 0700; never mkdir -p). Creating a
// new leaf is the ONLY moment a pool enrolls in the reap-all registry: subsequent
// resolves take the validate branch and stay registry-side-effect-free, so read-only
// commands never enroll a pool in auto-reaping.
func ensurePoolDir(root string) error {
	if _, err := os.ReadDir(root); os.IsNotExist(err) {
		if _, perr := os.Stat(filepath.Dir(root)); perr != nil {
			return fmt.Errorf("pool parent does not exist: %s", filepath.Dir(root))
		}
		if err := os.Mkdir(root, 0o700); err != nil {
			return err
		}
		// Register on creation only. A registry failure must not break pool use
		// (the pool is created and usable); reap-all just won't sweep it until the
		// next create. Best-effort, mirroring the trust-write activation pattern.
		_ = registry.Ensure(SocketFor(root), root)
		return nil
	} else if err != nil {
		return fmt.Errorf("read pool dir %s: %w", root, err)
	}
	return ValidatePoolDir(root)
}
