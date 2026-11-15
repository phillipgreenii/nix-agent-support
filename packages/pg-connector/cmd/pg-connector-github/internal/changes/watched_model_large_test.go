//go:build integration

package changes

import "testing"

// The large run of the watched-flag model: 25 seeds of 120 steps each. It is
// the same property as the unit loop in watched_model_test.go, run long enough
// to reach rarer interleavings, and takes tens of seconds on a quiet host, so
// it lives in the integration tier (checks pg-connector-integration-tests).
func TestModel_AnUnwatchedPRNeverEmitsAndTheFlagIsStickyUntilEviction_Large(t *testing.T) {
	runWatchedModel(t, 25, 120)
}
