// ratelimit.go: the rate-limit reading pjira conveys on stderr.
//
// For every HTTP response that carries at least one Atlassian rate-limit
// header, pjira writes one stderr line: the literal prefix "pjira-ratelimit: "
// and then ONE compact JSON object. Stdout is unchanged and any other stderr
// line is ordinary pjira output. The object's keys are status (always), limit,
// remaining, near_limit, retry_after_seconds, reset and reason; every key but
// status is omitted when its header is absent or unparsable, so an absent key
// is never read as zero. A pjira run makes more than one request for a paged
// search, so it can write more than one line; the LAST is the freshest. A
// tenant that sends none of the headers, or a pjira that predates the lines,
// produces no line, and that is "no reading", never a failure.
//
// The format is owned by pjira (its README's "Rate-limit headers"); this file
// only reads it.
package internal

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
)

// RateLimitPrefix is the literal prefix of a rate-limit stderr line.
const RateLimitPrefix = "pjira-ratelimit: "

// RateLimit is one parsed reading. A nil pointer, an empty string or a zero
// RetryAfterSeconds is a key the line did not carry.
type RateLimit struct {
	// Status is the HTTP status of the response that carried the headers.
	Status int `json:"status"`
	// Limit is X-RateLimit-Limit: requests per burst window (one second).
	Limit *int64 `json:"limit,omitempty"`
	// Remaining is X-RateLimit-Remaining: requests left in the burst window.
	Remaining *int64 `json:"remaining,omitempty"`
	// NearLimit is X-RateLimit-NearLimit: true below 20% of capacity.
	NearLimit *bool `json:"near_limit,omitempty"`
	// RetryAfterSeconds is Retry-After, a MINIMUM delay, on a 429 and some 5xx.
	RetryAfterSeconds int64 `json:"retry_after_seconds,omitempty"`
	// Reset is X-RateLimit-Reset verbatim (a 429 carries it).
	Reset string `json:"reset,omitempty"`
	// Reason is RateLimit-Reason verbatim (a 429 carries it).
	Reason string `json:"reason,omitempty"`
}

// ParseRateLimit returns the freshest reading in stderr: the last line that
// carries the prefix and decodes as a JSON object with a status. A line that
// does not decode is skipped (a later or earlier good line still counts), and
// ok is false when no line qualifies, which is the normal answer for a pjira
// that predates the lines or a tenant that sends no headers.
func ParseRateLimit(stderr []byte) (rl RateLimit, ok bool) {
	for _, line := range bytes.Split(stderr, []byte("\n")) {
		s := strings.TrimRight(string(line), "\r")
		rest, found := strings.CutPrefix(s, RateLimitPrefix)
		if !found {
			continue
		}
		var cand RateLimit
		if err := json.Unmarshal([]byte(rest), &cand); err != nil || cand.Status == 0 {
			continue
		}
		rl, ok = cand, true
	}
	return rl, ok
}

// StripRateLimitLines returns stderr without its rate-limit lines, so a failure
// message folds the real error text and the cap is not spent on readings.
func StripRateLimitLines(stderr []byte) []byte {
	if !bytes.Contains(stderr, []byte(RateLimitPrefix)) {
		return stderr
	}
	var out bytes.Buffer
	for _, line := range bytes.SplitAfter(stderr, []byte("\n")) {
		if bytes.HasPrefix(line, []byte(RateLimitPrefix)) {
			continue
		}
		out.Write(line)
	}
	return out.Bytes()
}

type rateLimitSinkKey struct{}

// WithRateLimitSink returns ctx carrying a sink that CLIRunner.Run calls with
// the freshest reading of each run that produced one (success or failure). A
// runner that execs no process (a test fake) simply never calls it.
func WithRateLimitSink(ctx context.Context, sink func(RateLimit)) context.Context {
	return context.WithValue(ctx, rateLimitSinkKey{}, sink)
}

func rateLimitSinkFrom(ctx context.Context) func(RateLimit) {
	s, _ := ctx.Value(rateLimitSinkKey{}).(func(RateLimit))
	return s
}

// ReportRateLimit hands rl to the sink in ctx, if any. CLIRunner calls it with
// what it parsed; a test fake of the Runner calls it to play a response that
// carried a reading.
func ReportRateLimit(ctx context.Context, rl RateLimit) {
	if s := rateLimitSinkFrom(ctx); s != nil {
		s(rl)
	}
}
