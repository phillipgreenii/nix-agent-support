package ledger

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Topic event kinds (the topic_event table).
const (
	EventAutoDeferred = "auto-deferred"
	EventReconsidered = "reconsidered"
	EventPromoted     = "promoted"
	EventWaiting      = "waiting"
)

// Reconsideration defaults.
const (
	// DeferredFirstReturnDays is the first return of a Deferred topic with no
	// recorded condition; each later return doubles it up to the cap.
	DeferredFirstReturnDays = 30
	// DeferredMaxReturnDays caps the backoff.
	DeferredMaxReturnDays = 120
	// RejectedRateMultiple is the rate multiple at which a Rejected topic with
	// no recorded condition returns.
	RejectedRateMultiple = 2.0
)

const day = 24 * time.Hour

// Observation is what the current run saw of a topic, used to evaluate
// reconsideration. It is derived from the evidence rows the finding cites.
type Observation struct {
	Now time.Time
	// Rate and Cost are the largest "rate" and "cost" metrics of the cited
	// evidence rows; HasRate and HasCost say whether any row carried one.
	Rate, Cost       float64
	HasRate, HasCost bool
	// TargetSeenAt is the latest last_seen of the cited evidence rows: the
	// time the target (the subject's defining file) was last seen changing.
	TargetSeenAt time.Time
	// VersionChanged reports that the cited rows' "version" metric differs
	// from the one of the topic's previous occurrence.
	VersionChanged bool
}

// Trigger clause names in a recorded reconsider-when condition. A condition
// is clauses separated by ";" or ","; any one firing returns the topic.
//
//	date:2026-12-01            on or after a date (YYYY-MM-DD or RFC 3339)
//	rate-multiple:2            the rate reaches this multiple of the rate at decision
//	cost:5000                  the cost reaches this threshold
//	target-changed             the target file changed after the decision
//	version-changed            a version changed (version-sensitive topics only)
//
// Free text the grammar does not recognise is ignored; a condition with no
// recognised clause counts as none recorded, so the defaults apply.
type clause struct {
	name string
	arg  string
}

func parseCondition(s string) []clause {
	var out []clause
	for _, part := range strings.FieldsFunc(s, func(r rune) bool { return r == ';' || r == ',' }) {
		part = strings.TrimSpace(part)
		name, arg, _ := strings.Cut(part, ":")
		name, arg = strings.ToLower(strings.TrimSpace(name)), strings.TrimSpace(arg)
		switch name {
		case "date", "rate-multiple", "cost":
			if arg != "" {
				out = append(out, clause{name, arg})
			}
		case "target-changed", "version-changed":
			out = append(out, clause{name, ""})
		}
	}
	return out
}

func parseDate(s string) (time.Time, bool) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// reconsideration decides whether a decided topic returns to Pending. d is
// the ruling the topic stands on (zero when it was deferred automatically),
// returns is how many times the topic has already been reconsidered, and
// since is when it entered its current state. It returns the reason when a
// trigger fired.
func reconsideration(tp Topic, d Decision, hasDecision bool, returns int, since time.Time, o Observation) (string, bool) {
	var clauses []clause
	var decided time.Time
	var rate float64
	if hasDecision {
		clauses = parseCondition(d.ReconsiderWhen)
		decided = parseTime(d.Decided)
		rate, _ = strconv.ParseFloat(strings.TrimSpace(d.RateAtDecision), 64)
	}
	if len(clauses) == 0 {
		switch tp.State {
		case StateDeferred:
			days := DeferredFirstReturnDays
			for i := 0; i < returns && days < DeferredMaxReturnDays; i++ {
				days *= 2
			}
			if days > DeferredMaxReturnDays {
				days = DeferredMaxReturnDays
			}
			if !since.IsZero() && o.Now.Sub(since) >= time.Duration(days)*day {
				return fmt.Sprintf("deferred for %d days", days), true
			}
		case StateRejected:
			if o.HasRate && rate > 0 && o.Rate >= RejectedRateMultiple*rate {
				return fmt.Sprintf("rate %g reached twice the rate at decision %g", o.Rate, rate), true
			}
		}
		return "", false
	}
	for _, c := range clauses {
		switch c.name {
		case "date":
			if t, ok := parseDate(c.arg); ok && !o.Now.Before(t) {
				return "date " + c.arg + " reached", true
			}
		case "rate-multiple":
			m, err := strconv.ParseFloat(c.arg, 64)
			if err == nil && o.HasRate && rate > 0 && o.Rate >= m*rate {
				return fmt.Sprintf("rate %g reached %g times the rate at decision %g", o.Rate, m, rate), true
			}
		case "cost":
			th, err := strconv.ParseFloat(c.arg, 64)
			if err == nil && o.HasCost && o.Cost >= th {
				return fmt.Sprintf("cost %g reached the threshold %g", o.Cost, th), true
			}
		case "target-changed":
			if !o.TargetSeenAt.IsZero() && !decided.IsZero() && o.TargetSeenAt.After(decided) {
				return "target file changed", true
			}
		case "version-changed":
			if tp.VersionSensitive && o.VersionChanged {
				return "version changed", true
			}
		}
	}
	return "", false
}

// addEvent records an automatic state change of a topic.
func (t *Tx) addEvent(ctx context.Context, topicID, kind, note string) error {
	_, err := t.exec(ctx, `INSERT INTO topic_event(topic_id,kind,note,created_at) VALUES(?,?,?,?)`,
		topicID, kind, note, fmtTime(t.now()))
	return err
}

// TopicEvent is one automatic state change of a topic.
type TopicEvent struct {
	Kind      string
	Note      string
	CreatedAt time.Time
}

// TopicEvents returns a topic's automatic state changes, oldest first.
func (l *Ledger) TopicEvents(ctx context.Context, topicID string) ([]TopicEvent, error) {
	rows, err := l.db.QueryContext(ctx, `SELECT kind,note,created_at FROM topic_event WHERE topic_id=? ORDER BY id`, topicID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TopicEvent
	for rows.Next() {
		var e TopicEvent
		var at string
		if err := rows.Scan(&e.Kind, &e.Note, &at); err != nil {
			return nil, err
		}
		e.CreatedAt = parseTime(at)
		out = append(out, e)
	}
	return out, rows.Err()
}

// latestDecisionTx returns the newest ruling of a topic and whether there is one.
func (t *Tx) latestDecision(ctx context.Context, topicID string) (Decision, bool, error) {
	d, err := scanDecision(t.queryRow(ctx, decisionSelect+` WHERE topic_id=? ORDER BY id DESC LIMIT 1`, topicID))
	if errors.Is(err, ErrNotFound) {
		return Decision{}, false, nil
	}
	return d, err == nil, err
}

// lastEventTime returns the time of the topic's newest event of a kind.
func (t *Tx) lastEventTime(ctx context.Context, topicID, kind string) (time.Time, error) {
	var s string
	err := t.queryRow(ctx, `SELECT created_at FROM topic_event WHERE topic_id=? AND kind=? ORDER BY id DESC LIMIT 1`, topicID, kind).Scan(&s)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, nil
	}
	return parseTime(s), err
}

func (t *Tx) countEvents(ctx context.Context, topicID, kind string) (int, error) {
	var n int
	err := t.queryRow(ctx, `SELECT COUNT(*) FROM topic_event WHERE topic_id=? AND kind=?`, topicID, kind).Scan(&n)
	return n, err
}

// Reconsider evaluates a Deferred or Rejected topic against what the current
// run saw. When a trigger fired it records the reconsideration (the caller
// moves the topic to Pending or Waiting) and returns the reason.
func (t *Tx) Reconsider(ctx context.Context, tp Topic, o Observation) (string, bool, error) {
	if tp.State != StateDeferred && tp.State != StateRejected {
		return "", false, nil
	}
	d, has, err := t.latestDecision(ctx, tp.ID)
	if err != nil {
		return "", false, err
	}
	// A ruling other than the one the topic's state stands on (a Reopen, or
	// an automatic deferral after the ruling) carries no recorded condition.
	if has {
		auto, err := t.lastEventTime(ctx, tp.ID, EventAutoDeferred)
		if err != nil {
			return "", false, err
		}
		if (d.Ruling != RulingDeferred && d.Ruling != RulingRejected) || (!auto.IsZero() && !auto.Before(parseTime(d.Decided))) {
			has = false
		}
	}
	returns, err := t.countEvents(ctx, tp.ID, EventReconsidered)
	if err != nil {
		return "", false, err
	}
	reason, fired := reconsideration(tp, d, has, returns, tp.UpdatedAt, o)
	return reason, fired, nil
}

// AutoDefer defers every Pending topic that has been Pending for days or more
// (measured from its last revision into Pending), with the note "deferred
// automatically: no ruling for N days". It is not a ruling: no decision row is
// written, so it does not lift a lens pause. days <= 0 disables it. It
// returns the deferred topic ids.
func (t *Tx) AutoDefer(ctx context.Context, days int) ([]string, error) {
	if days <= 0 {
		return nil, nil
	}
	rows, err := t.query(ctx, `SELECT topic_id,updated_at FROM topic WHERE state='Pending' ORDER BY seq`)
	if err != nil {
		return nil, err
	}
	type cand struct {
		id   string
		days int
	}
	var cands []cand
	now := t.now()
	for rows.Next() {
		var id, upd string
		if err := rows.Scan(&id, &upd); err != nil {
			_ = rows.Close()
			return nil, err
		}
		age := now.Sub(parseTime(upd))
		if age >= time.Duration(days)*day {
			cands = append(cands, cand{id, int(age / day)})
		}
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	var ids []string
	for _, c := range cands {
		if err := t.Transition(ctx, c.id, StateDeferred); err != nil {
			return nil, err
		}
		if err := t.addEvent(ctx, c.id, EventAutoDeferred, fmt.Sprintf("deferred automatically: no ruling for %d days", c.days)); err != nil {
			return nil, err
		}
		ids = append(ids, c.id)
	}
	return ids, nil
}

// AutoDefer runs Tx.AutoDefer in its own transaction. pending and maintain
// call it.
func (l *Ledger) AutoDefer(ctx context.Context, days int) ([]string, error) {
	var ids []string
	err := l.WithTx(ctx, func(t *Tx) error {
		var err error
		ids, err = t.AutoDefer(ctx, days)
		return err
	})
	return ids, err
}

// PromoteWaiting promotes Waiting topics to Pending, oldest first, up to
// maxPending, in its own transaction. rule, the start of every run and
// maintain call it. It never lets the Pending count exceed maxPending.
func (l *Ledger) PromoteWaiting(ctx context.Context, maxPending int) ([]string, error) {
	var ids []string
	err := l.WithTx(ctx, func(t *Tx) error {
		var err error
		ids, err = t.PromoteWaitingLogged(ctx, maxPending)
		return err
	})
	return ids, err
}

// PromoteWaitingLogged is Tx.PromoteWaiting that also records a promoted event
// for each topic.
func (t *Tx) PromoteWaitingLogged(ctx context.Context, maxPending int) ([]string, error) {
	ids, err := t.PromoteWaiting(ctx, maxPending)
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		if err := t.addEvent(ctx, id, EventPromoted, "promoted from Waiting"); err != nil {
			return nil, err
		}
	}
	return ids, nil
}
