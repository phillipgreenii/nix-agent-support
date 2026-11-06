package ledger

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/validate"
)

var (
	// ErrNotShown means the operator has not shown the item since it last
	// changed (no stored view): "show it first".
	ErrNotShown = errors.New("ledger: show it first")
	// ErrStaleView means the item changed after the operator last saw it.
	ErrStaleView = errors.New("ledger: the item changed since it was shown; show it again")
	// ErrNotRulable means the item cannot take a ruling in its state (Waiting).
	ErrNotRulable = errors.New("ledger: the item is not pending")
)

// Item is a topic with its newest stored write-up, as the human commands see
// it. Number is the integer the operator types (the topic's sequence number).
type Item struct {
	Number    int64
	TopicID   string
	Lens      string
	Kind      string
	State     string
	Revision  int
	UpdatedAt time.Time
	RunID     int64
	Scope     string
	Writeup   validate.Finding
	Rendered  string
	// Evidence is the number of evidence rows the write-up cites.
	Evidence int
}

const itemSelect = `SELECT t.seq,t.topic_id,t.lens,t.kind,t.state,t.revision,t.updated_at,
  r.run_id, COALESCE((SELECT o.scope FROM occurrence o WHERE o.topic_id=t.topic_id ORDER BY o.id DESC LIMIT 1),''),
  r.writeup_json, r.writeup_rendered
FROM topic t JOIN revision r ON r.id=(SELECT MAX(id) FROM revision WHERE topic_id=t.topic_id)`

type rowScanner interface{ Scan(...any) error }

func scanItem(s rowScanner) (Item, error) {
	var it Item
	var upd, wj string
	if err := s.Scan(&it.Number, &it.TopicID, &it.Lens, &it.Kind, &it.State, &it.Revision, &upd,
		&it.RunID, &it.Scope, &wj, &it.Rendered); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Item{}, ErrNotFound
		}
		return Item{}, err
	}
	it.UpdatedAt = parseTime(upd)
	if err := json.Unmarshal([]byte(wj), &it.Writeup); err != nil {
		return Item{}, fmt.Errorf("%w: stored write-up of item %d: %v", ErrIntegrity, it.Number, err)
	}
	it.Evidence = len(it.Writeup.Evidence)
	return it, nil
}

// ItemByNumber returns one item or ErrNotFound.
func (l *Ledger) ItemByNumber(ctx context.Context, n int64) (Item, error) {
	return scanItem(l.db.QueryRowContext(ctx, itemSelect+` WHERE t.seq=?`, n))
}

func (t *Tx) itemByNumber(ctx context.Context, n int64) (Item, error) {
	return scanItem(t.queryRow(ctx, itemSelect+` WHERE t.seq=?`, n))
}

// PendingItems lists the Pending items ordered by evidence strength (the
// number of evidence rows cited, most first), then age (oldest first).
func (l *Ledger) PendingItems(ctx context.Context) ([]Item, error) {
	rows, err := l.db.QueryContext(ctx, itemSelect+` WHERE t.state='Pending'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Item
	for rows.Next() {
		it, err := scanItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Evidence != out[j].Evidence {
			return out[i].Evidence > out[j].Evidence
		}
		if !out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].UpdatedAt.Before(out[j].UpdatedAt)
		}
		return out[i].Number < out[j].Number
	})
	return out, nil
}

// DecidedEntry is one earlier ruling for `pending --decided`.
type DecidedEntry struct {
	Number    int64
	Lens      string
	Title     string
	Ruling    string
	Decided   time.Time
	Rationale string
	BeadState string
	WorkBead  string
}

// DecidedItems lists every stored ruling, newest first.
func (l *Ledger) DecidedItems(ctx context.Context) ([]DecidedEntry, error) {
	rows, err := l.db.QueryContext(ctx, `SELECT t.seq,t.lens,d.ruling,d.decided,d.rationale,d.bead_state,d.work_bead,
		COALESCE((SELECT writeup_json FROM revision WHERE topic_id=t.topic_id ORDER BY id DESC LIMIT 1),'{}')
		FROM decision d JOIN topic t ON t.topic_id=d.topic_id ORDER BY d.id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DecidedEntry
	for rows.Next() {
		var e DecidedEntry
		var at, wj string
		if err := rows.Scan(&e.Number, &e.Lens, &e.Ruling, &at, &e.Rationale, &e.BeadState, &e.WorkBead, &wj); err != nil {
			return nil, err
		}
		e.Decided = parseTime(at)
		var f validate.Finding
		_ = json.Unmarshal([]byte(wj), &f)
		e.Title = f.Title
		out = append(out, e)
	}
	return out, rows.Err()
}

// ShowItems records a view of every id in ONE transaction and returns the
// items as shown. An unknown id fails the whole call with ErrNotFound and
// stores no view.
func (l *Ledger) ShowItems(ctx context.Context, numbers []int64) ([]Item, error) {
	var out []Item
	err := l.WithTx(ctx, func(t *Tx) error {
		out = out[:0]
		for _, n := range numbers {
			it, err := t.itemByNumber(ctx, n)
			if err != nil {
				return fmt.Errorf("item %d: %w", n, err)
			}
			if _, err := t.RecordView(ctx, it.TopicID); err != nil {
				return err
			}
			out = append(out, it)
		}
		return nil
	})
	return out, err
}

// ValidCondition reports whether a reconsider-when text holds at least one
// clause of the recorded grammar.
func ValidCondition(s string) bool { return len(parseCondition(s)) > 0 }

// RuleRequest is one item's ruling.
type RuleRequest struct {
	Number         int64
	Ruling         string // one of the Ruling* constants
	Rationale      string
	ReconsiderWhen string
	Useful         string // "", "yes" or "no"
	// For an Adopted ruling: the tracker root and repo label the bead files
	// with. An empty label is stored and fails the filing visibly.
	TargetTracker string
	RepoLabel     string
}

// RuleResult is what a ruling did to one item.
type RuleResult struct {
	Number int64
	Title  string
	State  string
	// Unchanged is true when the item already stood on the ruling.
	Unchanged  bool
	DecisionID int64
}

// RuleItems applies the rulings in ONE transaction: every item must have a
// stored view whose revision is the current one (else ErrNotShown or
// ErrStaleView and nothing changes). An item already terminal on the same
// ruling is reported Unchanged and not touched. After the rulings Waiting
// topics are promoted up to maxPending. The rulings are then appended to the
// rulings log (a failure of the log is returned alongside the results).
func (l *Ledger) RuleItems(ctx context.Context, reqs []RuleRequest, maxPending int) ([]RuleResult, error) {
	var out []RuleResult
	var logs []RulingLogEntry
	err := l.WithTx(ctx, func(t *Tx) error {
		out, logs = out[:0], logs[:0]
		for _, rq := range reqs {
			it, err := t.itemByNumber(ctx, rq.Number)
			if err != nil {
				return fmt.Errorf("item %d: %w", rq.Number, err)
			}
			res := RuleResult{Number: rq.Number, Title: it.Writeup.Title}
			if t.alreadyOn(ctx, it, rq.Ruling) {
				res.State, res.Unchanged = it.State, true
				out = append(out, res)
				continue
			}
			if it.State == StateWaiting {
				return fmt.Errorf("item %d: %w", rq.Number, ErrNotRulable)
			}
			var seen int
			err = t.queryRow(ctx, `SELECT revision_seen FROM view WHERE topic_id=?`, it.TopicID).Scan(&seen)
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("item %d: %w", rq.Number, ErrNotShown)
			}
			if err != nil {
				return err
			}
			if seen != it.Revision {
				return fmt.Errorf("item %d: %w", rq.Number, ErrStaleView)
			}
			var rate string
			var mj string
			if err := t.queryRow(ctx, `SELECT metrics FROM occurrence WHERE topic_id=? ORDER BY id DESC LIMIT 1`, it.TopicID).Scan(&mj); err == nil {
				var m map[string]float64
				if json.Unmarshal([]byte(mj), &m) == nil {
					if v, ok := m["rate"]; ok {
						rate = fmt.Sprintf("%g", v)
					}
				}
			}
			d, err := t.RecordRuling(ctx, Ruling{
				TopicID: it.TopicID, Ruling: rq.Ruling, Rationale: rq.Rationale,
				ReconsiderWhen: rq.ReconsiderWhen, RateAtDecision: rate, Useful: rq.Useful,
				TargetTracker: rq.TargetTracker, RepoLabel: rq.RepoLabel,
			})
			if err != nil {
				return err
			}
			tp, err := t.GetTopic(ctx, it.TopicID)
			if err != nil {
				return err
			}
			if rq.Ruling == RulingAdopted {
				key := BeadKey(d.ID, tp.Revision)
				if _, err := t.exec(ctx, `UPDATE decision SET bead_idempotency_key=? WHERE id=?`, key, d.ID); err != nil {
					return err
				}
			} else if err := t.resetBeads(ctx, it.TopicID, d.ID); err != nil {
				return err
			}
			res.State, res.DecisionID = tp.State, d.ID
			out = append(out, res)
			logs = append(logs, RulingLogEntry{
				TopicID: it.TopicID, Lens: it.Lens, Ruling: rq.Ruling,
				Rationale: rq.Rationale, ReconsiderWhen: rq.ReconsiderWhen,
			})
		}
		_, err := t.PromoteWaitingLogged(ctx, maxPending)
		return err
	})
	if err != nil {
		return nil, err
	}
	for _, e := range logs {
		if lerr := l.AppendRulingLog(e); lerr != nil {
			return out, lerr
		}
	}
	return out, nil
}

// BeadKey is the idempotency key of an Adopted decision: its id and the
// topic revision the ruling produced.
func BeadKey(decisionID int64, revision int) string {
	return fmt.Sprintf("pg-review:decision-%d:rev-%d", decisionID, revision)
}

// alreadyOn reports that the item already stands on the ruling: its state
// matches and its newest ruling is the same one (or, for Reopen, it is Pending).
func (t *Tx) alreadyOn(ctx context.Context, it Item, ruling string) bool {
	d, has, err := t.latestDecision(ctx, it.TopicID)
	if err != nil {
		return false
	}
	switch ruling {
	case RulingReopen:
		return it.State == StatePending
	case RulingAdopted:
		return it.State == StateAdopted && has && d.Ruling == RulingAdopted
	case RulingDeferred:
		return it.State == StateDeferred && has && d.Ruling == RulingDeferred
	case RulingRejected:
		return it.State == StateRejected && has && d.Ruling == RulingRejected
	}
	return false
}

// resetBeads cancels the unfinished beads of the topic's earlier Adopted
// decisions when the item is ruled otherwise: pending, filing and failed
// states go back to none with a zero attempt count. A filed bead stays.
func (t *Tx) resetBeads(ctx context.Context, topicID string, except int64) error {
	_, err := t.exec(ctx, `UPDATE decision SET bead_state='none', bead_attempts=0, bead_lease_expires_at=''
		WHERE topic_id=? AND id<>? AND bead_state IN ('pending','filing','failed')`, topicID, except)
	return err
}

// StaleLenses returns the lenses with a stale schedule: for some schedule
// (runs sharing a schedule id) the time since its last Filed run ended exceeds
// twice that run's window. Runs without a schedule id are ignored.
func (l *Ledger) StaleLenses(ctx context.Context) ([]string, error) {
	rows, err := l.db.QueryContext(ctx, `SELECT lens, schedule_id, window_from, window_to, ended_at, created_at
		FROM run WHERE state='Filed' AND schedule_id<>'' ORDER BY seq`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type key struct{ lens, sched string }
	type last struct {
		window time.Duration
		at     time.Time
	}
	latest := map[key]last{}
	for rows.Next() {
		var lens, sched, from, to, ended, created string
		if err := rows.Scan(&lens, &sched, &from, &to, &ended, &created); err != nil {
			return nil, err
		}
		f, e1 := time.Parse(time.RFC3339Nano, from)
		tt, e2 := time.Parse(time.RFC3339Nano, to)
		if e1 != nil || e2 != nil || !tt.After(f) {
			continue
		}
		at := parseTime(ended)
		if at.IsZero() {
			at = parseTime(created)
		}
		latest[key{lens, sched}] = last{tt.Sub(f), at}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	now := l.now()
	set := map[string]bool{}
	for k, v := range latest {
		if now.Sub(v.at) > 2*v.window {
			set[k.lens] = true
		}
	}
	var out []string
	for lens := range set {
		out = append(out, lens)
	}
	sort.Strings(out)
	return out, nil
}

// LastRun is the newest run of a lens, for the pending banner.
type LastRun struct {
	Lens  string
	State string
	At    time.Time
}

// LastRuns returns the newest run of every lens that has one.
func (l *Ledger) LastRuns(ctx context.Context) ([]LastRun, error) {
	rows, err := l.db.QueryContext(ctx, `SELECT lens,state,created_at FROM run r
		WHERE seq=(SELECT MAX(seq) FROM run WHERE lens=r.lens) ORDER BY lens`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LastRun
	for rows.Next() {
		var r LastRun
		var at string
		if err := rows.Scan(&r.Lens, &r.State, &at); err != nil {
			return nil, err
		}
		r.At = parseTime(at)
		out = append(out, r)
	}
	return out, rows.Err()
}

// Lenses returns every lens that has a topic or a run.
func (l *Ledger) Lenses(ctx context.Context) ([]string, error) {
	rows, err := l.db.QueryContext(ctx, `SELECT lens FROM topic UNION SELECT lens FROM run ORDER BY 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// Now returns the ledger clock, so callers share the injectable seam.
func (l *Ledger) Now() time.Time { return l.now().UTC() }

var _ = strings.TrimSpace
