package metered

import (
	"slices"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
)

func TestRouteFor(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		kind     kind
		bucket   governor.Bucket
		cost     int64
		restArgs []string // the arguments after `api` the Feed gets (kindREST only)
	}{
		{"graphql query", []string{"api", "graphql", "-F", "query=x", "-f", "q=y"}, kindGraphQL, governor.BucketGraphQL, 1, nil},
		{"graphql mutation on stdin", []string{"api", "graphql", "--input", "-"}, kindGraphQL, governor.BucketGraphQL, 1, nil},
		{"graphql after a header flag", []string{"api", "-H", "Accept: application/json", "graphql"}, kindGraphQL, governor.BucketGraphQL, 1, nil},
		{"graphql with a leading slash", []string{"api", "/graphql"}, kindGraphQL, governor.BucketGraphQL, 1, nil},
		{"rest read", []string{"api", "repos/o/r/actions/runs", "-f", "per_page=10"}, kindREST, governor.BucketRESTCore, 1, []string{"repos/o/r/actions/runs", "-f", "per_page=10"}},
		{"rest write on stdin", []string{"api", "repos/o/r/pulls/1/reviews", "--method", "POST", "--input", "-"}, kindREST, governor.BucketRESTCore, 1, []string{"repos/o/r/pulls/1/reviews", "--method", "POST", "--input", "-"}},
		{"rest with the method first", []string{"api", "--method", "POST", "repos/o/r/issues/1/comments", "-f", "body=x"}, kindREST, governor.BucketRESTCore, 1, []string{"--method", "POST", "repos/o/r/issues/1/comments", "-f", "body=x"}},
		{"rest with a short method flag", []string{"api", "-X", "POST", "repos/o/r/issues/1/comments"}, kindREST, governor.BucketRESTCore, 1, []string{"-X", "POST", "repos/o/r/issues/1/comments"}},
		{"rest with an attached method", []string{"api", "--method=POST", "repos/o/r/issues/1/comments"}, kindREST, governor.BucketRESTCore, 1, []string{"--method=POST", "repos/o/r/issues/1/comments"}},
		{"search endpoint", []string{"api", "search/issues", "-f", "q=is:pr"}, kindREST, governor.BucketRESTSearch, 1, []string{"search/issues", "-f", "q=is:pr"}},
		{"search endpoint with a leading slash", []string{"api", "/search/issues"}, kindREST, governor.BucketRESTSearch, 1, []string{"/search/issues"}},
		{"search text in a field is not the endpoint", []string{"api", "repos/o/r/issues", "-f", "q=search/x"}, kindREST, governor.BucketRESTCore, 1, []string{"repos/o/r/issues", "-f", "q=search/x"}},
		// --paginate prints several responses back to back, which the Feed cannot
		// split; the Provider never pages that way, and such a call runs as given.
		{"paginate runs as given on core", []string{"api", "repos/o/r/pulls/1/comments", "--paginate"}, kindCLI, governor.BucketRESTCore, 1, nil},
		{"paginate runs as given on search", []string{"api", "search/issues", "--paginate"}, kindCLI, governor.BucketRESTSearch, 1, nil},
		{"api without an endpoint", []string{"api"}, kindCLI, governor.BucketRESTCore, 1, nil},
		{"api with only flags", []string{"api", "--include"}, kindCLI, governor.BucketRESTCore, 1, nil},
		{"gh search prs with a limit", []string{"search", "prs", "--json", "number", "--limit", "250", "--", "is:open"}, kindCLI, governor.BucketRESTSearch, 3, nil},
		{"gh search prs with the default limit", []string{"search", "prs", "--json", "number", "--", "is:open"}, kindCLI, governor.BucketRESTSearch, 1, nil},
		{"porcelain pr view", []string{"pr", "view", "7", "--repo", "o/r"}, kindCLI, governor.BucketRESTCore, 1, nil},
		{"porcelain run list", []string{"run", "list", "--repo", "o/r"}, kindCLI, governor.BucketRESTCore, 1, nil},
		{"gh search of another kind is porcelain", []string{"search", "repos", "x"}, kindCLI, governor.BucketRESTCore, 1, nil},
		{"no arguments", nil, kindCLI, governor.BucketRESTCore, 1, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := routeFor(tc.args)
			if got.kind != tc.kind || got.bucket != tc.bucket || got.cost != tc.cost {
				t.Errorf("routeFor(%q) = kind %d bucket %s cost %d, want kind %d bucket %s cost %d",
					tc.args, got.kind, got.bucket, got.cost, tc.kind, tc.bucket, tc.cost)
			}
			if !slices.Equal(got.restArgs, tc.restArgs) {
				t.Errorf("restArgs = %q, want %q", got.restArgs, tc.restArgs)
			}
		})
	}
}

func TestAPIEndpointSkipsFlagsAndTheirValues(t *testing.T) {
	cases := []struct {
		args []string
		want string
		ok   bool
	}{
		{[]string{"graphql", "-F", "query=x"}, "graphql", true},
		{[]string{"-F", "query=x", "graphql"}, "graphql", true},
		{[]string{"-f", "a=b", "-F", "c=d", "--hostname", "ghe.example.com", "repos/o/r"}, "repos/o/r", true},
		{[]string{"--jq", ".[]", "-q", ".x", "-t", "{{.}}", "repos/o/r"}, "repos/o/r", true},
		{[]string{"--cache", "1h", "-p", "x", "repos/o/r"}, "repos/o/r", true},
		{[]string{"-X", "POST", "repos/o/r"}, "repos/o/r", true},
		{[]string{"-XPOST", "repos/o/r"}, "repos/o/r", true},
		{[]string{"--include", "--silent", "repos/o/r"}, "repos/o/r", true},
		{[]string{"-Fquery=x", "graphql"}, "graphql", true},
		{[]string{"--", "repos/o/r"}, "repos/o/r", true},
		{[]string{"--"}, "", false},
		{[]string{"--include"}, "", false},
		{[]string{"-F", "query=x"}, "", false},
		{nil, "", false},
	}
	for _, tc := range cases {
		if got, ok := apiEndpoint(tc.args); got != tc.want || ok != tc.ok {
			t.Errorf("apiEndpoint(%q) = %q, %v, want %q, %v", tc.args, got, ok, tc.want, tc.ok)
		}
	}
}

// gh pages a search by 100, so a limit of N costs ceil(N / 100) requests; GitHub
// returns at most 1000 results for one search.
func TestSearchRequestsIsCeilOfTheLimitOverOneHundred(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want int64
	}{
		{"no limit is gh's default of 30", nil, 1},
		{"1", []string{"--limit", "1"}, 1},
		{"100", []string{"--limit", "100"}, 1},
		{"101", []string{"--limit", "101"}, 2},
		{"250", []string{"--limit", "250"}, 3},
		{"attached with =", []string{"--limit=250"}, 3},
		{"short flag", []string{"-L", "250"}, 3},
		{"short flag attached", []string{"-L250"}, 3},
		{"short flag attached with =", []string{"-L=250"}, 3},
		{"1000", []string{"--limit", "1000"}, 10},
		{"more than GitHub returns is clamped", []string{"--limit", "5000"}, 10},
		{"zero is still one request", []string{"--limit", "0"}, 1},
		{"negative is still one request", []string{"--limit", "-5"}, 1},
		{"unparsable keeps the default", []string{"--limit", "many"}, 1},
		{"a limit with no value", []string{"--limit"}, 1},
		{"the last limit wins", []string{"--limit", "10", "--limit", "300"}, 3},
		{"flags among others", []string{"--json", "number", "--limit", "100", "--", "is:open"}, 1},
		{"after -- is query text, not a flag", []string{"--json", "number", "--", "--limit", "500"}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := searchRequests(tc.args); got != tc.want {
				t.Errorf("searchRequests(%q) = %d, want %d", tc.args, got, tc.want)
			}
		})
	}
}
