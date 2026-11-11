package locks

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/outcome"
)

// Why says how a holder was judged gone (the holder_converted event's why).
type Why string

// Reasons a holder is judged gone.
const (
	WhyDead    Why = "dead"
	WhyExpired Why = "expired"
)

// Freshness is the class of a running holder as this run observes it.
type Freshness string

// Freshness classes.
const (
	// FreshnessFresh is a record this run has watched, unchanged for less
	// than the lease, or one that changed since this run first saw it.
	FreshnessFresh Freshness = "fresh"
	// FreshnessUnobserved is a record this run has not watched yet.
	FreshnessUnobserved Freshness = "unobserved"
	// FreshnessDead is a holder on this host whose pid is gone, reused or a
	// zombie.
	FreshnessDead Freshness = "dead"
	// FreshnessForeignHost is a holder on another host: its pid cannot be
	// probed, so only lease expiry by observation applies.
	FreshnessForeignHost Freshness = "foreign-host"
)

// Conversion is one conversion or takeover of a lock whose holder was dead or
// expired, handed to the log and events writer through Layer.OnConvert
// (holder_converted: lock, previous_owner, why).
type Conversion struct {
	// Lock is the ref the waiter found held.
	Lock          string
	PreviousOwner Owner
	Why           Why
	OpID          string
	// Takeover is true when a TARGET (or admin) record was replaced; false
	// when a BRANCH or worktree record was converted to stopped.
	Takeover bool
	// Refs lists every ref the transaction changed.
	Refs []string
}

type observation struct {
	oid  string
	seen time.Time
}

// observe records the first time this run saw oid at ref (resetting when the
// oid changed) and returns that time. The time is read from Clock.Now and
// judged with Clock.Since, so a wall-clock step does not matter.
func (l *Layer) observe(ref, oid string) time.Time {
	l.obsMu.Lock()
	defer l.obsMu.Unlock()
	if l.obs == nil {
		l.obs = map[string]observation{}
	}
	if o, ok := l.obs[ref]; ok && o.oid == oid {
		return o.seen
	}
	now := l.Clock.Now()
	l.obs[ref] = observation{oid: oid, seen: now}
	return now
}

func (l *Layer) observedAt(ref, oid string) (time.Time, bool) {
	l.obsMu.Lock()
	defer l.obsMu.Unlock()
	o, ok := l.obs[ref]
	return o.seen, ok && o.oid == oid
}

func (l *Layer) sameHost(rec *Record) bool {
	return l.Host != nil && l.Liveness != nil && rec.Owner.Host == l.Host.Name()
}

func (l *Layer) pidGone(rec *Record) bool {
	if !l.sameHost(rec) {
		return false
	}
	tok := ""
	if rec.Owner.PIDStart > 0 {
		tok = strconv.FormatInt(rec.Owner.PIDStart, 10)
	}
	return !l.Liveness.Alive(rec.Owner.PID, tok)
}

func (l *Layer) leaseExpired(ref, oid string) bool {
	if l.Config.LeaseTTL <= 0 {
		return false
	}
	seen, ok := l.observedAt(ref, oid)
	return ok && l.Clock.Since(seen) >= l.Config.LeaseTTL
}

// judgeDeadOrExpired records the observation of a running record and returns
// why its holder is gone, or "" while it is alive.
func (l *Layer) judgeDeadOrExpired(st ReadState) Why {
	if st.Rec == nil || st.Rec.State != StateRunning {
		return ""
	}
	if l.pidGone(st.Rec) {
		return WhyDead
	}
	l.observe(st.Ref, st.OID)
	if l.leaseExpired(st.Ref, st.OID) {
		return WhyExpired
	}
	return ""
}

// Judge returns why a read lock's holder is gone ("" while alive), recording
// the observation: the waiter's per-second poll calls it.
func (l *Layer) Judge(st ReadState) Why { return l.judgeDeadOrExpired(st) }

// Classify returns the freshness class of a read lock as this run observes it
// (without recording an observation, so status never starts a lease clock):
// "" for a record that is absent, stopped or unreadable.
func (l *Layer) Classify(st ReadState) Freshness {
	if st.Rec == nil || st.Rec.State != StateRunning {
		return ""
	}
	if l.pidGone(st.Rec) {
		return FreshnessDead
	}
	if l.Host != nil && st.Rec.Owner.Host != l.Host.Name() {
		return FreshnessForeignHost
	}
	if _, ok := l.observedAt(st.Ref, st.OID); !ok {
		return FreshnessUnobserved
	}
	return FreshnessFresh
}

var errRetryRead = errors.New("lock changed while judging: re-read")

func (l *Layer) emitConversion(c Conversion) {
	if l.Out != nil {
		fmt.Fprintf(l.Out, "lock %s: holder of op %s (pid %d on %s) is %s; %s\n",
			c.Lock, c.OpID, c.PreviousOwner.PID, c.PreviousOwner.Host, c.Why, map[bool]string{true: "taken over", false: "converted to stopped"}[c.Takeover])
	}
	if l.OnConvert != nil {
		l.OnConvert(c)
	}
}

// takeOver handles a running record whose holder is dead or whose lease
// expired (section 8.6). A TARGET-role (or admin) record is replaced by a
// compare-and-swap against the stale record read and the new Held returned.
// A BRANCH- or worktree-role record is converted, with its companions, to
// stopped in one transaction and the waiter is refused with holder-died.
// (nil, nil) means nothing could be converted (the BRANCH record the worktree
// record names is gone): the lock stays an ordinary running conflict.
// errRetryRead means the ref changed hands: re-read.
func (l *Layer) takeOver(ctx context.Context, s Spec, st ReadState) (*Held, error) {
	why := l.judgeDeadOrExpired(st)
	switch st.Rec.Role {
	case RoleTarget, RoleAdmin:
		rec := s.Rec.Clone()
		rec.Role, rec.State = s.Role, StateRunning
		l.stamp(&rec)
		oid, err := l.write(ctx, s.Ref, rec, st.OID, nil)
		if errors.Is(err, ErrTaken) {
			return nil, errRetryRead
		}
		if err != nil {
			return nil, err
		}
		l.emitConversion(Conversion{Lock: s.Ref, PreviousOwner: st.Rec.Owner, Why: why, OpID: st.Rec.OpID, Takeover: true, Refs: []string{s.Ref}})
		return &Held{l: l, Ref: s.Ref, rec: rec, oid: oid, lastRenew: l.Clock.Now()}, nil
	case RoleBranch, RoleWorktree:
		done, err := l.convert(ctx, st, why)
		if err != nil {
			return nil, err
		}
		if !done {
			return nil, nil
		}
		h, _ := l.holderOf(st.Rec)
		return nil, &RefusedError{Ref: s.Ref, Reason: outcome.RefHolderDied, Holder: h}
	}
	return nil, nil
}

type txnEdit struct {
	ref, old string
	rec      *Record // nil: delete
}

func (l *Layer) stoppedCopy(r Record, at time.Time, why Why) Record {
	n := r.Clone()
	n.State, n.Lease = StateStopped, nil
	if r.Role == RoleBranch {
		n.CurrentCommand = nil
		msg := "holder died"
		if why == WhyExpired {
			msg = "holder's lease expired"
		}
		n.Stop = &Stop{
			Reason: string(outcome.StopHolderDied), Retryable: outcome.Stops[outcome.StopHolderDied].Retryable,
			At: at.UTC().Truncate(time.Second), Stage: r.Stage, Paths: []string{}, Message: msg,
		}
	}
	return n
}

// convert turns a dead or expired operation into a stopped one in ONE
// update-ref --stdin transaction: its BRANCH record and worktree records
// become stopped (reason holder-died) and its TARGET record is deleted if it
// still holds the dead owner. It reports whether anything was converted.
func (l *Layer) convert(ctx context.Context, trigger ReadState, why Why) (bool, error) {
	for attempt := 0; ; attempt++ {
		edits, err := l.planConversion(ctx, trigger, why)
		if err != nil || len(edits) == 0 {
			return false, err
		}
		var in strings.Builder
		for i := range edits {
			e := &edits[i]
			if e.rec == nil {
				fmt.Fprintf(&in, "delete %s %s\n", e.ref, e.old)
				continue
			}
			blob, err := l.hashBlob(ctx, *e.rec)
			if err != nil {
				return false, err
			}
			fmt.Fprintf(&in, "update %s %s %s\n", e.ref, blob, e.old)
		}
		r := l.Git.RunInput(ctx, l.Dir, []byte(in.String()), "update-ref", "--stdin")
		if r.OK() {
			refs := make([]string, len(edits))
			for i, e := range edits {
				refs[i] = e.ref
			}
			l.emitConversion(Conversion{Lock: trigger.Ref, PreviousOwner: trigger.Rec.Owner, Why: why, OpID: trigger.Rec.OpID, Refs: refs})
			return true, nil
		}
		// Re-read (section 8.4): a changed ref means another run converted or
		// the holder revived; re-plan from fresh reads. An unchanged one is
		// git's own lock: back off and retry.
		changed := false
		for _, e := range edits {
			cur, err := l.Read(ctx, e.ref)
			if err != nil {
				return false, err
			}
			if cur.OID != e.old {
				changed = true
			}
		}
		if changed {
			cur, err := l.Read(ctx, trigger.Ref)
			if err != nil {
				return false, err
			}
			if cur.OID != trigger.OID {
				return false, errRetryRead
			}
			continue
		}
		if attempt >= WriteRetries {
			return false, fmt.Errorf("convert %s: %w: %s", trigger.Ref, ErrWriteFailed, strings.TrimSpace(string(r.Stderr)))
		}
		if err := l.sleep(ctx, l.jitter(backoffBase<<attempt)); err != nil {
			return false, err
		}
	}
}

func sameOwner(a, b Owner) bool { return a.Token == b.Token && a.PID == b.PID && a.Host == b.Host }

// planConversion reads the records of the stale operation and lists the edits
// of its conversion.
func (l *Layer) planConversion(ctx context.Context, trigger ReadState, why Why) ([]txnEdit, error) {
	now := l.Clock.Now()
	rec := trigger.Rec
	bst := trigger
	if rec.Role != RoleBranch {
		var err error
		if bst, err = l.Read(ctx, BranchRef(rec.Branch)); err != nil {
			return nil, err
		}
	}
	if bst.Rec == nil || bst.Rec.Role != RoleBranch || bst.Rec.OpID != rec.OpID {
		return nil, nil
	}
	var edits []txnEdit
	seen := map[string]bool{}
	add := func(st ReadState, del bool) {
		if seen[st.Ref] {
			return
		}
		seen[st.Ref] = true
		if del {
			edits = append(edits, txnEdit{ref: st.Ref, old: st.OID})
			return
		}
		c := l.stoppedCopy(*st.Rec, now, why)
		edits = append(edits, txnEdit{ref: st.Ref, old: st.OID, rec: &c})
	}
	switch bst.Rec.State {
	case StateRunning:
		add(bst, false)
	case StateStopped:
		// already converted or stopped: only stragglers remain
	default:
		return nil, nil
	}
	if rec.Role == RoleWorktree {
		add(trigger, false)
	}
	for _, w := range bst.Rec.Worktrees {
		ws, err := l.Read(ctx, WorktreeRef(w.Admin))
		if err != nil {
			return nil, err
		}
		if ws.Rec != nil && ws.Rec.Role == RoleWorktree && ws.Rec.OpID == rec.OpID && ws.Rec.State == StateRunning {
			add(ws, false)
		}
	}
	if bst.Rec.Operation == "land" && bst.Rec.Target != "" {
		ts, err := l.Read(ctx, BranchRef(bst.Rec.Target))
		if err != nil {
			return nil, err
		}
		if ts.Rec != nil && ts.Rec.Role == RoleTarget && ts.Rec.State == StateRunning && ts.Rec.OpID == rec.OpID && sameOwner(ts.Rec.Owner, rec.Owner) {
			add(ts, true)
		}
	}
	return edits, nil
}
