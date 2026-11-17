package query

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/testkit/storetpl"
)

// The plan tests each need a store holding thousands of rows, and building one
// under -race costs about a second. Tests that only READ the same fixture share
// one store, built on first use (so any test order or a single -run works) and
// removed by TestMain. A test that writes to its store MUST NOT use this.
var shared struct {
	mu     sync.Mutex
	dir    string
	stores map[string]*store.Store
}

func TestMain(m *testing.M) {
	code := m.Run()
	shared.mu.Lock()
	for _, st := range shared.stores {
		_ = st.Close()
	}
	if shared.dir != "" {
		_ = os.RemoveAll(shared.dir)
	}
	shared.mu.Unlock()
	os.Exit(code)
}

// openPrepared opens a store in dir with its schema restored from the
// per-process template (storetpl) instead of built from scratch, and with no
// close-time statistics: these stores are thrown away, and a test that wants
// statistics runs ANALYZE itself. A test of schema creation MUST NOT use it.
func openPrepared(t testing.TB, dir string, opts store.Options) (*store.Store, error) {
	t.Helper()
	opts.NoCloseStats = true
	storetpl.Prepare(t, dir, opts)
	return store.Open(dir, opts)
}

// sharedStore returns the read-only store named key, building it with seed on
// first use.
func sharedStore(t *testing.T, key string, opts store.Options, seed func(tx *sql.Tx) error) *store.Store {
	t.Helper()
	shared.mu.Lock()
	defer shared.mu.Unlock()
	if st, ok := shared.stores[key]; ok {
		return st
	}
	if shared.dir == "" {
		d, err := os.MkdirTemp("", "query-shared-")
		if err != nil {
			t.Fatal(err)
		}
		shared.dir, shared.stores = d, map[string]*store.Store{}
	}
	opts.NoCloseStats = true // closed only by TestMain; nobody reads statistics after
	st, err := openPrepared(t, fmt.Sprintf("%s/%d", shared.dir, len(shared.stores)), opts)
	if err != nil {
		t.Fatal(err)
	}
	shared.stores[key] = st // closed by TestMain even if seeding fails
	if err := st.WithWrite("", seed); err != nil {
		t.Fatal(err)
	}
	return st
}

// sessionPlanStore is the fixture of the two session-pinned plan tests: 100
// sessions of 10 records each, every record a main-thread one (is_sidechain
// false), records alternating user and assistant, no statistics.
func sessionPlanStore(t *testing.T) *store.Store {
	t.Helper()
	return sharedStore(t, "session-plan", store.Options{}, func(tx *sql.Tx) error {
		for _, q := range []string{
			`INSERT INTO files(id,source,mode,first_seen_ns,cur_gen) VALUES(1,'claude-transcript','append',1,0)`,
			`INSERT INTO field_names(id,name) VALUES(1,'is_sidechain')`,
			`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<100) INSERT INTO sessions(session_ref,session_id,first_time_ns) SELECT i,'sess-'||i,0 FROM n`,
			`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<1000)
			 INSERT INTO records(id,file_id,gen,off,item,len,source,type,parser_version,time_ns,session_ref)
			 SELECT i,1,0,i,-1,1,'claude-transcript',CASE WHEN i%2=0 THEN 'user' ELSE 'assistant' END,1,i,i%100+1 FROM n`,
			`INSERT INTO fields(rec_id,name_id,n) SELECT id,1,0 FROM records`,
		} {
			if _, err := tx.ExecContext(context.Background(), q); err != nil {
				return err
			}
		}
		return nil
	})
}
