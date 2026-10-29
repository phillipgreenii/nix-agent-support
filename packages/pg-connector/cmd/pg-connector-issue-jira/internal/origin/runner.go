package origin

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/eventlog"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// Results the Observer is told, one per call.
const (
	ResultOK              = "ok"
	ResultError           = "error"
	ResultRateLimited     = "rate_limited"
	ResultUnauthenticated = "unauthenticated"
	ResultRefused         = "refused"
	// ResultNotFound is a call pjira answered "not found": the caller named an
	// issue that does not exist, which is the caller's miss, not an origin fault.
	ResultNotFound = "not_found"
)

// Failure kinds of a CallError, which are also the governor outcome words.
const (
	KindRateLimited     = "rate_limited"
	KindUnauthenticated = "unauthenticated"
	KindError           = "error"
	KindNotFound        = "not_found"
)

// Call says what one origin call is for. It rides on the context (WithCall).
type Call struct {
	// Class is the governor priority class. A zero Class runs as
	// governor.ClassInteractive.
	Class governor.Class
	// Purpose is what the call is for, recorded on the spend. A zero Purpose
	// is governor.PurposeInteractive.
	Purpose governor.Purpose
	// Group is the field group the call refreshes ("" when none).
	Group string
	// BatchSize is the number of entities the call carries.
	BatchSize int
}

type callKey struct{}

// WithCall returns ctx carrying c. A context without a call runs as
// ClassInteractive and PurposeInteractive.
func WithCall(ctx context.Context, c Call) context.Context {
	return context.WithValue(ctx, callKey{}, c)
}

func callFrom(ctx context.Context) Call {
	c, _ := ctx.Value(callKey{}).(Call)
	if c.Class == 0 {
		c.Class = governor.ClassInteractive
	}
	if c.Purpose == "" {
		c.Purpose = governor.PurposeInteractive
	}
	return c
}

// Observer is told the result of every call (ok, error, rate_limited,
// unauthenticated or refused) and how long it took.
type Observer interface {
	Request(result string, d time.Duration)
}

// Auth is the authentication marker the runner keeps: an authentication
// failure marks it and a success clears it.
type Auth interface {
	MarkUnauthenticated(err error)
	Clear()
}

// CallError is the error the runner returns for a call that reached pjira and
// failed. Error is the inner error's text, so a message classifier
// (internal.ClassifyPjiraError) keeps working on it.
type CallError struct {
	// Kind is KindRateLimited, KindUnauthenticated, KindNotFound or KindError.
	Kind string
	Err  error
}

// Error returns the inner error's text.
func (e *CallError) Error() string { return e.Err.Error() }

// Unwrap returns the inner error.
func (e *CallError) Unwrap() error { return e.Err }

// IsRateLimited reports whether err is a call answered with HTTP 429.
func IsRateLimited(err error) bool {
	var ce *CallError
	return errors.As(err, &ce) && ce.Kind == KindRateLimited
}

// IsUnauthenticated reports whether err is a call refused for authentication.
func IsUnauthenticated(err error) bool {
	var ce *CallError
	return errors.As(err, &ce) && ce.Kind == KindUnauthenticated
}

// Options configure New.
type Options struct {
	// Inner executes pjira (an *internal.CLIRunner in production, built with
	// the absolute pjira_path and an empty, non-nil Env of the pjira_env
	// entries only). Required.
	Inner internal.Runner
	// Governor admits and records every call. Required. The caller builds it
	// from GovernorConfig.
	Governor *governor.Governor
	// Bucket is the governor bucket spent from; empty means Bucket.
	Bucket governor.Bucket
	// Concurrency is the most pjira processes running at once. It MUST be
	// positive.
	Concurrency int
	// DefaultPause is the pause after a 429 that states no delay. It MUST be
	// positive.
	DefaultPause time.Duration
	// Clock times the calls; nil means governor.SystemClock.
	Clock governor.Clock
	// Jitter returns a value in [0,1); nil means math/rand.
	Jitter func() float64
	// RetryAfter parses a stated delay out of a failure text; nil means none
	// is ever stated, so the default pause always applies.
	RetryAfter func(errText string) (time.Duration, bool)
	// Observer is told each result; nil means none.
	Observer Observer
	// Auth is marked on an authentication failure and cleared on a success;
	// nil means none.
	Auth Auth
	// Floor is rate_reserve_requests, the requests of the origin burst window
	// below which nothing spends. The governor enforces it (GovernorConfig puts
	// it in the bucket); the runner uses it only to leave a reading unenforced
	// when the origin's own limit is at or below it. Zero disables the floor.
	Floor int64
	// Readings keeps the latest rate-limit reading for the status op and the
	// gauges; nil means the runner makes its own (Readings returns it).
	Readings *Readings
}

// Runner is the governed internal.Runner.
type Runner struct {
	inner      internal.Runner
	gov        *governor.Governor
	bucket     governor.Bucket
	sem        chan struct{}
	def        time.Duration
	clock      governor.Clock
	jitter     func() float64
	retryAfter func(string) (time.Duration, bool)
	obs        Observer
	auth       Auth
	floor      int64
	readings   *Readings

	mu    sync.Mutex
	pause pauseTracker
}

var _ internal.Runner = (*Runner)(nil)

// New returns the governed runner.
func New(opts Options) (*Runner, error) {
	switch {
	case opts.Inner == nil:
		return nil, errors.New("origin: Inner runner is required")
	case opts.Governor == nil:
		return nil, errors.New("origin: Governor is required")
	case opts.Concurrency <= 0:
		return nil, fmt.Errorf("origin: Concurrency must be positive, got %d", opts.Concurrency)
	case opts.DefaultPause <= 0:
		return nil, fmt.Errorf("origin: DefaultPause must be positive, got %s", opts.DefaultPause)
	}
	r := &Runner{
		inner: opts.Inner, gov: opts.Governor, bucket: opts.Bucket,
		sem: make(chan struct{}, opts.Concurrency), def: opts.DefaultPause,
		clock: opts.Clock, jitter: opts.Jitter, retryAfter: opts.RetryAfter,
		obs: opts.Observer, auth: opts.Auth, floor: opts.Floor, readings: opts.Readings,
	}
	if r.readings == nil {
		r.readings = NewReadings()
	}
	if r.bucket == "" {
		r.bucket = Bucket
	}
	if r.clock == nil {
		r.clock = governor.SystemClock{}
	}
	if r.jitter == nil {
		r.jitter = rand.Float64
	}
	return r, nil
}

// Readings is the holder of the latest rate-limit reading the runner took.
func (r *Runner) Readings() *Readings { return r.readings }

// Binary returns the configured pjira path (the inner runner's binary).
func (r *Runner) Binary() string { return r.inner.Binary() }

func (r *Runner) observe(result string, d time.Duration) {
	if r.obs != nil {
		r.obs.Request(result, d)
	}
}

// Run executes `pjira <args...>` under the governor, the concurrency cap and
// the 429 rules. The Call (class, purpose, group, batch size) comes from ctx.
func (r *Runner) Run(ctx context.Context, args ...string) (string, error) {
	call := callFrom(ctx)
	start := r.clock.Now()

	// A pause holds only the background classes inside the governor; an
	// interactive call is refused here so it is answered stale by its caller
	// instead of being sent to a tenant that has just said 429.
	if !call.Class.Background() {
		if until, reason, ok := r.Paused(); ok {
			r.observe(ResultRefused, r.clock.Now().Sub(start))
			return "", &PausedError{Until: until, Reason: reason}
		}
	}

	ticket, err := r.gov.Admit(governor.Request{Bucket: r.bucket, Class: call.Class, Cost: 1})
	if err != nil {
		r.observe(ResultRefused, r.clock.Now().Sub(start))
		return "", err
	}

	select {
	case r.sem <- struct{}{}:
	case <-ctx.Done():
		ticket.Release()
		return "", ctx.Err()
	}
	var reading *internal.RateLimit
	runCtx := internal.WithRateLimitSink(ctx, func(rl internal.RateLimit) { reading = &rl })
	out, runErr := r.inner.Run(runCtx, args...)
	<-r.sem

	spend := governor.Spend{Purpose: call.Purpose, Group: call.Group, BatchSize: call.BatchSize, Cost: 1}
	// The reading the response carried (a 429 included) goes to the status
	// holder and, through the spend, to the governor, which enforces the floor.
	if reading != nil {
		latest := Latest{RateLimit: *reading, At: r.clock.Now()}
		r.readings.Record(latest.RateLimit, latest.At)
		spend.Reading = governorReading(latest, r.floor, BurstWindow)
	}
	if runErr == nil {
		r.endRateLimitRun()
		if r.auth != nil {
			r.auth.Clear()
		}
		spend.Outcome = ResultOK
		_ = ticket.Settle(spend)
		r.observe(ResultOK, r.clock.Now().Sub(start))
		return out, nil
	}

	text := runErr.Error()
	kind := KindError
	switch {
	case eventlog.ClassifyFailure(text) == eventlog.ClassRateLimited:
		kind = KindRateLimited
	case errors.Is(internal.ClassifyPjiraError(text), scriptout.ErrUnauthenticated):
		kind = KindUnauthenticated
	case errors.Is(internal.ClassifyPjiraError(text), scriptout.ErrNotFound):
		kind = KindNotFound
	}
	spend.Outcome = kind
	if kind == KindRateLimited {
		r.startPause(text, reading)
	} else {
		r.endRateLimitRun()
	}
	if kind == KindUnauthenticated && r.auth != nil {
		r.auth.MarkUnauthenticated(runErr)
	}
	_ = ticket.Settle(spend)
	r.observe(kind, r.clock.Now().Sub(start))
	return out, &CallError{Kind: kind, Err: runErr}
}

func (r *Runner) endRateLimitRun() {
	r.mu.Lock()
	r.pause.reset()
	r.mu.Unlock()
}

// startPause pauses the background classes for the pause this 429 earns.
func (r *Runner) startPause(text string, rl *internal.RateLimit) {
	var stated time.Duration
	var has bool
	switch {
	case rl != nil && rl.RetryAfterSeconds > 0:
		// Retry-After is a minimum delay: the pause never goes below it.
		stated, has = time.Duration(rl.RetryAfterSeconds)*time.Second, true
	case r.retryAfter != nil:
		stated, has = r.retryAfter(text)
		has = has && stated > 0
	}
	r.mu.Lock()
	d := r.pause.next(stated, has, r.def, r.jitter)
	n := r.pause.count
	r.mu.Unlock()
	reason := fmt.Sprintf("jira answered HTTP 429 (%d in a row): background paused for %s", n, d.Round(time.Millisecond))
	_ = r.gov.Pause(r.bucket, d, reason)
}

// Search runs `pjira search --jql <jql> --all [--expand a,b]` and decodes the
// answer. Truncated and Items are what callers read.
func (r *Runner) Search(ctx context.Context, jql string, expand []string) (*internal.PjiraSearchResult, error) {
	args := []string{"search", "--jql", jql, "--all"}
	if len(expand) > 0 {
		args = append(args, "--expand", strings.Join(expand, ","))
	}
	out, err := r.Run(ctx, args...)
	if err != nil {
		return nil, err
	}
	res, err := internal.DecodeSearchResult(out)
	if err != nil {
		return nil, fmt.Errorf("pjira search: decode: %w", err)
	}
	return res, nil
}

// Issue runs `pjira issue -- <key>` and decodes the answer.
func (r *Runner) Issue(ctx context.Context, key string) (*internal.PjiraIssue, error) {
	out, err := r.Run(ctx, "issue", "--", key)
	if err != nil {
		return nil, err
	}
	iss, err := internal.DecodeIssue(out)
	if err != nil {
		return nil, fmt.Errorf("pjira issue: decode: %w", err)
	}
	return iss, nil
}

// AuthState is the daemon's authentication tracker. One value is passed to the
// origin runner, the query runner, the detail service and the status report.
// It satisfies Auth, query.Auth and groupsvc.AuthMarker.
type AuthState struct {
	mu       sync.Mutex
	unauth   bool
	err      error
	identity string
}

// NewAuthState returns a tracker that starts authenticated, with no identity.
func NewAuthState() *AuthState { return &AuthState{} }

// MarkUnauthenticated records an authentication failure.
func (a *AuthState) MarkUnauthenticated(err error) {
	a.mu.Lock()
	a.unauth, a.err = true, err
	a.mu.Unlock()
}

// Clear records an authenticated answer.
func (a *AuthState) Clear() {
	a.mu.Lock()
	a.unauth, a.err = false, nil
	a.mu.Unlock()
}

// Unauthenticated reports whether the last answer was an authentication
// failure, and that failure.
func (a *AuthState) Unauthenticated() (bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.unauth, a.err
}

// SetIdentity records the acting identity (from the last auth_status answer or
// the resolved operator).
func (a *AuthState) SetIdentity(id string) {
	a.mu.Lock()
	a.identity = id
	a.mu.Unlock()
}

// Identity returns the acting identity; empty when unknown.
func (a *AuthState) Identity() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.identity
}
