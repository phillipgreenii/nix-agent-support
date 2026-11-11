package lifecycle

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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
	// Ctx is the daemon root context (Background when nil): the registry load
	// the settle stage asks for each pass ends with it.
	Ctx context.Context

	// live is the last LiveSessions answer with the store state it was read at.
	live atomic.Pointer[liveEntry]

	// full is the last complete model with the store state it was read at.
	full atomic.Pointer[modelEntry]

	// inc is the registry state LiveSessions extends instead of rereading; incMu
	// guards it.
	incMu sync.Mutex
	inc   *liveInc

	// reg is the registry state a model load extends instead of rereading; regMu
	// guards it.
	regMu sync.Mutex
	reg   regCache
}

// modelEntry is a cached complete model. The model is a function of the
// registry, history and end-reason records, the Process observations and the
// stage rows alone, so it stays valid while the store has committed none of
// them and rolled back no write since it was read. A cached model is shared by
// every caller and MUST be treated as read-only.
type modelEntry struct {
	data, epoch uint64
	ready       bool
	m           *model
}

// liveEntry is a cached LiveSessions answer. The answer is a function of the
// stored registry records and Process observations alone,
// so it stays valid while the store has committed no registry record or Process
// observation and rolled back no write since it was read.
type liveEntry struct {
	data, epoch uint64
	sessions    map[string]bool
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
	// session is the session the registry file named (set by LiveSessions' fold).
	session string
	key     pkey
	start   int64
	end     int64 // when the registry stopped naming it (0: it still does)
	cwd     string
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

// The queries of load. Each filters records by source (and type) and then reads
// fields by (rec_id, name_id): records_source_type serves the first step and
// the fields primary key the second. Without that index the planner scans every
// fields row of the store. TestLoadQueryPlansUseTheSourceIndex guards the plans.
const registryQuery = `SELECT r.id, r.session, r.time_ns,
  MAX(CASE WHEN fn.name='pid' THEN f.n END), MAX(CASE WHEN fn.name='proc_start' THEN f.s END),
  MAX(CASE WHEN fn.name='cwd' THEN f.s END), MAX(CASE WHEN fn.name='started_at' THEN f.n END),
  MAX(CASE WHEN fn.name='name' THEN f.s END), MAX(CASE WHEN fn.name='status' THEN f.s END),
  MAX(CASE WHEN fn.name='waiting_for' THEN f.s END)
FROM records r JOIN fields f ON f.rec_id=r.id JOIN field_names fn ON fn.id=f.name_id
WHERE r.source='session-registry' AND r.type='registry' GROUP BY r.id ORDER BY r.id`

const registryDeletedQuery = `SELECT r.time_ns, MAX(CASE WHEN fn.name='pid' THEN f.n END), MAX(CASE WHEN fn.name='proc_start' THEN f.s END)
FROM records r JOIN fields f ON f.rec_id=r.id JOIN field_names fn ON fn.id=f.name_id WHERE r.source='session-registry' AND r.type='registry_deleted' GROUP BY r.id`

const slashCommandQuery = `SELECT r.session, r.time_ns, f.s FROM records r JOIN fields f ON f.rec_id=r.id AND f.name_id=(SELECT id FROM field_names WHERE name='slash_command')
WHERE r.source='claude-history'`

// sessionRefExpr reads a record's session id through its session_ref; the
// *Ref queries below are the text queries with it in place of r.session, used
// when the store marks every row as carrying a session_ref.
const sessionRefExpr = "(SELECT s.session_id FROM sessions s WHERE s.session_ref=r.session_ref)"

func viaSessionRef(q string) string { return strings.ReplaceAll(q, "r.session,", sessionRefExpr+",") }

const endReasonQuery = `SELECT r.session, f.s FROM records r JOIN fields f ON f.rec_id=r.id AND f.name_id=(SELECT id FROM field_names WHERE name='reason')
WHERE r.source='claude-control-end' ORDER BY r.time_ns, r.id`

func (e *Engine) load() (*model, error) {
	if e.Ctx != nil {
		return e.loadCtx(e.Ctx)
	}
	return e.loadCtx(context.Background())
}

// loadCtx is load with every statement under ctx, so a cancelled request ends
// the read in flight.
//
// The complete model is kept while the store has committed no input of it (see
// Store.ModelInputSeq), so a poll with nothing new reads nothing; the caller
// MUST NOT modify the model it gets.
func (e *Engine) loadCtx(ctx context.Context) (*model, error) {
	// The counters are read BEFORE the load: a write that lands during it moves
	// them, and the next call reloads.
	data, epoch, ready := e.St.ModelInputSeq(), e.St.RollbackEpoch(), e.St.SessionRefReady()
	if c := e.full.Load(); c != nil && c.data == data && c.epoch == epoch && c.ready == ready {
		// A cancelled request ends as it would on a load.
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return c.m, nil
	}
	m, err := e.loadWith(ctx, false)
	if err != nil {
		return nil, err
	}
	e.full.Store(&modelEntry{data: data, epoch: epoch, ready: ready, m: m})
	return m, nil
}

// loadWith is loadCtx; liveOnly reads just what decides whether a Process holds
// a session (the registry, its deletions and the processes) and leaves the
// history, end reasons and stages of the model empty.
func (e *Engine) loadWith(ctx context.Context, liveOnly bool) (*model, error) {
	// One read transaction: the registry, history, end reasons, stages and
	// processes are read under one lock round trip and from one snapshot.
	var m *model
	err := e.St.ReadTxCtx(ctx, func(rd *store.Reader) error {
		var e2 error
		m, e2 = e.loadFrom(rd, liveOnly)
		return e2
	})
	return m, err
}

func (e *Engine) loadFrom(rd *store.Reader, liveOnly bool) (*model, error) {
	m := &model{
		vers: map[pkey][]regRow{}, segs: map[string][]seg{}, deleted: map[pkey]int64{}, procs: map[pkey]store.Proc{},
		clears: map[string][]int64{}, exits: map[string][]int64{}, ends: map[string]string{}, stages: map[string]string{},
	}
	db := rd.Q()
	ready := e.St.SessionRefReady()
	pick := func(q string) string {
		if ready {
			return viaSessionRef(q)
		}
		return q
	}
	epoch := e.St.RollbackEpoch()
	e.regMu.Lock()
	defer e.regMu.Unlock()
	if err := e.syncRegistry(db, epoch, ready, pick); err != nil {
		return nil, err
	}
	for k, vs := range e.reg.vers {
		// Clipped, so a later append to the cache never shows in this model.
		m.vers[k] = vs[:len(vs):len(vs)]
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
	for k, t := range e.reg.deleted {
		m.deleted[k] = t
	}
	if !liveOnly {
		hrows, err := db.Query(pick(slashCommandQuery))
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
		erows, err := db.Query(pick(endReasonQuery))
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
		srows, err := rd.Stages()
		if err != nil {
			return nil, err
		}
		for _, r := range srows {
			m.stages[r.Session] = r.Stage
		}
	}
	ps, err := rd.Procs()
	if err != nil {
		return nil, err
	}
	for _, p := range ps {
		m.procs[pkey{p.Pid, p.ProcStart}] = p
	}
	return m, nil
}

// LiveSessions returns the sessions a live Process holds now (one load of the
// registry for every session, so the settle stage asks once per pass). The
// answer is kept while the store has committed no registry record or Process
// observation since it was read, so a poll with nothing new reads nothing; the
// caller gets its own copy.
func (e *Engine) LiveSessions() (map[string]bool, error) {
	// The counters are read BEFORE the load: a write that lands during it moves
	// them, and the next call reloads.
	data, epoch := e.St.LiveInputSeq(), e.St.RollbackEpoch()
	if c := e.live.Load(); c != nil && c.data == data && c.epoch == epoch {
		return copyLive(c.sessions), nil
	}
	ctx := e.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	out, err := e.liveIncremental(ctx)
	if err != nil {
		return nil, err
	}
	e.live.Store(&liveEntry{data: data, epoch: epoch, sessions: out})
	return copyLive(out), nil
}

func copyLive(m map[string]bool) map[string]bool {
	out := make(map[string]bool, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
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

// cwdQuery finds the newest cwd of a session's transcript. CROSS JOIN makes
// records the outer loop (records_session for the session, then the fields
// primary key per record). The store has no sqlite_stat1, so the planner cannot
// see that session=? is far more selective than the cwd name; on an earlier
// schema it started from every cwd field in the store. CROSS JOIN pins the
// order whatever the planner or schema does. INDEXED BY is avoided on
// purpose: it can fail at prepare time. TestCwdQueryPlanStartsFromTheSessionIndex
// guards the plan.
const cwdQuery = `SELECT f.s FROM records r CROSS JOIN fields f ON f.rec_id=r.id AND f.name_id=(SELECT id FROM field_names WHERE name='cwd')
WHERE r.session=? AND r.source='claude-transcript' AND r.time_ns>0 ORDER BY r.time_ns DESC, r.id DESC LIMIT 1`

// fileFactsQuery reads, for one session's transcript records, the newest
// write per file in one grouped statement. The newest write overall is the
// largest of them and the files are the distinct file ids, so one pass over the
// records_session_source_time index replaces a MAX query and a DISTINCT query.
const fileFactsQuery = `SELECT file_id, MAX(time_ns) FROM records WHERE session=? AND source='claude-transcript' GROUP BY file_id ORDER BY file_id`

// cwdQueryRef and fileFactsQueryRef are the same two queries over session_ref,
// used when the store marks every row as carrying one (store.SessionRefReady).
// The session id resolves to its reference once, in a scalar subquery, and
// records_session_ref_source_time serves the rest.
// session_files and session_stats are not used here on purpose: they cover every
// source of a session (registry, history and control-end records carry the id
// too), while these facts are about the transcript only, so reading them would
// change the answer.
const cwdQueryRef = `SELECT f.s FROM records r CROSS JOIN fields f ON f.rec_id=r.id AND f.name_id=(SELECT id FROM field_names WHERE name='cwd')
WHERE r.session_ref=(SELECT session_ref FROM sessions WHERE session_id=?) AND r.source='claude-transcript' AND r.time_ns>0 ORDER BY r.time_ns DESC, r.id DESC LIMIT 1`

const fileFactsQueryRef = `SELECT file_id, MAX(time_ns) FROM records WHERE session_ref=(SELECT session_ref FROM sessions WHERE session_id=?) AND source='claude-transcript' GROUP BY file_id ORDER BY file_id`

// maxTranscriptFiles bounds how many of a session's files are checked for
// surviving originals.
const maxTranscriptFiles = 50

// sessionFileFacts returns the newest transcript write time in nanoseconds (0
// when there is none) and up to maxTranscriptFiles file ids of the session.
func sessionFileFacts(db store.Querier, session string, byRef bool) (last int64, ids []int64, err error) {
	q := fileFactsQuery
	if byRef {
		q = fileFactsQueryRef
	}
	rows, err := db.Query(q, session)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id int64
		var t sql.NullInt64
		if err := rows.Scan(&id, &t); err != nil {
			return 0, nil, err
		}
		if t.Valid && t.Int64 > last {
			last = t.Int64
		}
		if len(ids) < maxTranscriptFiles {
			ids = append(ids, id)
		}
	}
	return last, ids, rows.Err()
}

// transcriptFacts fills what a transcript says: the newest cwd (when its files
// disagree, the newest file's value), the last write, whether the originals
// still exist, and the end time when nothing better is known.
func (e *Engine) transcriptFacts(ctx context.Context, c *Conversation) error {
	db := store.BindCtx(ctx, e.St.Read())
	rd := e.St.WithContext(ctx)
	var cwd sql.NullString
	byRef := e.St.SessionRefReady()
	cq := cwdQuery
	if byRef {
		cq = cwdQueryRef
	}
	err := db.QueryRow(cq, c.SessionID).Scan(&cwd)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if cwd.Valid && cwd.String != "" {
		c.Cwd = cwd.String
	}
	last, ids, err := sessionFileFacts(db, c.SessionID, byRef)
	if err != nil {
		return err
	}
	if last > 0 {
		c.LastWrite = tp(last)
	}
	if c.Ended && c.EndTime == nil {
		switch {
		case c.LastWrite != nil:
			c.EndTime, c.EndTimeApprox = c.LastWrite, true
		case c.regTime > 0:
			c.EndTime, c.EndTimeApprox = tp(c.regTime), true
		}
	}
	for _, id := range ids {
		paths, _ := rd.CurrentPaths(id)
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
func (e *Engine) List(ctx context.Context, f ConversationFilter) ([]Conversation, error) {
	var since int64
	if f.Since != "" {
		t, err := time.Parse(time.RFC3339Nano, f.Since)
		if err != nil {
			return nil, err
		}
		since = t.UnixNano()
	}
	m, err := e.loadCtx(ctx)
	if err != nil {
		return nil, err
	}
	var out []Conversation
	for session := range m.segs {
		c, ok := m.conversation(session)
		if !ok || !f.evidenceMatch(&c) {
			continue
		}
		if err := e.transcriptFacts(ctx, &c); err != nil {
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
func (e *Engine) Show(ctx context.Context, session string) (Conversation, error) {
	m, err := e.loadCtx(ctx)
	if err != nil {
		return Conversation{}, err
	}
	c, ok := m.conversation(session)
	if !ok {
		return Conversation{}, ErrNotFound
	}
	if err := e.transcriptFacts(ctx, &c); err != nil {
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
func (e *Engine) Processes(ctx context.Context, f ProcessFilter) ([]Process, error) {
	m, err := e.loadCtx(ctx)
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
func (e *Engine) Process(ctx context.Context, pid int64, procStart string) (Process, error) {
	m, err := e.loadCtx(ctx)
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

// incRegistryQuery and incDeletedQuery are the registry and deletion queries
// restricted to records after a given id (the one argument), the deletion query
// also returning the id.
var (
	incRegistryQuery = strings.Replace(registryQuery, "AND r.type='registry' GROUP BY", "AND r.type='registry' AND r.id > ? GROUP BY", 1)
	incDeletedQuery  = strings.Replace(strings.Replace(registryDeletedQuery, "SELECT r.time_ns,", "SELECT r.id, r.time_ns,", 1),
		"AND r.type='registry_deleted'", "AND r.type='registry_deleted' AND r.id > ?", 1)
)

// liveInc is the registry as LiveSessions has folded it so far: the segments of
// each Process (the last one open until the registry names another session) and
// its deletion times, with the highest record id read of each kind. Registry
// records are only ever appended, so a call reads the records after those ids.
// The state is valid while the store has not rolled back a write, changed the
// session-ref mode, or committed an input of the lifecycle model that is not a
// live input (which can remove records); otherwise it is rebuilt from scratch.
type liveInc struct {
	epoch  uint64
	ready  bool
	gap    uint64 // ModelInputSeq - LiveInputSeq when it was read
	maxReg int64
	maxDel int64
	segs   map[pkey][]seg
	del    map[pkey]int64
}

type incReg struct {
	id, t, started int64
	session        string
	k              pkey
	cwd            string
}

type incDel struct {
	id, t int64
	k     pkey
}

// liveIncremental derives the live sessions from the registry state held in
// e.inc, first reading only what the store gained since.
func (e *Engine) liveIncremental(ctx context.Context) (map[string]bool, error) {
	e.incMu.Lock()
	defer e.incMu.Unlock()
	// Read BEFORE the queries, like the caches above.
	live, modelSeq := e.St.LiveInputSeq(), e.St.ModelInputSeq()
	epoch, ready := e.St.RollbackEpoch(), e.St.SessionRefReady()
	gap := modelSeq - live
	st := e.inc
	if st == nil || st.epoch != epoch || st.ready != ready || st.gap != gap {
		st = &liveInc{epoch: epoch, ready: ready, gap: gap, segs: map[pkey][]seg{}, del: map[pkey]int64{}}
	}
	var regs []incReg
	var dels []incDel
	var procs []store.Proc
	err := e.St.ReadTxCtx(ctx, func(rd *store.Reader) error {
		db := rd.Q()
		q := incRegistryQuery
		if ready {
			q = viaSessionRef(q)
		}
		rows, err := db.Query(q, st.maxReg)
		if err != nil {
			return err
		}
		for rows.Next() {
			var x regRow
			var pid, started sql.NullInt64
			var ps, cwd, name, status, waiting sql.NullString
			if err := rows.Scan(&x.id, &x.session, &x.t, &pid, &ps, &cwd, &started, &name, &status, &waiting); err != nil {
				_ = rows.Close()
				return err
			}
			regs = append(regs, incReg{id: x.id, t: x.t, started: started.Int64, session: x.session, k: pkey{pid.Int64, ps.String}, cwd: cwd.String})
		}
		if err := rows.Close(); err != nil {
			return err
		}
		drows, err := db.Query(incDeletedQuery, st.maxDel)
		if err != nil {
			return err
		}
		for drows.Next() {
			var d incDel
			var pid sql.NullInt64
			var ps sql.NullString
			if err := drows.Scan(&d.id, &d.t, &pid, &ps); err != nil {
				_ = drows.Close()
				return err
			}
			d.k = pkey{pid.Int64, ps.String}
			dels = append(dels, d)
		}
		if err := drows.Close(); err != nil {
			return err
		}
		procs, err = rd.Procs()
		return err
	})
	if err != nil {
		return nil, err
	}
	// Fold only after every read succeeded, so a failure leaves the state as it was.
	for _, r := range regs {
		if r.id > st.maxReg {
			st.maxReg = r.id
		}
		ss := st.segs[r.k]
		if n := len(ss); n > 0 && ss[n-1].session == r.session {
			if r.cwd != "" {
				ss[n-1].cwd = r.cwd
			}
			continue
		} else if n > 0 {
			ss[n-1].end = r.t
		}
		st.segs[r.k] = append(ss, seg{key: r.k, session: r.session, start: r.t, cwd: r.cwd, started: r.started})
	}
	for _, d := range dels {
		if d.id > st.maxDel {
			st.maxDel = d.id
		}
		if d.t > st.del[d.k] {
			st.del[d.k] = d.t
		}
	}
	e.inc = st
	m := &model{
		segs: map[string][]seg{}, deleted: st.del, procs: make(map[pkey]store.Proc, len(procs)),
		vers: map[pkey][]regRow{}, clears: map[string][]int64{}, exits: map[string][]int64{}, ends: map[string]string{}, stages: map[string]string{},
	}
	for _, p := range procs {
		m.procs[pkey{p.Pid, p.ProcStart}] = p
	}
	for _, ss := range st.segs {
		for _, s := range ss {
			m.segs[s.session] = append(m.segs[s.session], s)
		}
	}
	for _, ss := range m.segs {
		sort.SliceStable(ss, func(i, j int) bool { return ss[i].start < ss[j].start })
	}
	out := map[string]bool{}
	for session := range m.segs {
		if c, ok := m.conversation(session); ok && c.State == StateOpen {
			out[session] = true
		}
	}
	return out, nil
}

// regCache holds the registry versions and deletion times a model load has read,
// with the highest record id and the row count of each kind. Registry records
// are only ever appended, so a load reads the records after those ids; a miss of
// the model cache therefore costs the new records, not every version ever
// stored. The cache is dropped when the store may have undone a write, the
// session-ref mode changed, or the rows at or below the highest id are no
// longer the rows read (a generation rewrite cleared some).
type regCache struct {
	valid   bool
	epoch   uint64
	ready   bool
	maxReg  int64
	nReg    int64
	maxDel  int64
	nDel    int64
	vers    map[pkey][]regRow
	deleted map[pkey]int64
}

// registryCountQuery and registryDeletedCountQuery count the stored records of a
// kind up to an id; they read the records_source_type index alone.
const (
	registryCountQuery        = `SELECT COUNT(*) FROM records WHERE source='session-registry' AND type='registry' AND id<=?`
	registryDeletedCountQuery = `SELECT COUNT(*) FROM records WHERE source='session-registry' AND type='registry_deleted' AND id<=?`
)

// syncRegistry brings e.reg up to the registry as db sees it. It runs under
// e.regMu, inside the read transaction of the load. On error the cache is left
// invalid, so the next load rebuilds it.
func (e *Engine) syncRegistry(db store.Querier, epoch uint64, ready bool, pick func(string) string) error {
	c := &e.reg
	if c.valid && (c.epoch != epoch || c.ready != ready || !e.regUnchanged(db, c)) {
		c.valid = false
	}
	if !c.valid {
		*c = regCache{epoch: epoch, ready: ready, vers: map[pkey][]regRow{}, deleted: map[pkey]int64{}}
	}
	c.valid = false
	rows, err := db.Query(pick(incRegistryQuery), c.maxReg)
	if err != nil {
		return err
	}
	for rows.Next() {
		var x regRow
		var pid, started sql.NullInt64
		var ps, cwd, name, status, waiting sql.NullString
		if err := rows.Scan(&x.id, &x.session, &x.t, &pid, &ps, &cwd, &started, &name, &status, &waiting); err != nil {
			_ = rows.Close()
			return err
		}
		x.pid, x.ps, x.cwd, x.started = pid.Int64, ps.String, cwd.String, started.Int64
		x.name, x.status, x.waiting = name.String, status.String, waiting.String
		k := pkey{x.pid, x.ps}
		c.vers[k] = append(c.vers[k], x)
		if x.id > c.maxReg {
			c.maxReg = x.id
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	drows, err := db.Query(incDeletedQuery, c.maxDel)
	if err != nil {
		return err
	}
	for drows.Next() {
		var id, t int64
		var pid sql.NullInt64
		var ps sql.NullString
		if err := drows.Scan(&id, &t, &pid, &ps); err != nil {
			_ = drows.Close()
			return err
		}
		k := pkey{pid.Int64, ps.String}
		if t > c.deleted[k] {
			c.deleted[k] = t
		}
		if id > c.maxDel {
			c.maxDel = id
		}
	}
	if err := drows.Close(); err != nil {
		return err
	}
	// The counts are taken after the reads, from the same snapshot: they include
	// records without fields, which the joins skip.
	if err := db.QueryRow(registryCountQuery, c.maxReg).Scan(&c.nReg); err != nil {
		return err
	}
	if err := db.QueryRow(registryDeletedCountQuery, c.maxDel).Scan(&c.nDel); err != nil {
		return err
	}
	c.valid = true
	return nil
}

// regUnchanged reports whether the stored registry records at or below the
// cached ids are exactly the ones cached (none cleared).
func (e *Engine) regUnchanged(db store.Querier, c *regCache) bool {
	var n, d int64
	if db.QueryRow(registryCountQuery, c.maxReg).Scan(&n) != nil || n != c.nReg {
		return false
	}
	return db.QueryRow(registryDeletedCountQuery, c.maxDel).Scan(&d) == nil && d == c.nDel
}
