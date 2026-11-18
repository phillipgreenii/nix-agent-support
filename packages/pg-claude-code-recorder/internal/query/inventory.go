package query

import (
	"context"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/wire"
)

// InventorySQL builds the statement and its arguments. It names only file_facts
// and files, so the planner has nothing in records to scan.
func InventorySQL(req InventoryRequest, since int64, limit int) (string, []any) {
	var where []string
	var args []any
	if len(req.Sources) > 0 {
		where = append(where, "f.source IN ("+placeholders(len(req.Sources))+")")
		for _, s := range req.Sources {
			args = append(args, s)
		}
	}
	if req.After > 0 {
		where = append(where, "ff.file_id>?")
		args = append(args, req.After)
	}
	var narrow []string
	if req.Since != "" {
		narrow = append(narrow, "ff.last_time_ns>=?")
		args = append(args, since)
	}
	if len(req.Sessions) > 0 {
		narrow = append(narrow, "ff.file_session IN ("+placeholders(len(req.Sessions))+")")
		for _, s := range req.Sessions {
			args = append(args, s)
		}
	}
	if len(narrow) > 0 {
		where = append(where, "("+strings.Join(narrow, " OR ")+")")
	}
	q := `SELECT ff.file_id, f.source, COALESCE(ff.project,''), COALESCE(ff.file_session,''), COALESCE(ff.agent_id,''), ff.last_time_ns
FROM file_facts ff JOIN files f ON f.id=ff.file_id`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY ff.file_id LIMIT ?"
	args = append(args, limit+1)
	return q, args
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// Inventory answers files.inventory.
func (e *Engine) Inventory(ctx context.Context, req InventoryRequest) (*InventoryAnswer, *wire.Error) {
	var since int64
	if req.Since != "" {
		t, err := time.Parse(time.RFC3339Nano, req.Since)
		if err != nil {
			return nil, badf("since %q: %v", req.Since, err)
		}
		since = t.UnixNano()
	}
	limit := limitOf(req.Limit)
	dbtx, err := e.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, wire.FromErr(err)
	}
	defer func() { _ = dbtx.Rollback() }()
	tx := store.BindCtx(ctx, dbtx)
	ans := &InventoryAnswer{AsOf: e.asOf(tx), Files: []InventoryFile{}}
	if e.Behind != nil && e.Behind() {
		ans.Degraded = true
	}
	var complete string
	if tx.QueryRow(`SELECT value FROM meta WHERE key=?`, store.MetaFileFactsComplete).Scan(&complete) != nil || complete != "1" {
		ans.Degraded = true
	}
	q, args := InventorySQL(req, since, limit)
	rows, err := tx.Query(q, args...)
	if err != nil {
		return nil, wire.FromErr(err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var f InventoryFile
		var ns int64
		if err := rows.Scan(&f.File, &f.Source, &f.Project, &f.FileSession, &f.AgentID, &ns); err != nil {
			return nil, wire.FromErr(err)
		}
		if len(ans.Files) == limit {
			ans.More = true
			break
		}
		if ns > 0 {
			f.Last = time.Unix(0, ns).UTC().Format(time.RFC3339Nano)
		}
		ans.Files = append(ans.Files, f)
	}
	if err := rows.Err(); err != nil {
		return nil, wire.FromErr(err)
	}
	return ans, nil
}
