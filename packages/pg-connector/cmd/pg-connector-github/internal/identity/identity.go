// Package identity is the daemon's idea of who it acts as: the viewer login of
// the one GitHub host in the config, and whether GitHub currently accepts the
// token.
//
// # Host and token
//
// The daemon assumes ONE GitHub host (the config's host, passed to every gh
// call as GH_HOST, which NewGH wires). Tokens stay with gh: nothing in this
// package or in the store holds a credential, and the viewer call is made
// through the module's gh choke point (github.CLI), which injects the token
// into the child's environment only.
//
// # Viewer
//
// Tracker.Run resolves the viewer login at start and every Interval (10
// minutes), caches it with the time it was fetched, and keeps the last error. A
// refresh that fails keeps the previous identity and is retried after
// RetryInterval, not after a full Interval, so a transient failure at the
// moment of a `gh auth switch` does not leave the old identity cached for
// another 10 minutes.
//
// When the login CHANGES, the tracker runs every registered flush
// (RegisterFlush) exactly once and writes one structured-log record,
// "viewer_changed", naming the old and the new login. A flush that fails stays
// pending and is retried by the next refresh (each failure is logged as
// "identity_flush_failed"); a flush that succeeded is not run again for the same
// change. An unchanged viewer flushes nothing. The first resolution is not a
// change.
//
// # Unauthenticated state
//
// MarkUnauthenticated and Clear set and clear the typed state the fetching
// packets consult to answer unauthenticated. Observe is the one-call form for a
// fetch's outcome: a nil error clears the state, an error that wraps the
// auth-invalid sentinel marks it, any other error leaves it alone. Cached reads
// are still served while the state is set (the reader reports the age); only a
// fetch is refused.
package identity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/github"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/ratefeed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/vcs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
)

const (
	// DefaultInterval is how often the viewer is re-resolved.
	DefaultInterval = 10 * time.Minute
	// DefaultRetryInterval is how soon a failed refresh is retried.
	DefaultRetryInterval = time.Minute
)

// viewerQuery reads the viewer login and folds in the rate-limit selection so
// the governor books the call from the response.
const viewerQuery = `{ viewer { login } rateLimit { cost remaining resetAt } }`

// Runner runs `gh <args...>` and returns its standard output. *github.CLI
// satisfies it.
type Runner interface {
	Run(ctx context.Context, args ...string) ([]byte, error)
}

var _ Runner = (*github.CLI)(nil)

// NewGH returns the daemon's gh gateway for host: every call it makes carries
// GH_HOST=host (and the `gh auth token` lookup behind it does too). gh is the
// configured gh path; empty means `gh` on PATH.
func NewGH(host, gh string) *github.CLI {
	return github.NewCLIWithOptions(github.Options{Binary: gh, Host: host})
}

// Clock is the tracker's time source.
type Clock interface {
	Now() time.Time
	// After returns a channel that receives once d has elapsed.
	After(d time.Duration) <-chan time.Time
}

// SystemClock is the wall clock.
type SystemClock struct{}

// Now returns time.Now().
func (SystemClock) Now() time.Time { return time.Now() }

// After is time.After.
func (SystemClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// Options configure a Tracker.
type Options struct {
	// Host is the configured GitHub host; informational (Host()), since the
	// GH_HOST every call carries is wired into GH by NewGH.
	Host string
	// GH runs the viewer call. Required.
	GH Runner
	// Clock is the time source; nil means the system clock.
	Clock Clock
	// Interval and RetryInterval default to DefaultInterval and
	// DefaultRetryInterval.
	Interval      time.Duration
	RetryInterval time.Duration
	// Log receives the structured records; nil discards them.
	Log connectord.Logger
	// Governor and Feed, when both set, meter the viewer call as one
	// interactive GraphQL spend. Either nil means the call is not metered.
	Governor *governor.Governor
	Feed     *ratefeed.Feed
}

// Snapshot is the cached identity at one moment.
type Snapshot struct {
	// Login is the cached viewer login; empty until the first resolution
	// succeeds.
	Login string
	// FetchedAt is when Login was fetched; zero until then.
	FetchedAt time.Time
	// LastError is the text of the last failed refresh; empty after a
	// success.
	LastError string
	// Unauthenticated is true while GitHub rejects the token; AuthError is
	// the error that put the daemon there.
	Unauthenticated bool
	AuthError       string
}

type flushEntry struct {
	name    string
	fn      func(context.Context) error
	pending bool
}

// Tracker holds the identity and the flush registry.
type Tracker struct {
	opts Options

	refreshMu sync.Mutex // serializes Refresh

	mu        sync.Mutex
	login     string
	fetchedAt time.Time
	lastErr   string
	unauth    error
	flushes   []*flushEntry
}

// New returns a Tracker. It performs no I/O.
func New(opts Options) *Tracker {
	if opts.Clock == nil {
		opts.Clock = SystemClock{}
	}
	if opts.Interval <= 0 {
		opts.Interval = DefaultInterval
	}
	if opts.RetryInterval <= 0 {
		opts.RetryInterval = DefaultRetryInterval
	}
	if opts.Log == nil {
		opts.Log = connectord.NopLogger
	}
	return &Tracker{opts: opts}
}

// Host returns the configured host.
func (t *Tracker) Host() string { return t.opts.Host }

// RegisterFlush adds an identity-scoped datum to flush when the viewer
// changes. name is for the log. Registration order is run order.
func (t *Tracker) RegisterFlush(name string, fn func(ctx context.Context) error) {
	if name == "" || fn == nil {
		panic("identity: RegisterFlush needs a name and a function")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.flushes = append(t.flushes, &flushEntry{name: name, fn: fn})
}

// Current returns the cached identity and the auth state.
func (t *Tracker) Current() Snapshot {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := Snapshot{Login: t.login, FetchedAt: t.fetchedAt, LastError: t.lastErr}
	if t.unauth != nil {
		s.Unauthenticated = true
		s.AuthError = t.unauth.Error()
	}
	return s
}

// Unauthenticated reports whether GitHub currently rejects the token, and the
// error that said so.
func (t *Tracker) Unauthenticated() (bool, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.unauth != nil, t.unauth
}

// MarkUnauthenticated puts the daemon in the unauthenticated state: a fetch
// answers unauthenticated until Clear. A fetching packet calls it when gh
// reports an invalid token or GitHub answers 401. The first call of a spell,
// and a change of reason, is logged as "auth_state_changed"; repeats are not.
func (t *Tracker) MarkUnauthenticated(err error) {
	if err == nil {
		err = vcs.ErrAuthInvalid
	}
	t.mu.Lock()
	was := t.unauth != nil
	t.unauth = err
	t.mu.Unlock()
	if !was {
		t.opts.Log.Log("auth_state_changed", "state", "unauthenticated", "error", err.Error())
	}
}

// Clear leaves the unauthenticated state. A fetching packet calls it after a
// successful origin call; a successful viewer refresh calls it too.
func (t *Tracker) Clear() {
	t.mu.Lock()
	was := t.unauth != nil
	t.unauth = nil
	t.mu.Unlock()
	if was {
		t.opts.Log.Log("auth_state_changed", "state", "authenticated")
	}
}

// Observe books the outcome of one origin call: nil clears the state, an error
// that wraps vcs.ErrAuthInvalid marks it, any other error leaves it as it was.
// It reports whether err was an authentication failure.
func (t *Tracker) Observe(err error) bool {
	switch {
	case err == nil:
		t.Clear()
		return false
	case errors.Is(err, vcs.ErrAuthInvalid):
		t.MarkUnauthenticated(err)
		return true
	default:
		return false
	}
}

// Refresh resolves the viewer once. On success it caches the identity, clears
// the unauthenticated state and, when the login differs from the cached one,
// runs the flushes. On failure the previous identity stays cached, the error is
// kept (an authentication failure also marks the unauthenticated state) and
// the error is returned.
func (t *Tracker) Refresh(ctx context.Context) error {
	t.refreshMu.Lock()
	defer t.refreshMu.Unlock()

	login, err := t.fetchViewer(ctx)
	if err != nil {
		t.mu.Lock()
		t.lastErr = err.Error()
		t.mu.Unlock()
		t.Observe(err)
		return err
	}

	now := t.opts.Clock.Now()
	t.mu.Lock()
	old := t.login
	t.login, t.fetchedAt, t.lastErr = login, now, ""
	changed := old != "" && old != login
	if changed {
		for _, f := range t.flushes {
			f.pending = true
		}
	}
	t.mu.Unlock()
	t.Clear()

	if changed {
		t.opts.Log.Log("viewer_changed", "old_login", old, "new_login", login, "host", t.opts.Host)
	}
	t.runPendingFlushes(ctx)
	return nil
}

// runPendingFlushes runs every flush still pending once, in registration order.
func (t *Tracker) runPendingFlushes(ctx context.Context) {
	t.mu.Lock()
	var todo []*flushEntry
	for _, f := range t.flushes {
		if f.pending {
			todo = append(todo, f)
		}
	}
	t.mu.Unlock()
	for _, f := range todo {
		if err := f.fn(ctx); err != nil {
			t.opts.Log.Log("identity_flush_failed", "flush", f.name, "error", err.Error())
			continue
		}
		t.mu.Lock()
		f.pending = false
		t.mu.Unlock()
	}
}

// fetchViewer makes the one viewer call, metered when a governor is set.
func (t *Tracker) fetchViewer(ctx context.Context) (string, error) {
	var tk *governor.Ticket
	meta := ratefeed.Meta{Class: governor.ClassInteractive, Purpose: governor.PurposeRefresh, Group: "viewer", BatchSize: 1}
	metered := t.opts.Governor != nil && t.opts.Feed != nil
	if metered {
		var err error
		tk, err = t.opts.Governor.Admit(governor.Request{Bucket: governor.BucketGraphQL, Class: meta.Class, Cost: 1})
		if err != nil {
			return "", fmt.Errorf("viewer: %w", err)
		}
	}
	out, err := t.opts.GH.Run(ctx, "api", "graphql", "-f", "query="+viewerQuery)
	if metered {
		_ = t.opts.Feed.SettleGraphQL(tk, meta, out, err)
	}
	if err != nil {
		return "", fmt.Errorf("viewer: %w", err)
	}
	var resp struct {
		Data struct {
			Viewer struct {
				Login string `json:"login"`
			} `json:"viewer"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		return "", fmt.Errorf("viewer: parse graphql response: %w", err)
	}
	if resp.Data.Viewer.Login == "" {
		return "", errors.New("viewer: graphql response carried no login")
	}
	return resp.Data.Viewer.Login, nil
}

// Run resolves the viewer now and then every Interval (RetryInterval after a
// failure) until ctx is done. It blocks; start it in its own goroutine.
func (t *Tracker) Run(ctx context.Context) {
	for {
		wait := t.opts.Interval
		if err := t.Refresh(ctx); err != nil {
			wait = t.opts.RetryInterval
			t.opts.Log.Log("viewer_refresh_failed", "error", err.Error())
		}
		select {
		case <-ctx.Done():
			return
		case <-t.opts.Clock.After(wait):
		}
	}
}
