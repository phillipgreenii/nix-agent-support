package enrich

import (
	"sort"

	enry "github.com/go-enry/go-enry/v2"
)

// detectLanguages maps changed-file paths to languages using go-enry's
// path-only detection (no blob contents), tallies by file count, and returns
// the languages sorted by count desc then name asc. Unrecognized paths are
// skipped. Returns nil for no input (or no recognized files).
func detectLanguages(files []string) []string {
	if len(files) == 0 {
		return nil
	}
	counts := map[string]int{}
	for _, f := range files {
		if lang := enry.GetLanguage(f, nil); lang != "" {
			counts[lang]++
		}
	}
	if len(counts) == 0 {
		return nil
	}
	out := make([]string, 0, len(counts))
	for l := range counts {
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool {
		if counts[out[i]] != counts[out[j]] {
			return counts[out[i]] > counts[out[j]]
		}
		return out[i] < out[j]
	})
	return out
}
