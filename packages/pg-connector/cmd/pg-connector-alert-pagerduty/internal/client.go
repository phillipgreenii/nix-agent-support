// client.go: the HTTP transport to the PagerDuty REST API v2, this backend's
// own injectable seam (production dials for real, tests inject a fake).
package internal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	// DefaultBaseURL is PagerDuty's REST API.
	DefaultBaseURL = "https://api.pagerduty.com"

	pageLimit        = 100
	maxPages         = 20 // bounds a runaway `more` loop at 2000 incidents
	maxResponseBytes = 32 << 20
	defaultTimeout   = 15 * time.Second
)

var (
	// errUnauthorized: PagerDuty answered 401 or 403.
	errUnauthorized = errors.New("pagerduty rejected the API key")
	// errMissing: PagerDuty answered 404.
	errMissing = errors.New("pagerduty resource not found")
)

// Transport fetches raw incidents. rawQuery is an already-encoded, ready-to-send
// query string (repeated-key arrays with literal brackets, e.g.
// `statuses[]=triggered&statuses[]=acknowledged`); the transport only appends
// paging parameters. Errors other than errUnauthorized and errMissing mean
// unreachable, HTTP error or malformed JSON.
type Transport interface {
	Incidents(ctx context.Context, baseURL, token, rawQuery string) ([]apiIncident, error)
	Incident(ctx context.Context, baseURL, token, id string) (*apiIncident, error)
	IncidentAlerts(ctx context.Context, baseURL, token, id string) ([]apiIncidentAlert, error)
}

// apiIncident mirrors the subset of the incident JSON this backend reads.
type apiIncident struct {
	ID             string `json:"id"`
	IncidentNumber int    `json:"incident_number"`
	Title          string `json:"title"`
	Description    string `json:"description"`
	Status         string `json:"status"`
	Urgency        string `json:"urgency"`
	CreatedAt      string `json:"created_at"`
	HTMLURL        string `json:"html_url"`
	Service        struct {
		ID      string `json:"id"`
		Summary string `json:"summary"`
	} `json:"service"`
}

// apiIncidentAlert is one alert under an incident.
type apiIncidentAlert struct {
	ID       string `json:"id"`
	Severity string `json:"severity"`
}

// HTTPClient is the production Transport.
type HTTPClient struct{ client *http.Client }

// NewHTTPClient returns a Transport over a plain net/http client.
func NewHTTPClient() *HTTPClient {
	return &HTTPClient{client: &http.Client{Timeout: defaultTimeout}}
}

func (c *HTTPClient) get(ctx context.Context, baseURL, token, path, rawQuery string) ([]byte, error) {
	u := strings.TrimRight(baseURL, "/") + path
	if rawQuery != "" {
		u += "?" + rawQuery
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("build %s request: %w", path, err)
	}
	req.Header.Set("Authorization", "Token token="+token)
	req.Header.Set("Accept", "application/vnd.pagerduty+json;version=2")

	resp, err := c.client.Do(req)
	if err != nil {
		// Never echo the URL-bearing error text beyond the cause; no token is in it.
		return nil, fmt.Errorf("pagerduty unreachable: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("read pagerduty response: %w", err)
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, errUnauthorized
	case resp.StatusCode == http.StatusNotFound:
		return nil, errMissing
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		return nil, fmt.Errorf("pagerduty answered HTTP %d", resp.StatusCode)
	}
	return body, nil
}

// Incidents implements Transport, following offset paging while `more`.
func (c *HTTPClient) Incidents(ctx context.Context, baseURL, token, rawQuery string) ([]apiIncident, error) {
	var all []apiIncident
	for page := 0; page < maxPages; page++ {
		q := rawQuery + "&limit=" + strconv.Itoa(pageLimit) + "&offset=" + strconv.Itoa(page*pageLimit)
		body, err := c.get(ctx, baseURL, token, "/incidents", strings.TrimPrefix(q, "&"))
		if err != nil {
			return nil, err
		}
		var resp struct {
			Incidents []apiIncident `json:"incidents"`
			More      bool          `json:"more"`
		}
		if err := json.Unmarshal(body, &resp); err != nil {
			return nil, fmt.Errorf("malformed pagerduty incidents response: %w", err)
		}
		all = append(all, resp.Incidents...)
		if !resp.More {
			return all, nil
		}
	}
	return all, nil
}

// Incident implements Transport.
func (c *HTTPClient) Incident(ctx context.Context, baseURL, token, id string) (*apiIncident, error) {
	body, err := c.get(ctx, baseURL, token, "/incidents/"+url.PathEscape(id), "")
	if err != nil {
		return nil, err
	}
	var resp struct {
		Incident *apiIncident `json:"incident"`
	}
	if err := json.Unmarshal(body, &resp); err != nil || resp.Incident == nil {
		return nil, fmt.Errorf("malformed pagerduty incident response")
	}
	return resp.Incident, nil
}

// IncidentAlerts implements Transport.
func (c *HTTPClient) IncidentAlerts(ctx context.Context, baseURL, token, id string) ([]apiIncidentAlert, error) {
	body, err := c.get(ctx, baseURL, token, "/incidents/"+url.PathEscape(id)+"/alerts", "limit="+strconv.Itoa(pageLimit))
	if err != nil {
		return nil, err
	}
	var resp struct {
		Alerts []apiIncidentAlert `json:"alerts"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("malformed pagerduty alerts response: %w", err)
	}
	return resp.Alerts, nil
}
