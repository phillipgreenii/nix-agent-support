package interpret

import "strings"

// classifyCategory ranks cfg's category_vocabulary entries by how many
// times any of their keyword patterns occur in the PR's title+body (case
// insensitive), picking the highest-scoring category with an alphabetical
// tie-break on the category name for determinism. Returns "" when the
// vocabulary is empty or nothing matches (uncategorized).
//
// There is no Go source in this repo to port this from — "df-categorize" is
// today's LLM/ccpool-prompt-driven classifier in the private deployment, not
// a package under packages/pg-pr (see interpret.go's package doc). This is
// therefore a new, deterministic, config-vocabulary-driven implementation,
// not a port — a documented, cited deviation the packet's Validation section
// explicitly allows.
func classifyCategory(pr prShow, vocabulary map[string][]string) string {
	if len(vocabulary) == 0 {
		return ""
	}
	hay := strings.ToLower(pr.Title + "\n" + pr.Body)

	best := ""
	bestCount := 0
	for _, name := range sortedKeys(vocabulary) {
		count := 0
		for _, kw := range vocabulary[name] {
			if kw == "" {
				continue
			}
			count += strings.Count(hay, strings.ToLower(kw))
		}
		if count > bestCount {
			bestCount = count
			best = name
		}
	}
	return best
}
