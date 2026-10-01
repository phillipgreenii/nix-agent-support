// backend.go: Backend implements pkg/provider/alert.Provider (Show/List) and
// pkg/provider/attention.Provider against a local Grafana's Alertmanager v2
// alerts API (design: docs/superpowers/specs/2026-10-01-pg-connector-alert-
// entity-design.md, sections 4-8, 10), bead pg2-rejc3.
//
// Binding decisions:
//
//   - Firing-only (INV-ALERT-1): the backend keeps only instances whose
//     status.state is "active" (silenced, inhibited and unprocessed are
//     excluded) CLIENT-SIDE, after the fetch, so it never depends on server
//     parameter semantics, and a query can only narrow that set.
//   - Queries (5.1-5.3): config.queries maps a caller-facing name to one or
//     more Alertmanager matcher sets; each set becomes repeated filter=
//     parameters. A list of sets is run per element and unioned, deduplicated
//     by id. No query names are built in. A nil/empty query means the whole
//     firing set.
//   - attention_query (5.2): unset means the unfiltered firing set; set to a
//     name config.queries does not define makes ListAttention fail
//     invalid_argument.
//   - Severity (6): a closed table, never defaulted (severityFromLabel).
//   - Failure semantics (8.1): unreachable / HTTP error / malformed response
//     answer unavailable; reachable-and-empty is a successful empty result.
//     There is no cache fallback (8.2); Stale is always false.
//   - ListHistory (bead pg2-rwuhs, history.go) enumerates rules and reads each
//     rule's state history; it is the ONLY caller of the history transport
//     methods, so attention and list stay stateless and history-free.
//   - No AuthChecker: Grafana needs no auth, so `auth status` reports
//     "disabled: not applicable" via the wire-level unknown_op sentinel.
package internal

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/alert"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/attention"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// providerName is Alert.Provider and the id namespace prefix.
const providerName = "grafana"

// idPrefix namespaces every alert id (INV-ALERT-3).
const idPrefix = providerName + ":"

// alertType is the AttentionItem.Type this backend emits.
const alertType = "alert"

// activeState is the Alertmanager instance state that counts as firing.
const activeState = "active"

// ruleUIDLabel is the label Grafana sets to the alerting rule's uid.
const ruleUIDLabel = "__alert_rule_uid__"

// Backend is pg-connector-alert-grafana's concrete provider.
type Backend struct {
	transport Transport
}

// New returns a Backend over t. Production wiring passes NewHTTPClient();
// tests inject a stub.
func New(t Transport) *Backend { return &Backend{transport: t} }

var (
	_ alert.Provider     = (*Backend)(nil)
	_ attention.Provider = (*Backend)(nil)
)

// Deliberately NO `var _ provider.AuthChecker = (*Backend)(nil)`: Grafana
// needs no credential (design 10).

// backendConfig is the per-backend config block decoded from
// scriptout.ConfigFromContext on every call.
type backendConfig struct {
	BaseURL        string                      `json:"base_url"`
	AttentionQuery string                      `json:"attention_query"`
	Queries        map[string]schema.QueryExpr `json:"queries"`
}

func decodeConfig(ctx context.Context) (backendConfig, error) {
	var cfg backendConfig
	raw := scriptout.ConfigFromContext(ctx)
	if len(raw) > 0 {
		if err := scriptout.Decode(raw, &cfg); err != nil {
			return backendConfig{}, scriptout.WrapError(scriptout.ErrInvalidArgument, "alert-grafana: decode config: "+err.Error())
		}
	}
	cfg.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if cfg.BaseURL == "" {
		return backendConfig{}, scriptout.WrapError(scriptout.ErrInvalidArgument, "alert-grafana: config.base_url is required")
	}
	if !strings.HasPrefix(cfg.BaseURL, "http://") && !strings.HasPrefix(cfg.BaseURL, "https://") {
		return backendConfig{}, scriptout.WrapError(scriptout.ErrInvalidArgument, "alert-grafana: config.base_url must be an http(s) URL")
	}
	return cfg, nil
}

// fetchFiring fetches the instances matching one matcher-set element's
// filters and keeps only the actively-firing ones (INV-ALERT-1).
func (b *Backend) fetchFiring(ctx context.Context, baseURL string, filters []string) ([]apiAlert, error) {
	raw, err := b.transport.Alerts(ctx, baseURL, filters)
	if err != nil {
		return nil, scriptout.WrapError(scriptout.ErrUnavailable, "alert-grafana: "+err.Error())
	}
	firing := make([]apiAlert, 0, len(raw))
	for _, a := range raw {
		if a.Status.State == activeState {
			firing = append(firing, a)
		}
	}
	return firing, nil
}

// firingSet runs every element of query (a nil/empty query is one unfiltered
// run), unions the results deduplicated by id in first-seen order, and maps
// them to schema.Alert. An unparseable element is invalid_argument.
func (b *Backend) firingSet(ctx context.Context, cfg backendConfig, query schema.QueryExpr) ([]schema.Alert, error) {
	filterSets := make([][]string, 0, max(1, len(query)))
	if len(query) == 0 {
		filterSets = append(filterSets, nil)
	}
	for _, el := range query {
		f, err := translateMatcherSet(el)
		if err != nil {
			return nil, scriptout.WrapError(scriptout.ErrInvalidArgument, "alert-grafana: "+err.Error())
		}
		filterSets = append(filterSets, f)
	}

	asOf := time.Now().UTC().Format(time.RFC3339)
	seen := make(map[string]bool)
	out := []schema.Alert{}
	for _, filters := range filterSets {
		firing, err := b.fetchFiring(ctx, cfg.BaseURL, filters)
		if err != nil {
			return nil, err
		}
		for _, a := range firing {
			mapped, err := toSchemaAlert(a, cfg.BaseURL, asOf)
			if err != nil {
				return nil, scriptout.WrapError(scriptout.ErrUnavailable, "alert-grafana: malformed alert: "+err.Error())
			}
			if seen[mapped.ID] {
				continue
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
	alerts, err := b.firingSet(ctx, cfg, query)
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

// Show implements alert.Provider.Show: the currently-firing alert with id, or
// not_found.
func (b *Backend) Show(ctx context.Context, id string) (*schema.Alert, error) {
	cfg, err := decodeConfig(ctx)
	if err != nil {
		return nil, err
	}
	notFound := scriptout.WrapError(scriptout.ErrNotFound, fmt.Sprintf("alert-grafana: alert %q is not currently firing", id))
	if !strings.HasPrefix(id, idPrefix) || len(id) == len(idPrefix) {
		return nil, notFound
	}
	alerts, err := b.firingSet(ctx, cfg, nil)
	if err != nil {
		return nil, err
	}
	for i := range alerts {
		if alerts[i].ID == id {
			return &alerts[i], nil
		}
	}
	return nil, notFound
}

// ListAttention implements attention.Provider: every alert in the
// attention_query set (unfiltered when unset), one AttentionItem each.
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
				fmt.Sprintf("alert-grafana: attention_query %q is not defined in config.queries", cfg.AttentionQuery))
		}
		query = expr
	}
	alerts, err := b.firingSet(ctx, cfg, query)
	if err != nil {
		return nil, err
	}
	items := make([]schema.AttentionItem, 0, len(alerts))
	for _, a := range alerts {
		items = append(items, toAttentionItem(a))
	}
	return items, nil
}

// toAttentionItem maps an alert onto the attention wire shape (design 7). The
// acknowledged lowering (drop one level, floored at low, only when
// acknowledged is present and true; INV-ALERT-8) is applied here so the rule
// lives with the mapping; Grafana never sets acknowledged, so it is inert for
// this backend today.
func toAttentionItem(a schema.Alert) schema.AttentionItem {
	summary := a.Title
	severity := a.Severity
	if a.Acknowledged != nil && *a.Acknowledged {
		summary += " (acknowledged)"
		severity = lowerOneLevel(severity)
	}
	return schema.AttentionItem{Type: alertType, ID: a.ID, Summary: summary, Severity: severity}
}

// lowerOneLevel drops s one rank, floored at low; an absent severity stays
// absent (INV-ALERT-4).
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

// severityFromLabel is design 6's closed Grafana table. Anything absent or
// unrecognized yields "" (omitted, never defaulted).
func severityFromLabel(v string) schema.Severity {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "critical":
		return schema.SeverityCritical
	case "error", "high":
		return schema.SeverityHigh
	case "warning":
		return schema.SeverityMedium
	case "info":
		return schema.SeverityLow
	default:
		return ""
	}
}

// grafanaExtension is the typed per-provider data under extensions.grafana.
type grafanaExtension struct {
	Receivers    []string `json:"receivers,omitempty"`
	GeneratorURL string   `json:"generator_url,omitempty"`
	RuleUID      string   `json:"rule_uid,omitempty"`
	Folder       string   `json:"folder,omitempty"`
}

// toSchemaAlert maps one firing instance onto the shared Alert shape
// (design 3.1). A missing fingerprint or an unparseable startsAt is a
// malformed response.
func toSchemaAlert(a apiAlert, baseURL, asOf string) (schema.Alert, error) {
	if a.Fingerprint == "" {
		return schema.Alert{}, fmt.Errorf("instance has no fingerprint")
	}
	started, err := time.Parse(time.RFC3339Nano, a.StartsAt)
	if err != nil {
		return schema.Alert{}, fmt.Errorf("instance %s: startsAt %q: %w", a.Fingerprint, a.StartsAt, err)
	}

	title := a.Labels["alertname"]
	if title == "" {
		title = a.Fingerprint
	}
	description := a.Annotations["description"]
	if description == "" {
		description = a.Annotations["summary"]
	}

	ruleUID := a.Labels[ruleUIDLabel]
	url := a.GeneratorURL
	if ruleUID != "" {
		url = baseURL + "/alerting/grafana/" + ruleUID + "/view"
	}

	attrs := make(map[string]string, len(a.Labels)+len(a.Annotations))
	for k, v := range a.Labels {
		attrs["label."+k] = v
	}
	for k, v := range a.Annotations {
		attrs["annotation."+k] = v
	}
	if len(attrs) == 0 {
		attrs = nil
	}

	ext := grafanaExtension{GeneratorURL: a.GeneratorURL, RuleUID: ruleUID, Folder: a.Labels["grafana_folder"]}
	for _, r := range a.Receivers {
		ext.Receivers = append(ext.Receivers, r.Name)
	}
	sort.Strings(ext.Receivers)
	extJSON, err := json.Marshal(ext)
	if err != nil {
		return schema.Alert{}, fmt.Errorf("marshal extension: %w", err)
	}

	return schema.Alert{
		ID:          idPrefix + a.Fingerprint,
		Provider:    providerName,
		Title:       title,
		Description: description,
		Severity:    severityFromLabel(a.Labels["severity"]),
		Since:       started.UTC().Format(time.RFC3339),
		URL:         url,
		Attributes:  attrs,
		Extensions:  map[string]json.RawMessage{providerName: extJSON},
		AsOf:        asOf,
		Stale:       false,
	}, nil
}
