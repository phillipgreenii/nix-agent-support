// backend.go: Backend implements pkg/provider/alert.Provider (Show/List),
// pkg/provider/attention.Provider and pkg/provider.AuthChecker against the
// PagerDuty REST API (design: docs/superpowers/specs/2026-10-01-pg-connector-
// alert-entity-design.md, sections 5.3, 6, 7, 10), bead pg2-t79bc.
//
// Binding decisions:
//
//   - Firing-only (INV-ALERT-1): the backend adds statuses[]=triggered and
//     statuses[]=acknowledged itself and a query element naming any other
//     key (statuses included) is invalid_argument, so a query can only
//     narrow. Show re-checks the incident's status client-side.
//   - Queries (5.3): a query element is URL-query form with repeated-key
//     arrays (service_ids[]=A&service_ids[]=B), never comma-joined. Elements
//     are run separately and unioned, deduplicated by id.
//   - Severity (6): the highest severity among the incident's alerts
//     (GET /incidents/{id}/alerts), falling back to urgency when that call
//     fails or yields no recognized severity.
//   - Acknowledged is derived from the incident status; list_attention keeps
//     acknowledged incidents and emits their severity one level lower,
//     floored at low (design 7, INV-ALERT-8).
//   - Failure semantics (8.1): unreachable / HTTP error / malformed response
//     answer unavailable; a rejected or missing key answers unauthenticated.
//     No cache fallback; Stale is always false.
//   - list_history is not implemented (not in this bead's scope): it answers
//     unknown_op.
package internal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/alert"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/attention"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

const (
	providerName = "pagerduty"
	idPrefix     = providerName + ":"
	alertType    = "alert"

	statusTriggered    = "triggered"
	statusAcknowledged = "acknowledged"
)

// firingStatuses is the fixed, backend-owned status filter.
const firingStatuses = "statuses[]=" + statusTriggered + "&statuses[]=" + statusAcknowledged

// Backend is pg-connector-alert-pagerduty's concrete provider.
type Backend struct {
	transport Transport
	tokens    *TokenResolver
}

// New returns a Backend over t and r. Production wiring passes NewHTTPClient()
// and NewTokenResolver(os.Getenv); tests inject fakes.
func New(t Transport, r *TokenResolver) *Backend { return &Backend{transport: t, tokens: r} }

var (
	_ alert.Provider       = (*Backend)(nil)
	_ attention.Provider   = (*Backend)(nil)
	_ provider.AuthChecker = (*Backend)(nil)
)

// backendConfig is the per-backend config block.
type backendConfig struct {
	BaseURL         string                     `json:"base_url"`
	KeychainService string                     `json:"keychain_service"`
	KeychainAccount string                     `json:"keychain_account"`
	DisableKeychain bool                       `json:"disable_keychain"`
	AttentionQuery  string                     `json:"attention_query"`
	Queries         map[string]schema.QueryDef `json:"queries"`
}

func decodeConfig(ctx context.Context) (backendConfig, error) {
	var cfg backendConfig
	if raw := scriptout.ConfigFromContext(ctx); len(raw) > 0 {
		if err := scriptout.Decode(raw, &cfg); err != nil {
			return backendConfig{}, scriptout.WrapError(scriptout.ErrInvalidArgument, "alert-pagerduty: decode config: "+err.Error())
		}
	}
	cfg.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	if !strings.HasPrefix(cfg.BaseURL, "http://") && !strings.HasPrefix(cfg.BaseURL, "https://") {
		return backendConfig{}, scriptout.WrapError(scriptout.ErrInvalidArgument, "alert-pagerduty: config.base_url must be an http(s) URL")
	}
	if cfg.KeychainService == "" {
		cfg.KeychainService = DefaultKeychainService
	}
	if cfg.KeychainAccount == "" {
		cfg.KeychainAccount = DefaultKeychainAccount
	}
	return cfg, nil
}

// CheckAuth implements provider.AuthChecker: nil when a key resolves. It
// makes no network call; a key PagerDuty later rejects surfaces as
// unauthenticated from the data ops.
func (b *Backend) CheckAuth(ctx context.Context) error {
	cfg, err := decodeConfig(ctx)
	if err != nil {
		return err
	}
	_, err = b.tokens.Resolve(ctx, cfg)
	return err
}

func (b *Backend) token(ctx context.Context, cfg backendConfig) (string, error) {
	tok, err := b.tokens.Resolve(ctx, cfg)
	if err != nil {
		return "", scriptout.WrapError(scriptout.ErrUnauthenticated, "alert-pagerduty: "+err.Error())
	}
	return tok, nil
}

// mapTransportErr turns a transport failure into the wire taxonomy.
func mapTransportErr(err error) error {
	if errors.Is(err, errUnauthorized) {
		return scriptout.WrapError(scriptout.ErrUnauthenticated, "alert-pagerduty: "+err.Error())
	}
	return scriptout.WrapError(scriptout.ErrUnavailable, "alert-pagerduty: "+err.Error())
}

// arrayKeys are the incident filters that take repeated-key arrays.
var arrayKeys = map[string]bool{"service_ids": true, "team_ids": true, "urgencies": true, "user_ids": true}

// scalarKeys are the incident filters that take a single value.
var scalarKeys = map[string]bool{"incident_key": true}

// translateQuery parses one URL-query-form element and returns the encoded
// query fragment it narrows by (empty for an empty element). Array keys are
// re-emitted with literal brackets, one repeated key per value. Any key
// outside the allowlist (notably statuses) is an error.
func translateQuery(element string) (string, error) {
	element = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(element), "?"))
	if element == "" {
		return "", nil
	}
	var parts []string
	for _, pair := range strings.Split(element, "&") {
		if pair == "" {
			continue
		}
		rawK, rawV, ok := strings.Cut(pair, "=")
		if !ok {
			return "", fmt.Errorf("query element %q: %q is not key=value", element, pair)
		}
		k, err := url.QueryUnescape(rawK)
		if err != nil {
			return "", fmt.Errorf("query element %q: bad key %q: %w", element, rawK, err)
		}
		v, err := url.QueryUnescape(rawV)
		if err != nil {
			return "", fmt.Errorf("query element %q: bad value %q: %w", element, rawV, err)
		}
		base := strings.TrimSuffix(k, "[]")
		switch {
		case arrayKeys[base]:
			parts = append(parts, base+"[]="+url.QueryEscape(v))
		case scalarKeys[base] && base == k:
			parts = append(parts, base+"="+url.QueryEscape(v))
		default:
			return "", fmt.Errorf("query element %q: filter %q is not allowed (allowed: service_ids[], team_ids[], urgencies[], user_ids[], incident_key); statuses are fixed to firing", element, k)
		}
	}
	return strings.Join(parts, "&"), nil
}

func rawQueryFor(fragment string) string {
	if fragment == "" {
		return firingStatuses
	}
	return firingStatuses + "&" + fragment
}

func isFiring(status string) bool { return status == statusTriggered || status == statusAcknowledged }

// firingSet runs every element of query (nil/empty is one unfiltered run),
// unions results deduplicated by id in first-seen order, and maps them.
func (b *Backend) firingSet(ctx context.Context, cfg backendConfig, tok string, query schema.QueryExpr) ([]schema.Alert, error) {
	fragments := make([]string, 0, max(1, len(query)))
	if len(query) == 0 {
		fragments = append(fragments, "")
	}
	for _, el := range query {
		f, err := translateQuery(el)
		if err != nil {
			return nil, scriptout.WrapError(scriptout.ErrInvalidArgument, "alert-pagerduty: "+err.Error())
		}
		fragments = append(fragments, f)
	}

	asOf := time.Now().UTC().Format(time.RFC3339)
	seen := make(map[string]bool)
	out := []schema.Alert{}
	for _, frag := range fragments {
		incidents, err := b.transport.Incidents(ctx, cfg.BaseURL, tok, rawQueryFor(frag))
		if err != nil {
			return nil, mapTransportErr(err)
		}
		for _, inc := range incidents {
			if !isFiring(inc.Status) || seen[idPrefix+inc.ID] {
				continue
			}
			mapped, err := b.toSchemaAlert(ctx, cfg.BaseURL, tok, inc, asOf)
			if err != nil {
				return nil, err
			}
			seen[mapped.ID] = true
			out = append(out, mapped)
		}
	}
	return out, nil
}

// List implements alert.Provider.List.
func (b *Backend) List(ctx context.Context, query schema.QueryExpr, idsOnly bool) (*schema.AlertListResult, error) {
	cfg, err := decodeConfig(ctx)
	if err != nil {
		return nil, err
	}
	tok, err := b.token(ctx, cfg)
	if err != nil {
		return nil, err
	}
	alerts, err := b.firingSet(ctx, cfg, tok, query)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(alerts))
	for _, a := range alerts {
		ids = append(ids, a.ID)
	}
	res := &schema.AlertListResult{Entities: alerts, PresentIDs: ids, Cursor: nil, Truncated: false}
	if idsOnly {
		res.Entities = nil
	}
	return res, nil
}

// Show implements alert.Provider.Show: the incident with id when it is
// triggered or acknowledged, else not_found.
func (b *Backend) Show(ctx context.Context, id string) (*schema.Alert, error) {
	cfg, err := decodeConfig(ctx)
	if err != nil {
		return nil, err
	}
	notFound := scriptout.WrapError(scriptout.ErrNotFound, fmt.Sprintf("alert-pagerduty: alert %q is not currently firing", id))
	if !strings.HasPrefix(id, idPrefix) || len(id) == len(idPrefix) {
		return nil, notFound
	}
	tok, err := b.token(ctx, cfg)
	if err != nil {
		return nil, err
	}
	inc, err := b.transport.Incident(ctx, cfg.BaseURL, tok, strings.TrimPrefix(id, idPrefix))
	if errors.Is(err, errMissing) {
		return nil, notFound
	}
	if err != nil {
		return nil, mapTransportErr(err)
	}
	if !isFiring(inc.Status) {
		return nil, notFound
	}
	a, err := b.toSchemaAlert(ctx, cfg.BaseURL, tok, *inc, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// ListHistory is not implemented for PagerDuty (out of this bead's scope).
func (b *Backend) ListHistory(context.Context, time.Time, time.Time, schema.QueryExpr) (*schema.AlertHistoryResult, error) {
	return nil, scriptout.WrapError(scriptout.ErrUnknownOp, "alert-pagerduty: list_history is not supported")
}

// ListAttention implements attention.Provider.
func (b *Backend) ListAttention(ctx context.Context) ([]schema.AttentionItem, error) {
	cfg, err := decodeConfig(ctx)
	if err != nil {
		return nil, err
	}
	var query schema.QueryExpr
	if cfg.AttentionQuery != "" {
		expr, ok := cfg.Queries[cfg.AttentionQuery]
		if !ok {
			return nil, scriptout.WrapError(scriptout.ErrInvalidArgument,
				fmt.Sprintf("alert-pagerduty: attention_query %q is not defined in config.queries", cfg.AttentionQuery))
		}
		query = expr.Expr
	}
	tok, err := b.token(ctx, cfg)
	if err != nil {
		return nil, err
	}
	alerts, err := b.firingSet(ctx, cfg, tok, query)
	if err != nil {
		return nil, err
	}
	items := make([]schema.AttentionItem, 0, len(alerts))
	for _, a := range alerts {
		items = append(items, toAttentionItem(a))
	}
	return items, nil
}

// toAttentionItem maps an alert onto the attention wire shape (design 7),
// applying the acknowledged lowering (one level, floored at low; INV-ALERT-8)
// only when acknowledged is present and true.
func toAttentionItem(a schema.Alert) schema.AttentionItem {
	summary := a.Title
	severity := a.Severity
	if a.Acknowledged != nil && *a.Acknowledged {
		summary += " (acknowledged)"
		severity = lowerOneLevel(severity)
	}
	return schema.AttentionItem{Type: alertType, ID: a.ID, Summary: summary, Severity: severity, URL: a.URL}
}

// lowerOneLevel drops s one rank, floored at low; absent stays absent.
func lowerOneLevel(s schema.Severity) schema.Severity {
	switch s {
	case schema.SeverityCritical:
		return schema.SeverityHigh
	case schema.SeverityHigh:
		return schema.SeverityMedium
	case schema.SeverityMedium, schema.SeverityLow:
		return schema.SeverityLow
	default:
		return s
	}
}

// alertSeverity is design 6's closed PagerDuty alert-severity table. Anything
// else (including "unknown") yields "".
func alertSeverity(v string) schema.Severity {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "critical":
		return schema.SeverityCritical
	case "error":
		return schema.SeverityHigh
	case "warning":
		return schema.SeverityMedium
	case "info":
		return schema.SeverityLow
	default:
		return ""
	}
}

// urgencySeverity is the fallback table: urgency high/low to high/low.
func urgencySeverity(v string) schema.Severity {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "high":
		return schema.SeverityHigh
	case "low":
		return schema.SeverityLow
	default:
		return ""
	}
}

func severityRank(s schema.Severity) int {
	switch s {
	case schema.SeverityCritical:
		return 4
	case schema.SeverityHigh:
		return 3
	case schema.SeverityMedium:
		return 2
	case schema.SeverityLow:
		return 1
	default:
		return 0
	}
}

// incidentSeverity returns the highest recognized severity among the
// incident's alerts, falling back to urgency when the alerts call fails or
// carries no recognized severity. A rejected key is returned as an error: it
// is not a reason to quietly degrade. It also returns the raw alert severity
// strings PagerDuty sent (lower-cased, deduplicated, sorted), recognized or not,
// so a reader can tell a real alert severity from the urgency fallback and
// spot a value the closed mapping table does not know.
func (b *Backend) incidentSeverity(ctx context.Context, baseURL, tok string, inc apiIncident) (schema.Severity, []string, error) {
	alerts, err := b.transport.IncidentAlerts(ctx, baseURL, tok, inc.ID)
	if errors.Is(err, errUnauthorized) {
		return "", nil, mapTransportErr(err)
	}
	var best schema.Severity
	var raw []string
	if err == nil {
		seen := make(map[string]bool)
		for _, a := range alerts {
			if s := alertSeverity(a.Severity); severityRank(s) > severityRank(best) {
				best = s
			}
			if v := strings.ToLower(strings.TrimSpace(a.Severity)); v != "" && !seen[v] {
				seen[v] = true
				raw = append(raw, v)
			}
		}
		sort.Strings(raw)
	}
	if best == "" {
		best = urgencySeverity(inc.Urgency)
	}
	return best, raw, nil
}

// pagerdutyExtension is the typed per-provider data under extensions.pagerduty.
type pagerdutyExtension struct {
	IncidentNumber int    `json:"incident_number,omitempty"`
	Status         string `json:"status"`
	Urgency        string `json:"urgency,omitempty"`
	ServiceID      string `json:"service_id,omitempty"`
	ServiceSummary string `json:"service_summary,omitempty"`
	// AlertSeverities are the raw severity strings of the incident's alerts, as
	// PagerDuty sent them (empty when the alerts call failed or carried none).
	AlertSeverities []string `json:"alert_severities,omitempty"`
}

// toSchemaAlert maps one firing incident onto the shared Alert shape
// (design 3.1). A missing id or an unparseable created_at is a malformed
// response (unavailable).
func (b *Backend) toSchemaAlert(ctx context.Context, baseURL, tok string, inc apiIncident, asOf string) (schema.Alert, error) {
	malformed := func(msg string) error {
		return scriptout.WrapError(scriptout.ErrUnavailable, "alert-pagerduty: malformed incident: "+msg)
	}
	if inc.ID == "" {
		return schema.Alert{}, malformed("no id")
	}
	started, err := time.Parse(time.RFC3339Nano, inc.CreatedAt)
	if err != nil {
		return schema.Alert{}, malformed(fmt.Sprintf("incident %s: created_at %q: %v", inc.ID, inc.CreatedAt, err))
	}
	sev, rawSeverities, err := b.incidentSeverity(ctx, baseURL, tok, inc)
	if err != nil {
		return schema.Alert{}, err
	}

	title := inc.Title
	if title == "" {
		title = inc.ID
	}
	attrs := map[string]string{"detail.status": inc.Status}
	if inc.Urgency != "" {
		attrs["detail.urgency"] = inc.Urgency
	}
	if inc.IncidentNumber != 0 {
		attrs["detail.incident_number"] = strconv.Itoa(inc.IncidentNumber)
	}
	if inc.Service.Summary != "" {
		attrs["detail.service"] = inc.Service.Summary
	}
	extJSON, err := json.Marshal(pagerdutyExtension{
		IncidentNumber: inc.IncidentNumber, Status: inc.Status, Urgency: inc.Urgency,
		ServiceID: inc.Service.ID, ServiceSummary: inc.Service.Summary,
		AlertSeverities: rawSeverities,
	})
	if err != nil {
		return schema.Alert{}, malformed("marshal extension: " + err.Error())
	}
	ack := inc.Status == statusAcknowledged

	return schema.Alert{
		ID:           idPrefix + inc.ID,
		Provider:     providerName,
		Title:        title,
		Description:  inc.Description,
		Severity:     sev,
		Acknowledged: &ack,
		Since:        started.UTC().Format(time.RFC3339),
		URL:          inc.HTMLURL,
		Attributes:   attrs,
		Extensions:   map[string]json.RawMessage{providerName: extJSON},
		AsOf:         asOf,
		Stale:        false,
	}, nil
}
