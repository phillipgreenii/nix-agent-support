// Package settle implements the core half of a cursor-feed source's settlement
// report: a durable per-(query, lane) ledger of the events the latest successful
// poll of a lane emitted, the frontier computed from it, and the settle
// invocation run (at most one per lane at a time) when the frontier advances.
//
// The package never interprets a lane or a token: both are opaque strings the
// source supplied. Settlement of an event is looked up by event id, never by
// comparing tokens.
package settle

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Entry is one emitted event of a lane: its id, the token of the newest poll
// that listed it, the instant the core first saw the id on this lane (ingested
// or refused at ingest) and whether it is settled.
type Entry struct {
	ID        string    `json:"id"`
	Token     string    `json:"token"`
	FirstSeen time.Time `json:"firstSeen"`
	Settled   bool      `json:"settled,omitempty"`
}

// Item is one event of a poll: its id and token, in the order the source emitted.
type Item struct {
	ID    string
	Token string
}

type lane struct {
	Query   string  `json:"query"`
	Lane    string  `json:"lane"`
	Entries []Entry `json:"entries"`
}

// Ledger is the durable lane ledger. A zero path keeps it in memory only.
type Ledger struct {
	mu    sync.Mutex
	path  string
	lanes map[string]*lane
}

func laneKey(query, laneName string) string { return query + "\x00" + laneName }

// OpenLedger loads the ledger persisted at path (a missing file is an empty
// ledger). An empty path gives an in-memory ledger.
func OpenLedger(path string) (*Ledger, error) {
	l := &Ledger{path: path, lanes: map[string]*lane{}}
	if path == "" {
		return l, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return l, nil
	}
	if err != nil {
		return nil, fmt.Errorf("settle: read ledger: %w", err)
	}
	var ls []*lane
	if err := json.Unmarshal(data, &ls); err != nil {
		return nil, fmt.Errorf("settle: decode ledger %s: %w", path, err)
	}
	for _, x := range ls {
		l.lanes[laneKey(x.Query, x.Lane)] = x
	}
	return l, nil
}

// saveLocked writes the whole ledger atomically (temp file and rename). Caller
// holds l.mu.
func (l *Ledger) saveLocked() error {
	if l.path == "" {
		return nil
	}
	ls := make([]*lane, 0, len(l.lanes))
	for _, x := range l.lanes {
		ls = append(ls, x)
	}
	sort.Slice(ls, func(i, j int) bool {
		if ls[i].Query != ls[j].Query {
			return ls[i].Query < ls[j].Query
		}
		return ls[i].Lane < ls[j].Lane
	})
	data, err := json.Marshal(ls)
	if err != nil {
		return err
	}
	tmp := l.path + ".tmp"
	if err := os.MkdirAll(filepath.Dir(l.path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, l.path)
}

// Poll records a successful poll of one lane: items (in emission order)
// REPLACE the lane's list. An id the old list held keeps its settlement and its
// first-seen time and takes the newest token; any other id starts unsettled with
// first-seen now. An old entry the new list lacks is dropped (the source's own
// cursor has moved past it).
func (l *Ledger) Poll(query, laneName string, items []Item, now time.Time) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	k := laneKey(query, laneName)
	old := map[string]Entry{}
	if x, ok := l.lanes[k]; ok {
		for _, e := range x.Entries {
			old[e.ID] = e
		}
	}
	entries := make([]Entry, 0, len(items))
	seen := map[string]bool{}
	for _, it := range items {
		if seen[it.ID] {
			continue // a duplicate id in one poll is one event
		}
		seen[it.ID] = true
		e := Entry{ID: it.ID, Token: it.Token, FirstSeen: now}
		if o, ok := old[it.ID]; ok {
			e.FirstSeen, e.Settled = o.FirstSeen, o.Settled
		}
		entries = append(entries, e)
	}
	l.lanes[k] = &lane{Query: query, Lane: laneName, Entries: entries}
	return l.saveLocked()
}

// MarkSettled records id as settled in every lane that lists it and reports
// whether anything changed.
func (l *Ledger) MarkSettled(id string) (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	changed := false
	for _, x := range l.lanes {
		for i := range x.Entries {
			if x.Entries[i].ID == id && !x.Entries[i].Settled {
				x.Entries[i].Settled = true
				changed = true
			}
		}
	}
	if !changed {
		return false, nil
	}
	return true, l.saveLocked()
}

// Frontier returns the token of the last event, in list order, whose
// predecessors are all settled (and which is itself settled). ok is false when
// the first event is unsettled or the list is empty. Tokens are never compared.
func (l *Ledger) Frontier(query, laneName string) (token string, ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	x := l.lanes[laneKey(query, laneName)]
	if x == nil {
		return "", false
	}
	for _, e := range x.Entries {
		if !e.Settled {
			break
		}
		token, ok = e.Token, true
	}
	return token, ok
}

// UnsettledIDs lists the ids of every unsettled entry in every lane.
func (l *Ledger) UnsettledIDs() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for _, x := range l.lanes {
		for _, e := range x.Entries {
			if !e.Settled {
				out = append(out, e.ID)
			}
		}
	}
	sort.Strings(out)
	return out
}

// Lanes lists the lane names the ledger holds for query, sorted.
func (l *Ledger) Lanes(query string) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for _, x := range l.lanes {
		if x.Query == query {
			out = append(out, x.Lane)
		}
	}
	sort.Strings(out)
	return out
}

// Queries lists every query the ledger holds a lane for, sorted.
func (l *Ledger) Queries() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	set := map[string]bool{}
	for _, x := range l.lanes {
		set[x.Query] = true
	}
	out := make([]string, 0, len(set))
	for q := range set {
		out = append(out, q)
	}
	sort.Strings(out)
	return out
}

// Entries returns a copy of a lane's list.
func (l *Ledger) Entries(query, laneName string) []Entry {
	l.mu.Lock()
	defer l.mu.Unlock()
	x := l.lanes[laneKey(query, laneName)]
	if x == nil {
		return nil
	}
	return append([]Entry(nil), x.Entries...)
}
