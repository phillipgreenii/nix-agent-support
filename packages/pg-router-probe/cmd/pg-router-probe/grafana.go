// grafana.go: this binary's own small, read-only Grafana alerting HTTP
// client [design: "pg-router-probe run checks" item 1]. "Same Grafana
// access pattern lat-survey already uses" is a pattern-reuse citation
// only, not a code dependency: lat-survey lives in the separate
// phillipg-nix-ziprecruiter repo, and this repo's own CLAUDE.md forbids a
// dependency on another custom flake, so this client is implemented from
// scratch here [design: same item, Contract's "Grafana" bullet].
//
// Endpoint/response-shape choice (this packet's own implementation
// choice, no design citation beyond "read-only Grafana alerting API"):
// Grafana's Alertmanager-compatible API,
// GET {base}/api/alertmanager/grafana/api/v2/alerts, returns every
// current alert instance with its own labels (unified alerting stamps
// __alert_rule_uid__ onto every instance) and status.state ("active" for
// firing, "suppressed" otherwise) — filtering to the 4 registered rule
// UIDs happens client-side here, after decode, rather than server-side
// via a query parameter, so this client's own correctness does not
// depend on Grafana's query-parameter support for that filter.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// grafanaAlert is the subset of one Grafana alert instance this probe
// needs.
type grafanaAlert struct {
	RuleUID      string
	Labels       map[string]string
	State        string
	EpisodeCount int
}

// grafanaAlertEvidence renders one alert's raw evidence block for the bd
// body template's "Evidence:" section [design: "Body template" code
// block].
func grafanaAlertEvidence(a grafanaAlert) string {
	return fmt.Sprintf("rule_uid=%s\nstate=%s\nlabels=%v\nepisode_count=%d", a.RuleUID, a.State, a.Labels, a.EpisodeCount)
}

// grafanaAlertInstance is the wire shape one element of the Alertmanager
// v2 `/api/v2/alerts` response array decodes into — only the fields this
// probe reads.
type grafanaAlertInstance struct {
	Labels map[string]string `json:"labels"`
	Status struct {
		State string `json:"state"`
	} `json:"status"`
}

// grafanaClient is this binary's own Grafana alerting client. httpClient
// MUST already carry an explicit timeout (set by its caller, run.go, via
// context.WithTimeout on every call below) — command-type roles get no
// watchdog at all in pg-router-ccpool-handler, so a hang here is
// invisible to pg-router's own dispatch-failure tracking [Binding
// decisions: "every external call in run MUST carry an explicit
// timeout"].
type grafanaClient struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

func newGrafanaClient(baseURL, token string, httpClient *http.Client) *grafanaClient {
	return &grafanaClient{baseURL: baseURL, token: token, httpClient: httpClient}
}

// firingAlerts fetches every current alert instance and returns only
// those whose __alert_rule_uid__ label matches one of ruleUIDs and whose
// status is "active" (Grafana's Alertmanager-API term for firing) — the
// filtering to the 4 registered rule UIDs this packet's own Contract
// requires [design: "pg-router-probe run checks" item 1].
func (c *grafanaClient) firingAlerts(ctx context.Context, ruleUIDs []string) ([]grafanaAlert, error) {
	url := c.baseURL + "/api/alertmanager/grafana/api/v2/alerts"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("grafana: build request: %w", err)
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("grafana: request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("grafana: read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("grafana: unexpected status %d: %s", resp.StatusCode, string(body))
	}

	var instances []grafanaAlertInstance
	if err := json.Unmarshal(body, &instances); err != nil {
		return nil, fmt.Errorf("grafana: decode response: %w", err)
	}

	wanted := make(map[string]bool, len(ruleUIDs))
	for _, u := range ruleUIDs {
		wanted[u] = true
	}

	alerts := make([]grafanaAlert, 0, len(instances))
	for _, inst := range instances {
		if inst.Status.State != "active" {
			continue
		}
		ruleUID := inst.Labels["__alert_rule_uid__"]
		if !wanted[ruleUID] {
			continue
		}
		alerts = append(alerts, grafanaAlert{
			RuleUID: ruleUID,
			Labels:  inst.Labels,
			State:   inst.Status.State,
			// EpisodeCount has no Alertmanager v2 wire equivalent this
			// client decodes today (no design citation for a concrete
			// source) -- left at zero; dedup.go's own grafana comparison
			// still works (state/severity alone already drives it), and a
			// future revision can populate this once a real episode-count
			// source is identified.
			EpisodeCount: 0,
		})
	}
	return alerts, nil
}
