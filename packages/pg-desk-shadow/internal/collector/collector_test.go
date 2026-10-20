package collector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-desk-shadow/internal/runner"
	"github.com/phillipgreenii/pg-desk-shadow/internal/schema"
	"github.com/phillipgreenii/pg-desk-shadow/internal/scratch"
	"github.com/phillipgreenii/pg-desk-shadow/internal/sqlite"
	"github.com/phillipgreenii/pg-desk-shadow/internal/testutil"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }
func (c *clock) sleep(_ context.Context, until time.Time) error {
	if until.After(c.t) {
		c.t = until
	}
	return nil
}

// clientCall is one request the collector sent to the (fake) client.
type clientCall struct {
	instance, op, query string
	consumer            string
	reset               bool
	nextSeq             int64
}

// fakeExec stands in for the sandboxed client binary: every RunStdin call is
// one {op, args} request on one instance.
type fakeExec struct {
	clk   *clock
	calls []clientCall
	argv  [][]string
	// client programs the answer; nil answers every poll with an empty
	// succeeded feed and every acknowledgement with its position.
	client func(c clientCall) (runner.Result, error)
	// advance is how long the first poll of a tick takes on the fake clock.
	advance time.Duration
	// firstQuery is the query whose pr poll starts a tick.
	firstQuery string
}

func (f *fakeExec) Run(context.Context, time.Duration, ...string) (runner.Result, error) {
	return runner.Result{}, nil
}

func (f *fakeExec) RunStdin(_ context.Context, _ time.Duration, stdin string, _ []string, argv ...string) (runner.Result, error) {
	var req struct {
		Op   string `json:"op"`
		Args struct {
			Consumer string `json:"consumer"`
			Query    string `json:"query"`
			Reset    bool   `json:"reset"`
			NextSeq  int64  `json:"next_seq"`
		} `json:"args"`
	}
	if err := json.Unmarshal([]byte(stdin), &req); err != nil {
		return runner.Result{}, err
	}
	c := clientCall{instance: argv[len(argv)-1], op: req.Op, query: req.Args.Query, consumer: req.Args.Consumer, reset: req.Args.Reset, nextSeq: req.Args.NextSeq}
	f.calls = append(f.calls, c)
	f.argv = append(f.argv, argv)
	if c.op == "changes" && c.instance == "pr" && c.query == f.firstQuery {
		f.clk.t = f.clk.t.Add(f.advance)
	}
	if f.client != nil {
		return f.client(c)
	}
	return defaultAnswer(c), nil
}

func defaultAnswer(c clientCall) runner.Result {
	if c.op == "changes_ack" {
		return runner.Result{Stdout: ackAnswer(c.query, c.nextSeq)}
	}
	return runner.Result{Stdout: okAnswer(0)}
}

// polls counts the changes requests (not the acknowledgements).
func (f *fakeExec) polls() (n int) {
	for _, c := range f.calls {
		if c.op == "changes" {
			n++
		}
	}
	return n
}

// acks returns the changes_ack requests.
func (f *fakeExec) acks() (out []clientCall) {
	for _, c := range f.calls {
		if c.op == "changes_ack" {
			out = append(out, c)
		}
	}
	return out
}

type env struct {
	l   scratch.Layout
	m   scratch.Manifest
	db  sqlite.DB
	clk *clock
	ex  *fakeExec
	cfg Config
}

// okAnswer is a succeeded changes answer with one source at nextSeq.
func okAnswer(nextSeq int64, changes ...string) string {
	return fmt.Sprintf(`{"protocolVersion":1,"schemaVersion":12,"result":{"sources":[{"backend":"b","status":"succeeded","version":0,"truncated":false,"next_seq":%d}],"changes":[%s],"sources_freshness":[]}}`, nextSeq, strings.Join(changes, ","))
}

// sourceAnswer is a changes answer whose single source has the given status.
func sourceAnswer(status, reason string, nextSeq int64) string {
	return fmt.Sprintf(`{"protocolVersion":1,"schemaVersion":12,"result":{"sources":[{"backend":"b","status":%q,"version":0,"truncated":false,"reason":%q,"next_seq":%d}],"changes":[],"sources_freshness":[]}}`, status, reason, nextSeq)
}

func ackAnswer(query string, seq int64) string {
	return fmt.Sprintf(`{"protocolVersion":1,"schemaVersion":12,"result":{"consumer":"shadow-compare","query":%q,"acked_seq":%d}}`, query, seq)
}

func errAnswer(code, msg string) string {
	return fmt.Sprintf(`{"protocolVersion":1,"error":{"code":%q,"message":%q}}`, code, msg)
}

func fieldsJSON(names []string) string {
	var parts []string
	for _, n := range names {
		parts = append(parts, fmt.Sprintf(`{"name":%q,"before":null,"after":"x"}`, n))
	}
	return "[" + strings.Join(parts, ",") + "]"
}

func kindsJSON(kinds []string) string {
	b, _ := json.Marshal(kinds)
	return string(b)
}

// prChange is one changes[] row of the pr instance.
func prChange(change, id string, seq int64, kinds []string, fields ...string) string {
	return fmt.Sprintf(`{"change":%q,"source":"b","entity":{"id":%q,"version":1,"head_sha":"abc","updated_at":"2026-01-05T09:58:00Z","title":"t"},"seq":%d,"kinds":%s,"fields":%s}`, change, id, seq, kindsJSON(kinds), fieldsJSON(fields))
}

// ciChange is one changes[] row of the ci instance.
func ciChange(change, prID, rollup string, seq int64, kinds []string, fields ...string) string {
	return fmt.Sprintf(`{"change":%q,"source":"b","entity":{"pr_id":%q,"rollup":%q,"runs":[],"version":1,"head_sha":"abc"},"seq":%d,"kinds":%s,"fields":%s}`, change, prID, rollup, seq, kindsJSON(kinds), fieldsJSON(fields))
}

func newEnv(t *testing.T) *env {
	t.Helper()
	testutil.RequireSQLite(t)
	root := t.TempDir()
	l := scratch.Layout{Root: root}
	if err := l.MkdirAll(); err != nil {
		t.Fatal(err)
	}
	live := t.TempDir()
	m := scratch.Manifest{
		Phase: "A", CreatedAt: "2026-01-05T09:00:00Z", Seeded: true, BDMode: "passthrough", Consumer: "shadow-compare", MaxPerPoll: 50,
		Builds: map[string]string{"pg-desk": "pg-desk version 0.0.0-test"}, Scrub: []string{"acme/api", "someone"},
		Live: scratch.LiveSources{RouterEvents: filepath.Join(live, "events.jsonl"), RouterQueue: filepath.Join(live, "queue.jsonl"), RunRecord: filepath.Join(live, "run-record.log"), ConnectorEvents: filepath.Join(live, "conn.jsonl")},
	}
	// The scratch pg-desk store only serves Recover now; the feed never reads it.
	db := testutil.FakeStore(t, l.StoreDB())
	ctx := context.Background()
	if err := db.Exec(ctx, `INSERT INTO consumer (name, type, cursor) VALUES ('shadow-compare','pr',0);`); err != nil {
		t.Fatal(err)
	}
	clk := &clock{t: time.Date(2026, 1, 5, 9, 59, 30, 0, time.UTC)}
	ex := &fakeExec{clk: clk, advance: 10 * time.Second, firstQuery: "mine"}
	cfg := Config{
		Layout: l, Manifest: m, Exec: ex, Period: time.Minute, TickTimeout: time.Minute, KillPointsPerHour: 1500, MaxFailedTicks: 10,
		FloorMargin: 200, WarmupExclusion: true, Now: clk.now, Sleep: clk.sleep,
		ClientBin: filepath.Join(l.BinDir(), scratch.DaemonBinary), ClientConfig: l.DaemonConfig(), Queries: []string{"mine", "team"}, Consumer: "shadow-compare",
	}
	return &env{l: l, m: m, db: db, clk: clk, ex: ex, cfg: cfg}
}

func (e *env) collector(t *testing.T) *Collector {
	t.Helper()
	e.cfg.Manifest = e.m
	c, err := New(e.cfg)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func (e *env) rows(t *testing.T) []schema.Row {
	t.Helper()
	rows, err := schema.ReadAll(e.l.TicksFile())
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func (e *env) ticks(t *testing.T) []schema.Row {
	var out []schema.Row
	for _, r := range e.rows(t) {
		if r.Kind == schema.KindTick {
			out = append(out, r)
		}
	}
	return out
}

// tickAt runs one Tick at the given slot and fails the test on an error.
func (e *env) tickAt(t *testing.T, c *Collector, slot time.Time) {
	t.Helper()
	if _, err := c.Tick(context.Background(), slot); err != nil {
		t.Fatal(err)
	}
}

var slot0 = time.Date(2026, 1, 5, 10, 0, 0, 0, time.UTC)

// originRow appends one kind=origin row of the daemon event log at t.
func (e *env) originRow(t *testing.T, at time.Time, cost int) {
	t.Helper()
	row := fmt.Sprintf(`{"time":%q,"kind":"origin","purpose":"list","bucket":"graphql","graphql_cost":%d}`+"\n", at.Format(time.RFC3339Nano), cost)
	f, err := os.OpenFile(e.l.ConnectorLog(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(row); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
}

// standin points the collector at the stand-in client script, run by a real
// (unsandboxed) runner, with its canned answers in a fresh directory.
type standin struct {
	t   *testing.T
	dir string
}

func (e *env) useStandin(t *testing.T) *standin {
	t.Helper()
	jqDir := testutil.RequireJQ(t)
	script, err := filepath.Abs(filepath.Join("testdata", "standin-client.sh"))
	if err != nil {
		t.Fatal(err)
	}
	s := &standin{t: t, dir: t.TempDir()}
	e.cfg.Exec = testutil.NewRunner(t)
	e.cfg.ClientBin = script
	e.cfg.ClientEnv = []string{"STANDIN_DIR=" + s.dir, "TICKS_FILE=" + e.l.TicksFile(), "PATH=" + jqDir + ":/usr/bin:/bin"}
	return s
}

// put writes one canned answer file, for example put("pr-changes-mine.json", ...).
func (s *standin) put(name, body string) {
	s.t.Helper()
	if err := os.WriteFile(filepath.Join(s.dir, name), []byte(body), 0o600); err != nil {
		s.t.Fatal(err)
	}
}

// putQuiet answers every poll of the queries with an empty feed at seq 0 and
// every acknowledgement with its position.
func (s *standin) putQuiet(queries ...string) {
	for _, q := range queries {
		for _, inst := range []string{"pr", "ci"} {
			s.put(inst+"-changes-"+q+".json", okAnswer(0))
			s.put(inst+"-changes_ack-"+q+".json", ackAnswer(q, 0))
		}
	}
}

// log is the stand-in's call log, one line per entry.
func (s *standin) log() []string {
	s.t.Helper()
	b, err := os.ReadFile(filepath.Join(s.dir, "calls.log"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		s.t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

// ---- the feed poll: mapping, acknowledgement, redelivery, errors -----------

func TestPRFeedRowsMapToItems(t *testing.T) {
	cases := []struct {
		name, change string
		kinds        []string
		fields       []string
	}{
		{"added", "added", []string{"opened"}, nil},
		{"changed", "changed", []string{"head_changed", "reconcile"}, []string{"head_sha", "state"}},
		{"removed keeps its row and its kinds", "removed", []string{"closed"}, []string{"state"}},
		{"a row with no kinds carries an empty array, never null", "changed", []string{}, []string{"label_count"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			s := e.useStandin(t)
			e.cfg.Queries = []string{"mine"}
			s.putQuiet("mine")
			s.put("pr-changes-mine.json", okAnswer(7, prChange(c.change, "owner/repo#12", 7, c.kinds, c.fields...)))
			col := e.collector(t)
			e.tickAt(t, col, slot0)
			r := e.ticks(t)[0]
			if r.Status != schema.StatusOK || r.ExitCode == nil || *r.ExitCode != 0 || r.Hydrations != 0 {
				t.Fatalf("row = %+v", r)
			}
			if len(r.Items) != 1 {
				t.Fatalf("items = %+v", r.Items)
			}
			it := r.Items[0]
			want := schema.Item{
				EntityID: "owner/repo#12", Kinds: c.kinds, Seq: 7, Origin: schema.OriginDaemonFeed, At: "2026-01-05T09:59:30.000Z",
				Fields: c.fields, UpdatedAt: "2026-01-05T09:58:00Z",
			}
			if it.EntityID != want.EntityID || strings.Join(it.Kinds, ",") != strings.Join(want.Kinds, ",") || it.Kinds == nil || it.Seq != 7 ||
				it.Origin != "daemon-feed" || it.At != want.At || strings.Join(it.Fields, ",") != strings.Join(want.Fields, ",") ||
				it.UpdatedAt != want.UpdatedAt || it.Replay || it.CIRollup != "" {
				t.Errorf("item = %+v, want %+v", it, want)
			}
			if b, _ := json.Marshal(it); strings.Contains(string(b), `"kinds":null`) {
				t.Errorf("kinds serialised as null: %s", b)
			}
		})
	}
}

func TestCIFeedRowMapsTheRollup(t *testing.T) {
	e := newEnv(t)
	s := e.useStandin(t)
	e.cfg.Queries = []string{"mine"}
	s.putQuiet("mine")
	s.put("ci-changes-mine.json", okAnswer(4, ciChange("changed", "owner/repo#12", "failure", 4, []string{"checks"}, "rollup", "runs")))
	col := e.collector(t)
	e.tickAt(t, col, slot0)
	r := e.ticks(t)[0]
	if len(r.Items) != 1 {
		t.Fatalf("items = %+v", r.Items)
	}
	it := r.Items[0]
	if it.EntityID != "owner/repo#12" || it.Origin != schema.OriginDaemonCIFeed || it.CIRollup != "failure" || it.UpdatedAt != "" ||
		it.Seq != 4 || strings.Join(it.Kinds, ",") != "checks" || strings.Join(it.Fields, ",") != "rollup,runs" || it.At != "2026-01-05T09:59:30.000Z" {
		t.Errorf("ci item = %+v", it)
	}
	if b, _ := json.Marshal(it); !strings.Contains(string(b), `"ci_rollup":"failure"`) {
		t.Errorf("item JSON = %s", b)
	}
}

func TestSourcesListEveryQueryOnBothInstancesAndTheCursorIsFirstAndLastNextSeq(t *testing.T) {
	e := newEnv(t)
	s := e.useStandin(t)
	s.putQuiet("mine", "team")
	s.put("pr-changes-mine.json", okAnswer(5))
	s.put("ci-changes-mine.json", okAnswer(6))
	s.put("pr-changes-team.json", okAnswer(7))
	s.put("ci-changes-team.json", sourceAnswer("degraded", "rate limited for acme/api", 9))
	col := e.collector(t)
	e.tickAt(t, col, slot0)
	r := e.ticks(t)[0]
	var got []string
	for _, src := range r.Sources {
		got = append(got, src.Query+"="+src.Status)
		if strings.Contains(src.Reason, "acme/api") {
			t.Errorf("source reason leaks: %q", src.Reason)
		}
	}
	want := "mine=succeeded,ci:mine=succeeded,team=succeeded,ci:team=degraded"
	if strings.Join(got, ",") != want {
		t.Errorf("sources = %v, want %s", got, want)
	}
	if r.CursorFrom != 5 || r.CursorTo != 9 {
		t.Errorf("cursor %d..%d, want 5..9 (the first and last next_seq seen)", r.CursorFrom, r.CursorTo)
	}
	if r.Status != schema.StatusOK {
		t.Errorf("a degraded source is an answer, not a failure: %+v", r)
	}
	// The degraded key is not acknowledged (no succeeded source); the rest are.
	var acked []string
	for _, line := range s.log() {
		if strings.Contains(line, `"op":"changes_ack"`) {
			acked = append(acked, strings.Fields(line)[0]+":"+line[strings.Index(line, `"query":"`)+9:strings.Index(line, `"query":"`)+13])
		}
	}
	if len(acked) != 3 {
		t.Errorf("acks = %v, want 3 (the degraded ci:team is not acknowledged)", acked)
	}
}

func TestTheAckFollowsTheTickRowOnDisk(t *testing.T) {
	e := newEnv(t)
	s := e.useStandin(t)
	e.cfg.Queries = []string{"mine"}
	s.putQuiet("mine")
	s.put("pr-changes-mine.json", okAnswer(3, prChange("changed", "owner/repo#1", 3, []string{"head_changed"}, "head_sha")))
	s.put("pr-changes_ack-mine.json", ackAnswer("mine", 3))
	col := e.collector(t)
	e.tickAt(t, col, slot0)
	lines := s.log()
	var acks int
	for i, line := range lines {
		if !strings.Contains(line, `"op":"changes_ack"`) {
			continue
		}
		acks++
		if i+1 >= len(lines) || lines[i+1] != "2" {
			t.Errorf("after the ack request %q the stand-in saw %v ticks lines, want 2 (tick_start and the tick row)", line, lines[i+1:])
		}
	}
	if acks != 2 {
		t.Fatalf("%d acks, want 2 (one per instance): %v", acks, lines)
	}
	if !strings.Contains(strings.Join(lines, "\n"), `"next_seq":3,"query":"mine"`) {
		t.Errorf("the pr ack must carry next_seq 3: %v", lines)
	}
	// Without the ordering the count would be 1 (tick_start only): prove the
	// observable differs, so the assertion above cannot pass vacuously.
	if rows := e.rows(t); len(rows) != 2 || rows[1].Kind != schema.KindTick {
		t.Errorf("rows = %+v", rows)
	}
}

func TestAPollWithNoRowsStillAcknowledgesItsNextSeq(t *testing.T) {
	e := newEnv(t)
	s := e.useStandin(t)
	e.cfg.Queries = []string{"mine"}
	s.putQuiet("mine")
	s.put("pr-changes-mine.json", okAnswer(11))
	s.put("ci-changes-mine.json", okAnswer(12))
	col := e.collector(t)
	e.tickAt(t, col, slot0)
	if r := e.ticks(t)[0]; len(r.Items) != 0 {
		t.Fatalf("items = %+v", r.Items)
	}
	log := strings.Join(s.log(), "\n")
	for _, want := range []string{`pr {"args":{"consumer":"shadow-compare","next_seq":11,"query":"mine"},"op":"changes_ack"}`, `ci {"args":{"consumer":"shadow-compare-ci","next_seq":12,"query":"mine"},"op":"changes_ack"}`} {
		if !strings.Contains(log, want) {
			t.Errorf("missing ack %s in:\n%s", want, log)
		}
	}
}

func TestRowsRedeliveredAfterACrashProduceNoDuplicateItem(t *testing.T) {
	e := newEnv(t)
	s := e.useStandin(t)
	e.cfg.Queries = []string{"mine"}
	s.putQuiet("mine")
	rows := []string{prChange("changed", "owner/repo#1", 3, []string{"head_changed"}, "head_sha"), prChange("added", "owner/repo#2", 4, []string{"opened"})}
	s.put("pr-changes-mine.json", okAnswer(4, rows...))
	crash := errors.New("simulated crash between the row write and the ack")
	e.cfg.AfterRowWrite = func() error { return crash }
	col := e.collector(t)
	if _, err := col.Tick(context.Background(), slot0); !errors.Is(err, crash) {
		t.Fatalf("tick = %v, want the crash", err)
	}
	if strings.Contains(strings.Join(s.log(), "\n"), "changes_ack") {
		t.Fatalf("no ack may be sent when the tick ended after the row write: %v", s.log())
	}
	if got := e.ticks(t); len(got) != 1 || len(got[0].Items) != 2 {
		t.Fatalf("first row = %+v", got)
	}

	// A new collector over the same persisted state; the stand-in redelivers the
	// same two rows and one new one.
	s.put("pr-changes-mine.2.json", okAnswer(5, append(rows, prChange("changed", "owner/repo#2", 5, []string{"reconcile"}))...))
	e.cfg.AfterRowWrite = nil
	e.clk.t = e.clk.t.Add(time.Minute)
	col2 := e.collector(t)
	e.tickAt(t, col2, slot0.Add(time.Minute))
	type key struct {
		origin, id string
		seq        int64
	}
	seen := map[key]int{}
	for _, r := range e.ticks(t) {
		for _, it := range r.Items {
			seen[key{it.Origin, it.EntityID, it.Seq}]++
		}
	}
	if len(seen) != 3 {
		t.Errorf("distinct items = %v, want 3", seen)
	}
	for k, n := range seen {
		if n != 1 {
			t.Errorf("item %+v written %d times", k, n)
		}
	}
	if !strings.Contains(strings.Join(s.log(), "\n"), `"next_seq":5,"query":"mine"},"op":"changes_ack"`) {
		t.Errorf("the second tick must acknowledge next_seq 5: %v", s.log())
	}
}

func TestACursorExpiredAnswerCausesExactlyOneResetRePollWithTheReplayedRowsMarked(t *testing.T) {
	e := newEnv(t)
	s := e.useStandin(t)
	e.cfg.Queries = []string{"mine"}
	s.putQuiet("mine")
	s.put("pr-changes-mine.json", errAnswer("invalid_argument", "cursor_expired: the position of this key is no longer retained; re-run with --reset"))
	s.put("pr-changes-mine.2.json", okAnswer(9, prChange("added", "owner/repo#1", 0, []string{"opened"}), prChange("added", "owner/repo#2", 0, []string{"opened"})))
	col := e.collector(t)
	e.tickAt(t, col, slot0)
	var prPolls []string
	for _, line := range s.log() {
		if strings.HasPrefix(line, "pr ") && strings.Contains(line, `"op":"changes"`) {
			prPolls = append(prPolls, line)
		}
	}
	if len(prPolls) != 2 || strings.Contains(prPolls[0], "reset") || !strings.Contains(prPolls[1], `"reset":true`) {
		t.Fatalf("pr polls = %v, want a plain poll then exactly one reset poll", prPolls)
	}
	r := e.ticks(t)[0]
	if r.Status != schema.StatusOK || len(r.Items) != 2 {
		t.Fatalf("row = %+v", r)
	}
	for _, it := range r.Items {
		if !it.Replay || it.Seq != 0 {
			t.Errorf("a reset row must be marked replay with seq 0: %+v", it)
		}
	}
	if r.Sources[0].Query != "mine" || r.Sources[0].Status != "succeeded" {
		t.Errorf("a successful re-poll is succeeded: %+v", r.Sources[0])
	}
	if !strings.Contains(strings.Join(s.log(), "\n"), `"next_seq":9,"query":"mine"},"op":"changes_ack"`) {
		t.Errorf("the reset poll's next_seq must be acknowledged: %v", s.log())
	}
	if got := col.st.HighestSeq["pr:mine"]; got != 9 {
		t.Errorf("highest seq after a reset = %d, want the acknowledged position 9", got)
	}
	// Replayed rows are never de-duplicated against the map, and a later plain
	// poll repeating seq 0 is below the new position.
	e.clk.t = e.clk.t.Add(time.Minute)
	e.tickAt(t, col, slot0.Add(time.Minute))
	if r2 := e.ticks(t)[1]; len(r2.Items) != 0 {
		t.Errorf("rows at or below the position are dropped: %+v", r2.Items)
	}
}

func TestAnyOtherErrorAnswerFailsThatQuerysSlotAndTheTick(t *testing.T) {
	t.Run("every query errors: failed, exit 3, no ack", func(t *testing.T) {
		e := newEnv(t)
		s := e.useStandin(t)
		e.cfg.Queries = []string{"mine"}
		s.putQuiet("mine")
		bad := errAnswer("unavailable", "gh failed for acme/api#3 as someone at /Users/someone/x")
		s.put("pr-changes-mine.json", bad)
		s.put("ci-changes-mine.json", bad)
		col := e.collector(t)
		e.tickAt(t, col, slot0)
		r := e.ticks(t)[0]
		if r.Status != schema.StatusFailed || r.ExitCode == nil || *r.ExitCode != 3 || col.st.FailedStreak != 1 {
			t.Fatalf("row = %+v streak %d", r, col.st.FailedStreak)
		}
		for _, src := range r.Sources {
			if src.Status != "failed" || !strings.Contains(src.Reason, "unavailable") || strings.Contains(src.Reason, "acme/api") || strings.Contains(src.Reason, "someone") {
				t.Errorf("source = %+v", src)
			}
		}
		if strings.Contains(r.Error, "acme/api") || r.Error == "" {
			t.Errorf("row error = %q", r.Error)
		}
		if strings.Contains(strings.Join(s.log(), "\n"), "changes_ack") {
			t.Errorf("a failed query is not acknowledged: %v", s.log())
		}
	})
	t.Run("one instance errors: partial, exit 2, the other still acked, the streak counts", func(t *testing.T) {
		e := newEnv(t)
		s := e.useStandin(t)
		e.cfg.Queries = []string{"mine"}
		s.putQuiet("mine")
		s.put("ci-changes-mine.json", errAnswer("not_found", "no such thing"))
		s.put("pr-changes-mine.json", okAnswer(2, prChange("added", "owner/repo#1", 2, []string{"opened"})))
		col := e.collector(t)
		e.tickAt(t, col, slot0)
		r := e.ticks(t)[0]
		if r.Status != schema.StatusPartial || *r.ExitCode != 2 || len(r.Items) != 1 || col.st.FailedStreak != 1 {
			t.Fatalf("row = %+v streak %d", r, col.st.FailedStreak)
		}
		var acks []string
		for _, line := range s.log() {
			if strings.Contains(line, "changes_ack") {
				acks = append(acks, line)
			}
		}
		if len(acks) != 1 || !strings.HasPrefix(acks[0], "pr ") {
			t.Errorf("acks = %v, want only the pr one", acks)
		}
	})
}

func TestEveryCallIsTheDaemonClientAndNothingElse(t *testing.T) {
	e := newEnv(t)
	e.cfg.MaxTicks = 2
	c := e.collector(t)
	if err := c.Run(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if len(e.ex.argv) == 0 {
		t.Fatal("no client call")
	}
	for _, a := range e.ex.argv {
		if len(a) != 4 || a[0] != e.cfg.ClientBin || a[1] != "--config" || a[2] != e.cfg.ClientConfig || (a[3] != "pr" && a[3] != "ci") {
			t.Errorf("argv = %v, want <client> --config <cfg> pr|ci", a)
		}
		if strings.Contains(strings.Join(a, " "), "pg-desk") {
			t.Errorf("the collector must not run pg-desk: %v", a)
		}
	}
	// The daemon refuses one consumer on both feeds of a query, so the ci
	// instance is polled and acknowledged as its own consumer.
	for _, call := range e.ex.calls {
		want := "shadow-compare"
		if call.instance == "ci" {
			want = "shadow-compare-ci"
		}
		if call.consumer != want {
			t.Errorf("%s %s used consumer %q, want %q", call.instance, call.op, call.consumer, want)
		}
	}
	if got, want := e.ex.polls(), 2*2*2; got != want {
		t.Errorf("%d polls, want %d (2 ticks x 2 queries x 2 instances)", got, want)
	}
	if got, want := len(e.ex.acks()), 2*2*2; got != want {
		t.Errorf("%d acks, want %d", got, want)
	}
}

func TestTheModuleHasNoCompileTimeDependencyOnTheConnector(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"pg-connector", "replace", "nix-agent-support"} {
		if strings.Contains(string(b), bad) {
			t.Errorf("go.mod mentions %q: the harness execs the daemon, it never imports it", bad)
		}
	}
}

// ---- cost accounting -------------------------------------------------------

func TestSkippedRowsAndExecutedRowsSumDisjointCostWindows(t *testing.T) {
	e := newEnv(t)
	// Tick 1 spans 09:59:30..09:59:40, tick 2 09:59:40..09:59:50, tick 3
	// 09:59:50..10:00:00 on the fake clock; each row owns (previous end, own end].
	t0 := e.clk.t
	e.originRow(t, t0.Add(-time.Millisecond), 1)                 // before the run: nobody's
	e.originRow(t, t0, 2)                                        // the first instant: row 1
	e.originRow(t, t0.Add(10*time.Second), 4)                    // exactly row 1's end: row 1
	e.originRow(t, t0.Add(10*time.Second+time.Millisecond), 8)   // just after: row 2
	e.originRow(t, t0.Add(20*time.Second), 16)                   // exactly row 2's end: row 2
	e.originRow(t, t0.Add(20*time.Second+time.Nanosecond), 32)   // a nanosecond after: row 3
	e.originRow(t, t0.Add(20*time.Second+time.Nanosecond*2), 64) // row 3
	e.originRow(t, t0.Add(30*time.Second+time.Millisecond), 128) // past row 3's end: row 4
	col := e.collector(t)
	for i := 0; i < 3; i++ {
		e.tickAt(t, col, slot0.Add(time.Duration(i)*time.Minute))
	}
	var got []int
	for _, r := range e.ticks(t) {
		got = append(got, r.GraphQLCostSum)
	}
	if fmt.Sprint(got) != "[6 24 96]" {
		t.Errorf("cost sums = %v, want [6 24 96] (2+4, 8+16, 32+64): none twice, none dropped between rows", got)
	}
	// The window end is persisted for the next row (and the next run).
	b, err := os.ReadFile(e.l.StateFile())
	if err != nil {
		t.Fatal(err)
	}
	var st State
	if err := json.Unmarshal(b, &st); err == nil && st.CostWindowEnd != "" {
		t.Logf("state on disk: %s", st.CostWindowEnd)
	}
	if want := e.clk.t.UTC().Format(time.RFC3339Nano); col.st.CostWindowEnd != want {
		t.Errorf("cost window end = %q, want %q", col.st.CostWindowEnd, want)
	}
	// Row 4 picks up exactly what row 3 left.
	e.tickAt(t, col, slot0.Add(3*time.Minute))
	if r := e.ticks(t)[3]; r.GraphQLCostSum != 128 {
		t.Errorf("row 4 cost = %d, want 128", r.GraphQLCostSum)
	}
}

func TestTheFirstRowOfARunStartsAtTheRunStart(t *testing.T) {
	e := newEnv(t)
	e.cfg.MaxTicks = 1
	e.originRow(t, e.clk.t.Add(-time.Second), 100) // before the run started
	e.originRow(t, e.clk.t.Add(15*time.Second), 7) // inside the first window (the run starts at 09:59:30)
	c := e.collector(t)
	if err := c.Run(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if r := e.ticks(t)[0]; r.GraphQLCostSum != 7 {
		t.Errorf("first row cost = %d, want 7", r.GraphQLCostSum)
	}
	b, err := os.ReadFile(e.l.StateFile())
	if err != nil {
		t.Fatal(err)
	}
	var st State
	if err := json.Unmarshal(b, &st); err != nil || st.CostWindowEnd == "" {
		t.Errorf("the window end must be persisted: %s %v", b, err)
	}
}

func TestASkippedSlotCarriesItsCostAndFeedsTheKillCriterion(t *testing.T) {
	e := newEnv(t)
	e.cfg.KillPointsPerHour = 100
	e.cfg.Queries = []string{"mine"}
	c := e.collector(t)
	e.originRow(t, e.clk.t.Add(5*time.Second), 60) // tick 1: 09:59:30..09:59:40
	e.tickAt(t, c, slot0)
	if r := e.ticks(t)[0]; r.GraphQLCostSum != 60 || r.SkippedBudget {
		t.Fatalf("first row = %+v", r)
	}
	// The live connector now reports a low budget, so the next slot is skipped,
	// but the daemon keeps spending during it.
	reset := slot0.Add(10 * time.Minute)
	low := fmt.Sprintf(`{"time":%q,"op":"list","graphql_remaining":1500,"graphql_reset_at":%q}`+"\n", e.clk.t.Format(time.RFC3339Nano), reset.Format(time.RFC3339))
	if err := os.WriteFile(e.m.Live.ConnectorEvents, []byte(low), 0o600); err != nil {
		t.Fatal(err)
	}
	e.originRow(t, e.clk.t.Add(20*time.Second), 60)
	e.clk.t = slot0.Add(time.Minute)
	callsBefore := len(e.ex.calls)
	ran, err := c.Tick(context.Background(), e.clk.t)
	if ran || !errors.Is(err, ErrKill) || !strings.Contains(err.Error(), "spend") {
		t.Fatalf("120 points in the hour, 60 of them during a skipped slot, must kill: ran=%v err=%v", ran, err)
	}
	if len(e.ex.calls) != callsBefore {
		t.Error("a skipped slot makes no client call")
	}
	r := e.ticks(t)[1]
	if !r.SkippedBudget || r.GraphQLCostSum != 60 {
		t.Errorf("skipped row = %+v, want cost 60", r)
	}
	if n := len(c.st.Spend); n != 2 {
		t.Errorf("spend points = %d, want 2 (the skipped slot appends one)", n)
	}
}

// ---- the tick loop ---------------------------------------------------------

func TestFeedFailuresAreFailedTicksWithAScrubbedReason(t *testing.T) {
	cases := []struct {
		name string
		res  runner.Result
		err  error
	}{
		{"an error answer", runner.Result{Exit: 7, Stdout: errAnswer("unavailable", "list acme/api#1 failed for someone")}, nil},
		{"exit 1 with no answer", runner.Result{Exit: 1, Stderr: "client: store acme/api is old-schema\nmore"}, nil},
		{"a deadline", runner.Result{}, fmt.Errorf("deadline: context deadline exceeded")},
		{"garbage output", runner.Result{Stdout: "not json"}, nil},
		{"a non-zero exit carrying a result", runner.Result{Exit: 4, Stdout: okAnswer(1)}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			e.ex.client = func(clientCall) (runner.Result, error) { return c.res, c.err }
			col := e.collector(t)
			e.tickAt(t, col, slot0)
			r := e.ticks(t)[0]
			if r.Status != schema.StatusFailed || r.ExitCode == nil || *r.ExitCode != 3 || len(r.Items) != 0 || len(r.Sources) != 4 {
				t.Errorf("row = %+v", r)
			}
			if strings.Contains(r.Error, "acme/api") || strings.Contains(r.Error, "someone") {
				t.Errorf("error text leaks: %q", r.Error)
			}
			for _, src := range r.Sources {
				if src.Status != "failed" || src.Reason == "" || strings.Contains(src.Reason, "acme/api") || strings.Contains(src.Reason, "someone") {
					t.Errorf("source = %+v", src)
				}
			}
			if len(e.ex.acks()) != 0 {
				t.Errorf("a failed poll is not acknowledged: %v", e.ex.acks())
			}
		})
	}
}

func TestWarmupWindowEndsAfterTwoConsecutiveZeroHydrationTicks(t *testing.T) {
	e := newEnv(t)
	c := e.collector(t)
	for i := 0; i < 4; i++ {
		e.tickAt(t, c, slot0.Add(time.Duration(i)*time.Minute))
	}
	var got []bool
	for _, r := range e.ticks(t) {
		got = append(got, r.Warmup)
	}
	// The feed hydrates nothing (Hydrations is always 0), so the window ends
	// after the second tick.
	if want := []bool{true, true, false, false}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("warm-up flags = %v, want %v", got, want)
	}
}

func TestPhaseBHasNoWarmupExclusion(t *testing.T) {
	e := newEnv(t)
	e.cfg.WarmupExclusion = false
	c := e.collector(t)
	e.tickAt(t, c, slot0)
	if e.ticks(t)[0].Warmup {
		t.Error("phase B ticks are the measurement and must not be excluded")
	}
}

func TestSlotsAreAlignedAndTicksNeverOverlap(t *testing.T) {
	e := newEnv(t)
	e.cfg.MaxTicks = 4
	c := e.collector(t)
	if err := c.Run(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	ts := e.ticks(t)
	if len(ts) != 4 {
		t.Fatalf("%d ticks", len(ts))
	}
	var prevEnd time.Time
	for _, r := range ts {
		slot, _ := schema.Time(r.Slot)
		if slot.Second() != 0 || slot.Nanosecond() != 0 {
			t.Errorf("slot %s is not aligned to the minute", r.Slot)
		}
		start, _ := schema.Time(r.TickStart)
		end, _ := schema.Time(r.TickEnd)
		if start.Before(slot) {
			t.Errorf("tick started before its slot")
		}
		if !prevEnd.IsZero() && start.Before(prevEnd) {
			t.Errorf("ticks overlap: start %s before the previous end %s", start, prevEnd)
		}
		prevEnd = end
	}
	if got := e.ex.polls(); got != 16 {
		t.Errorf("%d feed polls, want 16 (4 ticks x 2 queries x 2 instances)", got)
	}
}

func TestOverrunSkipsMissedSlotsAndLogsAGap(t *testing.T) {
	e := newEnv(t)
	e.ex.advance = 75 * time.Second // longer than a slot
	e.cfg.MaxTicks = 3
	c := e.collector(t)
	if err := c.Run(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	var gaps []schema.Row
	for _, r := range e.rows(t) {
		if r.Kind == schema.KindGap {
			gaps = append(gaps, r)
		}
	}
	if len(gaps) == 0 || gaps[0].Reason != schema.GapOverrun {
		t.Fatalf("expected an overrun gap, got %+v", gaps)
	}
	ts := e.ticks(t)
	s0, _ := schema.Time(ts[0].Slot)
	s1, _ := schema.Time(ts[1].Slot)
	if s1.Sub(s0) != 2*time.Minute {
		t.Errorf("the missed slot must be skipped, not caught up: %s -> %s", s0, s1)
	}
}

func TestLateWakeRecordsASleepGapAndRealigns(t *testing.T) {
	e := newEnv(t)
	woke := false
	e.cfg.Sleep = func(ctx context.Context, until time.Time) error {
		if !woke && len(e.ex.calls) > 0 {
			woke = true
			e.clk.t = until.Add(3 * time.Hour) // the lid was closed
			return nil
		}
		return e.clk.sleep(ctx, until)
	}
	e.cfg.MaxTicks = 2
	c := e.collector(t)
	if err := c.Run(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	var sleepGaps int
	for _, r := range e.rows(t) {
		if r.Kind == schema.KindGap && r.Reason == schema.GapSleep {
			sleepGaps++
		}
	}
	if sleepGaps != 1 {
		t.Errorf("%d sleep gaps, want 1 (no duplicate for the realigned slot)", sleepGaps)
	}
}

func TestBudgetSkipWritesARowAndResumesAfterReset(t *testing.T) {
	e := newEnv(t)
	slot := time.Date(2026, 1, 5, 10, 0, 0, 0, time.UTC)
	e.clk.t = slot
	reset := slot.Add(2 * time.Minute)
	low := fmt.Sprintf(`{"time":%q,"op":"list","graphql_remaining":1500,"graphql_reset_at":%q}`+"\n", slot.Add(-time.Minute).Format(time.RFC3339Nano), reset.Format(time.RFC3339))
	if err := os.WriteFile(e.m.Live.ConnectorEvents, []byte(low), 0o600); err != nil {
		t.Fatal(err)
	}
	c := e.collector(t)
	ran, err := c.Tick(context.Background(), slot)
	if err != nil || ran {
		t.Fatalf("below the floor the tick must be skipped: ran=%v err=%v", ran, err)
	}
	if len(e.ex.calls) != 0 {
		t.Fatal("the client must not be called when the budget is below the floor")
	}
	r := e.ticks(t)[0]
	if !r.SkippedBudget || r.Status != schema.StatusSkippedBudget || r.Budget == nil || r.Budget.Remaining != 1500 || r.Budget.Floor != 2000 {
		t.Errorf("row = %+v", r)
	}
	// The window resets and nothing new is logged: the guard must not deadlock.
	e.clk.t = reset.Add(10 * time.Second)
	ran, err = c.Tick(context.Background(), NextBoundary(e.clk.t, time.Minute))
	if err != nil || !ran {
		t.Fatalf("after reset+5s the guard must resume: ran=%v err=%v", ran, err)
	}
}

func TestKillCriteria(t *testing.T) {
	t.Run("spend above the ceiling", func(t *testing.T) {
		e := newEnv(t)
		e.cfg.KillPointsPerHour = 100
		e.ex.client = func(c clientCall) (runner.Result, error) {
			if c.op == "changes" && c.instance == "pr" && c.query == "mine" {
				e.originRow(t, e.clk.t.Add(-time.Second), 60)
			}
			return defaultAnswer(c), nil
		}
		c := e.collector(t)
		slot := time.Date(2026, 1, 5, 10, 0, 0, 0, time.UTC)
		if _, err := c.Tick(context.Background(), slot); err != nil {
			t.Fatalf("60 points is under the ceiling: %v", err)
		}
		if _, err := c.Tick(context.Background(), slot.Add(time.Minute)); err == nil || !strings.Contains(err.Error(), "spend") {
			t.Fatalf("120 points in the hour must kill, got %v", err)
		}
	})
	t.Run("consecutive failed ticks", func(t *testing.T) {
		e := newEnv(t)
		e.cfg.MaxFailedTicks = 3
		e.ex.client = func(clientCall) (runner.Result, error) { return runner.Result{Exit: 1, Stderr: "boom"}, nil }
		c := e.collector(t)
		slot := time.Date(2026, 1, 5, 10, 0, 0, 0, time.UTC)
		var err error
		n := 0
		for ; err == nil && n < 10; n++ {
			_, err = c.Tick(context.Background(), slot.Add(time.Duration(n)*time.Minute))
		}
		if err == nil || n != 4 {
			t.Fatalf("must abort on the 4th consecutive failure (more than 3), got n=%d err=%v", n, err)
		}
	})
	t.Run("a sandbox denial", func(t *testing.T) {
		e := newEnv(t)
		e.ex.client = func(c clientCall) (runner.Result, error) {
			r := defaultAnswer(c)
			r.Denied = true
			return r, nil
		}
		c := e.collector(t)
		if _, err := c.Tick(context.Background(), time.Date(2026, 1, 5, 10, 0, 0, 0, time.UTC)); err == nil || !strings.Contains(err.Error(), "denial") {
			t.Fatalf("a denial must kill the run, got %v", err)
		}
	})
	t.Run("a rejected write verb", func(t *testing.T) {
		e := newEnv(t)
		e.ex.client = func(c clientCall) (runner.Result, error) {
			_ = os.WriteFile(e.l.ShimLog("gh"), []byte(`{"tool":"gh","argv":["pr","merge","1"],"verdict":"reject-write"}`+"\n"), 0o600)
			return defaultAnswer(c), nil
		}
		c := e.collector(t)
		if _, err := c.Tick(context.Background(), time.Date(2026, 1, 5, 10, 0, 0, 0, time.UTC)); err == nil || !strings.Contains(err.Error(), "write verb") {
			t.Fatalf("a rejected write must kill the run, got %v", err)
		}
	})
	t.Run("a safety refusal from the runner", func(t *testing.T) {
		e := newEnv(t)
		e.ex.client = func(clientCall) (runner.Result, error) {
			return runner.Result{}, fmt.Errorf("%w: XDG_STATE_HOME is live", runner.ErrSafety)
		}
		c := e.collector(t)
		if _, err := c.Tick(context.Background(), time.Date(2026, 1, 5, 10, 0, 0, 0, time.UTC)); !errors.Is(err, ErrKill) {
			t.Fatalf("a safety refusal must stop the run with ErrKill, got %v", err)
		}
	})
}

func TestCrashAndResumeRecoversTheUnfinishedTickFromTheScratchChangeLog(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	// A previous collector wrote tick_start, the scratch store gained two rows,
	// then the collector died before the tick row (Recover still reads the store).
	if err := schema.Append(e.l.TicksFile(), schema.Row{Kind: schema.KindTickStart, Phase: "A", Slot: "2026-01-05T10:00:00.000Z", TickStart: "2026-01-05T10:00:01.000Z", CursorFrom: 0}); err != nil {
		t.Fatal(err)
	}
	if err := e.db.Exec(ctx, `INSERT INTO change_log (repo, entity_type, entity_id, version, kinds, origin, at) VALUES
 ('acme/api','pr','acme/api#1',1,'["head_changed"]','pg-connector','2026-01-05T10:00:05Z'),
 ('acme/api','pr','acme/api#2',1,'["reconcile"]','local-reconcile','2026-01-05T10:00:06Z');
UPDATE consumer SET cursor=2 WHERE name='shadow-compare';`); err != nil {
		t.Fatal(err)
	}
	e.clk.t = time.Date(2026, 1, 5, 10, 5, 0, 0, time.UTC)
	c := e.collector(t)
	if err := c.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	ts := e.ticks(t)
	if len(ts) != 1 || ts[0].Status != schema.StatusRecovered || !ts[0].Recovered || len(ts[0].Items) != 2 || ts[0].Items[0].Origin != "pg-connector" || ts[0].CursorTo != 2 {
		t.Fatalf("recovered row = %+v", ts)
	}
	reasons := map[string]bool{}
	for _, r := range e.rows(t) {
		if r.Kind == schema.KindGap {
			reasons[r.Reason] = true
		}
	}
	if !reasons[schema.GapUnfinish] || !reasons[schema.GapRestart] {
		t.Errorf("gap reasons = %v", reasons)
	}
}

func TestStateSurvivesARestart(t *testing.T) {
	e := newEnv(t)
	e.ex.client = func(c clientCall) (runner.Result, error) {
		if c.op == "changes" && c.instance == "pr" && c.query == "mine" {
			return runner.Result{Stdout: okAnswer(3, prChange("added", "owner/repo#1", 3, []string{"opened"}))}, nil
		}
		return defaultAnswer(c), nil
	}
	e.cfg.MaxTicks = 1
	c := e.collector(t)
	if err := c.Run(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(e.l.StateFile())
	if err != nil {
		t.Fatal(err)
	}
	var st State
	if err := json.Unmarshal(b, &st); err != nil || st.Executed != 1 || st.LastSlot == "" || st.HighestSeq["pr:mine"] != 3 || st.CostWindowEnd == "" {
		t.Fatalf("state = %+v %v", st, err)
	}
	c2 := e.collector(t)
	if c2.st.Executed != 1 || c2.st.LastSlot != st.LastSlot || c2.st.HighestSeq["pr:mine"] != 3 || c2.st.CostWindowEnd != st.CostWindowEnd {
		t.Errorf("a new collector must load the saved state, got %+v", c2.st)
	}
}

func TestLockRefusesASecondCollector(t *testing.T) {
	e := newEnv(t)
	unlock, err := Lock(e.l)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Lock(e.l); err != ErrAlreadyRunning {
		t.Fatalf("second lock: %v", err)
	}
	unlock()
	if u2, err := Lock(e.l); err != nil {
		t.Fatalf("after release: %v", err)
	} else {
		u2()
	}
}

func TestScrubRemovesIdentifiers(t *testing.T) {
	in := "hydrate acme/api#12: gh failed for someone at /Users/someone/work/x.go with @octo-handle"
	out := Scrub(in, []string{"someone"})
	for _, bad := range []string{"acme/api", "#12", "someone", "/Users", "@octo"} {
		if strings.Contains(out, bad) {
			t.Errorf("Scrub left %q in %q", bad, out)
		}
	}
	if len(Scrub(strings.Repeat("x", 1000), nil)) > 240 {
		t.Error("Scrub must truncate")
	}
}

func TestNextBoundary(t *testing.T) {
	at := time.Date(2026, 1, 5, 10, 0, 59, 0, time.UTC)
	if got := NextBoundary(at, time.Minute); !got.Equal(time.Date(2026, 1, 5, 10, 1, 0, 0, time.UTC)) {
		t.Errorf("NextBoundary = %s", got)
	}
	exact := time.Date(2026, 1, 5, 10, 1, 0, 0, time.UTC)
	if got := NextBoundary(exact, time.Minute); !got.Equal(exact.Add(time.Minute)) {
		t.Errorf("a boundary instant must advance to the next slot: %s", got)
	}
}

func TestCostInWindowSumsOnlyOriginRows(t *testing.T) {
	from := time.Date(2026, 1, 5, 10, 0, 0, 0, time.UTC)
	to := from.Add(time.Minute)
	at := func(d time.Duration) string { return from.Add(d).Format(time.RFC3339Nano) }
	lines := []string{
		fmt.Sprintf(`{"time":%q,"kind":"origin","purpose":"list","bucket":"graphql","graphql_cost":5,"graphql_remaining":3000}`, at(time.Second)),
		fmt.Sprintf(`{"time":%q,"kind":"request","op":"list","graphql_cost":100}`, at(2*time.Second)),
		fmt.Sprintf(`{"time":%q,"op":"list","graphql_cost":1000}`, at(3*time.Second)), // kind-less (old format)
		fmt.Sprintf(`{"time":%q,"kind":"origin","purpose":"show","bucket":"graphql","graphql_cost":7}`, at(30*time.Second)),
		fmt.Sprintf(`{"time":%q,"kind":"origin","purpose":"files","bucket":"rest_core","graphql_cost":0,"rest_requests":3,"rest_remaining":4000}`, at(31*time.Second)),
		fmt.Sprintf(`{"time":%q,"kind":"origin","purpose":"list","bucket":"graphql","graphql_cost":50}`, at(-time.Second)),  // before the window
		fmt.Sprintf(`{"time":%q,"kind":"origin","purpose":"list","bucket":"graphql","graphql_cost":60}`, at(2*time.Minute)), // after the window
		`not json`,
	}
	path := filepath.Join(t.TempDir(), "events.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := CostInWindow(path, from, to); got != 12 {
		t.Errorf("CostInWindow = %d, want 12 (5 + 7, origin rows inside the window only)", got)
	}
	if got := CostInWindow(filepath.Join(t.TempDir(), "none"), from, to); got != 0 {
		t.Errorf("missing log = %d, want 0", got)
	}
}

// fakeDaemon stands in for the supervised daemon child.
type fakeDaemon struct {
	samples       []schema.DaemonSample
	starts, stops int
	samplesTaken  int
	startErr      error
}

func (d *fakeDaemon) Start(context.Context) error { d.starts++; return d.startErr }
func (d *fakeDaemon) Stop() error                 { d.stops++; return nil }
func (d *fakeDaemon) Sample(context.Context) schema.DaemonSample {
	i := d.samplesTaken
	d.samplesTaken++
	if i >= len(d.samples) {
		i = len(d.samples) - 1
	}
	return d.samples[i]
}

func TestTickRowsCarryTheDaemonSample(t *testing.T) {
	for _, c := range []struct {
		name   string
		sample schema.DaemonSample
	}{
		{"healthy", schema.DaemonSample{State: schema.DaemonHealthy, StatusExit: 0, Alive: true, Restarts: 0}},
		{"degraded", schema.DaemonSample{State: schema.DaemonDegraded, StatusExit: 2, Alive: true, Restarts: 1}},
		{"down", schema.DaemonSample{State: schema.DaemonDown, StatusExit: 3, Alive: true, Restarts: 2}},
		{"dead process", schema.DaemonSample{State: schema.DaemonDown, StatusExit: -1, Alive: false, Restarts: 3}},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			d := &fakeDaemon{samples: []schema.DaemonSample{c.sample}}
			e.cfg.Daemon = d
			c2 := e.collector(t)
			e.tickAt(t, c2, slot0)
			r := e.ticks(t)[0]
			if r.Daemon == nil || *r.Daemon != c.sample {
				t.Fatalf("row daemon = %+v, want %+v", r.Daemon, c.sample)
			}
			// The sample survives the JSON round trip under the pinned keys.
			b, _ := json.Marshal(r)
			for _, key := range []string{`"daemon":{"state":"` + c.sample.State + `"`, `"status_exit":`, `"alive":`, `"restarts":`} {
				if !strings.Contains(string(b), key) {
					t.Errorf("row JSON lacks %s: %s", key, b)
				}
			}
		})
	}
}

func TestASkippedBudgetRowCarriesTheDaemonSampleToo(t *testing.T) {
	e := newEnv(t)
	slot := time.Date(2026, 1, 5, 10, 0, 0, 0, time.UTC)
	e.clk.t = slot
	low := fmt.Sprintf(`{"time":%q,"op":"list","graphql_remaining":1500,"graphql_reset_at":%q}`+"\n", slot.Add(-time.Minute).Format(time.RFC3339Nano), slot.Add(2*time.Minute).Format(time.RFC3339))
	if err := os.WriteFile(e.m.Live.ConnectorEvents, []byte(low), 0o600); err != nil {
		t.Fatal(err)
	}
	want := schema.DaemonSample{State: schema.DaemonDegraded, StatusExit: 2, Alive: true, Restarts: 1}
	e.cfg.Daemon = &fakeDaemon{samples: []schema.DaemonSample{want}}
	c := e.collector(t)
	ran, err := c.Tick(context.Background(), slot)
	if err != nil || ran {
		t.Fatalf("tick: ran=%v err=%v", ran, err)
	}
	r := e.ticks(t)[0]
	if !r.SkippedBudget || r.Daemon == nil || *r.Daemon != want {
		t.Errorf("skipped row = %+v", r)
	}
}

func TestASamplerThatNamesNoStateIsRecordedDownNotHealthy(t *testing.T) {
	e := newEnv(t)
	e.cfg.Daemon = &fakeDaemon{samples: []schema.DaemonSample{{}}}
	c := e.collector(t)
	e.tickAt(t, c, slot0)
	if r := e.ticks(t)[0]; r.Daemon == nil || r.Daemon.State != schema.DaemonDown {
		t.Errorf("daemon = %+v, want state down", r.Daemon)
	}
}

func TestWithoutADaemonTheRowsCarryNoSample(t *testing.T) {
	e := newEnv(t)
	c := e.collector(t)
	e.tickAt(t, c, slot0)
	r := e.ticks(t)[0]
	b, _ := json.Marshal(r)
	if r.Daemon != nil || strings.Contains(string(b), `"daemon"`) {
		t.Errorf("a run with no daemon must not write a sample: %s", b)
	}
}

func TestRunSamplesOncePerSlotAndNeverStartsOrStopsTheChild(t *testing.T) {
	e := newEnv(t)
	e.cfg.MaxTicks = 3
	d := &fakeDaemon{samples: []schema.DaemonSample{
		{State: schema.DaemonHealthy, Alive: true},
		{State: schema.DaemonDown, StatusExit: -1, Restarts: 1},
		{State: schema.DaemonDegraded, StatusExit: 2, Alive: true, Restarts: 1},
	}}
	e.cfg.Daemon = d
	c := e.collector(t)
	if err := c.Run(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	ts := e.ticks(t)
	if len(ts) != 3 || d.samplesTaken != 3 {
		t.Fatalf("%d tick rows and %d samples, want 3 and 3", len(ts), d.samplesTaken)
	}
	for i, r := range ts {
		if r.Daemon == nil || *r.Daemon != d.samples[i] {
			t.Errorf("tick %d daemon = %+v, want %+v", i, r.Daemon, d.samples[i])
		}
	}
	if d.starts != 0 || d.stops != 0 {
		t.Errorf("Run must not start (%d) or stop (%d) the child: cmdRun owns that", d.starts, d.stops)
	}
}
