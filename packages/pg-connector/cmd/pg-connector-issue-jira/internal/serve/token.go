package serve

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/ops/status"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/origin"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/telemetry"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/tokenfile"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/tokenguard"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/tokenobs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/keychain"
)

// KeepaliveInterval is how often the daemon refreshes the token file's
// timestamps, so the OS temp cleaner (about 3 days unused) leaves it alone.
const KeepaliveInterval = 6 * time.Hour

// fileRetryBackoff is the wait before each retry of a failed token file
// write; after the last one the keepalive interval applies.
var fileRetryBackoff = []time.Duration{time.Minute, 2 * time.Minute, 5 * time.Minute}

// tokenHandoff is the daemon's token machinery: the guard, its store and the
// keepalive loop.
type tokenHandoff struct {
	guard  *tokenguard.Guard
	store  *tokenStore
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// tokenStore is the guard's Store over a tokenfile.Store that is built at
// Start, not before: a temp directory the checks refuse (unset, /tmp, not
// owned, group or other bits) fails Start and Write with the typed error, so
// the guard degrades with token_file, and a later retry re-checks the
// directory instead of staying refused for good.
type tokenStore struct {
	tempDir, link string
	obs           tokenfile.Observer

	mu sync.Mutex
	s  *tokenfile.Store
}

func (t *tokenStore) current() *tokenfile.Store {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.s
}

// Start validates the temp directory, then removes leftovers and prepares the
// subdirectory (the guard calls it before the keychain read).
func (t *tokenStore) Start() error {
	s, err := tokenfile.New(t.tempDir, t.link)
	if err != nil {
		t.mu.Lock()
		t.s = nil
		t.mu.Unlock()
		return err
	}
	s.SetObserver(t.obs)
	t.mu.Lock()
	t.s = s
	t.mu.Unlock()
	return s.Start()
}

var errNoTokenStore = errors.New("token file store is not prepared")

// Write replaces the token file.
func (t *tokenStore) Write(token []byte) error {
	s := t.current()
	if s == nil {
		return errNoTokenStore
	}
	return s.Write(token)
}

// Touch refreshes the file's timestamps; a store that never started reports
// the file as missing.
func (t *tokenStore) Touch() error {
	s := t.current()
	if s == nil {
		return os.ErrNotExist
	}
	return s.Touch()
}

// Remove deletes the file, the owned subdirectory entries and the link.
func (t *tokenStore) Remove() error {
	s := t.current()
	if s == nil {
		return nil
	}
	return s.Remove()
}

// lateMetrics is the tokenobs.Metrics the daemon hands the observer before the
// telemetry exists (the guard starts first): events that arrive until set is
// called are logged but not counted.
type lateMetrics struct {
	t atomic.Pointer[telemetry.Telemetry]
}

func (m *lateMetrics) set(t *telemetry.Telemetry) { m.t.Store(t) }

func (m *lateMetrics) TokenRead(reason, result, errorKind string) {
	if t := m.t.Load(); t != nil {
		t.TokenRead(reason, result, errorKind)
	}
}

func (m *lateMetrics) TokenFileOp(op, result string) {
	if t := m.t.Load(); t != nil {
		t.TokenFileOp(op, result)
	}
}

// tokenState is the telemetry's TokenState: whether the guard is degraded and
// why; healthy when the token handoff is off.
func (d *daemon) tokenState() (bool, string) {
	th := d.tok.Load()
	if th == nil {
		return false, ""
	}
	st := th.guard.State()
	return st.Degraded, st.Reason
}

// tokenGuard is the guard for the status op, or nil when the handoff is off.
func (d *daemon) tokenGuard() status.TokenGuard {
	if th := d.tok.Load(); th != nil {
		return th.guard
	}
	return nil
}

// startToken runs the start sequence of the token handoff and returns the
// credential guard for the origin runner, or nil when token_link is unset
// (today's behavior: no keychain read, no file). It runs after proc.Start
// holds the instance lock. A failure never stops the daemon: the guard starts
// degraded and calls are refused until a read and a write succeed.
func (d *daemon) startToken(ctx context.Context) origin.Refresher {
	cfg := d.cfg
	if cfg.TokenLink == "" {
		return nil
	}
	// TMPDIR is read ONCE, here: the documented exception to the daemon
	// reading no environment variable.
	tempDir := os.Getenv("TMPDIR")
	if d.o.TempDir != nil {
		tempDir = d.o.TempDir()
	}
	obs := &tokenobs.Observer{
		Metrics:  &d.tokMetrics,
		Log:      d.log.Log,
		FilePath: filepath.Join(tempDir, tokenfile.SubdirName, tokenfile.FileName),
		LinkPath: cfg.TokenLink,
	}
	th := &tokenHandoff{store: &tokenStore{tempDir: tempDir, link: cfg.TokenLink, obs: obs.StoreEvent}}
	kc := d.o.Keychain
	if kc == nil {
		kc = keychain.Security{}
	}
	th.guard = tokenguard.New(tokenguard.Config{
		Keychain: kc, Store: th.store, Service: cfg.KeychainService, Account: cfg.KeychainAccount,
		Clock: d.o.TokenClock, Timer: d.o.TokenTimer, Recorder: obs,
	})
	d.tok.Store(th)
	// The guard's Start prepares the store (cleaning leftovers), makes the one
	// keychain read and writes the file and the link.
	th.guard.Start(ctx)

	kctx, cancel := context.WithCancel(context.Background())
	th.cancel = cancel
	th.wg.Add(1)
	go func() {
		defer th.wg.Done()
		d.keepalive(kctx, th)
	}()
	return th.guard
}

// keepalive touches the token file every KeepaliveInterval, rewrites it after
// one keychain read when it vanished, and retries a failed write with a short
// backoff.
func (d *daemon) keepalive(ctx context.Context, th *tokenHandoff) {
	timer := d.o.TokenTimer
	if timer == nil {
		timer = tokenguard.SystemTimer{}
	}
	retries := 0
	for {
		delay := KeepaliveInterval
		if th.guard.State().Reason == tokenguard.ReasonTokenFile {
			if retries < len(fileRetryBackoff) {
				delay = fileRetryBackoff[retries]
			}
			retries++
		} else {
			retries = 0
		}
		select {
		case <-timer.After(delay):
		case <-ctx.Done():
			return
		}
		if th.guard.State().Reason == tokenguard.ReasonTokenFile {
			th.guard.RetryFile(ctx)
			continue
		}
		err := th.store.Touch()
		switch {
		case err == nil:
		case errors.Is(err, os.ErrNotExist):
			th.guard.Refresh(ctx, th.guard.Generation(), origin.ReasonTokenFileMissing)
		default:
			d.log.Log("token_file_touch_failed", "error", err.Error())
		}
	}
}

// removeToken is the drain hook "token-file", and the cleanup of an error
// return from start. It marks the guard closing first, so a call failing on
// the vanished file never reads the keychain or degrades, stops the keepalive,
// then removes the file, its subdirectory entries and the link. A removal
// failure is logged and never blocks shutdown.
func (d *daemon) removeToken(context.Context) error {
	th := d.tok.Load()
	if th == nil {
		return nil
	}
	th.guard.Close()
	if th.cancel != nil {
		th.cancel()
	}
	th.wg.Wait()
	if err := th.store.Remove(); err != nil {
		d.log.Log("token_file_remove_failed", "error", err.Error())
	}
	return nil
}
