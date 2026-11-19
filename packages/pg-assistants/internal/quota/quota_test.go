package quota

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-assistants/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/client"
)

var pts = config.Points{Nudge: 0.8, Interrupt: 0.95, Kill: 1}

type src map[string]Tokens

func (s src) Totals(context.Context, string, time.Time) (map[string]Tokens, error) { return s, nil }

func TestTokensQuotaSumsEveryCounter(t *testing.T) {
	q := config.Quota{Tokens: 2000, Window: time.Hour, Points: pts}
	r, err := Read(t.Context(), src{
		"a": {Input: 1, Output: 2, CacheCreation: 3, CacheRead: 4},
		"b": {Output: 90},
	}, "/x", q, time.Unix(0, 0))
	if err != nil {
		t.Fatal(err)
	}
	if r.Used != 100 || r.Meter.Fraction != 0.05 || r.Meter.Name != "quota" || r.Meter.Detail != "100 of 2000 tokens in 1h0m0s" {
		t.Errorf("reading = %+v", r)
	}
}

func TestDollarsQuotaPricesPerModel(t *testing.T) {
	q := config.Quota{Dollars: 5, Window: 24 * time.Hour, Points: pts}
	// sonnet-5-5: in 2, out 10, cc 2.50, cr 0.20 per million; opus-5-5: out 20.
	r, err := Read(t.Context(), src{
		"claude-sonnet-5-5": {Input: 1_000_000, Output: 100_000, CacheCreation: 200_000, CacheRead: 5_000_000},
		"claude-opus-5-5":   {Output: 50_000},
	}, "/x", q, time.Unix(0, 0))
	if err != nil {
		t.Fatal(err)
	}
	// 2 + 1 + 0.5 + 1 = 4.5 for sonnet; 1 for opus.
	if math.Abs(r.Used-5.5) > 1e-9 || math.Abs(r.Meter.Fraction-1.1) > 1e-9 || r.Meter.Detail != "$5.50 of $5.00 in 24h0m0s" {
		t.Errorf("reading = %+v, want $5.50 of $5.00", r)
	}
}

func TestUnknownModelPricesAtTheDearestTierAndIgnoresTheSuffix(t *testing.T) {
	if got := Price("claude-novel-9").Output; got != 25 {
		t.Errorf("unknown model output price = %v, want 25", got)
	}
	if got := Price("claude-sonnet-5-5[1m]").Output; got != 10 {
		t.Errorf("[1m] sonnet output price = %v, want 10", got)
	}
}

func TestReadPassesTheWindowStart(t *testing.T) {
	var gotDir string
	var gotSince time.Time
	s := sourceFunc(func(_ context.Context, dir string, since time.Time) (map[string]Tokens, error) {
		gotDir, gotSince = dir, since
		return nil, nil
	})
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	if _, err := Read(t.Context(), s, "/work/a", config.Quota{Tokens: 10, Window: 2 * time.Hour, Points: pts}, now); err != nil {
		t.Fatal(err)
	}
	if gotDir != "/work/a" || !gotSince.Equal(now.Add(-2*time.Hour)) {
		t.Errorf("dir %q since %v", gotDir, gotSince)
	}
	boom := errors.New("boom")
	if _, err := Read(t.Context(), sourceFunc(func(context.Context, string, time.Time) (map[string]Tokens, error) { return nil, boom }), "/x",
		config.Quota{Tokens: 10, Window: time.Hour, Points: pts}, now); !errors.Is(err, boom) {
		t.Errorf("err = %v, want boom", err)
	}
}

type sourceFunc func(context.Context, string, time.Time) (map[string]Tokens, error)

func (f sourceFunc) Totals(ctx context.Context, d string, s time.Time) (map[string]Tokens, error) {
	return f(ctx, d, s)
}

type fakeQuerier struct {
	req client.QueryRequest
	ans *client.QueryAnswer
	err error
}

func (f *fakeQuerier) Query(_ context.Context, req client.QueryRequest) (*client.QueryAnswer, error) {
	f.req = req
	return f.ans, f.err
}

func TestRecorderBuildsOneDedupedGroupedQuery(t *testing.T) {
	f := &fakeQuerier{ans: &client.QueryAnswer{Rows: [][]any{
		{"claude-sonnet-5-5", float64(10), float64(20), float64(30), float64(40)},
		{"claude-opus-5-5", json.Number("1"), json.Number("2"), int64(3), nil},
	}}}
	since := time.Date(2026, 11, 18, 11, 0, 0, 0, time.UTC)
	got, err := Recorder{C: f}.Totals(t.Context(), "/work/a", since)
	if err != nil {
		t.Fatal(err)
	}
	if want := (Tokens{10, 20, 30, 40}); got["claude-sonnet-5-5"] != want {
		t.Errorf("sonnet = %+v, want %+v", got["claude-sonnet-5-5"], want)
	}
	if want := (Tokens{1, 2, 3, 0}); got["claude-opus-5-5"] != want {
		t.Errorf("opus = %+v, want %+v", got["claude-opus-5-5"], want)
	}

	if len(f.req.Group) != 1 || f.req.Group[0] != "model" {
		t.Errorf("group = %v, want [model]", f.req.Group)
	}
	for _, a := range f.req.Aggregate {
		if a.Fn != "sum" || a.Distinct != "message.id" {
			t.Errorf("aggregate %+v must be a sum distinct by message.id", a)
		}
	}
	var under, rng bool
	for _, w := range f.req.Where.All {
		switch {
		case w.Field == "cwd" && w.Under == "/work/a":
			under = true
		case w.Field == "time" && w.Range != nil && w.Range.Gte == "2026-11-18T11:00:00Z":
			rng = true
		}
	}
	if !under || !rng {
		t.Errorf("where = %+v, want cwd under /work/a and time >= the window start", f.req.Where)
	}
}

func TestRecorderErrors(t *testing.T) {
	if _, err := (Recorder{C: &fakeQuerier{err: errors.New("down")}}).Totals(t.Context(), "/x", time.Now()); err == nil {
		t.Error("a query error must be returned")
	}
	if _, err := (Recorder{C: &fakeQuerier{ans: &client.QueryAnswer{Degraded: true}}}).Totals(t.Context(), "/x", time.Now()); err == nil {
		t.Error("a degraded answer must be an error, not a low reading")
	}
	if _, err := (Recorder{C: &fakeQuerier{ans: &client.QueryAnswer{Rows: [][]any{{"m"}}}}}).Totals(t.Context(), "/x", time.Now()); err == nil {
		t.Error("a short row must be an error")
	}
}
