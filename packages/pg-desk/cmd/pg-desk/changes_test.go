package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/changes"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

var changesTestNow = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

// changesFixture is a new-schema store, a config watching the named queries
// of one entity type, and a fake pg-connector on PATH driven by files in dir:
// <type>-changes-<query>.json (+ .exit) answers `<type> changes --query q`,
// show-<id>.json answers `<type> show <id>`, and calls.log records every call.
type changesFixture struct {
	t    *testing.T
	typ  string
	cfg  *config.Config
	seed *store.Store
	dir  string
}

func newChangesFixture(t *testing.T, typ string, queries ...string) *changesFixture {
	t.Helper()
	open, seed := seedLinkStore(t)
	cfg := openTestConfig("o/r")
	switch typ {
	case "pr":
		cfg.Watch.PR.Queries = queries
	case "issue":
		cfg.Watch.Issue.Queries = queries
	case "thread":
		cfg.Watch.Thread.Queries = queries
	}
	withOpenSeams(t, cfg, open)
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())

	origNow := changesNow
	t.Cleanup(func() { changesNow = origNow })
	changesNow = func() time.Time { return changesTestNow }

	dir := t.TempDir()
	installFakePGConnector(t, fmt.Sprintf(`D=%q
t="$1"; v="$2"
if [ "$v" = changes ]; then
  q=""; prev=""
  for a in "$@"; do if [ "$prev" = "--query" ]; then q="$a"; fi; prev="$a"; done
  echo "$t changes $q" >> "$D/calls.log"
  code=0
  if [ -f "$D/$t-changes-$q.exit" ]; then code=$(cat "$D/$t-changes-$q.exit"); fi
  if [ -f "$D/$t-changes-$q.json" ]; then cat "$D/$t-changes-$q.json"; fi
  exit "$code"
fi
if [ "$v" = show ]; then
  id=$(echo "$3" | tr '/#' '__')
  echo "$t show $3" >> "$D/calls.log"
  if [ -f "$D/show-$id.json" ]; then cat "$D/show-$id.json"; exit 0; fi
  echo '{"error":{"code":"boom","message":"no fixture"}}'
  exit 1
fi
exit 99`, dir))
	return &changesFixture{t: t, typ: typ, cfg: cfg, seed: seed, dir: dir}
}

// listing sets what `<type> changes --query q` answers: exit code and one
// change per (kind, id), with the given backend rows.
func (f *changesFixture) listing(query string, exit int, sources string, changes ...[2]string) {
	f.t.Helper()
	var entries []string
	for _, c := range changes {
		entries = append(entries, fmt.Sprintf(`{"change":%q,"source":"b","entity":{"id":%q,"title":"title of %s"}}`, c[0], c[1], c[1]))
	}
	body := fmt.Sprintf(`{"sources":[%s],"changes":[%s]}`, sources, strings.Join(entries, ","))
	f.write(fmt.Sprintf("%s-changes-%s.json", f.typ, query), body)
	f.write(fmt.Sprintf("%s-changes-%s.exit", f.typ, query), fmt.Sprint(exit))
}

const okSource = `{"backend":"b","status":"succeeded"}`

func (f *changesFixture) write(name, body string) {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(f.dir, name), []byte(body), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

// showIssue makes `issue show id` answer a minimal issue with updated_at.
func (f *changesFixture) showIssue(id, updatedAt string) {
	f.t.Helper()
	f.write("show-"+strings.NewReplacer("/", "_", "#", "_").Replace(id)+".json", fmt.Sprintf(
		`{"protocolVersion":1,"schemaVersion":4,"result":{"id":%q,"title":"title of %s","owner":"me","assignee":"x","issue_type":"task","state":"open","updated_at":%q,"as_of":"2026-09-30T00:00:00Z"}}`,
		id, id, updatedAt,
	))
}

func (f *changesFixture) calls() []string {
	b, err := os.ReadFile(filepath.Join(f.dir, "calls.log"))
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

func runChangesCmd(t *testing.T, entityType string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	c := newChangesCmd(entityType)
	var out, errb bytes.Buffer
	c.SetOut(&out)
	c.SetErr(&errb)
	c.SetArgs(args)
	c.SilenceUsage = true
	c.SilenceErrors = true
	err = c.ExecuteContext(context.Background())
	return out.String(), errb.String(), err
}

// envOf runs changes with --json and decodes the envelope from stdout.
func (f *changesFixture) envOf(args ...string) (changes.Envelope, error) {
	f.t.Helper()
	out, _, err := runChangesCmd(f.t, f.typ, append(args, "--json")...)
	var env changes.Envelope
	if out != "" {
		if jerr := json.Unmarshal([]byte(out), &env); jerr != nil {
			f.t.Fatalf("stdout is not an envelope: %v\n%s", jerr, out)
		}
	}
	return env, err
}

func (f *changesFixture) consumer(name string) (store.Consumer, bool) {
	f.t.Helper()
	cs, err := f.seed.ListConsumers()
	if err != nil {
		f.t.Fatal(err)
	}
	for _, c := range cs {
		if c.Name == name && c.Type == f.typ {
			return c, true
		}
	}
	return store.Consumer{}, false
}

func (f *changesFixture) logLen() int {
	f.t.Helper()
	rows, err := f.seed.ListChangesAfter(f.typ, 0, 0)
	if err != nil {
		f.t.Fatal(err)
	}
	return len(rows)
}

func envKindsByID(env changes.Envelope) map[string][]string {
	out := map[string][]string{}
	for _, r := range env.Records {
		out[r.ID] = r.Kinds
	}
	return out
}

func TestChangesIsRegisteredUnderEveryTypeGroup(t *testing.T) {
	for _, typ := range []string{"pr", "issue", "thread"} {
		c, _, err := rootCmd.Find([]string{typ, "changes"})
		if err != nil || c.Name() != "changes" || c.Parent() != typeGroup(typ) {
			t.Errorf("%s changes not registered: %v, %v", typ, c, err)
		}
	}
}

func TestChangesPullThroughHydratesLogsAndAdvances(t *testing.T) {
	f := newChangesFixture(t, "issue", "open")
	f.listing("open", 0, okSource, [2]string{"added", "bd-1"}, [2]string{"changed", "bd-2"}, [2]string{"removed", "bd-9"})
	f.showIssue("bd-1", "2026-09-30T00:00:00Z")
	f.showIssue("bd-2", "2026-09-30T00:00:00Z")

	env, err := f.envOf("--consumer", "router")
	if err != nil {
		t.Fatalf("changes: %v", err)
	}
	if env.Contract != changes.Contract || env.Type != "issue" || env.Consumer != "router" {
		t.Errorf("envelope header = %+v", env)
	}
	if len(env.Sources) != 1 || env.Sources[0] != (changes.Source{Query: "open", Status: changes.StatusOK}) {
		t.Errorf("sources = %+v", env.Sources)
	}
	if got := envKindsByID(env); len(got) != 2 || got["bd-1"][0] != "reconcile" || got["bd-2"][0] != "reconcile" {
		t.Errorf("first observation must yield only reconcile for the two reported entities, got %v", got)
	}
	for _, r := range env.Records {
		if r.Origin != "pg-connector" || r.Title != "title of "+r.ID {
			t.Errorf("record = %+v", r)
		}
	}
	// pg-connector is asked once per watched query, as the pg-desk consumer,
	// and the removed entry is not hydrated.
	calls := f.calls()
	if calls[0] != "issue changes open" || len(calls) != 3 {
		t.Errorf("connector calls = %v", calls)
	}
	c, ok := f.consumer("router")
	if !ok || c.Cursor != env.Cursor.To || env.Cursor.From != 0 || env.Cursor.To == 0 {
		t.Errorf("cursor = %+v, envelope cursor %+v", c, env.Cursor)
	}

	// A second call with nothing new delivers nothing and keeps the cursor.
	f.listing("open", 0, okSource)
	again, err := f.envOf("--consumer", "router")
	if err != nil || len(again.Records) != 0 || again.Cursor.From != env.Cursor.To || again.Cursor.To != env.Cursor.To {
		t.Errorf("second call = %+v, %v", again, err)
	}
}

func TestChangesCachedDoesNotAdvanceCursor(t *testing.T) {
	f := newChangesFixture(t, "issue", "open")
	f.listing("open", 0, okSource, [2]string{"added", "bd-1"})
	f.showIssue("bd-1", "2026-09-30T00:00:00Z")
	if _, err := f.envOf("--consumer", "other"); err != nil { // fills the log
		t.Fatal(err)
	}
	if _, err := f.seed.ReadChanges("peeker", "issue", 0, changesTestNow.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	callsBefore := len(f.calls())
	before, _ := f.consumer("peeker")

	first, err := f.envOf("--consumer", "peeker", "--cached")
	if err != nil || len(first.Records) != 1 {
		t.Fatalf("cached = %+v, %v", first, err)
	}
	second, err := f.envOf("--consumer", "peeker", "--cached")
	if err != nil || len(second.Records) != 1 || second.Records[0].Seq != first.Records[0].Seq {
		t.Errorf("a second cached call must return the same records: %+v, %v", second, err)
	}
	if len(first.Sources) != 0 {
		t.Errorf("a cached call consults no query, sources = %+v", first.Sources)
	}
	after, _ := f.consumer("peeker")
	if after.Cursor != before.Cursor || after.SeenAt != before.SeenAt {
		t.Errorf("--cached moved the consumer: %+v -> %+v", before, after)
	}
	if len(f.calls()) != callsBefore {
		t.Errorf("--cached called pg-connector: %v", f.calls())
	}
	// A consumer that was never registered is not registered by a peek.
	if _, err := f.envOf("--consumer", "ghost", "--cached"); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.consumer("ghost"); ok {
		t.Error("--cached registered the consumer")
	}
}

func TestChangesCachedIsUnaffectedByMissingWatchConfig(t *testing.T) {
	f := newChangesFixture(t, "issue") // no watch.issue.queries
	if _, err := f.envOf("--consumer", "router", "--cached"); err != nil {
		t.Errorf("--cached with no watched queries: %v", err)
	}
}

func TestChangesRealCallWithNoWatchedQueriesIsAUsageError(t *testing.T) {
	f := newChangesFixture(t, "thread")
	_, _, err := runChangesCmd(t, "thread", "--consumer", "router")
	if err == nil || !strings.Contains(err.Error(), "watch.thread.queries") || exitCodeFor(err) != 1 {
		t.Fatalf("err = %v (exit %d), want exit 1 naming watch.thread.queries", err, exitCodeFor(err))
	}
	if len(f.calls()) != 0 {
		t.Errorf("pg-connector was called: %v", f.calls())
	}
}

func TestChangesRejectsUnknownQuery(t *testing.T) {
	f := newChangesFixture(t, "issue", "open", "mine")
	_, _, err := runChangesCmd(t, "issue", "--consumer", "router", "--query", "nope")
	if err == nil || !strings.Contains(err.Error(), `"nope"`) || !strings.Contains(err.Error(), "open, mine") || exitCodeFor(err) != 1 {
		t.Fatalf("err = %v (exit %d)", err, exitCodeFor(err))
	}
	if len(f.calls()) != 0 {
		t.Errorf("pg-connector was called: %v", f.calls())
	}
}

func TestChangesQueryRestrictsTheCall(t *testing.T) {
	f := newChangesFixture(t, "issue", "open", "mine")
	f.listing("open", 0, okSource)
	f.listing("mine", 0, okSource)
	env, err := f.envOf("--consumer", "router", "--query", "mine")
	if err != nil || len(env.Sources) != 1 || env.Sources[0].Query != "mine" {
		t.Fatalf("env = %+v, %v", env, err)
	}
	if calls := f.calls(); len(calls) != 1 || calls[0] != "issue changes mine" {
		t.Errorf("calls = %v", calls)
	}
}

func TestChangesFlagValidation(t *testing.T) {
	newChangesFixture(t, "issue", "open")
	for name, args := range map[string][]string{
		"negative limit":       {"--consumer", "r", "--limit", "-1"},
		"reset with cached":    {"--consumer", "r", "--reset", "--cached"},
		"empty consumer":       {"--consumer", " "},
		"missing consumer":     {},
		"positional arguments": {"--consumer", "r", "extra"},
	} {
		if _, _, err := runChangesCmd(t, "issue", args...); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestChangesResetReplaysActiveEntities(t *testing.T) {
	f := newChangesFixture(t, "issue", "open")
	f.listing("open", 0, okSource, [2]string{"added", "bd-1"}, [2]string{"added", "bd-2"}, [2]string{"added", "bd-3"})
	for _, id := range []string{"bd-1", "bd-2", "bd-3"} {
		f.showIssue(id, "2026-09-30T00:00:00Z")
	}
	first, err := f.envOf("--consumer", "router")
	if err != nil || len(first.Records) != 3 {
		t.Fatalf("seed call = %+v, %v", first, err)
	}
	// bd-3 leaves the watched set: inactive entities are not replayed.
	ent, _, _ := f.seed.GetEntity("o/r", "issue", "bd-3")
	if _, err := f.seed.WriteEntityStateWithLog(ent, ent.Version, "2026-09-30T01:00:00Z", false, []string{"removed"}, "sweep", "2026-09-30T01:00:00Z"); err != nil {
		t.Fatal(err)
	}
	f.listing("open", 0, okSource)
	if _, err := f.envOf("--consumer", "router"); err != nil { // consume the removed record
		t.Fatal(err)
	}

	env, err := f.envOf("--consumer", "router", "--reset")
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	got := envKindsByID(env)
	if len(got) != 2 || len(got["bd-1"]) != 1 || got["bd-1"][0] != "reconcile" || len(got["bd-2"]) != 1 || got["bd-2"][0] != "reconcile" {
		t.Fatalf("reset must replay the two active entities as reconcile only, got %v", got)
	}
	if _, ok := got["bd-3"]; ok {
		t.Error("an inactive entity was replayed")
	}
	for _, r := range env.Records {
		if r.Origin != "reset" {
			t.Errorf("reset record origin = %q, want reset", r.Origin)
		}
	}
}

func TestChangesExit2OnPartialFailure(t *testing.T) {
	f := newChangesFixture(t, "issue", "good", "down", "partial", "brokenhydrate")
	f.listing("good", 0, okSource, [2]string{"added", "bd-1"})
	f.showIssue("bd-1", "2026-09-30T00:00:00Z")
	// "down" fails outright (exit 3 from pg-connector).
	f.listing("down", 3, "")
	// "partial": pg-connector exit 2 with one degraded backend; its entity hydrates.
	f.listing("partial", 2, `{"backend":"b1","status":"succeeded"},{"backend":"b2","status":"degraded","reason":"rate_limited"},{"backend":"b3","status":"disabled"}`, [2]string{"changed", "bd-2"})
	f.showIssue("bd-2", "2026-09-30T00:00:00Z")
	// "brokenhydrate": its entity cannot be read.
	f.listing("brokenhydrate", 0, okSource, [2]string{"added", "bd-3"})

	env, err := f.envOf("--consumer", "router")
	if exitCodeFor(err) != exitPartial {
		t.Fatalf("exit = %d (%v), want %d", exitCodeFor(err), err, exitPartial)
	}
	bySource := map[string]changes.Source{}
	for _, s := range env.Sources {
		bySource[s.Query] = s
	}
	if bySource["good"].Status != changes.StatusOK {
		t.Errorf("good = %+v", bySource["good"])
	}
	if s := bySource["down"]; s.Status != changes.StatusFailed || s.Reason == "" {
		t.Errorf("down = %+v", s)
	}
	if s := bySource["partial"]; s.Status != changes.StatusDegraded || s.Reason != "rate_limited" {
		t.Errorf("partial = %+v (disabled counts as healthy)", s)
	}
	if s := bySource["brokenhydrate"]; s.Status != changes.StatusDegraded || !strings.Contains(s.Reason, "bd-3") {
		t.Errorf("brokenhydrate = %+v", s)
	}
	// Partial records are returned and the cursor advanced.
	got := envKindsByID(env)
	if _, ok := got["bd-1"]; !ok || len(got) != 2 {
		t.Errorf("records = %v, want bd-1 and bd-2 only", got)
	}
	if c, _ := f.consumer("router"); c.Cursor != env.Cursor.To || env.Cursor.To == 0 {
		t.Errorf("cursor = %+v, envelope %+v", c, env.Cursor)
	}
	// A failed hydration never produces a removed/closed record.
	for _, r := range env.Records {
		for _, k := range r.Kinds {
			if k == "removed" || k == "closed" {
				t.Errorf("unexpected %s for %s", k, r.ID)
			}
		}
	}
	// The failure is persisted for status and doctor.
	stats, err := changes.ReadHydrationStats(f.seed, "issue")
	if err != nil || stats.Failures != 1 || stats.Hydrations != 3 || stats.Degraded["bd-3"].Count != 1 {
		t.Errorf("hydration stats = %+v, %v", stats, err)
	}
}

func TestChangesTotalFailureLogsNothingAndKeepsCursor(t *testing.T) {
	f := newChangesFixture(t, "issue", "a", "b")
	// An existing record and a registered consumer behind it.
	f.listing("a", 0, okSource, [2]string{"added", "bd-1"})
	f.listing("b", 0, okSource)
	f.showIssue("bd-1", "2026-09-30T00:00:00Z")
	if _, err := f.envOf("--consumer", "other"); err != nil {
		t.Fatal(err)
	}
	if err := f.seed.RegisterConsumer("router", "issue", changesTestNow.Add(-48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	logBefore := f.logLen()

	f.listing("a", 3, "")
	f.listing("b", 3, "")
	env, err := f.envOf("--consumer", "router", "--reset")
	if exitCodeFor(err) != exitTotal || !errors.Is(err, changes.ErrTotalFailure) {
		t.Fatalf("err = %v (exit %d), want exit %d", err, exitCodeFor(err), exitTotal)
	}
	if len(env.Records) != 0 || len(env.Sources) != 2 || env.Sources[0].Status != changes.StatusFailed {
		t.Errorf("envelope = %+v", env)
	}
	if f.logLen() != logBefore {
		t.Errorf("a total failure logged records: %d -> %d", logBefore, f.logLen())
	}
	c, _ := f.consumer("router")
	if c.Cursor != 0 {
		t.Errorf("cursor moved on total failure: %+v", c)
	}
}

func TestSeenAtLivenessReplacesHeartbeat(t *testing.T) {
	f := newChangesFixture(t, "issue", "open")
	f.listing("open", 0, okSource)
	old := changesTestNow.Add(-72 * time.Hour)
	if err := f.seed.RegisterConsumer("router", "issue", old); err != nil {
		t.Fatal(err)
	}

	// A cached call is not a real call: seen_at is untouched.
	if _, err := f.envOf("--consumer", "router", "--cached"); err != nil {
		t.Fatal(err)
	}
	if c, _ := f.consumer("router"); c.SeenAt != old.Format(time.RFC3339) {
		t.Errorf("--cached touched seen_at: %q", c.SeenAt)
	}
	// A real call sets it, and no heartbeat key is involved.
	if _, err := f.envOf("--consumer", "router"); err != nil {
		t.Fatal(err)
	}
	if c, _ := f.consumer("router"); c.SeenAt != changesTestNow.Format(time.RFC3339) {
		t.Errorf("real call seen_at = %q, want %q", c.SeenAt, changesTestNow.Format(time.RFC3339))
	}
	if _, found, _ := f.seed.GetMeta(store.MetaKeyLastHeartbeat); found {
		t.Error("changes wrote the retired heartbeat key")
	}
	// A consumer first seen by a real call is registered with that seen_at.
	if _, err := f.envOf("--consumer", "fresh"); err != nil {
		t.Fatal(err)
	}
	if c, ok := f.consumer("fresh"); !ok || c.SeenAt != changesTestNow.Format(time.RFC3339) {
		t.Errorf("fresh = %+v, %v", c, ok)
	}
}

func TestChangesLimitPagesThroughTheLog(t *testing.T) {
	f := newChangesFixture(t, "issue", "open")
	f.listing("open", 0, okSource)
	for i := 1; i <= 5; i++ {
		if _, err := f.seed.WriteEntityWithLog(
			store.Entity{Repo: "o/r", EntityType: "issue", EntityID: fmt.Sprintf("bd-%d", i), Facts: `{}`, AsOf: "2026-09-29T00:00:00Z"},
			0, []string{"reconcile"}, "sweep", "2026-09-29T10:00:00Z",
		); err != nil {
			t.Fatal(err)
		}
	}
	var pages [][]int64
	for i := 0; i < 4; i++ {
		env, err := f.envOf("--consumer", "router", "--limit", "2")
		if err != nil {
			t.Fatal(err)
		}
		var seqs []int64
		for _, r := range env.Records {
			seqs = append(seqs, r.Seq)
		}
		pages = append(pages, seqs)
	}
	// seed's own records are 1..5 (each insert logs one record); a page never
	// exceeds the limit and pages never overlap.
	var all []int64
	for i, p := range pages {
		if len(p) > 2 {
			t.Errorf("page %d has %d records, limit is 2", i, len(p))
		}
		all = append(all, p...)
	}
	if len(pages[0]) != 2 || len(pages[1]) != 2 || len(pages[2]) != 1 || len(pages[3]) != 0 {
		t.Fatalf("pages = %v, want sizes 2,2,1,0", pages)
	}
	for i := 1; i < len(all); i++ {
		if all[i] <= all[i-1] {
			t.Errorf("pages overlap or are out of order: %v", pages)
		}
	}
}

func TestChangesConcurrentCallsForOneConsumerSerialize(t *testing.T) {
	f := newChangesFixture(t, "issue", "open")
	f.listing("open", 0, okSource)
	for i := 1; i <= 6; i++ {
		if _, err := f.seed.WriteEntityWithLog(
			store.Entity{Repo: "o/r", EntityType: "issue", EntityID: fmt.Sprintf("bd-%d", i), Facts: `{}`, AsOf: "2026-09-29T00:00:00Z"},
			0, []string{"reconcile"}, "sweep", "2026-09-29T10:00:00Z",
		); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	results := make([]changes.Envelope, 4)
	errs := make([]error, 4)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := newChangesCmd("issue")
			var out bytes.Buffer
			c.SetOut(&out)
			c.SetErr(&bytes.Buffer{})
			c.SetArgs([]string{"--consumer", "router", "--json", "--limit", "2"})
			c.SilenceUsage, c.SilenceErrors = true, true
			errs[i] = c.ExecuteContext(context.Background())
			_ = json.Unmarshal(out.Bytes(), &results[i])
		}()
	}
	wg.Wait()
	seen := map[int64]int{}
	for i, env := range results {
		if errs[i] != nil {
			t.Fatalf("call %d: %v", i, errs[i])
		}
		for _, r := range env.Records {
			seen[r.Seq]++
		}
	}
	var seqs []int64
	for s, n := range seen {
		if n != 1 {
			t.Errorf("record %d delivered %d times: concurrent calls did not serialize", s, n)
		}
		seqs = append(seqs, s)
	}
	sort.Slice(seqs, func(i, j int) bool { return seqs[i] < seqs[j] })
	if len(seqs) != 6 {
		t.Errorf("four serialized limit-2 calls must deliver all 6 records exactly once, got %v", seqs)
	}
}

func TestChangesPrunesWithConfiguredRetentionAndStaleness(t *testing.T) {
	for _, tc := range []struct {
		name         string
		slowSeenAt   time.Time
		wantRowsLeft int
	}{
		{"a stale consumer does not hold rows back", changesTestNow.Add(-3 * time.Hour), 0},
		{"a live slow consumer holds rows back", changesTestNow.Add(-10 * time.Minute), 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newChangesFixture(t, "issue", "open")
			f.cfg.ChangeLogRetentionRaw = "1h"
			f.cfg.ConsumerStaleAfterRaw = "1h"
			f.listing("open", 0, okSource)
			for i := 1; i <= 2; i++ {
				if _, err := f.seed.WriteEntityWithLog(
					store.Entity{Repo: "o/r", EntityType: "issue", EntityID: fmt.Sprintf("bd-%d", i), Facts: `{}`, AsOf: "2026-09-01T00:00:00Z"},
					0, []string{"reconcile"}, "sweep", "2026-09-01T10:00:00Z",
				); err != nil {
					t.Fatal(err)
				}
			}
			if err := f.seed.RegisterConsumer("slow", "issue", tc.slowSeenAt); err != nil {
				t.Fatal(err)
			}
			if _, err := f.envOf("--consumer", "router"); err != nil {
				t.Fatal(err)
			}
			if got := f.logLen(); got != tc.wantRowsLeft {
				t.Errorf("rows left = %d, want %d", got, tc.wantRowsLeft)
			}
		})
	}
}

func TestChangesRefusesOldSchemaStore(t *testing.T) {
	path := storeAtVersion(t, "old")
	cfg := openTestConfig("o/r")
	cfg.Watch.Issue.Queries = []string{"open"}
	withOpenSeams(t, cfg, func() (*store.Store, error) { return store.Open(path) })
	out, _, err := runChangesCmd(t, "issue", "--consumer", "router")
	if !errors.Is(err, store.ErrOldSchema) || !strings.Contains(err.Error(), "pg-desk migrate --cutover") {
		t.Fatalf("err = %v, want the old-schema refusal naming pg-desk migrate --cutover", err)
	}
	if out != "" || exitCodeFor(err) != 1 {
		t.Errorf("stdout %q, exit %d", out, exitCodeFor(err))
	}
	// --cached is refused too: there is no change_log to peek at.
	if _, _, err := runChangesCmd(t, "issue", "--consumer", "router", "--cached"); !errors.Is(err, store.ErrOldSchema) {
		t.Errorf("--cached on an old-schema store: %v", err)
	}
}

func TestChangesTextRendering(t *testing.T) {
	f := newChangesFixture(t, "issue", "open", "mine")
	f.listing("open", 0, okSource, [2]string{"added", "bd-1"})
	f.showIssue("bd-1", "2026-09-30T00:00:00Z")
	f.listing("mine", 2, `{"backend":"b","status":"degraded","reason":"rate_limited"}`)
	out, _, err := runChangesCmd(t, "issue", "--consumer", "router")
	if exitCodeFor(err) != exitPartial {
		t.Fatalf("err = %v", err)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 4 ||
		!strings.HasPrefix(lines[0], "issue bd-1  ") || !strings.HasSuffix(lines[0], "  reconcile  origin=pg-connector") ||
		lines[1] != "source open: ok" || lines[2] != "source mine: degraded (rate_limited)" ||
		!strings.HasPrefix(lines[3], "cursor 0 -> ") {
		t.Errorf("text output:\n%s", out)
	}
}

// TestChangesIssueEnvelopeGolden pins the wire shape of an issue call against
// testdata/changes_issue_envelope.golden.json. For issues comments_changed is
// an updated_at PROXY: the landed issue payload carries no comment field, so
// the kind means "updated_at advanced while nothing else did", not a
// comment-level diff. Only the store-assigned seq/at fields are normalized.
func TestChangesIssueEnvelopeGolden(t *testing.T) {
	f := newChangesFixture(t, "issue", "open")
	f.listing("open", 0, okSource, [2]string{"added", "bd-1"})
	f.showIssue("bd-1", "2026-09-30T00:00:00Z")
	if _, err := f.envOf("--consumer", "router"); err != nil {
		t.Fatal(err)
	}
	f.listing("open", 0, okSource, [2]string{"changed", "bd-1"})
	f.showIssue("bd-1", "2026-09-30T05:00:00Z")
	out, _, err := runChangesCmd(t, "issue", "--consumer", "router", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var env changes.Envelope
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatal(err)
	}
	if len(env.Records) != 1 || env.Records[0].Kinds[0] != "comments_changed" {
		t.Fatalf("records = %+v, want one comments_changed", env.Records)
	}
	env.Cursor = changes.Cursor{From: 1, To: 2}
	env.Records[0].Seq = 2
	env.Records[0].At = "2026-10-01T12:00:00Z"
	got, _ := json.MarshalIndent(env, "", "  ")

	want, err := os.ReadFile(filepath.Join("testdata", "changes_issue_envelope.golden.json"))
	if err != nil {
		t.Fatal(err)
	}
	var a, b bytes.Buffer
	if err := json.Compact(&a, got); err != nil {
		t.Fatal(err)
	}
	if err := json.Compact(&b, want); err != nil {
		t.Fatal(err)
	}
	if a.String() != b.String() {
		t.Errorf("issue envelope drifted from the golden:\n got: %s\nwant: %s", a.String(), b.String())
	}
}
