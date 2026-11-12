package lifecycle

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
)

// The queries over session_ref must not leave a text session read behind.
func TestSessionRefQueriesReadNoTextSession(t *testing.T) {
	for _, q := range []string{registryQuery, slashCommandQuery, endReasonQuery} {
		if v := viaSessionRef(q); strings.Contains(v, "r.session,") || !strings.Contains(v, sessionRefExpr) {
			t.Errorf("viaSessionRef left the text column in:\n%s", v)
		}
	}
}

// loadFixture fills a real store with the facts the lifecycle engine reads:
// two sessions with a registry entry each, a transcript across two files with
// its cwd, a slash command and an end reason.
func (f *cwdFixture) loadFixture() {
	f.t.Helper()
	for i, s := range []string{"s1", "s2"} {
		pid := fmt.Sprint(100 + i)
		f.rec(s, "session-registry", int64(10+i), [2]any{"pid", nil}, [2]any{"proc_start", "ps" + pid}, [2]any{"cwd", "/example/reg/" + s}, [2]any{"name", "n" + s}, [2]any{"status", "idle"})
		f.rec(s, transcript, int64(20+i), cwd("/example/old/"+s))
		f.recFile(s, transcript, int64(7+i), int64(30+i))
		f.rec(s, transcript, int64(40+i), cwd("/example/new/"+s))
		f.rec(s, "claude-history", int64(25+i), [2]any{"slash_command", "/clear"})
		f.rec(s, "claude-control-end", int64(50+i), [2]any{"reason", "logout"})
	}
	if _, err := f.db.ExecContext(context.Background(), `UPDATE records SET type='registry' WHERE source='session-registry';
UPDATE fields SET n=100+rec_id, s=NULL WHERE name_id=(SELECT id FROM field_names WHERE name='pid')`); err != nil {
		f.t.Fatal(err)
	}
}

// On a real store the lifecycle engine answers the same from session_ref as from
// the text session column, which is what a store without the mark still reads.
func TestLifecycleAnswersThroughSessionRefEqualTheTextSessionPath(t *testing.T) {
	f := newCwdFixture(t)
	f.loadFixture()
	if !f.st.SessionRefReady() {
		t.Fatal("the fixture store is not marked ready")
	}
	read := func(st *store.Store) (list []Conversation, shows []Conversation) {
		e := &Engine{St: st, Exists: func(string) bool { return true }}
		list, err := e.List(context.Background(), ConversationFilter{})
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range []string{"s1", "s2", "absent"} {
			c, err := e.Show(context.Background(), s)
			if err != nil && err != ErrNotFound {
				t.Fatal(err)
			}
			shows = append(shows, c)
		}
		return list, shows
	}
	wantList, wantShows := read(f.st)
	if len(wantList) == 0 || wantShows[0].Cwd != "/example/new/s1" || wantShows[0].LastWrite == nil {
		t.Fatalf("the fixture shows nothing to compare: %+v", wantShows[0])
	}

	// The same data as a store that predates session_ref: no mark, session_ref 0,
	// no session rows.
	dir := f.st.Dir()
	for _, q := range []string{`UPDATE records SET session_ref=0`, `DELETE FROM session_files`, `DELETE FROM session_stats`, `DELETE FROM sessions`, `DELETE FROM meta WHERE key='session_ref_complete'`, `DROP INDEX records_session_ref_source_time`} {
		if _, err := f.db.ExecContext(context.Background(), q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	_ = f.st.Close()
	old, err := store.Open(dir, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = old.Close() })
	if old.SessionRefReady() {
		t.Fatal("a store without the mark is ready")
	}
	gotList, gotShows := read(old)
	if !reflect.DeepEqual(wantList, gotList) {
		t.Errorf("List differs:\n ref: %+v\ntext: %+v", wantList, gotList)
	}
	if !reflect.DeepEqual(wantShows, gotShows) {
		t.Errorf("Show differs:\n ref: %+v\ntext: %+v", wantShows, gotShows)
	}
}
