package ledger

import (
	"context"
	"sort"
	"strings"
)

// The leading words of a Skipped run's reason that mark an infrastructure
// skip: the pool or its recorder, not the lens or the analyst, ended the run.
// The runner builds its reasons from these prefixes and the metrics classify
// stored reasons by them, so the two cannot drift apart.
const (
	SkipPrefixCapacity   = "ccpool capacity check failed"
	SkipPrefixUsageLimit = "ccpool usage limit reached"
	SkipPrefixBusy       = "ccpool busy"
)

// The reason classes of an infrastructure skip, as the metric labels them.
const (
	SkipClassCapacity   = "capacity"
	SkipClassUsageLimit = "usage_limit"
	SkipClassBusy       = "busy"
)

// SkipClass returns the infrastructure reason class of a skip reason, or ""
// when the skip is not an infrastructure one (pending cap, paused lens).
func SkipClass(reason string) string {
	switch {
	case strings.HasPrefix(reason, SkipPrefixCapacity):
		return SkipClassCapacity
	case strings.HasPrefix(reason, SkipPrefixUsageLimit):
		return SkipClassUsageLimit
	case strings.HasPrefix(reason, SkipPrefixBusy):
		return SkipClassBusy
	}
	return ""
}

// SkipCount is the number of Skipped runs of one (lens, scope) in one
// infrastructure reason class.
type SkipCount struct {
	Lens, Scope, Class string
	N                  int
}

func (l *Ledger) infraSkips(ctx context.Context) ([]SkipCount, error) {
	rows, err := l.db.QueryContext(ctx, `SELECT lens, scope, skip_reason, COUNT(*) FROM run
		WHERE state='Skipped' GROUP BY 1,2,3`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	type key struct{ lens, scope, class string }
	sums := map[key]int{}
	for rows.Next() {
		var lens, scope, reason string
		var n int
		if err := rows.Scan(&lens, &scope, &reason, &n); err != nil {
			return nil, err
		}
		if c := SkipClass(reason); c != "" {
			sums[key{lens, scope, c}] += n
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]SkipCount, 0, len(sums))
	for k, n := range sums {
		out = append(out, SkipCount{Lens: k.lens, Scope: k.scope, Class: k.class, N: n})
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Lens != b.Lens {
			return a.Lens < b.Lens
		}
		if a.Scope != b.Scope {
			return a.Scope < b.Scope
		}
		return a.Class < b.Class
	})
	return out, nil
}
