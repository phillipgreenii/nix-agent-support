package github

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// Synthetic transient and non-transient gh failure texts. None names a real
// host account, org or repository.
const (
	errConnect = "gh api graphql: exit status 1: error connecting to api.github.com\n" +
		"check your internet connection or https://githubstatus.com"
	errBadGateway = "gh: Bad Gateway (HTTP 502)"
	errUnavail    = "gh: Service Unavailable (HTTP 503)"
	errGateway    = "gh: Gateway Timeout (HTTP 504)"

	okRateLimit = `{"data":{"rateLimit":{"remaining":4000,"resetAt":"2030-01-01T00:00:00Z"}}}`
	okSearch    = `{"data":{"search":{"nodes":[{"number":7,"title":"t","url":"https://example.invalid/acme/widgets/pull/7","repository":{"nameWithOwner":"acme/widgets"}}],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}`
)

type scripted struct {
	out []byte
	err error
}

// scriptedGH answers successive Run/RunStdin calls from a script; the last
// entry repeats. It records every call's args.
type scriptedGH struct {
	script []scripted
	calls  [][]string
}

func (s *scriptedGH) next(args []string) ([]byte, error) {
	s.calls = append(s.calls, append([]string(nil), args...))
	i := len(s.calls) - 1
	if i >= len(s.script) {
		i = len(s.script) - 1
	}
	return s.script[i].out, s.script[i].err
}

func (s *scriptedGH) Run(_ context.Context, args ...string) ([]byte, error) { return s.next(args) }
func (s *scriptedGH) RunStdin(_ context.Context, _ []byte, args ...string) ([]byte, error) {
	return s.next(args)
}

// fakeSleeper records requested backoff delays without sleeping.
type fakeSleeper struct{ delays []time.Duration }

func (f *fakeSleeper) sleep(_ context.Context, d time.Duration) error {
	f.delays = append(f.delays, d)
	return nil
}

func testPolicy(sl *fakeSleeper) RetryPolicy {
	return RetryPolicy{
		MaxAttempts:    3,
		Backoff:        func(retry int) time.Duration { return time.Duration(retry) * time.Second },
		MinRetryWindow: 0,
		Sleep:          sl.sleep,
	}
}

func retryProvider(gh *scriptedGH, sl *fakeSleeper) *Provider {
	return NewWithRunner(gh).WithRetryPolicy(testPolicy(sl))
}

func transient(msg string) scripted { return scripted{err: errors.New(msg)} }
func okOut(s string) scripted       { return scripted{out: []byte(s)} }

func TestDefaultRetryPolicy_IsSmallAndBounded(t *testing.T) {
	rp := DefaultRetryPolicy()
	if rp.MaxAttempts != 3 {
		t.Errorf("MaxAttempts = %d, want 3", rp.MaxAttempts)
	}
	if got := rp.Backoff(1); got != 500*time.Millisecond {
		t.Errorf("Backoff(1) = %v, want 500ms", got)
	}
	if got := rp.Backoff(2); got != time.Second {
		t.Errorf("Backoff(2) = %v, want 1s", got)
	}
	if rp.MinRetryWindow <= 0 {
		t.Errorf("MinRetryWindow = %v, want > 0", rp.MinRetryWindow)
	}
}

func TestNew_UsesDefaultRetry_NewWithRunnerDoesNot(t *testing.T) {
	if got := New().retry.MaxAttempts; got != DefaultRetryPolicy().MaxAttempts {
		t.Errorf("New() MaxAttempts = %d, want the default %d", got, DefaultRetryPolicy().MaxAttempts)
	}
	// NewWithRunner is the test seam: no retry unless a test opts in, so the
	// pre-existing error-path tests keep their one-call semantics.
	if got := NewWithRunner(&scriptedGH{script: []scripted{okOut("[]")}}).retry.MaxAttempts; got > 1 {
		t.Errorf("NewWithRunner MaxAttempts = %d, want no retry", got)
	}
}

func TestSleepCtx_ReturnsPromptlyOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if err := sleepCtx(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Errorf("sleepCtx err = %v, want context.Canceled", err)
	}
	if time.Since(start) > time.Second {
		t.Errorf("sleepCtx did not return promptly on cancel")
	}
}

func TestRead_TransientThenSuccess(t *testing.T) {
	for name, first := range map[string]scripted{
		"connect":     transient(errConnect),
		"bad gateway": transient(errBadGateway),
		"unavailable": transient(errUnavail),
		"gateway":     transient(errGateway),
		"truncated":   okOut(`{"data":{"search":{"nodes":[{"num`),
		"empty":       okOut(""),
		"gh exit 1 truncated body": {err: &ghExecError{
			msg:    "gh search prs: exit status 1: unexpected end of JSON input",
			stderr: "unexpected end of JSON input",
			err:    errors.New("exit status 1"),
		}},
	} {
		t.Run(name, func(t *testing.T) {
			gh := &scriptedGH{script: []scripted{first, okOut(okSearch)}}
			sl := &fakeSleeper{}
			prs, err := retryProvider(gh, sl).SearchPRsEnriched(context.Background(), "is:open")
			if err != nil {
				t.Fatalf("SearchPRsEnriched: %v", err)
			}
			if len(prs) != 1 || prs[0].Number != 7 {
				t.Errorf("prs = %+v", prs)
			}
			if len(gh.calls) != 2 {
				t.Errorf("gh calls = %d, want 2", len(gh.calls))
			}
			if len(sl.delays) != 1 || sl.delays[0] != time.Second {
				t.Errorf("backoff delays = %v, want [1s]", sl.delays)
			}
		})
	}
}

// A persistent transient failure exhausts the bounded attempts, then surfaces
// as the SAME unclassified error it always did (wire code "unavailable") with
// the diagnostic stderr tail still present.
func TestRead_PersistentTransientExhaustsAttemptsKeepingTail(t *testing.T) {
	gh := &scriptedGH{script: []scripted{transient(errConnect)}}
	sl := &fakeSleeper{}
	_, err := retryProvider(gh, sl).SearchPRsEnriched(context.Background(), "is:open")
	if err == nil {
		t.Fatal("expected error")
	}
	if len(gh.calls) != 3 {
		t.Errorf("gh calls = %d, want 3 (MaxAttempts)", len(gh.calls))
	}
	if len(sl.delays) != 2 {
		t.Errorf("sleeps = %v, want 2 (between 3 attempts)", sl.delays)
	}
	if !strings.Contains(err.Error(), "error connecting to api.github.com") {
		t.Errorf("diagnostic tail lost: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "3 attempts") {
		t.Errorf("attempt count not reported: %q", err.Error())
	}
	if code := scriptout.ErrorResponse(err).Error.Code; code != "unavailable" {
		t.Errorf("error code = %q, want unavailable", code)
	}
}

func TestRead_PersistentTruncatedJSONSurfacesParseError(t *testing.T) {
	gh := &scriptedGH{script: []scripted{okOut(`{"data":{"sea`)}}
	sl := &fakeSleeper{}
	_, err := retryProvider(gh, sl).SearchPRsEnriched(context.Background(), "is:open")
	if err == nil || !strings.Contains(err.Error(), "unexpected end of JSON input") {
		t.Fatalf("err = %v, want the parse error naming 'unexpected end of JSON input'", err)
	}
	if len(gh.calls) != 3 {
		t.Errorf("gh calls = %d, want 3", len(gh.calls))
	}
	if code := scriptout.ErrorResponse(err).Error.Code; code != "unavailable" {
		t.Errorf("error code = %q, want unavailable", code)
	}
}

func TestRead_NonTransientNotRetried(t *testing.T) {
	cases := map[string]error{
		"auth":         fmt.Errorf("gh x: bad: %w", ErrGHAuthInvalid),
		"not found":    errors.New("gh: Not Found (HTTP 404)"),
		"forbidden":    errors.New("gh: Resource not accessible (HTTP 403)"),
		"unauthorized": errors.New("gh: Bad credentials (HTTP 401)"),
		"too many":     errors.New("gh: API rate limit exceeded (HTTP 429)"),
		"graphql rate": errors.New("gh: GraphQL: API rate limit already exceeded for user ID 1"),
		"no resolve":   errors.New("GraphQL: Could not resolve to a PullRequest with the number of 9. (repository.pullRequest)"),
		"unrelated":    errors.New("gh: exit status 1: unknown flag --nope"),
	}
	for name, e := range cases {
		t.Run(name, func(t *testing.T) {
			gh := &scriptedGH{script: []scripted{{err: e}}}
			sl := &fakeSleeper{}
			_, err := retryProvider(gh, sl).SearchPRsEnriched(context.Background(), "is:open")
			if err == nil {
				t.Fatal("expected error")
			}
			if len(gh.calls) != 1 {
				t.Errorf("gh calls = %d, want 1 (no retry)", len(gh.calls))
			}
			if len(sl.delays) != 0 {
				t.Errorf("slept %v despite non-transient error", sl.delays)
			}
			if err.Error() != e.Error() {
				t.Errorf("non-transient error was rewritten: %q", err.Error())
			}
		})
	}
}

// Only the stderr is classified: a gh command line that merely CONTAINS a
// non-transient-looking phrase must not suppress a retry of a connect error.
func TestRead_ClassifiesStderrNotCommandLine(t *testing.T) {
	e := &ghExecError{
		msg:    "gh search prs -- label:\"rate limit\" http 404: exit status 1: error connecting to api.github.com",
		stderr: "error connecting to api.github.com",
		err:    errors.New("exit status 1"),
	}
	if !isTransientGHError(context.Background(), e) {
		t.Errorf("connect failure not classified transient when the command line mentions 'rate limit'/'http 404'")
	}
}

func TestRead_NoRetryWhenContextDone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	gh := &scriptedGH{script: []scripted{transient(errConnect)}}
	sl := &fakeSleeper{}
	if _, err := retryProvider(gh, sl).GetPR(ctx, "acme/widgets", 7); err == nil {
		t.Fatal("expected error")
	}
	if len(gh.calls) != 1 {
		t.Errorf("gh calls = %d, want 1", len(gh.calls))
	}
}

// The retry loop stays inside the per-call budget: a retry is not started
// unless backoff plus MinRetryWindow still fits before the deadline.
func TestRead_NoRetryWithoutTimeBudget(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	gh := &scriptedGH{script: []scripted{transient(errConnect)}}
	sl := &fakeSleeper{}
	rp := testPolicy(sl)
	rp.MinRetryWindow = 5 * time.Second
	_, err := NewWithRunner(gh).WithRetryPolicy(rp).GetPR(ctx, "acme/widgets", 7)
	if err == nil {
		t.Fatal("expected error")
	}
	if len(gh.calls) != 1 {
		t.Errorf("gh calls = %d, want 1 (no time left for a retry)", len(gh.calls))
	}
	if !strings.Contains(err.Error(), "error connecting to api.github.com") {
		t.Errorf("original diagnostic lost: %q", err.Error())
	}
}

func TestRead_BackoffSleepCancelledStopsRetrying(t *testing.T) {
	gh := &scriptedGH{script: []scripted{transient(errConnect)}}
	rp := testPolicy(&fakeSleeper{})
	rp.Sleep = func(context.Context, time.Duration) error { return context.Canceled }
	_, err := NewWithRunner(gh).WithRetryPolicy(rp).GetPR(context.Background(), "acme/widgets", 7)
	if err == nil || !strings.Contains(err.Error(), "error connecting to api.github.com") {
		t.Fatalf("err = %v, want the original connect diagnostic", err)
	}
	if len(gh.calls) != 1 {
		t.Errorf("gh calls = %d, want 1", len(gh.calls))
	}
}

func TestRead_AllReadOpsRetry(t *testing.T) {
	type op struct {
		name string
		ok   string
		run  func(p *Provider) error
	}
	ctx := context.Background()
	ops := []op{
		{"GetPR", `{"number":7}`, func(p *Provider) error { _, e := p.GetPR(ctx, "acme/widgets", 7); return e }},
		{"GetFiles", `{"files":[]}`, func(p *Provider) error { _, e := p.GetFiles(ctx, "acme/widgets", 7); return e }},
		{"GetCommits", `{"commits":[]}`, func(p *Provider) error { _, e := p.GetCommits(ctx, "acme/widgets", 7); return e }},
		{"ListReviews", `{"reviews":[]}`, func(p *Provider) error { _, e := p.ListReviews(ctx, "acme/widgets", 7); return e }},
		{"ListComments", `[]`, func(p *Provider) error { _, e := p.ListComments(ctx, "acme/widgets", 7); return e }},
		{"SearchPRs", `[]`, func(p *Provider) error { _, e := p.SearchPRs(ctx, "is:open"); return e }},
		{"ListMyPRs", `[]`, func(p *Provider) error { _, e := p.ListMyPRs(ctx, "acme/widgets"); return e }},
		{"ReadRateLimit", okRateLimit, func(p *Provider) error { _, e := p.ReadRateLimit(ctx); return e }},
	}
	for _, o := range ops {
		t.Run(o.name, func(t *testing.T) {
			gh := &scriptedGH{script: []scripted{transient(errBadGateway), okOut(o.ok)}}
			if err := o.run(retryProvider(gh, &fakeSleeper{})); err != nil {
				t.Fatalf("%s: %v", o.name, err)
			}
			// ListComments issues two reads (issue + review comments); the first
			// one fails once and is retried, the second succeeds first time.
			want := 2
			if o.name == "ListComments" {
				want = 3
			}
			if len(gh.calls) != want {
				t.Errorf("%s: gh calls = %d, want %d", o.name, len(gh.calls), want)
			}
		})
	}
}

// ListComments' --paginate reads legitimately answer empty output (no
// comments); that must not be mistaken for a truncated response.
func TestRead_PaginatedEmptyOutputIsNotRetried(t *testing.T) {
	gh := &scriptedGH{script: []scripted{okOut("")}}
	if _, err := retryProvider(gh, &fakeSleeper{}).ListComments(context.Background(), "acme/widgets", 7); err != nil {
		t.Fatalf("ListComments: %v", err)
	}
	if len(gh.calls) != 2 {
		t.Errorf("gh calls = %d, want 2 (one per endpoint, no retry)", len(gh.calls))
	}
}

// Concatenated JSON arrays (gh --paginate output) are a valid stream; a cut
// stream is not.
func TestValidJSONStream(t *testing.T) {
	for in, want := range map[string]bool{
		`{"a":1}`:     true,
		`[1][2]`:      true,
		"[1]\n[2]\n":  true,
		`[1][2`:       false,
		`{"a":`:       false,
		``:            false,
		"  \n":        false,
		`not json`:    false,
		`{"a":1}{"b"`: false,
	} {
		if got := validJSONStream([]byte(in), false); got != want {
			t.Errorf("validJSONStream(%q) = %v, want %v", in, got, want)
		}
	}
	if !validJSONStream(nil, true) {
		t.Error("empty output must be valid when allowEmpty")
	}
}

// Writes are never retried, however transient the failure looks: a repeated
// POST could duplicate a comment or re-run a merge.
func TestWrites_NeverRetried(t *testing.T) {
	ctx := context.Background()
	writes := map[string]func(p *Provider) error{
		"AddComment": func(p *Provider) error { _, e := p.AddComment(ctx, "acme/widgets", 7, "hello"); return e },
		"Merge":      func(p *Provider) error { return p.Merge(ctx, "acme/widgets", 7) },
		"Close":      func(p *Provider) error { return p.Close(ctx, "acme/widgets", 7) },
		"SetDraft":   func(p *Provider) error { return p.SetDraft(ctx, "acme/widgets", 7, true) },
		"CheckAuth":  func(p *Provider) error { return p.CheckAuth(ctx) },
	}
	for name, run := range writes {
		t.Run(name, func(t *testing.T) {
			gh := &scriptedGH{script: []scripted{transient(errConnect)}}
			sl := &fakeSleeper{}
			if err := run(retryProvider(gh, sl)); err == nil {
				t.Fatal("expected error")
			}
			if len(gh.calls) != 1 {
				t.Errorf("%s: gh calls = %d, want exactly 1", name, len(gh.calls))
			}
			if len(sl.delays) != 0 {
				t.Errorf("%s: slept %v", name, sl.delays)
			}
		})
	}
}

// The reserve guard runs before EVERY retry (never before the first attempt,
// which the caller already guarded) and a refusal stops the retrying.
func TestRead_GuardRunsBeforeEachRetry(t *testing.T) {
	gh := &scriptedGH{script: []scripted{transient(errConnect), transient(errConnect), okOut(okSearch)}}
	var guarded int
	p := retryProvider(gh, &fakeSleeper{})
	p.SetRetryGuard(func(context.Context) error { guarded++; return nil })
	if _, err := p.SearchPRsEnriched(context.Background(), "is:open"); err != nil {
		t.Fatalf("SearchPRsEnriched: %v", err)
	}
	if guarded != 2 {
		t.Errorf("guard calls = %d, want 2 (one per retry)", guarded)
	}
}

func TestRead_GuardRefusalStopsRetrying(t *testing.T) {
	gh := &scriptedGH{script: []scripted{transient(errConnect), okOut(okSearch)}}
	refused := scriptout.WrapError(scriptout.ErrUnavailable, "GraphQL rate limit remaining (10) is below the configured reserve (1000)")
	p := retryProvider(gh, &fakeSleeper{})
	p.SetRetryGuard(func(context.Context) error { return refused })
	_, err := p.SearchPRsEnriched(context.Background(), "is:open")
	if !errors.Is(err, scriptout.ErrUnavailable) || !strings.Contains(err.Error(), "below the configured reserve") {
		t.Fatalf("err = %v, want the guard's verdict", err)
	}
	if len(gh.calls) != 1 {
		t.Errorf("gh calls = %d, want 1 (guard refused the retry)", len(gh.calls))
	}
	if !strings.Contains(err.Error(), "error connecting to api.github.com") {
		t.Errorf("original failure not reported alongside the guard verdict: %q", err.Error())
	}
}

// ReadRateLimit is what the guard itself calls: it must not invoke the guard
// (that would recurse) yet is still retried on a transient failure.
func TestReadRateLimit_RetriesWithoutInvokingGuard(t *testing.T) {
	gh := &scriptedGH{script: []scripted{transient(errConnect), okOut(okRateLimit)}}
	p := retryProvider(gh, &fakeSleeper{})
	p.SetRetryGuard(func(context.Context) error { t.Fatal("guard invoked from ReadRateLimit"); return nil })
	rl, err := p.ReadRateLimit(context.Background())
	if err != nil || rl.Remaining != 4000 {
		t.Fatalf("ReadRateLimit = %+v, %v", rl, err)
	}
}

func TestIsTransientGHError(t *testing.T) {
	ctx := context.Background()
	yes := []string{
		"error connecting to api.github.com",
		"gh: Bad Gateway (HTTP 502)", "gh: Service Unavailable (HTTP 503)", "gh: Gateway Timeout (HTTP 504)",
		"dial tcp 192.0.2.1:443: i/o timeout", "read tcp: connection reset by peer",
		"net/http: TLS handshake timeout", "dial tcp: lookup api.github.com: no such host",
		"Get \"https://api.github.com/graphql\": unexpected EOF",
		"unexpected end of JSON input", "gh: exit status 1: Unexpected End of JSON Input",
		"Post \"https://api.github.com/graphql\": context deadline exceeded (Client.Timeout exceeded while awaiting headers)",
	}
	for _, m := range yes {
		if !isTransientGHError(ctx, errors.New(m)) {
			t.Errorf("not transient: %q", m)
		}
	}
	no := []string{
		"gh: Not Found (HTTP 404)", "gh: Validation Failed (HTTP 422)", "gh: Too Many Requests (HTTP 429)",
		"API rate limit exceeded", "exit status 1", "signal: killed", "",
	}
	for _, m := range no {
		if isTransientGHError(ctx, errors.New(m)) {
			t.Errorf("wrongly transient: %q", m)
		}
	}
	if isTransientGHError(ctx, fmt.Errorf("x: %w", ErrGHAuthInvalid)) {
		t.Error("auth failure classified transient")
	}
	if isTransientGHError(ctx, nil) {
		t.Error("nil error classified transient")
	}
}
