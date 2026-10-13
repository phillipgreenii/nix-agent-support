package ratefeed

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
)

var (
	t0    = time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	reset = t0.Add(30 * time.Minute)
)

// ghInclude builds `gh api --include` output the way gh prints it: the status
// line ends with a line feed, each header line and the blank line with a
// carriage return and line feed, and gh canonicalizes header names (so
// X-RateLimit-Limit arrives as X-Ratelimit-Limit).
func ghInclude(status string, headers [][2]string, body string) []byte {
	var b strings.Builder
	b.WriteString("HTTP/2.0 " + status + "\n")
	for _, h := range headers {
		b.WriteString(h[0] + ": " + h[1] + "\r\n")
	}
	b.WriteString("\r\n")
	b.WriteString(body)
	return []byte(b.String())
}

func rateHeaders(resource string, limit, remaining, used int, extra ...[2]string) [][2]string {
	h := [][2]string{
		{"Content-Type", "application/json; charset=utf-8"},
		{"X-Github-Request-Id", "0000:0000:0000:0000:00000000"},
		{"X-Ratelimit-Limit", fmt.Sprint(limit)},
		{"X-Ratelimit-Remaining", fmt.Sprint(remaining)},
		{"X-Ratelimit-Reset", fmt.Sprint(reset.Unix())},
		{"X-Ratelimit-Resource", resource},
		{"X-Ratelimit-Used", fmt.Sprint(used)},
	}
	return append(h, extra...)
}

// sameReading compares two readings with Equal on their times.
func sameReading(a, b governor.Reading) bool {
	return a.Bucket == b.Bucket && a.Limit == b.Limit && a.Remaining == b.Remaining &&
		a.ResetAt.Equal(b.ResetAt) && a.At.Equal(b.At)
}

func TestParseGraphQLReadsTheFoldedRateLimitSelection(t *testing.T) {
	const searchPage = `{"data":{"rateLimit":{"cost":1,"remaining":3987,"resetAt":"2026-10-12T12:30:00Z"},` +
		`"search":{"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[{"id":"PR_node1","number":7}]}}}`
	const filesPage = `{"data":{"rateLimit":{"cost":2,"remaining":3985,"resetAt":"2026-10-12T12:30:00Z"},` +
		`"repository":{"pullRequest":{"files":{"totalCount":0,"nodes":[]}}}}}`
	cases := []struct {
		name, body        string
		found             bool
		cost, remaining   int64
		wantReset         time.Time
		wantErrSubstrings string
	}{
		{name: "a search page", body: searchPage, found: true, cost: 1, remaining: 3987, wantReset: reset},
		{name: "a connection page", body: filesPage, found: true, cost: 2, remaining: 3985, wantReset: reset},
		{name: "no resetAt reported", body: `{"data":{"rateLimit":{"cost":1,"remaining":10}}}`, found: true, cost: 1, remaining: 10},
		{name: "an unparsable resetAt", body: `{"data":{"rateLimit":{"cost":1,"remaining":10,"resetAt":"soon"}}}`, found: true, cost: 1, remaining: 10},
		// The conversation page documents select `rateLimit { cost }` alone: the
		// cost is real, the absent remaining is NOT a reading of zero.
		{name: "a cost-only selection", body: `{"data":{"rateLimit":{"cost":1},"repository":{}}}`, found: true, cost: 1, remaining: -1},
		{name: "a cost and resetAt without remaining", body: `{"data":{"rateLimit":{"cost":2,"resetAt":"2026-10-12T12:30:00Z"}}}`, found: true, cost: 2, remaining: -1, wantReset: reset},
		{name: "a genuine zero remaining", body: `{"data":{"rateLimit":{"cost":1,"remaining":0,"resetAt":"2026-10-12T12:30:00Z"}}}`, found: true, cost: 1, remaining: 0, wantReset: reset},
		{name: "a document that did not select it", body: `{"data":{"viewer":{"login":"example-user"}}}`},
		{name: "an error with null data", body: `{"data":null,"errors":[{"message":"boom"}]}`},
		{name: "not JSON", body: `HTTP/2.0 502`, wantErrSubstrings: "parse graphql response"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, found, err := ParseGraphQL([]byte(tc.body))
			if tc.wantErrSubstrings != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErrSubstrings) {
					t.Fatalf("err = %v, want one naming %q", err, tc.wantErrSubstrings)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if found != tc.found {
				t.Fatalf("found = %v", found)
			}
			if !found {
				return
			}
			want := governor.Reading{Bucket: governor.BucketGraphQL, Remaining: tc.remaining, ResetAt: tc.wantReset}
			if got.Cost != tc.cost || !sameReading(got.Reading, want) {
				t.Errorf("got %+v, want cost %d reading %+v", got, tc.cost, want)
			}
		})
	}
}

func TestParseResponseSplitsStatusHeadersAndBody(t *testing.T) {
	out := ghInclude("200 OK", rateHeaders("core", 5000, 4987, 13), `{"total_count":1,"workflow_runs":[]}`)
	resp, err := ParseResponse(out)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Proto != "HTTP/2.0" || resp.StatusCode != 200 || resp.Status != "200 OK" {
		t.Errorf("status = %q %d %q", resp.Proto, resp.StatusCode, resp.Status)
	}
	// Lookup is by the canonical name whichever way the origin cased it.
	if got := resp.Header.Get("X-RateLimit-Remaining"); got != "4987" {
		t.Errorf("X-RateLimit-Remaining = %q", got)
	}
	if got := resp.Header.Get("content-type"); !strings.HasPrefix(got, "application/json") {
		t.Errorf("content-type = %q", got)
	}
	if string(resp.Body) != `{"total_count":1,"workflow_runs":[]}` {
		t.Errorf("body = %q", resp.Body)
	}

	// Line feeds alone work too, a no-content response has an empty body, and a
	// body that itself contains a blank line stays whole.
	resp, err = ParseResponse([]byte("HTTP/1.1 204 No Content\nX-Ratelimit-Remaining: 7\n\n"))
	if err != nil || resp.StatusCode != 204 || len(resp.Body) != 0 || resp.Header.Get("X-RateLimit-Remaining") != "7" {
		t.Errorf("204 = %+v err %v", resp, err)
	}
	resp, err = ParseResponse(ghInclude("200 OK", nil, "line one\n\nline three"))
	if err != nil || string(resp.Body) != "line one\n\nline three" {
		t.Errorf("multi-line body = %+v err %v", resp, err)
	}
}

func TestParseResponseRefusesOutputWithoutAResponse(t *testing.T) {
	for name, out := range map[string]string{
		"empty":        "",
		"an error":     "gh: error connecting to api.github.com\n",
		"no newline":   "HTTP/2.0 200 OK",
		"bad status":   "HTTP/2.0 abc OK\n\n",
		"a JSON body":  `{"message":"Not Found"}`,
		"out of range": "HTTP/2.0 99 Nope\n\n",
	} {
		if _, err := ParseResponse([]byte(out)); !errors.Is(err, ErrNoResponse) {
			t.Errorf("%s: err = %v, want ErrNoResponse", name, err)
		}
	}
}

func TestParseHeadersReadsEveryRateHeader(t *testing.T) {
	resp, err := ParseResponse(ghInclude("200 OK", rateHeaders("core", 5000, 4987, 13), "{}"))
	if err != nil {
		t.Fatal(err)
	}
	h := ParseHeaders(resp.Header, t0)
	if !h.Reset.Equal(reset) {
		t.Errorf("X-RateLimit-Reset = %v, want %v", h.Reset, reset)
	}
	h.Reset = time.Time{}
	want := Headers{Limit: 5000, Remaining: 4987, Used: 13, Resource: "core"}
	if h != want {
		t.Errorf("headers = %+v, want %+v", h, want)
	}

	// Nothing present: every number is -1 and nothing is invented.
	if h := ParseHeaders(http.Header{}, t0); h != (Headers{Limit: -1, Remaining: -1, Used: -1}) {
		t.Errorf("empty headers = %+v", h)
	}
	// Junk is not read as a number.
	bad := http.Header{}
	bad.Set("X-RateLimit-Remaining", "lots")
	bad.Set("X-RateLimit-Reset", "tomorrow")
	bad.Set("X-RateLimit-Limit", "-5")
	if h := ParseHeaders(bad, t0); h.Remaining != -1 || h.Limit != -1 || !h.Reset.IsZero() {
		t.Errorf("junk headers = %+v", h)
	}
}

func TestParseHeadersReadsRetryAfter(t *testing.T) {
	cases := []struct {
		value string
		want  time.Duration
	}{
		{"60", 60 * time.Second},
		{" 1 ", time.Second},
		{t0.Add(90 * time.Second).Format(http.TimeFormat), 90 * time.Second},
		{t0.Add(-time.Minute).Format(http.TimeFormat), 0}, // already past
		{"0", 0},
		{"-3", 0},
		{"soon", 0},
		{"", 0},
	}
	for _, tc := range cases {
		h := http.Header{}
		if tc.value != "" {
			h.Set("Retry-After", tc.value)
		}
		if got := ParseHeaders(h, t0).RetryAfter; got != tc.want {
			t.Errorf("Retry-After %q = %v, want %v", tc.value, got, tc.want)
		}
	}
}

func TestHeadersNameTheBucketTheOriginCharged(t *testing.T) {
	cases := []struct {
		resource string
		fallback governor.Bucket
		want     governor.Bucket
		ok       bool
	}{
		{"core", governor.BucketRESTCore, governor.BucketRESTCore, true},
		{"search", governor.BucketRESTSearch, governor.BucketRESTSearch, true},
		{"graphql", governor.BucketGraphQL, governor.BucketGraphQL, true},
		// The origin's answer is the truth when it differs from the caller's guess.
		{"search", governor.BucketRESTCore, governor.BucketRESTSearch, true},
		// No resource header: the caller's bucket.
		{"", governor.BucketRESTCore, governor.BucketRESTCore, true},
		// An allowance the governor does not meter is not fed in.
		{"code_search", governor.BucketRESTCore, "", false},
	}
	for _, tc := range cases {
		hd := Headers{Limit: 30, Remaining: 12, Used: 18, Reset: reset, Resource: tc.resource}
		b, ok := hd.Bucket(tc.fallback)
		if b != tc.want || ok != tc.ok {
			t.Errorf("resource %q: bucket %q ok %v, want %q %v", tc.resource, b, ok, tc.want, tc.ok)
		}
		r, ok := hd.Reading(tc.fallback)
		if ok != tc.ok {
			t.Errorf("resource %q: reading ok = %v", tc.resource, ok)
		}
		if ok && !sameReading(r, governor.Reading{Bucket: tc.want, Limit: 30, Remaining: 12, ResetAt: reset}) {
			t.Errorf("resource %q: reading = %+v", tc.resource, r)
		}
	}
	if _, ok := (Headers{Limit: -1, Remaining: -1}).Reading(governor.BucketRESTCore); ok {
		t.Error("headers without a remaining count produced a reading")
	}
}

func TestSecondaryLimitNeedsRetryAfterOnA403Or429(t *testing.T) {
	withWait := Headers{Remaining: 4000, RetryAfter: 60 * time.Second}
	for _, tc := range []struct {
		status int
		h      Headers
		want   bool
	}{
		{403, withWait, true},
		{429, withWait, true},
		{200, withWait, false}, // Retry-After on a success is not a limit
		{500, withWait, false},
		{403, Headers{Remaining: 0}, false}, // the primary limit: no Retry-After
		{429, Headers{Remaining: 4000}, false},
	} {
		d, ok := SecondaryLimit(tc.status, tc.h)
		if ok != tc.want || (ok && d != 60*time.Second) {
			t.Errorf("status %d %+v = %v %v, want %v", tc.status, tc.h, d, ok, tc.want)
		}
	}
}

func TestBucketForPicksTheBucketOfTheEndpoint(t *testing.T) {
	for args, want := range map[string]governor.Bucket{
		"repos/o/r/actions/runs":                  governor.BucketRESTCore,
		"search/issues -f q=is:pr":                governor.BucketRESTSearch,
		"/search/issues":                          governor.BucketRESTSearch,
		"--method GET search/issues":              governor.BucketRESTSearch,
		"-H Accept:application/json repos/o/r":    governor.BucketRESTCore,
		"repos/o/r/search/in/a/path/is/not/a/hit": governor.BucketRESTCore,
		"": governor.BucketRESTCore,
	} {
		if got := BucketFor(strings.Fields(args)); got != want {
			t.Errorf("BucketFor(%q) = %q, want %q", args, got, want)
		}
	}
}
