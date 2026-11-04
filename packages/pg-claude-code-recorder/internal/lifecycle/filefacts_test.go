package lifecycle

import (
	"database/sql"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// oldFileFacts is what transcriptFacts read before the grouped statement: a MAX
// query and a DISTINCT file_id query, kept as the equivalence oracle.
func oldFileFacts(t *testing.T, db *sql.DB, session string) (int64, []int64) {
	t.Helper()
	var last sql.NullInt64
	if err := db.QueryRow(`SELECT MAX(time_ns) FROM records WHERE session=? AND source='claude-transcript'`, session).Scan(&last); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query(`SELECT DISTINCT file_id FROM records WHERE session=? AND source='claude-transcript' LIMIT 50`, session)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return last.Int64, ids
}

// recFile inserts a record of the given file.
func (f *cwdFixture) recFile(session, source string, fileID, timeNS int64) {
	f.t.Helper()
	f.id++
	if _, err := f.db.Exec(`INSERT INTO records(id,file_id,gen,off,item,len,source,type,parser_version,session,time_ns)
VALUES(?,?,1,?,0,1,?,'x',1,?,?)`, f.id, fileID, f.id, source, session, timeNS); err != nil {
		f.t.Fatal(err)
	}
}

func TestSessionFileFactsEqualTheOldPerQueryResult(t *testing.T) {
	f := newCwdFixture(t)
	// s: three files, out of time order; other sessions and non-transcript
	// records must not leak in.
	f.recFile("s", transcript, 1, 10)
	f.recFile("s", transcript, 2, 50)
	f.recFile("s", transcript, 1, 30)
	f.recFile("s", transcript, 3, 20)
	f.recFile("s", "session-registry", 9, 999)
	f.recFile("other", transcript, 7, 888)
	f.recFile("registry-only", "session-registry", 4, 5)
	// many: more files than the cap.
	for i := int64(1); i <= 60; i++ {
		f.recFile("many", transcript, 100+i, i)
	}

	for _, session := range []string{"s", "other", "registry-only", "absent", "many"} {
		oldLast, oldIDs := oldFileFacts(t, f.db, session)
		last, ids, err := sessionFileFacts(f.db, session)
		if err != nil {
			t.Fatal(err)
		}
		if last != oldLast {
			t.Errorf("%s: last write %d, old %d", session, last, oldLast)
		}
		if session == "many" {
			// Both are capped at 50 of 60 files; which 50 is unspecified for the old query.
			if len(ids) != 50 || len(oldIDs) != 50 {
				t.Errorf("many: %d and %d ids, want 50", len(ids), len(oldIDs))
			}
			continue
		}
		if !reflect.DeepEqual(ids, oldIDs) && (len(ids) != 0 || len(oldIDs) != 0) {
			t.Errorf("%s: files %v, old %v", session, ids, oldIDs)
		}
	}
}

func TestFileFactsQueryUsesTheSessionSourceTimeIndex(t *testing.T) {
	f := newCwdFixture(t)
	f.recFile("s", transcript, 1, 10)
	rows, err := f.db.Query(`EXPLAIN QUERY PLAN `+fileFactsQuery, "s")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var plan []string
	for rows.Next() {
		var id, parent, unused int
		var d string
		if err := rows.Scan(&id, &parent, &unused, &d); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, d)
	}
	if p := strings.Join(plan, "\n"); !strings.Contains(p, "records_session_source_time") {
		t.Errorf("plan does not use records_session_source_time:\n%s", p)
	}
}
