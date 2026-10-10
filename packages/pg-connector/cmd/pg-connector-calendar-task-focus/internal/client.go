// client.go: the HTTP transport to the pg-task-focus daemon's loopback API,
// this backend's own injectable seam ("production dials for real, tests
// inject a fake", the same shape as cmd/pg-connector-alert-grafana's
// client). It depends on the daemon's HTTP contract only: the two reads
// GET /api/v1/calendar and GET /api/v1/attention, decoded into types defined
// here by struct tag, never an import of the daemon's Go packages and never a
// read of its event log. The daemon needs no authentication (it listens on
// loopback only), so no credential is read or sent.
package internal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-calendar-task-focus/internal/eventlog"
)

// API paths of the two reads this backend makes.
const (
	calendarPath  = "/api/v1/calendar"
	attentionPath = "/api/v1/attention"
)

// clientName is the X-Client value the daemon's closed set reserves for this
// backend; the daemon labels http_requests_total{client="connector"} with it.
const clientName = "connector"

// maxResponseBytes bounds how much of a response body is read.
const maxResponseBytes = 8 << 20

// defaultTimeout is the HTTP client's per-request deadline. It sits well under
// the wire protocol's own per-call budget, so a wedged daemon fails here with a
// message that names the daemon rather than at the backend deadline.
const defaultTimeout = 10 * time.Second

// Transport reads the daemon. Every error it returns is a *DaemonError, and
// the caller maps it onto the wire taxonomy.
type Transport interface {
	// Calendar reads the running segments overlapping [from, to), optionally
	// filtered to one calendar name or id.
	Calendar(ctx context.Context, baseURL string, from, to time.Time, calendar string) (apiCalendar, error)
	// Attention reads the attention feed.
	Attention(ctx context.Context, baseURL string) (apiAttention, error)
}

// DaemonError is a failed daemon request. Status is the HTTP status the daemon
// answered, or 0 when it never did (connection refused, timeout, unreadable
// body). Reason and Detail carry the RFC 9457 problem the daemon sent, when it
// sent one.
type DaemonError struct {
	Status int
	Reason string
	Detail string
	Stage  string
	Err    error
}

func (e *DaemonError) Error() string {
	switch {
	case e.Status == 0:
		return fmt.Sprintf("pg-task-focus daemon unreachable (is it running?): %v", e.Err)
	case e.Reason != "" && e.Detail != "":
		return fmt.Sprintf("pg-task-focus daemon answered HTTP %d %s: %s", e.Status, e.Reason, e.Detail)
	case e.Err != nil:
		return fmt.Sprintf("pg-task-focus daemon answered HTTP %d: %v", e.Status, e.Err)
	default:
		return fmt.Sprintf("pg-task-focus daemon answered HTTP %d", e.Status)
	}
}

func (e *DaemonError) Unwrap() error { return e.Err }

// apiSegment mirrors the daemon's Segment schema (api/openapi.yaml,
// components.schemas.Segment).
type apiSegment struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Start string `json:"start"`
	End   string `json:"end"`
	Notes string `json:"notes"`
}

// apiCalendar mirrors the daemon's Calendar schema.
type apiCalendar struct {
	AsOf       string       `json:"as_of"`
	Stale      bool         `json:"stale"`
	CalendarID string       `json:"calendar_id"`
	Calendar   string       `json:"calendar"`
	Events     []apiSegment `json:"events"`
}

// apiAttentionGroup mirrors the daemon's AttentionGroup schema.
type apiAttentionGroup struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

// apiAttentionItem mirrors the daemon's AttentionItem schema.
type apiAttentionItem struct {
	Type     string            `json:"type"`
	ID       string            `json:"id"`
	Summary  string            `json:"summary"`
	Severity string            `json:"severity"`
	Group    apiAttentionGroup `json:"group"`
	URL      string            `json:"url"`
}

// apiAttention mirrors the daemon's Attention schema.
type apiAttention struct {
	AsOf  string             `json:"as_of"`
	Items []apiAttentionItem `json:"items"`
}

// apiProblem is the subset of the daemon's RFC 9457 Problem this backend
// reads.
type apiProblem struct {
	Detail string `json:"detail"`
	Reason string `json:"reason"`
}

// HTTPClient is the production Transport.
type HTTPClient struct {
	client *http.Client
	// traceparent, when non-empty, is sent as the W3C traceparent header so the
	// daemon's span for the request joins the caller's trace.
	traceparent string
}

// NewHTTPClient returns a Transport over a plain net/http client. traceparent
// is the caller's W3C trace context, or "" for none.
func NewHTTPClient(traceparent string) *HTTPClient {
	return &HTTPClient{client: &http.Client{Timeout: defaultTimeout}, traceparent: strings.TrimSpace(traceparent)}
}

// get performs one GET against baseURL+path with query q and returns the 200
// body. Every failure is a *DaemonError and is recorded on the call's event
// (eventlog.RecordRequest); a 200 is recorded by decode, once the body is known
// good.
func (c *HTTPClient) get(ctx context.Context, baseURL, path string, q url.Values) ([]byte, error) {
	u, err := url.Parse(strings.TrimRight(baseURL, "/") + path)
	if err != nil {
		return nil, fail(ctx, &DaemonError{Stage: eventlog.StageConnect, Err: fmt.Errorf("build %s URL: %w", path, err)})
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fail(ctx, &DaemonError{Stage: eventlog.StageConnect, Err: fmt.Errorf("build %s request: %w", path, err)})
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Client", clientName)
	if c.traceparent != "" {
		req.Header.Set("traceparent", c.traceparent)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fail(ctx, &DaemonError{Stage: eventlog.StageConnect, Err: err})
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, fail(ctx, &DaemonError{Status: resp.StatusCode, Stage: eventlog.StageConnect, Err: fmt.Errorf("read response: %w", err)})
	}
	if len(body) > maxResponseBytes {
		return nil, fail(ctx, &DaemonError{Status: resp.StatusCode, Stage: eventlog.StageDecode, Err: errors.New("response exceeds the size limit")})
	}
	if resp.StatusCode != http.StatusOK {
		de := &DaemonError{Status: resp.StatusCode, Stage: eventlog.StageStatus}
		var p apiProblem
		if json.Unmarshal(body, &p) == nil {
			de.Reason, de.Detail = p.Reason, p.Detail
		}
		return nil, fail(ctx, de)
	}
	return body, nil
}

// fail records a failed request on the call's event and returns it.
func fail(ctx context.Context, e *DaemonError) error {
	eventlog.RecordRequest(ctx, e.Status, e.Stage)
	return e
}

// decode unmarshals a 200 body into v and records the request as answered: a
// body that does not decode is a DaemonError at the decode stage.
func decode(ctx context.Context, body []byte, v any) error {
	if err := json.Unmarshal(body, v); err != nil {
		return fail(ctx, &DaemonError{Status: http.StatusOK, Stage: eventlog.StageDecode, Err: fmt.Errorf("malformed response: %w", err)})
	}
	eventlog.RecordRequest(ctx, http.StatusOK, "")
	return nil
}

// Calendar implements Transport.
func (c *HTTPClient) Calendar(ctx context.Context, baseURL string, from, to time.Time, calendar string) (apiCalendar, error) {
	q := url.Values{}
	q.Set("from", from.UTC().Format(time.RFC3339Nano))
	q.Set("to", to.UTC().Format(time.RFC3339Nano))
	if calendar != "" {
		q.Set("calendar", calendar)
	}
	body, err := c.get(ctx, baseURL, calendarPath, q)
	if err != nil {
		return apiCalendar{}, err
	}
	var cal apiCalendar
	if err := decode(ctx, body, &cal); err != nil {
		return apiCalendar{}, err
	}
	return cal, nil
}

// Attention implements Transport.
func (c *HTTPClient) Attention(ctx context.Context, baseURL string) (apiAttention, error) {
	body, err := c.get(ctx, baseURL, attentionPath, url.Values{})
	if err != nil {
		return apiAttention{}, err
	}
	var att apiAttention
	if err := decode(ctx, body, &att); err != nil {
		return apiAttention{}, err
	}
	return att, nil
}
