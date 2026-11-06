package ledger

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// Topic states.
const (
	StatePending  = "Pending"
	StateAdopted  = "Adopted"
	StateDeferred = "Deferred"
	StateRejected = "Rejected"
	StateWaiting  = "Waiting"
)

// Rulings. RulingReopen returns a decided topic to Pending.
const (
	RulingAdopted  = "Adopted"
	RulingDeferred = "Deferred"
	RulingRejected = "Rejected"
	RulingReopen   = "Reopen"
)

// Bead states of a decision (the outbox is the decision record itself).
const (
	BeadNone    = "none"
	BeadPending = "pending"
	BeadFiling  = "filing"
	BeadFiled   = "filed"
	BeadFailed  = "failed"
)

// TopicID is the mechanical topic identity: SHA-256, lowercase hex, over
// defined_in, subject_key and kind joined by a NUL byte.
func TopicID(definedIn, subjectKey, kind string) string {
	h := sha256.New()
	h.Write([]byte(definedIn))
	h.Write([]byte{0})
	h.Write([]byte(subjectKey))
	h.Write([]byte{0})
	h.Write([]byte(kind))
	return hex.EncodeToString(h.Sum(nil))
}

// Topic is one item the operator rules on.
type Topic struct {
	ID               string
	DefinedIn        string
	SubjectKey       string
	Kind             string
	Lens             string
	State            string
	Revision         int
	VersionSensitive bool
	Seq              int64
	// UpdatedAt is the time of the topic's last revision.
	UpdatedAt time.Time
}

// NewTopic describes a topic to create.
type NewTopic struct {
	DefinedIn        string
	SubjectKey       string
	SubjectType      string
	Kind             string
	Lens             string
	VersionSensitive bool
	// Waiting creates the topic in state Waiting (the pending cap is reached)
	// instead of Pending.
	Waiting bool
}

// CreateTopic upserts the subject and creates the topic at revision 1. A topic
// with the same identity yields ErrDuplicate.
func (t *Tx) CreateTopic(ctx context.Context, n NewTopic) (Topic, error) {
	if n.DefinedIn == "" || n.SubjectKey == "" || n.Kind == "" || n.Lens == "" {
		return Topic{}, fmt.Errorf("%w: topic needs defined_in, subject_key, kind and lens", ErrInvalid)
	}
	now := fmtTime(t.now())
	if _, err := t.exec(ctx, `INSERT INTO subject(defined_in,key,type,first_seen,last_seen)
		VALUES(?,?,?,?,?)
		ON CONFLICT(defined_in,key) DO UPDATE SET last_seen=excluded.last_seen,
		  type=CASE WHEN excluded.type<>'' THEN excluded.type ELSE subject.type END`,
		n.DefinedIn, n.SubjectKey, n.SubjectType, now, now); err != nil {
		return Topic{}, err
	}
	seq, err := t.nextSeq(ctx)
	if err != nil {
		return Topic{}, err
	}
	state := StatePending
	if n.Waiting {
		state = StateWaiting
	}
	id := TopicID(n.DefinedIn, n.SubjectKey, n.Kind)
	vs := 0
	if n.VersionSensitive {
		vs = 1
	}
	if _, err := t.exec(ctx, `INSERT INTO topic(topic_id,defined_in,subject_key,kind,lens,state,revision,version_sensitive,seq,created_at,updated_at)
		VALUES(?,?,?,?,?,?,1,?,?,?,?)`,
		id, n.DefinedIn, n.SubjectKey, n.Kind, n.Lens, state, vs, seq, now, now); err != nil {
		return Topic{}, err
	}
	return t.GetTopic(ctx, id)
}

// GetTopic returns one topic or ErrNotFound.
func (t *Tx) GetTopic(ctx context.Context, id string) (Topic, error) {
	return scanTopic(t.queryRow(ctx, topicSelect+` WHERE topic_id=?`, id))
}

// GetTopic returns one topic or ErrNotFound.
func (l *Ledger) GetTopic(ctx context.Context, id string) (Topic, error) {
	return scanTopic(l.db.QueryRowContext(ctx, topicSelect+` WHERE topic_id=?`, id))
}

const topicSelect = `SELECT topic_id,defined_in,subject_key,kind,lens,state,revision,version_sensitive,seq,updated_at FROM topic`

func scanTopic(r *sql.Row) (Topic, error) {
	var tp Topic
	var vs int
	var upd string
	err := r.Scan(&tp.ID, &tp.DefinedIn, &tp.SubjectKey, &tp.Kind, &tp.Lens, &tp.State, &tp.Revision, &vs, &tp.Seq, &upd)
	if errors.Is(err, sql.ErrNoRows) {
		return Topic{}, ErrNotFound
	}
	tp.UpdatedAt = parseTime(upd)
	tp.VersionSensitive = vs != 0
	return tp, err
}

// bumpRevision is the single place a topic's revision changes. Every
// state-changing function calls it, so a stored view goes stale on any change
// of the write-up or of the ruling state.
func (t *Tx) bumpRevision(ctx context.Context, topicID string) (int, error) {
	res, err := t.exec(ctx, `UPDATE topic SET revision=revision+1, updated_at=? WHERE topic_id=?`,
		fmtTime(t.now()), topicID)
	if err != nil {
		return 0, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return 0, ErrNotFound
	}
	var rev int
	err = t.queryRow(ctx, `SELECT revision FROM topic WHERE topic_id=?`, topicID).Scan(&rev)
	return rev, err
}

// AddRevision stores a write-up for a topic and bumps the topic revision. The
// first write-up of a new topic is stored at revision 1 without a bump.
func (t *Tx) AddRevision(ctx context.Context, topicID string, runID int64, writeupJSON, rendered string) (int, error) {
	var rev int
	var n int
	if err := t.queryRow(ctx, `SELECT COUNT(*) FROM revision WHERE topic_id=?`, topicID).Scan(&n); err != nil {
		return 0, err
	}
	if n == 0 {
		if err := t.queryRow(ctx, `SELECT revision FROM topic WHERE topic_id=?`, topicID).Scan(&rev); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return 0, ErrNotFound
			}
			return 0, err
		}
	} else {
		var err error
		if rev, err = t.bumpRevision(ctx, topicID); err != nil {
			return 0, err
		}
	}
	_, err := t.exec(ctx, `INSERT INTO revision(topic_id,run_id,topic_revision,writeup_json,writeup_rendered,created_at)
		VALUES(?,?,?,?,?,?)`, topicID, runID, rev, writeupJSON, rendered, fmtTime(t.now()))
	return rev, err
}

// LatestWriteup returns the newest stored write-up of a topic.
func (l *Ledger) LatestWriteup(ctx context.Context, topicID string) (writeupJSON, rendered string, err error) {
	err = l.db.QueryRowContext(ctx, `SELECT writeup_json,writeup_rendered FROM revision
		WHERE topic_id=? ORDER BY id DESC LIMIT 1`, topicID).Scan(&writeupJSON, &rendered)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	return
}

// AddOccurrence records that a run saw a topic. One occurrence per (topic, run).
func (t *Tx) AddOccurrence(ctx context.Context, topicID string, runID int64, scope, lens, evidenceRefsJSON, metricsJSON string) error {
	if evidenceRefsJSON == "" {
		evidenceRefsJSON = "[]"
	}
	if metricsJSON == "" {
		metricsJSON = "{}"
	}
	_, err := t.exec(ctx, `INSERT INTO occurrence(topic_id,run_id,scope,lens,evidence_refs,metrics,created_at)
		VALUES(?,?,?,?,?,?,?)`, topicID, runID, scope, lens, evidenceRefsJSON, metricsJSON, fmtTime(t.now()))
	return err
}

// Transition moves a topic to a new state without a ruling (auto-defer,
// reconsideration, Waiting promotion) and bumps its revision.
func (t *Tx) Transition(ctx context.Context, topicID, to string) error {
	switch to {
	case StatePending, StateAdopted, StateDeferred, StateRejected, StateWaiting:
	default:
		return fmt.Errorf("%w: state %q", ErrInvalid, to)
	}
	res, err := t.exec(ctx, `UPDATE topic SET state=? WHERE topic_id=?`, to, topicID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	_, err = t.bumpRevision(ctx, topicID)
	return err
}

// Ruling is the input of RecordRuling.
type Ruling struct {
	TopicID            string
	Ruling             string
	Rationale          string
	ReconsiderWhen     string
	RateAtDecision     string
	Useful             string
	TargetTracker      string
	RepoLabel          string
	BeadIdempotencyKey string
}

// Decision is a stored ruling row.
type Decision struct {
	ID                 int64
	Seq                int64
	TopicID            string
	Ruling             string
	Decided            string
	Rationale          string
	ReconsiderWhen     string
	RateAtDecision     string
	Useful             string
	TargetTracker      string
	RepoLabel          string
	BeadState          string
	BeadIdempotencyKey string
	BeadAttempts       int
	BeadLeaseExpiresAt string
	WorkBead           string
}

// RecordRuling stores a ruling, moves the topic to the matching state and
// bumps its revision. An Adopted ruling is stored with bead_state pending;
// every other ruling with none. A Reopen returns the topic to Pending.
func (t *Tx) RecordRuling(ctx context.Context, r Ruling) (Decision, error) {
	var state string
	bead := BeadNone
	switch r.Ruling {
	case RulingAdopted:
		state, bead = StateAdopted, BeadPending
	case RulingDeferred:
		state = StateDeferred
	case RulingRejected:
		state = StateRejected
	case RulingReopen:
		state = StatePending
	default:
		return Decision{}, fmt.Errorf("%w: ruling %q", ErrInvalid, r.Ruling)
	}
	if _, err := t.GetTopic(ctx, r.TopicID); err != nil {
		return Decision{}, err
	}
	seq, err := t.nextSeq(ctx)
	if err != nil {
		return Decision{}, err
	}
	var idem any
	if r.BeadIdempotencyKey != "" {
		idem = r.BeadIdempotencyKey
	}
	res, err := t.exec(ctx, `INSERT INTO decision(seq,topic_id,ruling,decided,rationale,reconsider_when,rate_at_decision,useful,target_tracker,repo_label,bead_state,bead_idempotency_key)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, seq, r.TopicID, r.Ruling, fmtTime(t.now()), r.Rationale, r.ReconsiderWhen,
		r.RateAtDecision, r.Useful, r.TargetTracker, r.RepoLabel, bead, idem)
	if err != nil {
		return Decision{}, err
	}
	id, _ := res.LastInsertId()
	if err := t.Transition(ctx, r.TopicID, state); err != nil {
		return Decision{}, err
	}
	return t.getDecision(ctx, id)
}

func (t *Tx) getDecision(ctx context.Context, id int64) (Decision, error) {
	return scanDecision(t.queryRow(ctx, decisionSelect+` WHERE id=?`, id))
}

const decisionSelect = `SELECT id,seq,topic_id,ruling,decided,rationale,reconsider_when,rate_at_decision,useful,target_tracker,repo_label,bead_state,COALESCE(bead_idempotency_key,''),bead_attempts,bead_lease_expires_at,work_bead FROM decision`

func scanDecision(r *sql.Row) (Decision, error) {
	var d Decision
	err := r.Scan(&d.ID, &d.Seq, &d.TopicID, &d.Ruling, &d.Decided, &d.Rationale, &d.ReconsiderWhen, &d.RateAtDecision,
		&d.Useful, &d.TargetTracker, &d.RepoLabel, &d.BeadState, &d.BeadIdempotencyKey, &d.BeadAttempts, &d.BeadLeaseExpiresAt, &d.WorkBead)
	if errors.Is(err, sql.ErrNoRows) {
		return Decision{}, ErrNotFound
	}
	return d, err
}

// LatestDecision returns the newest ruling of a topic.
func (l *Ledger) LatestDecision(ctx context.Context, topicID string) (Decision, error) {
	return scanDecision(l.db.QueryRowContext(ctx, decisionSelect+` WHERE topic_id=? ORDER BY id DESC LIMIT 1`, topicID))
}

// RecordView stores what the operator was shown: the topic revision seen.
func (t *Tx) RecordView(ctx context.Context, topicID string) (int, error) {
	tp, err := t.GetTopic(ctx, topicID)
	if err != nil {
		return 0, err
	}
	_, err = t.exec(ctx, `INSERT INTO view(topic_id,revision_seen,shown_at) VALUES(?,?,?)
		ON CONFLICT(topic_id) DO UPDATE SET revision_seen=excluded.revision_seen, shown_at=excluded.shown_at`,
		topicID, tp.Revision, fmtTime(t.now()))
	return tp.Revision, err
}

// ViewRevision returns the revision the operator last saw, or ErrNotFound when
// the topic was never shown.
func (l *Ledger) ViewRevision(ctx context.Context, topicID string) (int, error) {
	var rev int
	err := l.db.QueryRowContext(ctx, `SELECT revision_seen FROM view WHERE topic_id=?`, topicID).Scan(&rev)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	return rev, err
}

// Counts are the item counts the run lifecycle and the human CLI use.
type Counts struct {
	Pending int
	Waiting int
	Filing  int
}

// Counts returns the number of Pending and Waiting topics and of decisions
// whose bead_state is filing.
func (l *Ledger) Counts(ctx context.Context) (Counts, error) {
	var c Counts
	err := l.db.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM topic WHERE state='Pending'),
		(SELECT COUNT(*) FROM topic WHERE state='Waiting'),
		(SELECT COUNT(*) FROM decision WHERE bead_state='filing')`).Scan(&c.Pending, &c.Waiting, &c.Filing)
	return c, err
}

// PromoteWaiting moves Waiting topics to Pending, oldest first, until the
// pending count reaches cap. It returns the promoted topic ids.
func (t *Tx) PromoteWaiting(ctx context.Context, cap int) ([]string, error) {
	var pending int
	if err := t.queryRow(ctx, `SELECT COUNT(*) FROM topic WHERE state='Pending'`).Scan(&pending); err != nil {
		return nil, err
	}
	free := cap - pending
	if free <= 0 {
		return nil, nil
	}
	rows, err := t.query(ctx, `SELECT topic_id FROM topic WHERE state='Waiting' ORDER BY seq LIMIT ?`, free)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for _, id := range ids {
		if err := t.Transition(ctx, id, StatePending); err != nil {
			return nil, err
		}
	}
	return ids, nil
}

// Rule records a ruling in its own transaction and then appends it to the
// append-only rulings log. The ruling stands even if the log append fails (the
// error is returned alongside the decision).
func (l *Ledger) Rule(ctx context.Context, r Ruling) (Decision, error) {
	var d Decision
	var lens string
	err := l.WithTx(ctx, func(t *Tx) error {
		var err error
		if d, err = t.RecordRuling(ctx, r); err != nil {
			return err
		}
		tp, err := t.GetTopic(ctx, r.TopicID)
		lens = tp.Lens
		return err
	})
	if err != nil {
		return Decision{}, err
	}
	return d, l.AppendRulingLog(RulingLogEntry{
		TopicID: r.TopicID, Lens: lens, Ruling: r.Ruling,
		Rationale: r.Rationale, ReconsiderWhen: r.ReconsiderWhen,
	})
}

// TopicsForLens lists the topics of a lens in creation order (the known
// topics `agent context` returns to the analyst).
func (l *Ledger) TopicsForLens(ctx context.Context, lens string) ([]Topic, error) {
	rows, err := l.db.QueryContext(ctx, topicSelect+` WHERE lens=? ORDER BY seq`, lens)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Topic
	for rows.Next() {
		var tp Topic
		var vs int
		var upd string
		if err := rows.Scan(&tp.ID, &tp.DefinedIn, &tp.SubjectKey, &tp.Kind, &tp.Lens, &tp.State, &tp.Revision, &vs, &tp.Seq, &upd); err != nil {
			return nil, err
		}
		tp.VersionSensitive = vs != 0
		tp.UpdatedAt = parseTime(upd)
		out = append(out, tp)
	}
	return out, rows.Err()
}
