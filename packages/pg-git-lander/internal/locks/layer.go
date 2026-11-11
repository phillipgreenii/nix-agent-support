package locks

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"strings"
	"sync"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/gitexec"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/seams"
)

const (
	// PollInterval is how often a waiter reads a held lock.
	PollInterval = time.Second
	// MessageEvery is how often a waiter reports the holder on Out.
	MessageEvery = 10 * time.Second
	// SetRetryMax bounds the random wait before a whole lock set is retried.
	SetRetryMax = 500 * time.Millisecond
	// WriteRetries is the number of retries of a failed lock write whose
	// ref value did not change.
	WriteRetries = 5
	backoffBase  = 100 * time.Millisecond
	// StaleLockAge is the age of a ref .lock file left by a killed git.
	StaleLockAge = 60 * time.Second
)

// Config holds the two configuration inputs of the lock layer
// (lock.lease_ttl and lock.wait, section 12.4).
type Config struct {
	LeaseTTL time.Duration
	Wait     time.Duration
}

// Layer reads, writes, renews and releases lock refs of one repository.
type Layer struct {
	Git   *gitexec.Git
	Clock seams.Clock
	// Dir is any directory of the repository (the common dir is derived).
	Dir    string
	Config Config
	// Out receives the waiting messages; nil discards them.
	Out io.Writer
	// Sleep waits d or until ctx ends; nil sleeps in real time. Tests inject
	// one that advances their fake Clock.
	Sleep func(ctx context.Context, d time.Duration) error
	// Jitter returns a random duration in [0, max]; nil is math/rand.
	Jitter func(max time.Duration) time.Duration
	// Interval overrides the renewal period (LeaseTTL/3) when positive.
	Interval time.Duration
	// Liveness and Host judge a holder on this host dead (pid gone or reused,
	// a zombie counting as gone). With either nil no holder is judged dead by
	// its pid and only lease expiry by observation applies.
	Liveness seams.Liveness
	Host     seams.Host
	// OnConvert, when set, receives every conversion and takeover of a lock
	// whose holder was dead or expired, for the log and events writer.
	OnConvert func(Conversion)
	// OnWait, when set, receives every wait for a held lock that ended in
	// acquisition or in a busy outcome, for the log and events writer.
	OnWait func(LockWait)

	obsMu sync.Mutex
	obs   map[string]observation

	commonOnce sync.Once
	commonDir  string
	commonErr  error
}

var (
	// ErrTaken is returned by a failed lock write when the ref's value
	// changed: the lock changed hands.
	ErrTaken = errors.New("lock ref changed hands")
	// ErrWriteFailed is returned when a lock write kept failing with the ref
	// value unchanged (git's own ref lock held).
	ErrWriteFailed = errors.New("lock write failed with the ref unchanged")
	// ErrLockLost is returned by a renewal that can no longer keep the lease.
	ErrLockLost = errors.New("lock lost")
)

func (l *Layer) sleep(ctx context.Context, d time.Duration) error {
	if l.Sleep != nil {
		return l.Sleep(ctx, d)
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

func (l *Layer) jitter(max time.Duration) time.Duration {
	if l.Jitter != nil {
		return l.Jitter(max)
	}
	if max <= 0 {
		return 0
	}
	return time.Duration(rand.Int64N(int64(max) + 1))
}

func (l *Layer) common(ctx context.Context) (string, error) {
	l.commonOnce.Do(func() { l.commonDir, l.commonErr = l.Git.CommonDir(ctx, l.Dir) })
	return l.commonDir, l.commonErr
}

// ReadState is what a lock ref holds.
type ReadState struct {
	Ref string
	// Absent is true when the ref does not exist.
	Absent bool
	// OID is the blob oid the ref points at (empty when absent).
	OID string
	// Rec is the parsed record; nil when absent or unreadable.
	Rec *Record
	// Unreadable is non-nil when the ref does not point at a blob, the
	// record does not parse, or its schema is newer: the lock is held by a
	// live holder and is never taken over or converted.
	Unreadable error
}

// Read reads one lock ref.
func (l *Layer) Read(ctx context.Context, ref string) (ReadState, error) {
	st := ReadState{Ref: ref}
	r := l.Git.Run(ctx, l.Dir, "for-each-ref", "--format=%(refname) %(objectname) %(objecttype)", ref)
	if !r.OK() {
		return st, fmt.Errorf("git for-each-ref %s: %s", ref, strings.TrimSpace(string(r.Stderr)))
	}
	var oid, typ string
	for _, line := range strings.Split(string(r.Stdout), "\n") {
		f := strings.Fields(line)
		if len(f) == 3 && f[0] == ref {
			oid, typ = f[1], f[2]
		}
	}
	if oid == "" {
		st.Absent = true
		return st, nil
	}
	st.OID = oid
	if typ != "blob" {
		st.Unreadable = fmt.Errorf("lock ref %s points at a %s, not a blob", ref, typ)
		return st, nil
	}
	b := l.Git.Run(ctx, l.Dir, "cat-file", "blob", oid)
	if !b.OK() {
		st.Unreadable = fmt.Errorf("lock ref %s: cannot read blob %s", ref, oid)
		return st, nil
	}
	rec, err := Parse(b.Stdout)
	if err != nil {
		st.Unreadable = fmt.Errorf("lock ref %s: %w", ref, err)
		return st, nil
	}
	st.Rec = rec
	return st, nil
}

func zeroOID(like string) string {
	if len(like) == 64 {
		return strings.Repeat("0", 64)
	}
	return strings.Repeat("0", 40)
}

func isZero(oid string) bool { return strings.Trim(oid, "0") == "" }

func (l *Layer) hashBlob(ctx context.Context, rec Record) (string, error) {
	b, err := rec.MarshalJSON()
	if err != nil {
		return "", err
	}
	r := l.Git.RunInput(ctx, l.Dir, append(b, '\n'), "hash-object", "-w", "--stdin")
	if !r.OK() {
		return "", fmt.Errorf("git hash-object: %s", strings.TrimSpace(string(r.Stderr)))
	}
	return strings.TrimSpace(string(r.Stdout)), nil
}

// write swaps ref from old (empty for create-if-absent) to a blob holding rec.
// After any failed git update-ref the ref is re-read (section 8.4): a changed
// value is ErrTaken; an unchanged value is retried with jittered backoff, up
// to WriteRetries times, then ErrWriteFailed. giveUp, when non-nil, is asked
// before each backoff sleep whether to stop instead (the renewal's two thirds
// budget) and, when it says so, write returns ErrLockLost.
func (l *Layer) write(ctx context.Context, ref string, rec Record, old string, giveUp func(next time.Duration) bool) (string, error) {
	blob, err := l.hashBlob(ctx, rec)
	if err != nil {
		return "", err
	}
	exp := old
	if exp == "" {
		exp = zeroOID(blob)
	}
	for attempt := 0; ; attempt++ {
		r := l.Git.Run(ctx, l.Dir, "update-ref", ref, blob, exp)
		if r.OK() {
			return blob, nil
		}
		cur, rerr := l.Read(ctx, ref)
		if rerr != nil {
			return "", rerr
		}
		if cur.OID != old {
			return "", fmt.Errorf("%s: %w", ref, ErrTaken)
		}
		if attempt >= WriteRetries {
			return "", fmt.Errorf("%s: %w: %s", ref, ErrWriteFailed, strings.TrimSpace(string(r.Stderr)))
		}
		d := l.jitter(backoffBase << attempt)
		if giveUp != nil && giveUp(d) {
			return "", fmt.Errorf("%s: %w", ref, ErrLockLost)
		}
		if err := l.sleep(ctx, d); err != nil {
			return "", err
		}
	}
}

func (l *Layer) deleteRef(ctx context.Context, ref, old string) error {
	r := l.Git.Run(ctx, l.Dir, "update-ref", "-d", ref, old)
	if r.OK() {
		return nil
	}
	cur, err := l.Read(ctx, ref)
	if err == nil && cur.Absent {
		return nil
	}
	return fmt.Errorf("release %s: %s", ref, strings.TrimSpace(string(r.Stderr)))
}

func (l *Layer) stamp(rec *Record) {
	rec.Schema = SchemaVersion
	if rec.State == StateRunning {
		rec.Lease = &Lease{RenewedAt: l.Clock.Now().UTC().Truncate(time.Second), TTLSec: int(l.Config.LeaseTTL / time.Second)}
	}
}

// Held is a lock this process holds. One current in-memory record is guarded
// by one mutex, so the renewal loop and the operation record's field writes
// mutate and swap from the same record (a renewal never reverts a stage or
// move write).
type Held struct {
	l   *Layer
	Ref string

	mu        sync.Mutex
	rec       Record
	oid       string
	lastRenew time.Time
	released  bool
	// prior is the stopped record this lock was taken back from, if any.
	prior *Record
}

// Record returns a copy of the current record.
func (h *Held) Record() Record {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.rec.Clone()
}

// OID is the oid of the current record blob (for the fence's verify list).
func (h *Held) OID() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.oid
}

// RenewAge is the age of the last successful renewal on the monotonic clock.
func (h *Held) RenewAge() time.Duration {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.l.Clock.Since(h.lastRenew)
}

func (h *Held) budget(next time.Duration) bool {
	return h.l.Clock.Since(h.lastRenew)+next >= h.l.Config.LeaseTTL*2/3
}

// Update applies mutate to a copy of the current record and swaps the result
// in with the current record as the expected value. Callers write stage, move
// and stop fields through it.
func (h *Held) Update(ctx context.Context, mutate func(*Record)) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.updateLocked(ctx, mutate, false)
}

func (h *Held) updateLocked(ctx context.Context, mutate func(*Record), renewal bool) error {
	if h.released {
		return fmt.Errorf("%s: %w", h.Ref, ErrLockLost)
	}
	next := h.rec.Clone()
	mutate(&next)
	var giveUp func(time.Duration) bool
	if renewal {
		giveUp = h.budget
	}
	oid, err := h.l.write(ctx, h.Ref, next, h.oid, giveUp)
	if err != nil {
		if errors.Is(err, ErrTaken) || errors.Is(err, ErrWriteFailed) {
			err = fmt.Errorf("%w: %w", ErrLockLost, err)
		}
		return err
	}
	h.rec, h.oid = next, oid
	if renewal {
		h.lastRenew = h.l.Clock.Now()
	}
	return nil
}

// Renew writes a new record with a new renewed_at, swapped in with the
// current record as the expected value. It returns ErrLockLost when the ref
// changed hands or when retries would outlast two thirds of the lease.
func (h *Held) Renew(ctx context.Context) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.renewLocked(ctx)
}

func (h *Held) renewLocked(ctx context.Context) error {
	return h.updateLocked(ctx, func(r *Record) {
		r.Lease = &Lease{RenewedAt: h.l.Clock.Now().UTC().Truncate(time.Second), TTLSec: int(h.l.Config.LeaseTTL / time.Second)}
	}, true)
}

// MaybeRenew renews only while the current step is within its timeout (the
// record's current_command.deadline), so a stuck step lets its lease lapse.
// It reports whether it renewed.
func (h *Held) MaybeRenew(ctx context.Context) (bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if cc := h.rec.CurrentCommand; cc != nil && !cc.Deadline.IsZero() && h.l.Clock.Now().After(cc.Deadline) {
		return false, nil
	}
	return true, h.renewLocked(ctx)
}

// Release deletes the lock ref if it still holds this holder's record.
func (h *Held) Release(ctx context.Context) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.released {
		return nil
	}
	h.released = true
	return h.l.deleteRef(ctx, h.Ref, h.oid)
}

// Undo gives the lock back: a lock taken back from a stopped record is
// swapped back to that record (so a resume that cannot get its other locks
// never strands a record in running); any other lock is released.
func (h *Held) Undo(ctx context.Context) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.released {
		return nil
	}
	if h.prior == nil {
		h.released = true
		return h.l.deleteRef(ctx, h.Ref, h.oid)
	}
	if _, err := h.l.write(ctx, h.Ref, *h.prior, h.oid, nil); err != nil {
		return err
	}
	h.released = true
	return nil
}

// RunRenewal renews h every third of the lease (gated by MaybeRenew) until
// stop is called. A renewal that fails sends its error on lost and ends the
// loop; the holder then stops with lock-lost, moving nothing.
func (h *Held) RunRenewal(ctx context.Context) (stop func(), lost <-chan error) {
	iv := h.l.Interval
	if iv <= 0 {
		iv = h.l.Config.LeaseTTL / 3
	}
	ctx, cancel := context.WithCancel(ctx)
	ch := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(iv)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if _, err := h.MaybeRenew(ctx); err != nil && ctx.Err() == nil {
					ch <- err
					return
				}
			}
		}
	}()
	return func() { cancel(); <-done }, ch
}
