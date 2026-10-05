package scriptout

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestTimeRange_Contains(t *testing.T) {
	since := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	before := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		r    TimeRange
		t    time.Time
		want bool
	}{
		{"open range contains anything", TimeRange{}, before.Add(1000 * time.Hour), true},
		{"since inclusive", TimeRange{Since: since}, since, true},
		{"before since", TimeRange{Since: since}, since.Add(-time.Second), false},
		{"before exclusive", TimeRange{Before: before}, before, false},
		{"just under before", TimeRange{Before: before}, before.Add(-time.Second), true},
		{"inside both", TimeRange{Since: since, Before: before}, since.Add(24 * time.Hour), true},
		{"after both", TimeRange{Since: since, Before: before}, before.Add(time.Hour), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.r.Contains(tt.t); got != tt.want {
				t.Fatalf("Contains = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTimeRange_MergeInto(t *testing.T) {
	since := time.Date(2026, 10, 1, 12, 0, 0, 0, time.FixedZone("x", 3600))
	before := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)

	t.Run("zero range returns config byte-identical", func(t *testing.T) {
		in := json.RawMessage(`{"queries":{"mine":"x"}}`)
		got, err := TimeRange{}.MergeInto(in, ConfigKeyListSince, ConfigKeyListBefore)
		if err != nil || string(got) != string(in) {
			t.Fatalf("got %s, %v; want unchanged", got, err)
		}
		got, err = TimeRange{}.MergeInto(nil, ConfigKeyListSince, ConfigKeyListBefore)
		if err != nil || got != nil {
			t.Fatalf("nil in: got %s, %v; want nil", got, err)
		}
	})
	t.Run("merges onto static block, UTC RFC3339", func(t *testing.T) {
		got, err := TimeRange{Since: since, Before: before}.MergeInto(json.RawMessage(`{"queries":{"mine":"x"}}`), ConfigKeyListSince, ConfigKeyListBefore)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(got, &m); err != nil {
			t.Fatal(err)
		}
		if m[ConfigKeyListSince] != "2026-10-01T11:00:00Z" || m[ConfigKeyListBefore] != "2026-10-05T00:00:00Z" {
			t.Fatalf("keys = %v", m)
		}
		if _, ok := m["queries"]; !ok {
			t.Fatalf("static block dropped: %v", m)
		}
	})
	t.Run("only since adds only the since key", func(t *testing.T) {
		got, err := TimeRange{Since: since}.MergeInto(nil, ConfigKeySearchSince, ConfigKeySearchBefore)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		_ = json.Unmarshal(got, &m)
		if _, ok := m[ConfigKeySearchBefore]; ok || m[ConfigKeySearchSince] == nil {
			t.Fatalf("keys = %v", m)
		}
	})
	t.Run("null config treated as empty object", func(t *testing.T) {
		got, err := TimeRange{Before: before}.MergeInto(json.RawMessage(`null`), ConfigKeyListSince, ConfigKeyListBefore)
		if err != nil || string(got) != `{"list_before":"2026-10-05T00:00:00Z"}` {
			t.Fatalf("got %s, %v", got, err)
		}
	})
	t.Run("non-object config is an error", func(t *testing.T) {
		if _, err := (TimeRange{Since: since}).MergeInto(json.RawMessage(`[1]`), ConfigKeyListSince, ConfigKeyListBefore); err == nil {
			t.Fatal("expected error")
		}
	})
}

func TestRangeFromConfig(t *testing.T) {
	t.Run("absent keys and empty config are open", func(t *testing.T) {
		for _, c := range []json.RawMessage{nil, json.RawMessage(`{}`), json.RawMessage(`{"queries":{}}`)} {
			r, err := RangeFromConfig(c, ConfigKeyListSince, ConfigKeyListBefore)
			if err != nil || !r.IsZero() {
				t.Fatalf("config %s: %+v, %v", c, r, err)
			}
		}
	})
	t.Run("reads both keys", func(t *testing.T) {
		r, err := RangeFromConfig(json.RawMessage(`{"list_since":"2026-10-01T00:00:00Z","list_before":"2026-10-05T00:00:00Z"}`), ConfigKeyListSince, ConfigKeyListBefore)
		if err != nil || r.Since.IsZero() || r.Before.IsZero() {
			t.Fatalf("%+v, %v", r, err)
		}
	})
	t.Run("search keys are independent of list keys", func(t *testing.T) {
		r, err := RangeFromConfig(json.RawMessage(`{"list_since":"2026-10-01T00:00:00Z"}`), ConfigKeySearchSince, ConfigKeySearchBefore)
		if err != nil || !r.IsZero() {
			t.Fatalf("%+v, %v", r, err)
		}
	})
	t.Run("malformed value errors", func(t *testing.T) {
		for _, c := range []string{`{"list_since":"7d"}`, `{"list_since":5}`} {
			if _, err := RangeFromConfig(json.RawMessage(c), ConfigKeyListSince, ConfigKeyListBefore); err == nil {
				t.Fatalf("config %s: expected error", c)
			}
		}
	})
}

func TestRangeFromContext(t *testing.T) {
	ctx := WithConfig(context.Background(), json.RawMessage(`{"list_since":"2026-10-01T00:00:00Z","search_before":"2026-10-05T00:00:00Z"}`))
	lr, err := ListRangeFromContext(ctx)
	if err != nil || lr.Since.IsZero() || !lr.Before.IsZero() {
		t.Fatalf("list: %+v, %v", lr, err)
	}
	sr, err := SearchRangeFromContext(ctx)
	if err != nil || !sr.Since.IsZero() || sr.Before.IsZero() {
		t.Fatalf("search: %+v, %v", sr, err)
	}
	bad := WithConfig(context.Background(), json.RawMessage(`{"list_since":"garbage"}`))
	if _, err := ListRangeFromContext(bad); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("err = %v, want invalid_argument", err)
	}
}
