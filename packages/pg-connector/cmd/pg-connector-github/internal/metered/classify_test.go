package metered

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/github"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/ratefeed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// refusalFor returns a real *governor.Refusal of the given kind from h's governor.
func refusalFor(t *testing.T, h *harness, kind governor.RefusalKind) *governor.Refusal {
	t.Helper()
	b := governor.BucketGraphQL
	class := governor.ClassInteractive
	switch kind {
	case governor.RefusedCap:
		spend(h, b, governor.ClassInteractive, h.bucket(b).Cap)
	case governor.RefusedReserve:
		if err := h.gov.Observe(governor.Reading{Bucket: b, Remaining: 1000, ResetAt: reset}); err != nil {
			t.Fatal(err)
		}
	case governor.RefusedBackgroundShare:
		spend(h, b, governor.ClassQuery, h.bucket(b).BackgroundCap)
		class = governor.ClassKeepAlive
	case governor.RefusedPaused:
		// The reason of a pause caused by a rate limit carries the rate-limit words.
		h.gov.PauseAll(time.Minute, "secondary rate limit (HTTP 403, retry after 1m0s)")
		class = governor.ClassKeepAlive
	}
	_, err := h.gov.Admit(governor.Request{Bucket: b, Class: class, Cost: 1})
	var r *governor.Refusal
	if !errors.As(err, &r) || r.Kind != kind {
		t.Fatalf("wanted a %s refusal, got %v", kind, err)
	}
	return r
}

func TestClassifyTurnsARateLimitAnswerIntoASecondaryErrorAndAPause(t *testing.T) {
	for _, text := range []string{
		"gh api graphql: exit status 1: gh: You have exceeded a secondary rate limit (HTTP 403)",
		"gh api graphql: exit status 1: gh: API rate limit exceeded for user ID 1 (HTTP 403)",
		"gh api graphql: GraphQL: API rate limit already exceeded for user ID 1",
		"gh api graphql: exit status 1: gh: You have triggered an abuse detection mechanism (HTTP 403)",
		"GH: YOU HAVE EXCEEDED A SECONDARY RATE LIMIT",
		"gh: You have triggered an Abuse Detection mechanism",
	} {
		t.Run(text, func(t *testing.T) {
			h := newHarness(t, nil)
			// gh calls the HTTP 403 an authentication failure.
			in := fmt.Errorf("%s: run `gh auth login`: %w", text, github.ErrGHAuthInvalid)
			out := Classify(h.gov, 45*time.Second, in)
			var rl *ratefeed.RateLimitError
			if !errors.As(out, &rl) || !rl.Secondary || rl.RetryAfter != 45*time.Second {
				t.Fatalf("Classify = %v, want a secondary *ratefeed.RateLimitError for 45s", out)
			}
			if errors.Is(out, github.ErrGHAuthInvalid) {
				t.Error("the typed error wraps the authentication sentinel")
			}
			if !IsRateLimit(in) || !IsRateLimit(out) {
				t.Error("IsRateLimit does not recognize the answer or its typed form")
			}
			// Every bucket's background classes are paused for the fallback, once.
			if len(h.pauses) != 3 {
				t.Errorf("pause starts = %v, want one per bucket", h.pauses)
			}
			for _, b := range governor.Buckets() {
				bs := h.bucket(b)
				if !bs.Paused || !bs.PausedUntil.Equal(t0.Add(45*time.Second)) || !strings.Contains(bs.PauseReason, "rate limit") {
					t.Errorf("%s = %+v", b, bs)
				}
				// Classes 1 and 2 stay admitted; the background does not.
				if err := h.gov.Check(governor.Request{Bucket: b, Class: governor.ClassInvalidated, Cost: 1}); err != nil {
					t.Errorf("%s: class 2 refused during the pause: %v", b, err)
				}
				if err := h.gov.Check(governor.Request{Bucket: b, Class: governor.ClassQuery, Cost: 1}); !errors.Is(err, governor.ErrRefused) {
					t.Errorf("%s: class 3 admitted during the pause: %v", b, err)
				}
			}
		})
	}
}

func TestClassifyLeavesEverythingElseUntouchedAndPausesNothing(t *testing.T) {
	typed := &ratefeed.RateLimitError{StatusCode: 403, Secondary: true, RetryAfter: 30 * time.Second}
	cases := []struct {
		name string
		err  func(h *harness) error
	}{
		{"nil", func(*harness) error { return nil }},
		{"an HTTP 403 with another text stays an authentication failure", func(*harness) error {
			return forbiddenText("api", "graphql")
		}},
		{"an HTTP 401", func(*harness) error {
			return fmt.Errorf("gh api graphql: Bad credentials (HTTP 401): %w", github.ErrGHAuthInvalid)
		}},
		{"a transport failure", func(*harness) error { return errors.New("gh api graphql: error connecting to api.github.com") }},
		{"the rateLimit selection's own name is not the phrase", func(*harness) error {
			return errors.New("gh api graphql -F query={ rateLimit { cost } }: exit status 1")
		}},
		{"a missing PR", func(*harness) error { return errors.New("GraphQL: Could not resolve to a PullRequest (HTTP 404)") }},
		{"an already-typed secondary limit", func(*harness) error { return typed }},
		{"an already-typed limit inside another error", func(*harness) error { return fmt.Errorf("github: list comments: %w", typed) }},
		{"a paused-bucket refusal, whose text carries the rate-limit words", func(h *harness) error {
			return refusalFor(h.t, h, governor.RefusedPaused)
		}},
		{"a wrapped paused-bucket refusal", func(h *harness) error {
			return fmt.Errorf("retry of transient gh failure abandoned: %w", refusalFor(h.t, h, governor.RefusedPaused))
		}},
		{"a cap refusal", func(h *harness) error { return refusalFor(h.t, h, governor.RefusedCap) }},
		{"a reserve refusal", func(h *harness) error { return refusalFor(h.t, h, governor.RefusedReserve) }},
		{"a background share refusal", func(h *harness) error { return refusalFor(h.t, h, governor.RefusedBackgroundShare) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, nil)
			in := tc.err(h)
			pausesBefore := len(h.pauses)
			untilBefore := h.bucket(governor.BucketGraphQL).PausedUntil
			if got := Classify(h.gov, time.Hour, in); got != in {
				t.Errorf("Classify(%v) = %v, want the same error", in, got)
			}
			if len(h.pauses) != pausesBefore {
				t.Errorf("Classify paused: %v", h.pauses[pausesBefore:])
			}
			if !h.bucket(governor.BucketGraphQL).PausedUntil.Equal(untilBefore) {
				t.Error("Classify moved a pause")
			}
		})
	}
}

func TestClassifyDefaultsAZeroFallbackAndToleratesNoGovernor(t *testing.T) {
	in := errors.New("gh: You have exceeded a secondary rate limit")
	h := newHarness(t, nil)
	var rl *ratefeed.RateLimitError
	if out := Classify(h.gov, 0, in); !errors.As(out, &rl) || rl.RetryAfter != DefaultFallbackPause || DefaultFallbackPause != 60*time.Second {
		t.Errorf("Classify with no fallback = %v, want the default of 60s", out)
	}
	if bs := h.bucket(governor.BucketRESTCore); !bs.PausedUntil.Equal(t0.Add(DefaultFallbackPause)) {
		t.Errorf("paused until %v, want the default fallback", bs.PausedUntil)
	}
	if out := Classify(nil, time.Minute, in); !errors.As(out, &rl) {
		t.Errorf("Classify without a governor = %v, want the typed error", out)
	}
	// The Runner's own zero FallbackPause is the default too.
	h2 := newHarness(t, func(c innerCall) ([]byte, error) { return nil, secondaryLimitText(c.args...) })
	_, err := h2.runner.Run(WithCall(context.Background(), ratefeed.Meta{Class: governor.ClassInteractive}), "pr", "view", "1")
	if !errors.As(err, &rl) || rl.RetryAfter != DefaultFallbackPause {
		t.Errorf("a Runner with no FallbackPause: err = %v", err)
	}
}

func TestIsRateLimit(t *testing.T) {
	h := newHarness(t, nil)
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"secondary text", errors.New("gh: You have exceeded a secondary rate limit"), true},
		{"primary text", errors.New("gh: API rate limit exceeded for user ID 1"), true},
		{"abuse detection", errors.New("gh: abuse detection mechanism"), true},
		{"mixed case", errors.New("Gh: RATE LIMIT"), true},
		{"a typed secondary error", &ratefeed.RateLimitError{Secondary: true, RetryAfter: time.Minute}, true},
		{"a typed primary error", &ratefeed.RateLimitError{StatusCode: 403, ResetAt: reset, Resource: "core"}, true},
		{"a wrapped typed error", fmt.Errorf("github: list: %w", &ratefeed.RateLimitError{}), true},
		{"an ordinary 403", forbiddenText("api", "graphql"), false},
		{"a transport failure", errors.New("error connecting to api.github.com"), false},
		{"the rateLimit selection", errors.New("query { rateLimit { cost } }"), false},
		{"a paused-bucket refusal, whose text names a rate limit", refusalFor(t, h, governor.RefusedPaused), false},
		{"a wrapped paused-bucket refusal", fmt.Errorf("giving up: %w", refusalFor(t, newHarness(t, nil), governor.RefusedPaused)), false},
	}
	for _, tc := range cases {
		if got := IsRateLimit(tc.err); got != tc.want {
			t.Errorf("%s: IsRateLimit = %v, want %v", tc.name, got, tc.want)
		}
	}
	if !strings.Contains(strings.ToLower(refusalFor(t, newHarness(t, nil), governor.RefusedPaused).Error()), "rate limit") {
		t.Error("the paused refusal this test relies on no longer names the rate limit")
	}
}

func TestIsNotFound(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{nil, false},
		{errors.New("gh pr view 999999999: GraphQL: Could not resolve to a PullRequest with the number of 999999999. (repository.pullRequest)"), true},
		{errors.New("gh api repos/o/r/issues/1/comments: gh: Not Found (HTTP 404)"), true},
		{errors.New("gh run view 9 --log: failed to get run: HTTP 404: Not Found"), true},
		{errors.New("gh: COULD NOT RESOLVE TO A Repository"), true},
		{fmt.Errorf("github: wrapped: %w", errors.New("HTTP 404")), true},
		{fmt.Errorf("gh: Bad credentials (HTTP 401): %w", github.ErrGHAuthInvalid), false},
		{errors.New("gh: Resource not accessible by integration (HTTP 403)"), false},
		{errors.New("gh: Server Error (HTTP 500)"), false},
		{errors.New("error connecting to api.github.com"), false},
	}
	for _, tc := range cases {
		if got := IsNotFound(tc.err); got != tc.want {
			t.Errorf("IsNotFound(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
}

// The seven values of pkg/scriptout/errors.go, which the daemon's wire errors stay inside.
var wireCodes = []string{
	"not_found", "unauthenticated", "unavailable", "unknown_op", "version_mismatch", "invalid_argument", "query_not_recognized",
}

func TestWireErrorMapsOntoTheSevenValueTaxonomyOnly(t *testing.T) {
	h := newHarness(t, nil)
	authFailure := fmt.Errorf("gh api graphql: Bad credentials (HTTP 401): run `gh auth login`: %w", github.ErrGHAuthInvalid)
	transport := errors.New("gh api graphql: error connecting to api.github.com")
	alreadyWire := scriptout.WrapError(scriptout.ErrInvalidArgument, "the PR id is malformed")
	cases := []struct {
		name string
		err  error
		code string
		// text is a substring the wire message must carry, naming the cap, the
		// reserve, the pause or the limit.
		text string
		// same is true when the error must pass through unchanged.
		same bool
	}{
		{name: "an authentication failure", err: authFailure, code: "unauthenticated", text: "Bad credentials"},
		{name: "an HTTP 403 that is not a rate limit", err: forbiddenText("api", "graphql"), code: "unauthenticated", text: "not accessible"},
		{name: "a missing PR (GraphQL)", err: errors.New("gh pr view 9: GraphQL: Could not resolve to a PullRequest with the number of 9."), code: "not_found", text: "Could not resolve"},
		{name: "a missing resource (REST)", err: errors.New("gh api repos/o/r/issues/1/comments: gh: Not Found (HTTP 404)"), code: "not_found", text: "HTTP 404"},
		{name: "a cap refusal", err: refusalFor(t, h, governor.RefusedCap), code: "unavailable", text: "cap reached"},
		{name: "a reserve refusal", err: refusalFor(t, newHarness(t, nil), governor.RefusedReserve), code: "unavailable", text: "below reserve"},
		{name: "a background share refusal", err: refusalFor(t, newHarness(t, nil), governor.RefusedBackgroundShare), code: "unavailable", text: "background share"},
		{name: "a pause refusal", err: refusalFor(t, newHarness(t, nil), governor.RefusedPaused), code: "unavailable", text: "paused until"},
		{name: "a wrapped refusal", err: fmt.Errorf("github: list review threads: %w", refusalFor(t, newHarness(t, nil), governor.RefusedCap)), code: "unavailable", text: "cap reached"},
		{name: "a typed secondary limit", err: &ratefeed.RateLimitError{StatusCode: 403, Secondary: true, RetryAfter: time.Minute}, code: "unavailable", text: "retry after 1m0s"},
		{name: "a typed primary limit", err: &ratefeed.RateLimitError{StatusCode: 403, ResetAt: reset, Resource: "core"}, code: "unavailable", text: "resets at 2026-10-12T12:30:00Z"},
		{name: "a rate limit gh called an authentication failure", err: secondaryLimitText("api", "graphql"), code: "unavailable", text: "secondary rate limit"},
		{name: "abuse detection", err: errors.New("gh: abuse detection mechanism (HTTP 403)"), code: "unavailable", text: "abuse detection"},
		{name: "a transport failure passes through for scriptout's own fallback", err: transport, code: "unavailable", same: true},
		{name: "an error already on the taxonomy passes through", err: alreadyWire, code: "invalid_argument", same: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := WireError(tc.err)
			if tc.same && got != tc.err {
				t.Errorf("WireError(%v) = %v, want it unchanged", tc.err, got)
			}
			code := scriptout.CodeForError(got)
			if code != tc.code {
				t.Errorf("code = %q, want %q (err %v)", code, tc.code, got)
			}
			if !slices.Contains(wireCodes, code) || scriptout.ExitCodeForCode(code) == 0 {
				t.Errorf("code %q is outside the seven-value taxonomy", code)
			}
			if tc.text != "" && !strings.Contains(got.Error(), tc.text) {
				t.Errorf("message %q does not name %q", got, tc.text)
			}
			if !tc.same && !errors.Is(got, wireSentinel(tc.code)) {
				t.Errorf("errors.Is(%v, the %s sentinel) is false", got, tc.code)
			}
		})
	}
	if WireError(nil) != nil {
		t.Error("WireError(nil) is not nil")
	}
}

func wireSentinel(code string) error {
	switch code {
	case "not_found":
		return scriptout.ErrNotFound
	case "unauthenticated":
		return scriptout.ErrUnauthenticated
	default:
		return scriptout.ErrUnavailable
	}
}

// The mapping keeps what it came from in the error chain: a caller can still read
// the refusal behind an unavailable, and the daemon's restart-to-refresh
// escalation can still see the authentication failure behind an unauthenticated.
func TestWireErrorKeepsTheOriginalErrorInItsChain(t *testing.T) {
	refusal := refusalFor(t, newHarness(t, nil), governor.RefusedCap)
	var got *governor.Refusal
	if !errors.As(WireError(refusal), &got) || got != refusal {
		t.Error("the refusal is no longer reachable from its unavailable")
	}
	auth := fmt.Errorf("gh: Bad credentials (HTTP 401): %w", github.ErrGHAuthInvalid)
	if !errors.Is(WireError(auth), github.ErrGHAuthInvalid) {
		t.Error("the authentication sentinel is no longer reachable from its unauthenticated")
	}
	typed := &ratefeed.RateLimitError{Secondary: true, RetryAfter: time.Minute}
	var rl *ratefeed.RateLimitError
	if !errors.As(WireError(typed), &rl) || rl != typed {
		t.Error("the typed rate-limit error is no longer reachable from its unavailable")
	}
	// A rate limit is never an authentication failure on the wire, whichever way it
	// reached the mapping.
	for _, err := range []error{secondaryLimitText("api", "graphql"), &ratefeed.RateLimitError{Secondary: true}} {
		if errors.Is(WireError(err), scriptout.ErrUnauthenticated) {
			t.Errorf("%v became unauthenticated", err)
		}
	}
}

// isRateLimitText is a local copy of the rule in internal/github/review_write.go,
// which this package may not import and does not change. The two must not drift.
func TestIsRateLimitTextMatchesTheGithubPackage(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "github", "review_write.go"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?s)func isRateLimitText\(lower string\) bool \{(.*?)\n\}`).FindSubmatch(src)
	if m == nil {
		t.Fatal("github.isRateLimitText was not found in review_write.go: update this guard with it")
	}
	var theirs []string
	for _, lit := range regexp.MustCompile(`"([^"]*)"`).FindAllSubmatch(m[1], -1) {
		theirs = append(theirs, string(lit[1]))
	}
	slices.Sort(theirs)
	if want := []string{"abuse detection", "rate limit"}; !slices.Equal(theirs, want) {
		t.Fatalf("github.isRateLimitText matches %q, but metered.isRateLimitText matches %q: bring them back together", theirs, want)
	}
	for _, tc := range []struct {
		lower string
		want  bool
	}{
		{"rate limit", true},
		{"you have exceeded a secondary rate limit.", true},
		{"abuse detection mechanism", true},
		{"ratelimit", false},
		{"rate-limit", false},
		{"ratelimit { cost }", false},
		{"", false},
	} {
		if got := isRateLimitText(tc.lower); got != tc.want {
			t.Errorf("isRateLimitText(%q) = %v, want %v", tc.lower, got, tc.want)
		}
	}
}
