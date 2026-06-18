package discover

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/phillipgreenii/pr-pool/internal/item"
	"github.com/phillipgreenii/pr-pool/internal/query"
	"github.com/phillipgreenii/pr-pool/internal/roles"
)

// fakeQuery is a stand-in query.Query returning canned items (or an error).
type fakeQuery struct {
	items []item.Item
	err   error
}

func (f fakeQuery) Validate() error { return nil }
func (f fakeQuery) Run(context.Context, query.Env) ([]item.Item, error) {
	return f.items, f.err
}

func TestDiscover_orderAndEnabled(t *testing.T) {
	rs := roles.RoleSet{
		{Name: "a", Enabled: true, Query: fakeQuery{items: []item.Item{{ID: "1"}}}},
		{Name: "b", Enabled: false, Query: fakeQuery{items: []item.Item{{ID: "2"}}}},
		{Name: "c", Enabled: true, Query: fakeQuery{items: []item.Item{{ID: "3"}}}},
	}
	got, err := Discover(context.Background(), query.Env{}, rs)
	if err != nil {
		t.Fatal(err)
	}
	// config order preserved; the disabled role yields nothing.
	if len(got) != 2 || got[0].Role.Name != "a" || got[0].Item.ID != "1" || got[1].Role.Name != "c" || got[1].Item.ID != "3" {
		t.Fatalf("order/enabled wrong: %+v", got)
	}
}

// pg2-qq9v: a query failure must NOT look like "no ready work" — it must propagate.
func TestDiscover_queryErrorPropagates(t *testing.T) {
	sentinel := errors.New("bd down")
	rs := roles.RoleSet{{Name: "a", Enabled: true, Query: fakeQuery{err: sentinel}}}
	got, err := Discover(context.Background(), query.Env{}, rs)
	if err == nil {
		t.Fatal("a query error must propagate, not be swallowed as no-work")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("propagated error should wrap the query error; got %v", err)
	}
	if got != nil {
		t.Errorf("on error, dispatches must be nil; got %v", got)
	}
}

func TestForRole_bypassesEnabled(t *testing.T) {
	// ForRole runs the query even when the role is disabled (smoke harness).
	role := roles.Role{Name: "a", Enabled: false, Query: fakeQuery{items: []item.Item{{ID: "z"}}}}
	got, err := ForRole(context.Background(), query.Env{}, role)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Item.ID != "z" {
		t.Fatalf("ForRole should run a disabled role's query; got %+v", got)
	}
}

func TestDispatchContext_Validate(t *testing.T) {
	cases := []struct {
		name     string
		d        DispatchContext
		wantErr  bool
		wantSubs []string
	}{
		{"valid", DispatchContext{Role: roles.Role{Name: "worker"}, Item: item.Item{ID: "zr-1"}}, false, nil},
		{"missing-item", DispatchContext{Role: roles.Role{Name: "worker"}}, true, []string{"item"}},
		{"missing-role", DispatchContext{Item: item.Item{ID: "zr-1"}}, true, []string{"role"}},
		{"missing-both", DispatchContext{}, true, []string{"role", "item"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.d.Validate()
			if tc.wantErr != (err != nil) {
				t.Fatalf("Validate() err=%v, wantErr=%v", err, tc.wantErr)
			}
			for _, sub := range tc.wantSubs {
				if !strings.Contains(err.Error(), sub) {
					t.Errorf("err %q should mention %q", err, sub)
				}
			}
		})
	}
}
