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
	"strings"
	"time"
)

// alertsPath is Grafana's built-in Alertmanager v2 alerts endpoint.
const alertsPath = "/api/alertmanager/grafana/api/v2/alerts"

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

// HTTPClient is the production Transport.
type HTTPClient struct {
	client *http.Client
}

// NewHTTPClient returns a Transport over a plain net/http client.
func NewHTTPClient() *HTTPClient {
	return &HTTPClient{client: &http.Client{Timeout: defaultTimeout}}
}

// Alerts implements Transport.
func (c *HTTPClient) Alerts(ctx context.Context, baseURL string, filters []string) ([]apiAlert, error) {
	u, err := url.Parse(strings.TrimRight(baseURL, "/") + alertsPath)
	if err != nil {
		return nil, fmt.Errorf("build alerts URL: %w", err)
	}
	q := u.Query()
	for _, f := range filters {
		q.Add("filter", f)
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("build alerts request: %w", err)
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

	var alerts []apiAlert
	if err := json.Unmarshal(body, &alerts); err != nil {
		return nil, fmt.Errorf("malformed grafana response: %w", err)
	}
	return alerts, nil
}
