package trackercompare

import (
	"fmt"
	"time"
)

// parseCreatedAt reads the ask log's created_at (RFC 3339, UTC).
func parseCreatedAt(s string) (int64, error) {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return 0, fmt.Errorf("created_at %q is not RFC 3339: %w", s, err)
	}
	return t.UnixMilli(), nil
}

// ParseTime reads a window bound: an RFC 3339 timestamp, a YYYY-MM-DD date
// (UTC midnight), or a duration such as 48h meaning that long before now.
func ParseTime(s string, now time.Time) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t, nil
	}
	if d, err := time.ParseDuration(s); err == nil && d >= 0 {
		return now.Add(-d), nil
	}
	return time.Time{}, fmt.Errorf("%q: want an RFC 3339 timestamp, a YYYY-MM-DD date or a duration such as 48h", s)
}
