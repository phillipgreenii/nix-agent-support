package query

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/pg-ccaudit/internal/store"
)

// bigFixture describes a synthetic corpus shaped like the real one: many
// sessions spread over thirty days, a handful of tool calls per hundred events,
// roughly one result in ten an error. Every value is invented.
type bigFixture struct {
	sessions int
	perSess  int // events per session (one file per session)
}

// windowDay is the one-day window used against the fixture: it cuts through the
// sessions that straddle its start and end.
const (
	fixtureEpoch = "2026-10-01"
	windowSince  = "2026-10-15T00:00:00.000Z"
	windowUntil  = "2026-10-16T00:00:00.000Z"
)

// buildBigFixture writes the fixture with the production schema and indexes (no
// ANALYZE, like the live database) and returns a read-only handle.
//
// Session s starts s*30/sessions days after the epoch and spans six hours.
// Event seq 1 has a NULL cwd (the bookkeeping line); later events carry the
// session's cwd: /w/a/..., /w/b/... or /elsewhere by s mod 3, except that
// every seventh session leaves its first cwd later (so first cwd and later cwd
// differ). Line seq%16 == 1 is an assistant line with one tool call, seq%16 == 2
// its result (an error when the call's ordinal is divisible by 10), seq%16 == 5
// a human line whose text is an interruption sentinel when the ordinal is
// divisible by 20.
func buildBigFixture(t testing.TB, f bigFixture) *sql.DB {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "big.db")
	w, err := store.Open(dbPath, false)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	ctx := context.Background()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := w.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("exec: %v\n%s", err, q)
		}
	}
	n := f.sessions * f.perSess
	exec(fmt.Sprintf(`WITH RECURSIVE n(i) AS (SELECT 0 UNION ALL SELECT i+1 FROM n WHERE i < %d)
INSERT INTO files (path, project_dir, size, mtime, lines_ok, lines_bad, ingested_at)
SELECT printf('/t/s%%05d.jsonl', i), '', 0, 0, 1, 0, 0 FROM n`, f.sessions-1))
	exec(fmt.Sprintf(`WITH RECURSIVE n(i) AS (SELECT 0 UNION ALL SELECT i+1 FROM n WHERE i < %[1]d)
INSERT INTO events (path, seq, session_id, ts, type, is_sidechain, cwd)
SELECT printf('/t/s%%05d.jsonl', i / %[2]d),
       i %% %[2]d + 1,
       printf('sess%%05d', i / %[2]d),
       strftime('%%Y-%%m-%%dT%%H:%%M:%%f', julianday('%[4]s') + (i / %[2]d) * (30.0 / %[3]d) + (i %% %[2]d) * (0.25 / %[2]d)) || 'Z',
       CASE (i %% %[2]d + 1) %% 16 WHEN 1 THEN 'assistant' WHEN 2 THEN 'user' ELSE 'system' END,
       i %% 5 = 0,
       CASE WHEN i %% %[2]d = 0 THEN NULL
            WHEN (i / %[2]d) %% 7 = 3 AND i %% %[2]d < 3 THEN '/elsewhere'
            WHEN (i / %[2]d) %% 3 = 0 THEN printf('/w/a/p%%d', (i / %[2]d) %% 11)
            WHEN (i / %[2]d) %% 3 = 1 THEN printf('/w/b/p%%d', (i / %[2]d) %% 11)
            ELSE '/elsewhere' END
FROM n`, n-1, f.perSess, f.sessions, fixtureEpoch))
	// Tool calls and results ride on the lines chosen above.
	exec(`INSERT INTO tool_calls (tool_use_id, path, seq, tool_name, input_json, lead_cmd)
SELECT 'c' || path || seq, path, seq,
       CASE (seq / 16) % 4 WHEN 0 THEN 'Bash' WHEN 1 THEN 'Read' WHEN 2 THEN 'Edit' ELSE 'Skill' END,
       '{"command":"x","skill":"s' || (seq % 5) || '"}', 'x'
FROM events WHERE seq % 16 = 1`)
	exec(`INSERT INTO tool_results (tool_use_id, path, seq, is_error, content_len, content, signature)
SELECT 'c' || path || (seq - 1), path, seq,
       ((seq / 16) % 10 = 0),
       10,
       CASE WHEN (seq / 16) % 10 <> 0 THEN NULL
            WHEN (seq / 16 + length(path)) % 3 = 0 THEN 'Permission for this tool use was denied'
            WHEN (seq / 16 + length(path)) % 3 = 1 THEN 'Hook: refusing this command'
            ELSE 'boom' END,
       CASE WHEN (seq / 16) % 10 = 0 THEN 'sig' || (seq % 7) END
FROM events WHERE seq % 16 = 2`)
	exec(`INSERT INTO user_text (path, seq, text_len, text, interrupted)
SELECT path, seq, 30, '[Request interrupted by user]', ((seq / 16) % 2 = 0)
FROM events WHERE seq % 16 = 5`)
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	db, err := store.OpenReadOnly(dbPath)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
