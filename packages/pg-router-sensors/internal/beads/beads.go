// Package beads implements beads-sensor: for each configured tracker it reads
// beads_exporter_up{db="<db>"} from the beads-exporter metrics text and answers
// the resource beads_<tracker> on the UpDown scale (1 is UP, 0 is DOWN). The
// consecutive-failure count behind a 0 is the exporter's own. The sensor reads
// the exporter's metric text over HTTP only and never runs bd.
//
// Config keys: exporter_url (required, no default), timeout (duration, default
// 5s), trackers (required table, <tracker> = "<db label>"). A tracker with no
// series in the text emits no line (the router then reads unknown, the best
// level). A GET that times out, is refused or answers a status other than 200
// is a measurement failure: no line, the cause on stderr, exit status 1.
//
// Exported names: Sensor, New, Parse, MetricUp, DefaultTimeout, ResourcePrefix.
package beads

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/phillipgreenii/pg-router-sensors/internal/levels"
	"github.com/phillipgreenii/pg-router-sensors/internal/sensorcli"
)

// Names and defaults.
const (
	// MetricUp is the exporter family the sensor reads.
	MetricUp = "beads_exporter_up"
	// ResourcePrefix prefixes each tracker name to form its resource.
	ResourcePrefix = "beads_"
	// DefaultTimeout is the GET timeout (strawman, configurable).
	DefaultTimeout = 5 * time.Second
	maxBody        = 8 << 20
)

// trackerName is the shape of a full resource name (the answer schema pattern).
var trackerName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// Sensor is the beads-sensor probe set.
type Sensor struct {
	// Client is the HTTP client (nil: a default client).
	Client   *http.Client
	URL      string
	Timeout  time.Duration
	Trackers map[string]string // tracker name -> db label
}

// New returns an unconfigured Sensor.
func New() *Sensor { return &Sensor{Timeout: DefaultTimeout} }

// Resources implements sensorcli.Sensor: beads_<tracker> per tracker, sorted.
func (s *Sensor) Resources() []string {
	names := make([]string, 0, len(s.Trackers))
	for t := range s.Trackers {
		names = append(names, ResourcePrefix+t)
	}
	sort.Strings(names)
	return names
}

// Bands implements sensorcli.Sensor: a two-level scale has no bands.
func (s *Sensor) Bands() []levels.Info {
	var out []levels.Info
	for _, r := range s.Resources() {
		out = append(out, levels.NoBands(r, "exporter_up"))
	}
	return out
}

// Configure implements sensorcli.Sensor.
func (s *Sensor) Configure(cfg sensorcli.Config) error {
	if err := cfg.CheckKeys("exporter_url", "timeout", "trackers"); err != nil {
		return err
	}
	raw, err := cfg.String("exporter_url", "")
	if err != nil {
		return err
	}
	u, perr := url.Parse(raw)
	if raw == "" || perr != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("config key exporter_url: want an http(s) URL, got %q", raw)
	}
	timeout, err := cfg.Duration("timeout", DefaultTimeout)
	if err != nil {
		return err
	}
	if timeout == 0 {
		return fmt.Errorf("config key timeout: must be positive")
	}
	trackers, err := cfg.Table("trackers")
	if err != nil {
		return err
	}
	if len(trackers) == 0 {
		return fmt.Errorf("config key trackers: want a non-empty table of <tracker> = \"<db label>\"")
	}
	for t, db := range trackers {
		if !trackerName.MatchString(ResourcePrefix+t) || db == "" {
			return fmt.Errorf("config key trackers: invalid entry %q = %q (a tracker name is lower-case letters, digits and underscores, and beads_<tracker> is at most 64 characters)", t, db)
		}
	}
	s.URL, s.Timeout, s.Trackers = raw, timeout, trackers
	return nil
}

// Measure implements sensorcli.Sensor.
func (s *Sensor) Measure(ctx context.Context, env sensorcli.Env) ([]levels.Observation, error) {
	if s.URL == "" || len(s.Trackers) == 0 {
		return nil, levels.ErrNoReading
	}
	body, err := s.fetch(ctx)
	if err != nil {
		return nil, err
	}
	up := Parse(body)
	var obs []levels.Observation
	for _, res := range s.Resources() {
		if env.Resource != "" && env.Resource != res {
			continue
		}
		db := s.Trackers[strings.TrimPrefix(res, ResourcePrefix)]
		v, ok := up[db]
		if !ok {
			fmt.Fprintf(env.Stderr, "%s: no %s series for db %q\n", res, MetricUp, db)
			continue
		}
		level := levels.UpDown.Worst()
		if v {
			level = levels.UpDown.Best()
		}
		obs = append(obs, levels.Observation{Resource: res, Scale: levels.UpDown, Level: level})
	}
	return obs, nil
}

func (s *Sensor) fetch(ctx context.Context) (string, error) {
	client := http.DefaultClient
	if s.Client != nil {
		client = s.Client
	}
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.URL, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("exporter GET: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("exporter GET: status %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return "", fmt.Errorf("exporter read: %w", err)
	}
	return string(b), nil
}

// Parse extracts beads_exporter_up from Prometheus text: db label to healthy.
// A value other than 0 or 1, a sample without a db label and comment lines are
// skipped. When a db has several series, any 0 wins (DOWN).
func Parse(text string) map[string]bool {
	out := map[string]bool{}
	sc := bufio.NewScanner(strings.NewReader(text))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, MetricUp+"{") {
			continue
		}
		end := strings.LastIndex(line, "}")
		if end < 0 {
			continue
		}
		db, ok := label(line[len(MetricUp)+1:end], "db")
		if !ok {
			continue
		}
		fields := strings.Fields(line[end+1:])
		if len(fields) == 0 {
			continue
		}
		f, err := strconv.ParseFloat(fields[0], 64)
		if err != nil || (f != 0 && f != 1) {
			continue
		}
		if prev, seen := out[db]; seen && !prev {
			continue
		}
		out[db] = f == 1
	}
	return out
}

// label returns the value of name in a `a="x",b="y"` label list.
func label(list, name string) (string, bool) {
	for list != "" {
		eq := strings.Index(list, `="`)
		if eq < 0 {
			return "", false
		}
		key := strings.TrimLeft(list[:eq], ", ")
		rest := list[eq+2:]
		i := 0
		var val strings.Builder
		for i < len(rest) && rest[i] != '"' {
			if rest[i] == '\\' && i+1 < len(rest) {
				i++
			}
			val.WriteByte(rest[i])
			i++
		}
		if i >= len(rest) {
			return "", false
		}
		if key == name {
			return val.String(), true
		}
		list = rest[i+1:]
	}
	return "", false
}
