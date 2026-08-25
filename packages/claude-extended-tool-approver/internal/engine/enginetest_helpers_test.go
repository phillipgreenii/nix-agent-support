// Shared full-chain test helpers, deliberately carrying NO build tag.
//
// buildFullEngine/buildFullEngineWithConfig/makeBashJSON/makeFileJSON started
// life inside engine_integration_test.go, but several sibling `engine_test`
// files (declare_incommandvars_test.go, jq_flag_operands_test.go, and others)
// also call buildFullEngine/makeBashJSON to drive the real composed rule
// chain for a single focused case. engine_integration_test.go itself became
// the curated `TestIntegration_*` regression suite and picked up
// `//go:build integration` (bead pg2-h05lt) so it stops running in the unit
// tier; these three helpers had to move here first; otherwise every untagged
// sibling that calls them would fail to compile once the integration tag is
// absent (the default `go test ./...` build).
package engine_test

import (
	"encoding/json"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/engine"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/patheval"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/rules/configrules"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/setup"
)

// zrFixture is the ZR consumer config fixture, injected into the kubectl/
// build-tools rules so the kc/prove integration cases exercise real ZR behavior —
// fully config-driven (ADR 0033). It mirrors the ZR machine config's inline
// rules.json block. It carries NO ssh/vault/curl/monorepo blocks, so those rules
// sit at their safe base default (Abstain) in this engine; the
// command-blocks fixture (engine_integration_test.go) supplies data for them.
const zrFixture = "../rules/configrules/testdata/consumer-rules.json"

// buildFullEngine assembles the FULL production rule chain over a synthetic
// project root.
//
// The rule list is DERIVED from setup.RuleChain — the exact function
// setup.newEngineForCWD uses — so a rule added to production is automatically
// present, in production's position, in every case that uses it. Only the
// leaves that must be synthetic for a hermetic test are substituted: the path
// evaluator is rooted at an in-memory projectRoot/cwd instead of
// patheval.DetectProjectRoot, and the consumer config comes from a fixture
// instead of $XDG_CONFIG_HOME. Nothing about WHICH rules run, or in WHAT ORDER,
// is restated here.
//
// One consequence of deriving rather than restating: the gh and primary-commit
// rules get production's REAL resolvers, which shell out to git/gh. That stays
// hermetic only as long as no case reaches a resolver-dependent branch
// (`gh run rerun`, or a `git commit` in bypassPermissions mode). A new case that
// does must build its own engine with a stub resolver rather than relax this one.
func buildFullEngine(projectRoot, cwd string) *engine.Engine {
	return buildFullEngineWithConfig(projectRoot, cwd, zrFixture)
}

// buildFullEngineWithConfig is buildFullEngine with an explicit consumer-config
// fixture, for the rules whose behavior is config-gated (ssh/vault/curl/monorepo).
func buildFullEngineWithConfig(projectRoot, cwd, fixture string) *engine.Engine {
	cfg := configrules.Load(fixture)

	pe := patheval.NewWithCWD(projectRoot, cwd)
	eng := engine.New()
	eng.SetPathEvaluator(pe)
	// shells is nil: no persistent shell-ownership store offline, so the killshell
	// rule fails secure (Ask) — the same posture as offline replay.
	eng.RegisterRules(setup.RuleChain(eng, pe, cfg, nil)...)
	return eng
}

func makeFileJSON(path string) json.RawMessage {
	b, _ := json.Marshal(hookio.FileToolInput{FilePath: path})
	return b
}

func makeBashJSON(cmd string) json.RawMessage {
	if cmd == "" {
		return json.RawMessage(`{}`)
	}
	b, _ := json.Marshal(hookio.BashToolInput{Command: cmd})
	return b
}
