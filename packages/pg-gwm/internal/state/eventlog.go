package state

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Event is one line of the event log.
type Event struct {
	At         time.Time      `json:"at"`
	Kind       string         `json:"kind"`
	WorktreeID string         `json:"worktree_id"`
	LeaseID    string         `json:"lease_id"`
	Name       string         `json:"name"`
	Owner      string         `json:"owner"`
	Outcome    string         `json:"outcome"`
	Details    map[string]any `json:"details"`
}

const eventPrefix, eventSuffix = "events-", ".jsonl"

// Append writes e to the daily file Dir/events-YYYYMMDD.jsonl (UTC day of the
// store clock) with O_APPEND. The first append of a day deletes every daily
// file whose whole day is at least Retention old (day end + retention <= now),
// so history is never shorter than the retention. A zero At is filled from the
// clock.
func (s Store) Append(e Event) error {
	now := s.now().UTC()
	if e.At.IsZero() {
		e.At = now
	}
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(s.Dir, eventPrefix+now.Format("20060102")+eventSuffix)
	_, statErr := os.Stat(path)
	first := errors.Is(statErr, fs.ErrNotExist)
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, werr := f.Write(append(b, '\n'))
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return werr
	}
	if first {
		s.pruneEvents(now)
	}
	return nil
}

func (s Store) pruneEvents(now time.Time) {
	if s.Retention.None || s.Retention.D <= 0 {
		return
	}
	ents, err := os.ReadDir(s.Dir)
	if err != nil {
		return
	}
	for _, e := range ents {
		n := e.Name()
		if !strings.HasPrefix(n, eventPrefix) || !strings.HasSuffix(n, eventSuffix) {
			continue
		}
		day, err := time.Parse("20060102", strings.TrimSuffix(strings.TrimPrefix(n, eventPrefix), eventSuffix))
		if err != nil {
			continue
		}
		if !day.Add(24*time.Hour + s.Retention.D).After(now) {
			_ = os.Remove(filepath.Join(s.Dir, n))
		}
	}
}

// NoLeaseEvent reports whether the event log proves that no lease ever took
// worktree id, for a worktree created at createdAt: no "lease" event names it
// and the retained daily files reach back to the day it was created, so
// retention has not dropped the evidence. An unreadable or absent log proves
// nothing, so it reports false.
func (s Store) NoLeaseEvent(id string, createdAt time.Time) bool {
	ents, err := os.ReadDir(s.Dir)
	if err != nil {
		return false
	}
	var oldest time.Time
	for _, e := range ents {
		n := e.Name()
		if !strings.HasPrefix(n, eventPrefix) || !strings.HasSuffix(n, eventSuffix) {
			continue
		}
		day, err := time.Parse("20060102", strings.TrimSuffix(strings.TrimPrefix(n, eventPrefix), eventSuffix))
		if err != nil {
			continue
		}
		if oldest.IsZero() || day.Before(oldest) {
			oldest = day
		}
		b, err := os.ReadFile(filepath.Join(s.Dir, n))
		if err != nil {
			return false
		}
		for _, line := range strings.Split(string(b), "\n") {
			if !strings.Contains(line, id) {
				continue
			}
			var ev Event
			if json.Unmarshal([]byte(line), &ev) == nil && ev.Kind == "lease" && ev.WorktreeID == id {
				return false
			}
		}
	}
	return !oldest.IsZero() && !createdAt.IsZero() && !oldest.After(createdAt.UTC())
}
