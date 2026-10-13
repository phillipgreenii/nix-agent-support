package metered

import (
	"strconv"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/ratefeed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
)

// kind says how a gh invocation is run and booked.
type kind int

const (
	// kindGraphQL is `gh api graphql ...`: the bytes pass through, the admission is
	// an estimate, and the response's own rateLimit cost is what gets booked.
	kindGraphQL kind = iota + 1
	// kindREST is `gh api <endpoint> ...`: it runs through ratefeed.Feed (gh api
	// --include), which admits, books and strips the headers itself.
	kindREST
	// kindCLI is every other invocation (`gh search prs`, `gh pr view`, ...): it
	// runs as given and the estimate is booked as the spend, because a porcelain
	// command shows no rate headers.
	kindCLI
)

// route is the decision for one invocation.
type route struct {
	kind   kind
	bucket governor.Bucket
	// cost is the admission estimate in the bucket's unit (1 for everything but
	// `gh search prs`).
	cost int64
	// restArgs are the arguments after `api` that ratefeed.Feed gets (kindREST).
	restArgs []string
}

// routeFor maps the arguments of one gh invocation to how it is metered:
//
//	gh api graphql ...            GraphQL points  cost 1, the response's cost booked
//	gh api <endpoint> ...         REST core       through the Feed
//	gh api search/...             REST search     through the Feed
//	gh api ... --paginate         the endpoint's  run as given: --paginate prints several
//	                              bucket          responses back to back, which the Feed
//	                              cannot split, and the Provider never pages this way
//	gh search prs ... --limit N   REST search     ceil(N / 100) requests, run as given
//	any other gh command          REST core       cost 1, run as given
func routeFor(args []string) route {
	if len(args) > 0 && args[0] == "api" {
		api := args[1:]
		endpoint, ok := apiEndpoint(api)
		switch {
		case !ok:
			// No endpoint to read: gh will refuse it; meter it like any other command.
		case strings.TrimPrefix(endpoint, "/") == "graphql":
			return route{kind: kindGraphQL, bucket: governor.BucketGraphQL, cost: 1}
		case hasFlag(api, "--paginate"):
			return route{kind: kindCLI, bucket: ratefeed.BucketFor(api), cost: 1}
		default:
			return route{kind: kindREST, bucket: ratefeed.BucketFor(api), cost: 1, restArgs: api}
		}
	}
	if len(args) >= 2 && args[0] == "search" && args[1] == "prs" {
		return route{kind: kindCLI, bucket: governor.BucketRESTSearch, cost: searchRequests(args[2:])}
	}
	return route{kind: kindCLI, bucket: governor.BucketRESTCore, cost: 1}
}

// apiValueFlags are the `gh api` flags that take a value in the next argument.
// (-i/--include, --paginate, --silent, --slurp and --verbose are booleans.)
var apiValueFlags = map[string]bool{
	"--cache": true, "--field": true, "-F": true, "--header": true, "-H": true,
	"--hostname": true, "--input": true, "--jq": true, "-q": true, "--method": true,
	"-X": true, "--preview": true, "-p": true, "--raw-field": true, "-f": true,
	"--template": true, "-t": true,
}

// apiEndpoint returns the endpoint of `gh api` arguments (the ones after `api`):
// the first argument that is neither a flag nor a flag's value. ok is false when
// there is none.
func apiEndpoint(args []string) (endpoint string, ok bool) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			if i+1 < len(args) {
				return args[i+1], true
			}
			return "", false
		case !strings.HasPrefix(a, "-") || a == "-":
			return a, true
		case strings.Contains(a, "="):
			// --flag=value, or a short flag with its value attached: one argument.
		case apiValueFlags[a]:
			i++
		}
	}
	return "", false
}

// hasFlag reports whether flag appears among args before any `--`.
func hasFlag(args []string, flag string) bool {
	for _, a := range args {
		if a == "--" {
			return false
		}
		if a == flag {
			return true
		}
	}
	return false
}

const (
	// defaultSearchLimit is gh search's own default result count.
	defaultSearchLimit = 30
	// searchPageSize is how many results gh asks for per request.
	searchPageSize = 100
	// maxSearchResults is the most GitHub ever returns for one search; a larger
	// --limit cannot cost more than this many results' worth of requests.
	maxSearchResults = 1000
)

// searchRequests is the REST search requests one `gh search prs` makes:
// ceil(limit / 100), gh paging by 100, for the --limit it was given (30 when it
// was given none, at least one request, at most what GitHub's 1000-result cap
// allows). args are the arguments after `search prs`; anything after `--` is the
// query, never a flag.
func searchRequests(args []string) int64 {
	limit := defaultSearchLimit
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			break
		}
		var v string
		switch {
		case a == "--limit" || a == "-L":
			if i+1 >= len(args) {
				continue
			}
			i++
			v = args[i]
		case strings.HasPrefix(a, "--limit="):
			v = strings.TrimPrefix(a, "--limit=")
		case strings.HasPrefix(a, "-L"): // -L250 or -L=250
			v = strings.TrimPrefix(strings.TrimPrefix(a, "-L"), "=")
		default:
			continue
		}
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	limit = min(limit, maxSearchResults)
	if limit < 1 {
		return 1
	}
	return int64((limit + searchPageSize - 1) / searchPageSize)
}
