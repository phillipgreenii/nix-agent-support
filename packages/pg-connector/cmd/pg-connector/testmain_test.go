//go:build !contract

package main

import (
	"os"
	"testing"
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
