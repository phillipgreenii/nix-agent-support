package originguard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// RealmState is the persisted breaker and login state of one realm.
type RealmState struct {
	ConsecutiveFailures int       `json:"consecutive_failures"`
	Class               string    `json:"class,omitempty"`
	Since               time.Time `json:"since,omitzero"`
	OpenUntil           time.Time `json:"open_until,omitzero"`
	Attempts            int       `json:"attempts"`
	NextAttemptAt       time.Time `json:"next_attempt_at,omitzero"`
	LastSuccessAt       time.Time `json:"last_success_at,omitzero"`
	AttemptStartedAt    time.Time `json:"attempt_started_at,omitzero"`
	HolderPID           int       `json:"holder_pid,omitempty"`
	HolderToken         string    `json:"holder_token,omitempty"`
	LastHeartbeatAt     time.Time `json:"last_heartbeat_at,omitzero"`
	LastError           string    `json:"last_error,omitempty"`
	// LastReason is the reason of the last recorded verdict. Empty means no
	// verdict was ever recorded, which counts as available.
	LastReason string `json:"last_reason,omitempty"`
	// TrialAt is when the one trial caller of the current open-breaker
	// window was let through. Zero means no trial yet in this window.
	TrialAt time.Time `json:"trial_at,omitzero"`
}

// Available reports whether the last recorded verdict was available.
func (s RealmState) Available() bool {
	return s.LastReason == "" || Reason(s.LastReason) == ReasonOK
}

// ErrLockHeld is returned by a non-blocking lock attempt that found the lock held.
var ErrLockHeld = errors.New("lock held")

// Store is the on-disk state: one JSON file per realm, a short lock around
// load-modify-store, a login lock held for a whole login attempt, and an
// ignore marker. Every file is local, mode 0600, in directories of mode 0700.
type Store struct {
	dir        string
	clock      Clock
	maxBackoff time.Duration
}

// NewStore returns a store rooted at dir (the pg-origin-guard state
// directory). maxBackoff caps next_attempt_at on read (it is wall-clock).
func NewStore(dir string, clock Clock, maxBackoff time.Duration) *Store {
	return &Store{dir: dir, clock: clock, maxBackoff: maxBackoff}
}

// Dir is the state directory.
func (s *Store) Dir() string { return s.dir }

func (s *Store) realmsDir() string { return filepath.Join(s.dir, "realms") }

func (s *Store) path(realm, suffix string) (string, error) {
	if !ValidRealmName(realm) {
		return "", &UsageError{Msg: fmt.Sprintf("invalid realm name %q", realm)}
	}
	return filepath.Join(s.realmsDir(), realm+suffix), nil
}

func (s *Store) ensureDirs() error {
	for _, d := range []string{s.dir, s.realmsDir()} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
		if err := os.Chmod(d, 0o700); err != nil {
			return err
		}
	}
	return nil
}

// Load reads the state of realm without locking: writes are atomic renames,
// so a read sees a whole file. A corrupt file is moved aside and read as no
// state. next_attempt_at is capped at now + the longest backoff.
func (s *Store) Load(realm string) (RealmState, error) {
	p, err := s.path(realm, ".json")
	if err != nil {
		return RealmState{}, err
	}
	st := s.loadFile(p)
	s.capNext(&st)
	return st, nil
}

func (s *Store) capNext(st *RealmState) {
	if s.maxBackoff <= 0 || st.NextAttemptAt.IsZero() {
		return
	}
	if limit := s.clock.Now().Add(s.maxBackoff); st.NextAttemptAt.After(limit) {
		st.NextAttemptAt = limit
	}
}

func (s *Store) loadFile(p string) RealmState {
	data, err := os.ReadFile(p)
	if err != nil {
		return RealmState{}
	}
	var st RealmState
	if err := json.Unmarshal(data, &st); err != nil {
		_ = os.Rename(p, p+".corrupt")
		return RealmState{}
	}
	return st
}

// Update runs fn on the realm's state under state.lock and stores the result
// atomically. If fn returns an error nothing is written.
func (s *Store) Update(realm string, fn func(*RealmState) error) error {
	p, err := s.path(realm, ".json")
	if err != nil {
		return err
	}
	if err := s.ensureDirs(); err != nil {
		return err
	}
	lk, err := lockFile(filepath.Join(s.realmsDir(), realm+".state.lock"), true)
	if err != nil {
		return err
	}
	defer lk.Unlock()

	st := s.loadFile(p)
	s.capNext(&st)
	if err := fn(&st); err != nil {
		return err
	}
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return atomicWrite(p, data)
}

// atomicWrite writes data to a 0600 temp file in path's directory, syncs it
// and renames it over path.
func atomicWrite(p string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(p), "."+filepath.Base(p)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	fail := func(err error) error {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Chmod(0o600); err != nil {
		return fail(err)
	}
	if _, err := f.Write(data); err != nil {
		return fail(err)
	}
	if err := f.Sync(); err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, p); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// Ignored reports whether the realm's ignore marker exists.
func (s *Store) Ignored(realm string) bool {
	p, err := s.path(realm, ".ignore")
	if err != nil {
		return false
	}
	_, err = os.Lstat(p)
	return err == nil
}

// SetIgnored creates or removes the realm's ignore marker.
func (s *Store) SetIgnored(realm string, ignored bool) error {
	p, err := s.path(realm, ".ignore")
	if err != nil {
		return err
	}
	if !ignored {
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	if err := s.ensureDirs(); err != nil {
		return err
	}
	return atomicWrite(p, nil)
}

// FileLock is a held flock.
type FileLock struct{ f *os.File }

// Unlock releases the lock.
func (l *FileLock) Unlock() {
	if l == nil || l.f == nil {
		return
	}
	_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	_ = l.f.Close()
	l.f = nil
}

func lockFile(p string, blocking bool) (*FileLock, error) {
	f, err := os.OpenFile(p, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	how := syscall.LOCK_EX
	if !blocking {
		how |= syscall.LOCK_NB
	}
	for {
		err = syscall.Flock(int(f.Fd()), how)
		if err != syscall.EINTR {
			break
		}
	}
	if err != nil {
		_ = f.Close()
		if err == syscall.EWOULDBLOCK {
			return nil, ErrLockHeld
		}
		return nil, err
	}
	return &FileLock{f: f}, nil
}

// TryLogin takes the realm's login lock without blocking. It returns
// ErrLockHeld when another process or goroutine holds it.
func (s *Store) TryLogin(realm string) (*FileLock, error) {
	p, err := s.path(realm, ".login.lock")
	if err != nil {
		return nil, err
	}
	if err := s.ensureDirs(); err != nil {
		return nil, err
	}
	return lockFile(p, false)
}

// WaitLogin waits for the realm's login lock, polling every poll, until ctx
// ends. It returns ctx.Err() when the wait is over without the lock.
func (s *Store) WaitLogin(ctx context.Context, realm string, poll time.Duration) (*FileLock, error) {
	for {
		lk, err := s.TryLogin(realm)
		if err == nil {
			return lk, nil
		}
		if !errors.Is(err, ErrLockHeld) {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(poll):
		}
	}
}

// Attempting reports whether a login attempt is in flight: the login lock is
// held AND state names an attempt. An attempt_started_at whose lock is free
// is ignored, so a crash never leaves a stale "attempting".
func (s *Store) Attempting(realm string) (bool, RealmState, error) {
	st, err := s.Load(realm)
	if err != nil {
		return false, st, err
	}
	if st.AttemptStartedAt.IsZero() {
		return false, st, nil
	}
	lk, err := s.TryLogin(realm)
	if errors.Is(err, ErrLockHeld) {
		return true, st, nil
	}
	if err != nil {
		return false, st, err
	}
	lk.Unlock()
	return false, st, nil
}

// ApplyOK records a successful startup or a valid cert: backoff and the
// breaker reset.
func (s *RealmState) ApplyOK(now time.Time) {
	s.ConsecutiveFailures = 0
	s.Class = ""
	s.OpenUntil = time.Time{}
	s.Attempts = 0
	s.NextAttemptAt = time.Time{}
	s.LastSuccessAt = now
	s.LastError = ""
	s.TrialAt = time.Time{}
	s.AttemptStartedAt = time.Time{}
	s.HolderPID = 0
	s.HolderToken = ""
}

// ApplyStartupFailure records a startup failure with a valid cert. An
// auth-rejected failure never counts toward the breaker. Any other class
// counts, and failure_threshold consecutive ones open the breaker for
// open_duration.
func (s *RealmState) ApplyStartupFailure(now time.Time, class Reason, lastError string, b BreakerConfig) {
	s.Class = string(class)
	s.LastError = lastError
	if class == ReasonAuthRejected {
		return
	}
	s.ConsecutiveFailures++
	if s.ConsecutiveFailures >= b.FailureThreshold {
		s.OpenUntil = now.Add(b.OpenDuration)
	}
}

// ApplyLoginFailure records a login attempt that ended with an invalid cert:
// attempts++ and next_attempt_at from the backoff list (the last step
// repeats).
func (s *RealmState) ApplyLoginFailure(now time.Time, l LoginConfig, lastError string) {
	step := min(s.Attempts, len(l.Backoff)-1)
	s.Attempts++
	s.NextAttemptAt = now.Add(l.Backoff[step])
	s.LastError = lastError
	s.AttemptStartedAt = time.Time{}
	s.HolderPID = 0
	s.HolderToken = ""
}

// BreakerOpen reports whether the breaker is open at now.
func (s RealmState) BreakerOpen(now time.Time) bool { return now.Before(s.OpenUntil) }
