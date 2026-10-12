package github

import (
	"encoding/json"
	"fmt"
)

// FoldedRateLimit reads the `rateLimit { cost remaining resetAt }` selection
// that a document folds into its own request (see rateLimitSelection and
// runGraphQL) out of a `gh api graphql` response body, so the daemon's budget
// governor takes its GraphQL readings from exactly the selection this package's
// documents already carry rather than from a second parser of the same shape.
//
// found is false, with no error, when the response is well-formed but the
// document did not select rateLimit (or the response carries no data). cost is
// the points the request spent, remaining what the token has left, and resetAt
// when GitHub replenishes it (RFC 3339, as GitHub reports it).
func FoldedRateLimit(body []byte) (cost, remaining int, resetAt string, found bool, err error) {
	var env struct {
		Data struct {
			RateLimit *rateLimitSelection `json:"rateLimit"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return 0, 0, "", false, fmt.Errorf("github: parse graphql response: %w", err)
	}
	sel := env.Data.RateLimit
	if sel == nil {
		return 0, 0, "", false, nil
	}
	return sel.Cost, sel.Remaining, sel.ResetAt, true, nil
}
