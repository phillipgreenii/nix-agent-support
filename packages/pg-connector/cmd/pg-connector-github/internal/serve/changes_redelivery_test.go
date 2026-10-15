package serve_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/servetest"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

// At-least-once delivery against the real client binary: a poll that is never
// acknowledged (the consumer died, or never sent changes_ack) is answered again
// with the same rows; after the acknowledgement nothing is returned. The client
// binary answers one request per run and never acknowledges by itself, so "the
// consumer dies before changes_ack" is a run that is not followed by an
// acknowledging one.

// clientPoll runs the real client binary once for the changes op of the pr
// instance and decodes the answer.
func clientPoll(t *testing.T, d *servetest.Daemon, consumer, query string) schema.PRChangesResult {
	t.Helper()
	resp := d.RunClient(t, "pr", req("changes", map[string]any{"consumer": consumer, "query": query}))
	if resp.Error != nil {
		t.Fatalf("client changes %s/%s: %s: %s", consumer, query, resp.Error.Code, resp.Error.Message)
	}
	return decode[schema.PRChangesResult](t, resp.Result)
}

// clientAck runs the real client binary once for changes_ack.
func clientAck(t *testing.T, d *servetest.Daemon, consumer, query string, next int64) schema.ChangesAckResult {
	t.Helper()
	resp := d.RunClient(t, "pr", req("changes_ack", map[string]any{"consumer": consumer, "query": query, "next_seq": next}))
	if resp.Error != nil {
		t.Fatalf("client changes_ack %s/%s: %s: %s", consumer, query, resp.Error.Code, resp.Error.Message)
	}
	return decode[schema.ChangesAckResult](t, resp.Result)
}

// deliveredKey is what "the same rows" means: equal seq, id, version and kinds.
type deliveredKey struct {
	Seq     int64
	ID      string
	Version int64
	Kinds   string
}

func deliveredKeys(res schema.PRChangesResult) []deliveredKey {
	var out []deliveredKey
	for _, c := range res.Changes {
		kinds := slices.Clone(c.Kinds)
		slices.Sort(kinds)
		out = append(out, deliveredKey{Seq: c.Seq, ID: c.Entity.ID, Version: c.Entity.Version, Kinds: strings.Join(kinds, ",")})
	}
	return out
}

func TestChangesRedelivery_AnUnacknowledgedPollIsRedeliveredToTheRealClientBinaryAndAnAcknowledgedOneIsNot(t *testing.T) {
	t.Parallel()
	d := startFilled(t, servetest.Options{Config: settleConfig("3s")})

	// The consumer's FIRST poll creates its key at the tail; it is acknowledged,
	// and only then is the change provoked.
	first := clientPoll(t, d, "c", servetest.QueryMine)
	if len(first.Changes) != 0 {
		t.Fatalf("the first poll answered %+v, want none (a key starts at the tail)", first.Changes)
	}
	clientAck(t, d, "c", servetest.QueryMine, sourceNext(t, first.Sources))

	// A title change settles at once (it awaits no dependent): one row.
	renamed := prA
	renamed.Title = "Renamed example change"
	d.Shim.SetPRs(servetest.MineExpr, renamed, prB, prC)
	d.Shim.SetPRs(servetest.TeamExpr, renamed, prB)
	until(t, 60*time.Second, "the title change to be appended", func() bool { return len(d.ChangeLog(t)) >= 1 })
	quiet(t, d)
	if rows := d.ChangeLog(t); len(rows) != 1 {
		t.Fatalf("the title change appended %+v, want one row", rows)
	}

	// The consumer polls and dies before it acknowledges: the next poll returns
	// the same rows, and so does the one after it.
	got := clientPoll(t, d, "c", servetest.QueryMine)
	if len(got.Changes) != 1 || got.Changes[0].Entity.ID != prA.Ref() {
		t.Fatalf("the poll answered %+v, want the one row of %s", got.Changes, prA.Ref())
	}
	for i := 0; i < 2; i++ {
		again := clientPoll(t, d, "c", servetest.QueryMine)
		if !slices.Equal(deliveredKeys(again), deliveredKeys(got)) {
			t.Fatalf("redelivery %d = %+v, want the same rows %+v", i+1, deliveredKeys(again), deliveredKeys(got))
		}
		if sourceNext(t, again.Sources) != sourceNext(t, got.Sources) {
			t.Errorf("redelivery %d: next_seq %d, was %d", i+1, sourceNext(t, again.Sources), sourceNext(t, got.Sources))
		}
	}

	// The acknowledgement ends it: nothing is returned afterwards, and a daemon
	// restart does not bring the row back.
	next := sourceNext(t, got.Sources)
	if ack := clientAck(t, d, "c", servetest.QueryMine, next); ack.AckedSeq != next {
		t.Errorf("acked_seq = %d, want %d", ack.AckedSeq, next)
	}
	if after := clientPoll(t, d, "c", servetest.QueryMine); len(after.Changes) != 0 {
		t.Errorf("a poll after the acknowledgement answered %+v", after.Changes)
	}
	servetest.StopDaemon(t, d)
	d.Start(t)
	if after := clientPoll(t, d, "c", servetest.QueryMine); len(after.Changes) != 0 {
		t.Errorf("a poll after a restart answered %+v although the row was acknowledged", after.Changes)
	}
}
