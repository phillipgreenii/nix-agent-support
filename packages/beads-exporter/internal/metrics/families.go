package metrics

// Family names. The main, throughput and stranded passes emit these; the exporter's own
// health families are emitted by the collector for every pass.
const (
	FamIssues          = "beads_issues"
	FamIssuesStored    = "beads_issues_stored"
	FamQueueCandidates = "beads_queue_candidates"
	FamQueueOldest     = "beads_queue_oldest_timestamp_seconds"
	FamByType          = "beads_not_closed_by_type"
	FamByPriority      = "beads_not_closed_by_priority"
	FamByLabel         = "beads_not_closed_by_bead_label"
	FamOldest          = "beads_oldest_timestamp_seconds"
	FamCreated24h      = "beads_created_last_24h"
	FamClosed24h       = "beads_closed_last_24h"
	FamStrandedClaims  = "beads_stranded_claims"
	FamOldestStranded  = "beads_oldest_stranded_claim_timestamp_seconds"
	FamExporterUp      = "beads_exporter_up"
	FamPassLastSuccess = "beads_exporter_pass_last_success_timestamp_seconds"
	FamCollectErrors   = "beads_exporter_collect_errors_total"
	FamCollectDuration = "beads_exporter_collect_duration_seconds"
	OtherLabelValue    = "__other__"
	stateHelpList      = "in_progress, deferred, blocked, ready, tracking or other"
	passHelpList       = "main, throughput or stranded"
	reasonHelpList     = "stale_issues_jsonl, timeout, bd_error, schema_skew, parse_error or transcript_error"
)

// Families returns the families of the main, throughput and stranded passes plus the
// exporter health families, in output order. A later pass registers its own
// families on the registry returned by Default.
func Families() []Family {
	return []Family{
		{
			FamIssues, Gauge,
			"Beads that are not closed, by derived state (" + stateHelpList + "). Each bead is in exactly one state, so the sum over state is the not-closed total.",
			[]string{"db", "state"},
		},
		{
			FamIssuesStored, Gauge,
			"Beads by stored status. Non-closed statuses come from the default list view; closed comes from the status count and includes closed gates. Zero-filled over every status bd knows.",
			[]string{"db", "status"},
		},
		{
			FamQueueCandidates, Gauge,
			"Beads currently claimable through each configured queue.",
			[]string{"db", "queue"},
		},
		{
			FamQueueOldest, Gauge,
			"Creation time of the oldest candidate in each queue, in Unix seconds. Omitted when the queue is empty.",
			[]string{"db", "queue"},
		},
		{
			FamByType, Gauge,
			"Not-closed beads by issue type.",
			[]string{"db", "type"},
		},
		{
			FamByPriority, Gauge,
			"Not-closed beads by priority.",
			[]string{"db", "priority"},
		},
		{
			FamByLabel, Gauge,
			"Not-closed beads carrying each label, for the labelCap most common labels; beads carrying any other label are counted once under bead_label=\"" + OtherLabelValue + "\". Labels overlap, so the sum over bead_label is NOT the not-closed total.",
			[]string{"db", "bead_label"},
		},
		{
			FamOldest, Gauge,
			"Creation time of the oldest not-closed bead in each derived state, in Unix seconds (start time for in_progress). Omitted when the state is empty.",
			[]string{"db", "state"},
		},
		{
			FamCreated24h, Gauge,
			"Beads created in the last 24 hours, counted by the throughput pass.",
			[]string{"db"},
		},
		{
			FamClosed24h, Gauge,
			"Beads closed in the last 24 hours, counted by the throughput pass.",
			[]string{"db"},
		},
		{
			FamStrandedClaims, Gauge,
			"Claimed beads whose claim has no live owner, by stored status (open, in_progress or hooked): no transcript written within the stale-claim window by, or naming as a claim value, the claimant. Counted by the stranded pass; zero-filled over every status.",
			[]string{"db", "status"},
		},
		{
			FamOldestStranded, Gauge,
			"Claim time (started_at, else updated_at) of the oldest claim with no live owner, in Unix seconds. Omitted when there are none.",
			[]string{"db"},
		},
		{
			FamExporterUp, Gauge,
			"1 if the last main collection cycle for this database succeeded, 0 if it failed.",
			[]string{"db"},
		},
		{
			FamPassLastSuccess, Gauge,
			"Time of the last successful run of each collection pass (" + passHelpList + "), in Unix seconds. Omitted until the pass has succeeded once.",
			[]string{"db", "pass"},
		},
		{
			FamCollectErrors, Counter,
			"Collection failures by pass and closed-enum reason (" + reasonHelpList + ").",
			[]string{"db", "pass", "reason"},
		},
		{
			FamCollectDuration, Gauge,
			"Duration of the last run of each collection pass, in seconds.",
			[]string{"db", "pass"},
		},
	}
}

// Default builds the registry of the families above.
func Default() *Registry {
	r, err := NewRegistry(Families()...)
	if err != nil {
		panic(err) // programmer error: the static family list is invalid
	}
	return r
}
