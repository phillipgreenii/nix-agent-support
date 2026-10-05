package internal

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

type fakeSearchRunner struct {
	fakeRunner
	searchJSON string
	searchErr  error
	// gotRange/searchCalls record what the backend forwarded.
	gotRange    scriptout.TimeRange
	searchCalls int
}

func (f *fakeSearchRunner) Search(_ context.Context, _ string, _ string, rng scriptout.TimeRange) ([]byte, error) {
	f.searchCalls++
	f.gotRange = rng
	return []byte(f.searchJSON), f.searchErr
}

func TestBackend_Search(t *testing.T) {
	r := &fakeSearchRunner{searchJSON: `{"query":"flaky","matches":[{"session_id":"s1","role":"assistant","line":2,"snippet":"the payments test is flaky"}]}`}
	b := New(r)
	got, err := b.Search(context.Background(), "flaky", nil)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 1 || got[0].ID != "s1" || got[0].Type != "agentsession" {
		t.Errorf("got %+v", got)
	}
}

func TestBackend_Search_NoMatches(t *testing.T) {
	r := &fakeSearchRunner{searchJSON: `{"query":"nope","matches":[]}`}
	b := New(r)
	got, err := b.Search(context.Background(), "nope", nil)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %+v, want none", got)
	}
}

func TestBackend_Search_RunnerError(t *testing.T) {
	r := &fakeSearchRunner{searchErr: errors.New("daemon unreachable")}
	b := New(r)
	if _, err := b.Search(context.Background(), "x", nil); err == nil {
		t.Fatal("expected an error")
	}
}

func TestBackend_Search_ForwardsRangeFromConfig(t *testing.T) {
	r := &fakeSearchRunner{searchJSON: `{"query":"flaky","matches":[]}`}
	ctx := scriptout.WithConfig(context.Background(), json.RawMessage(`{"search_since":"2026-10-01T00:00:00Z","search_before":"2026-10-05T00:00:00Z"}`))
	if _, err := New(r).Search(ctx, "flaky", nil); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if !r.gotRange.Since.Equal(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)) || !r.gotRange.Before.Equal(time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("forwarded range = %+v", r.gotRange)
	}
}

func TestBackend_Search_NoConfigKeys_Unbounded(t *testing.T) {
	r := &fakeSearchRunner{searchJSON: `{"query":"flaky","matches":[]}`}
	// A list_* key must NOT be mistaken for a search bound.
	ctx := scriptout.WithConfig(context.Background(), json.RawMessage(`{"list_since":"2026-10-01T00:00:00Z"}`))
	for _, c := range []context.Context{context.Background(), ctx} {
		if _, err := New(r).Search(c, "flaky", nil); err != nil {
			t.Fatalf("Search: %v", err)
		}
		if !r.gotRange.IsZero() {
			t.Fatalf("forwarded range = %+v, want unbounded", r.gotRange)
		}
	}
}

func TestBackend_Search_MalformedBound_InvalidArgumentWithoutExec(t *testing.T) {
	r := &fakeSearchRunner{searchJSON: `{"matches":[]}`}
	ctx := scriptout.WithConfig(context.Background(), json.RawMessage(`{"search_since":"7d"}`))
	_, err := New(r).Search(ctx, "flaky", nil)
	if !errors.Is(err, scriptout.ErrInvalidArgument) {
		t.Fatalf("err = %v, want invalid_argument", err)
	}
	if r.searchCalls != 0 {
		t.Fatal("pa-monitor was exec'd despite a malformed bound")
	}
}

func TestSearchArgs(t *testing.T) {
	since := time.Date(2026, 10, 1, 2, 0, 0, 0, time.FixedZone("x", 3600))
	before := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		sess string
		rng  scriptout.TimeRange
		want []string
	}{
		{"unbounded", "", scriptout.TimeRange{}, []string{"search", "q"}},
		{"session only", "s1", scriptout.TimeRange{}, []string{"search", "q", "--session", "s1"}},
		{
			"both as RFC3339 UTC", "",
			scriptout.TimeRange{Since: since, Before: before},
			[]string{"search", "q", "--since", "2026-10-01T01:00:00Z", "--before", "2026-10-05T00:00:00Z"},
		},
		{"before only", "", scriptout.TimeRange{Before: before}, []string{"search", "q", "--before", "2026-10-05T00:00:00Z"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := searchArgs("q", tt.sess, tt.rng); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}
