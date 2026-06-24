// Package enrich computes deterministic, LLM-free enrichment fields for a PR:
// kind, languages, size, and urgency. All functions are pure (no I/O, no clock,
// no network) so they are fully table-testable.
package enrich

// bucketSize maps a total changed-line count (additions+deletions) to a
// coarse size bucket.
func bucketSize(total int) string {
	switch {
	case total < 10:
		return "XS"
	case total < 30:
		return "S"
	case total < 100:
		return "M"
	case total < 500:
		return "L"
	default:
		return "XL"
	}
}
