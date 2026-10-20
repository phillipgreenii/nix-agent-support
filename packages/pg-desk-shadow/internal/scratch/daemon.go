package scratch

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
)

// The daemon (pg-connector-github) the shadow run starts as its own child. The
// harness has no compile-time dependency on the daemon module: every name here
// is a contract with its configuration file and environment, pinned by a test
// against a copy of the daemon's example configuration.
const (
	// DaemonBinary is the daemon and client binary.
	DaemonBinary = "pg-connector-github"
	// DefaultDaemonPointsPerHour is the shadow cap on GraphQL points per hour
	// (the daemon's own DefaultGraphQLPointsPerHour); the operator MAY raise it
	// at prepare time.
	DefaultDaemonPointsPerHour = 1500
	// MaxDaemonPointsPerHour bounds the prepare flag.
	MaxDaemonPointsPerHour = 100000
	// MaxSocketPathBytes is the longest unix socket path accepted: 104 is the
	// darwin sun_path size including the terminating NUL.
	MaxSocketPathBytes = 103
)

// DaemonStateDir is the daemon's state directory (events.jsonl, daemon and
// client logs, the store).
func (l Layout) DaemonStateDir() string { return l.j("state", DaemonBinary) }

// DaemonConfig is the rendered daemon configuration file.
func (l Layout) DaemonConfig() string { return l.j("config", DaemonBinary+".json") }

// DaemonSocket is the daemon's unix socket (short, under the run directory).
func (l Layout) DaemonSocket() string { return l.j("run", "pcg.sock") }

// DaemonLog receives the daemon child's own stdout and stderr.
func (l Layout) DaemonLog() string { return l.j("logs", "daemon.out.log") }

// DaemonGH is the read-only gh shim the daemon is pointed at.
func (l Layout) DaemonGH() string { return l.j("bin", "gh") }

// ValidateDaemonPoints refuses a cap outside 1..MaxDaemonPointsPerHour.
func ValidateDaemonPoints(n int) error {
	if n < 1 || n > MaxDaemonPointsPerHour {
		return fmt.Errorf("the daemon points-per-hour cap must be an integer from 1 to %d, got %d", MaxDaemonPointsPerHour, n)
	}
	return nil
}

// ValidateSocketPath refuses a scratch directory whose daemon socket path does
// not fit the platform's unix socket limit.
func (l Layout) ValidateSocketPath() error {
	if n := len(l.DaemonSocket()); n > MaxSocketPathBytes {
		return fmt.Errorf("the daemon socket path %s is %d bytes, over the %d-byte unix socket limit: use a shorter --scratch directory", l.DaemonSocket(), n, MaxSocketPathBytes)
	}
	return nil
}

// DaemonQueries maps a watched query name to its native-syntax expression: a
// string or a list of strings, the shape of pr.queries in the daemon's
// configuration.
type DaemonQueries map[string]any

// ParseDaemonQueries reads the --daemon-queries-file document: one JSON object
// whose values are non-empty strings or non-empty arrays of non-empty strings.
// An error names the key, never the expression.
func ParseDaemonQueries(raw []byte) (DaemonQueries, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	var doc map[string]json.RawMessage
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("the daemon queries file is not one JSON object: %v", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("the daemon queries file holds more than one JSON value")
	}
	out := DaemonQueries{}
	for k, v := range doc {
		var s string
		if err := json.Unmarshal(v, &s); err == nil {
			if strings.TrimSpace(s) == "" {
				return nil, fmt.Errorf("the daemon queries file has an empty expression for query %q", k)
			}
			out[k] = s
			continue
		}
		var list []string
		if err := json.Unmarshal(v, &list); err != nil {
			return nil, fmt.Errorf("the daemon queries file value for query %q is neither a string nor an array of strings", k)
		}
		if len(list) == 0 {
			return nil, fmt.Errorf("the daemon queries file has an empty list for query %q", k)
		}
		for _, e := range list {
			if strings.TrimSpace(e) == "" {
				return nil, fmt.Errorf("the daemon queries file has an empty expression in the list for query %q", k)
			}
		}
		out[k] = list
	}
	return out, nil
}

// RenderDaemonConfig renders the daemon's strict-key JSON configuration for
// this scratch directory: state_dir, socket_path, gh_path, pr.queries (only the
// watched names, each of which MUST be present in q) and
// graphql_points_per_hour. Every other key is left to the daemon's defaults.
// A missing query is refused naming the query, never an expression.
func (l Layout) RenderDaemonConfig(watched []string, q DaemonQueries, pointsPerHour int) ([]byte, error) {
	if err := ValidateDaemonPoints(pointsPerHour); err != nil {
		return nil, err
	}
	queries := map[string]any{}
	var missing []string
	for _, name := range watched {
		e, ok := q[name]
		if !ok {
			missing = append(missing, name)
			continue
		}
		queries[name] = e
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, fmt.Errorf("the daemon queries file has no native-syntax expression for watched query %s", quoteList(missing))
	}
	doc := map[string]any{
		"state_dir":               l.DaemonStateDir(),
		"socket_path":             l.DaemonSocket(),
		"gh_path":                 filepath.Join(l.BinDir(), "gh"),
		"pr":                      map[string]any{"queries": queries},
		"graphql_points_per_hour": pointsPerHour,
	}
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

func quoteList(names []string) string {
	q := make([]string, len(names))
	for i, n := range names {
		q[i] = fmt.Sprintf("%q", n)
	}
	return strings.Join(q, ", ")
}
