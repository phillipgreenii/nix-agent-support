package store

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
)

// The ask tables answer "is this session waiting on an AskUserQuestion" without
// reading the session's records. A session can hold millions of records of which
// a handful are questions, and the question's tool name and id are fields rows
// no index serves within a session, so the generic query walked the whole session
// (27 s on a 9.95M-record one).
//
//   - ask_questions holds one row per AskUserQuestion tool_use of a session's own
//     transcript (not a subagent's), keyed by the record id, with the record's
//     time and the tool_use_id;
//   - ask_answers holds one row per tool_result that answers such a question,
//     keyed by the result record's id.
//
// The parser writes both in the transaction that writes the record and deletes
// them with the record's generation (clearGen), so a rebuild writes them again by
// parsing. A database that existed before the tables is backfilled in the
// background (parser.BackfillAskFacts) and marked with MetaAskFactsComplete; until
// then AskFacts reports the answer unavailable and the caller falls back to a
// query.
const (
	// MetaAskFactsComplete marks an index.db whose ask tables cover every record.
	// A database created empty by this code carries it from the start.
	MetaAskFactsComplete = "ask_facts_complete"
	metaAskAsksCursor    = "ask_facts_asks_cursor"
	metaAskAsksEnd       = "ask_facts_asks_end"
	metaAskAnswersCursor = "ask_facts_answers_cursor"
)

// AskCandidates bounds how many of the newest questions of a session AskFacts
// examines (the same bound ccpool's query form used).
const AskCandidates = 50

const askTablesSQL = `
CREATE TABLE IF NOT EXISTS ask_questions(
  rec_id INTEGER PRIMARY KEY,
  session_ref INTEGER NOT NULL,
  time_ns INTEGER NOT NULL,
  tool_use_id TEXT NOT NULL
) STRICT;
CREATE TABLE IF NOT EXISTS ask_answers(
  rec_id INTEGER PRIMARY KEY,
  tool_use_id TEXT NOT NULL
) STRICT;
`

// AskFacts is the awaiting-input fact of one session.
type AskFacts struct {
	// Known is false when the session has never been seen.
	Known bool
	// Open reports a question among the newest AskCandidates that no tool_result
	// answers; ToolUseID and AskedNS are the newest such question's.
	Open      bool
	ToolUseID string
	AskedNS   int64
}

// ErrAskFactsUnavailable is returned by AskFacts while the ask tables do not yet
// cover the whole database (a backfill is running) or the store does not carry
// session_ref on every row.
var ErrAskFactsUnavailable = errors.New("ask facts are not available yet")

func metaIs(q Querier, key string) bool {
	ok, _ := metaIsErr(q, key)
	return ok
}

// metaIsErr is metaIs that reports a read error (a cancelled context) instead of
// answering false.
func metaIsErr(q Querier, key string) (bool, error) {
	var v string
	if err := q.QueryRow(`SELECT value FROM meta WHERE key=?`, key).Scan(&v); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return v == "1", nil
}

// AskFactsComplete reports whether the ask tables cover every record.
func (s *Store) AskFactsComplete() bool { return metaIs(s.rdb, MetaAskFactsComplete) }

// metaInt reads an integer meta value (0 when absent).
func metaInt(q Querier, key string) int64 {
	var v string
	if err := q.QueryRow(`SELECT value FROM meta WHERE key=?`, key).Scan(&v); err != nil {
		return 0
	}
	n, _ := strconv.ParseInt(v, 10, 64)
	return n
}

func setMeta(ctx context.Context, tx *sql.Tx, key, value string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

// AskFacts reads the awaiting-input fact of one session from the ask tables: the
// newest AskCandidates questions, each checked against ask_answers by its id.
// Both reads are index seeks whose cost does not depend on the session's size.
func (s *Reader) AskFacts(ctx context.Context, sessionID string, ready bool) (AskFacts, error) {
	q := BindCtx(ctx, s.q)
	if !ready {
		return AskFacts{}, ErrAskFactsUnavailable
	}
	complete, err := metaIsErr(q, MetaAskFactsComplete)
	if err != nil {
		return AskFacts{}, err
	}
	if !complete {
		return AskFacts{}, ErrAskFactsUnavailable
	}
	var ref int64
	if err := q.QueryRow(`SELECT session_ref FROM sessions WHERE session_id=?`, sessionID).Scan(&ref); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return AskFacts{}, nil
		}
		return AskFacts{}, err
	}
	out := AskFacts{Known: true}
	rows, err := q.Query(AskFactsSQL, ref, AskCandidates)
	if err != nil {
		return AskFacts{}, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id string
		var at int64
		var answered bool
		if err := rows.Scan(&id, &at, &answered); err != nil {
			return AskFacts{}, err
		}
		if !answered && !out.Open {
			out.Open, out.ToolUseID, out.AskedNS = true, id, at
		}
	}
	return out, rows.Err()
}

// AskFactsSQL is the candidate read: the newest questions of a session with
// whether each is answered. It names only the small ask tables.
const AskFactsSQL = `SELECT q.tool_use_id, q.time_ns,
  EXISTS(SELECT 1 FROM ask_answers a WHERE a.tool_use_id=q.tool_use_id)
FROM ask_questions q WHERE q.session_ref=? ORDER BY q.time_ns DESC, q.rec_id DESC LIMIT ?`

// AskBackfillState is the backfill's saved position.
type AskBackfillState struct {
	AsksCursor, AsksEnd, AnswersCursor int64
}

// AskBackfill reads the backfill's saved position.
func (s *Store) AskBackfill() AskBackfillState {
	return AskBackfillState{
		AsksCursor:    metaInt(s.rdb, metaAskAsksCursor),
		AsksEnd:       metaInt(s.rdb, metaAskAsksEnd),
		AnswersCursor: metaInt(s.rdb, metaAskAnswersCursor),
	}
}

// SetAskAsksEnd freezes the record id the question pass runs up to.
func (s *Store) SetAskAsksEnd(tx *sql.Tx, end int64) error {
	return setMeta(context.Background(), tx, metaAskAsksEnd, strconv.FormatInt(end, 10))
}

// SetAskAsksCursor saves the question pass position inside tx.
func (s *Store) SetAskAsksCursor(tx *sql.Tx, id int64) error {
	return setMeta(context.Background(), tx, metaAskAsksCursor, strconv.FormatInt(id, 10))
}

// SetAskAnswersCursor saves the answer pass position inside tx.
func (s *Store) SetAskAnswersCursor(tx *sql.Tx, id int64) error {
	return setMeta(context.Background(), tx, metaAskAnswersCursor, strconv.FormatInt(id, 10))
}

// MarkAskFactsComplete records that the backfill is done, inside tx.
func (s *Store) MarkAskFactsComplete(tx *sql.Tx) error {
	return setMeta(context.Background(), tx, MetaAskFactsComplete, "1")
}
