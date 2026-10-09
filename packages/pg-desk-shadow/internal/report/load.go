// Package report turns the collector's rows and the copied live logs into the
// comparison report. It is idempotent: its output depends only on its inputs.
package report

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/phillipgreenii/pg-desk-shadow/internal/schema"
	"github.com/phillipgreenii/pg-desk-shadow/internal/scratch"
)

// Dispatch is one live router dispatch row (events.jsonl), reduced.
type Dispatch struct {
	Time       time.Time
	EventType  string
	EnqueuedAt time.Time // zero when absent (rows before event_type existed)
	StartedAt  time.Time
	ID         string // bare PR id: the row's bead field without its `@<change hash>` suffix
	Raw        string // the row's bead field as written (`<pr id>@<hash>` once per-change event ids landed)
	Role       string
}

// BaseID strips the `@<change hash>` suffix the live router appends to a
// per-change event id (`<pr id>@<hash>`, also inside `pr.changed:<pr id>@<hash>`).
// A PR id is `<owner>/<repo>#<n>` and never contains `@`, so everything from the
// first `@` is the suffix. An id without one is returned unchanged.
func BaseID(s string) string {
	base, _, _ := strings.Cut(s, "@")
	return base
}

// QueueRow is one live queue.jsonl row, reduced. Its timestamps carry a local
// UTC offset on disk and are normalised to UTC here.
type QueueRow struct {
	Op      string
	EventID string
	At      time.Time
	Reason  string
}

// RunRow is one live run-record row.
type RunRow struct {
	End         time.Time // ts, UTC (second resolution)
	Start       time.Time // End minus duration
	EntityID    string
	Change      string
	HashChanged bool
	Anchor      bool
	Cause       string
	DurationMS  int64
	Degraded    bool
}

// Input is everything one scratch directory contributes.
type Input struct {
	Dir      string
	Manifest scratch.Manifest
	Rows     []schema.Row
	Events   []Dispatch
	Queue    []QueueRow
	Runs     []RunRow
	// HaveRunRecord is false when the run-record copy is absent.
	HaveRunRecord bool
	// SeedIDs is the set of ids active at seeding (T0).
	SeedIDs map[string]bool
	Key     []byte
	// Markers counts reset markers per copied log (data-quality header).
	Markers map[string]int
}

func parseTS(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

// Load reads a scratch directory.
func Load(dir string) (Input, error) {
	l := scratch.Layout{Root: dir}
	m, err := l.Load()
	if err != nil {
		return Input{}, err
	}
	in := Input{Dir: dir, Manifest: m, SeedIDs: map[string]bool{}, Markers: map[string]int{}}
	if in.Rows, err = schema.ReadAll(l.TicksFile()); err != nil {
		return Input{}, err
	}
	if key, err := os.ReadFile(l.HMACKey()); err == nil {
		in.Key = bytes.TrimSpace(key)
	} else {
		return Input{}, fmt.Errorf("report: read hmac key: %w", err)
	}
	if b, err := os.ReadFile(l.SeedFile()); err == nil {
		var s struct {
			IDs []string `json:"ids"`
		}
		if json.Unmarshal(b, &s) == nil {
			for _, id := range s.IDs {
				in.SeedIDs[id] = true
			}
		}
	}
	live := l.LiveDir()
	evPath := filepath.Join(live, "router-events.jsonl")
	if in.Events, in.Markers["router-events"], err = readEvents(evPath); err != nil {
		return Input{}, err
	}
	if in.Queue, in.Markers["router-queue"], err = readQueue(filepath.Join(live, "router-queue.jsonl")); err != nil {
		return Input{}, err
	}
	rrPath := filepath.Join(live, "run-record.log")
	if _, err := os.Stat(rrPath); err == nil {
		in.HaveRunRecord = true
		if in.Runs, in.Markers["run-record"], err = readRuns(rrPath); err != nil {
			return Input{}, err
		}
	}
	return in, nil
}

// lines yields the distinct, non-marker lines of a copy; markers are counted.
func lines(path string, fn func(line []byte)) (markers int, err error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer func() { _ = f.Close() }()
	seen := map[string]bool{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		b := bytes.TrimSpace(sc.Bytes())
		if len(b) == 0 {
			continue
		}
		if bytes.Contains(b, []byte(`"_shadow"`)) {
			markers++
			continue
		}
		if seen[string(b)] {
			continue
		}
		seen[string(b)] = true
		fn(b)
	}
	return markers, sc.Err()
}

func readEvents(path string) ([]Dispatch, int, error) {
	var out []Dispatch
	mk, err := lines(path, func(b []byte) {
		var r struct {
			Time       string `json:"time"`
			Kind       string `json:"kind"`
			EventType  string `json:"event_type"`
			EnqueuedAt string `json:"enqueued_at"`
			StartedAt  string `json:"started_at"`
			Bead       string `json:"bead"`
			Change     string `json:"change"`
			Role       string `json:"role"`
		}
		if json.Unmarshal(b, &r) != nil || r.Time == "" {
			return
		}
		d := Dispatch{Time: parseTS(r.Time), EventType: r.EventType, EnqueuedAt: parseTS(r.EnqueuedAt), StartedAt: parseTS(r.StartedAt), Raw: r.Bead, Role: r.Role}
		if d.Raw == "" {
			if _, id, ok := strings.Cut(r.Change, ":"); ok {
				d.Raw = id
			}
		}
		d.ID = BaseID(d.Raw)
		out = append(out, d)
	})
	sort.SliceStable(out, func(i, j int) bool { return out[i].Time.Before(out[j].Time) })
	return out, mk, err
}

func readQueue(path string) ([]QueueRow, int, error) {
	var out []QueueRow
	mk, err := lines(path, func(b []byte) {
		var r struct {
			Op      string `json:"op"`
			EventID string `json:"eventId"`
			At      string `json:"at"`
			Reason  string `json:"reason"`
		}
		if json.Unmarshal(b, &r) != nil || r.Op == "" {
			return
		}
		out = append(out, QueueRow{Op: r.Op, EventID: r.EventID, At: parseTS(r.At), Reason: r.Reason})
	})
	return out, mk, err
}

func readRuns(path string) ([]RunRow, int, error) {
	var out []RunRow
	mk, err := lines(path, func(b []byte) {
		var r struct {
			Ts          string `json:"ts"`
			EntityType  string `json:"entity_type"`
			EntityID    string `json:"entity_id"`
			Change      string `json:"change"`
			HashChanged bool   `json:"content_hash_changed"`
			Anchor      bool   `json:"anchor_written"`
			Cause       string `json:"anchor_cause"`
			DurationMS  int64  `json:"duration_ms"`
			Degraded    any    `json:"degraded"`
		}
		if json.Unmarshal(b, &r) != nil || r.Ts == "" || (r.EntityType != "" && r.EntityType != "pr") {
			return
		}
		end := parseTS(r.Ts)
		deg := false
		switch v := r.Degraded.(type) {
		case bool:
			deg = v
		case string:
			deg = v != ""
		}
		out = append(out, RunRow{
			End: end, Start: end.Add(-time.Duration(r.DurationMS) * time.Millisecond), EntityID: r.EntityID,
			Change: r.Change, HashChanged: r.HashChanged, Anchor: r.Anchor, Cause: r.Cause, DurationMS: r.DurationMS, Degraded: deg,
		})
	})
	return out, mk, err
}
