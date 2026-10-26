package main

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/servetest"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

// The watched flag as entity_meta on show and list answers, over the REAL client
// binary, a REAL daemon child and a real store: only pjira is faked. The
// in-process tests of internal/ops/read prove the stored-flag rules; these prove
// the wire shape survives the client, the socket and the JSON codec, and that
// the flag follows the real watched query and the real one-off watch.

var (
	metaMine1 = servetest.Open("EXA-1") // member of the watched query mine
	metaMine2 = servetest.Open("EXA-2") // member of the watched query mine
	metaTeam  = servetest.Open("EXA-3") // member of the unwatched query team only
	metaLone  = servetest.Open("EXA-9") // member of no query
)

// metaDaemon starts a daemon whose watched query mine holds EXA-1 and EXA-2 and
// whose unwatched query team holds EXA-3, and waits until both queries ran.
func metaDaemon(t *testing.T, extra map[string]any) *servetest.Daemon {
	t.Helper()
	d := servetest.StartDaemon(t, servetest.Options{
		Hooks:        "poll_interval=50ms",
		ClientBudget: "3s",
		Config:       extra,
		Before: func(d *servetest.Daemon) {
			d.Shim.SetIssues(servetest.MineJQL, metaMine1, metaMine2)
			d.Shim.SetIssues(servetest.TeamJQL, metaTeam)
			d.Shim.SetIssue(metaLone)
		},
	})
	if resp := d.MustCall(t, servetest.Op("list", `{"query":"`+servetest.QueryMine+`"}`), 20*time.Second); resp.Error != nil {
		t.Fatalf("warm the mine query: %+v", resp.Error)
	}
	if resp := d.MustCall(t, servetest.Op("list", `{"query":"`+servetest.QueryTeam+`"}`), 20*time.Second); resp.Error != nil {
		t.Fatalf("warm the team query: %+v", resp.Error)
	}
	confAwaitBaseline(t, d, servetest.QueryMine)
	return d
}

// metaShow is show over the real client; args are the op's argument object.
func metaShow(t *testing.T, d *servetest.Daemon, args string) (schema.Issue, string) {
	t.Helper()
	r := invoke(t, confBackend(d), `{"op":"show","args":`+args+`}`)
	r.wantResult(t, "show "+args, schema.IssueSchemaVersion)
	var iss schema.Issue
	r.decode(t, "show "+args, &iss)
	return iss, string(r.resp.Result)
}

func metaList(t *testing.T, d *servetest.Daemon, query string) schema.IssueListResult {
	t.Helper()
	r := invoke(t, confBackend(d), `{"op":"list","args":{"query":"`+query+`"}}`)
	r.wantResult(t, "list "+query, schema.IssueSchemaVersion)
	var res schema.IssueListResult
	r.decode(t, "list "+query, &res)
	return res
}

func metaWant(t *testing.T, what string, iss schema.Issue, want bool) {
	t.Helper()
	if got, stated := iss.EntityMeta.WatchedState(); !stated || got != want {
		t.Errorf("%s: entity_meta = %+v, want watched %v stated", what, iss.EntityMeta, want)
	}
}

func TestEntityMetaWatchedOnShowAndListOverTheRealBinary(t *testing.T) {
	d := metaDaemon(t, nil)

	// list: every member of the watched query is stated watched; every member
	// of the unwatched query is stated NOT watched (tracked only).
	mine := metaList(t, d, servetest.QueryMine)
	if len(mine.Entities) != 2 {
		t.Fatalf("mine entities = %d, want 2", len(mine.Entities))
	}
	for _, e := range mine.Entities {
		metaWant(t, "list mine "+e.ID, e, true)
	}
	team := metaList(t, d, servetest.QueryTeam)
	if len(team.Entities) != 1 || team.Entities[0].ID != "EXA-3" {
		t.Fatalf("team entities = %+v, want EXA-3", team.Entities)
	}
	metaWant(t, "list team EXA-3", team.Entities[0], false)

	// show: a member of a watched query, a tracked-only issue and an issue the
	// daemon first meets through a plain read.
	iss, raw := metaShow(t, d, `{"id":"EXA-1"}`)
	metaWant(t, "show EXA-1", iss, true)
	var wire struct {
		Meta map[string]any `json:"entity_meta"`
	}
	if err := json.Unmarshal([]byte(raw), &wire); err != nil || wire.Meta["watched"] != true {
		t.Errorf("wire entity_meta = %v (%v), want {watched: true} beside the entity", wire.Meta, err)
	}
	iss, _ = metaShow(t, d, `{"id":"EXA-3"}`)
	metaWant(t, "show EXA-3 (tracked only)", iss, false)
	iss, _ = metaShow(t, d, `{"id":"EXA-9"}`)
	metaWant(t, "show EXA-9 (read only)", iss, false)

	// A one-off watch read states the flag it just set, and it is sticky.
	iss, _ = metaShow(t, d, `{"id":"EXA-9","watch":true}`)
	metaWant(t, "show EXA-9 with watch", iss, true)
	iss, _ = metaShow(t, d, `{"id":"EXA-9"}`)
	metaWant(t, "show EXA-9 after the watch", iss, true)
	// ... while the plain reads above watched nothing: EXA-3 is still tracked only.
	iss, _ = metaShow(t, d, `{"id":"EXA-3"}`)
	metaWant(t, "show EXA-3 again", iss, false)
}

func TestEntityMetaReadsProduceNoChangeRowAndNoVersionMove(t *testing.T) {
	d := metaDaemon(t, nil)
	b := confBackend(d)
	const consumer = "meta-reads"
	invoke(t, b, confChangesReq(consumer, servetest.QueryMine, nil)).wantResult(t, "tail poll", schema.IssueSchemaVersion)

	first, _ := metaShow(t, d, `{"id":"EXA-1"}`)
	for i := 0; i < 3; i++ {
		metaShow(t, d, `{"id":"EXA-1"}`)
		metaShow(t, d, `{"id":"EXA-3"}`)
		metaList(t, d, servetest.QueryMine)
		metaList(t, d, servetest.QueryTeam)
	}
	last, _ := metaShow(t, d, `{"id":"EXA-1"}`)
	if last.Version != first.Version {
		t.Errorf("version %d -> %d across reads that state the flag, want unchanged", first.Version, last.Version)
	}
	doc := invoke(t, b, confChangesReq(consumer, servetest.QueryMine, nil)).confDoc(t)
	if rows := confChanges(doc); len(rows) != 0 {
		t.Errorf("rows after reads that state the flag = %v, want none", rows)
	}
}

func TestEntityMetaWatchedIsStickyAfterTheIssueLeavesTheWatchedQuery(t *testing.T) {
	// A short query lifetime lets the daemon's own poll re-run the watched query
	// after the origin changes its membership.
	d := metaDaemon(t, map[string]any{"query_ttl_default": "1s"})
	iss, _ := metaShow(t, d, `{"id":"EXA-2"}`)
	metaWant(t, "EXA-2 as a member", iss, true)

	d.Shim.SetIssues(servetest.MineJQL, metaMine1) // EXA-2 leaves the watched query
	deadline := time.Now().Add(60 * time.Second)
	for {
		res := metaList(t, d, servetest.QueryMine)
		gone := true
		for _, id := range res.PresentIDs {
			gone = gone && id != "EXA-2"
		}
		if gone {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("EXA-2 never left the mine query; present %v", res.PresentIDs)
		}
		time.Sleep(100 * time.Millisecond)
	}
	iss, _ = metaShow(t, d, `{"id":"EXA-2"}`)
	metaWant(t, "EXA-2 after it left every query", iss, true)
}
