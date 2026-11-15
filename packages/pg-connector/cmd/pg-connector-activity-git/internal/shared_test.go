package internal

import (
	"sort"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

// Values shared by the unit tests and the integration-tagged real-git tests.
const (
	meEmail    = "me@example.test"
	otherEmail = "other@example.test"
)

var (
	rangeSince  = time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	rangeBefore = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	pinnedNow   = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
)

func subjects(items []schema.ActivityItem) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.Summary)
	}
	sort.Strings(out)
	return out
}
