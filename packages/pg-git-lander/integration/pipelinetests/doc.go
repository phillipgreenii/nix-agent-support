//go:build integration

// Package pipelinetests holds the real-git scenarios of internal/pipeline:
// every test builds a real repository with x/gittest (git init, worktrees,
// rebases, lock refs, hook scripts, signals) and drives pipeline.Start, Run
// and Resume against it with real git processes. That is integration work, so
// it lives here behind the integration build tag, outside the unit gate
// (go test ./...), and runs in the pg-git-lander-integration-tests check. The
// process-free unit tests of the package stay in internal/pipeline and use
// the fake Runner of x/cmdruntest.
package pipelinetests
