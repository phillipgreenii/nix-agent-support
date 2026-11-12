//go:build !contract

package main

import (
	"context"
	"os"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// TestMain routes backend invocations through the in-process fake registry
// (see fake_inproc_test.go) and removes the shared fake-backend script cache
// (see installExec) once every test has run. The contract-tagged build has its
// own TestMain.
func TestMain(m *testing.M) {
	installFakeRunner()
	code := m.Run()
	removeExecCache()
	os.Exit(code)
}

// installFakeRunner routes backend invocations through the registry; TestMain
// calls it once before any test runs. It lives beside TestMain, in the same
// build-tag scope, so the contract-tagged build does not carry it unused.
func installFakeRunner() (restore func()) {
	return scriptout.SwapRunner(func(ctx context.Context, c scriptout.Call) scriptout.Result {
		fakeMu.Lock()
		h, ok := fakeHandlers[c.Name]
		fakeMu.Unlock()
		if !ok {
			return scriptout.ExecRunner(ctx, c)
		}
		return scriptout.Result{Stdout: h(c.Stdin)}
	})
}
