package serve

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/github"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/ratefeed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/vcs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
)

// scripted is a gh stand-in: each call answers the next scripted (stdout, err),
// the last one repeating, and records its argv.
type scripted struct {
	mu    sync.Mutex
	steps []step
	calls [][]string
}

type step struct {
	out string
	err error
}

func (s *scripted) Run(_ context.Context, args ...string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, append([]string(nil), args...))
	i := min(len(s.calls)-1, len(s.steps)-1)
	return []byte(s.steps[i].out), s.steps[i].err
}

func (s *scripted) argv(i int) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[i]
}

func (s *scripted) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

const okBody = `{"data":{"rateLimit":{"cost":1,"remaining":4000,"resetAt":"2999-01-01T00:00:00Z"}}}`

func response(status, headers, body string) string {
	return "HTTP/2.0 " + status + "\r\n" + headers + "\r\n" + body
}

func newTestGovernor(t *testing.T) *governor.Governor {
	t.Helper()
	g, err := governor.New(governor.Config{
		GraphQL:         governor.Limits{Cap: 100},
		RESTCore:        governor.Limits{Cap: 100},
		RESTSearch:      governor.Limits{Cap: 100},
		BackgroundShare: 0.9,
	}, governor.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return g
}

var noWait = github.RetryPolicy{
	MaxAttempts: 3,
	Backoff:     func(int) time.Duration { return time.Millisecond },
	Sleep:       func(context.Context, time.Duration) error { return nil },
}

func TestOriginAsksForHeadersAndReturnsTheBody(t *testing.T) {
	gh := &scripted{steps: []step{{out: response("200 OK", "X-Ratelimit-Remaining: 4000\r\n", okBody)}}}
	run := newOrigin(gh, newTestGovernor(t), nil, noWait)
	out, err := run.Run(context.Background(), "api", "graphql", "-f", "query=x")
	if err != nil || string(out) != okBody {
		t.Fatalf("got %q, %v; want the bare body", out, err)
	}
	if got := strings.Join(gh.argv(0), " "); got != "api --include graphql -f query=x" {
		t.Errorf("argv = %q", got)
	}
}

func TestOriginLeavesANonAPICallAlone(t *testing.T) {
	gh := &scripted{steps: []step{{out: "token\n"}}}
	out, err := newOrigin(gh, newTestGovernor(t), nil, noWait).Run(context.Background(), "auth", "token")
	if err != nil || string(out) != "token\n" || strings.Join(gh.argv(0), " ") != "auth token" {
		t.Errorf("got %q %v argv %v", out, err, gh.argv(0))
	}
}

func TestOriginSecondaryLimitPausesTheBackgroundAndIsNotAnAuthFailure(t *testing.T) {
	// gh calls every 403 an auth failure; the origin must see through that.
	authErr := errors.New("gh api: run `gh auth login`: " + vcs.ErrAuthInvalid.Error())
	authErr = errors.Join(vcs.ErrAuthInvalid, authErr)
	gh := &scripted{steps: []step{{
		out: response("403 Forbidden", "Retry-After: 120\r\n", `{"message":"secondary"}`),
		err: authErr,
	}}}
	gov := newTestGovernor(t)
	run := newOrigin(gh, gov, nil, noWait)

	_, err := run.Run(context.Background(), "api", "graphql", "-f", "query=x")
	var rl *ratefeed.RateLimitError
	if !errors.As(err, &rl) || !rl.Secondary || rl.RetryAfter != 120*time.Second {
		t.Fatalf("want a secondary *RateLimitError of 120s, got %v", err)
	}
	if errors.Is(err, vcs.ErrAuthInvalid) {
		t.Error("a rate limit must not read as an authentication failure")
	}
	if gh.count() != 1 {
		t.Errorf("a rate limit must not be retried, %d calls", gh.count())
	}
	b, _ := gov.State().Bucket(governor.BucketGraphQL)
	if !b.Paused || !strings.Contains(b.PauseReason, "secondary") {
		t.Errorf("the background must be paused: %+v", b)
	}
	if gov.Check(governor.Request{Bucket: governor.BucketGraphQL, Class: governor.ClassQuery, Cost: 1}) == nil {
		t.Error("a background class must be refused while paused")
	}
	if err := gov.Check(governor.Request{Bucket: governor.BucketGraphQL, Class: governor.ClassInteractive, Cost: 1}); err != nil {
		t.Errorf("an interactive read stays admitted: %v", err)
	}
}

func TestOriginPrimaryLimitFeedsTheReadingAndIsNotRetried(t *testing.T) {
	gh := &scripted{steps: []step{{
		out: response("403 Forbidden", "X-Ratelimit-Remaining: 0\r\nX-Ratelimit-Resource: graphql\r\nX-Ratelimit-Reset: 32503680000\r\n", `{}`),
		err: vcs.ErrAuthInvalid,
	}}}
	gov := newTestGovernor(t)
	_, err := newOrigin(gh, gov, nil, noWait).Run(context.Background(), "api", "graphql")
	var rl *ratefeed.RateLimitError
	if !errors.As(err, &rl) || rl.Secondary || errors.Is(err, vcs.ErrAuthInvalid) {
		t.Fatalf("want a primary *RateLimitError, got %v", err)
	}
	if gh.count() != 1 {
		t.Errorf("%d calls, want 1", gh.count())
	}
	b, _ := gov.State().Bucket(governor.BucketGraphQL)
	if b.Remaining != 0 {
		t.Errorf("the reading (remaining 0) must reach the governor: %+v", b)
	}
}

func TestOriginAForbiddenThatIsNotARateLimitStaysAnAuthFailure(t *testing.T) {
	want := errors.Join(vcs.ErrAuthInvalid, errors.New("SAML enforcement (HTTP 403)"))
	gh := &scripted{steps: []step{{out: response("403 Forbidden", "X-Ratelimit-Remaining: 4000\r\n", `{}`), err: want}}}
	_, err := newOrigin(gh, newTestGovernor(t), nil, noWait).Run(context.Background(), "api", "graphql")
	if !errors.Is(err, vcs.ErrAuthInvalid) {
		t.Errorf("got %v, want the auth failure to stand", err)
	}
}

type execLike struct{ msg string }

func (e execLike) Error() string { return e.msg }

func TestOriginRetriesATransientFailureThreeTimesInAll(t *testing.T) {
	bad := response("502 Bad Gateway", "", `{}`)
	fail := execLike{"gh api graphql: exit status 1: gh: Bad Gateway (HTTP 502)"}
	gh := &scripted{steps: []step{{out: bad, err: fail}, {out: bad, err: fail}, {out: response("200 OK", "", okBody)}}}
	out, err := newOrigin(gh, newTestGovernor(t), nil, noWait).Run(context.Background(), "api", "graphql")
	if err != nil || string(out) != okBody || gh.count() != 3 {
		t.Fatalf("got %q %v after %d calls, want the body after 3", out, err, gh.count())
	}

	gh = &scripted{steps: []step{{out: bad, err: fail}}}
	_, err = newOrigin(gh, newTestGovernor(t), nil, noWait).Run(context.Background(), "api", "graphql")
	if err == nil || gh.count() != 3 || !strings.Contains(err.Error(), "gave up after 3 attempts") {
		t.Errorf("a failure that persists ends after exactly 3 attempts: %d calls, %v", gh.count(), err)
	}
}

func TestOriginNoResponseLeavesGhsOwnError(t *testing.T) {
	gh := &scripted{steps: []step{{out: "", err: vcs.ErrAuthInvalid}}}
	_, err := newOrigin(gh, newTestGovernor(t), nil, noWait).Run(context.Background(), "api", "graphql")
	if !errors.Is(err, vcs.ErrAuthInvalid) || gh.count() != 1 {
		t.Errorf("got %v after %d calls", err, gh.count())
	}
}

func TestOriginTheRetryGuardStopsARetryThatWouldSpendBelowTheReserve(t *testing.T) {
	gov, err := governor.New(governor.Config{
		GraphQL:         governor.Limits{Cap: 100, Reserve: 50},
		RESTCore:        governor.Limits{Cap: 100},
		RESTSearch:      governor.Limits{Cap: 100},
		BackgroundShare: 0.9,
	}, governor.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := gov.Observe(governor.Reading{Bucket: governor.BucketGraphQL, Remaining: 10, ResetAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	bad := response("502 Bad Gateway", "", `{}`)
	gh := &scripted{steps: []step{{out: bad, err: execLike{"gh: Bad Gateway (HTTP 502)"}}}}
	_, err = newOrigin(gh, gov, nil, noWait).Run(context.Background(), "api", "graphql")
	var refusal *governor.Refusal
	if !errors.As(err, &refusal) || gh.count() != 1 {
		t.Errorf("the retry must be refused by the governor after 1 call, got %d calls: %v", gh.count(), err)
	}
}

func TestParseHooks(t *testing.T) {
	h, err := ParseHooks("viewer_interval=300ms, retry_delay=1s,poll_interval=50ms,migration_delay=2s,sweep_interval=1m")
	if err != nil {
		t.Fatal(err)
	}
	want := Hooks{
		ViewerInterval: 300 * time.Millisecond, RetryDelay: time.Second, PollInterval: 50 * time.Millisecond,
		MigrationDelay: 2 * time.Second, SweepInterval: time.Minute,
	}
	if h != want {
		t.Errorf("got %+v, want %+v", h, want)
	}
	if h, err := ParseHooks(""); err != nil || h != (Hooks{}) {
		t.Errorf("empty = %+v %v, want zero", h, err)
	}
	for _, bad := range []string{"nope=1s", "viewer_interval", "viewer_interval=fast", "viewer_interval=-1s", "viewer_interval=0s"} {
		if _, err := ParseHooks(bad); err == nil {
			t.Errorf("%q must be rejected", bad)
		}
	}
}

func TestCutQueryKey(t *testing.T) {
	for key, want := range map[string]struct {
		name, args string
		adhoc      bool
	}{
		"mine":                  {"mine", "", false},
		"mine?author=x&label=y": {"mine", "author=x&label=y", true},
		"":                      {"", "", false},
	} {
		name, args, adhoc := cutQueryKey(key)
		if name != want.name || args != want.args || adhoc != want.adhoc {
			t.Errorf("%q: got (%q, %q, %v)", key, name, args, adhoc)
		}
	}
}
