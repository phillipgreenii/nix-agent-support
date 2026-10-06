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

// freshHydratedAt stamps seeded entities as hydrated well inside sweep.max_age
// of changesTestNow, so the rolling sweep leaves them alone.
const freshHydratedAt = "2026-10-01T11:00:00Z"

// changesFixture is a new-schema store, a config watching the named queries
// of one entity type, and a fake pg-connector on PATH driven by files in dir:
// <type>-list-<query>.json (+ .exit) answers `<type> list --query q
// --fingerprints`, show-<id>.json answers `<type> show <id>`, and calls.log
// records every call.
type changesFixture struct {
	t    *testing.T
	typ  string
	cfg  *config.Config
	seed *store.Store
	dir  string
	// fps is the fingerprint each id is currently listed with; bumps counts
	// how often listing marked an id changed.
	fps   map[string]string
	bumps int
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
if [ "$v" = list ]; then
  q=""; prev=""
  for a in "$@"; do if [ "$prev" = "--query" ]; then q="$a"; fi; prev="$a"; done
  echo "$t list $q" >> "$D/calls.log"
  code=0
  if [ -f "$D/$t-list-$q.exit" ]; then code=$(cat "$D/$t-list-$q.exit"); fi
  if [ -f "$D/$t-list-$q.json" ]; then cat "$D/$t-list-$q.json"; fi
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
	return &changesFixture{t: t, typ: typ, cfg: cfg, seed: seed, dir: dir, fps: map[string]string{}}
}

// listing sets what `<type> list --query q --fingerprints` answers: the exit
// code, the backend rows and one listed entity per (kind, id). Kind "added"
// lists the id with its current fingerprint (stable across calls, so a repeat
// is unchanged); "changed" lists it with a NEW fingerprint; "removed" lists
// nothing (an entity that left the query is simply absent from its listing).
func (f *changesFixture) listing(query string, exit int, sources string, changes ...[2]string) {
	f.t.Helper()
	var entities, fps []string
	for _, c := range changes {
		kind, id := c[0], c[1]
		if kind == "removed" {
			continue
		}
		if kind == "changed" {
			f.bumps++
			f.fps[id] = fmt.Sprintf("fp-%s-v%d", id, f.bumps)
		} else if f.fps[id] == "" {
			f.fps[id] = "fp-" + id
		}
		entities = append(entities, fmt.Sprintf(`{"id":%q,"title":"title of %s","stale":false}`, id, id))
		fps = append(fps, fmt.Sprintf(`%q:%q`, id, f.fps[id]))
	}
	body := fmt.Sprintf(`{"entities":[%s],"present_ids":[],"sources":[%s],"truncated":false,"fingerprints":{%s}}`,
		strings.Join(entities, ","), sources, strings.Join(fps, ","))
	f.write(fmt.Sprintf("%s-list-%s.json", f.typ, query), body)
	f.write(fmt.Sprintf("%s-list-%s.exit", f.typ, query), fmt.Sprint(exit))
}

const okSource = `{"source":"b","status":"succeeded","count":0}`

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

func TestChangesListAndDiffHydratesLogsAndAdvances(t *testing.T) {
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
	if calls[0] != "issue list open" || len(calls) != 3 {
		t.Errorf("connector calls = %v", calls)
	}
	c, ok := f.consumer("router")
	if !ok || c.Cursor != env.Cursor.To || env.Cursor.From != 0 || env.Cursor.To == 0 {
		t.Errorf("cursor = %+v, envelope cursor %+v", c, env.Cursor)
	}

	// A second call with an unchanged listing delivers nothing and keeps the
	// cursor: the stored list fingerprints equal the listed ones.
	f.listing("open", 0, okSource, [2]string{"added", "bd-1"}, [2]string{"added", "bd-2"})
	callsBefore := len(f.calls())
	again, err := f.envOf("--consumer", "router")
	if err != nil || len(again.Records) != 0 || again.Cursor.From != env.Cursor.To || again.Cursor.To != env.Cursor.To {
		t.Errorf("second call = %+v, %v", again, err)
	}
	if got := f.calls()[callsBefore:]; len(got) != 1 || got[0] != "issue list open" {
		t.Errorf("an unchanged tick must only list, got %v", got)
	}
	// Whatever the fingerprints, nothing is ever queued for a later tick.
	if _, found, _ := f.seed.GetMeta("change_flow.deferred.issue"); found {
		t.Error("a deferral queue was persisted")
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
	f := newChangesFixture(t, "issue")
	_, _, err := runChangesCmd(t, "issue", "--consumer", "router")
	if err == nil || !strings.Contains(err.Error(), "watch.issue.queries") || exitCodeFor(err) != 1 {
		t.Fatalf("err = %v (exit %d), want exit 1 naming watch.issue.queries", err, exitCodeFor(err))
	}
	if len(f.calls()) != 0 {
		t.Errorf("pg-connector was called: %v", f.calls())
	}
}

// A thread list cannot be fingerprinted, so its changes verb is refused with a
// clear error before any connector call, with or without --cached.
func TestChangesRefusesThreadWithoutCallingTheConnector(t *testing.T) {
	f := newChangesFixture(t, "thread", "mentions")
	for _, args := range [][]string{{"--consumer", "router"}, {"--consumer", "router", "--cached"}} {
		_, _, err := runChangesCmd(t, "thread", args...)
		if !errors.Is(err, changes.ErrUnsupportedType) || !strings.Contains(err.Error(), "cannot be fingerprinted") || exitCodeFor(err) != 1 {
			t.Fatalf("%v: err = %v (exit %d)", args, err, exitCodeFor(err))
		}
	}
	if len(f.calls()) != 0 {
		t.Errorf("pg-connector was called: %v", f.calls())
	}
	if _, ok := f.consumer("router"); ok {
		t.Error("a refused call registered the consumer")
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
	if calls := f.calls(); len(calls) != 1 || calls[0] != "issue list mine" {
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
	f.listing("open", 0, okSource, [2]string{"added", "bd-1"}, [2]string{"added", "bd-2"})
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
	f.listing("partial", 2, `{"source":"b1","status":"succeeded"},{"source":"b2","status":"degraded","reason":"rate_limited"},{"source":"b3","status":"disabled"}`, [2]string{"changed", "bd-2"})
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
	// The seeded records are days old on purpose; keep the local reconcile
	// tier (sweep.reconcile_age) from re-emitting a record for them.
	f.cfg.Sweep.ReconcileAge = "3650d"
	f.listing("open", 0, okSource)
	for i := 1; i <= 5; i++ {
		if _, err := f.seed.WriteEntityStateWithLog(
			store.Entity{Repo: "o/r", EntityType: "issue", EntityID: fmt.Sprintf("bd-%d", i), Facts: `{}`, AsOf: "2026-09-29T00:00:00Z"},
			0, freshHydratedAt, true, []string{"reconcile"}, "sweep", "2026-09-29T10:00:00Z",
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
	// The seeded records are days old on purpose; keep the local reconcile
	// tier (sweep.reconcile_age) from re-emitting a record for them.
	f.cfg.Sweep.ReconcileAge = "3650d"
	f.listing("open", 0, okSource)
	for i := 1; i <= 6; i++ {
		if _, err := f.seed.WriteEntityStateWithLog(
			store.Entity{Repo: "o/r", EntityType: "issue", EntityID: fmt.Sprintf("bd-%d", i), Facts: `{}`, AsOf: "2026-09-29T00:00:00Z"},
			0, freshHydratedAt, true, []string{"reconcile"}, "sweep", "2026-09-29T10:00:00Z",
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
			// The seeded records are old on purpose (they are what prune removes);
			// keep the local reconcile tier from re-emitting fresh ones.
			f.cfg.Sweep.ReconcileAge = "3650d"
			f.listing("open", 0, okSource)
			for i := 1; i <= 2; i++ {
				if _, err := f.seed.WriteEntityStateWithLog(
					store.Entity{Repo: "o/r", EntityType: "issue", EntityID: fmt.Sprintf("bd-%d", i), Facts: `{}`, AsOf: "2026-09-01T00:00:00Z"},
					0, freshHydratedAt, true, []string{"reconcile"}, "sweep", "2026-09-01T10:00:00Z",
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
	f.listing("mine", 2, `{"source":"b","status":"degraded","reason":"rate_limited"}`)
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

// --- watched-set membership, source-terminal deactivation, sweep, budget ---

// showIssueState is showIssue with an explicit issue state.
func (f *changesFixture) showIssueState(id, state string) {
	f.t.Helper()
	f.write("show-"+strings.NewReplacer("/", "_", "#", "_").Replace(id)+".json", fmt.Sprintf(
		`{"protocolVersion":1,"schemaVersion":4,"result":{"id":%q,"title":"title of %s","owner":"me","assignee":"x","issue_type":"task","state":%q,"updated_at":"2026-09-30T00:00:00Z","as_of":"2026-09-30T00:00:00Z"}}`,
		id, id, state,
	))
}

// seedHydrated stores an issue hydrated at hydratedAt (an unobserved-by-the-
// connector row, as the cutover leaves them) and shows it as open.
func (f *changesFixture) seedHydrated(id, hydratedAt string) {
	f.t.Helper()
	if _, err := f.seed.WriteEntityStateWithLog(
		store.Entity{Repo: "o/r", EntityType: "issue", EntityID: id, Facts: `{}`, AsOf: "2026-09-29T00:00:00Z"},
		0, hydratedAt, true, []string{"reconcile"}, "sweep", "2026-09-29T10:00:00Z",
	); err != nil {
		f.t.Fatal(err)
	}
	f.showIssue(id, "2026-09-30T00:00:00Z")
}

func (f *changesFixture) entity(id string) store.Entity {
	f.t.Helper()
	e, found, err := f.seed.GetEntity("o/r", f.typ, id)
	if err != nil || !found {
		f.t.Fatalf("entity %s: found=%v err=%v", id, found, err)
	}
	return e
}

func (f *changesFixture) shows() []string {
	var out []string
	for _, c := range f.calls() {
		if strings.HasPrefix(c, f.typ+" show ") {
			out = append(out, strings.TrimPrefix(c, f.typ+" show "))
		}
	}
	return out
}

func recordsOf(env changes.Envelope, id string) []changes.Record {
	var out []changes.Record
	for _, r := range env.Records {
		if r.ID == id {
			out = append(out, r)
		}
	}
	return out
}

func TestChangesDroppedEntityBecomesInactive(t *testing.T) {
	f := newChangesFixture(t, "issue", "a", "b")
	f.listing("a", 0, okSource, [2]string{"added", "bd-1"}, [2]string{"added", "bd-2"})
	f.listing("b", 0, okSource, [2]string{"added", "bd-2"})
	f.showIssue("bd-1", "2026-09-30T00:00:00Z")
	f.showIssue("bd-2", "2026-09-30T00:00:00Z")
	if _, err := f.envOf("--consumer", "router"); err != nil {
		t.Fatal(err)
	}

	// bd-1 drops out of its only query; bd-2 drops out of "a" but "b" still
	// lists it, so only bd-1 leaves the watched set.
	f.listing("a", 0, okSource)
	f.listing("b", 0, okSource, [2]string{"added", "bd-2"})
	env, err := f.envOf("--consumer", "router")
	if err != nil {
		t.Fatal(err)
	}
	if got := recordsOf(env, "bd-1"); len(got) != 1 || len(got[0].Kinds) != 1 || got[0].Kinds[0] != "removed" {
		t.Errorf("bd-1 records = %+v, want exactly one removed", got)
	}
	if got := recordsOf(env, "bd-2"); len(got) != 0 {
		t.Errorf("bd-2 is still listed by query b, got %+v", got)
	}
	if !f.entity("bd-1").Inactive || f.entity("bd-2").Inactive {
		t.Errorf("bd-1 inactive=%v bd-2 inactive=%v, want true/false", f.entity("bd-1").Inactive, f.entity("bd-2").Inactive)
	}

	// Once the last query drops bd-2 it is removed too; a repeat is a no-op.
	f.listing("b", 0, okSource)
	env, err = f.envOf("--consumer", "router")
	if err != nil || len(recordsOf(env, "bd-2")) != 1 || !f.entity("bd-2").Inactive {
		t.Fatalf("bd-2: env=%+v err=%v", env, err)
	}
	again, err := f.envOf("--consumer", "router")
	if err != nil || len(again.Records) != 0 {
		t.Errorf("an unchanged view must write nothing, got %+v, %v", again, err)
	}
}

func TestChangesReturningEntityGetsReconcile(t *testing.T) {
	f := newChangesFixture(t, "issue", "a")
	f.listing("a", 0, okSource, [2]string{"added", "bd-1"})
	f.showIssue("bd-1", "2026-09-30T00:00:00Z")
	if _, err := f.envOf("--consumer", "router"); err != nil {
		t.Fatal(err)
	}
	f.listing("a", 0, okSource)
	if _, err := f.envOf("--consumer", "router"); err != nil {
		t.Fatal(err)
	}
	if !f.entity("bd-1").Inactive {
		t.Fatal("bd-1 should be inactive")
	}

	f.listing("a", 0, okSource, [2]string{"added", "bd-1"})
	env, err := f.envOf("--consumer", "router")
	if err != nil {
		t.Fatal(err)
	}
	if got := envKindsByID(env); len(got["bd-1"]) != 1 || got["bd-1"][0] != "reconcile" || len(env.Records) != 1 {
		t.Errorf("a returning entity must get exactly one reconcile, got %v", got)
	}
	if f.entity("bd-1").Inactive {
		t.Error("a returning entity must be active again")
	}
}

func TestChangesDegradedSourceYieldsNoRemoved(t *testing.T) {
	f := newChangesFixture(t, "issue", "a")
	f.listing("a", 0, okSource, [2]string{"added", "bd-1"})
	f.showIssue("bd-1", "2026-09-30T00:00:00Z")
	if _, err := f.envOf("--consumer", "router"); err != nil {
		t.Fatal(err)
	}

	f.listing("a", 2, `{"source":"b","status":"degraded","reason":"rate_limited"}`)
	env, err := f.envOf("--consumer", "router")
	if exitCodeFor(err) != exitPartial {
		t.Fatalf("err = %v, want exit %d", err, exitPartial)
	}
	if len(env.Records) != 0 || f.entity("bd-1").Inactive {
		t.Errorf("a degraded source must not remove: records=%+v inactive=%v", env.Records, f.entity("bd-1").Inactive)
	}
}

func TestChangesSourceTerminalEntityBecomesInactive(t *testing.T) {
	f := newChangesFixture(t, "issue", "a")
	f.listing("a", 0, okSource, [2]string{"added", "bd-1"})
	f.showIssue("bd-1", "2026-09-30T00:00:00Z")
	if _, err := f.envOf("--consumer", "router"); err != nil {
		t.Fatal(err)
	}
	if f.entity("bd-1").Inactive {
		t.Fatal("an open issue must stay active")
	}

	f.listing("a", 0, okSource, [2]string{"changed", "bd-1"})
	f.showIssueState("bd-1", "closed")
	env, err := f.envOf("--consumer", "router")
	if err != nil {
		t.Fatal(err)
	}
	got := recordsOf(env, "bd-1")
	if len(got) != 1 {
		t.Fatalf("records = %+v", got)
	}
	hasClosed := false
	for _, k := range got[0].Kinds {
		hasClosed = hasClosed || k == "closed"
		if k == "removed" {
			t.Errorf("source-terminal deactivation must not append removed: %v", got[0].Kinds)
		}
	}
	if !hasClosed {
		t.Errorf("kinds = %v, want closed from the classifier", got[0].Kinds)
	}
	if !f.entity("bd-1").Inactive {
		t.Error("a closed issue must become inactive")
	}

	// The sweep leaves it alone from now on (and the listing no longer holds
	// it, so the persisted membership drops it: already inactive, a no-op).
	f.listing("a", 0, okSource)
	before := len(f.shows())
	if env, err = f.envOf("--consumer", "router"); err != nil || len(env.Records) != 0 || len(f.shows()) != before {
		t.Errorf("an inactive entity must not be swept: env=%+v err=%v shows=%v", env, err, f.shows())
	}
}

func TestSweepUnchangedEntityGetsExactlyOneReconcile(t *testing.T) {
	f := newChangesFixture(t, "issue", "a")
	f.listing("a", 0, okSource, [2]string{"added", "bd-1"})
	f.showIssue("bd-1", "2026-09-30T00:00:00Z")
	if _, err := f.envOf("--consumer", "router"); err != nil {
		t.Fatal(err)
	}
	// Age the row past sweep.max_age without changing its facts.
	e := f.entity("bd-1")
	if _, err := f.seed.WriteEntityStateWithLog(e, e.Version, "2026-09-01T00:00:00Z", true, nil, "", ""); err != nil {
		t.Fatal(err)
	}

	f.listing("a", 0, okSource, [2]string{"added", "bd-1"})
	env, err := f.envOf("--consumer", "router")
	if err != nil {
		t.Fatal(err)
	}
	if len(env.Records) != 1 || env.Records[0].ID != "bd-1" || len(env.Records[0].Kinds) != 1 ||
		env.Records[0].Kinds[0] != "reconcile" || env.Records[0].Origin != "sweep" {
		t.Errorf("records = %+v, want one reconcile with origin sweep", env.Records)
	}
	// Now freshly hydrated, so an immediate repeat sweeps nothing.
	if env, err = f.envOf("--consumer", "router"); err != nil || len(env.Records) != 0 {
		t.Errorf("repeat = %+v, %v", env, err)
	}
}

func TestSweepSelectsOldestFirstCapN(t *testing.T) {
	f := newChangesFixture(t, "issue", "a")
	two := 2
	f.cfg.Sweep.MaxPerPoll = &two
	f.seedHydrated("bd-3", "2026-09-10T00:00:00Z")
	f.seedHydrated("bd-1", "2026-09-30T00:00:00Z")
	f.seedHydrated("bd-4", "2026-09-05T00:00:00Z")
	f.seedHydrated("bd-2", "2026-09-20T00:00:00Z")
	f.seedHydrated("bd-fresh", "2026-10-01T11:00:00Z") // inside sweep.max_age: not due
	f.listing("a", 0, okSource)

	env, err := f.envOf("--consumer", "router")
	if err != nil {
		t.Fatal(err)
	}
	if got := f.shows(); len(got) != 2 || got[0] != "bd-4" || got[1] != "bd-3" {
		t.Errorf("swept %v, want the two oldest first: bd-4, bd-3", got)
	}
	for _, r := range env.Records {
		if r.Origin == "sweep" && (len(r.Kinds) != 1 || r.Kinds[0] != "reconcile") {
			t.Errorf("a sweep must log reconcile only, got %+v", r)
		}
	}
}

func TestSweepExcludesInactive(t *testing.T) {
	f := newChangesFixture(t, "issue", "a")
	f.seedHydrated("bd-1", "2026-09-01T00:00:00Z")
	f.seedHydrated("bd-2", "2026-09-01T00:00:00Z")
	e := f.entity("bd-1")
	if _, err := f.seed.WriteEntityStateWithLog(e, e.Version, e.HydratedAt, false, []string{"removed"}, "pg-connector", "2026-09-29T11:00:00Z"); err != nil {
		t.Fatal(err)
	}
	f.listing("a", 0, okSource)
	if _, err := f.envOf("--consumer", "router"); err != nil {
		t.Fatal(err)
	}
	if got := f.shows(); len(got) != 1 || got[0] != "bd-2" {
		t.Errorf("swept %v, want only the active bd-2", got)
	}
}

func TestHydrationBudgetHydratesChangedBeforeSweepDue(t *testing.T) {
	f := newChangesFixture(t, "issue", "a")
	three := 3
	f.cfg.Hydration.MaxPerPoll = &three
	f.seedHydrated("bd-old1", "2026-09-01T00:00:00Z")
	f.seedHydrated("bd-old2", "2026-09-02T00:00:00Z")
	f.listing("a", 0, okSource, [2]string{"added", "bd-1"}, [2]string{"changed", "bd-2"})
	f.showIssue("bd-1", "2026-09-30T00:00:00Z")
	f.showIssue("bd-2", "2026-09-30T00:00:00Z")

	env, err := f.envOf("--consumer", "router")
	if exitCodeFor(err) != exitPartial {
		t.Fatalf("err = %v, want exit %d (sweep deferred by the budget)", err, exitPartial)
	}
	got := f.shows()
	if len(got) != 3 || got[0] != "bd-1" || got[1] != "bd-2" || got[2] != "bd-old1" {
		t.Errorf("hydrated %v, want changed/added first then the oldest sweep-due one", got)
	}
	if len(env.Sources) != 1 || env.Sources[0].Status != changes.StatusDegraded || !strings.Contains(env.Sources[0].Reason, changes.ReasonHydrationBudget) {
		t.Errorf("sources = %+v, want degraded with %s (sweep work was deferred)", env.Sources, changes.ReasonHydrationBudget)
	}
}

func TestBudgetExhaustionMarksSourceDegradedAndKeepsCursor(t *testing.T) {
	f := newChangesFixture(t, "issue", "a", "b")
	one := 1
	f.cfg.Hydration.MaxPerPoll = &one
	f.listing("a", 0, okSource, [2]string{"added", "bd-1"}, [2]string{"added", "bd-2"})
	f.listing("b", 0, okSource)
	f.showIssue("bd-1", "2026-09-30T00:00:00Z")
	f.showIssue("bd-2", "2026-09-30T00:00:00Z")

	env, err := f.envOf("--consumer", "router")
	if exitCodeFor(err) != exitPartial {
		t.Fatalf("err = %v, want exit %d", err, exitPartial)
	}
	bySource := map[string]changes.Source{}
	for _, s := range env.Sources {
		bySource[s.Query] = s
	}
	if s := bySource["a"]; s.Status != changes.StatusDegraded || !strings.Contains(s.Reason, changes.ReasonHydrationBudget) {
		t.Errorf("a = %+v, want degraded with %s", s, changes.ReasonHydrationBudget)
	}
	if bySource["b"].Status != changes.StatusOK {
		t.Errorf("b reported nothing deferred, got %+v", bySource["b"])
	}
	// The delivered record advanced the cursor; the deferred entity has none.
	if len(env.Records) != 1 || env.Records[0].ID != "bd-1" {
		t.Fatalf("records = %+v, want only bd-1", env.Records)
	}
	if c, _ := f.consumer("router"); c.Cursor != env.Cursor.To || env.Cursor.To == 0 {
		t.Errorf("cursor = %+v, envelope %+v", c, env.Cursor)
	}

	// The next poll finds the entity the cap left over, the same way: its row
	// is missing while the unchanged bd-1 matches its stored fingerprint.
	f.listing("a", 0, okSource, [2]string{"added", "bd-1"}, [2]string{"added", "bd-2"})
	next, err := f.envOf("--consumer", "router")
	if err != nil {
		t.Fatalf("next poll: %v", err)
	}
	if len(next.Records) != 1 || next.Records[0].ID != "bd-2" || next.Cursor.From != env.Cursor.To {
		t.Errorf("next = %+v, want bd-2's reconcile past cursor %d", next, env.Cursor.To)
	}
	for _, s := range next.Sources {
		if s.Status != changes.StatusOK {
			t.Errorf("source %+v should be ok once nothing is deferred", s)
		}
	}
}

func TestHydrationBudgetDoesNotCapAResetReplay(t *testing.T) {
	f := newChangesFixture(t, "issue", "a")
	one := 1
	f.cfg.Hydration.MaxPerPoll = &one
	for _, id := range []string{"bd-1", "bd-2", "bd-3"} {
		f.seedHydrated(id, "2026-10-01T11:00:00Z")
	}
	f.listing("a", 0, okSource)
	env, err := f.envOf("--consumer", "router", "--reset")
	if err != nil {
		t.Fatal(err)
	}
	reset := 0
	for _, r := range env.Records {
		if r.Origin == "reset" {
			reset++
		}
	}
	if reset != 3 {
		t.Errorf("reset records = %d, want all 3 active entities regardless of the budget", reset)
	}
}
