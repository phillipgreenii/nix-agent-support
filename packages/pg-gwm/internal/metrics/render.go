// Package metrics builds the pg-gwm metric set. The manager is a set of short
// CLI invocations with no daemon, so every value is read at scrape time from
// the files they leave behind: each repository's state.json, the user-level
// counters.json and reaper.json, the registry and the configuration. Nothing
// here takes a repository lock.
package metrics

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/phillipgreenii/pg-gwm/internal/state"
)

// Prefix is the metric namespace.
const Prefix = "pg_gwm_"

// Statuses are the worktree statuses a gauge series is always exported for, so
// a status with no worktrees reads 0 instead of vanishing.
var Statuses = []string{
	string(state.Preparing), string(state.Leased), string(state.LeaseExpired), string(state.Released),
	string(state.Available), string(state.Creating), string(state.Reclaiming), string(state.Broken),
}

// Phases are the lease phases a histogram is exported for.
var Phases = []string{"wait", "create", "switch"}

// baselineRefusals are exported at 0 for every repository, so increase() over
// the first refusal has a starting point.
var baselineRefusals = []string{"max-reached", "busy", "internal"}

// Repo is one repository's series. A nil pointer omits its series.
type Repo struct {
	Label string
	// Unreachable is set for a repository that cannot be opened; only
	// pg_gwm_repo_unreachable and the reap age are then exported.
	Unreachable bool
	// HaveState is false when state.json could not be read: the state-derived
	// series are omitted.
	HaveState      bool
	Worktrees      map[string]int
	FreeBytes      *uint64
	FloorBytes     *uint64
	OldestExpired  float64
	OldestNoExpiry float64
	Preserved      *int
	Broken         int
	LastReapAge    *float64
	Counters       *state.RepoCounters
}

// Snapshot is everything one scrape exports.
type Snapshot struct {
	Version string
	Repos   []Repo
	// ReaperLastSuccessAge is nil when the reaper never succeeded.
	ReaperLastSuccessAge *float64
	ConfigValid          bool
	// Unmanaged is the last audit count of worktrees that are neither
	// pg-gwm-managed nor excepted; nil when no audit has written a count.
	Unmanaged *int
}

func num(f float64) string { return strconv.FormatFloat(f, 'g', -1, 64) }

type writer struct {
	b    strings.Builder
	seen map[string]bool
}

func (w *writer) head(name, help, typ string) {
	if w.seen[name] {
		return
	}
	w.seen[name] = true
	fmt.Fprintf(&w.b, "# HELP %s%s %s\n# TYPE %s%s %s\n", Prefix, name, help, Prefix, name, typ)
}

func (w *writer) line(name, labels, val string) {
	if labels != "" {
		labels = "{" + labels + "}"
	}
	fmt.Fprintf(&w.b, "%s%s%s %s\n", Prefix, name, labels, val)
}

func rl(label string) string { return "repo=" + strconv.Quote(label) }

// Render returns the Prometheus text exposition of s.
func Render(s Snapshot) string {
	w := &writer{seen: map[string]bool{}}
	repos := append([]Repo(nil), s.Repos...)
	sort.Slice(repos, func(i, j int) bool { return repos[i].Label < repos[j].Label })

	w.head("build_info", "Build information; always 1.", "gauge")
	w.line("build_info", "version="+strconv.Quote(s.Version), "1")
	w.head("config_valid", "1 when the configuration file parsed, 0 when it did not.", "gauge")
	cv := "0"
	if s.ConfigValid {
		cv = "1"
	}
	w.line("config_valid", "", cv)
	if s.ReaperLastSuccessAge != nil {
		w.head("reaper_last_success_age_seconds", "Seconds since the last successful reaper run.", "gauge")
		w.line("reaper_last_success_age_seconds", "", num(*s.ReaperLastSuccessAge))
	}

	if s.Unmanaged != nil {
		w.head("unmanaged_worktrees", "Worktrees the last weekly audit found neither pg-gwm-managed nor excepted.", "gauge")
		w.line("unmanaged_worktrees", "", strconv.Itoa(*s.Unmanaged))
	}

	gaugeRepo := func(name, help string, emit func(r Repo) (string, bool)) {
		for _, r := range repos {
			if v, ok := emit(r); ok {
				w.head(name, help, "gauge")
				w.line(name, rl(r.Label), v)
			}
		}
	}
	for _, r := range repos {
		if r.HaveState && !r.Unreachable {
			w.head("worktrees", "Managed worktrees by status.", "gauge")
			for _, st := range Statuses {
				w.line("worktrees", rl(r.Label)+",status="+strconv.Quote(st), strconv.Itoa(r.Worktrees[st]))
			}
		}
	}
	hasState := func(r Repo) bool { return r.HaveState && !r.Unreachable }
	gaugeRepo("free_bytes", "Free bytes on the volume holding the repository's worktree-root.", func(r Repo) (string, bool) {
		if r.FreeBytes == nil || r.Unreachable {
			return "", false
		}
		return strconv.FormatUint(*r.FreeBytes, 10), true
	})
	gaugeRepo("free_space_floor_bytes", "The repository's free-space-floor, when configured.", func(r Repo) (string, bool) {
		if r.FloorBytes == nil || r.Unreachable {
			return "", false
		}
		return strconv.FormatUint(*r.FloorBytes, 10), true
	})
	gaugeRepo("oldest_expired_lease_age_seconds", "Seconds since the oldest lease-expired lease expired; 0 when none.", func(r Repo) (string, bool) {
		return num(r.OldestExpired), hasState(r)
	})
	gaugeRepo("oldest_no_expiry_lease_age_seconds", "Age of the oldest lease with no expiry; 0 when none.", func(r Repo) (string, bool) {
		return num(r.OldestNoExpiry), hasState(r)
	})
	gaugeRepo("preserved_branches", "Branches under pg-gwm/dirty-reaped-.", func(r Repo) (string, bool) {
		if r.Preserved == nil || r.Unreachable {
			return "", false
		}
		return strconv.Itoa(*r.Preserved), true
	})
	gaugeRepo("broken_worktrees", "Worktrees whose status is broken.", func(r Repo) (string, bool) {
		return strconv.Itoa(r.Broken), hasState(r)
	})
	gaugeRepo("last_reap_age_seconds", "Seconds since the repository's last reap that wrote its reaper entry.", func(r Repo) (string, bool) {
		if r.LastReapAge == nil {
			return "", false
		}
		return num(*r.LastReapAge), true
	})
	gaugeRepo("repo_unreachable", "1 when the repository cannot be opened, else 0.", func(r Repo) (string, bool) {
		if r.Unreachable {
			return "1", true
		}
		return "0", true
	})

	counter := func(name, help string, val func(c *state.RepoCounters) int64) {
		for _, r := range repos {
			if r.Unreachable {
				continue
			}
			var n int64
			if r.Counters != nil {
				n = val(r.Counters)
			}
			w.head(name, help, "counter")
			w.line(name, rl(r.Label), strconv.FormatInt(n, 10))
		}
	}
	counter("reap_removed_total", "Worktrees removed by reap.", func(c *state.RepoCounters) int64 { return c.ReapRemoved })
	counter("reap_kept_total", "Worktrees reap kept.", func(c *state.RepoCounters) int64 { return c.ReapKept })

	for _, r := range repos {
		if r.Unreachable {
			continue
		}
		names := map[string]bool{}
		for _, n := range baselineRefusals {
			names[n] = true
		}
		if r.Counters != nil {
			for n := range r.Counters.Refusals {
				names[n] = true
			}
		}
		sorted := make([]string, 0, len(names))
		for n := range names {
			sorted = append(sorted, n)
		}
		sort.Strings(sorted)
		w.head("refusals_total", "Refused commands by error name.", "counter")
		for _, n := range sorted {
			var v int64
			if r.Counters != nil {
				v = r.Counters.Refusals[n]
			}
			w.line("refusals_total", rl(r.Label)+",error="+strconv.Quote(n), strconv.FormatInt(v, 10))
		}
	}

	for _, r := range repos {
		if r.Unreachable {
			continue
		}
		w.head("lease_seconds", "Seconds a lease command spent in each phase.", "histogram")
		for _, ph := range Phases {
			var h state.Histogram
			if r.Counters != nil && r.Counters.LeasePhases[ph] != nil {
				h = *r.Counters.LeasePhases[ph]
			}
			lbl := rl(r.Label) + ",phase=" + strconv.Quote(ph)
			for i, ub := range state.LeasePhaseBuckets {
				var c int64
				if i < len(h.Buckets) {
					c = h.Buckets[i]
				}
				w.line("lease_seconds_bucket", lbl+",le="+strconv.Quote(num(ub)), strconv.FormatInt(c, 10))
			}
			w.line("lease_seconds_bucket", lbl+",le=\"+Inf\"", strconv.FormatInt(h.Count, 10))
			w.line("lease_seconds_sum", lbl, num(h.Sum))
			w.line("lease_seconds_count", lbl, strconv.FormatInt(h.Count, 10))
		}
	}
	return w.b.String()
}
