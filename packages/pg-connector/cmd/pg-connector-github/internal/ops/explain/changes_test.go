package explain

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/router"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// fakeChanges is a ChangeReader: pending rows by node id, change rows by
// reference (newest first, as the contract says), and the arguments it saw.
type fakeChanges struct {
	pending   map[string]PendingSettle
	last      map[string][]ChangeRef
	err       error
	gotNode   string
	gotRef    string
	gotN      int
	pendCalls int
}

func (f *fakeChanges) PendingSettle(_ context.Context, nodeID string) (PendingSettle, bool, error) {
	f.pendCalls++
	f.gotNode = nodeID
	p, ok := f.pending[nodeID]
	return p, ok, f.err
}

func (f *fakeChanges) LastChanges(_ context.Context, ref string, n int) ([]ChangeRef, error) {
	f.gotRef, f.gotN = ref, n
	return f.last[ref], f.err
}

// rigWithChanges is newRig with explain re-registered on a fresh router that
// has the change reader.
func rigWithChanges(t *testing.T, ch ChangeReader) *rig {
	t.Helper()
	g := newRig(t)
	g.r = router.New(router.Options{Instances: []string{"pr", "ci"}})
	if err := Register(g.r, Config{Store: g.st, Queue: g.queue, Clock: governor.NewManualClock(t0), Changes: ch}); err != nil {
		t.Fatal(err)
	}
	return g
}

func sevenChanges() []ChangeRef {
	var out []ChangeRef
	for seq := int64(9); seq >= 3; seq-- { // newest first
		out = append(out, ChangeRef{Seq: seq, Version: seq - 1, Kinds: []string{"head_changed"}, Origin: "refresh", At: t0.Add(time.Duration(seq) * time.Minute)})
	}
	return out
}

func TestExplainShowsThePendingSettleRowAndTheLastFiveChangesAndStillTheHeadAndBase(t *testing.T) {
	ch := &fakeChanges{
		pending: map[string]PendingSettle{nodeID: {
			Awaited: []AwaitedGroup{
				{Group: "files", Since: t0.Add(-20 * time.Second)},
				{Group: "commits", Since: t0.Add(-10 * time.Second)},
			},
			DetectedAt: t0.Add(-25 * time.Second), Deadline: t0.Add(5 * time.Second), Kinds: []string{"head_changed", "files_changed"},
		}},
		last: map[string][]ChangeRef{ref: sevenChanges()},
	}
	g := rigWithChanges(t, ch)
	g.putGroup(t, store.GroupRow{
		Group: "summary", Content: []byte(`{}`), FetchedAt: t0.Add(-time.Minute), FreshUntil: t0.Add(time.Minute),
		FetchedForHead: "head-sha-9", FetchedForBase: "base-sha-9",
	})

	for _, inst := range []string{"pr", "ci"} {
		res := decode(t, g.explain(inst, `{"id":"`+ref+`"}`))
		ps := res.PendingSettle
		if ps == nil || len(ps.Awaited) != 2 || ps.Awaited[0].Group != "files" || !ps.Awaited[0].Since.Equal(t0.Add(-20*time.Second)) ||
			ps.Awaited[1].Group != "commits" || !ps.Deadline.Equal(t0.Add(5*time.Second)) || !ps.DetectedAt.Equal(t0.Add(-25*time.Second)) ||
			strings.Join(ps.Kinds, ",") != "head_changed,files_changed" {
			t.Errorf("%s: pending_settle %+v", inst, ps)
		}
		if len(res.LastChanges) != MaxLastChanges || res.LastChanges[0].Seq != 9 || res.LastChanges[4].Seq != 5 {
			t.Errorf("%s: last_changes %+v, want the five newest (9..5)", inst, res.LastChanges)
		}
		c := res.LastChanges[0]
		if c.Version != 8 || c.Origin != "refresh" || strings.Join(c.Kinds, ",") != "head_changed" || !c.At.Equal(t0.Add(9*time.Minute)) {
			t.Errorf("%s: change ref %+v", inst, c)
		}
		if len(res.Groups) != 1 || res.Groups[0].FetchedForHead != "head-sha-9" || res.Groups[0].FetchedForBase != "base-sha-9" {
			t.Errorf("%s: the head and base each group was fetched for must still show: %+v", inst, res.Groups)
		}
	}
	if ch.gotNode != nodeID || ch.gotRef != ref || ch.gotN != MaxLastChanges {
		t.Errorf("the reader was asked for node %q ref %q n %d", ch.gotNode, ch.gotRef, ch.gotN)
	}
}

func TestExplainWithoutAPendingRowOrChangesOmitsBothFields(t *testing.T) {
	g := rigWithChanges(t, &fakeChanges{})
	g.putGroup(t, store.GroupRow{Group: "summary", FetchedAt: t0, FreshUntil: t0.Add(time.Minute)})
	resp := g.explain("pr", `{"id":"`+ref+`"}`)
	if resp.Error != nil {
		t.Fatal(resp.Error)
	}
	for _, field := range []string{"pending_settle", "last_changes"} {
		if strings.Contains(string(resp.Result), field) {
			t.Errorf("%s must be omitted:\n%s", field, resp.Result)
		}
	}
}

func TestExplainWithoutAChangeReaderAnswersTheEarlierDocument(t *testing.T) {
	g := newRig(t) // Config.Changes nil
	g.putGroup(t, store.GroupRow{Group: "summary", FetchedAt: t0, FreshUntil: t0.Add(time.Minute)})
	resp := g.explain("pr", `{"id":"`+ref+`"}`)
	if strings.Contains(string(resp.Result), "pending_settle") || strings.Contains(string(resp.Result), "last_changes") {
		t.Errorf("%s", resp.Result)
	}
}

func TestExplainTruncatesAReaderThatReturnsMoreThanFive(t *testing.T) {
	g := rigWithChanges(t, &overfull{})
	res := decode(t, g.explain("pr", `{"id":"`+ref+`"}`))
	if len(res.LastChanges) != MaxLastChanges {
		t.Errorf("%d rows", len(res.LastChanges))
	}
}

type overfull struct{}

func (overfull) PendingSettle(context.Context, string) (PendingSettle, bool, error) {
	return PendingSettle{}, false, nil
}

func (overfull) LastChanges(context.Context, string, int) ([]ChangeRef, error) {
	return sevenChanges(), nil
}

func TestExplainAnswersAReaderFailureAsAnError(t *testing.T) {
	g := rigWithChanges(t, &fakeChanges{err: errors.New("database is locked")})
	if resp := g.explain("pr", `{"id":"`+ref+`"}`); resp.Error == nil {
		t.Errorf("a store failure is not silently an empty answer: %s", resp.Result)
	}
}

func TestPendingSettleJSONNames(t *testing.T) {
	raw, err := json.Marshal(Result{PendingSettle: &PendingSettle{Awaited: []AwaitedGroup{{Group: "files"}}}, LastChanges: []ChangeRef{{Seq: 1, Kinds: []string{"k"}}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{`"pending_settle"`, `"awaited"`, `"group"`, `"since"`, `"detected_at"`, `"deadline"`, `"last_changes"`, `"seq"`, `"version"`, `"kinds"`, `"at"`} {
		if !strings.Contains(string(raw), name) {
			t.Errorf("%s missing from %s", name, raw)
		}
	}
}
