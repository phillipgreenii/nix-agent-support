// timerange.go: the per-call time-range knobs the umbrella delivers to a
// backend through the request's opaque Config member (bead pg2-ttk9t, work-tracker
// design WT-D18; search flavor from the 2026-09-18 search time-bound design).
//
// The umbrella parses --since/--before (RFC3339, Go duration, or <N>d) ONCE
// and merges the resulting RFC3339 instants onto the backend's static
// backends.<name> block under list_since/list_before (the "list" op) or
// search_since/search_before (the "search" op). A backend NEVER sees a raw
// "7d": the keys always hold RFC3339 instants. The key prefixes avoid
// collision with the attention_* keys in the same block. A key is present
// ONLY when its flag was given, so an unbounded call's config is untouched.
//
// Semantics: Since is inclusive, Before is exclusive, and a zero value means
// "open on that side". A backend that never reads these keys silently
// returns an unbounded result (the freedom boundary --help documents).
package scriptout

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// Config keys carrying a per-call time range. See this file's header.
const (
	ConfigKeyListSince    = "list_since"
	ConfigKeyListBefore   = "list_before"
	ConfigKeySearchSince  = "search_since"
	ConfigKeySearchBefore = "search_before"
)

// TimeRange is a half-open [Since, Before) window; a zero side is open.
type TimeRange struct {
	Since  time.Time
	Before time.Time
}

// IsZero reports whether neither side is set.
func (r TimeRange) IsZero() bool { return r.Since.IsZero() && r.Before.IsZero() }

// Contains reports whether t falls inside the window (Since inclusive,
// Before exclusive; an open side never excludes).
func (r TimeRange) Contains(t time.Time) bool {
	if !r.Since.IsZero() && t.Before(r.Since) {
		return false
	}
	if !r.Before.IsZero() && !t.Before(r.Before) {
		return false
	}
	return true
}

// MergeInto returns config (an opaque backend config block; nil/empty/JSON
// null is treated as an empty object) with sinceKey/beforeKey set to r's
// sides as RFC3339 UTC instants. A zero side adds no key. When r.IsZero()
// the config is returned UNCHANGED (same bytes), so an unbounded call stays
// byte-identical to today's. A non-object, non-null config is an error.
func (r TimeRange) MergeInto(config json.RawMessage, sinceKey, beforeKey string) (json.RawMessage, error) {
	if r.IsZero() {
		return config, nil
	}
	obj := map[string]json.RawMessage{}
	if len(config) > 0 && string(config) != "null" {
		if err := json.Unmarshal(config, &obj); err != nil {
			return nil, fmt.Errorf("merge time range: backend config is not a JSON object: %w", err)
		}
	}
	put := func(key string, t time.Time) error {
		if t.IsZero() {
			return nil
		}
		raw, err := json.Marshal(t.UTC().Format(time.RFC3339))
		if err != nil {
			return err
		}
		obj[key] = raw
		return nil
	}
	if err := put(sinceKey, r.Since); err != nil {
		return nil, err
	}
	if err := put(beforeKey, r.Before); err != nil {
		return nil, err
	}
	return json.Marshal(obj)
}

// RangeFromConfig reads sinceKey/beforeKey (RFC3339 strings) out of config.
// An absent key leaves that side open; a present but malformed value is an
// error (a backend MUST NOT silently drop a bound it was asked to apply).
func RangeFromConfig(config json.RawMessage, sinceKey, beforeKey string) (TimeRange, error) {
	var r TimeRange
	if len(config) == 0 {
		return r, nil
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(config, &raw); err != nil {
		// Not an object: no keys to read (other readers of this block own
		// reporting a malformed block).
		return r, nil
	}
	read := func(key string, dst *time.Time) error {
		v, ok := raw[key]
		if !ok {
			return nil
		}
		var s string
		if err := json.Unmarshal(v, &s); err != nil {
			return fmt.Errorf("config %q: not a string: %w", key, err)
		}
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return fmt.Errorf("config %q: not an RFC3339 timestamp: %w", key, err)
		}
		*dst = t
		return nil
	}
	if err := read(sinceKey, &r.Since); err != nil {
		return TimeRange{}, err
	}
	if err := read(beforeKey, &r.Before); err != nil {
		return TimeRange{}, err
	}
	return r, nil
}

// ListRangeFromContext reads the "list" op's list_since/list_before from the
// request config threaded onto ctx. A malformed value is wrapped as
// invalid_argument.
func ListRangeFromContext(ctx context.Context) (TimeRange, error) {
	r, err := RangeFromConfig(ConfigFromContext(ctx), ConfigKeyListSince, ConfigKeyListBefore)
	if err != nil {
		return TimeRange{}, WrapError(ErrInvalidArgument, err.Error())
	}
	return r, nil
}

// SearchRangeFromContext is ListRangeFromContext for the "search" op's
// search_since/search_before keys.
func SearchRangeFromContext(ctx context.Context) (TimeRange, error) {
	r, err := RangeFromConfig(ConfigFromContext(ctx), ConfigKeySearchSince, ConfigKeySearchBefore)
	if err != nil {
		return TimeRange{}, WrapError(ErrInvalidArgument, err.Error())
	}
	return r, nil
}
