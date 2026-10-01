package alert

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

type fakeProvider struct {
	showFn    func(ctx context.Context, id string) (*schema.Alert, error)
	listFn    func(ctx context.Context, q schema.QueryExpr, idsOnly bool) (*schema.AlertListResult, error)
	historyFn func(ctx context.Context, since, until time.Time, q schema.QueryExpr) (*schema.AlertHistoryResult, error)
}

var _ Provider = (*fakeProvider)(nil)

func (f *fakeProvider) Show(ctx context.Context, id string) (*schema.Alert, error) {
	return f.showFn(ctx, id)
}

func (f *fakeProvider) List(ctx context.Context, q schema.QueryExpr, idsOnly bool) (*schema.AlertListResult, error) {
	return f.listFn(ctx, q, idsOnly)
}

func (f *fakeProvider) ListHistory(ctx context.Context, since, until time.Time, q schema.QueryExpr) (*schema.AlertHistoryResult, error) {
	return f.historyFn(ctx, since, until, q)
}

type fakeWithAuth struct {
	fakeProvider
	err error
}

func (f *fakeWithAuth) CheckAuth(context.Context) error { return f.err }

func TestShow_PassesIDAndErrorsThrough(t *testing.T) {
	p := &fakeProvider{showFn: func(_ context.Context, id string) (*schema.Alert, error) {
		if id == "grafana:x" {
			return &schema.Alert{ID: id}, nil
		}
		return nil, scriptout.WrapError(scriptout.ErrNotFound, "not firing")
	}}
	tbl := NewDispatchTable(p)
	if tbl["show"].SchemaVersion != schema.AlertSchemaVersion {
		t.Fatal("schema version")
	}
	res, err := tbl["show"].Handle(context.Background(), json.RawMessage(`{"id":"grafana:x"}`))
	if err != nil || res.(*schema.Alert).ID != "grafana:x" {
		t.Fatalf("res=%v err=%v", res, err)
	}
	_, err = tbl["show"].Handle(context.Background(), json.RawMessage(`{"id":"nope"}`))
	if !errors.Is(err, scriptout.ErrNotFound) {
		t.Fatalf("err=%v", err)
	}
	_, err = tbl["show"].Handle(context.Background(), json.RawMessage(`{"id":`))
	if !errors.Is(err, scriptout.ErrInvalidArgument) {
		t.Fatalf("err=%v", err)
	}
}

func TestList_NoQueryMeansUnfiltered(t *testing.T) {
	var gotQ schema.QueryExpr
	called := false
	p := &fakeProvider{listFn: func(_ context.Context, q schema.QueryExpr, ids bool) (*schema.AlertListResult, error) {
		called, gotQ = true, q
		if !ids {
			t.Error("ids_only not passed")
		}
		return &schema.AlertListResult{}, nil
	}}
	// No config at all: an empty query must still work.
	if _, err := NewDispatchTable(p)["list"].Handle(context.Background(), json.RawMessage(`{"ids_only":true}`)); err != nil {
		t.Fatal(err)
	}
	if !called || len(gotQ) != 0 {
		t.Fatalf("called=%v q=%v", called, gotQ)
	}
}

func TestList_NamedQueryResolvedAndUnknownRejected(t *testing.T) {
	var gotQ schema.QueryExpr
	p := &fakeProvider{listFn: func(_ context.Context, q schema.QueryExpr, _ bool) (*schema.AlertListResult, error) {
		gotQ = q
		return &schema.AlertListResult{}, nil
	}}
	ctx := scriptout.WithConfig(context.Background(), json.RawMessage(`{"queries":{"crit":["{severity=\"critical\"}","{severity=\"error\"}"]}}`))
	tbl := NewDispatchTable(p)
	if _, err := tbl["list"].Handle(ctx, json.RawMessage(`{"query":"crit"}`)); err != nil {
		t.Fatal(err)
	}
	if len(gotQ) != 2 {
		t.Fatalf("q=%v", gotQ)
	}
	_, err := tbl["list"].Handle(ctx, json.RawMessage(`{"query":"bogus"}`))
	if !errors.Is(err, scriptout.ErrQueryNotRecognized) {
		t.Fatalf("err=%v", err)
	}
}

func TestListHistory_ParsesWindowAndQuery(t *testing.T) {
	var gs, gu time.Time
	var gq schema.QueryExpr
	p := &fakeProvider{historyFn: func(_ context.Context, s, u time.Time, q schema.QueryExpr) (*schema.AlertHistoryResult, error) {
		gs, gu, gq = s, u, q
		return &schema.AlertHistoryResult{}, nil
	}}
	ctx := scriptout.WithConfig(context.Background(), json.RawMessage(`{"queries":{"q":"{}"}}`))
	tbl := NewDispatchTable(p)
	_, err := tbl["list_history"].Handle(ctx, json.RawMessage(`{"since":"2026-10-01T00:00:00Z","until":"2026-10-02T00:00:00Z","query":"q"}`))
	if err != nil {
		t.Fatal(err)
	}
	if gs.Day() != 1 || gu.Day() != 2 || len(gq) != 1 {
		t.Fatalf("s=%v u=%v q=%v", gs, gu, gq)
	}
	for _, bad := range []string{
		`{"since":"x","until":"2026-10-02T00:00:00Z"}`,
		`{"since":"2026-10-01T00:00:00Z","until":"y"}`,
	} {
		if _, err := tbl["list_history"].Handle(ctx, json.RawMessage(bad)); !errors.Is(err, scriptout.ErrInvalidArgument) {
			t.Errorf("%s: err=%v", bad, err)
		}
	}
	if _, err := tbl["list_history"].Handle(ctx, json.RawMessage(`{"since":"2026-10-01T00:00:00Z","until":"2026-10-02T00:00:00Z","query":"zz"}`)); !errors.Is(err, scriptout.ErrQueryNotRecognized) {
		t.Errorf("err=%v", err)
	}
}

func TestAuthStatusOnlyWhenAuthChecker(t *testing.T) {
	if _, ok := NewDispatchTable(&fakeProvider{})[scriptout.OpAuthStatus]; ok {
		t.Fatal("auth_status present without AuthChecker")
	}
	tbl := NewDispatchTable(&fakeWithAuth{err: errors.New("no key")})
	res, err := tbl[scriptout.OpAuthStatus].Handle(context.Background(), nil)
	if err != nil || res.(scriptout.AuthStatus).State != scriptout.AuthMissing {
		t.Fatalf("res=%v err=%v", res, err)
	}
	tbl = NewDispatchTable(&fakeWithAuth{})
	res, _ = tbl[scriptout.OpAuthStatus].Handle(context.Background(), nil)
	if res.(scriptout.AuthStatus).State != scriptout.AuthOK {
		t.Fatalf("res=%v", res)
	}
}
