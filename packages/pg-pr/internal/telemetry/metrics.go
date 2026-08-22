// metrics.go — Prometheus metric definitions for pg-pr.
//
// All metrics use the `pg_pr_` prefix. Labels are kept narrow on purpose
// — high-cardinality labels like pr_number are tagged only on gauges
// scoped to a single observable per PR (ci_only_attempts). Counters and
// histograms label only by repo to keep the time-series count bounded.

package telemetry

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Registry is the prometheus registry pg-pr metrics live in. Tests may
// construct their own registry via NewRegistry; the package-level
// `defaultRegistry` is what the daemon's `/metrics` endpoint scrapes.
var defaultRegistry = prometheus.NewRegistry()

// Per-metric package-level variables registered against defaultRegistry.
// The constructors return *Vec types so callers can do
// SyncPRDuration.WithLabelValues(repo, group).Observe(...).
var (
	SyncPRDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "pg_pr_sync_pr_duration_seconds",
			Help:    "Time spent syncing a single PR through the engine, labeled by repo and ownership group.",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"repo", "group"},
	)

	SyncErrorsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "pg_pr_sync_errors_total",
			Help: "Number of sync errors recorded, labeled by repo.",
		},
		[]string{"repo"},
	)

	FeedbackCreatedTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "pg_pr_feedback_created_total",
			Help: "Number of feedback beads created, labeled by repo and feedback kind.",
		},
		[]string{"repo", "kind"},
	)

	CIOnlyAttempts = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "pg_pr_ci_only_attempts",
			Help: "Consecutive processing cycles closed with only CI-failure feedback per PR.",
		},
		[]string{"repo", "pr_number"},
	)

	FingerprintPollDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name: "pg_pr_fingerprint_poll_duration_seconds",
			Help: "Fingerprint poll latency by group.", Buckets: prometheus.DefBuckets,
		},
		[]string{"group"},
	)

	FingerprintPollErrorsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "pg_pr_fingerprint_poll_errors_total",
			Help: "Fingerprint poll errors by group.",
		}, []string{"group"},
	)

	FingerprintPollTruncatedTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "pg_pr_fingerprint_poll_truncated_total",
			Help: "Fingerprint polls that hit the page cap (incomplete roster).",
		}, []string{"group"},
	)

	FingerprintPollSuccessTimestamp = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "pg_pr_fingerprint_poll_success_timestamp_seconds",
			Help: "Unix time of last successful fingerprint poll by group.",
		}, []string{"group"},
	)

	FingerprintChangesTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "pg_pr_fingerprint_changes_total",
			Help: "Detected changes by group and kind.",
		}, []string{"group", "kind"},
	)

	RefreshQueueDepth = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "pg_pr_refresh_queue_depth",
			Help: "Current refresh queue depth by group.",
		}, []string{"group"},
	)

	RefreshEnqueuedTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "pg_pr_refresh_enqueued_total",
			Help: "PRs enqueued for refresh by group.",
		}, []string{"group"},
	)

	GraphQLCost = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "pg_pr_graphql_cost",
			Help: "Last fingerprint query point cost by group.",
		}, []string{"group"},
	)

	GraphQLRateRemaining = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "pg_pr_graphql_rate_remaining",
			Help: "GraphQL rate-limit points remaining.",
		},
	)

	// GraphQLRatePaused is 1 while the daemon is proactively skipping its
	// GraphQL fingerprint poll to reserve the bottom ~1000 points for direct
	// `gh` use (self-imposed safety buffer), 0 otherwise. Distinct from
	// GraphQLRateRemaining hitting 0 (actual GitHub exhaustion): this signals a
	// SELF-imposed pause that clears automatically once the rate window resets.
	GraphQLRatePaused = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "pg_pr_graphql_rate_paused",
			Help: "1 while the daemon self-pauses GraphQL polling to reserve the bottom ~1000 rate-limit points for direct gh use; 0 otherwise.",
		},
	)

	SnapshotPresent = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "pg_pr_snapshot_present",
		Help: "1 once the dashboard snapshot has been populated for the first time this process; otherwise 0.",
	})

	GHAuthFailuresTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "pg_pr_gh_auth_failures_total",
			Help: "gh auth failures by stage (preflight|poll).",
		}, []string{"stage"},
	)

	// ReviewPreFetchFailuresTotal counts daemon pre-spawn PR-head fetch
	// failures by reason. "credential" is transient (cert expiry / network) and
	// self-heals on retry; "step_missing" means the `step` SSH cert helper was
	// not found on the fetch PATH — most often the ssh ProxyCommand shell's
	// environment, not a missing deploy — an SSH ProxyCommand/credential-env
	// issue that needs operator attention (waiting will not fix it).
	ReviewPreFetchFailuresTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "pg_pr_review_prefetch_failures_total",
			Help: "daemon pre-spawn PR-head fetch failures by reason (credential|step_missing).",
		}, []string{"reason"},
	)

	// VerdictPendingTotal counts bot verdict comments carrying a configured
	// generation's BodyMarker that resolved no generation at all
	// (verdict.Pending — see internal/verdict/verdict.go's Authority doc),
	// labeled only by repo (pg2-4dz88.1.11; INV-APPROVAL-5 in
	// docs/behavior/invariants.md requires this be observable). Deliberately
	// NOT labeled by pr_number or approver login — see this file's header
	// comment on cardinality. A body with NO configured marker at all
	// (Authority Absent) is correctly not a verdict and MUST NOT increment
	// this counter; see internal/sync/approver.go's botVerdictApprovals.
	VerdictPendingTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "pg_pr_verdict_pending_total",
			Help: "Bot verdict comments whose configured BodyMarker matched but no generation's grammar resolved a verdict, labeled by repo.",
		}, []string{"repo"},
	)
)

func init() {
	defaultRegistry.MustRegister(
		SyncPRDuration,
		SyncErrorsTotal,
		FeedbackCreatedTotal,
		CIOnlyAttempts,
		FingerprintPollDuration,
		FingerprintPollErrorsTotal,
		FingerprintPollTruncatedTotal,
		FingerprintPollSuccessTimestamp,
		FingerprintChangesTotal,
		RefreshQueueDepth,
		RefreshEnqueuedTotal,
		GraphQLCost,
		GraphQLRateRemaining,
		GraphQLRatePaused,
		SnapshotPresent,
		GHAuthFailuresTotal,
		ReviewPreFetchFailuresTotal,
		VerdictPendingTotal,
	)
}

// DefaultRegistry exposes the package-level prometheus registry. The
// daemon `/metrics` endpoint reads from this; tests that scrape from a
// custom registry can call MetricsHandler with their own *prometheus.Registry.
func DefaultRegistry() *prometheus.Registry {
	return defaultRegistry
}

// MetricsHandler returns an http.Handler serving the OpenMetrics text
// format over /metrics. The handler reads from the package-level
// defaultRegistry; pass a custom one only for tests.
func MetricsHandler() http.Handler {
	return promhttp.HandlerFor(defaultRegistry, promhttp.HandlerOpts{})
}
