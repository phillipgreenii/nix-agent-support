package setup

import (
	"os"
	"sync"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/engine"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/rules/configrules"
)

// EngineCache memoizes engine construction by CWD, for the OFFLINE REPLAY code
// paths only: the `evaluate`, `baseline`, and `compare` subcommands in
// cmd/claude-extended-tool-approver, which each replay every row of the ask
// log through a freshly-built engine. Measured (pg2-rszk3, 2026-08-14):
// 351,719 rows against only 1,240 distinct CWD values, so building one engine
// per row rebuilds the SAME engine up to tens of thousands of times.
//
// SCOPE BOUNDARY — deliberately NOT used by hook mode. The live PreToolUse
// handler (cmd/claude-extended-tool-approver/main.go, handlePreToolUse) calls
// NewEngineForCWD / NewEngineForCWDWithShellStore directly and is UNCHANGED by
// this cache. That handler runs inside `main()`, which parses exactly one
// hookio.HookInput from stdin and exits — one process per real tool-use
// invocation — so it already constructs its engine exactly once per process
// and a cache would give it nothing. Adding a cross-call cache there would
// instead add a NEW risk with a different profile: an engine held across
// calls could outlive an edit to rules.json and serve a stale verdict. Since
// hook mode has no repeat calls to amortize across, that risk is taken on for
// zero benefit, so EngineCache is exposed only for replay callers to opt into
// and main.go does not reference it.
//
// rules.json is NOT CWD-dependent: configrules.Load reads
// $XDG_CONFIG_HOME/claude-extended-tool-approver/rules.json, which cannot
// change mid-run for a single replay process. So it is parsed AT MOST ONCE per
// EngineCache, not once per CWD (let alone once per row). The resulting
// *configrules.Config, and the per-rule sub-configs read out of it
// (KubectlConfig/BuildtoolsConfig/SshConfig/VaultConfig/CurlConfig/
// MonorepoConfig), are documented in configrules.go as DATA ONLY, and every
// rule constructor that consumes one (curl.New, sshrule.New, vaultrule.New,
// kubectl.New, buildtools.New, monorepo.New) takes it BY VALUE and copies what
// it needs into its own lookup structures rather than retaining or mutating
// the shared *Config — so sharing one loaded Config across every cached
// engine is safe.
type EngineCache struct {
	rulesOnce sync.Once
	rulesCfg  *configrules.Config

	byCWD map[string]*cachedCWD
}

// cachedCWD is the memoized outcome for one CWD: either the built engine, or
// (when the CWD does not exist on disk) staleness, so the replay loops' own
// per-row os.Stat "stale-cwd" check is amortized by the same cache instead of
// re-run for every row of a CWD already seen.
type cachedCWD struct {
	engine *engine.Engine // nil when stale
	stale  bool
}

// NewEngineCache constructs an empty cache, scoped to a single replay run.
// It is intended for the sequential, single-goroutine use the evaluate/
// baseline/compare row loops make of it and is NOT safe for concurrent use
// from multiple goroutines.
func NewEngineCache() *EngineCache {
	return &EngineCache{byCWD: make(map[string]*cachedCWD)}
}

// config returns the cache's memoized *configrules.Config, loading it from
// disk on the first call and reusing that value for the lifetime of the
// cache.
func (c *EngineCache) config() *configrules.Config {
	c.rulesOnce.Do(func() {
		c.rulesCfg = configrules.Load(configrules.DefaultPath())
	})
	return c.rulesCfg
}

// EngineForCWD returns the memoized engine for cwd, building (and caching) it
// on first use. stale reports whether cwd does not exist on disk, matching
// the `os.IsNotExist` check the replay loops previously ran per row: when
// stale is true, eng is nil and no engine was built or is built for that CWD,
// exactly as the uncached code skipped construction entirely for a stale row.
func (c *EngineCache) EngineForCWD(cwd string) (eng *engine.Engine, stale bool) {
	if entry, ok := c.byCWD[cwd]; ok {
		return entry.engine, entry.stale
	}

	_, statErr := os.Stat(cwd)
	stale = os.IsNotExist(statErr)

	var built *engine.Engine
	if !stale {
		built = newEngineForCWDWithConfig(cwd, nil, c.config())
	}
	c.byCWD[cwd] = &cachedCWD{engine: built, stale: stale}
	return built, stale
}
