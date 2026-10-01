// client.go: the HTTP transport to a local Grafana's Alertmanager v2 alerts
// API — this backend's own injectable seam, mirroring
// cmd/pg-connector-calendar-osx-bridge/internal/client.go's "production dials
// for real, tests inject a fake" structure. Grafana needs no authentication
// (design 10), so no credential is read or sent.
package internal

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// alertsPath is Grafana's built-in Alertmanager v2 alerts endpoint.
const alertsPath = "/api/alertmanager/grafana/api/v2/alerts"

// rulerRulesPath is Grafana's ruler endpoint that enumerates every alerting
// rule definition (grouped by folder).
const rulerRulesPath = "/api/ruler/grafana/api/v1/rules"

// historyPath is Grafana's per-rule state-history endpoint; ruleUID is
// REQUIRED (there is no bulk endpoint), so history is one call per rule.
const historyPath = "/api/v1/rules/history"

// maxResponseBytes bounds how much of a response body is read.
const maxResponseBytes = 32 << 20

// defaultTimeout is the HTTP client's per-request deadline.
const defaultTimeout = 15 * time.Second

// Transport fetches the raw alert instances from one Grafana. filters are
// already-translated Alertmanager `filter=` values (matcher.go), sent as
// repeated filter= query parameters. Any error returned means the source is
// unreachable, answered an HTTP error, or answered malformed JSON, and the
// caller reports it as unavailable.
type Transport interface {
	Alerts(ctx context.Context, baseURL string, filters []string) ([]apiAlert, error)
	// Rules enumerates the alerting rule definitions (uid and title). Any
	// error means unreachable, HTTP error or malformed JSON.
	Rules(ctx context.Context, baseURL string) ([]apiRule, error)
	// RuleHistory fetches one rule's state-history frame for [from, to]
	// (unix seconds on the wire), at most limit rows. Same error contract as
	// Alerts. It is called ONLY by ListHistory, never by attention.
	RuleHistory(ctx context.Context, baseURL, ruleUID string, from, to time.Time, limit int) (apiHistory, error)
}

// apiAlert mirrors the subset of the Alertmanager v2 GettableAlert JSON this
// backend reads, by struct tag (never an import of any Grafana type).
type apiAlert struct {
	Annotations  map[string]string `json:"annotations"`
	Labels       map[string]string `json:"labels"`
	Fingerprint  string            `json:"fingerprint"`
	StartsAt     string            `json:"startsAt"`
	GeneratorURL string            `json:"generatorURL"`
	Receivers    []struct {
		Name string `json:"name"`
	} `json:"receivers"`
	Status struct {
		State string `json:"state"`
	} `json:"status"`
}

// apiRule is the identity of one alerting rule, from the ruler's
// grafana_alert object.
type apiRule struct {
	UID   string `json:"uid"`
	Title string `json:"title"`
}

// apiHistory is the state-history frame: `{data: {values: [times[], texts[],
// prevs[], nexts[], datas[]]}}`, column-oriented, one row per transition.
// Columns stay raw here; history.go validates and decodes them so a malformed
// frame is a handled error, not a decode panic.
type apiHistory struct {
	Data *struct {
		Values []json.RawMessage `json:"values"`
	} `json:"data"`
}

// HTTPClient is the production Transport.
type HTTPClient struct {
	client *http.Client
}

// NewHTTPClient returns a Transport over a plain net/http client.
func NewHTTPClient() *HTTPClient {
	return &HTTPClient{client: &http.Client{Timeout: defaultTimeout}}
}

// get performs one GET against baseURL+path with query q and returns the 2xx
// body. Every failure mode is an error the caller reports as unavailable.
func (c *HTTPClient) get(ctx context.Context, baseURL, path string, q url.Values) ([]byte, error) {
	u, err := url.Parse(strings.TrimRight(baseURL, "/") + path)
	if err != nil {
		return nil, fmt.Errorf("build %s URL: %w", path, err)
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("build %s request: %w", path, err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("grafana unreachable: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("read grafana response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("grafana answered HTTP %d", resp.StatusCode)
	}
	return body, nil
}

// Alerts implements Transport.
func (c *HTTPClient) Alerts(ctx context.Context, baseURL string, filters []string) ([]apiAlert, error) {
	q := url.Values{}
	for _, f := range filters {
		q.Add("filter", f)
	}
	body, err := c.get(ctx, baseURL, alertsPath, q)
	if err != nil {
		return nil, err
	}
	var alerts []apiAlert
	if err := json.Unmarshal(body, &alerts); err != nil {
		return nil, fmt.Errorf("malformed grafana response: %w", err)
	}
	return alerts, nil
}

// Rules implements Transport: it flattens the ruler's
// `{<folder>: [{name, rules: [{grafana_alert: {uid, title}}]}]}` object into
// a list, folders in sorted order so the result is deterministic.
func (c *HTTPClient) Rules(ctx context.Context, baseURL string) ([]apiRule, error) {
	body, err := c.get(ctx, baseURL, rulerRulesPath, url.Values{})
	if err != nil {
		return nil, err
	}
	var groups map[string][]struct {
		Rules []struct {
			GrafanaAlert apiRule `json:"grafana_alert"`
		} `json:"rules"`
	}
	if err := json.Unmarshal(body, &groups); err != nil {
		return nil, fmt.Errorf("malformed grafana rules response: %w", err)
	}
	folders := make([]string, 0, len(groups))
	for f := range groups {
		folders = append(folders, f)
	}
	sort.Strings(folders)
	rules := []apiRule{}
	for _, f := range folders {
		for _, g := range groups[f] {
			for _, r := range g.Rules {
				rules = append(rules, r.GrafanaAlert)
			}
		}
	}
	return rules, nil
}

// RuleHistory implements Transport.
func (c *HTTPClient) RuleHistory(ctx context.Context, baseURL, ruleUID string, from, to time.Time, limit int) (apiHistory, error) {
	q := url.Values{}
	q.Set("ruleUID", ruleUID)
	q.Set("from", strconv.FormatInt(from.Unix(), 10))
	q.Set("to", strconv.FormatInt(to.Unix(), 10))
	q.Set("limit", strconv.Itoa(limit))
	body, err := c.get(ctx, baseURL, historyPath, q)
	if err != nil {
		return apiHistory{}, err
	}
	var h apiHistory
	if err := json.Unmarshal(body, &h); err != nil {
		return apiHistory{}, fmt.Errorf("malformed grafana history response: %w", err)
	}
	return h, nil
}
