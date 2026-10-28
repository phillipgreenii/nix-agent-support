package status

import (
	"fmt"
	"strings"
	"time"
)

// Health is the report's verdict.
type Health string

const (
	Healthy  Health = "healthy"
	Degraded Health = "degraded"
	Down     Health = "down"
)

// Exit codes of the status mode, the workspace taxonomy.
const (
	ExitHealthy  = 0
	ExitDegraded = 2
	ExitDown     = 3
)

// The degraded thresholds beyond those the design names; doc.go records them.
const (
	// QueryStaleTTLs is how many of a query's TTLs may pass without a
	// whole-query success before the report is degraded.
	QueryStaleTTLs = 3
	// ConsumerLagRows is the pending-row count at which a consumer is far
	// behind the head.
	ConsumerLagRows = 1000
	// ConsumerOldestUnacked is the age of the oldest unacknowledged row at
	// which a consumer is far behind the head.
	ConsumerOldestUnacked = 24 * time.Hour
)

// Source values of Report.Source.
const (
	SourceDaemon = "daemon"
	SourceLocal  = "local"
)

// Supervisor kinds.
const (
	KindLaunchd = "launchd"
	KindNone    = "none"
)

// Auth states of Auth.State.
const (
	AuthOK              = "ok"
	AuthUnauthenticated = "unauthenticated"
)

// Time is an instant that marshals as RFC 3339; the report holds UTC at whole
// seconds.
type Time = time.Time

// Report is the status document. doc.go describes every field. It carries
// counts only.
type Report struct {
	Health  Health   `json:"health"`
	Reasons []string `json:"reasons,omitempty"`
	Source  string   `json:"source"`

	Version       string `json:"version"`
	BuildID       string `json:"build_id"`
	StartedAt     *Time  `json:"started_at,omitempty"`
	UptimeSeconds *int64 `json:"uptime_seconds,omitempty"`
	StateDir      string `json:"state_dir"`
	Socket        string `json:"socket"`

	Store      Store      `json:"store"`
	Logs       Logs       `json:"logs"`
	Supervisor Supervisor `json:"supervisor"`

	Requests  *Requests  `json:"requests,omitempty"`
	Pause     *Pause     `json:"pause,omitempty"`
	RateLimit *RateLimit `json:"rate_limit,omitempty"`
	Queue     *Queue     `json:"queue,omitempty"`
	Queries   []Query    `json:"queries,omitempty"`
	Auth      *Auth      `json:"auth,omitempty"`
	Changes   *Changes   `json:"changes,omitempty"`
	Tracked   *Tracked   `json:"tracked,omitempty"`
}

// Exit is the process exit code of the verdict: 0 healthy, 2 degraded, 3 down.
// Anything that is neither healthy nor degraded is down.
func (r Report) Exit() int {
	switch r.Health {
	case Healthy:
		return ExitHealthy
	case Degraded:
		return ExitDegraded
	default:
		return ExitDown
	}
}

// Store describes the store file and, from a daemon, the open store.
type Store struct {
	Path          string `json:"path"`
	Present       bool   `json:"present"`
	SizeBytes     int64  `json:"size_bytes"`
	ModifiedAt    *Time  `json:"modified_at,omitempty"`
	AgeSeconds    int64  `json:"age_seconds"`
	SchemaVersion int    `json:"schema_version,omitempty"`
	Identity      string `json:"identity,omitempty"`
}

// Logs are the log paths.
type Logs struct {
	Events string `json:"events"`
	Daemon string `json:"daemon"`
	Client string `json:"client"`
}

// Supervisor is the process supervisor, the probe's answer and the command
// that restarts the daemon.
type Supervisor struct {
	Kind        string `json:"kind"`
	Label       string `json:"label"`
	State       string `json:"state"`
	RestartHint string `json:"restart_hint,omitempty"`
}

// Requests is the request counter and the governor bucket the requests spend
// from.
type Requests struct {
	Total           int64 `json:"total"`
	WindowSpent     int64 `json:"window_spent"`
	BackgroundSpent int64 `json:"background_spent"`
	InFlight        int64 `json:"in_flight"`
	Cap             int64 `json:"cap"`
	WindowSeconds   int64 `json:"window_seconds"`
}

// Pause is the origin pause in force.
type Pause struct {
	Reason string `json:"reason"`
	Until  Time   `json:"until"`
}

// RateLimit is the origin's own rate-limit reading and the floor the daemon
// holds below it. Floor is rate_reserve_requests and is always stated. The other
// fields come from the last reading while it is no older than
// origin.ReadingMaxAge, and each is absent when that response did not carry it
// (absent is never 0): Limit and Remaining are requests per burst window,
// NearLimit is the origin's below-20% flag, Headroom is Remaining less Floor,
// ResetAt is the reset the origin stated (a 429 carries it). BelowFloor is true
// when Headroom is negative. ReadingAgeSeconds and LastStatus (the HTTP status
// of that response) are stated with any reading.
type RateLimit struct {
	Floor             int64  `json:"floor"`
	Limit             *int64 `json:"limit,omitempty"`
	Remaining         *int64 `json:"remaining,omitempty"`
	NearLimit         *bool  `json:"near_limit,omitempty"`
	Headroom          *int64 `json:"headroom,omitempty"`
	BelowFloor        bool   `json:"below_floor,omitempty"`
	ResetAt           *Time  `json:"reset_at,omitempty"`
	Reason            string `json:"reason,omitempty"`
	LastStatus        int    `json:"last_status,omitempty"`
	ReadingAgeSeconds *int64 `json:"reading_age_seconds,omitempty"`
}

// Queue is the scheduler's depth.
type Queue struct {
	Running int          `json:"running"`
	Classes []QueueClass `json:"classes"`
}

// QueueClass is one priority class's queue.
type QueueClass struct {
	Class                int    `json:"class"`
	Name                 string `json:"name"`
	Depth                int    `json:"depth"`
	OldestOverdueSeconds int64  `json:"oldest_overdue_seconds"`
}

// Query is one configured query's freshness. LastSuccessAgeSeconds is set with
// LastSuccessAt; BaselineOpenAgeSeconds is set while BaselineAt is not, and is
// the time since the daemon started waiting for the baseline.
type Query struct {
	Name                   string `json:"name"`
	LastSuccessAt          *Time  `json:"last_success_at,omitempty"`
	LastSuccessAgeSeconds  *int64 `json:"last_success_age_seconds,omitempty"`
	LastError              string `json:"last_error,omitempty"`
	BaselineAt             *Time  `json:"baseline_at,omitempty"`
	BaselineOpenAgeSeconds *int64 `json:"baseline_open_age_seconds,omitempty"`
}

// Auth is the authentication state and the acting identity.
type Auth struct {
	State    string `json:"state"`
	Detail   string `json:"detail,omitempty"`
	Identity string `json:"identity,omitempty"`
}

// Changes is the change engine's part of the report: counts and positions.
// PendingSettle is the number of pending settle rows, never the rows.
type Changes struct {
	HeadSeq                int64      `json:"head_seq"`
	PendingSettle          int        `json:"pending_settle"`
	SettleTimeoutsLastHour int        `json:"settle_timeouts_last_hour"`
	SettleFailuresLastHour int        `json:"settle_failures_last_hour"`
	Consumers              []Consumer `json:"consumers,omitempty"`
}

// Consumer is the position of one stored cursor.
type Consumer struct {
	Consumer                string `json:"consumer"`
	View                    string `json:"view"`
	Scope                   string `json:"scope"`
	AckedSeq                int64  `json:"acked_seq"`
	HeadSeq                 int64  `json:"head_seq"`
	LagRows                 int    `json:"lag_rows"`
	OldestUnackedAgeSeconds int64  `json:"oldest_unacked_age_seconds"`
	SeenAt                  Time   `json:"seen_at"`
}

// Tracked is the tracked count by class and the watched count.
type Tracked struct {
	Members     int `json:"members"`
	OtherOpen   int `json:"other_open"`
	ClosedGrace int `json:"closed_grace"`
	Watched     int `json:"watched"`
}

// assess sets Health and Reasons of a daemon-built report by the rule in
// doc.go.
func assess(r *Report, stale []string) {
	var reasons []string
	if r.Pause != nil {
		reasons = append(reasons, fmt.Sprintf("origin paused until %s: %s", ts(&r.Pause.Until), r.Pause.Reason))
	}
	if r.Auth != nil && r.Auth.State == AuthUnauthenticated {
		reasons = append(reasons, "unauthenticated: "+r.Auth.Detail)
	}
	for _, q := range r.Queries {
		if q.LastError != "" {
			reasons = append(reasons, "query "+q.Name+" last_error: "+q.LastError)
		}
	}
	if rl := r.RateLimit; rl != nil && rl.BelowFloor {
		age := int64(0)
		if rl.ReadingAgeSeconds != nil {
			age = *rl.ReadingAgeSeconds
		}
		reasons = append(reasons, fmt.Sprintf("origin budget below the floor: %d requests remaining of the burst window, floor %d (reading %ds old)",
			*rl.Remaining, rl.Floor, age))
	}
	reasons = append(reasons, stale...)
	if r.Changes != nil {
		if n := r.Changes.SettleTimeoutsLastHour; n > 0 {
			reasons = append(reasons, fmt.Sprintf("%d settle timeouts in the last hour", n))
		}
		if n := r.Changes.SettleFailuresLastHour; n > 0 {
			reasons = append(reasons, fmt.Sprintf("%d settle failures in the last hour", n))
		}
		for _, c := range r.Changes.Consumers {
			if c.LagRows >= ConsumerLagRows || (c.LagRows > 0 && time.Duration(c.OldestUnackedAgeSeconds)*time.Second >= ConsumerOldestUnacked) {
				reasons = append(reasons, fmt.Sprintf("consumer %s (%s %s) is behind the head by %d rows, oldest unacknowledged %ds",
					c.Consumer, c.View, c.Scope, c.LagRows, c.OldestUnackedAgeSeconds))
			}
		}
	}
	r.Reasons = append(r.Reasons, reasons...)
	r.Health = Healthy
	if len(reasons) > 0 {
		r.Health = Degraded
	}
}

// queryStale says whether a query with no success since `since` has gone past
// QueryStaleTTLs of its TTL at now, and names it.
func queryStale(name string, since time.Time, now time.Time, ttl time.Duration) (string, bool) {
	if ttl <= 0 || now.Sub(since) <= QueryStaleTTLs*ttl {
		return "", false
	}
	return fmt.Sprintf("query %s has had no whole-query success within %d query TTLs", name, QueryStaleTTLs), true
}

func ts(t *time.Time) string { return t.UTC().Format(time.RFC3339) }

// utc is t in UTC at whole-second precision, the report's timestamp form.
func utc(t time.Time) time.Time { return t.UTC().Truncate(time.Second) }

// className is a scheduler class's name without its number ("query" for
// "3 query").
func className(s string) string {
	_, name, _ := strings.Cut(s, " ")
	return name
}
