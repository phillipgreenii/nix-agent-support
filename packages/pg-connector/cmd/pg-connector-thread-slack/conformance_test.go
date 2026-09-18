// conformance_test.go: this bead's own required conformance-suite proof —
// pkg/scriptout/conformance's existing Backend/ExecBackend/driver.Run
// suite (unchanged; this bead's own Contract: "this packet is a NEW
// consumer of it, not a modifier") run against the REAL COMPILED
// pg-connector-thread-slack binary, with a fake `claude` executable
// resolved via internal.EnvBinary (this file's own env-var override,
// mirroring cmd/pg-connector-issue-jira's identical "a fake claude
// executable resolved via $PATH or an env-var override, mirroring
// pjira's own resolution convention" contract clause). Deliberately NOT
// gated behind a build tag: this bead's own Validation section runs this
// package's tests with plain `go test`, no `-tags` flag, so the binary
// build + subprocess exec below run as part of every ordinary
// `go test ./...` pass over this module (unlike
// cmd/pg-connector/e2e_contract_test.go's `contract`-tagged real-external-
// credential suite, or packages/pg-router/cmd/pg-router/e2e_test.go's
// `smoke`-tagged one) — this suite is fully hermetic (a fake claude
// script, no network, no real credential) so there is no reason to keep
// it out of the default run.
package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	internal "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-thread-slack/internal"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout/conformance"
)

// buildThreadSlackBinary compiles this package's own real binary via
// `go build -o <dir>/pg-connector-thread-slack .`, run with THIS
// package's own directory as the build's working directory — mirrors
// packages/pg-router/cmd/pg-router/e2e_test.go's own
// buildPgRouterBinary/cmd/pg-connector/e2e_contract_test.go's
// buildContractBinaries precedent, minus their build-tag gating (see this
// file's own package doc comment for why).
func buildThreadSlackBinary(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "pg-connector-thread-slack")
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "build", "-o", bin, ".")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go build ./cmd/pg-connector-thread-slack: %v\n%s", err, out)
	}
	return bin
}

// writeFakeClaude writes a fake `claude` executable script to a fresh
// temp dir and returns its path — never on real $PATH, resolved instead
// via internal.EnvBinary below. It discards stdin (the prompt) and always
// answers a well-formed `claude -p --output-format json` envelope whose
// inner result is a well-formed, found:true thread reply — enough for
// invokingCapabilities'/invokingUnknownOp's/invokingMalformedStdin's own
// generic wire-envelope checks, none of which actually call this binary's
// "show"/"list" ops with a real payload (driver.go's own Run only
// exercises unknown-op/malformed-stdin/capabilities against a live
// backend — see its own doc comment).
func writeFakeClaude(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	inner := `{\"found\":true,\"id\":\"1\",\"channel\":\"C1\",\"permalink\":\"https://x.invalid/1\",\"text\":\"hi\"}`
	script := "#!/bin/sh\ncat >/dev/null\necho '{\"result\":\"" + inner + "\",\"is_error\":false}'\n"
	path := filepath.Join(dir, "claude")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake claude: %v", err)
	}
	return path
}

// TestConformance_RealBinary_FakeClaudeOnEnvOverride is this bead's own
// required proof: "the full conformance suite passes when run against
// the real compiled pg-connector-thread-slack binary with a fake claude
// executable on PATH" [design: 8's acceptance-criteria block].
func TestConformance_RealBinary_FakeClaudeOnEnvOverride(t *testing.T) {
	bin := buildThreadSlackBinary(t)
	fakeClaude := writeFakeClaude(t)
	// ExecBackend spawns bin with no explicit Env, so it inherits this
	// test process's own env — t.Setenv here reaches the child exactly
	// the way cmd/pg-connector-issue-jira/internal.CLIRunner's own
	// EnvBinary resolution is exercised against a real subprocess.
	t.Setenv(internal.EnvBinary, fakeClaude)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	results := conformance.Run(ctx, conformance.ExecBackend{Binary: bin})
	if len(results) == 0 {
		t.Fatal("conformance.Run produced no results at all")
	}
	for _, r := range results {
		if r.Err != nil {
			t.Errorf("%s: %v", r.Name, r.Err)
		}
	}
}
