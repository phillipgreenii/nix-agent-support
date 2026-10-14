package passthrough

import (
	"fmt"
	"strings"
)

// cannedGH answers the gh invocations of the golden scenario with fixed output
// (generic names: owner/repo "o/r", the viewer "phillipgreenii"). The same function was run
// against the old backend (cmd/pg-connector-pr-github) to produce testdata/golden_*.json,
// so the golden files state what the old backend answers for this gh output.
//
// reply is the stdout, ok false means the invocation is not part of the scenario.
func cannedGH(args []string) (reply string, ok bool) {
	line := strings.Join(args, " ")
	switch {
	case len(args) >= 2 && args[0] == "search" && args[1] == "prs":
		return cannedSearch(line), true
	case len(args) >= 2 && args[0] == "api" && args[1] == "graphql" && strings.Contains(line, "viewer"):
		return `{"data":{"viewer":{"login":"phillipgreenii"}}}`, true
	case len(args) >= 2 && args[0] == "api" && args[1] == "graphql" && strings.Contains(line, "rateLimit"):
		// Only the old backend probes the reserve before its first read.
		return `{"data":{"rateLimit":{"remaining":4000,"resetAt":"2026-09-30T00:00:00Z"}}}`, true
	case len(args) >= 3 && args[0] == "pr" && args[1] == "view" && args[2] == "12":
		return `{"id":"PR_node_12","number":12,"title":"Add feature","baseRefName":"main","url":"https://example.invalid/o/r/pull/12",` +
			`"author":{"login":"phillipgreenii"},"state":"MERGED","mergedAt":"2026-09-07T12:00:00Z"}`, true
	case len(args) >= 3 && args[0] == "pr" && args[1] == "view" && args[2] == "15":
		return `{"reviews":[` +
			`{"id":"PRR_1","author":{"login":"phillipgreenii"},"state":"APPROVED","body":"ok","submittedAt":"2026-09-10T08:00:00Z"},` +
			`{"id":"PRR_2","author":{"login":"teammate"},"state":"COMMENTED","body":"","submittedAt":"2026-09-10T09:00:00Z"}]}`, true
	}
	return "", false
}

func prJSON(n int, title, author, created, closed string) string {
	return fmt.Sprintf(`{"number":%d,"title":%q,"url":"https://example.invalid/o/r/pull/%d","state":"open","isDraft":false,`+
		`"author":{"login":%q},"labels":[],"repository":{"nameWithOwner":"o/r"},"createdAt":%q,"closedAt":%q}`,
		n, title, n, author, created, closed)
}

func cannedSearch(line string) string {
	switch {
	case strings.Contains(line, "created:"):
		return "[" + prJSON(12, "Add feature", "phillipgreenii", "2026-09-05T10:00:00Z", "") + "," +
			prJSON(13, "Other work", "teammate", "2026-09-06T10:00:00Z", "") + "]"
	case strings.Contains(line, "merged:"):
		return "[" + prJSON(12, "Add feature", "phillipgreenii", "2026-09-05T10:00:00Z", "2026-09-07T12:00:00Z") + "]"
	case strings.Contains(line, "closed:"):
		return "[" + prJSON(14, "Abandoned idea", "phillipgreenii", "2026-09-01T00:00:00Z", "2026-09-09T08:30:00Z") + "]"
	case strings.Contains(line, "reviewed-by:"):
		return "[" + prJSON(15, "Review me", "teammate", "2026-09-02T00:00:00Z", "") + "]"
	case strings.Contains(line, "commenter:"):
		return "[]"
	}
	// The search op's own query.
	return "[" + prJSON(7, "Add feature", "phillipgreenii", "2026-09-05T10:00:00Z", "") + "," +
		prJSON(8, "Fix the thing", "teammate", "2026-09-06T10:00:00Z", "") + "]"
}
