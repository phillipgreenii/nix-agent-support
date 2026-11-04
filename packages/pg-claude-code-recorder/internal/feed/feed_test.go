package feed_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/clock"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/feed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/query"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/testkit"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/wire"
)

func line(typ, uuid string) string {
	return fmt.Sprintf(`{"type":%q,"uuid":%q,"sessionId":"example-session-1","timestamp":"2026-10-29T20:00:00Z","message":{"role":%q}}`+"\n", typ, uuid, typ)
}

type env struct {
	k   *testkit.Kit
	fe  *feed.Engine
	clk *clock.Fake
	id  int64
}

func newEnv(t *testing.T) *env {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
	k := testkit.NewWithClock(t, clk)
	fe := &feed.Engine{Q: k.Engine, Store: k.Store, Clock: clk, Obs: k.Obs}
	k.Store.OnWrite(fe.Notify)
	return &env{k: k, fe: fe, clk: clk}
}

func (e *env) seed(content string) {
	e.id = e.k.Add("example-proj", "example-session-1.jsonl", content)
	e.k.Parse()
}

func (e *env) append(content string) {
	e.k.Append(e.id, content)
	if _, err := e.k.Parser.ParseFile(e.id); err != nil {
		e.k.T.Fatal(err)
	}
}

func (e *env) feed(req string) (*feed.Answer, *wire.Error) {
	e.k.T.Helper()
	ans, err := e.fe.Feed(context.Background(), []byte(req))
	if err != nil {
		return nil, err
	}
	return ans.(*feed.Answer), nil
}

func (e *env) mustFeed(req string) *feed.Answer {
	e.k.T.Helper()
	a, err := e.feed(req)
	if err != nil {
		e.k.T.Fatalf("%s: %v", req, err)
	}
	return a
}

func after(c string) string { return fmt.Sprintf(`{"after":%q}`, c) }

func uuids(a *feed.Answer) []string {
	var out []string
	for _, en := range a.Entries {
		if en.Kind == "record" {
			out = append(out, fmt.Sprint(en.Fields["type"]))
		} else {
			out = append(out, "reset")
		}
	}
	return out
}

func TestCursorResumeAndAFilteredFeedAdvancesPastNonMatchingCommits(t *testing.T) {
	e := newEnv(t)
	e.seed(line("user", "u1") + line("assistant", "u2") + line("user", "u3"))
	a := e.mustFeed(`{"project":["type","session_id"]}`)
	if got := strings.Join(uuids(a), ","); got != "user,assistant,user" {
		t.Fatalf("entries %s", got)
	}
	if a.More || a.TimedOut || !strings.Contains(a.NextCursor, ":") {
		t.Fatalf("answer %+v", a)
	}
	if a.Entries[0].Fields["session_id"] != "example-session-1" {
		t.Fatalf("session_id alias %v", a.Entries[0].Fields)
	}
	c1 := a.NextCursor
	if again := e.mustFeed(after(c1)); len(again.Entries) != 0 || again.NextCursor != c1 {
		t.Fatalf("resume at head %+v", again)
	}
	e.append(line("assistant", "u4"))
	b := e.mustFeed(after(c1))
	if len(b.Entries) != 1 || b.Entries[0].Fields["type"] != "assistant" || b.NextCursor == c1 {
		t.Fatalf("resume %+v", b)
	}
	// A filter that matches nothing of a new commit still advances the cursor.
	c2 := b.NextCursor
	e.append(line("user", "u5"))
	f := e.mustFeed(fmt.Sprintf(`{"after":%q,"where":{"field":"type","eq":"assistant"}}`, c2))
	if len(f.Entries) != 0 || f.NextCursor == c2 {
		t.Fatalf("empty match must advance next_cursor: %+v (was %s)", f, c2)
	}
}

func TestLimitAndMoreAreKeptAcrossPages(t *testing.T) {
	e := newEnv(t)
	e.seed(line("user", "u1") + line("user", "u2") + line("user", "u3") + line("user", "u4") + line("user", "u5"))
	var seen int
	c := ""
	for pages := 0; pages < 10; pages++ {
		a := e.mustFeed(fmt.Sprintf(`{"after":%q,"limit":2}`, c))
		seen += len(a.Entries)
		c = a.NextCursor
		if !a.More {
			break
		}
		if len(a.Entries) != 2 {
			t.Fatalf("a page with more must be full: %+v", a)
		}
	}
	if seen != 5 {
		t.Fatalf("saw %d records across pages", seen)
	}
}

func TestAWaitReturnsOnAMatchAndTimesOutEmptyOnTheFakeClock(t *testing.T) {
	e := newEnv(t)
	e.seed(line("user", "u1"))
	head := e.mustFeed(`{}`).NextCursor
	req := fmt.Sprintf(`{"after":%q,"wait_ms":30000,"where":{"field":"type","eq":"assistant"}}`, head)

	type res struct {
		a   *feed.Answer
		err *wire.Error
	}
	run := func() chan res {
		ch := make(chan res, 1)
		req := req
		go func() {
			a, err := e.feed(req)
			ch <- res{a, err}
		}()
		for e.fe.Waiters() == 0 {
			time.Sleep(time.Millisecond)
		}
		return ch
	}
	// A commit that does not match wakes nobody: the waiter keeps waiting.
	ch := run()
	e.append(line("user", "u2"))
	select {
	case r := <-ch:
		t.Fatalf("a non-matching commit ended the wait: %+v", r)
	case <-time.After(50 * time.Millisecond):
	}
	e.append(line("assistant", "u3"))
	r := <-ch
	if r.err != nil || len(r.a.Entries) != 1 || r.a.TimedOut {
		t.Fatalf("matching commit: %+v %v", r.a, r.err)
	}
	if e.fe.Waiters() != 0 {
		t.Fatalf("waiter leaked: %d", e.fe.Waiters())
	}
	// With nothing arriving, advancing the fake clock past wait_ms ends the wait
	// with an empty, timed-out answer that still carries next_cursor.
	head = e.mustFeed(`{}`).NextCursor
	req = fmt.Sprintf(`{"after":%q,"wait_ms":30000,"where":{"field":"type","eq":"assistant"}}`, head)
	ch = run()
	e.clk.Advance(31 * time.Second)
	r = <-ch
	if r.err != nil || len(r.a.Entries) != 0 || !r.a.TimedOut || r.a.NextCursor == "" {
		t.Fatalf("timeout: %+v %v", r.a, r.err)
	}
}

func TestWaitMSOverTheCapIsABadRequestAndTheWaiterLimitIsBusy(t *testing.T) {
	e := newEnv(t)
	e.seed(line("user", "u1"))
	if _, err := e.feed(`{"wait_ms":60001}`); err == nil || err.Name != wire.ErrBadRequest {
		t.Fatalf("wait_ms over 60000: %v", err)
	}
	if _, err := e.feed(`{"wait_ms":-1}`); err == nil || err.Name != wire.ErrBadRequest {
		t.Fatalf("negative wait: %v", err)
	}
	head := e.mustFeed(`{}`).NextCursor
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	for i := 1; i <= feed.DefaultMaxWaiters; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = e.fe.Feed(ctx, []byte(fmt.Sprintf(`{"after":%q,"wait_ms":60000}`, head)))
		}()
		for e.fe.Waiters() < i { // admit one at a time: one read connection at once
			time.Sleep(100 * time.Microsecond)
		}
	}
	_, err := e.fe.Feed(context.Background(), []byte(fmt.Sprintf(`{"after":%q,"wait_ms":60000}`, head)))
	if err == nil || err.Name != wire.ErrBusy {
		t.Fatalf("257th waiter: %v", err)
	}
	if v, _ := e.k.Obs.Reg.Gauge("feed_waiters", nil); v != float64(feed.DefaultMaxWaiters) {
		t.Fatalf("feed_waiters gauge %v", v)
	}
	cancel()
	wg.Wait()
	if e.fe.Waiters() != 0 {
		t.Fatalf("waiters after cancel: %d", e.fe.Waiters())
	}
}

func TestAReparseEmitsAResetBeforeTheNewRecordsAndKeepsRecordIds(t *testing.T) {
	e := newEnv(t)
	e.seed(line("user", "u1") + line("assistant", "u2"))
	a := e.mustFeed(`{"project":["type"]}`)
	c0 := a.NextCursor
	first := a.Entries
	if err := e.k.Parser.ReparseGen(e.id, 1); err != nil {
		t.Fatal(err)
	}
	b := e.mustFeed(after(c0))
	if got := strings.Join(uuids(b), ","); got != "reset,user,assistant" {
		t.Fatalf("reparse entries %s", got)
	}
	r := b.Entries[0]
	if r.File != query.FileRef(e.id) || r.Gen != 1 || !strings.HasPrefix(r.Parser, "claude-transcript@") {
		t.Fatalf("reset entry %+v", r)
	}
	// The same record ids come again (a client applies the reset first).
	for i, en := range b.Entries[1:] {
		if *en.Record != *first[i].Record {
			t.Fatalf("record %d ref changed: %+v vs %+v", i, en.Record, first[i].Record)
		}
	}
	for i := 1; i < len(b.Entries); i++ {
		if seq(t, b.Entries[i-1]) >= seq(t, b.Entries[i]) {
			t.Fatalf("sequence not increasing at %d", i)
		}
	}
}

func seq(t *testing.T, en feed.Entry) int64 {
	t.Helper()
	_, s, ok := query.ParseCursor(en.Seq)
	if !ok {
		t.Fatalf("seq %q", en.Seq)
	}
	return s
}

func TestAResetReachesOnlyFeedsWhoseFilterCouldMatchTheFile(t *testing.T) {
	e := newEnv(t)
	e.seed(line("user", "u1") + line("assistant", "u2"))
	c0 := e.mustFeed(`{}`).NextCursor
	if err := e.k.Parser.ReparseGen(e.id, 1); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		where string
		reset bool
	}{
		"none":               {``, true},
		"type present":       {`{"field":"type","eq":"assistant"}`, true},
		"type in":            {`{"field":"type","in":["x","user"]}`, true},
		"source matches":     {`{"field":"source","eq":"claude-transcript"}`, true},
		"other source":       {`{"field":"source","eq":"claude-history"}`, false},
		"type never in file": {`{"field":"type","eq":"registry"}`, false},
		"all with miss":      {`{"all":[{"field":"type","eq":"user"},{"field":"source","eq":"session-registry"}]}`, false},
		"any with hit":       {`{"any":[{"field":"type","eq":"registry"},{"field":"type","eq":"user"}]}`, true},
		"unknowable leaf":    {`{"field":"session","eq":"someone-else"}`, true},
	} {
		req := fmt.Sprintf(`{"after":%q`, c0)
		if tc.where != "" {
			req += `,"where":` + tc.where
		}
		a := e.mustFeed(req + `}`)
		got := false
		for _, en := range a.Entries {
			got = got || en.Kind == "reset"
		}
		if got != tc.reset {
			t.Errorf("%s: reset delivered=%v, want %v", name, got, tc.reset)
		}
	}
}

func TestAResetSplitAcrossPagesStillDeliversTheNewRecords(t *testing.T) {
	e := newEnv(t)
	e.seed(line("user", "u1") + line("assistant", "u2") + line("user", "u3"))
	c0 := e.mustFeed(`{}`).NextCursor
	if err := e.k.Parser.ReparseGen(e.id, 1); err != nil {
		t.Fatal(err)
	}
	want := uuids(e.mustFeed(after(c0) + ""))
	var got []string
	c := c0
	pages := 0
	for {
		a := e.mustFeed(fmt.Sprintf(`{"after":%q,"limit":1}`, c))
		got = append(got, uuids(a)...)
		pages++
		c = a.NextCursor
		if !a.More {
			break
		}
	}
	if strings.Join(got, ",") != strings.Join(want, ",") || pages < 4 || got[0] != "reset" {
		t.Fatalf("paged %v (%d pages), unpaged %v", got, pages, want)
	}
}

func TestCursorRulesFloorHeadEpochAndSyntax(t *testing.T) {
	e := newEnv(t)
	e.seed(line("user", "u1"))
	ca := e.mustFeed(`{}`).NextCursor
	e.append(line("user", "u2"))
	cb := e.mustFeed(`{}`).NextCursor
	// Both commits are 15 days old by the time a third arrives.
	e.clk.Advance(15 * 24 * time.Hour)
	e.append(line("user", "u3"))

	if _, err := e.feed(after(cb)); err != nil {
		t.Fatalf("a cursor at the floor is valid: %v", err)
	}
	_, err := e.feed(after(ca))
	if err == nil || err.Name != wire.ErrCursorExpired || err.Cursor == "" {
		t.Fatalf("below the retention floor: %v", err)
	}
	if a := e.mustFeed(`{}`); len(a.Entries) != 1 {
		t.Fatalf("an empty cursor starts at the floor: %+v", a.Entries)
	}
	head := e.mustFeed(`{}`).NextCursor
	epoch, hs, _ := query.ParseCursor(head)
	for name, c := range map[string]string{
		"beyond the head": query.Cursor(epoch, hs+10),
		"old epoch":       query.Cursor(epoch+3, 0),
	} {
		if _, err := e.feed(after(c)); err == nil || err.Name != wire.ErrCursorExpired || err.Cursor != head {
			t.Errorf("%s: %v (want resync cursor %s)", name, err, head)
		}
	}
	for _, c := range []string{"abc", "1:x", "1:-2", ":"} {
		if _, err := e.feed(after(c)); err == nil || err.Name != wire.ErrBadRequest {
			t.Errorf("cursor %q: %v", c, err)
		}
	}
}

func TestSnapshotThenTailDeliversEachRecordOnce(t *testing.T) {
	e := newEnv(t)
	e.seed(line("user", "u1") + line("assistant", "u2") + line("user", "u3") + line("assistant", "u4"))
	snap := func(req string) *feed.SnapshotAnswer {
		a, err := e.fe.Snapshot(context.Background(), []byte(req))
		if err != nil {
			t.Fatalf("%s: %v", req, err)
		}
		return a.(*feed.SnapshotAnswer)
	}
	p1 := snap(`{"limit":2,"project":["type"]}`)
	if len(p1.Entries) != 2 || !p1.More || p1.NextPage == "" {
		t.Fatalf("page 1 %+v", p1)
	}
	// The file is re-parsed between the pages and a line arrives: the snapshot
	// stays at its cursor, and the feed after it carries the reset and the rest.
	if err := e.k.Parser.ReparseGen(e.id, 1); err != nil {
		t.Fatal(err)
	}
	e.append(line("user", "u5"))
	p2 := snap(fmt.Sprintf(`{"limit":2,"at":%q,"page":%q,"project":["type"]}`, p1.Cursor, p1.NextPage))
	if p2.Cursor != p1.Cursor {
		t.Fatalf("cursor moved: %s vs %s", p2.Cursor, p1.Cursor)
	}
	tail := e.mustFeed(after(p1.Cursor))
	if len(tail.Entries) == 0 || tail.Entries[0].Kind != "reset" {
		t.Fatalf("the feed after the snapshot carries the reset first: %v", uuids(tail))
	}
	if n := strings.Count(strings.Join(uuids(tail), ","), "user"); n < 3 {
		t.Fatalf("tail %v", uuids(tail))
	}
	if _, err := e.fe.Snapshot(context.Background(), []byte(`{"at":"99:0"}`)); err == nil || err.Name != wire.ErrCursorExpired {
		t.Fatalf("a snapshot cursor from another epoch: %v", err)
	}
	if _, err := e.fe.Snapshot(context.Background(), []byte(`{"page":"x"}`)); err == nil || err.Name != wire.ErrBadRequest {
		t.Fatalf("bad page: %v", err)
	}
}

func TestRawReadsARecordAFileRangeAndAnArchivedCopy(t *testing.T) {
	e := newEnv(t)
	l1, l2 := line("user", "u1"), line("assistant", "u2")
	e.seed(l1 + l2)
	raw := func(req string) (*feed.RawAnswer, *wire.Error) {
		a, err := e.fe.Raw(context.Background(), []byte(req))
		if err != nil {
			return nil, err
		}
		r := a.(feed.RawAnswer)
		return &r, nil
	}
	a, err := raw(fmt.Sprintf(`{"record":{"file":%q,"gen":1,"offset":%d,"item":-1}}`, query.FileRef(e.id), len(l1)))
	if err != nil || a.Data != l2 || a.Encoding != "utf8" {
		t.Fatalf("record: %+v %v", a, err)
	}
	a, err = raw(fmt.Sprintf(`{"file":%q,"offset":5,"length":10}`, query.FileRef(e.id)))
	if err != nil || a.Data != (l1 + l2)[5:15] || a.Gen != 1 {
		t.Fatalf("range: %+v %v", a, err)
	}
	for name, req := range map[string]string{
		"unknown record": `{"record":{"file":"f_999","gen":1,"offset":0,"item":-1}}`,
		"no copy":        `{"file":"f_999","gen":1,"length":4}`,
	} {
		if _, err := raw(req); err == nil || err.Name != wire.ErrNotFound {
			t.Errorf("%s: %v", name, err)
		}
	}
	for name, req := range map[string]string{
		"nothing named": `{}`, "bad ref": `{"file":"x","length":1}`, "too long": `{"file":"f_1","length":99999999}`,
	} {
		if _, err := raw(req); err == nil || err.Name != wire.ErrBadRequest {
			t.Errorf("%s: %v", name, err)
		}
	}
	// One pointer abstraction: once the copy moves to the archive the same
	// request answers the same bytes.
	if err := os.Rename(e.k.Store.CopyPath(e.id, 1), e.k.Store.ArchivePath(e.id, 1)); err != nil {
		t.Fatal(err)
	}
	a, err = raw(fmt.Sprintf(`{"record":{"file":%q,"gen":1,"offset":0,"item":-1}}`, query.FileRef(e.id)))
	if err != nil || a.Data != l1 {
		t.Fatalf("archived record: %+v %v", a, err)
	}
	if v, err := e.k.Parser.Value(e.id, 1, 0, len(l1), "claude-transcript", -1, "type"); err != nil || v != "user" {
		t.Fatalf("field read through the archive: %q %v", v, err)
	}
}

func TestWhileBehindAnEveryOperationAnswersAnywayMarkedDegraded(t *testing.T) {
	e := newEnv(t)
	e.seed(line("user", "u1"))
	e.fe.Behind = func() bool { return true }
	e.k.Engine.Behind = func() bool { return true }
	if a := e.mustFeed(`{}`); len(a.Entries) != 1 || !a.Degraded {
		t.Fatalf("feed %+v", a)
	}
	snap, err := e.fe.Snapshot(context.Background(), []byte(`{}`))
	if err != nil || !snap.(*feed.SnapshotAnswer).Degraded {
		t.Fatalf("snapshot %+v %v", snap, err)
	}
	q, werr := e.k.Engine.Query(context.Background(), query.Request{})
	if werr != nil || !q.Degraded || len(q.Rows) != 1 {
		t.Fatalf("query %+v %v", q, werr)
	}
	e.fe.Behind = func() bool { return false }
	if a := e.mustFeed(`{}`); a.Degraded {
		t.Fatalf("caught up but degraded: %+v", a)
	}
}

func TestFloorQueryPlanUsesTheAtNsIndex(t *testing.T) {
	e := newEnv(t)
	rows, err := e.k.Store.Read().Query(`EXPLAIN QUERY PLAN `+feed.FloorSQL, int64(1))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var plan []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, detail)
	}
	if got := strings.Join(plan, "; "); !strings.Contains(got, "feed_marks_at") {
		t.Fatalf("floor plan does not use feed_marks_at: %s", got)
	}
}

// A clock step back leaves at_ns non-monotonic in rec_id; the floor must equal
// what the plain MAX(rec_id) query returns.
func TestFloorIsUnchangedWhenAtNsIsNotMonotonicInRecID(t *testing.T) {
	e := newEnv(t)
	db := e.k.Store.Read()
	w, err := sql.Open("sqlite", filepath.Join(e.k.Store.Dir(), store.DBName)+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()
	for _, m := range [][2]int64{{1, 100}, {2, 500}, {3, 200}, {4, 900}, {5, 300}} {
		if _, err := w.Exec(`INSERT INTO feed_marks(rec_id,at_ns) VALUES(?,?)`, m[0], m[1]); err != nil {
			t.Fatal(err)
		}
	}
	for _, cut := range []int64{50, 150, 250, 350, 600, 1000} {
		var want, got int64
		if err := db.QueryRow(`SELECT COALESCE(MAX(rec_id),0) FROM feed_marks WHERE at_ns < ?`, cut).Scan(&want); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(feed.FloorSQL, cut).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("cutoff %d: floor %d, original %d", cut, got, want)
		}
	}
}
