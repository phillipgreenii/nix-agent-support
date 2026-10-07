package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// Bounded retry for transient gh failures on READ-ONLY operations (bead
// pg2-daktd).
//
// Why: the host's link to GitHub drops intermittently, and `gh` answers a
// dropped connection with "error connecting to api.github.com" (or a 502/503/
// 504 from the edge, or a response cut off mid-body). One such blip failed the
// whole list/show op as "unavailable" even though an immediate second try
// would have succeeded, and a sustained run of those blips tripped the
// sustained-unavailable alert. A short, bounded retry absorbs the blip without
// hiding an outage: when every attempt fails, the SAME error (same wire code,
// same stderr tail) still surfaces.
//
// What is retried. Only the reads that go through Provider.runRead: show
// (GetPR, ListComments, ListReviews), files, commits, and the search/list
// reads. Writes (comments, merge, close, draft, reviews, ...) are never
// retried: replaying a POST that actually reached GitHub could duplicate it.
// Only transient failures are retried (isTransientGHError); auth, 4xx,
// rate-limit and not-found answers are final on the first attempt.
//
// Budget. At most RetryPolicy.MaxAttempts attempts with a short backoff
// between them, and never a retry that would not fit in the context's
// remaining time (the op's whole-call deadline, scriptout.DefaultBackendTimeout):
// the loop never extends the caller's wall-clock bound.
//
// Rate-limit reserve. List/Search check the GraphQL rate-limit
// reserve ONCE before searching (Backend.checkRateReserve). A retry is more
// GraphQL spend after that check, so the Backend installs that same check as
// the Provider's retry guard (SetRetryGuard) and it runs before EVERY retry;
// a refusal ends the retrying and is returned instead of another attempt.

// RetryPolicy bounds the retry of transient read failures.
type RetryPolicy struct {
	// MaxAttempts is the total number of attempts, the first included. 1 (or
	// less) disables retrying.
	MaxAttempts int
	// Backoff returns the delay before retry number n (1-based: Backoff(1) is
	// the pause between the first and second attempt).
	Backoff func(retry int) time.Duration
	// MinRetryWindow is the least time that must remain before the context
	// deadline, after the backoff, for a retry to be started. It keeps the
	// loop inside the per-call budget: an attempt that cannot plausibly finish
	// is not begun, so the original diagnostic is returned instead of a
	// deadline error from a doomed retry. Zero disables the check.
	MinRetryWindow time.Duration
	// Sleep waits d or until ctx is done, returning ctx.Err() in the latter
	// case. nil means a real, context-aware timer. Tests inject a recorder so
	// they never sleep.
	Sleep func(ctx context.Context, d time.Duration) error
}

// DefaultRetryPolicy is the production policy: three attempts in all, backing
// off 500ms then 1s. That is small enough to add about 1.5s at most to a
// failing call, long enough to ride out a momentary connection reset, and
// bounded in total because the op's context deadline caps it regardless
// (MinRetryWindow keeps a retry from starting with under 5s left).
func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{
		MaxAttempts: 3,
		Backoff: func(retry int) time.Duration {
			if retry < 1 {
				retry = 1
			}
			return 500 * time.Millisecond << (retry - 1)
		},
		MinRetryWindow: 5 * time.Second,
	}
}

// WithRetryPolicy sets the retry policy for the Provider's read operations and
// returns p. New installs DefaultRetryPolicy; NewWithRunner (the test seam)
// installs none, so a test opts in explicitly.
func (p *Provider) WithRetryPolicy(rp RetryPolicy) *Provider {
	p.retry = rp
	return p
}

// SetRetryGuard installs the check that runs before every retry of a read; a
// non-nil error from it stops the retrying and is returned in place of another
// attempt. The Backend installs its GraphQL rate-limit reserve check here.
func (p *Provider) SetRetryGuard(g func(ctx context.Context) error) { p.retryGuard = g }

// readOpts tunes runRead for one call site.
type readOpts struct {
	// allowEmpty accepts an empty stdout as a valid answer (the --paginate REST
	// reads print nothing for "no comments"), instead of treating it as a
	// truncated response.
	allowEmpty bool
	// noGuard skips the retry guard. ReadRateLimit sets it: it IS the guard's
	// own read, so guarding it would recurse.
	noGuard bool
}

// runRead runs a read-only `gh <args...>`, retrying transient failures per the
// Provider's RetryPolicy. A successful run whose stdout is not a complete JSON
// document (empty, or cut off mid-body) counts as a transient failure too; if
// it is still malformed on the last attempt it is returned as-is so the
// caller's own parse reports the usual "unexpected end of JSON input".
func (p *Provider) runRead(ctx context.Context, opts readOpts, args ...string) ([]byte, error) {
	maxAttempts := p.retry.MaxAttempts
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	var prior []string
	for attempt := 1; ; attempt++ {
		raw, err := p.gh.Run(ctx, args...)
		if err == nil && attempt < maxAttempts && !validJSONStream(raw, opts.allowEmpty) {
			err = errTruncatedOutput
		}
		if err == nil {
			return raw, nil
		}
		if attempt >= maxAttempts || !isTransientGHError(ctx, err) {
			return raw, annotateAttempts(err, attempt, prior)
		}
		delay := p.retry.Backoff(attempt)
		if !fitsBudget(ctx, delay, p.retry.MinRetryWindow) {
			return stopRetrying(raw, err, attempt, prior)
		}
		cur := summarizeErr(err)
		if serr := p.sleep(ctx, delay); serr != nil {
			return stopRetrying(raw, err, attempt, prior)
		}
		if !opts.noGuard && p.retryGuard != nil {
			if gerr := p.retryGuard(ctx); gerr != nil {
				return nil, fmt.Errorf("retry of transient gh failure abandoned: %w (previous attempt: %s)", gerr, cur)
			}
		}
		prior = append(prior, cur)
	}
}

// stopRetrying ends the loop without another attempt. A truncated-output
// verdict is dropped (the raw bytes are returned as the answer) so the
// caller's own parse reports the usual "unexpected end of JSON input"; any
// other failure is returned, annotated with the attempts made so far.
func stopRetrying(raw []byte, err error, attempt int, prior []string) ([]byte, error) {
	if errors.Is(err, errTruncatedOutput) {
		return raw, nil
	}
	return raw, annotateAttempts(err, attempt, prior)
}

// errTruncatedOutput marks a gh run that exited 0 but printed a body that is
// empty or cut off, which a flaky connection can produce.
var errTruncatedOutput = errors.New("gh printed empty or truncated JSON output")

func (p *Provider) sleep(ctx context.Context, d time.Duration) error {
	if p.retry.Sleep != nil {
		return p.retry.Sleep(ctx, d)
	}
	return sleepCtx(ctx, d)
}

// sleepCtx waits d, or returns ctx.Err() as soon as ctx is done.
func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// fitsBudget reports whether a retry started after delay still leaves at least
// minWindow before ctx's deadline. A context with no deadline always fits (the
// attempt count is then the only bound).
func fitsBudget(ctx context.Context, delay, minWindow time.Duration) bool {
	dl, ok := ctx.Deadline()
	if !ok || minWindow <= 0 {
		return true
	}
	return time.Until(dl) >= delay+minWindow
}

// annotateAttempts reports, on an error that ends a retried read, how many
// attempts were made and what the earlier ones failed with. The final error
// text is untouched and still wrapped (errors.Is/As and the wire-code
// classification see through it). With a single attempt the error is returned
// unchanged, so the non-retried path is byte-identical to before.
func annotateAttempts(err error, attempts int, prior []string) error {
	if attempts <= 1 || len(prior) == 0 {
		return err
	}
	return fmt.Errorf("%w [gave up after %d attempts; earlier: %s]", err, attempts, strings.Join(prior, " | "))
}

// summarizeErr is a short, single-line digest of an attempt's failure: its
// last bytes, since a wrapped exec error ends with the stderr that names the
// cause.
func summarizeErr(err error) string {
	const max = 120
	s := strings.Join(strings.Fields(failureText(err)), " ")
	if len(s) <= max {
		return s
	}
	cut := len(s) - max
	for cut < len(s) && !utf8.RuneStart(s[cut]) {
		cut++
	}
	return "..." + s[cut:]
}

// ghExecError is a gh run that exited non-zero. Its message is the usual
// "gh <args>: <exit error>: <stderr>"; stderr is kept apart so transient-vs-
// final classification reads only what gh printed, never the command line
// (which can carry a whole GraphQL query and arbitrary search text).
type ghExecError struct {
	msg    string
	stderr string
	err    error
}

func (e *ghExecError) Error() string { return e.msg }
func (e *ghExecError) Unwrap() error { return e.err }

// failureText is the text to classify: gh's stderr when the error is a
// ghExecError, else the whole message (errors from a non-exec runner).
func failureText(err error) string {
	var ge *ghExecError
	if errors.As(err, &ge) {
		return ge.stderr
	}
	return err.Error()
}

// finalHTTP4xx matches an HTTP 4xx status in gh's "(HTTP 404)" / "HTTP 429"
// phrasing: a client error will not heal by retrying.
var finalHTTP4xx = regexp.MustCompile(`http 4\d\d`)

// finalMarkers are phrases that mark a failure as final. They are checked
// before the transient markers so a message carrying both is never retried.
var finalMarkers = []string{
	"rate limit",
	"could not resolve to a",
	"not found",
	"bad credentials",
	"requires authentication",
	"saml enforcement",
}

// transientMarkers are lowercase substrings of gh stderr / Go net errors that
// indicate a passing network or edge fault.
var transientMarkers = []string{
	"error connecting to",
	"connection reset",
	"connection refused",
	"connection timed out",
	"i/o timeout",
	"tls handshake timeout",
	"client.timeout exceeded",
	"unexpected eof",
	"unexpected end of json input",
	"no such host",
	"temporary failure in name resolution",
	"http 502",
	"http 503",
	"http 504",
	"bad gateway",
	"service unavailable",
	"gateway timeout",
}

// isTransientGHError reports whether err is a passing fault worth retrying:
// not an auth failure, not a 4xx / rate-limit / not-found answer, not a
// cancelled or expired context, and matching one of the transient markers.
func isTransientGHError(ctx context.Context, err error) bool {
	if err == nil || ctx.Err() != nil {
		return false
	}
	if errors.Is(err, ErrGHAuthInvalid) {
		return false
	}
	if errors.Is(err, errTruncatedOutput) {
		return true
	}
	msg := strings.ToLower(failureText(err))
	if finalHTTP4xx.MatchString(msg) {
		return false
	}
	for _, m := range finalMarkers {
		if strings.Contains(msg, m) {
			return false
		}
	}
	for _, m := range transientMarkers {
		if strings.Contains(msg, m) {
			return true
		}
	}
	return false
}

// validJSONStream reports whether raw is a complete sequence of JSON values:
// one document, or the several concatenated documents `gh api --paginate`
// prints. Empty output is valid only when allowEmpty.
func validJSONStream(raw []byte, allowEmpty bool) bool {
	if len(bytes.TrimSpace(raw)) == 0 {
		return allowEmpty
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	for {
		var v json.RawMessage
		switch err := dec.Decode(&v); {
		case err == nil:
		case errors.Is(err, io.EOF):
			return true
		default:
			return false
		}
	}
}
