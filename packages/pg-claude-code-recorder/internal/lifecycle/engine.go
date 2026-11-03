package lifecycle

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/proc"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
)

// ErrNotFound is returned by Show and Process for an unknown id.
var ErrNotFound = errors.New("lifecycle: not found")

// Engine reads Conversation and Process facts from the store. Every fact is a
// direct read of a record or an observation (registry versions, history entries,
// end files, the procs table); nothing here judges.
type Engine struct {
	St *store.Store
	// ControlDir is the control mod's state directory ("" when none): the inbox
	// FIFO's presence is read from it.
	ControlDir string
	// Exists reports whether a path exists (os.Lstat when nil).
	Exists func(path string) bool
	// Now is the time (time.Now when nil).
	Now func() time.Time
}

func (e *Engine) exists(p string) bool {
	if e.Exists != nil {
		return e.Exists(p)
	}
	_, err := os.Lstat(p)
	return err == nil
}

type pkey struct {
	pid int64
	ps  string
}

type regRow struct {
	id      int64
	session string
	t       int64
	pid     int64
	ps      string
	cwd     string
	started int64
	name    string
	status  string
	waiting string
}

// seg is the span during which one Process's registry file named one session.
type seg struct {
	key   pkey
	start int64
	end   int64 // when the registry stopped naming it (0: it still does)
	cwd   string
	// started is the Process's startedAt (unix ns), 0 when absent.
	started int64
}

type model struct {
	vers    map[pkey][]regRow
	segs    map[string][]seg
	deleted map[pkey]int64
	procs   map[pkey]store.Proc
	// slash maps a session to its history entry times by command.
	clears map[string][]int64
	exits  map[string][]int64
	ends   map[string]string
	// stages is the persisted stage (settled or held) of a Conversation.
	stages map[string]string
}

func ns(t int64) time.Time { return time.Unix(0, t).UTC() }

func tp(t int64) *time.Time {
	if t == 0 {
		return nil
	}
	v := ns(t)
	return &v
}

func (e *Engine) load() (*model, error) {
	m := &model{
		vers: map[pkey][]regRow{}, segs: map[string][]seg{}, deleted: map[pkey]int64{}, procs: map[pkey]store.Proc{},
		clears: map[string][]int64{}, exits: map[string][]int64{}, ends: map[string]string{}, stages: map[string]string{},
	}
	db := e.St.Read()
	rows, err := db.Query(`SELECT r.id, r.session, r.time_ns,
  MAX(CASE WHEN fn.name='pid' THEN f.n END), MAX(CASE WHEN fn.name='proc_start' THEN f.s END),
  MAX(CASE WHEN fn.name='cwd' THEN f.s END), MAX(CASE WHEN fn.name='started_at' THEN f.n END),
  MAX(CASE WHEN fn.name='name' THEN f.s END), MAX(CASE WHEN fn.name='status' THEN f.s END),
  MAX(CASE WHEN fn.name='waiting_for' THEN f.s END)
FROM records r JOIN fields f ON f.rec_id=r.id JOIN field_names fn ON fn.id=f.name_id
WHERE r.source='session-registry' AND r.type='registry' GROUP BY r.id ORDER BY r.id`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var x regRow
		var pid, started sql.NullInt64
		var ps, cwd, name, status, waiting sql.NullString
		if err := rows.Scan(&x.id, &x.session, &x.t, &pid, &ps, &cwd, &started, &name, &status, &waiting); err != nil {
			_ = rows.Close()
			return nil, err
		}
		x.pid, x.ps, x.cwd, x.started = pid.Int64, ps.String, cwd.String, started.Int64
		x.name, x.status, x.waiting = name.String, status.String, waiting.String
		k := pkey{x.pid, x.ps}
		m.vers[k] = append(m.vers[k], x)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for k, vs := range m.vers {
		var cur *seg
		var curSession string
		for _, v := range vs {
			if cur != nil && v.session == curSession {
				if v.cwd != "" {
					cur.cwd = v.cwd
				}
				continue
			}
			if cur != nil {
				cur.end = v.t
				m.segs[curSession] = append(m.segs[curSession], *cur)
			}
			curSession = v.session
			cur = &seg{key: k, start: v.t, cwd: v.cwd, started: v.started}
		}
		if cur != nil {
			m.segs[curSession] = append(m.segs[curSession], *cur)
		}
	}
	for _, ss := range m.segs {
		sort.SliceStable(ss, func(i, j int) bool { return ss[i].start < ss[j].start })
	}
	drows, err := db.Query(`SELECT r.time_ns, MAX(CASE WHEN fn.name='pid' THEN f.n END), MAX(CASE WHEN fn.name='proc_start' THEN f.s END)
FROM records r JOIN fields f ON f.rec_id=r.id JOIN field_names fn ON fn.id=f.name_id WHERE r.source='session-registry' AND r.type='registry_deleted' GROUP BY r.id`)
	if err != nil {
		return nil, err
	}
	for drows.Next() {
		var t int64
		var pid sql.NullInt64
		var ps sql.NullString
		if err := drows.Scan(&t, &pid, &ps); err != nil {
			_ = drows.Close()
			return nil, err
		}
		k := pkey{pid.Int64, ps.String}
		if t > m.deleted[k] {
			m.deleted[k] = t
		}
	}
	if err := drows.Close(); err != nil {
		return nil, err
	}
	hrows, err := db.Query(`SELECT r.session, r.time_ns, f.s FROM records r JOIN fields f ON f.rec_id=r.id AND f.name_id=(SELECT id FROM field_names WHERE name='slash_command')
WHERE r.source='claude-history'`)
	if err != nil {
		return nil, err
	}
	for hrows.Next() {
		var s, c string
		var t int64
		if err := hrows.Scan(&s, &t, &c); err != nil {
			_ = hrows.Close()
			return nil, err
		}
		switch c {
		case "clear":
			m.clears[s] = append(m.clears[s], t)
		case "exit":
			m.exits[s] = append(m.exits[s], t)
		}
	}
	if err := hrows.Close(); err != nil {
		return nil, err
	}
	erows, err := db.Query(`SELECT r.session, f.s FROM records r JOIN fields f ON f.rec_id=r.id AND f.name_id=(SELECT id FROM field_names WHERE name='reason')
WHERE r.source='claude-control-end' ORDER BY r.time_ns, r.id`)
	if err != nil {
		return nil, err
	}
	for erows.Next() {
		var s, reason string
		if err := erows.Scan(&s, &reason); err != nil {
			_ = erows.Close()
			return nil, err
		}
		m.ends[s] = reason
	}
	if err := erows.Close(); err != nil {
		return nil, err
	}
	srows, err := e.St.Stages()
	if err != nil {
		return nil, err
	}
	for _, r := range srows {
		m.stages[r.Session] = r.Stage
	}
	ps, err := e.St.Procs()
	if err != nil {
		return nil, err
	}
	for _, p := range ps {
		m.procs[pkey{p.Pid, p.ProcStart}] = p
	}
	return m, nil
}

// LiveSessions returns the sessions a live Process holds now (one load of the
// registry for every session, so the settle stage asks once per pass).
func (e *Engine) LiveSessions() (map[string]bool, error) {
	m, err := e.load()
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for session := range m.segs {
		if c, ok := m.conversation(session); ok && c.State == StateOpen {
			out[session] = true
		}
	}
	return out, nil
}

func anyAfter(ts []int64, after int64) bool {
	for _, t := range ts {
		if t > after {
			return true
		}
	}
	return false
}

func anyAtOrAfter(ts []int64, at int64) bool {
	for _, t := range ts {
		if t >= at {
			return true
		}
	}
	return false
}

func processStart(s seg) int64 {
	if s.started > 0 {
		return s.started
	}
	if t, ok := proc.ParseStart(s.key.ps); ok {
		return t.UnixNano()
	}
	return 0
}

func (m *model) alive(k pkey) bool {
	if p, ok := m.procs[k]; ok {
		return p.ExitNS == 0
	}
	return m.deleted[k] == 0
}

// conversation derives one Conversation's facts (cwd and the transcript facts
// are filled later, for the rows that survive the filters).
func (m *model) conversation(session string) (Conversation, bool) {
	ss := m.segs[session]
	if len(ss) == 0 {
		return Conversation{}, false
	}
	last := ss[len(ss)-1]
	k := last.key
	prow, tracked := m.procs[k]
	c := Conversation{SessionID: session, Pid: k.pid, ProcStart: k.ps, Stage: StageQuiet, State: StateClosed, EndReason: m.ends[session]}
	c.Cwd = last.cwd
	if vs := m.vers[k]; len(vs) > 0 {
		c.regTime = vs[len(vs)-1].t
	}
	pstart := processStart(last)
	c.RegistryStaleAtDiscovery = tracked && prow.Stale
	if tracked && prow.LastSeenNS > 0 {
		c.LastSeenAlive = tp(prow.LastSeenNS)
	}
	left := last.end != 0
	c.ExitEntry = anyAfter(m.exits[session], pstart)
	switch {
	case left:
		// The Process moved on to another session while it lives: /clear ends the
		// Conversation, an in-process /resume does not.
		c.ClearEntry = anyAtOrAfter(m.clears[session], pstart)
		c.Ended = c.ClearEntry
		if c.Ended {
			c.EndTime = tp(last.end)
		}
	case m.alive(k):
		c.State, c.Stage = StateOpen, StageLive
	default:
		c.Ended = true
		if st := m.stages[session]; st == StageSettled || st == StageHeld || st == StageRemovalPending || st == StageRemoved {
			c.Stage = st
		}
		if tracked && prow.ExitNS > 0 {
			c.ExitObserved = prow.ExitKind == store.ExitObserved
			if c.ExitObserved {
				c.RegistryPresentAtExit = prow.RegistryPresent
				c.ExitTime = tp(prow.ExitNS)
			}
		}
		switch {
		case c.ExitTime != nil:
			c.EndTime = c.ExitTime
		case c.LastSeenAlive != nil:
			c.EndTime, c.EndTimeApprox = c.LastSeenAlive, true
		case m.deleted[k] > 0:
			c.EndTime, c.EndTimeApprox = tp(m.deleted[k]), true
		}
	}
	return c, true
}

// transcriptFacts fills what a transcript says: the newest cwd (when its files
// disagree, the newest file's value), the last write, whether the originals
// still exist, and the end time when nothing better is known.
func (e *Engine) transcriptFacts(c *Conversation) error {
	db := e.St.Read()
	var cwd sql.NullString
	err := db.QueryRow(`SELECT f.s FROM records r JOIN fields f ON f.rec_id=r.id AND f.name_id=(SELECT id FROM field_names WHERE name='cwd')
WHERE r.session=? AND r.source='claude-transcript' ORDER BY r.time_ns DESC, r.id DESC LIMIT 1`, c.SessionID).Scan(&cwd)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if cwd.Valid && cwd.String != "" {
		c.Cwd = cwd.String
	}
	var last sql.NullInt64
	if err := db.QueryRow(`SELECT MAX(time_ns) FROM records WHERE session=? AND source='claude-transcript'`, c.SessionID).Scan(&last); err != nil {
		return err
	}
	if last.Valid && last.Int64 > 0 {
		c.LastWrite = tp(last.Int64)
	}
	if c.Ended && c.EndTime == nil {
		switch {
		case c.LastWrite != nil:
			c.EndTime, c.EndTimeApprox = c.LastWrite, true
		case c.regTime > 0:
			c.EndTime, c.EndTimeApprox = tp(c.regTime), true
		}
	}
	frows, err := db.Query(`SELECT DISTINCT file_id FROM records WHERE session=? AND source='claude-transcript' LIMIT 50`, c.SessionID)
	if err != nil {
		return err
	}
	var ids []int64
	for frows.Next() {
		var id int64
		if err := frows.Scan(&id); err != nil {
			_ = frows.Close()
			return err
		}
		ids = append(ids, id)
	}
	if err := frows.Close(); err != nil {
		return err
	}
	for _, id := range ids {
		paths, _ := e.St.CurrentPaths(id)
		for _, p := range paths {
			if e.exists(p) {
				c.OriginalsExist = true
			}
		}
	}
	return nil
}

func (c *Conversation) sortTime() int64 {
	switch {
	case c.EndTime != nil:
		return c.EndTime.UnixNano()
	case c.LastWrite != nil:
		return c.LastWrite.UnixNano()
	case c.LastSeenAlive != nil:
		return c.LastSeenAlive.UnixNano()
	}
	return 0
}

func okBool(f *bool, v bool) bool { return f == nil || *f == v }

func (f ConversationFilter) evidenceMatch(c *Conversation) bool {
	return (f.State == "" || f.State == c.State) && okBool(f.Ended, c.Ended) &&
		okBool(f.RegistryPresentAtExit, c.RegistryPresentAtExit) && okBool(f.ExitObserved, c.ExitObserved) &&
		okBool(f.ClearEntry, c.ClearEntry) && okBool(f.ExitEntry, c.ExitEntry) &&
		okBool(f.RegistryStaleAtDiscovery, c.RegistryStaleAtDiscovery)
}

// DefaultLimit and MaxLimit bound conversation.list.
const (
	DefaultLimit = 1000
	MaxLimit     = 100000
)

// List returns the Conversations that match the filter, newest first.
func (e *Engine) List(f ConversationFilter) ([]Conversation, error) {
	var since int64
	if f.Since != "" {
		t, err := time.Parse(time.RFC3339Nano, f.Since)
		if err != nil {
			return nil, err
		}
		since = t.UnixNano()
	}
	m, err := e.load()
	if err != nil {
		return nil, err
	}
	var out []Conversation
	for session := range m.segs {
		c, ok := m.conversation(session)
		if !ok || !f.evidenceMatch(&c) {
			continue
		}
		if err := e.transcriptFacts(&c); err != nil {
			return nil, err
		}
		if since > 0 && c.sortTime() < since {
			continue
		}
		if f.Cwd != "" {
			p := strings.TrimRight(f.Cwd, "/")
			if c.Cwd != p && !strings.HasPrefix(c.Cwd, p+"/") {
				continue
			}
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		if a, b := out[i].sortTime(), out[j].sortTime(); a != b {
			return a > b
		}
		return out[i].SessionID < out[j].SessionID
	})
	limit := f.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	if limit > MaxLimit {
		limit = MaxLimit
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func holdings(m *model, k pkey) []Holding {
	var out []Holding
	for _, ss := range m.segs {
		for _, s := range ss {
			if s.key == k {
				h := Holding{Pid: k.pid, ProcStart: k.ps, From: ns(s.start), To: nil}
				if s.end != 0 {
					t := ns(s.end)
					h.To = &t
				}
				out = append(out, h)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].From.Before(out[j].From) })
	return out
}

// Show returns one Conversation with its Process history.
func (e *Engine) Show(session string) (Conversation, error) {
	m, err := e.load()
	if err != nil {
		return Conversation{}, err
	}
	c, ok := m.conversation(session)
	if !ok {
		return Conversation{}, ErrNotFound
	}
	if err := e.transcriptFacts(&c); err != nil {
		return Conversation{}, err
	}
	for _, s := range m.segs[session] {
		h := Holding{Pid: s.key.pid, ProcStart: s.key.ps, From: ns(s.start)}
		if s.end != 0 {
			t := ns(s.end)
			h.To = &t
		}
		c.Processes = append(c.Processes, h)
	}
	return c, nil
}

func (e *Engine) process(m *model, k pkey, detail bool) Process {
	p := Process{Pid: k.pid, ProcStart: k.ps}
	if vs := m.vers[k]; len(vs) > 0 {
		v := vs[len(vs)-1]
		p.SessionID, p.Name, p.Status, p.Cwd = v.session, v.name, v.status, v.cwd
		p.StartedAt = tp(v.started)
		if p.StartedAt == nil {
			if t, ok := proc.ParseStart(k.ps); ok {
				p.StartedAt = &t
			}
		}
		if detail {
			for _, x := range vs {
				p.Versions = append(p.Versions, RegistryVersion{At: ns(x.t), SessionID: x.session, Name: x.name, Status: x.status, WaitingFor: x.waiting})
			}
			p.Conversations = holdings(m, k)
		}
	}
	p.Alive = m.alive(k)
	if pr, ok := m.procs[k]; ok {
		p.FirstSeen = ns(pr.FirstSeenNS)
		p.LastSeenAlive = tp(pr.LastSeenNS)
		p.RegistryStaleAtDiscovery = pr.Stale
		if pr.ExitNS > 0 {
			p.ExitObserved = pr.ExitKind == store.ExitObserved
			if p.ExitObserved {
				p.ExitTime = tp(pr.ExitNS)
				p.RegistryPresentAtExit = pr.RegistryPresent
			}
		}
		if pr.Host != "" {
			var h Host
			if json.Unmarshal([]byte(pr.Host), &h) == nil {
				p.Host = &h
			}
		}
	} else if vs := m.vers[k]; len(vs) > 0 {
		p.FirstSeen = ns(vs[0].t)
	}
	p.RegistryDeletedAt = tp(m.deleted[k])
	if e.ControlDir != "" {
		p.ControlInbox = e.exists(filepath.Join(e.ControlDir, "inbox", itoa(k.pid)+".fifo"))
	}
	return p
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func (m *model) processKeys() []pkey {
	seen := map[pkey]bool{}
	var ks []pkey
	for k := range m.vers {
		seen[k] = true
		ks = append(ks, k)
	}
	for k := range m.procs {
		if !seen[k] {
			ks = append(ks, k)
		}
	}
	return ks
}

// Processes returns the Processes that match the filter, newest first.
func (e *Engine) Processes(f ProcessFilter) ([]Process, error) {
	m, err := e.load()
	if err != nil {
		return nil, err
	}
	var out []Process
	for _, k := range m.processKeys() {
		p := e.process(m, k, false)
		if f.Alive != nil && *f.Alive != p.Alive {
			continue
		}
		if f.SessionID != "" && f.SessionID != p.SessionID {
			continue
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].FirstSeen.Equal(out[j].FirstSeen) {
			return out[i].FirstSeen.After(out[j].FirstSeen)
		}
		return out[i].Pid < out[j].Pid
	})
	if f.Limit > 0 && len(out) > f.Limit {
		out = out[:f.Limit]
	}
	return out, nil
}

// Process returns one Process with its registry versions and the Conversations
// it held. An empty procStart selects the newest Process with that pid.
func (e *Engine) Process(pid int64, procStart string) (Process, error) {
	m, err := e.load()
	if err != nil {
		return Process{}, err
	}
	var best *pkey
	var bestAt time.Time
	for _, k := range m.processKeys() {
		if k.pid != pid || (procStart != "" && k.ps != procStart) {
			continue
		}
		p := e.process(m, k, false)
		if best == nil || p.FirstSeen.After(bestAt) {
			kk := k
			best, bestAt = &kk, p.FirstSeen
		}
	}
	if best == nil {
		return Process{}, ErrNotFound
	}
	return e.process(m, *best, true), nil
}
