//go:build integration

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/servetest"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

// confAwaitBaseline waits until the queries' membership baselines are in the
// store, so the first poll of a key starts at the tail rather than racing the
// first complete run.
func confAwaitBaseline(t *testing.T, d *servetest.Daemon, queries ...string) {
	t.Helper()
	db := d.OpenStore(t)
	for _, q := range queries {
		deadline := time.Now().Add(60 * time.Second)
		for {
			var n int
			err := db.QueryRow(`SELECT COUNT(*) FROM query WHERE key = ? AND baseline_at IS NOT NULL`, q).Scan(&n)
			if err == nil && n == 1 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("the baseline of the query %s never appeared (%v)", q, err)
			}
			time.Sleep(25 * time.Millisecond)
		}
	}
}

// confChangesReq is a changes request; extra adds or overrides arguments.
func confChangesReq(consumer, query string, extra map[string]any) string {
	return confOpReq("changes", map[string]any{"consumer": consumer, "query": query}, extra)
}

func confOpReq(op string, args, extra map[string]any) string {
	for k, v := range extra {
		args[k] = v
	}
	raw, err := json.Marshal(map[string]any{"op": op, "args": args})
	if err != nil {
		panic(err)
	}
	return string(raw)
}

// confDoc is the success answer's result as a generic document.
func (r confReply) confDoc(t *testing.T) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(r.resp.Result, &doc); err != nil {
		t.Fatalf("result %s: %v", r.resp.Result, err)
	}
	return doc
}

// confNoNull fails for any JSON null anywhere in the document: every array of a
// change answer is [], never null.
func confNoNull(t *testing.T, what, path string, v any) {
	t.Helper()
	switch x := v.(type) {
	case nil:
		t.Errorf("%s: %s is null, want [] or a value", what, path)
	case map[string]any:
		for k, e := range x {
			confNoNull(t, what, path+"."+k, e)
		}
	case []any:
		for i, e := range x {
			confNoNull(t, what, fmt.Sprintf("%s[%d]", path, i), e)
		}
	}
}

// confSource is the single source of a changes answer.
func confSource(t *testing.T, what string, doc map[string]any) map[string]any {
	t.Helper()
	sources, _ := doc["sources"].([]any)
	if len(sources) != 1 {
		t.Fatalf("%s: sources = %v, want exactly one", what, doc["sources"])
	}
	src, _ := sources[0].(map[string]any)
	return src
}

// confNextSeq is the single source's numeric next_seq of a changes answer.
func confNextSeq(t *testing.T, what string, doc map[string]any) int64 {
	t.Helper()
	next, ok := confSource(t, what, doc)["next_seq"].(float64)
	if !ok {
		t.Fatalf("%s: next_seq in %v is not a number", what, doc["sources"])
	}
	return int64(next)
}

// confChanges are the rows of a changes answer.
func confChanges(doc map[string]any) []map[string]any {
	var out []map[string]any
	rows, _ := doc["changes"].([]any)
	for _, r := range rows {
		m, _ := r.(map[string]any)
		out = append(out, m)
	}
	return out
}

// confPollUntil polls the consumer until the answer carries a row for id, and
// returns that answer; it fails after max.
func confPollUntil(t *testing.T, d *servetest.Daemon, consumer, query, id string, max time.Duration) (confReply, map[string]any) {
	t.Helper()
	deadline := time.Now().Add(max)
	for {
		r := invoke(t, confBackend(d), confChangesReq(consumer, query, nil))
		doc := r.confDoc(t)
		for _, row := range confChanges(doc) {
			if ent, _ := row["entity"].(map[string]any); ent["id"] == id {
				return r, doc
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no change row for %s within %s; last answer %s", id, max, r.stdout)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestConformanceChangesFirstPollAndAckOverTheRealClient(t *testing.T) {
	d := confDaemon(t)
	confAwaitBaseline(t, d, servetest.QueryMine)
	b := confBackend(d)
	const consumer = "first-poll"
	what := "first changes poll"

	r := invoke(t, b, confChangesReq(consumer, servetest.QueryMine, nil))
	r.wantResult(t, what, schema.IssueSchemaVersion)
	doc := r.confDoc(t)
	confNoNull(t, what, "result", doc)

	// The first poll of a key starts at the tail: no rows, one source.
	if changes, ok := doc["changes"].([]any); !ok || len(changes) != 0 {
		t.Errorf("%s: changes = %v, want []", what, doc["changes"])
	}
	if !strings.Contains(string(r.resp.Result), `"changes":[]`) {
		t.Errorf("%s: the raw answer does not carry changes as []: %s", what, r.resp.Result)
	}
	src := confSource(t, what, doc)
	if src["backend"] != servetest.Instance || src["status"] != "succeeded" {
		t.Errorf("%s: source = %v, want the instance %s, succeeded", what, src, servetest.Instance)
	}
	next := confNextSeq(t, what, doc)
	if id, _ := doc["store_identity"].(string); len(id) != 32 {
		t.Errorf("%s: store_identity %q, want the 32 hex characters of the store's identity", what, doc["store_identity"])
	}

	// One sources_freshness entry per configured query; mine carries a
	// last_success_at that is an RFC 3339 instant.
	var fresh []schema.SourceFreshness
	raw, _ := json.Marshal(doc["sources_freshness"])
	if err := json.Unmarshal(raw, &fresh); err != nil {
		t.Fatal(err)
	}
	var names []string
	var mine *schema.SourceFreshness
	for i := range fresh {
		names = append(names, fresh[i].Query)
		if fresh[i].Query == servetest.QueryMine {
			mine = &fresh[i]
		}
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{servetest.QueryMine, servetest.QueryTeam}) || mine == nil {
		t.Fatalf("%s: sources_freshness queries = %v, want one entry per configured query", what, names)
	}
	if _, err := time.Parse(time.RFC3339, mine.LastSuccessAt); err != nil {
		t.Errorf("%s: last_success_at %q for %s: %v", what, mine.LastSuccessAt, servetest.QueryMine, err)
	}

	// The acknowledgement of that position succeeds, stamped, and answers it.
	ack := invoke(t, b, confOpReq("changes_ack", map[string]any{"consumer": consumer, "query": servetest.QueryMine}, map[string]any{"next_seq": next}))
	ack.wantResult(t, "changes_ack", schema.IssueSchemaVersion)
	var got schema.ChangesAckResult
	ack.decode(t, "changes_ack", &got)
	if got.AckedSeq != next || got.Consumer != consumer {
		t.Errorf("changes_ack answered %+v, want acked_seq %d for %s", got, next, consumer)
	}
	// An acknowledgement above the head of the log is refused.
	invoke(t, b, confOpReq("changes_ack", map[string]any{"consumer": consumer, "query": servetest.QueryMine}, map[string]any{"next_seq": next + 1000})).
		wantError(t, "an acknowledgement above the log head", "invalid_argument")
	// An unknown query is the daemon's own config's decision.
	invoke(t, b, confChangesReq(consumer, "no-such-query", nil)).wantError(t, "changes of an unknown query", "query_not_recognized")
}

func TestConformanceChangesResetReplaysTheMembersAsAdded(t *testing.T) {
	d := confDaemon(t)
	confAwaitBaseline(t, d, servetest.QueryMine)
	what := "reset poll"
	r := invoke(t, confBackend(d), confChangesReq("reset", servetest.QueryMine, map[string]any{"reset": true}))
	r.wantResult(t, what, schema.IssueSchemaVersion)
	doc := r.confDoc(t)
	confNoNull(t, what, "result", doc)
	var ids []string
	for _, row := range confChanges(doc) {
		if row["change"] != "added" {
			t.Errorf("%s: row %v, want change added", what, row)
		}
		ent, _ := row["entity"].(map[string]any)
		ids = append(ids, fmt.Sprint(ent["id"]))
	}
	slices.Sort(ids)
	if !slices.Equal(ids, []string{"EXA-1", "EXA-2"}) {
		t.Errorf("%s: replayed %v, want every member of the query once", what, ids)
	}
}

// TestConformanceAChangeAtTheOriginReachesTheConsumerOnceAndIsAcknowledged walks
// the whole path: the origin changes an issue, a write notice has the daemon
// re-pull it, the change settles into the log, the consumer polls it with its
// kinds and fields, acknowledges it, and the next poll is empty.
func TestConformanceAChangeAtTheOriginReachesTheConsumerOnceAndIsAcknowledged(t *testing.T) {
	d := confDaemon(t)
	confAwaitBaseline(t, d, servetest.QueryMine)
	b := confBackend(d)
	const consumer = "walk"
	first := invoke(t, b, confChangesReq(consumer, servetest.QueryMine, nil))
	first.wantResult(t, "tail poll", schema.IssueSchemaVersion)

	moved := confA.WithStatus("In Progress")
	d.Shim.SetIssue(moved)
	invoke(t, b, `{"op":"invalidate","args":{"id":"EXA-1"}}`).wantResult(t, "invalidate", schema.IssueSchemaVersion)

	r, doc := confPollUntil(t, d, consumer, servetest.QueryMine, "EXA-1", 30*time.Second)
	rows := confChanges(doc)
	if len(rows) != 1 {
		t.Fatalf("rows = %v, want exactly the one change of EXA-1", rows)
	}
	row := rows[0]
	kinds, _ := row["kinds"].([]any)
	if row["change"] != "changed" || !slices.Contains(kinds, any("status_changed")) {
		t.Errorf("row = %v, want change changed with the kind status_changed", row)
	}
	if row["source"] != servetest.Instance {
		t.Errorf("row source = %v, want %s", row["source"], servetest.Instance)
	}
	var statusField map[string]any
	for _, f := range asMaps(row["fields"]) {
		if f["name"] == "state" {
			statusField = f
		}
	}
	if statusField == nil || statusField["before"] != "To Do" || statusField["after"] != "In Progress" {
		t.Errorf("row fields = %v, want the state field from To Do to In Progress", row["fields"])
	}
	next := confNextSeq(t, "change poll", doc)
	ack := invoke(t, b, confOpReq("changes_ack", map[string]any{"consumer": consumer, "query": servetest.QueryMine}, map[string]any{"next_seq": next}))
	ack.wantResult(t, "changes_ack", schema.IssueSchemaVersion)
	_ = r

	again := invoke(t, b, confChangesReq(consumer, servetest.QueryMine, nil)).confDoc(t)
	if rows := confChanges(again); len(rows) != 0 {
		t.Errorf("after the acknowledgement the next poll is %v, want no rows", rows)
	}
}

// TestConformanceAnIssueTheOriginNoLongerShowsIsARemovedRowWhenWatched covers the
// last row of the failure catalog: a deleted or inaccessible issue is a removed
// row when it is watched (mine is a watched query).
func TestConformanceAnIssueTheOriginNoLongerShowsIsARemovedRowWhenWatched(t *testing.T) {
	d := confDaemon(t)
	confAwaitBaseline(t, d, servetest.QueryMine)
	b := confBackend(d)
	invoke(t, b, confChangesReq("removal", servetest.QueryMine, nil)).wantResult(t, "tail poll", schema.IssueSchemaVersion)

	d.Shim.RemoveIssue("EXA-2")
	d.Shim.SetIssues(servetest.MineJQL, confA)
	invoke(t, b, `{"op":"invalidate","args":{"id":"EXA-2"}}`).wantResult(t, "invalidate", schema.IssueSchemaVersion)

	_, doc := confPollUntil(t, d, "removal", servetest.QueryMine, "EXA-2", 30*time.Second)
	for _, row := range confChanges(doc) {
		if ent, _ := row["entity"].(map[string]any); ent["id"] == "EXA-2" && row["change"] != "removed" {
			t.Errorf("row of the vanished issue = %v, want change removed", row)
		}
	}
}

func asMaps(v any) []map[string]any {
	var out []map[string]any
	list, _ := v.([]any)
	for _, e := range list {
		if m, ok := e.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// goldenUpdateEnv is the environment variable that rewrites the golden files
// instead of comparing against them.
const goldenUpdateEnv = "PG_CONNECTOR_GOLDEN_UPDATE"

var (
	goldenTime     = regexp.MustCompile(`\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(\.\d+)?(Z|[+-]\d\d:?\d\d)`)
	goldenIdentity = regexp.MustCompile(`"store_identity":"[0-9a-f]{32}"`)
)

// goldenNormalise replaces what differs between two runs of the same scenario:
// the store's random identity and every instant.
func goldenNormalise(raw []byte) []byte {
	raw = goldenIdentity.ReplaceAll(raw, []byte(`"store_identity":"example-store-identity"`))
	return goldenTime.ReplaceAll(raw, []byte("example-time"))
}

// goldenStep is one recorded call.
type goldenStep struct {
	Call   string          `json:"call"`
	Answer json.RawMessage `json:"answer"`
}

// TestConformanceChangesWireGolden records one scripted scenario through the
// real client and the real daemon (a tail poll, a reset, a change and its
// acknowledgement) and compares every answer, whole envelope, with the committed
// files under golden/ in their compact, normalised form. A diff means the wire
// answer of the feed changed. To regenerate after an intended, reviewed change:
//
//	PG_CONNECTOR_GOLDEN_UPDATE=1 go test ./cmd/pg-connector-issue-jira -run TestConformanceChangesWireGolden
func TestConformanceChangesWireGolden(t *testing.T) {
	d := confDaemon(t)
	confAwaitBaseline(t, d, servetest.QueryMine)
	b := confBackend(d)
	steps := map[string][]goldenStep{}
	record := func(op, request string) confReply {
		t.Helper()
		r := invoke(t, b, request)
		var compact bytes.Buffer
		if err := json.Compact(&compact, goldenNormalise(r.stdout)); err != nil {
			t.Fatalf("compact %s: %v", r.stdout, err)
		}
		steps[op] = append(steps[op], goldenStep{Call: strings.TrimSpace(request), Answer: compact.Bytes()})
		return r
	}

	const consumer = "example-consumer"
	first := record("changes", confChangesReq(consumer, servetest.QueryMine, nil))
	record("changes_ack", confOpReq("changes_ack", map[string]any{"consumer": consumer, "query": servetest.QueryMine}, map[string]any{"next_seq": confNextSeq(t, "tail", first.confDoc(t))}))
	record("changes", confChangesReq("example-reset", servetest.QueryMine, map[string]any{"reset": true}))

	d.Shim.SetIssue(confA.WithStatus("In Progress"))
	invoke(t, b, `{"op":"invalidate","args":{"id":"EXA-1"}}`).wantResult(t, "invalidate", schema.IssueSchemaVersion)
	r, doc := confPollUntil(t, d, consumer, servetest.QueryMine, "EXA-1", 30*time.Second)
	_ = r
	// The poll that found the row is recorded with the row; it is asked again so
	// the recorded request and answer are one pair.
	changed := record("changes", confChangesReq(consumer, servetest.QueryMine, nil))
	record("changes_ack", confOpReq("changes_ack", map[string]any{"consumer": consumer, "query": servetest.QueryMine}, map[string]any{"next_seq": confNextSeq(t, "change", doc)}))
	_ = changed
	record("changes", confChangesReq(consumer, servetest.QueryMine, nil))

	dir := "golden"
	update := os.Getenv(goldenUpdateEnv) != ""
	if update {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for op, recorded := range steps {
		path := filepath.Join(dir, "changes_issue_"+op+".json")
		if update {
			raw, err := json.MarshalIndent(recorded, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, append(raw, '\n'), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read golden %s: %v (regenerate with %s=1)", path, err, goldenUpdateEnv)
		}
		var committed []goldenStep
		if err := json.Unmarshal(raw, &committed); err != nil {
			t.Fatalf("decode golden %s: %v", path, err)
		}
		if got, want := goldenFile(t, recorded), goldenFile(t, committed); !bytes.Equal(got, want) {
			t.Errorf("%s differs from the golden (regenerate with %s=1 only after an intended wire change)\n got: %s\nwant: %s", path, goldenUpdateEnv, got, want)
		}
	}
}

// goldenFile renders steps in the one compact form the comparison uses.
func goldenFile(t *testing.T, steps []goldenStep) []byte {
	t.Helper()
	var b bytes.Buffer
	for _, s := range steps {
		var compact bytes.Buffer
		if err := json.Compact(&compact, s.Answer); err != nil {
			t.Fatalf("answer of %s: %v", s.Call, err)
		}
		fmt.Fprintf(&b, "%s\n%s\n", s.Call, compact.Bytes())
	}
	return b.Bytes()
}

// TestConformanceBaselineTimeIsOnTheStatusOpNotTheFeedWire pins where a
// consumer learns a query's baseline marker time: the status op's
// queries[].baseline_at (and baseline_open_age_seconds while it is unset), never
// the feed wire, whose sources_freshness[] entries stay query, last_success_at
// and last_error because that type is shared with the GitHub daemon's wires.
func TestConformanceBaselineTimeIsOnTheStatusOpNotTheFeedWire(t *testing.T) {
	d := confDaemon(t)
	confAwaitBaseline(t, d, servetest.QueryMine)
	b := confBackend(d)

	// The status op, over the real client and socket, carries the baseline time
	// of the query whose first complete run is stored.
	r := invoke(t, b, `{"op":"status"}`)
	r.wantResult(t, "status", schema.IssueSchemaVersion)
	var report struct {
		Queries []map[string]any `json:"queries"`
	}
	r.decode(t, "status", &report)
	var mine map[string]any
	for _, q := range report.Queries {
		if q["name"] == servetest.QueryMine {
			mine = q
		}
	}
	if mine == nil {
		t.Fatalf("status queries = %v, want an entry for %s", report.Queries, servetest.QueryMine)
	}
	at, _ := mine["baseline_at"].(string)
	if _, err := time.Parse(time.RFC3339, at); err != nil {
		t.Errorf("status baseline_at %v for %s: want an RFC 3339 instant (%v)", mine["baseline_at"], servetest.QueryMine, err)
	}
	if _, open := mine["baseline_open_age_seconds"]; open {
		t.Errorf("status carries baseline_open_age_seconds for %s although its baseline is set: %v", servetest.QueryMine, mine)
	}

	// The feed wire's per-query entries carry no baseline field of any name.
	doc := invoke(t, b, confChangesReq("baseline-pin", servetest.QueryMine, nil)).confDoc(t)
	entries, _ := doc["sources_freshness"].([]any)
	if len(entries) == 0 {
		t.Fatalf("sources_freshness = %v, want one entry per configured query", doc["sources_freshness"])
	}
	allowed := map[string]bool{"query": true, "last_success_at": true, "last_error": true}
	for _, e := range entries {
		m, _ := e.(map[string]any)
		for k := range m {
			if !allowed[k] {
				t.Errorf("sources_freshness entry %v has the key %q: the baseline time is read from the status op, a new wire field is a shared-wire schema decision", m, k)
			}
		}
	}
}
