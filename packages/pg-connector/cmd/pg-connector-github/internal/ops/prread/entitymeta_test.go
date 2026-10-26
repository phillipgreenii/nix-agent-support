package prread

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// The watched flag as entity_meta on the show answer (bead pg2-xxcdow): read from
// the entity row of the PR the answer is about, and absent when no store was
// consulted. The wire behavior over the real daemon is in internal/serve.

type fakeEntities struct {
	row   store.EntityRow
	found bool
	err   error
	asked []string
}

func (f *fakeEntities) GetEntity(_ context.Context, kind, id string) (store.EntityRow, bool, error) {
	f.asked = append(f.asked, kind+"/"+id)
	return f.row, f.found, f.err
}

func showWith(t *testing.T, st EntityStore) (*schema.PR, error) {
	t.Helper()
	eng := &fakeEngine{res: showResult(t)}
	h := Handlers(Deps{Engine: eng, Store: st, Clock: governor.NewManualClock(t0)})[OpShow]
	out, err := h.Handle(context.Background(), json.RawMessage(args("octo/widgets#42", false)))
	if err != nil {
		return nil, err
	}
	return out.(*schema.PR), nil
}

func TestShowStatesTheStoredWatchedFlagOfTheEntityRow(t *testing.T) {
	for name, tc := range map[string]struct {
		st   *fakeEntities
		want bool
	}{
		"watched row":         {&fakeEntities{row: store.EntityRow{WatchedAt: t0}, found: true}, true},
		"tracked only row":    {&fakeEntities{found: true}, false},
		"no row at all":       {&fakeEntities{}, false},
		"stale zero-time row": {&fakeEntities{row: store.EntityRow{}, found: true}, false},
	} {
		out, err := showWith(t, tc.st)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got, stated := out.EntityMeta.WatchedState(); !stated || got != tc.want {
			t.Errorf("%s: entity_meta = %+v, want watched %v stated", name, out.EntityMeta, tc.want)
		}
		if len(tc.st.asked) != 1 || tc.st.asked[0] != "pr/PR_kwDOExample1" {
			t.Errorf("%s: the store was asked %v, want the PR's own node once", name, tc.st.asked)
		}
	}
}

func TestShowWithoutAStoreStatesNothingAboutTheFlag(t *testing.T) {
	out, err := showWith(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.EntityMeta != nil {
		t.Errorf("entity_meta = %+v, want none when the store was not consulted", out.EntityMeta)
	}
	raw, err := json.Marshal(out)
	if err != nil || strings.Contains(string(raw), "entity_meta") {
		t.Errorf("wire answer %s (%v) carries entity_meta, want it omitted", raw, err)
	}
}

func TestShowIsUnavailableWhenTheEntityRowCannotBeRead(t *testing.T) {
	_, err := showWith(t, &fakeEntities{err: errors.New("disk gone")})
	if err == nil || !errors.Is(err, scriptout.ErrUnavailable) {
		t.Errorf("err = %v, want unavailable", err)
	}
}
