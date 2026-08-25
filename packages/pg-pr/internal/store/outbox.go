package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// Tx is a store transaction with helpers to mutate state AND enqueue the event
// that resulted, so both commit or roll back together.
type Tx struct {
	tx  *sql.Tx
	ctx context.Context
}

// InTx runs fn in a transaction. If fn returns an error the tx rolls back (and
// any enqueued outbox rows vanish). On nil it commits.
func (db *DB) InTx(ctx context.Context, fn func(*Tx) error) error {
	sqlTx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin tx: %w", err)
	}
	t := &Tx{tx: sqlTx, ctx: ctx}
	if err := fn(t); err != nil {
		_ = sqlTx.Rollback()
		return err
	}
	if err := sqlTx.Commit(); err != nil {
		return fmt.Errorf("store: commit tx: %w", err)
	}
	return nil
}

// EnqueueEvent writes a pending outbox row inside the transaction.
func (t *Tx) EnqueueEvent(eventType string, payload json.RawMessage) error {
	_, err := t.tx.ExecContext(t.ctx,
		"INSERT INTO outbox (type, payload, status, created_at) VALUES (?,?, 'pending', ?)",
		eventType, string(payload), nowRFC3339())
	if err != nil {
		return fmt.Errorf("store: enqueue %s: %w", eventType, err)
	}
	return nil
}

// Exec runs a statement inside the transaction (used by *Tx mutator variants).
func (t *Tx) Exec(query string, args ...any) (sql.Result, error) {
	return t.tx.ExecContext(t.ctx, query, args...)
}

// QueryRow runs a single-row query inside the transaction.
func (t *Tx) QueryRow(query string, args ...any) *sql.Row {
	return t.tx.QueryRowContext(t.ctx, query, args...)
}

// DispatchFunc handles one event. Returning an error is ignored by RunOutbox —
// the row is marked complete regardless (fire-once / best-effort semantics).
// A crash between a successful dispatch and the completing UPDATE can re-dispatch
// the same event on the next run, so consumers should be idempotent.
type DispatchFunc func(ctx context.Context, e Event) error

// outboxLeaseDuration bounds how long a claimed-but-not-yet-completed row
// blocks another RunOutbox caller from claiming it. Without this, a process
// that crashes between the claiming UPDATE and the completing UPDATE would
// strand the row claimed forever. It is chosen well above any single
// dispatch's expected duration (in-process handler work, no network round
// trip on the hot path — flushOutbox callers do the network/bd I/O inside
// dispatch, but that is expected to complete in low seconds, not minutes) so
// a live claimer is never raced by a spurious steal, while a genuinely dead
// claimer's rows recover well within a daemon maintenance interval.
//
// Stealing an expired lease and redispatching is safe under the same
// fire-once contract DispatchFunc already documents (a crash between
// dispatch and the completing UPDATE can already redispatch on the next
// run), so this does not weaken any existing guarantee.
const outboxLeaseDuration = 10 * time.Minute

// newClaimToken returns a value identifying this RunOutbox call as the lease
// holder for whatever rows it claims. It only needs to be unique enough that
// two concurrent claimers (in this process or another) don't collide; it is
// not persisted or interpreted beyond equality checks, so pid+random bytes is
// sufficient (no coordination with any other identity scheme is needed).
func newClaimToken() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("pid%d-%s", os.Getpid(), hex.EncodeToString(b[:]))
}

// leaseCutoff returns, as an RFC3339 string comparable against claimed_at,
// the instant before which a claim is considered expired. It derives from
// nowRFC3339 (the package's single clock seam, overridable in tests) rather
// than time.Now directly.
func leaseCutoff(d time.Duration) string {
	now, err := time.Parse(time.RFC3339, nowRFC3339())
	if err != nil {
		now = time.Now().UTC()
	}
	return now.Add(-d).Format(time.RFC3339)
}

// RunOutbox pulls each pending-and-unclaimed (or staled-claim) row, atomically
// claims it, dispatches it, then marks it complete — regardless of the
// dispatch outcome (fire-once / best-effort, see DispatchFunc). Returns the
// first I/O error (not handler errors).
//
// The claim step is what makes two concurrent callers — e.g. the daemon and a
// one-shot `pg-pr sync`, which take none of the same locks (pg2-g42k5) — safe
// against double-dispatching the same row: the claiming UPDATE is
// conditioned on the row still being pending-and-unclaimed-or-stale, and its
// RowsAffected count tells this caller whether it actually won the row or
// lost the race to a concurrent claimer.
//
// A lost row is only skipped (left for that other caller to dispatch) once it
// is confirmed already status='complete'. If it is still in flight — claimed,
// not yet complete — this call STOPS here rather than continuing on to a
// LATER row from its own snapshot (pg2-scl9p): dispatching id N+k while a
// concurrent claimer is still mid-dispatch of the earlier id N would violate
// the FIFO-id-order guarantee every caller relies on for a same-identity event
// pair (e.g. beadsbridge's pr.opened → feedback.created invariant, proved by
// internal/sync's TestConcurrentFlushNeverMissesPRBead). Every row at or after
// the stop point — including the contended one — is left pending for a later
// RunOutbox call (the caller's own next tick, another concurrent drainer's
// pass, or a final synchronous drain) once the in-flight dispatch completes.
// This costs only the throughput of one drainer's pass on a genuinely
// contended row; an uncontended row is claimed and dispatched exactly as
// before.
func (db *DB) RunOutbox(ctx context.Context, dispatch DispatchFunc) error {
	cutoff := leaseCutoff(outboxLeaseDuration)
	rows, err := db.sql.QueryContext(ctx,
		`SELECT id, type, payload FROM outbox
		 WHERE status='pending' AND (claimed_by IS NULL OR claimed_at < ?)
		 ORDER BY id`, cutoff)
	if err != nil {
		return fmt.Errorf("store: select pending outbox: %w", err)
	}
	type pend struct {
		id int64
		e  Event
	}
	var pending []pend
	for rows.Next() {
		var p pend
		var payload string
		if err := rows.Scan(&p.id, &p.e.Type, &payload); err != nil {
			_ = rows.Close()
			return fmt.Errorf("store: scan outbox: %w", err)
		}
		p.e.Payload = json.RawMessage(payload)
		pending = append(pending, p)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	token := newClaimToken()
	for _, p := range pending {
		res, err := db.sql.ExecContext(ctx,
			`UPDATE outbox SET claimed_by=?, claimed_at=?
			 WHERE id=? AND status='pending' AND (claimed_by IS NULL OR claimed_at < ?)`,
			token, nowRFC3339(), p.id, cutoff)
		if err != nil {
			return fmt.Errorf("store: claim outbox %d: %w", p.id, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("store: claim outbox %d rows affected: %w", p.id, err)
		}
		if n == 0 {
			// Lost the race: another caller claimed (or already completed)
			// this row between our SELECT and this UPDATE. Not ours to
			// dispatch.
			//
			// Whether we may continue to a LATER row in our own snapshot
			// depends on whether this one already finished: if it's already
			// complete, whoever dispatched it did so before we could have
			// raced ahead of it, so ordering is unaffected. If it's still in
			// flight, continuing would risk dispatching a higher-id row
			// concurrently with (or before) this still-pending lower-id one —
			// see the FIFO-order note on RunOutbox above.
			var status string
			if err := db.sql.QueryRowContext(ctx,
				`SELECT status FROM outbox WHERE id=?`, p.id).Scan(&status); err != nil {
				return fmt.Errorf("store: check outbox %d status: %w", p.id, err)
			}
			if status == "complete" {
				continue
			}
			break
		}

		_ = dispatch(ctx, p.e)

		// Complete only if we still hold the lease. Losing it here means our
		// dispatch outlived outboxLeaseDuration and another caller already
		// stole and (re)dispatched the row — the same already-documented
		// fire-once risk as a mid-flight crash, not a new hazard.
		if _, err := db.sql.ExecContext(ctx,
			`UPDATE outbox SET status='complete', completed_at=?, claimed_by=NULL, claimed_at=NULL
			 WHERE id=? AND claimed_by=?`,
			nowRFC3339(), p.id, token); err != nil {
			return fmt.Errorf("store: complete outbox %d: %w", p.id, err)
		}
	}
	return nil
}
