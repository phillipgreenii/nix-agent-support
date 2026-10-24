package backfill

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"
)

// DefaultFloor is the GraphQL budget below which the driver stops: the floor the
// shadow runbook uses.
const DefaultFloor = 3000

// The two sources of the rate-limit signal, the values of the `rate_source`
// configuration key. Before the cutover the signal is the old backend's event log;
// after it, the daemon's metrics.
const (
	SourceOldBackendLog = "old-backend-log"
	SourceDaemonMetrics = "daemon-metrics"
)

// DefaultMetricsQuery is the daemon's gauge of the GraphQL points left in the
// current window (the daemon exports it as pg_connector_github.budget.remaining,
// label bucket).
const DefaultMetricsQuery = `pg_connector_github_budget_remaining{bucket="graphql"}`

// DefaultMetricsMaxAge is how old the newest metrics sample may be before the
// signal counts as unavailable, and DefaultLogMaxAge the same for the old backend's
// log (the GraphQL window is an hour; a log nothing has written to for longer is
// not a reading of the budget now, for example after the old backend is gone).
const (
	DefaultMetricsMaxAge = 15 * time.Minute
	DefaultLogMaxAge     = time.Hour
)

// Reading is one observation of the GraphQL points left.
type Reading struct {
	Remaining int
	// At is when the reading was taken.
	At time.Time
	// Source says where it came from (a file path or a URL), for the last line.
	Source string
}

// RateSource gives the NEWEST reading of the GraphQL points left. It returns an
// error when there is none: the driver fails closed on an unreadable signal
// rather than running blind against a shared token.
type RateSource interface {
	Newest(ctx context.Context) (Reading, error)
}

// LogSource reads the old backend's event log (events.jsonl): the newest row that
// carries `graphql_remaining` and a parseable `time`, by time, a tie going to the
// later line. When the live file holds no such row, its rotated `.1` sibling is
// read.
type LogSource struct {
	Path string
	// MaxAge, when positive, is how old the newest reading may be (by Now, default
	// the clock): an older one is an error, not a reading.
	MaxAge time.Duration
	Now    func() time.Time
}

const logTailBytes = 4 << 20

// Newest implements RateSource.
func (s LogSource) Newest(context.Context) (Reading, error) {
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	for _, p := range []string{s.Path, s.Path + ".1"} {
		b, err := readTail(p, logTailBytes)
		if err != nil {
			return Reading{}, fmt.Errorf("read the old backend's log %s: %w", p, err)
		}
		if r, ok := newestInLog(bytes.NewReader(b)); ok {
			r.Source = p
			if s.MaxAge > 0 && now().Sub(r.At) > s.MaxAge {
				return Reading{}, fmt.Errorf("the newest reading in the old backend's log %s is from %s, older than %s: the log is not being written (is rate_source still old-backend-log after the daemon cutover?)", p, r.At.Format(time.RFC3339), s.MaxAge)
			}
			return r, nil
		}
	}
	return Reading{}, fmt.Errorf("the old backend's log %s holds no graphql_remaining reading", s.Path)
}

func newestInLog(r io.Reader) (Reading, bool) {
	var best Reading
	found := false
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		var row struct {
			Time      string `json:"time"`
			Remaining *int   `json:"graphql_remaining"`
		}
		if json.Unmarshal(sc.Bytes(), &row) != nil || row.Remaining == nil {
			continue
		}
		t := parseTime(row.Time)
		if t.IsZero() {
			continue
		}
		if !found || !t.Before(best.At) {
			best, found = Reading{Remaining: *row.Remaining, At: t}, true
		}
	}
	return best, found
}

// readTail returns the last max bytes of a file, starting at a line boundary. A
// missing file is no bytes.
func readTail(path string, max int64) ([]byte, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	off := int64(0)
	if st.Size() > max {
		off = st.Size() - max
	}
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return nil, err
	}
	b, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	if off > 0 {
		if i := bytes.IndexByte(b, '\n'); i >= 0 {
			b = b[i+1:]
		}
	}
	return b, nil
}

// MetricsSource reads the daemon's gauge through a Prometheus-compatible HTTP API
// (an instant query against the local metrics store the daemon exports to). With
// several series (one per window) the smallest value decides, as the lowest bucket
// is the one that runs out first.
type MetricsSource struct {
	// BaseURL is the API's base, for example http://127.0.0.1:9090.
	BaseURL string
	// Query defaults to DefaultMetricsQuery.
	Query string
	// MaxAge defaults to DefaultMetricsMaxAge: a sample older than this is not a
	// reading.
	MaxAge time.Duration
	Client *http.Client
	Now    func() time.Time
}

// Newest implements RateSource.
func (s MetricsSource) Newest(ctx context.Context) (Reading, error) {
	q := s.Query
	if q == "" {
		q = DefaultMetricsQuery
	}
	maxAge := s.MaxAge
	if maxAge == 0 {
		maxAge = DefaultMetricsMaxAge
	}
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	client := s.Client
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	u, err := url.Parse(s.BaseURL)
	if err != nil || s.BaseURL == "" {
		return Reading{}, fmt.Errorf("the daemon metrics URL %q is not usable", s.BaseURL)
	}
	u = u.JoinPath("api", "v1", "query")
	vals := u.Query()
	vals.Set("query", q)
	u.RawQuery = vals.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return Reading{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return Reading{}, fmt.Errorf("query the daemon metrics at %s: %w", s.BaseURL, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return Reading{}, fmt.Errorf("read the daemon metrics answer: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return Reading{}, fmt.Errorf("the daemon metrics answered HTTP %d", resp.StatusCode)
	}
	var ans struct {
		Status string `json:"status"`
		Data   struct {
			Result []struct {
				Value []json.RawMessage `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &ans); err != nil || ans.Status != "success" {
		return Reading{}, fmt.Errorf("the daemon metrics answer is not a successful query result")
	}
	var best Reading
	found := false
	for _, r := range ans.Data.Result {
		if len(r.Value) != 2 {
			continue
		}
		var ts float64
		var sv string
		if json.Unmarshal(r.Value[0], &ts) != nil || json.Unmarshal(r.Value[1], &sv) != nil {
			continue
		}
		v, err := strconv.ParseFloat(sv, 64)
		if err != nil {
			continue
		}
		at := time.Unix(int64(ts), 0).UTC()
		if now().Sub(at) > maxAge {
			continue
		}
		if !found || int(v) < best.Remaining {
			best, found = Reading{Remaining: int(v), At: at, Source: s.BaseURL}, true
		}
	}
	if !found {
		return Reading{}, fmt.Errorf("the daemon metrics hold no fresh sample of %s (newer than %s)", q, maxAge)
	}
	return best, nil
}
