package parser

import (
	"context"
	"database/sql"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/source"
)

const (
	askSource    = "claude-transcript"
	askToolName  = "AskUserQuestion"
	askBackSpan  = 100000 // record ids per backfill transaction
	askNameField = "name"
	askIDField   = "tool_use_id"
)

func fieldS(r source.Record, name string) string {
	for _, f := range r.Fields {
		if f.Name == name {
			return f.S
		}
	}
	return ""
}

// noteAsk keeps ask_questions and ask_answers (see store/askfacts.go) in step
// with the record just inserted. Only the session's own transcript counts: a
// subagent's file carries the parent's session id but has an agent capture.
func (p *Parser) noteAsk(tx *pstx, src string, caps source.Captures, r source.Record, ref, tns, id int64) error {
	if src != askSource || caps["agent"] != "" {
		return nil
	}
	switch r.Type {
	case "tool_use":
		if fieldS(r, askNameField) != askToolName {
			return nil
		}
		tu := fieldS(r, askIDField)
		if tu == "" {
			return nil
		}
		_, err := tx.Exec(`INSERT OR REPLACE INTO ask_questions(rec_id,session_ref,time_ns,tool_use_id) VALUES(?,?,?,?)`, id, ref, tns, tu)
		return err
	case "tool_result":
		tu := fieldS(r, askIDField)
		if tu == "" {
			return nil
		}
		_, err := tx.Exec(`INSERT OR REPLACE INTO ask_answers(rec_id,tool_use_id) SELECT ?1,?2 WHERE EXISTS(SELECT 1 FROM ask_questions WHERE tool_use_id=?2)`, id, tu)
		return err
	}
	return nil
}

// clearAsk deletes the ask rows of a file generation's records; it runs before
// the records are deleted.
func (p *Parser) clearAsk(tx *pstx, file int64, gen int) error {
	for _, t := range []string{"ask_questions", "ask_answers"} {
		if _, err := tx.Exec(`DELETE FROM `+t+` WHERE rec_id IN (SELECT id FROM records WHERE file_id=?1 AND gen=?2)`, file, gen); err != nil {
			return err
		}
	}
	return nil
}

// The backfill statements. The question pass reads the type index of records
// and probes fields only for tool_use rows; the answer pass likewise for
// tool_result rows, keeping only those that answer a stored question.
const (
	askBackfillQuestions = `INSERT OR IGNORE INTO ask_questions(rec_id,session_ref,time_ns,tool_use_id)
SELECT r.id, r.session_ref, r.time_ns, fu.s
FROM records r
JOIN fields fn ON fn.rec_id=r.id AND fn.name_id=(SELECT id FROM field_names WHERE name='name') AND fn.s='AskUserQuestion'
JOIN fields fu ON fu.rec_id=r.id AND fu.name_id=(SELECT id FROM field_names WHERE name='tool_use_id')
LEFT JOIN file_facts ff ON ff.file_id=r.file_id
WHERE r.type='tool_use' AND r.id>?1 AND r.id<=?2 AND r.source='claude-transcript' AND ff.agent_id IS NULL AND fu.s<>''`
	askBackfillAnswers = `INSERT OR IGNORE INTO ask_answers(rec_id,tool_use_id)
SELECT r.id, fu.s
FROM records r
JOIN fields fu ON fu.rec_id=r.id AND fu.name_id=(SELECT id FROM field_names WHERE name='tool_use_id')
LEFT JOIN file_facts ff ON ff.file_id=r.file_id
WHERE r.type='tool_result' AND r.id>?1 AND r.id<=?2 AND r.source='claude-transcript' AND ff.agent_id IS NULL
  AND fu.s IN (SELECT tool_use_id FROM ask_questions)`
)

// BackfillAskFacts fills the ask tables for a database that existed before
// them, one bounded write transaction (askBackSpan record ids) per call. Pass one
// stores the questions up to the newest record id seen at its start; pass two
// stores the answers, and its last transaction runs to the then-newest id and
// marks the tables complete under the write lock, so a record parsed while the
// backfill ran is covered either by the live path or by that last range. It waits
// until file_facts is complete (it needs the agent capture).
func (p *Parser) BackfillAskFacts(ctx context.Context) (done bool, err error) {
	st := p.cfg.Store
	if st.AskFactsComplete() {
		return true, nil
	}
	if !st.FileFactsComplete() {
		return false, nil
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	state := st.AskBackfill()
	err = st.WithWrite("", func(tx *sql.Tx) error {
		if state.AsksEnd == 0 {
			var m sql.NullInt64
			if err := tx.QueryRowContext(context.Background(), `SELECT MAX(id) FROM records`).Scan(&m); err != nil {
				return err
			}
			if !m.Valid {
				return st.MarkAskFactsComplete(tx)
			}
			state.AsksEnd = m.Int64
			if err := st.SetAskAsksEnd(tx, state.AsksEnd); err != nil {
				return err
			}
		}
		if state.AsksCursor < state.AsksEnd {
			to := min(state.AsksCursor+askBackSpan, state.AsksEnd)
			if _, err := tx.ExecContext(context.Background(), askBackfillQuestions, state.AsksCursor, to); err != nil {
				return err
			}
			return st.SetAskAsksCursor(tx, to)
		}
		var maxID int64
		if err := tx.QueryRowContext(context.Background(), `SELECT COALESCE(MAX(id),0) FROM records`).Scan(&maxID); err != nil {
			return err
		}
		to := min(state.AnswersCursor+askBackSpan, maxID)
		if _, err := tx.ExecContext(context.Background(), askBackfillAnswers, state.AnswersCursor, to); err != nil {
			return err
		}
		if to >= maxID {
			return st.MarkAskFactsComplete(tx)
		}
		return st.SetAskAnswersCursor(tx, to)
	})
	if err != nil {
		return false, err
	}
	return st.AskFactsComplete(), nil
}
