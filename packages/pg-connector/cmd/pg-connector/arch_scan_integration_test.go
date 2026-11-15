//go:build integration

package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// Whole-module source scans. Each one parses or imports-walks every package of the
// module (1 to 3.5 s under -race, the three slowest tests of this package), so none
// belongs in the unit tier (V-4). They run, with every other integration-tagged
// test, through `nix build .#checks.<system>.pg-connector-integration-tests`, or
// `go test -tags integration -run <name> ./cmd/pg-connector/`. The fast fixture
// tests that prove each scanner detects what it must stay untagged next to the
// TestGHExecChokePoint is the module-wide half of bead pg2-ilzq9's guarantee:
// the four sites fixed there were fixed one at a time, and nothing stopped a
// fifth from appearing. It asserts that `gh` is spawned from exactly the
// choke points in ghChokePoints, one pair per backend that ports this
// mechanism, and (bead pg2-9df7aq) that it is caught however the binary is
// named: the literal, a const or var holding it, a multi-line or aliased
// call, and — in pg-connector-github, whose binary is configured — ANY exec
// outside the choke-point functions or any exec of a configured-binary value.
//
// Every other package reaches gh through a backend's own github.CLI
// (Run/RunStdin, or Command for sites that wire up their own
// stdout/stderr/dir), so a new direct exec here means an unauthenticated gh
// became reachable again — which is what popped interactive auth screens
// under the launchd sync agent.
func TestGHExecChokePoint(t *testing.T) {
	offenders, err := ghExecFindings(moduleRoot(t))
	if err != nil {
		t.Fatalf("scan module: %v", err)
	}
	if len(offenders) > 0 {
		t.Fatalf("gh is exec'd directly outside the protected choke point:\n  %s\n\n"+
			"Route it through github.NewCLI(): Run/RunStdin for a whole invocation, or\n"+
			"Command when the call site needs the *exec.Cmd (working directory, own\n"+
			"stderr capture). See ghexec.go.",
			strings.Join(offenders, "\n  "))
	}
}

func TestBackendFilesetCoversImports(t *testing.T) {
	moduleRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve module root: %v", err)
	}
	violations, err := evaluateFilesetCoverage(moduleRoot)
	if err != nil {
		t.Fatalf("evaluate fileset coverage: %v", err)
	}
	for _, v := range violations {
		t.Error(v)
	}
}

// TestUnitTierHasNoIntegrationTests fails when a test file compiled by a plain
// `go test ./...` starts the daemon, runs a built binary or Go tool, or needs a real
// external tool. It is the regression guard of the integration/unit split.
func TestUnitTierHasNoIntegrationTests(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve module root: %v", err)
	}
	violations, integrationFiles, err := evaluateUnitTierPurity(root, unitReexecAllowed)
	if err != nil {
		t.Fatalf("walk module: %v", err)
	}
	for _, v := range violations {
		t.Error(v)
	}
	if integrationFiles == 0 {
		t.Errorf("no test file under %s carries //go:build integration: either the integration tier is empty or this guard scans the wrong root", root)
	}
}
