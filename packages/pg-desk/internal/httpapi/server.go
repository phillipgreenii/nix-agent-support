// Package httpapi implements pg-desk serve's HTTP surface: the
// GET /api/v1/dashboard triage payload and the GET /metrics real Prometheus
// metrics catalog (design doc section 8; see the sibling internal/metrics
// package). Both are served behind the SAME 503-until-first-interpretation
// gate (this docket's packet 7 Binding decisions: "503 for every route
// until the interpretation table has ≥1 row").
//
// Ported skeleton: the serve-time freshness-stamping pattern of pg-pr's
// packages/pg-pr/internal/httpapi/dashboard.go, and the five-panel/root-field
// payload shape plus the match-reason decomposition of
// packages/pg-pr/internal/snapshot (snapshot.go's Snapshot/MineRow/TeamRow,
// builder.go's match-reason constants and hasMatchReason/hasWatchLabelReason
// helpers) — cited per declaration below.
//
// Row shape (see Row's doc comment): Phase 9 has no discrete display columns
// for arbitrary presentation facts (title, url, build_state, ...) anywhere in
// pg-desk's store — those field names are chosen by gather (entity.facts) and
// interpret (interpretation.enrichment/urgency/dispositions/approvals), this
// docket's packets 4 and 5, neither of which is a dependency of packet 7. This
// package therefore never invents that business logic itself: it flattens
// whatever those blobs already contain onto one JSON object per row, so
// whichever field names those packets choose reach the payload verbatim.
package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// nowUTC is the serve-time clock. Overridable in tests, mirroring pg-pr's
// packages/pg-pr/internal/httpapi/dashboard.go.
var nowUTC = func() time.Time { return time.Now().UTC() }

// defaultHeartbeatPeriod is the fallback cadence when Config.HeartbeatPeriod
// is empty or unparseable, mirroring pg-pr's freshness.DefaultSyncIntervalSeconds
// (packages/pg-pr/internal/freshness/freshness.go's 60-second default) — the
// same default cadence, so a config-less pg-desk and a config-less pg-pr fail
// closed to the same bound.
const defaultHeartbeatPeriod = 60 * time.Second

// boundIntervals is how many heartbeat periods a last_heartbeat MAY age
// before serve flags the payload stale, per the Binding decisions section
// ("stale: true ... older than TWO heartbeat_periods") — mirrors pg-pr's
// freshness.BoundIntervals.
const boundIntervals = 2

// Panel name constants, pinned verbatim from the design doc's section 7.7
// (docs/superpowers/specs/2026-09-09-pg-desk-and-connector-discovery-design.md
// lines 946-953) and cross-checked against the Grafana dashboard JSON's
// root_selector values in phillipgreenii-nix-support-apps
// (darwin/modules/observability/dashboards/pg-pr.json) — every byte here MUST
// match those root_selector strings exactly, or the Infinity datasource panel
// finds nothing.
const (
	PanelMineActNow              = "mine_act_now"
	PanelMineAwaitingOthers      = "mine_awaiting_others"
	PanelMineAwaitingOtherThings = "mine_awaiting_other_things"
	PanelTeamActNow              = "team_act_now"
	PanelTeamBlocked             = "team_blocked"
)

// Match-reason vocabulary, ported verbatim from pg-pr's
// packages/pg-pr/internal/snapshot/builder.go. The design doc's section 7.4
// ("Match reasons") recomputes the same membership signals server-side
// (team-authored, review-requested, watch-label); this package re-derives the
// three discrete Grafana columns (match_team_authored, match_review_requested,
// match_has_watch_label) from whatever match_reasons list interpret (this
// docket's packet 5, not yet landed) writes, exactly as pg-pr's own
// snapshot.Build already does at build time — a presentation-layer
// projection, not new interpretation business logic.
const (
	matchReasonTeamAuthored    = "team-authored"
	matchReasonReviewRequested = "review-requested"
	matchReasonLabelPrefix     = "label:"
)

// Row is one entry in a panel array or the hidden array.
//
// It is a flat JSON object assembled by merging, in order, the decoded
// contents of interpretation.enrichment, .urgency, .dispositions, and
// .approvals (a later blob's keys win on collision), then overlaying the
// interpretation row's own typed columns (repo, entity_type, entity_id,
// ownership, category, gate_state, as_of) and the three fields this phase
// adds (degraded, sync_error, ready_to_promote) — always sourced from those
// typed columns, never shadowed by same-named blob content. match_reasons is
// decoded separately (it is a JSON array, not an object) into the raw list
// plus the three derived booleans described above; both are present only
// when interpretation.match_reasons is non-empty, matching pg-pr's own
// MineRow (no match fields) versus TeamRow (match fields present) split.
//
// See this file's package doc comment for why the display facts (title,
// url, build_state, ...) are passed through opaquely rather than typed.
type Row map[string]any

func buildRow(interp store.Interpretation) Row {
	row := Row{}
	mergeJSONObject(row, interp.Enrichment)
	mergeJSONObject(row, interp.Urgency)
	mergeJSONObject(row, interp.Dispositions)
	mergeJSONObject(row, interp.Approvals)

	if reasons := decodeStringArray(interp.MatchReasons); reasons != nil {
		row["match_reasons"] = reasons
		row["match_team_authored"] = hasMatchReason(reasons, matchReasonTeamAuthored)
		row["match_review_requested"] = hasMatchReason(reasons, matchReasonReviewRequested)
		row["match_has_watch_label"] = hasWatchLabelReason(reasons)
	}

	row["repo"] = interp.Repo
	row["entity_type"] = interp.EntityType
	row["entity_id"] = interp.EntityID
	setIfNonEmpty(row, "ownership", interp.Ownership)
	setIfNonEmpty(row, "category", interp.Category)
	setIfNonEmpty(row, "gate_state", interp.GateState)
	setIfNonEmpty(row, "as_of", interp.AsOf)
	row["ready_to_promote"] = interp.ReadyToPromote
	row["degraded"] = interp.Degraded
	setIfNonEmpty(row, "sync_error", interp.SyncError)

	return row
}

func setIfNonEmpty(row Row, key, value string) {
	if value != "" {
		row[key] = value
	}
}

// mergeJSONObject decodes blob (a JSON object) and copies its keys onto dst.
// An empty or malformed blob is skipped rather than failing the whole
// request: a bad display-fact blob must never take down the operator's
// triage board.
func mergeJSONObject(dst Row, blob string) {
	if blob == "" {
		return
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(blob), &decoded); err != nil {
		return
	}
	for k, v := range decoded {
		dst[k] = v
	}
}

// decodeStringArray decodes blob (a JSON array of strings) or returns nil
// for an empty or malformed blob.
func decodeStringArray(blob string) []string {
	if blob == "" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(blob), &out); err != nil {
		return nil
	}
	return out
}

// hasMatchReason and hasWatchLabelReason are ported verbatim from pg-pr's
// packages/pg-pr/internal/snapshot/ordering.go (hasMatchReason) and
// builder.go's inline watch-label scan.
func hasMatchReason(reasons []string, want string) bool {
	for _, r := range reasons {
		if r == want {
			return true
		}
	}
	return false
}

func hasWatchLabelReason(reasons []string) bool {
	for _, r := range reasons {
		if strings.HasPrefix(r, matchReasonLabelPrefix) {
			return true
		}
	}
	return false
}

// PayloadError is one entry of Payload.Errors: a row currently carrying a
// non-empty interpretation.sync_error. Always empty in Phase 9 because no
// stage writes sync_error yet (docs/behavior/pg-desk/serve.md's "Out of
// scope"); wired now so Phase 10's sync needs no serve change.
type PayloadError struct {
	Repo       string `json:"repo"`
	EntityType string `json:"entity_type"`
	EntityID   string `json:"entity_id"`
	Error      string `json:"error"`
}

// Payload is the GET /api/v1/dashboard response body: the five named panel
// arrays, the hidden array, and root freshness/counter fields — pinned
// verbatim from the design doc's section 7.7. Every array field is always
// non-nil (serializes as "[]", never "null"), matching pg-pr's own Mine/Team
// convention (packages/pg-pr/internal/snapshot/snapshot.go).
type Payload struct {
	GeneratedAt         time.Time `json:"generated_at"`
	AgeSeconds          int       `json:"age_seconds"`
	Stale               bool      `json:"stale"`
	StaleAfterSeconds   int       `json:"stale_after_seconds"`
	SyncIntervalSeconds int       `json:"sync_interval_seconds"`
	// DroppedCount is always 0 in Phase 9: nothing in this packet's Consumes
	// scope (store reads only) computes a drop count — that is gather/
	// interpret's own concern, a later packet. Deliberately no `omitempty`,
	// matching pg-pr's own field (it must serialize as the numeral 0, never
	// vanish from the payload).
	DroppedCount int `json:"dropped_count"`

	MineActNow              []Row `json:"mine_act_now"`
	MineAwaitingOthers      []Row `json:"mine_awaiting_others"`
	MineAwaitingOtherThings []Row `json:"mine_awaiting_other_things"`
	TeamActNow              []Row `json:"team_act_now"`
	TeamBlocked             []Row `json:"team_blocked"`

	// Hidden carries every row excluded from the five panels above because
	// its PR-level annotation is hidden (annotation.hidden), per the Binding
	// decisions section and design doc section 7.4's "Hidden and WIP are NOT
	// interpreted": serve joins annotation at read time, and a hidden row
	// leaves the panels entirely rather than being flagged in place — fixing
	// the live pg-pr defect where hidden rows stayed in the panel arrays.
	Hidden []Row `json:"hidden"`

	LastRunAt     string         `json:"last_run_at,omitempty"`
	LastSweepAt   string         `json:"last_sweep_at,omitempty"`
	RunsFailed24h int            `json:"runs_failed_24h"`
	Errors        []PayloadError `json:"errors"`
}

// BuildPayload assembles the dashboard payload from the store, as of now.
// Callers MUST have already confirmed store.HasAnyInterpretation (the
// 503 gate); BuildPayload itself does not re-check it.
func BuildPayload(st *store.Store, cfg *config.Config, now time.Time) (*Payload, error) {
	interps, err := st.ListInterpretations()
	if err != nil {
		return nil, fmt.Errorf("httpapi: build payload: %w", err)
	}

	p := &Payload{
		MineActNow:              []Row{},
		MineAwaitingOthers:      []Row{},
		MineAwaitingOtherThings: []Row{},
		TeamActNow:              []Row{},
		TeamBlocked:             []Row{},
		Hidden:                  []Row{},
		Errors:                  []PayloadError{},
	}

	panels := map[string]*[]Row{
		PanelMineActNow:              &p.MineActNow,
		PanelMineAwaitingOthers:      &p.MineAwaitingOthers,
		PanelMineAwaitingOtherThings: &p.MineAwaitingOtherThings,
		PanelTeamActNow:              &p.TeamActNow,
		PanelTeamBlocked:             &p.TeamBlocked,
	}

	cutoff := now.Add(-24 * time.Hour)
	for _, interp := range interps {
		row := buildRow(interp)

		hidden := false
		if ann, found, aerr := st.GetPRAnnotation(interp.Repo, interp.EntityType, interp.EntityID); aerr == nil && found && ann.Hidden != nil {
			hidden = *ann.Hidden
		}
		// An annotation-read error is treated as "not hidden" (fail open
		// toward visible, not silently dropped): a store read glitch must
		// never disappear a row from the operator's triage board.

		switch {
		case hidden:
			p.Hidden = append(p.Hidden, row)
		default:
			if dest, ok := panels[interp.Panel]; ok {
				*dest = append(*dest, row)
			}
			// An interp.Panel value matching none of the five known panels
			// is dropped here: guaranteeing a valid value is interpret's job
			// (packet 5), not serve's.
		}

		if interp.SyncError != "" {
			p.Errors = append(p.Errors, PayloadError{
				Repo: interp.Repo, EntityType: interp.EntityType, EntityID: interp.EntityID,
				Error: interp.SyncError,
			})
			if asOf := parseAsOf(interp.AsOf); !asOf.IsZero() && asOf.After(cutoff) {
				p.RunsFailed24h++
			}
		}
	}

	heartbeatPeriod := parseHeartbeatPeriod(cfg)
	syncIntervalSeconds := int(heartbeatPeriod.Seconds())
	staleAfterSeconds := syncIntervalSeconds * boundIntervals

	lastHeartbeat, _, err := st.GetMeta(store.MetaKeyLastHeartbeat)
	if err != nil {
		return nil, fmt.Errorf("httpapi: build payload: read meta.last_heartbeat: %w", err)
	}
	generatedAt := parseAsOf(lastHeartbeat)

	p.GeneratedAt = generatedAt
	p.SyncIntervalSeconds = syncIntervalSeconds
	p.StaleAfterSeconds = staleAfterSeconds
	p.AgeSeconds = ageSeconds(generatedAt, now)
	p.Stale = isStale(generatedAt, now, staleAfterSeconds)

	if lastRun, found, gerr := st.GetMeta(store.MetaKeyLastRun); gerr != nil {
		return nil, fmt.Errorf("httpapi: build payload: read meta.last_run: %w", gerr)
	} else if found {
		p.LastRunAt = lastRun
	}
	if lastSweep, found, gerr := st.GetMeta(store.MetaKeyLastSweep); gerr != nil {
		return nil, fmt.Errorf("httpapi: build payload: read meta.last_sweep: %w", gerr)
	} else if found {
		p.LastSweepAt = lastSweep
	}

	return p, nil
}

// parseHeartbeatPeriod parses cfg.HeartbeatPeriod as a Go duration, falling
// back to defaultHeartbeatPeriod when cfg is nil, the field is empty, or it
// fails to parse as a positive duration — Binding decisions: "heartbeat_period
// is a config key ... never a hardcoded duration" governs the STALE BOUND
// computation; this fallback only covers the degenerate case of a config
// that omits the key entirely, mirroring pg-pr's own default-cadence pattern.
func parseHeartbeatPeriod(cfg *config.Config) time.Duration {
	if cfg == nil || cfg.HeartbeatPeriod == "" {
		return defaultHeartbeatPeriod
	}
	d, err := time.ParseDuration(cfg.HeartbeatPeriod)
	if err != nil || d <= 0 {
		return defaultHeartbeatPeriod
	}
	return d
}

// parseAsOf, ageSeconds, and isStale mirror pg-pr's
// packages/pg-pr/internal/freshness/freshness.go (ParseAsOf/AgeSeconds/
// IsStale) exactly: a zero/unparseable as-of is stale by definition (fail
// closed), a future as-of (clock skew) is fresh, and "exactly at the bound"
// is not yet past it.
func parseAsOf(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

func ageSeconds(asOf, now time.Time) int {
	if asOf.IsZero() {
		return 0
	}
	d := now.Sub(asOf)
	if d < 0 {
		return 0
	}
	return int(d.Seconds())
}

func isStale(asOf, now time.Time, boundSeconds int) bool {
	if asOf.IsZero() {
		return true
	}
	return now.Sub(asOf) > time.Duration(boundSeconds)*time.Second
}

// NewHandler returns the pg-desk serve HTTP handler: GET /api/v1/dashboard
// and GET /metrics, both gated behind the same 503-until-first-interpretation
// check. It returns an error only if constructing the OTel metrics catalog
// (newMetricsHandler, in metrics_handler.go) fails — in practice this
// cannot happen with a fresh registry and a fixed, valid instrument set,
// but the constructor is fallible so we propagate rather than panic.
func NewHandler(st *store.Store, cfg *config.Config) (http.Handler, error) {
	metricsHandler, err := newMetricsHandler(st, cfg)
	if err != nil {
		return nil, fmt.Errorf("httpapi: new handler: %w", err)
	}
	mux := http.NewServeMux()
	mux.Handle("/api/v1/dashboard", dashboardHandler(st, cfg))
	mux.Handle("/metrics", metricsHandler)
	return readyGate(st, mux), nil
}

// readyGate implements the Binding decisions section's "503 for every route
// until the interpretation table has ≥1 row" — it wraps every route this
// package registers, including /metrics.
func readyGate(st *store.Store, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ready, err := st.HasAnyInterpretation()
		if err != nil {
			http.Error(w, `{"error":"store read failed"}`, http.StatusInternalServerError)
			return
		}
		if !ready {
			http.Error(w, `{"error":"no interpretation yet"}`, http.StatusServiceUnavailable)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func dashboardHandler(st *store.Store, cfg *config.Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload, err := BuildPayload(st, cfg, nowUTC())
		if err != nil {
			http.Error(w, `{"error":"store read failed"}`, http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(payload)
	})
}
