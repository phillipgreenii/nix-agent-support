// Package timing ranks tools and commands by the time their calls spent
// running: by total time, and by a single run's time (median and p90).
//
// Run time is only the time the tool was running. Claude Code's own
// duration_ms (tracker.post_tool) excludes permission-prompt and hook time
// (design live check 3), so it is the source for every call; a call without
// it falls back to running_ts to call_end_ts. A call with neither is left out
// and counted.
package timing

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"sort"
	"strings"
)

// Options selects which calls count.
type Options struct {
	// IncludeFailed also counts calls that errored or were interrupted.
	IncludeFailed bool
}

// Key identifies a group: a tool (for Bash, a command class), in a repo.
type Key struct {
	Tool  string `json:"tool"`
	Class string `json:"class,omitempty"`
	Repo  string `json:"repo"`
}

// Group is the statistics of one group; times are milliseconds.
type Group struct {
	Key
	Runs     int     `json:"runs"`
	TotalMS  int64   `json:"total_ms"`
	MedianMS float64 `json:"median_ms"`
	P90MS    int64   `json:"p90_ms"`
	MaxMS    int64   `json:"max_ms"`
}

// Excluded counts the calls left out of the groups.
type Excluded struct {
	Failed      int `json:"failed"`         // errored (failed_timing = 1)
	Interrupted int `json:"interrupted"`    // result interrupted
	Refused     int `json:"refused"`        // never ran
	NoTime      int `json:"no_usable_time"` // neither duration_ms nor running to call_end
}

// Report is the result of Analyze.
type Report struct {
	Calls    int      `json:"calls"` // calls read
	Used     int      `json:"used"`  // calls in the groups
	Excluded Excluded `json:"excluded"`
	ByTotal  []Group  `json:"by_total"`
	ByP90    []Group  `json:"by_p90"`
}

// Call is one row of the tool_calls view, reduced to what timing needs.
type Call struct {
	Tool, Command, Class, Repo string
	DurationMS                 sql.NullInt64
	RunningTS, CallEndTS       sql.NullInt64
	FailedTiming               sql.NullInt64
	Result                     string
}

// RunTime is the call's run time in ms, and whether one is usable.
func RunTime(c Call) (int64, bool) {
	if c.DurationMS.Valid && c.DurationMS.Int64 >= 0 {
		return c.DurationMS.Int64, true
	}
	if c.RunningTS.Valid && c.CallEndTS.Valid && c.CallEndTS.Int64 >= c.RunningTS.Int64 {
		return c.CallEndTS.Int64 - c.RunningTS.Int64, true
	}
	return 0, false
}

// KeyOf groups a call: its tool; for Bash its ceta.command_class annotation,
// else the first word of the command; and its repo.
func KeyOf(c Call) Key {
	k := Key{Tool: c.Tool, Repo: c.Repo}
	if k.Tool == "" {
		k.Tool = "(unknown)"
	}
	if k.Repo == "" {
		k.Repo = "(none)"
	}
	if c.Tool == "Bash" {
		if cl := strings.TrimSpace(c.Class); cl != "" {
			k.Class = cl
		} else if f := strings.Fields(c.Command); len(f) > 0 {
			k.Class = f[0]
		}
	}
	return k
}

// Percentile is the nearest-rank percentile (0 < p <= 100) of sorted
// ascending values: the value at rank ceil(p/100*n).
func Percentile(sorted []int64, p float64) int64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	r := int(math.Ceil(p / 100 * float64(n)))
	if r < 1 {
		r = 1
	}
	if r > n {
		r = n
	}
	return sorted[r-1]
}

// Median is the middle value, or the mean of the two middle values for an even
// count, of sorted ascending values.
func Median(sorted []int64) float64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	if n%2 == 1 {
		return float64(sorted[n/2])
	}
	return float64(sorted[n/2-1]+sorted[n/2]) / 2
}

// Analyze groups the calls and builds both rankings.
func Analyze(calls []Call, opt Options) *Report {
	r := &Report{Calls: len(calls)}
	times := map[Key][]int64{}
	for _, c := range calls {
		switch {
		case c.Result == "refused":
			r.Excluded.Refused++
			continue
		case c.Result == "interrupted" && !opt.IncludeFailed:
			r.Excluded.Interrupted++
			continue
		case c.FailedTiming.Valid && c.FailedTiming.Int64 == 1 && !opt.IncludeFailed:
			r.Excluded.Failed++
			continue
		}
		ms, ok := RunTime(c)
		if !ok {
			r.Excluded.NoTime++
			continue
		}
		k := KeyOf(c)
		times[k] = append(times[k], ms)
		r.Used++
	}
	for k, ts := range times {
		sort.Slice(ts, func(i, j int) bool { return ts[i] < ts[j] })
		var total int64
		for _, v := range ts {
			total += v
		}
		r.ByTotal = append(r.ByTotal, Group{
			Key: k, Runs: len(ts), TotalMS: total,
			MedianMS: Median(ts), P90MS: Percentile(ts, 90), MaxMS: ts[len(ts)-1],
		})
	}
	byKey := func(a, b Group) bool {
		if a.Tool != b.Tool {
			return a.Tool < b.Tool
		}
		if a.Class != b.Class {
			return a.Class < b.Class
		}
		return a.Repo < b.Repo
	}
	r.ByP90 = append([]Group(nil), r.ByTotal...)
	sort.Slice(r.ByTotal, func(i, j int) bool {
		a, b := r.ByTotal[i], r.ByTotal[j]
		if a.TotalMS != b.TotalMS {
			return a.TotalMS > b.TotalMS
		}
		return byKey(a, b)
	})
	sort.Slice(r.ByP90, func(i, j int) bool {
		a, b := r.ByP90[i], r.ByP90[j]
		if a.P90MS != b.P90MS {
			return a.P90MS > b.P90MS
		}
		if a.TotalMS != b.TotalMS {
			return a.TotalMS > b.TotalMS
		}
		return byKey(a, b)
	})
	return r
}

// Load reads every call through the published views (tool_calls and
// annotations) of a database opened read-only.
func Load(db *sql.DB) ([]Call, error) {
	rows, err := db.QueryContext(context.Background(), `SELECT COALESCE(t.tool_name,''), COALESCE(json_extract(t.tool_input,'$.command'),''),
		COALESCE((SELECT json_extract(a.data,'$.class') FROM annotations a
			WHERE a.call_id = t.call_id AND a.kind = 'ceta.command_class' ORDER BY a.ts DESC LIMIT 1),''),
		COALESCE(t.repo,''), t.duration_ms, t.running_ts, t.call_end_ts, t.failed_timing, COALESCE(t.result,'')
		FROM tool_calls t`)
	if err != nil {
		return nil, fmt.Errorf("reading tool_calls: %w", err)
	}
	defer rows.Close()
	var out []Call
	for rows.Next() {
		var c Call
		if err := rows.Scan(&c.Tool, &c.Command, &c.Class, &c.Repo, &c.DurationMS, &c.RunningTS, &c.CallEndTS, &c.FailedTiming, &c.Result); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
