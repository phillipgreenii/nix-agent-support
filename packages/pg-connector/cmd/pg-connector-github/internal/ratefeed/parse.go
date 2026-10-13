package ratefeed

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/github"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
)

// ---- GraphQL: the folded rateLimit selection ------------------------------------

// GraphQLReading is what a GraphQL response carried about the rate limit: the
// points the request spent, and the reading it took. A document that selects only
// `rateLimit { cost }` (the conversation page documents of internal/github do)
// carries no reading: Reading.Remaining is then -1, which the governor ignores,
// so the cost is booked and the bucket's last real reading stays in force.
type GraphQLReading struct {
	Cost    int64
	Reading governor.Reading
}

// ParseGraphQL reads the `rateLimit { cost remaining resetAt }` selection folded
// into a document out of a `gh api graphql` response body. found is false, with
// no error, when the document did not select it.
//
// The selection's wire shape is the one this binary's GraphQL documents already
// carry (github.FoldedRateLimit decodes into the same struct they use); this
// function only turns it into the governor's reading. A selection without
// `remaining` is a cost and no reading, never a reading of zero: a zero would
// hold every class below the reserve until the bucket's window ended.
func ParseGraphQL(body []byte) (r GraphQLReading, found bool, err error) {
	cost, remaining, resetAt, found, err := github.FoldedRateLimit(body)
	if err != nil || !found {
		return GraphQLReading{}, false, err
	}
	rd := governor.Reading{Bucket: governor.BucketGraphQL, Remaining: int64(remaining)}
	if !selectsRemaining(body) {
		rd.Remaining = -1
	}
	if resetAt != "" {
		if t, perr := time.Parse(time.RFC3339, resetAt); perr == nil {
			rd.ResetAt = t.UTC()
		}
	}
	return GraphQLReading{Cost: int64(cost), Reading: rd}, true, nil
}

// selectsRemaining reports whether the rateLimit selection of body carries a
// `remaining` member. github.FoldedRateLimit cannot say: it decodes an absent
// member and a genuine zero alike to 0.
func selectsRemaining(body []byte) bool {
	var env struct {
		Data struct {
			RateLimit *struct {
				Remaining *int64 `json:"remaining"`
			} `json:"rateLimit"`
		} `json:"data"`
	}
	return json.Unmarshal(body, &env) == nil && env.Data.RateLimit != nil && env.Data.RateLimit.Remaining != nil
}

// ---- REST: the output of `gh api --include` ---------------------------------------

// Response is one HTTP response as `gh api --include` prints it: the status
// line, the headers, a blank line, then the body. gh prints it for error
// statuses too (and exits non-zero), so a 403 with Retry-After is readable.
type Response struct {
	Proto      string      // for example "HTTP/2.0"
	StatusCode int         // 403
	Status     string      // "403 Forbidden"
	Header     http.Header // canonicalized names, so Get("X-RateLimit-Limit") finds "X-Ratelimit-Limit"
	Body       []byte
}

// ErrNoResponse means the output held no HTTP response: gh failed before a
// request reached the origin (or printed something unexpected), so nothing was
// spent.
var ErrNoResponse = errors.New("ratefeed: no HTTP response in gh api --include output")

// ParseResponse splits `gh api --include` output into its status, headers and
// body. gh ends the status line with a line feed and each header line with a
// carriage return and line feed; both endings are accepted. It parses ONE
// response: a caller that pages issues one call per page (a `--paginate` run
// prints several responses back to back and is not supported here).
func ParseResponse(out []byte) (*Response, error) {
	line, rest, ok := cutLine(out)
	if !ok {
		return nil, ErrNoResponse
	}
	proto, status, ok := strings.Cut(line, " ")
	if !ok || !strings.HasPrefix(proto, "HTTP/") {
		return nil, ErrNoResponse
	}
	codeText, _, _ := strings.Cut(status, " ")
	code, err := strconv.Atoi(codeText)
	if err != nil || code < 100 || code > 599 {
		return nil, fmt.Errorf("%w: bad status line %q", ErrNoResponse, line)
	}
	hdr := http.Header{}
	for {
		line, next, more := cutLine(rest)
		if !more {
			// The output ended inside the header block: keep what was read.
			rest = nil
			break
		}
		rest = next
		if line == "" {
			break
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		hdr.Add(strings.TrimSpace(name), strings.TrimSpace(value))
	}
	return &Response{Proto: proto, StatusCode: code, Status: status, Header: hdr, Body: rest}, nil
}

// cutLine returns the first line of b without its line ending, the remainder, and
// whether a line feed was found.
func cutLine(b []byte) (line string, rest []byte, ok bool) {
	i := bytes.IndexByte(b, '\n')
	if i < 0 {
		return strings.TrimRight(string(b), "\r"), nil, false
	}
	return strings.TrimRight(string(b[:i]), "\r"), b[i+1:], true
}

// Headers are the rate headers of a REST response. A number that is absent or
// unparsable is -1.
type Headers struct {
	Limit     int64     // X-RateLimit-Limit
	Remaining int64     // X-RateLimit-Remaining
	Used      int64     // X-RateLimit-Used
	Reset     time.Time // X-RateLimit-Reset (epoch seconds); zero when absent
	Resource  string    // X-RateLimit-Resource, lower-cased ("core", "search", "graphql", ...)
	// RetryAfter is the Retry-After wait, in seconds or as an HTTP date; zero
	// when the header is absent, malformed or not in the future.
	RetryAfter time.Duration
}

// ParseHeaders reads the X-RateLimit-* and Retry-After headers. now resolves a
// Retry-After given as an HTTP date.
func ParseHeaders(h http.Header, now time.Time) Headers {
	hd := Headers{
		Limit:     headerInt(h, "X-RateLimit-Limit"),
		Remaining: headerInt(h, "X-RateLimit-Remaining"),
		Used:      headerInt(h, "X-RateLimit-Used"),
		Resource:  strings.ToLower(strings.TrimSpace(h.Get("X-RateLimit-Resource"))),
	}
	if n := headerInt(h, "X-RateLimit-Reset"); n > 0 {
		hd.Reset = time.Unix(n, 0).UTC()
	}
	if v := strings.TrimSpace(h.Get("Retry-After")); v != "" {
		if secs, err := strconv.ParseInt(v, 10, 64); err == nil {
			if secs > 0 {
				hd.RetryAfter = time.Duration(secs) * time.Second
			}
		} else if t, err := http.ParseTime(v); err == nil {
			if d := t.Sub(now); d > 0 {
				hd.RetryAfter = d
			}
		}
	}
	return hd
}

func headerInt(h http.Header, name string) int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(h.Get(name)), 10, 64)
	if err != nil || n < 0 {
		return -1
	}
	return n
}

// Bucket maps the X-RateLimit-Resource header to the governor's bucket: the
// header names the allowance the origin charged, which is the truth when it
// differs from the bucket the caller expected. An empty resource means fallback.
// ok is false for a resource the governor does not meter (for example
// "code_search"); its reading is ignored.
func (h Headers) Bucket(fallback governor.Bucket) (governor.Bucket, bool) {
	switch h.Resource {
	case "":
		return fallback, true
	case "core":
		return governor.BucketRESTCore, true
	case "search":
		return governor.BucketRESTSearch, true
	case "graphql":
		return governor.BucketGraphQL, true
	default:
		return "", false
	}
}

// Reading is the governor reading of the headers, for the bucket they name (or
// fallback when they name none). ok is false when the headers carried no
// remaining count or name a bucket the governor does not meter.
func (h Headers) Reading(fallback governor.Bucket) (governor.Reading, bool) {
	b, ok := h.Bucket(fallback)
	if !ok || h.Remaining < 0 {
		return governor.Reading{}, false
	}
	r := governor.Reading{Bucket: b, Remaining: h.Remaining, ResetAt: h.Reset}
	if h.Limit >= 0 {
		r.Limit = h.Limit
	}
	return r, true
}

// SecondaryLimit reports a secondary rate limit: a 403 or 429 that carries
// Retry-After. The wait is the header's, exactly. (A 403 or 429 without
// Retry-After and with nothing remaining is the PRIMARY limit; its reading, now
// at zero, holds every class through the reserve.)
func SecondaryLimit(statusCode int, h Headers) (time.Duration, bool) {
	if (statusCode == http.StatusForbidden || statusCode == http.StatusTooManyRequests) && h.RetryAfter > 0 {
		return h.RetryAfter, true
	}
	return 0, false
}
