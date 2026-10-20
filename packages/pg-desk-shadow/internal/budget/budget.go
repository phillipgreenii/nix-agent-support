// Package budget is the shared-token guard: the GitHub GraphQL token is shared
// with the live flow, so a shadow tick runs only when the newest reading
// leaves headroom above the floor.
package budget

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"time"
)

// ResetGrace is added to a window's reset time before its readings are void.
const ResetGrace = 5 * time.Second

// DefaultWindow is how far back readings are considered.
const DefaultWindow = 10 * time.Minute

// Reading is one graphql_remaining observation.
type Reading struct {
	Time      time.Time
	Remaining int
	ResetAt   time.Time
	Source    string // "live" | "live.1" | "scratch"
}

// Floor is max(2000, 1000 + maxPerPoll x 12 + margin): the connector's own
// reserve is 1000, 12 points is the rough cost of one hydration.
func Floor(maxPerPoll, margin int) int {
	f := 1000 + maxPerPoll*12 + margin
	if f < 2000 {
		f = 2000
	}
	return f
}

// SourceScratch is the ParseReadings source of the daemon's own event log.
// That log is read by kind=origin rows only; every other source keeps the old
// pr-github row format (no kind, with graphql_reset_at).
const SourceScratch = "scratch"

// ScratchWindow is the GraphQL budget window the origin rows are assigned to:
// the daemon's origin rows carry no reset time, and the window is hourly.
const ScratchWindow = time.Hour

// ParseReadings extracts the readings of a connector event log. Rows without
// graphql_remaining (start and heartbeat rows) are ignored.
//
// Source SourceScratch reads the daemon's log: only kind=origin rows of the
// graphql bucket (or with no bucket) that carry graphql_remaining count, and
// a row with a missing or unparseable time is skipped. Origin rows have no
// graphql_reset_at, so every reading of one call gets the same ResetAt, the
// newest reading's time plus ScratchWindow: they form one window in Decide and
// the newest wins. Aging is Gather's window alone (a reading older than it is
// dropped there and never decides a slot). Any other source keeps the old
// format: rows with no kind and a parseable graphql_reset_at.
func ParseReadings(r io.Reader, source string) []Reading {
	if source == SourceScratch {
		return parseScratch(r, source)
	}
	var out []Reading
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		var row struct {
			Time      string `json:"time"`
			Remaining *int   `json:"graphql_remaining"`
			ResetAt   string `json:"graphql_reset_at"`
		}
		if json.Unmarshal(sc.Bytes(), &row) != nil || row.Remaining == nil {
			continue
		}
		t, err1 := time.Parse(time.RFC3339Nano, row.Time)
		reset, err2 := time.Parse(time.RFC3339Nano, row.ResetAt)
		if err1 != nil || err2 != nil {
			continue
		}
		out = append(out, Reading{Time: t.UTC(), Remaining: *row.Remaining, ResetAt: reset.UTC(), Source: source})
	}
	return out
}

func parseScratch(r io.Reader, source string) []Reading {
	var out []Reading
	var newest time.Time
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		var row struct {
			Time      string `json:"time"`
			Kind      string `json:"kind"`
			Bucket    string `json:"bucket"`
			Remaining *int   `json:"graphql_remaining"`
		}
		if json.Unmarshal(sc.Bytes(), &row) != nil || row.Kind != "origin" || row.Remaining == nil {
			continue
		}
		if row.Bucket != "" && row.Bucket != "graphql" {
			continue
		}
		t, err := time.Parse(time.RFC3339Nano, row.Time)
		if err != nil {
			continue
		}
		t = t.UTC()
		if t.After(newest) {
			newest = t
		}
		out = append(out, Reading{Time: t, Remaining: *row.Remaining, Source: source})
	}
	reset := newest.Add(ScratchWindow)
	for i := range out {
		out[i].ResetAt = reset
	}
	return out
}

// ReadTail returns the last maxBytes of a file (starting at a line boundary).
func ReadTail(path string, maxBytes int64) ([]byte, error) {
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
	if st.Size() > maxBytes {
		off = st.Size() - maxBytes
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

// Gather reads the readings of the live connector log (and its rotated .1
// when the live file holds none inside the window) and the scratch log.
func Gather(now time.Time, window time.Duration, livePath, scratchPath string) ([]Reading, error) {
	var out []Reading
	add := func(path, src string) (int, error) {
		b, err := ReadTail(path, 4<<20)
		if err != nil {
			return 0, fmt.Errorf("budget: read %s: %w", src, err)
		}
		rs := ParseReadings(bytes.NewReader(b), src)
		n := 0
		for _, r := range rs {
			if now.Sub(r.Time) <= window {
				out = append(out, r)
				n++
			}
		}
		return n, nil
	}
	if livePath != "" {
		n, err := add(livePath, "live")
		if err != nil {
			return nil, err
		}
		if n == 0 {
			if _, err := add(livePath+".1", "live.1"); err != nil {
				return nil, err
			}
		}
	}
	if scratchPath != "" {
		if _, err := add(scratchPath, SourceScratch); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Decision is the guard's verdict for one slot.
type Decision struct {
	// Known is false when no usable reading exists (the tick then proceeds:
	// the guard cannot deadlock on an empty scratch log).
	Known     bool
	Skip      bool
	Remaining int
	ResetAt   time.Time
	Source    string
	Floor     int
}

// Decide applies the floor. Readings whose window has reset (plus ResetGrace)
// are void; per remaining window the NEWEST reading counts (whichever log
// supplied it); the minimum across windows decides.
func Decide(now time.Time, floor int, readings []Reading) Decision {
	newest := map[time.Time]Reading{}
	for _, r := range readings {
		if !r.ResetAt.Add(ResetGrace).After(now) {
			continue
		}
		cur, ok := newest[r.ResetAt]
		if !ok || r.Time.After(cur.Time) {
			newest[r.ResetAt] = r
		}
	}
	d := Decision{Floor: floor}
	if len(newest) == 0 {
		return d
	}
	keys := make([]time.Time, 0, len(newest))
	for k := range newest {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].Before(keys[j]) })
	min := newest[keys[0]]
	for _, k := range keys[1:] {
		if r := newest[k]; r.Remaining < min.Remaining {
			min = r
		}
	}
	d.Known = true
	d.Remaining, d.ResetAt, d.Source = min.Remaining, min.ResetAt, min.Source
	d.Skip = min.Remaining < floor
	return d
}
