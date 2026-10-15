package serve_test

import (
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/client"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/servetest"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// The change feed through the assembled daemon: the ops are registered and
// listed, a cold start causes no change burst, a brand-new member and a
// confirmed departure are delivered, a consumer cannot hold both keys of one
// query, and a store that was moved aside expires every cursor. The daemon runs
// in this process over the counting fake gh (runInProcess).

// pollChanges asks the changes op of an instance for the key (consumer, query)
// and returns the response, so a test can read the error.
func pollChanges(t *testing.T, d *servetest.Daemon, instance, consumer, query string, extra map[string]any) scriptout.Response {
	t.Helper()
	args := map[string]any{"consumer": consumer, "query": query}
	for k, v := range extra {
		args[k] = v
	}
	return d.MustCall(t, instance, req("changes", args), 30*time.Second)
}

// pollPR is the pr feed's poll that must succeed.
func pollPR(t *testing.T, d *servetest.Daemon, consumer, query string) schema.PRChangesResult {
	t.Helper()
	resp := pollChanges(t, d, "pr", consumer, query, nil)
	if resp.Error != nil {
		t.Fatalf("pr changes %s/%s: %s: %s", consumer, query, resp.Error.Code, resp.Error.Message)
	}
	return decode[schema.PRChangesResult](t, resp.Result)
}

// pollCI is the ci feed's poll that must succeed.
func pollCI(t *testing.T, d *servetest.Daemon, consumer, query string) schema.CIChangesResult {
	t.Helper()
	resp := pollChanges(t, d, "ci", consumer, query, nil)
	if resp.Error != nil {
		t.Fatalf("ci changes %s/%s: %s: %s", consumer, query, resp.Error.Code, resp.Error.Message)
	}
	return decode[schema.CIChangesResult](t, resp.Result)
}

// ackChanges acknowledges next on the key and returns the stored position.
func ackChanges(t *testing.T, d *servetest.Daemon, instance, consumer, query string, next int64) schema.ChangesAckResult {
	t.Helper()
	return decode[schema.ChangesAckResult](t, callOK(t, d, instance, "changes_ack",
		map[string]any{"consumer": consumer, "query": query, "next_seq": next}))
}

// sourceNext is the NextSeq of the only source of an answer.
func sourceNext(t *testing.T, sources []schema.ChangeSource) int64 {
	t.Helper()
	if len(sources) != 1 {
		t.Fatalf("sources = %+v, want exactly one", sources)
	}
	return sources[0].NextSeq
}

// awaitBaselines waits until every named configured query has its membership
// baseline in the store (the first complete run set it).
func awaitBaselines(t *testing.T, d *servetest.Daemon, queries ...string) {
	t.Helper()
	db := d.OpenStore(t)
	for _, q := range queries {
		until(t, 60*time.Second, "the baseline of the query "+q, func() bool {
			var n int
			err := db.QueryRow(`SELECT COUNT(*) FROM query WHERE key = ? AND baseline_at IS NOT NULL`, q).Scan(&n)
			return err == nil && n == 1
		})
	}
}

func TestChangesWire_TheOpsAreRegisteredAndListedForPRAndCI(t *testing.T) {
	d := servetest.NewDaemon(t, servetest.Options{})
	p := runInProcess(t, d, nil)
	defer p.stop(t)

	// The pr feed answers PRSchemaVersion and the ci feed CISchemaVersion: the
	// client table must list each op with its instance's current constant, so a
	// schema bump (the constants move with every wire-shape change) that misses
	// the table fails here.
	for _, c := range []struct {
		inst    client.Instance
		version int
	}{{client.PR(), schema.PRSchemaVersion}, {client.CI(), schema.CISchemaVersion}} {
		registered := p.rt.Ops(c.inst.Name)
		for _, op := range []string{"changes", "changes_ack"} {
			if !slices.Contains(registered, op) {
				t.Errorf("%s: the daemon does not register %s (has %v)", c.inst.Name, op, registered)
			}
			if !slices.Contains(c.inst.OpNames(), op) {
				t.Errorf("%s: the client table does not list %s", c.inst.Name, op)
			}
			if got := c.inst.Ops[op].SchemaVersion; got != c.version {
				t.Errorf("%s %s: schema version %d, want %d", c.inst.Name, op, got, c.version)
			}
		}
		caps := c.inst.Capabilities("v")
		if !slices.Contains(caps.Ops, "changes") || !slices.Contains(caps.Ops, "changes_ack") {
			t.Errorf("%s: capabilities ops %v must list changes and changes_ack", c.inst.Name, caps.Ops)
		}
		if caps.Vocabulary[client.VocabOwnsChanges] != true || caps.Vocabulary[client.VocabCacheOptOut] != true {
			t.Errorf("%s: vocabulary %v must declare owns_changes and cache_opt_out", c.inst.Name, caps.Vocabulary)
		}
	}
}

func TestChangesWire_ColdStartAppendsNoRowAndTheFirstPollAnswersNone(t *testing.T) {
	d := servetest.NewDaemon(t, servetest.Options{Before: seed3a})
	p := runInProcess(t, d, nil)
	defer p.stop(t)
	settle(t, d)
	awaitBaselines(t, d, servetest.QueryMine, servetest.QueryTeam)

	// Three PRs entered two queries and nothing was appended: the baseline is
	// not a change.
	if n := countRows(t, d, "change_log"); n != 0 {
		t.Fatalf("a cold start appended %d change rows", n)
	}
	if n := countRows(t, d, "pending_change"); n != 0 {
		t.Fatalf("a cold start left %d pending rows", n)
	}

	// The first poll of a key starts at the tail: it answers nothing, and the key
	// now exists. The same holds for the ci feed.
	pr := pollPR(t, d, "feed-reader", servetest.QueryMine)
	if len(pr.Changes) != 0 {
		t.Errorf("the first pr poll answered %d changes, want none: %+v", len(pr.Changes), pr.Changes)
	}
	ci := pollCI(t, d, "ci-reader", servetest.QueryMine)
	if len(ci.Changes) != 0 {
		t.Errorf("the first ci poll answered %d changes, want none: %+v", len(ci.Changes), ci.Changes)
	}
	if got := len(pr.SourcesFreshness); got != 2 {
		t.Errorf("sources_freshness has %d entries, want the two configured queries", got)
	}
	if n := countRows(t, d, "consumer"); n != 2 {
		t.Errorf("consumer rows = %d, want the two keys the polls created", n)
	}
	if n := countRows(t, d, "change_log"); n != 0 {
		t.Errorf("a poll appended %d change rows", n)
	}
}

func TestChangesWire_ABrandNewPRIsAddedAndAConfirmedDepartureIsRemoved(t *testing.T) {
	// The mine query is re-run every second, so a poll that is not cached finds
	// the run of the new answer without waiting an hour.
	d := servetest.NewDaemon(t, servetest.Options{
		Before: seed3a,
		Config: map[string]any{"query_ttl": map[string]any{servetest.QueryMine: "1s", servetest.QueryTeam: "1h"}},
	})
	p := runInProcess(t, d, nil)
	defer p.stop(t)
	settle(t, d)
	awaitBaselines(t, d, servetest.QueryMine, servetest.QueryTeam)
	pollPR(t, d, "member-reader", servetest.QueryMine) // the key starts at the tail

	// prD is new in the mine query; prC leaves it and its node says MERGED for
	// the confirming read. The unused expression only gives the shim prC's merged
	// node document.
	prD := servetest.PR{NodeID: "PR_node_d", Repo: "example-org/gadget", Number: 15}
	prCMerged := prC
	prCMerged.State = "MERGED"
	d.Shim.SetPRs("is:open label:unused-fixture", prCMerged)
	d.Shim.SetPRs(servetest.MineExpr, prA, prB, prD)

	// A poll that is not cached runs a stale query first, so the membership rows
	// are in the log when it reads it. Unacknowledged rows are redelivered, so
	// polling until both are there loses nothing.
	var res schema.PRChangesResult
	until(t, 60*time.Second, "the arrival and the departure to be delivered", func() bool {
		res = pollPR(t, d, "member-reader", servetest.QueryMine)
		return len(res.Changes) >= 2
	})
	got := map[string]string{}
	for _, c := range res.Changes {
		got[c.Entity.ID] = c.Change
		if c.Seq <= 0 || c.Source == "" {
			t.Errorf("change %+v has no seq or source", c)
		}
	}
	want := map[string]string{prD.Ref(): "added", prC.Ref(): "removed"}
	if len(got) != len(want) || got[prD.Ref()] != "added" || got[prC.Ref()] != "removed" {
		t.Fatalf("changes = %v, want %v", got, want)
	}
	for _, c := range res.Changes {
		switch c.Entity.ID {
		case prD.Ref():
			if !slices.Contains(c.Kinds, "entered_query") {
				t.Errorf("the arrival's kinds = %v, want entered_query", c.Kinds)
			}
		case prC.Ref():
			if !slices.Contains(c.Kinds, "left_query") {
				t.Errorf("the departure's kinds = %v, want left_query", c.Kinds)
			}
		}
	}

	// Acknowledged, they are not delivered again; the team query's key never saw
	// them (prD and prC are not its members).
	next := sourceNext(t, res.Sources)
	if next <= 0 {
		t.Fatalf("next_seq = %d after two appended rows", next)
	}
	if ack := ackChanges(t, d, "pr", "member-reader", servetest.QueryMine, next); ack.AckedSeq != next {
		t.Errorf("acked_seq = %d, want %d", ack.AckedSeq, next)
	}
	if again := pollPR(t, d, "member-reader", servetest.QueryMine); len(again.Changes) != 0 {
		t.Errorf("an acknowledged poll redelivered %d changes", len(again.Changes))
	}
}

func TestChangesWire_OneConsumerHoldingBothKeysOfAQueryIsRefused(t *testing.T) {
	d := servetest.NewDaemon(t, servetest.Options{Before: seed3a})
	p := runInProcess(t, d, nil)
	defer p.stop(t)
	settle(t, d)
	awaitBaselines(t, d, servetest.QueryMine)

	// The consumer takes the pr key first; its poll of the ci key for the same
	// query is refused, naming the key it already holds.
	pollPR(t, d, "both-keys", servetest.QueryMine)
	resp := pollChanges(t, d, "ci", "both-keys", servetest.QueryMine, nil)
	if resp.Error == nil || resp.Error.Code != "invalid_argument" {
		t.Fatalf("the ci poll of a consumer holding the pr key = %+v, want invalid_argument", resp.Error)
	}
	if !strings.Contains(resp.Error.Message, "pr") || !strings.Contains(resp.Error.Message, "both-keys") {
		t.Errorf("the refusal must name the existing key: %q", resp.Error.Message)
	}
	// The acknowledgement of the ci key is refused the same way, and no ci key
	// was created by either.
	ack := d.MustCall(t, "ci", req("changes_ack", map[string]any{"consumer": "both-keys", "query": servetest.QueryMine, "next_seq": 0}), 10*time.Second)
	if ack.Error == nil || ack.Error.Code != "invalid_argument" {
		t.Errorf("the ci ack of a consumer holding the pr key = %+v, want invalid_argument", ack.Error)
	}
	var n int
	if err := d.OpenStore(t).QueryRow(`SELECT COUNT(*) FROM consumer WHERE consumer = 'both-keys' AND kind = 'ci'`).Scan(&n); err != nil || n != 0 {
		t.Errorf("ci keys of the refused consumer = %d (%v), want none", n, err)
	}
}

func TestChangesWire_AStoreMovedAsideExpiresEveryCursorUntilReset(t *testing.T) {
	garbage := []byte(strings.Repeat("this is not a sqlite database; ", 200))
	d := servetest.NewDaemon(t, servetest.Options{Before: func(d *servetest.Daemon) {
		seed3a(d)
		if err := os.MkdirAll(d.StateDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(d.StorePath(), garbage, 0o600); err != nil {
			t.Fatal(err)
		}
	}})
	p := runInProcess(t, d, nil)
	defer p.stop(t)

	// The daemon started on an empty store, so a key it has never stored cannot
	// start at the tail without hiding what changed meanwhile: the first poll of
	// every key, pr and ci, is cursor_expired.
	for _, c := range []struct{ instance, consumer string }{{"pr", "pr-reader"}, {"ci", "ci-reader"}} {
		resp := pollChanges(t, d, c.instance, c.consumer, servetest.QueryMine, nil)
		if resp.Error == nil || resp.Error.Code != "invalid_argument" || !strings.HasPrefix(resp.Error.Message, "cursor_expired:") {
			t.Errorf("%s: first poll after a moved-aside store = %+v, want invalid_argument cursor_expired:", c.instance, resp.Error)
		}
	}
	ack := d.MustCall(t, "pr", req("changes_ack", map[string]any{"consumer": "pr-reader", "query": servetest.QueryMine, "next_seq": 0}), 10*time.Second)
	if ack.Error == nil || !strings.HasPrefix(ack.Error.Message, "cursor_expired:") {
		t.Errorf("the first ack of a key after a moved-aside store = %+v, want cursor_expired:", ack.Error)
	}

	// A reset is the answer the message names: it creates the key at the head.
	settle(t, d)
	awaitBaselines(t, d, servetest.QueryMine)
	resp := pollChanges(t, d, "pr", "pr-reader", servetest.QueryMine, map[string]any{"reset": true})
	if resp.Error != nil {
		t.Fatalf("a reset poll must succeed: %+v", resp.Error)
	}
	if got := decode[schema.PRChangesResult](t, resp.Result); len(got.Changes) != 3 {
		t.Errorf("the reset replay lists %d members, want the three fixture PRs", len(got.Changes))
	}
	if again := pollPR(t, d, "pr-reader", servetest.QueryMine); len(again.Changes) != 0 {
		t.Errorf("a poll after the reset answered %d changes", len(again.Changes))
	}
}
