package store

import (
	"context"
	"strings"
	"testing"
)

// SessionsOfFile reads the records of one file: both forms must search records
// by file_id (the primary key prefix), never scan the table.
func TestSessionsOfFilePlansSearchRecordsByFile(t *testing.T) {
	s := open(t, t.TempDir())
	defer func() { _ = s.Close() }()
	for name, q := range map[string]string{"text": sessionsOfFileQuery, "session_ref": sessionsOfFileRefQuery} {
		rows, err := s.Read().QueryContext(context.Background(), "EXPLAIN QUERY PLAN "+q, 1)
		if err != nil {
			t.Fatal(err)
		}
		var plan []string
		for rows.Next() {
			var id, parent, unused int
			var d string
			if err := rows.Scan(&id, &parent, &unused, &d); err != nil {
				t.Fatal(err)
			}
			plan = append(plan, d)
		}
		_ = rows.Close()
		p := strings.Join(plan, "\n")
		if !strings.Contains(p, "SEARCH r USING") && !strings.Contains(p, "SEARCH records USING") {
			t.Errorf("%s: records is not searched by file:\n%s", name, p)
		}
		if strings.Contains(p, "SCAN r") || strings.Contains(p, "SCAN records") {
			t.Errorf("%s: records is scanned:\n%s", name, p)
		}
	}
}
