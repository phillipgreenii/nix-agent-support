package lifecycle

import (
	"context"
	"database/sql"
	"reflect"
	"testing"
)

// uncachedModel is the model a brand-new Engine derives from the store: no
// registry cache and no model cache.
func (v *liveEnv) uncachedModel() *model {
	v.t.Helper()
	m, err := (&Engine{St: v.e.St}).loadWith(context.Background(), false)
	if err != nil {
		v.t.Fatal(err)
	}
	return m
}

func (v *liveEnv) writeRegistry(pid int, session, status string) {
	v.t.Helper()
	v.k.AddRegistry(itoa(int64(pid)), liveRegistry(pid, session, status))
	v.k.Parse()
}

// Answers of the process requests equal those of a fresh engine after
// incremental loads, including a Process that moves to another session and one
// whose registry file is deleted.
func TestIncrementalRegistryLoadKeepsProcessAnswers(t *testing.T) {
	v := newLiveEnv(t)
	ctx := context.Background()
	v.writeRegistry(73291, liveSessA, "idle")
	if _, err := v.e.Processes(ctx, ProcessFilter{}); err != nil {
		t.Fatal(err)
	}
	v.writeRegistry(73291, liveSessA, "busy")
	v.writeRegistry(73292, liveSessB, "idle")
	v.writeRegistry(73291, liveSessB, "busy")
	fresh := &Engine{St: v.e.St}
	got, err := v.e.Processes(ctx, ProcessFilter{})
	if err != nil {
		t.Fatal(err)
	}
	want, err := fresh.Processes(ctx, ProcessFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !reflect.DeepEqual(got, want) {
		t.Fatalf("process.list %v, fresh engine %v", got, want)
	}
	gp, err := v.e.Process(ctx, 73291, "")
	if err != nil {
		t.Fatal(err)
	}
	wp, err := fresh.Process(ctx, 73291, "")
	if err != nil {
		t.Fatal(err)
	}
	// The fixture's records share one timestamp, so the holdings order is not
	// defined; compare their count and the rest.
	if len(gp.Conversations) != len(wp.Conversations) || len(gp.Conversations) != 2 {
		t.Fatalf("holdings %v, fresh engine %v", gp.Conversations, wp.Conversations)
	}
	gp.Conversations, wp.Conversations = nil, nil
	if len(gp.Versions) != 3 || !reflect.DeepEqual(gp, wp) {
		t.Fatalf("process.show %+v, fresh engine %+v", gp, wp)
	}
	live, err := v.e.LiveSessions()
	if err != nil {
		t.Fatal(err)
	}
	wl, err := fresh.LiveSessions()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(live, wl) {
		t.Fatalf("LiveSessions %v, fresh engine %v", live, wl)
	}
}

// Registry records removed behind the cache (a rewritten generation clears
// rows without rolling anything back) are not served from it.
func TestIncrementalRegistryLoadDropsClearedRecords(t *testing.T) {
	v := newLiveEnv(t)
	ctx := context.Background()
	for i := 0; i < 4; i++ {
		v.writeRegistry(73291, liveSessA, []string{"idle", "busy"}[i%2])
	}
	if _, err := v.e.loadCtx(ctx); err != nil {
		t.Fatal(err)
	}
	err := v.k.Store.WithWrite("test-clear", func(tx *sql.Tx) error {
		const sub = `(SELECT id FROM records WHERE source='session-registry' ORDER BY id LIMIT 2)`
		if _, err := tx.ExecContext(context.Background(), `DELETE FROM fields WHERE rec_id IN `+sub); err != nil {
			return err
		}
		_, err := tx.ExecContext(context.Background(), `DELETE FROM records WHERE id IN `+sub)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	v.k.Store.NoteModelInput()
	v.writeRegistry(73291, liveSessA, "idle")
	m, err := v.e.loadCtx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := v.uncachedModel()
	if !reflect.DeepEqual(m, want) {
		t.Fatalf("model after a clear: %d versions, fresh load %d", len(m.vers[pkey{73291, liveProcStart}]), len(want.vers[pkey{73291, liveProcStart}]))
	}
}
