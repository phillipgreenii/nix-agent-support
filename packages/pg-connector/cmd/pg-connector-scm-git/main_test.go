package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"testing"

	internal "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-scm-git/internal"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// fakeRunner is a minimal double for internal.Provider's Runner seam, so
// this file's wiring tests never spawn a real `git` subprocess (the
// sibling backend's own internal Provider is exercised against real git
// checkouts in internal/provider_realgit_test.go).
type fakeRunner struct {
	branch string
}

func (f fakeRunner) Run(ctx context.Context, dir string, args ...string) (string, error) {
	switch {
	case len(args) >= 1 && args[0] == "rev-parse":
		// Matches both the "--git-common-dir" call repoRootFor makes and
		// any other rev-parse variant a future caller might add — the
		// common-dir answer is enough for every current Provider method.
		return "/home/u/repo/.git", nil
	case len(args) >= 2 && args[0] == "branch" && args[1] == "--show-current":
		return f.branch, nil
	}
	return "", nil
}

func newTestBackend() *internal.Provider {
	return internal.New(fakeRunner{branch: "feature"})
}

// TestNewDispatchTable_CapabilitiesDeclaresScmSchemaVersionAndNoAuthStatus
// is the packet's required test asserting this binary's own capabilities
// response — schemaVersions.scm populated, and auth_status deliberately
// absent from Ops since this backend implements no AuthChecker [design:
// §4.6, §4.7].
func TestNewDispatchTable_CapabilitiesDeclaresScmSchemaVersionAndNoAuthStatus(t *testing.T) {
	table := newDispatchTable(newTestBackend())
	entry, ok := table[scriptout.OpCapabilities]
	if !ok {
		t.Fatal("capabilities entry missing from this binary's own dispatch table")
	}
	result, err := entry.Handle(context.Background(), nil)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	resp, ok := result.(scriptout.CapabilitiesResponse)
	if !ok {
		t.Fatalf("result type = %T, want scriptout.CapabilitiesResponse", result)
	}
	if resp.SchemaVersions["scm"] == 0 {
		t.Fatalf("SchemaVersions = %#v, want a populated \"scm\" entry", resp.SchemaVersions)
	}
	for _, op := range resp.Ops {
		if op == scriptout.OpAuthStatus {
			t.Fatalf("Ops = %v, must not include auth_status: this backend implements no AuthChecker [design: §4.6, §4.7]", resp.Ops)
		}
	}
}

// TestServeLoop_WorktreeListRoundTripsThroughStdinStdout is the packet's
// required scriptout-level test that this binary's own main() correctly
// wires its op table into the Tier-1 core's generic serve loop
// (pkg/scriptout.ServeLoop): an end-to-end stdin-JSON-in, stdout-JSON-out
// exercise, mirroring the sibling pg-connector-pr-github backend's own
// main_test.go style (itself mirroring
// packages/pg-pr/pkg/plugin/scriptout/scriptout_test.go's runServe
// helper, unexported and in a different module's package this one cannot
// reach into) — the real os.Stdin/os.Stdout ServeLoop always reads,
// swapped for pipes here, is the only external seam scriptout.ServeLoop
// exposes.
func TestServeLoop_WorktreeListRoundTripsThroughStdinStdout(t *testing.T) {
	origStdin, origStdout := os.Stdin, os.Stdout
	defer func() { os.Stdin, os.Stdout = origStdin, origStdout }()

	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdin, os.Stdout = inR, outW

	if _, err := inW.WriteString(`{"op":"branch_detect","args":{"cwd":"/home/u/repo/sub"}}`); err != nil {
		t.Fatalf("write request: %v", err)
	}
	if err := inW.Close(); err != nil {
		t.Fatalf("close stdin writer: %v", err)
	}

	code := scriptout.ServeLoop(newDispatchTable(newTestBackend()))

	if err := outW.Close(); err != nil {
		t.Fatalf("close stdout writer: %v", err)
	}
	raw, err := io.ReadAll(outR)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}

	if code != 0 {
		t.Fatalf("exit code = %d, stdout=%s", code, raw)
	}
	var resp scriptout.Response
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("decode response: %v (stdout=%s)", err, raw)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected wire error: %+v", resp.Error)
	}
	var info struct {
		Repo   string `json:"repo"`
		Branch string `json:"branch"`
	}
	if err := json.Unmarshal(resp.Result, &info); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if info.Repo != "repo" || info.Branch != "feature" {
		t.Fatalf("result = %+v", info)
	}
}

// TestServeLoop_AuthStatusIsUnknownOp is the packet's required test
// asserting this binary never forces a meaningless answer to auth_status:
// it implements no pkg/provider.AuthChecker, so
// pkg/provider/scm.NewDispatchTable omits an auth_status entry from this
// binary's own dispatch table entirely, and a raw request for it comes
// back as the wire-level unknown_op sentinel — which pg-connector's own
// `auth status` fan-out (cmd/pg-connector/auth.go, a sibling packet's own
// suite) already reports as "disabled: not applicable" [design: §4.6].
func TestServeLoop_AuthStatusIsUnknownOp(t *testing.T) {
	origStdin, origStdout := os.Stdin, os.Stdout
	defer func() { os.Stdin, os.Stdout = origStdin, origStdout }()

	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdin, os.Stdout = inR, outW

	if _, err := inW.WriteString(`{"op":"auth_status","args":{}}`); err != nil {
		t.Fatalf("write request: %v", err)
	}
	if err := inW.Close(); err != nil {
		t.Fatalf("close stdin writer: %v", err)
	}

	code := scriptout.ServeLoop(newDispatchTable(newTestBackend()))

	if err := outW.Close(); err != nil {
		t.Fatalf("close stdout writer: %v", err)
	}
	raw, err := io.ReadAll(outR)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}

	// unknown_op now gets its own branchable exit code rather than the old
	// flat 1 (bead pg2-7vgn5); computed via scriptout.ExitCodeForCode
	// rather than hardcoded, so this test tracks the mapping table instead
	// of asserting an unrelated magic number.
	if want := scriptout.ExitCodeForCode("unknown_op"); code != want {
		t.Fatalf("exit code = %d, want %d (unknown_op); stdout=%s", code, want, raw)
	}
	var resp scriptout.Response
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("decode response: %v (stdout=%s)", err, raw)
	}
	if resp.Error == nil || resp.Error.Code != "unknown_op" {
		t.Fatalf("resp.Error = %+v, want code=unknown_op", resp.Error)
	}
}
