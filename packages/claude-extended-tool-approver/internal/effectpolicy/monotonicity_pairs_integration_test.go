//go:build integration

package effectpolicy

// monotonicityDefaultPairs is the integration-tier pair count: the Phase 2
// exit criterion's 10^4 pairs, run by the
// claude-extended-tool-approver-integration-tests check (-tags integration).
// The default unit tier uses a smaller sample
// (monotonicity_pairs_default_test.go).
const monotonicityDefaultPairs = 10000
