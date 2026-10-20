package main

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/servetest"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// The change feed, refresh and capability vocabulary over the real client: the
// in-process daemon tests (internal/serve/changes_wire_test.go) prove the
// behavior; these prove the same answers survive the real client binary, the
// real socket and a daemon child process, for both registry instances.

// confAwaitBaseline waits until the query's membership baseline is in the
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

// confResult is the success answer's result as a generic document.
func (r confReply) confResult(t *testing.T) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(r.resp.Result, &doc); err != nil {
		t.Fatalf("result %s: %v", r.resp.Result, err)
	}
	return doc
}

// confNoNull fails for any JSON null anywhere in the document: every array of
// a change answer is [], never null.
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

// confNextSeq is the single source's numeric next_seq of a changes answer.
func confNextSeq(t *testing.T, what string, doc map[string]any) int64 {
	t.Helper()
	sources, _ := doc["sources"].([]any)
	if len(sources) != 1 {
		t.Fatalf("%s: sources = %v, want exactly one", what, doc["sources"])
	}
	src, _ := sources[0].(map[string]any)
	next, ok := src["next_seq"].(float64)
	if !ok {
		t.Fatalf("%s: next_seq in %v is not a number", what, src)
	}
	return int64(next)
}

func TestConformanceCapabilitiesDeclareOwnsChangesAndCacheOptOut(t *testing.T) {
	d := confDaemon(t)
	bin := servetest.Binary(t, "", "")
	for _, inst := range confInstances {
		out, exit, err := confBackend(bin, d.ConfigPath, inst.name).Invoke(context.Background(), []byte(`{"op":"capabilities"}`))
		if err != nil || exit != 0 {
			t.Fatalf("%s capabilities: exit %d err %v", inst.name, exit, err)
		}
		var caps scriptout.CapabilitiesResponse
		if err := json.Unmarshal(out, &caps); err != nil {
			t.Fatal(err)
		}
		for _, op := range []string{"changes", "changes_ack", "refresh", "status", "explain"} {
			if !slices.Contains(caps.Ops, op) {
				t.Errorf("%s capabilities: ops %v miss %q", inst.name, caps.Ops, op)
			}
		}
		// The umbrella forwards on exactly these two declarations.
		for _, key := range []string{"owns_changes", "cache_opt_out"} {
			if caps.Vocabulary[key] != true {
				t.Errorf("%s capabilities: vocabulary %v must declare %s: true", inst.name, caps.Vocabulary, key)
			}
		}
	}
}

func TestConformanceChangesFirstPollAndAckOverTheRealClient(t *testing.T) {
	d := confDaemon(t)
	bin := servetest.Binary(t, "", "")
	confAwaitBaseline(t, d, servetest.QueryMine)

	for _, inst := range confInstances {
		b := confBackend(bin, d.ConfigPath, inst.name)
		consumer := "first-poll-" + inst.name
		what := inst.name + " first changes poll"

		r := invoke(t, b, confChangesReq(consumer, servetest.QueryMine, map[string]any{"cached": false, "reset": false}))
		r.wantResult(t, what, inst.schemaVersion)
		doc := r.confResult(t)
		confNoNull(t, what, "result", doc)

		// The first poll of a key starts at the tail: no rows, one source.
		if changes, ok := doc["changes"].([]any); !ok || len(changes) != 0 {
			t.Errorf("%s: changes = %v, want []", what, doc["changes"])
		}
		if !strings.Contains(string(r.resp.Result), `"changes":[]`) {
			t.Errorf("%s: the raw answer does not carry changes as []: %s", what, r.resp.Result)
		}
		next := confNextSeq(t, what, doc)

		// One sources_freshness entry per configured query; mine carries a
		// last_success_at that is an RFC 3339 instant.
		var fresh []schema.SourceFreshness
		raw, _ := json.Marshal(doc["sources_freshness"])
		if err := json.Unmarshal(raw, &fresh); err != nil {
			t.Fatal(err)
		}
		var mine *schema.SourceFreshness
		for i := range fresh {
			if fresh[i].Query == servetest.QueryMine {
				mine = &fresh[i]
			}
		}
		if mine == nil {
			t.Fatalf("%s: sources_freshness %v has no entry for %s", what, fresh, servetest.QueryMine)
		}
		if _, err := time.Parse(time.RFC3339, mine.LastSuccessAt); err != nil {
			t.Errorf("%s: last_success_at %q for %s: %v", what, mine.LastSuccessAt, servetest.QueryMine, err)
		}

		// The acknowledgement of that position succeeds, stamped, and answers it.
		ack := invoke(t, b, confOpReq("changes_ack", map[string]any{"consumer": consumer, "query": servetest.QueryMine}, map[string]any{"next_seq": next}))
		ack.wantResult(t, inst.name+" changes_ack", inst.schemaVersion)
		var got schema.ChangesAckResult
		if err := json.Unmarshal(ack.resp.Result, &got); err != nil {
			t.Fatal(err)
		}
		if got.AckedSeq != next || got.Consumer != consumer || got.Query != servetest.QueryMine {
			t.Errorf("%s changes_ack answered %+v, want acked_seq %d for (%s, %s)", inst.name, got, next, consumer, servetest.QueryMine)
		}
	}
}

func TestConformanceChangesResetReplaysTheMembersAsAdded(t *testing.T) {
	d := confDaemon(t)
	bin := servetest.Binary(t, "", "")
	confAwaitBaseline(t, d, servetest.QueryMine)
	refs := []string{confPRA.Ref(), confPRB.Ref()}

	for _, inst := range confInstances {
		b := confBackend(bin, d.ConfigPath, inst.name)
		what := inst.name + " reset poll"
		r := invoke(t, b, confChangesReq("reset-"+inst.name, servetest.QueryMine, map[string]any{"reset": true}))
		r.wantResult(t, what, inst.schemaVersion)
		doc := r.confResult(t)
		confNoNull(t, what, "result", doc)

		changes, _ := doc["changes"].([]any)
		if len(changes) != len(refs) {
			t.Fatalf("%s: %d changes, want the two fixture members: %s", what, len(changes), r.resp.Result)
		}
		var seen []string
		for _, c := range changes {
			row, _ := c.(map[string]any)
			if row["change"] != "added" {
				t.Errorf("%s: change = %v, want added", what, row["change"])
			}
			if seq, ok := row["seq"].(float64); !ok || seq != 0 {
				t.Errorf("%s: seq = %v, want 0", what, row["seq"])
			}
			for _, key := range []string{"kinds", "fields"} {
				if arr, ok := row[key].([]any); !ok || len(arr) != 0 {
					t.Errorf("%s: %s = %v, want []", what, key, row[key])
				}
			}
			ent, _ := row["entity"].(map[string]any)
			if inst.name == "pr" {
				id, _ := ent["id"].(string)
				seen = append(seen, id)
				continue
			}
			// The ci entity is in CI shape: the pull request, its rollup, its
			// runs (an array), the version and the head.
			id, _ := ent["pr_id"].(string)
			seen = append(seen, id)
			for _, key := range []string{"rollup", "runs", "version", "head_sha"} {
				if _, ok := ent[key]; !ok {
					t.Errorf("%s: the ci entity %v has no %s", what, ent, key)
				}
			}
			if _, ok := ent["runs"].([]any); !ok {
				t.Errorf("%s: runs = %v, want an array", what, ent["runs"])
			}
			if _, ok := ent["id"]; ok {
				t.Errorf("%s: a ci entity is not a PR (it carries id): %v", what, ent)
			}
		}
		slices.Sort(seen)
		want := slices.Clone(refs)
		slices.Sort(want)
		if !slices.Equal(seen, want) {
			t.Errorf("%s: replayed %v, want %v", what, seen, want)
		}
		confNextSeq(t, what, doc)
	}
}

// confQuiet waits until the daemon's own background fetches (the first-sighting
// groups of the fixture pull requests) have stopped calling gh: the call count
// is unchanged for a full second.
func confQuiet(t *testing.T, d *servetest.Daemon) {
	t.Helper()
	last, stable := -1, 0
	for deadline := time.Now().Add(60 * time.Second); time.Now().Before(deadline); time.Sleep(250 * time.Millisecond) {
		if n := len(d.Shim.Calls()); n == last {
			if stable++; stable >= 4 {
				return
			}
		} else {
			last, stable = n, 0
		}
	}
	t.Fatalf("the daemon kept calling gh for 60 s (%d calls)", last)
}

// A cached poll is answered from the store: it makes no gh call, while a poll
// that is not cached runs the query first; a fresh read is the control that
// shows the call counter moves when the daemon does call out.
func TestConformanceChangesCachedPollMakesNoOriginCall(t *testing.T) {
	d := confDaemon(t)
	bin := servetest.Binary(t, "", "")
	confAwaitBaseline(t, d, servetest.QueryMine)

	for _, inst := range confInstances {
		b := confBackend(bin, d.ConfigPath, inst.name)
		consumer := "cached-" + inst.name
		invoke(t, b, confChangesReq(consumer, servetest.QueryMine, nil)).wantResult(t, inst.name+" first poll", inst.schemaVersion)

		confQuiet(t, d)
		before := len(d.Shim.Calls())
		for i := 0; i < 2; i++ {
			invoke(t, b, confChangesReq(consumer, servetest.QueryMine, map[string]any{"cached": true})).
				wantResult(t, fmt.Sprintf("%s cached poll %d", inst.name, i+1), inst.schemaVersion)
		}
		if got := d.Shim.Calls(); len(got) != before {
			t.Errorf("%s: a cached poll made %d origin calls (%v)", inst.name, len(got)-before, got[before:])
		}

		// Control: a fresh read of the same instance goes to the origin, so the
		// counter does move when the daemon calls out (a stale-query poll cannot
		// serve as the control: the daemon re-runs a lapsed query by itself).
		confQuiet(t, d)
		before = len(d.Shim.Calls())
		invoke(t, b, confFreshRead(inst.name)).wantResult(t, inst.name+" fresh read", inst.schemaVersion)
		if got := len(d.Shim.Calls()); got <= before {
			t.Errorf("%s: a fresh read made no origin call (%d calls before, %d after): the cached assertion above proves nothing", inst.name, before, got)
		}
	}
}

// confFreshRead is a read of the instance that must go to the origin.
func confFreshRead(instance string) string {
	if instance == "pr" {
		return `{"op":"list","args":{"query":"mine","fresh":true}}`
	}
	return `{"op":"list_runs","args":{"pr_id":"` + confPRA.Ref() + `","fresh":true}}`
}

func TestConformanceChangesFailuresOverTheRealClient(t *testing.T) {
	d := confDaemon(t)
	bin := servetest.Binary(t, "", "")
	confAwaitBaseline(t, d, servetest.QueryMine)

	for _, inst := range confInstances {
		b := confBackend(bin, d.ConfigPath, inst.name)
		n := inst.name

		invoke(t, b, `{"op":"changes","args":{"query":"mine"}}`).wantError(t, n+" changes without a consumer", "invalid_argument")
		invoke(t, b, `{"op":"changes","args":{"consumer":"errs-`+n+`"}}`).wantError(t, n+" changes without a query", "invalid_argument")
		invoke(t, b, `{"op":"changes_ack","args":{"query":"mine","next_seq":0}}`).wantError(t, n+" changes_ack without a consumer", "invalid_argument")
		invoke(t, b, `{"op":"changes_ack","args":{"consumer":"errs-`+n+`","next_seq":0}}`).wantError(t, n+" changes_ack without a query", "invalid_argument")
		invoke(t, b, confChangesReq("errs-"+n, "no-such-query", nil)).wantError(t, n+" changes of an unconfigured query", "query_not_recognized")

		// An acknowledgement beyond the head of the log is refused.
		first := invoke(t, b, confChangesReq("errs-"+n, servetest.QueryMine, nil))
		first.wantResult(t, n+" first poll", inst.schemaVersion)
		next := confNextSeq(t, n+" first poll", first.confResult(t))
		invoke(t, b, confOpReq("changes_ack", map[string]any{"consumer": "errs-" + n, "query": servetest.QueryMine}, map[string]any{"next_seq": next + 1000})).
			wantError(t, n+" changes_ack beyond the head", "invalid_argument")
	}

	// One consumer cannot hold both feeds of a query, in either order, and the
	// refusal names the key it already holds.
	pr, ci := confBackend(bin, d.ConfigPath, "pr"), confBackend(bin, d.ConfigPath, "ci")
	invoke(t, pr, confChangesReq("holds-pr-first", servetest.QueryMine, nil)).wantResult(t, "pr poll", schema.PRSchemaVersion)
	r := invoke(t, ci, confChangesReq("holds-pr-first", servetest.QueryMine, nil))
	r.wantError(t, "ci poll of a consumer holding the pr key", "invalid_argument")
	if r.resp.Error != nil && !strings.Contains(r.resp.Error.Message, "(holds-pr-first, pr, mine)") {
		t.Errorf("the refusal must name the existing key (holds-pr-first, pr, mine): %q", r.resp.Error.Message)
	}

	invoke(t, ci, confChangesReq("holds-ci-first", servetest.QueryMine, nil)).wantResult(t, "ci poll", schema.CISchemaVersion)
	r = invoke(t, pr, confChangesReq("holds-ci-first", servetest.QueryMine, nil))
	r.wantError(t, "pr poll of a consumer holding the ci key", "invalid_argument")
	if r.resp.Error != nil && !strings.Contains(r.resp.Error.Message, "(holds-ci-first, ci, mine)") {
		t.Errorf("the refusal must name the existing key (holds-ci-first, ci, mine): %q", r.resp.Error.Message)
	}
}

func TestConformanceRefreshOverTheRealClient(t *testing.T) {
	d := confDaemon(t)
	bin := servetest.Binary(t, "", "")
	pr, ci := confBackend(bin, d.ConfigPath, "pr"), confBackend(bin, d.ConfigPath, "ci")

	// queued asserts a stamped success whose queued list names the fixture PR
	// with at least one group.
	queued := func(r confReply, what string, version int, want ...string) {
		t.Helper()
		r.wantResult(t, what, version)
		var res struct {
			Queued []struct {
				ID     string   `json:"id"`
				Groups []string `json:"groups"`
			} `json:"queued"`
		}
		if err := json.Unmarshal(r.resp.Result, &res); err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		ids := map[string][]string{}
		for _, q := range res.Queued {
			ids[q.ID] = q.Groups
		}
		for _, id := range want {
			if len(ids[id]) == 0 {
				t.Errorf("%s: queued %v has no entry with groups for %s", what, res.Queued, id)
			}
		}
		if len(res.Queued) == 0 {
			t.Errorf("%s: queued is empty: %s", what, r.resp.Result)
		}
	}
	queued(invoke(t, pr, `{"op":"refresh","args":{"id":"`+confPRA.Ref()+`"}}`), "pr refresh {id}", schema.PRSchemaVersion, confPRA.Ref())
	queued(invoke(t, pr, `{"op":"refresh","args":{"query":"mine"}}`), "pr refresh {query}", schema.PRSchemaVersion, confPRA.Ref(), confPRB.Ref())
	queued(invoke(t, ci, `{"op":"refresh","args":{"pr_id":"`+confPRA.Ref()+`"}}`), "ci refresh {pr_id}", schema.CISchemaVersion, confPRA.Ref())

	// Exactly one of id and query.
	invoke(t, pr, `{"op":"refresh","args":{"id":"`+confPRA.Ref()+`","query":"mine"}}`).wantError(t, "pr refresh with id and query", "invalid_argument")
	invoke(t, pr, `{"op":"refresh","args":{}}`).wantError(t, "pr refresh with neither", "invalid_argument")
	// ci refresh takes pr_id only.
	invoke(t, ci, `{"op":"refresh","args":{}}`).wantError(t, "ci refresh without pr_id", "invalid_argument")
}

// A pull request with a recorded change reports it in explain's last_changes,
// for both instances: the departure of the second fixture PR from the mine query
// is delivered to a consumer and then shows in the explanation.
func TestConformanceExplainReportsARecordedChange(t *testing.T) {
	d := servetest.StartDaemon(t, servetest.Options{
		Before: func(d *servetest.Daemon) {
			d.Shim.SetPRs(servetest.MineExpr, confPRA, confPRB)
			d.Shim.SetPRs(servetest.TeamExpr, confPRA)
		},
		// The mine query is re-run every second, so a poll that is not cached
		// finds the run of the new answer without waiting an hour.
		Config: map[string]any{"query_ttl": map[string]any{servetest.QueryMine: "1s", servetest.QueryTeam: "1h"}},
	})
	bin := servetest.Binary(t, "", "")
	confAwaitBaseline(t, d, servetest.QueryMine)
	pr := confBackend(bin, d.ConfigPath, "pr")
	invoke(t, pr, confChangesReq("explain-reader", servetest.QueryMine, nil)).wantResult(t, "first poll", schema.PRSchemaVersion)

	// The second PR leaves the mine query and its node says MERGED for the
	// confirming read (the unused expression only gives the shim its merged node).
	merged := confPRB
	merged.State = "MERGED"
	d.Shim.SetPRs("is:open label:unused-fixture", merged)
	d.Shim.SetPRs(servetest.MineExpr, confPRA)

	var res schema.PRChangesResult
	deadline := time.Now().Add(60 * time.Second)
	for {
		r := invoke(t, pr, confChangesReq("explain-reader", servetest.QueryMine, nil))
		r.wantResult(t, "departure poll", schema.PRSchemaVersion)
		if err := json.Unmarshal(r.resp.Result, &res); err != nil {
			t.Fatal(err)
		}
		if len(res.Changes) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the departure of the second fixture PR was never delivered")
		}
		time.Sleep(200 * time.Millisecond)
	}
	if c := res.Changes[0]; c.Change != "removed" || c.Entity.ID != confPRB.Ref() || c.Seq <= 0 {
		t.Fatalf("delivered change = %+v, want the removal of %s with a seq", c, confPRB.Ref())
	}

	for _, inst := range confInstances {
		r := invoke(t, confBackend(bin, d.ConfigPath, inst.name), `{"op":"explain","args":{"id":"`+confPRB.Ref()+`"}}`)
		r.wantResult(t, inst.name+" explain", inst.schemaVersion)
		var ex struct {
			LastChanges []struct {
				Seq   int64    `json:"seq"`
				Kinds []string `json:"kinds"`
			} `json:"last_changes"`
		}
		if err := json.Unmarshal(r.resp.Result, &ex); err != nil {
			t.Fatal(err)
		}
		if len(ex.LastChanges) == 0 || ex.LastChanges[0].Seq != res.Changes[0].Seq || !slices.Contains(ex.LastChanges[0].Kinds, "left_query") {
			t.Errorf("%s explain of %s: last_changes %+v, want the delivered row seq %d with kind left_query", inst.name, confPRB.Ref(), ex.LastChanges, res.Changes[0].Seq)
		}
	}
}
