//go:build !integration

package effectpolicy

// monotonicityDefaultPairs is the default-tier pair count: a fixed-seed
// sample large enough to exercise every strictness class (the test itself
// fails if a class is vacuous) while keeping the unit gate cheap under host
// load. The full 10^4-pair exit-criterion sweep runs in the integration tier
// (monotonicity_pairs_integration_test.go), covered by the
// claude-extended-tool-approver-integration-tests check.
// CETA_MONOTONICITY_PAIRS overrides either value.
const monotonicityDefaultPairs = 1500
