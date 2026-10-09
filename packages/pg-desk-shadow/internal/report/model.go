package report

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"time"
)

// SchemaID identifies the JSON report.
const SchemaID = "pg-desk-shadow.report/v1"

// Params tunes the computation; every value is reported.
type Params struct {
	LivePeriod    time.Duration // longest live query period (pr-team)
	SlotPeriod    time.Duration // shadow slot
	SweepPeriod   time.Duration // live pr-sweep period
	FirstHour     time.Duration // copy-staleness window after T0
	RouterDownGap time.Duration // a hole in the live dispatch log longer than this means the router was down
	// LateWindow bounds how long after a MISSED live event the first later shadow
	// item for the same PR still counts as that event's late detection.
	LateWindow      time.Duration
	WeekdayMinTicks int
	MinLive         int
	MinSweepCaught  int
	MaxDays         int
	UptimeMin       float64
	HashUnusable    float64 // content_hash_changed share above which the hash signal is unusable
	Loc             *time.Location
	// LiveStore, when set, enables the optional head-SHA comparison (g).
	LiveStore string
}

// DefaultParams are the values of the filing bead.
func DefaultParams() Params {
	return Params{
		LivePeriod: 120 * time.Second, SlotPeriod: 60 * time.Second, SweepPeriod: 30 * time.Minute,
		FirstHour: time.Hour, RouterDownGap: 10 * time.Minute, LateWindow: 3 * time.Hour,
		WeekdayMinTicks: 480, MinLive: 100, MinSweepCaught: 10, MaxDays: 7, UptimeMin: 0.95,
		HashUnusable: 0.20, Loc: time.Local,
	}
}

// Labeler maps an id to its HMAC label under the per-run key.
type Labeler struct{ Key []byte }

// Label returns pr-<8 hex>.
func (l Labeler) Label(id string) string {
	h := hmac.New(sha256.New, l.Key)
	h.Write([]byte(id))
	return "pr-" + hex.EncodeToString(h.Sum(nil))[:8]
}

// Miss classes.
const (
	ClassCollectorDown = "collector-down"
	ClassCopyStaleness = "copy-staleness"
	ClassSkippedBudget = "skipped-budget"
	ClassBudgetExhaust = "budget-exhausted"
	ClassHydrationFail = "hydration-failed"
	ClassBlindSpot     = "blind-spot"
	ClassLiveOnly      = "live-only"
	ClassUnexplained   = "unexplained"
)

// MissClasses lists the classes in precedence order.
var MissClasses = []string{ClassCollectorDown, ClassCopyStaleness, ClassSkippedBudget, ClassBudgetExhaust, ClassHydrationFail, ClassBlindSpot, ClassLiveOnly, ClassUnexplained}

// Dist is a distribution summary.
type Dist struct {
	N   int     `json:"n"`
	P50 float64 `json:"p50"`
	P90 float64 `json:"p90"`
	Max float64 `json:"max"`
}

// MakeDist summarises samples (nearest-rank percentiles).
func MakeDist(v []float64) Dist {
	if len(v) == 0 {
		return Dist{}
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	rank := func(p float64) float64 {
		i := int(p*float64(len(s))+0.999999) - 1
		if i < 0 {
			i = 0
		}
		if i >= len(s) {
			i = len(s) - 1
		}
		return s[i]
	}
	return Dist{N: len(s), P50: rank(0.5), P90: rank(0.9), Max: s[len(s)-1]}
}

// GapStat aggregates gaps of one reason.
type GapStat struct {
	Count   int     `json:"count"`
	Seconds float64 `json:"seconds"`
}

// Baseline is the live sweep baseline of every candidate signal.
type Baseline struct {
	SweepRows        int            `json:"sweep_rows"`
	HashChangedRows  int            `json:"hash_changed_rows"`
	HashChangedShare float64        `json:"hash_changed_share"`
	HashSignalUsable bool           `json:"hash_signal_usable"`
	AnchorRows       int            `json:"anchor_rows"`
	AnchorByCause    map[string]int `json:"anchor_by_cause"`
	RunRecordPresent bool           `json:"run_record_present"`
	HashStatement    string         `json:"hash_statement"`
	WindowFrom       string         `json:"window_from,omitempty"`
	WindowTo         string         `json:"window_to,omitempty"`
}

// DataQuality is the header of the report.
type DataQuality struct {
	Phase              string              `json:"phase"`
	Seeded             bool                `json:"seeded"`
	T0                 string              `json:"t0"`
	FirstTick          string              `json:"first_tick"`
	LastTick           string              `json:"last_tick"`
	ElapsedDays        float64             `json:"elapsed_days"`
	ExecutedTicks      int                 `json:"executed_ticks"`
	CompletedTicks     int                 `json:"completed_ticks"`
	FailedTicks        int                 `json:"failed_ticks"`
	SkippedTicks       int                 `json:"skipped_budget_ticks"`
	SlotsTotal         int                 `json:"slots_total"`
	SlotsEligible      int                 `json:"slots_eligible"`
	UptimeSpec         float64             `json:"uptime_spec"`
	UptimeRaw          float64             `json:"uptime_raw"`
	Gaps               map[string]GapStat  `json:"gaps"`
	RouterDownSecs     float64             `json:"router_down_seconds"`
	WarmupTicks        int                 `json:"warmup_ticks"`
	WarmupEnd          string              `json:"warmup_end,omitempty"`
	RouterEventsWindow string              `json:"router_events_window"`
	RunRecordWindow    string              `json:"run_record_window"`
	QueueWindow        string              `json:"queue_window"`
	ResetMarkers       map[string]int      `json:"reset_markers"`
	BuildsByDay        map[string][]string `json:"builds_by_day"`
	BDMode             string              `json:"bd_mode"`
	Baseline           Baseline            `json:"baseline"`
	Statements         []string            `json:"statements"`
	Params             map[string]any      `json:"params"`
}

// Live is the LIVE-DETECTED section.
type Live struct {
	Total             int     `json:"live_detected"`
	InWindow          int     `json:"in_window"`
	ExcludedWarmup    int     `json:"excluded_warmup"`
	ExcludedOutside   int     `json:"excluded_outside_run"`
	WithoutEnqueuedAt int     `json:"dispatch_rows_without_enqueued_at"`
	MedianWaitSeconds float64 `json:"median_enqueue_to_start_seconds"`
}

// MissEntry is one classified miss (label only).
type MissEntry struct {
	PR       string `json:"pr"`
	At       string `json:"live_enqueued_at"`
	Class    string `json:"class"`
	Evidence string `json:"evidence,omitempty"`
	// LateSeconds is the delay of the first later shadow item for the same PR
	// (within the late window), when there is one. An upper bound on the delay,
	// not proof that the item is the same change.
	LateSeconds *float64 `json:"later_shadow_detection_s,omitempty"`
}

// Misses is metric (a).
type Misses struct {
	Matched int `json:"matched"`
	Missed  int `json:"missed"`
	// LateDetected counts MISSED events the shadow flagged later (see MissEntry).
	LateDetected int `json:"missed_but_detected_later"`
	// CoverageCeiling is (Matched + LateDetected) over the in-window live events:
	// an UPPER BOUND on the share of live changes the shadow detected (a late
	// detection is the first later item for the same PR, not proof it is the same
	// change; each item stands for at most one missed event and never for a
	// matched one). Informational: it does not replace a stop criterion. Zero both
	// when nothing was covered and when there were no in-window events.
	CoverageCeiling float64 `json:"coverage_ceiling"`
	// CoverageCeilingWhenUp is the same bound over the events NOT classed
	// collector-down (matched plus later-detected misses of any other class, over
	// in-window events minus the collector-down ones): did the shadow detect the
	// change whenever it was running. Zero when there were no such events.
	CoverageCeilingWhenUp float64        `json:"coverage_ceiling_when_collector_up"`
	ByClass               map[string]int `json:"by_class"`
	Unexplained           int            `json:"unexplained"`
	Entries               []MissEntry    `json:"entries"`
	Tolerance             string         `json:"tolerance"`
	LateWindow            string         `json:"late_window"`
}

// SweepEntry is one SWEEP-CAUGHT row (label only).
type SweepEntry struct {
	PR         string `json:"pr"`
	End        string `json:"end"`
	Cause      string `json:"cause"`
	Result     string `json:"result"`
	FeedMissed bool   `json:"live_feed_missed"`
}

// Sweep is metric (b).
type Sweep struct {
	Headline         int            `json:"headline_count"`
	OtherCauses      map[string]int `json:"other_causes"`
	ShadowDetected   int            `json:"headline_shadow_detected"`
	CaughtLocalSweep int            `json:"headline_caught_by_local_or_sweep_origin"`
	ShadowMissed     int            `json:"headline_shadow_missed"`
	MissedBlindSpot  int            `json:"headline_missed_declared_blind_spot"`
	FeedMissedRate   float64        `json:"live_sweep_caught_what_feed_missed_rate"`
	FeedMissed       int            `json:"live_sweep_caught_what_feed_missed"`
	Entries          []SweepEntry   `json:"entries"`
}

// NoiseEntry is a shadow-only detection.
type ShadowOnly struct {
	Total            int            `json:"shadow_only"`
	FirstObservation int            `json:"first_observation_reconciles"`
	ByKind           map[string]int `json:"by_kind"`
	ByField          map[string]int `json:"by_field"`
	NoFieldChange    int            `json:"no_field_change"`
	NoiseRatePerHour float64        `json:"noise_rate_per_hour"`
	OtherOrigins     map[string]int `json:"other_origin_items"`
	OtherReal        map[string]int `json:"other_origin_real_changes"`
}

// Delay is metric (d), in seconds; negative means the shadow was first.
type Delay struct {
	VsLiveEnqueue    Dist `json:"shadow_minus_live_enqueue_s"`
	ShadowFromUpdate Dist `json:"shadow_minus_pr_updated_s"`
	LiveFromUpdate   Dist `json:"live_enqueue_minus_pr_updated_s"`
	// MissedThenDetected is the late-detection delay of the missed events the
	// shadow flagged later (an upper bound; see MissEntry).
	MissedThenDetected Dist `json:"missed_then_detected_shadow_minus_live_enqueue_s"`
}

// Hour is one hour bucket of metric (e).
type Hour struct {
	Hour           string `json:"hour"`
	Ticks          int    `json:"ticks"`
	Hydrations     int64  `json:"hydrations"`
	GraphQLCost    int    `json:"graphql_cost_lower_bound"`
	RemainingAtEnd int    `json:"shared_token_remaining_at_hour_end"`
	RemainingKnown bool   `json:"remaining_known"`
	MinRemaining   int    `json:"min_remaining"`
}

// Cost is metric (e).
type Cost struct {
	TickDurationS     Dist   `json:"tick_duration_s"`
	TicksOver60s      int    `json:"ticks_over_60s"`
	HydrationsPerHour Dist   `json:"hydrations_per_hour"`
	CostPerHour       Dist   `json:"graphql_points_per_hour"`
	Hours             []Hour `json:"hours"`
	HoursBelowReserve int    `json:"hours_with_remaining_below_reserve"`
	Ceiling           int    `json:"ceiling"`
	Statement         string `json:"statement"`
}

// HeadSHA is the optional metric (g).
type HeadSHA struct {
	Compared    int `json:"compared"`
	Disagreeing int `json:"disagreeing"`
	OnlyShadow  int `json:"only_in_shadow"`
	OnlyLive    int `json:"only_in_live"`
}

// Criterion is one stop criterion.
type Criterion struct {
	Name string `json:"name"`
	Need string `json:"need"`
	Have string `json:"have"`
	Met  bool   `json:"met"`
}

// Stop is the stop-criteria table.
type Stop struct {
	Criteria []Criterion `json:"criteria"`
	Complete bool        `json:"complete"`
}

// PhaseReport is one phase's report.
type PhaseReport struct {
	Phase              string      `json:"phase"`
	DataQuality        DataQuality `json:"data_quality"`
	Live               Live        `json:"live"`
	Misses             Misses      `json:"misses"`
	Sweep              Sweep       `json:"sweep"`
	ShadowOnly         ShadowOnly  `json:"shadow_only"`
	Delay              Delay       `json:"delay"`
	Cost               Cost        `json:"cost"`
	SkippedBudgetTicks int         `json:"skipped_budget_ticks"`
	HeadSHA            *HeadSHA    `json:"head_sha,omitempty"`
	Stop               Stop        `json:"stop"`

	samp samples
}

type samples struct {
	tickDur, hydrationsHour, costHour []float64
	dVsLive, dShadowUpd, dLiveUpd     []float64
	dLate                             []float64
}

// Report is the JSON document.
type Report struct {
	Schema   string        `json:"schema"`
	AsOf     string        `json:"as_of"`
	Phases   []PhaseReport `json:"phases"`
	Combined *Combined     `json:"combined,omitempty"`
}

// Combined merges the phases of several scratch directories.
type Combined struct {
	Phases            []string       `json:"phases"`
	LiveDetected      int            `json:"live_detected"`
	Matched           int            `json:"matched"`
	Missed            int            `json:"missed"`
	LateDetected      int            `json:"missed_but_detected_later"`
	ByClass           map[string]int `json:"misses_by_class"`
	Unexplained       int            `json:"unexplained"`
	SweepHeadline     int            `json:"sweep_headline"`
	ShadowOnly        int            `json:"shadow_only"`
	TickDurationS     Dist           `json:"tick_duration_s"`
	CostPerHour       Dist           `json:"graphql_points_per_hour"`
	HydrationsPerHour Dist           `json:"hydrations_per_hour"`
	VsLiveEnqueue     Dist           `json:"shadow_minus_live_enqueue_s"`
}
