// pools.go: enumerates every ccpool pool this probe must scan (bead
// pg2-bkzrc). pg-router-ccpool-handler can dedicate a ccpool pool PER ROLE
// (bead pg2-mr0sl: e.g. ~/.local/state/pg-router-ccpool-worker,
// ~/.local/state/pg-router-ccpool-review), and a probe that only asks the
// ambient pool is blind to sessions stuck in any of the others (the
// 2026-09-29 failure-rate incident, pg2-68005).
//
// Discovery source is ccpool's own permanent pool registry: a directory of
// symlinks, one per pool, each pointing at the pool's canonical directory
// (packages/ccpool/internal/registry; ccpool registers a pool there the
// moment any command resolves it, internal/config/pool.go). This probe
// reads that directory READ-ONLY with the standard library — never a
// compile-time import of packages/ccpool, same as ccpoolexec.go — and
// never creates it (unlike ccpool's own registry.Dir).
package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ambientPoolLabel is the label the ambient pool (whatever CCPOOL_POOL this
// process inherited, or ccpool's default XDG pool when unset) carries in
// finding text. Its fingerprints stay in the legacy, pool-less format so
// beads filed before pg2-bkzrc keep deduplicating.
const ambientPoolLabel = "default"

// poolRef is one ccpool pool to scan.
type poolRef struct {
	// Label names the pool in findings and fingerprints: the pool
	// directory's basename for a registered pool, ambientPoolLabel for the
	// ambient one.
	Label string
	// Dir is the pool's canonical directory, exported as CCPOOL_POOL on the
	// `ccpool list` child. "" means the ambient pool (the child inherits
	// this process's environment unmodified).
	Dir string
}

// fingerprintScope is the pool component of a finding fingerprint: "" for
// the ambient pool (legacy format), the pool label otherwise.
func (p poolRef) fingerprintScope() string {
	if p.Dir == "" {
		return ""
	}
	return p.Label
}

// registryDir resolves the ccpool pool-registry directory using the same
// rule as packages/ccpool/internal/registry.Dir (CCPOOL_REGISTRY_DIR, else
// $XDG_STATE_HOME/ccpool/pools.d, else ~/.local/state/ccpool/pools.d),
// WITHOUT creating it. An explicit override (the --registry-dir flag) wins.
func registryDir(override string) string {
	if override != "" {
		return override
	}
	if d := os.Getenv("CCPOOL_REGISTRY_DIR"); d != "" {
		return d
	}
	state := os.Getenv("XDG_STATE_HOME")
	if state == "" {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return ""
		}
		state = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(state, "ccpool", "pools.d")
}

// discoverPools returns the ambient pool first, then every registered pool
// whose target directory still exists, de-duplicated by directory and
// skipping the one the ambient pool already is (when CCPOOL_POOL points at
// a registered pool). Registry entries are returned sorted by label so a
// run is deterministic. A missing or unreadable registry is NOT an error:
// the ambient pool is still scanned (the pre-pg2-bkzrc behavior); the
// problem is surfaced through warn. Stale links (target gone) are skipped
// with a warning — ccpool's own reap-all GCs them.
func discoverPools(regOverride string, warn func(string)) []poolRef {
	pools := []poolRef{{Label: ambientPoolLabel}}

	seen := map[string]bool{}
	if amb := os.Getenv("CCPOOL_POOL"); amb != "" {
		seen[canonicalDir(amb)] = true
	}

	dir := registryDir(regOverride)
	if dir == "" {
		warn("cannot resolve the ccpool pool registry directory; scanning only the ambient pool")
		return pools
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !os.IsNotExist(err) {
			warn("cannot read ccpool pool registry " + dir + ": " + err.Error() + "; scanning only the ambient pool")
		}
		return pools
	}

	var registered []poolRef
	for _, e := range entries {
		// Temp links left by an interrupted registry.Ensure carry ".tmp-".
		if strings.Contains(e.Name(), ".tmp-") {
			continue
		}
		target, err := os.Readlink(filepath.Join(dir, e.Name()))
		if err != nil {
			continue // not a symlink
		}
		if info, err := os.Stat(target); err != nil || !info.IsDir() {
			warn("skipping stale ccpool registry entry " + e.Name() + " -> " + target)
			continue
		}
		canon := canonicalDir(target)
		if seen[canon] {
			continue
		}
		seen[canon] = true
		registered = append(registered, poolRef{Label: filepath.Base(canon), Dir: canon})
	}
	sort.Slice(registered, func(i, j int) bool { return registered[i].Label < registered[j].Label })
	return append(pools, registered...)
}

// canonicalDir resolves symlinks and cleans p, falling back to the cleaned
// path when it cannot be resolved.
func canonicalDir(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	return filepath.Clean(p)
}
