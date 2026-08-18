package setup

import (
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

// PURITY / CORRECTNESS: the whole point of EngineCache is that a memoized
// engine is a legitimate stand-in for a freshly-built one, for the lifetime of
// one replay run. These tests pin that: for the SAME cwd (and the SAME
// rules.json on disk), the cached path and the always-fresh
// NewEngineForCWD path MUST produce byte-identical verdicts (Decision,
// Module, Reason) for a representative sample of commands, across a ZR-style
// config, a command-aware-blocks config, and no config at all — the same
// three fixtures factory_test.go already exercises end-to-end.

// representativeCommands is shared by all three fixture scenarios below,
// deliberately spanning every decision the engine can reach (Approve, Reject,
// Ask, NoOpinion/abstain) so a cache bug that corrupts only one branch cannot
// hide.
var representativeCommands = []string{
	"ls -la",
	"unknown-cmd-xyz",
	"cat .env",
	"cat ../.ssh/id_rsa",
	"kubectl get pods",
	"gradle build",
	"ssh host ls -la",
	"ssh root@host ls",
	"vault read secret/foo",
	"vault write secret/foo x=1",
	"curl https://api.internal.example/health",
	"tc build",
	"bin/kc wslogs -n mp--ui--customer",
	"prove -v t/foo.t",
}

// assertCacheAgreesWithUncached builds an engine via a fresh EngineCache and
// an engine via the always-fresh NewEngineForCWD, for the same cwd, and
// asserts every command in representativeCommands replays identically
// through both.
func assertCacheAgreesWithUncached(t *testing.T, cwd string) {
	t.Helper()

	cache := NewEngineCache()
	cachedEng, stale := cache.EngineForCWD(cwd)
	if stale {
		t.Fatalf("cwd %q unexpectedly reported stale", cwd)
	}
	freshEng := NewEngineForCWD(cwd)

	for _, cmd := range representativeCommands {
		input := bashHook(cwd, cmd)
		gotCached := cachedEng.EvaluateHook(input)
		gotFresh := freshEng.EvaluateHook(input)

		if gotCached.Decision != gotFresh.Decision || gotCached.Module != gotFresh.Module || gotCached.Reason != gotFresh.Reason {
			t.Errorf("cmd %q: cached=(%s %q %q) fresh=(%s %q %q) — cache diverged from uncached construction",
				cmd, gotCached.Decision, gotCached.Module, gotCached.Reason,
				gotFresh.Decision, gotFresh.Module, gotFresh.Reason)
		}
	}
}

func TestEngineCache_AgreesWithUncached_ZRConfig(t *testing.T) {
	t.Setenv("WORKSPACE_ROOT", "/Users/testuser/workspace")
	withXDGConfig(t, zrFixture)
	assertCacheAgreesWithUncached(t, t.TempDir())
}

func TestEngineCache_AgreesWithUncached_CommandBlocksConfig(t *testing.T) {
	withXDGConfig(t, commandBlocksFixture)
	assertCacheAgreesWithUncached(t, t.TempDir())
}

func TestEngineCache_AgreesWithUncached_NoConfig(t *testing.T) {
	withXDGConfig(t, "")
	assertCacheAgreesWithUncached(t, t.TempDir())
}

// TestEngineCache_SameCWDReturnsSameEngine confirms the memoization itself:
// a second EngineForCWD call for a CWD already seen returns the identical
// *engine.Engine value rather than building a new one.
func TestEngineCache_SameCWDReturnsSameEngine(t *testing.T) {
	withXDGConfig(t, "")
	cwd := t.TempDir()
	cache := NewEngineCache()

	first, stale := cache.EngineForCWD(cwd)
	if stale {
		t.Fatalf("cwd %q unexpectedly reported stale", cwd)
	}
	second, stale := cache.EngineForCWD(cwd)
	if stale {
		t.Fatalf("cwd %q unexpectedly reported stale on second call", cwd)
	}
	if first != second {
		t.Errorf("EngineForCWD(%q) built a second engine instead of reusing the cached one", cwd)
	}
}

// TestEngineCache_DistinctCWDsGetDistinctEngines confirms the cache is keyed
// by CWD, not global: two different (real) CWDs must not collapse to the same
// engine instance.
func TestEngineCache_DistinctCWDsGetDistinctEngines(t *testing.T) {
	withXDGConfig(t, "")
	cache := NewEngineCache()

	a, staleA := cache.EngineForCWD(t.TempDir())
	b, staleB := cache.EngineForCWD(t.TempDir())
	if staleA || staleB {
		t.Fatalf("unexpected stale: a=%v b=%v", staleA, staleB)
	}
	if a == b {
		t.Error("two distinct CWDs produced the same cached engine instance")
	}
}

// TestEngineCache_StaleCWD_MemoizedNilEngine pins the stale-cwd behavior the
// replay loops depend on: a CWD that does not exist on disk reports stale on
// every call (not just the first), and no engine is ever built for it.
func TestEngineCache_StaleCWD_MemoizedNilEngine(t *testing.T) {
	withXDGConfig(t, "")
	cache := NewEngineCache()
	missing := t.TempDir() + "/does-not-exist"

	for i := 0; i < 2; i++ {
		eng, stale := cache.EngineForCWD(missing)
		if !stale {
			t.Fatalf("call %d: stale-cwd %q reported stale=false", i, missing)
		}
		if eng != nil {
			t.Fatalf("call %d: stale-cwd %q returned a non-nil engine", i, missing)
		}
	}
}

// TestEngineCache_RulesJSONLoadedOnce pins the SEPARATE half of the
// memoization: rules.json is parsed AT MOST ONCE per cache, not once per CWD.
// It edits the on-disk rules.json AFTER the cache has already loaded it for
// one CWD, then asks for a second, different CWD — if the cache reloaded the
// config per CWD, the second CWD's engine would observe the edit; because it
// does not, EngineForCWD is proven to reuse the first load.
func TestEngineCache_RulesJSONLoadedOnce(t *testing.T) {
	withXDGConfig(t, "") // starts with NO config: nothing is approved by config-rules
	cache := NewEngineCache()

	cwdA := t.TempDir()
	engA, staleA := cache.EngineForCWD(cwdA)
	if staleA {
		t.Fatalf("cwd %q unexpectedly reported stale", cwdA)
	}
	// With no config, an unconfigured build tool is not approved.
	if got := engA.EvaluateHook(bashHook(cwdA, "tc build")).Decision; got == hookio.Approve {
		t.Fatalf("cwd %q: %q approved before any config was ever loaded", cwdA, "tc build")
	}

	// Now write a rules.json that WOULD approve it, into the SAME
	// XDG_CONFIG_HOME withXDGConfig already pointed at. If the cache reloads
	// rules.json per CWD, cwdB below will see this and approve; the pinned
	// expectation is that it does NOT, because the config was already loaded
	// (once) on the very first EngineForCWD call above.
	withXDGConfig(t, commandBlocksFixture)

	cwdB := t.TempDir()
	engB, staleB := cache.EngineForCWD(cwdB)
	if staleB {
		t.Fatalf("cwd %q unexpectedly reported stale", cwdB)
	}
	if got := engB.EvaluateHook(bashHook(cwdB, "tc build")).Decision; got == hookio.Approve {
		t.Fatalf("cwd %q: %q approved after rules.json was rewritten mid-cache-lifetime; config was reloaded, not memoized once", cwdB, "tc build")
	}

	// Sanity check the fixture actually WOULD flip the verdict: a brand-new,
	// uncached engine built after the rewrite must approve it — proving the
	// difference above is the cache's memoization, not an inert fixture.
	cwdC := t.TempDir()
	freshEng := NewEngineForCWD(cwdC)
	if got := freshEng.EvaluateHook(bashHook(cwdC, "tc build")).Decision; got != hookio.Approve {
		t.Fatalf("control: a fresh (uncached) engine built after the rewrite did not approve %q; the fixture does not actually change this verdict, so this test proves nothing", "tc build")
	}
}
