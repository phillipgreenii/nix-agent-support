package query

import (
	"database/sql"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
)

// recordingQuerier remembers each statement and its arguments.
type recordingQuerier struct {
	store.Querier
	sql  []string
	args [][]any
}

func (r *recordingQuerier) Query(q string, args ...any) (*sql.Rows, error) {
	r.sql, r.args = append(r.sql, q), append(r.args, args)
	return r.Querier.Query(q, args...)
}
