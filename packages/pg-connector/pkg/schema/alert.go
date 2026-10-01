// alert.go: the alert entity's shared JSON wire shape — a Tier-1 entity
// (ADR 0062 Decision item 9), peer of pr/issue/ci/scm. An alert is an
// actively-firing signal from a monitoring system (Grafana, PagerDuty, ...).
//
// Alert is an independently defined shape (like CalendarEvent), not a
// re-export of any source API type. The core holds only facts every source can
// supply with a defined meaning; everything source-specific stays in
// Attributes (flat strings, for generic display and consumer-owned identity
// logic) or Extensions (typed, per-provider, opaque to Tier 1).
//
// Alerts are firing-only (INV-ALERT-1): there is deliberately NO state field
// and no resolved_at — an alert that resolves leaves the list.
package schema

import "encoding/json"

// AlertSchemaVersion is the alert capability's own schema version, populated
// into the wire envelope's schemaVersion field by each alert dispatch-table
// entry and registered in CurrentSchemaVersions (INV-VER-1).
const AlertSchemaVersion = 1

// Alert is one currently-firing alert.
type Alert struct {
	// ID MUST be stable across polls for the same firing instance (never
	// derived from a start time or any value that changes while firing) and
	// MUST be namespaced by provider (e.g. "grafana:<fingerprint>") so ids
	// are unique across sources (INV-ALERT-3). It is the attention merge key
	// together with type "alert".
	ID string `json:"id"`
	// Provider names the source system ("grafana", "pagerduty").
	Provider string `json:"provider"`
	// Title is the alert's short name (Grafana: alertname label).
	Title string `json:"title"`
	// Description is the longer explanation, when the source has one.
	Description string `json:"description,omitempty"`
	// Severity is the backend's own internal mapping of its source severity
	// onto the attention Severity enum. Omitted when the source has no
	// opinion; it MUST NOT be defaulted anywhere (INV-ALERT-4).
	Severity Severity `json:"severity,omitempty"`
	// Acknowledged is OPTIONAL. Nil (omitted) means "this source cannot
	// express it", NOT "unacknowledged"; a consumer MUST NOT read absence as
	// false (INV-ALERT-2).
	Acknowledged *bool `json:"acknowledged,omitempty"`
	// Since is the RFC3339 time the alert started firing.
	Since string `json:"since"`
	// URL links to the alert in the source system, when known.
	URL string `json:"url,omitempty"`
	// Attributes are flat, source-supplied strings, keyed with a namespace
	// prefix (label.<k>, annotation.<k>, detail.<k>) so they never collide.
	// The connector does not filter or interpret any of them.
	Attributes map[string]string `json:"attributes,omitempty"`
	// Extensions is a map {"<provider>": {...}} of typed per-provider data.
	// Tier 1 passes it through unread; the core MUST suffice for generic
	// display and for attention.
	Extensions map[string]json.RawMessage `json:"extensions,omitempty"`
	// AsOf is the RFC3339 read time.
	AsOf string `json:"as_of"`
	// Stale is always false for alerts: there is no cache fallback
	// (INV-ALERT-5); the field exists only for schema uniformity (INV-ASOF-1).
	Stale bool `json:"stale"`
}

// AlertListResult mirrors CalendarListResult / ThreadListResult so the
// generic list op and present_ids work unchanged.
type AlertListResult struct {
	Entities   []Alert  `json:"entities"`
	PresentIDs []string `json:"present_ids"`
	Cursor     *string  `json:"cursor"`
	Truncated  bool     `json:"truncated"`
}

// AlertEpisode is one firing interval of an alert, for the history op.
type AlertEpisode struct {
	// RuleID is the provider-defined definition id (Grafana rule uid).
	RuleID string `json:"rule_id"`
	// AlertID is omitted when the source cannot attribute the episode to an
	// instance id.
	AlertID string `json:"alert_id,omitempty"`
	Title   string `json:"title"`
	// StartedAt is RFC3339.
	StartedAt string `json:"started_at"`
	// EndedAt is RFC3339; omitted means still firing.
	EndedAt    string            `json:"ended_at,omitempty"`
	Severity   Severity          `json:"severity,omitempty"`
	Attributes map[string]string `json:"attributes,omitempty"`
}

// AlertHistoryResult is the list_history op's result.
type AlertHistoryResult struct {
	Episodes  []AlertEpisode `json:"episodes"`
	Truncated bool           `json:"truncated"`
}
