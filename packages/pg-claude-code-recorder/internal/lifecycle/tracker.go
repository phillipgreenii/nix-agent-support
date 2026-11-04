package lifecycle

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"hash/fnv"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/clock"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/fsx"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/proc"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/source/claudecontrolend"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
)

// EndFileMaxAge is how long an end file is kept after it was ingested.
const EndFileMaxAge = 24 * time.Hour

// seenFlushEvery bounds how often a live Process's last-seen-alive time is
// journaled (it is refreshed in memory at every liveness check, and journaled at
// discovery, at exit and at close).
const seenFlushEvery = time.Minute

// TrackerConfig configures a Tracker.
type TrackerConfig struct {
	Store *store.Store
	Sys   proc.System
	Obs   *obs.Obs
	Clock clock.Clock
	// Engine reads the Conversation evidence for the end counters (optional).
	Engine *Engine
	// Sync makes the parser catch up before an evidence read (optional).
	Sync func()
	// ClaudeDir holds sessions/<pid>.json; ControlDir is the control mod's state
	// directory ("" when none).
	ClaudeDir  string
	ControlDir string
	// GraceEnable and GraceWindow are the grace-window fallback: when the registry
	// file is still present at a Process's exit, it is read again after the window.
	GraceEnable bool
	GraceWindow time.Duration
	// OnChange, when set, runs after a commit that can change which
	// Conversations are live (a Process discovered or exited); a commit of only
	// liveness refreshes does not run it. It MUST NOT block.
	OnChange func()
	// FS is the file system requests.jsonl is read through (default the real
	// one); tests pass a counting wrapper.
	FS fsx.FS
}

type tracked struct {
	key     pkey
	stop    func()
	session string
	exited  atomic.Bool
	// lastSeen, flushed, sawInbox and session are guarded by Tracker.mu.
	lastSeen time.Time
	flushed  time.Time
	sawInbox bool
}

// setSeen records a liveness check; it reports whether the time should be
// journaled now.
func (t *Tracker) setSeen(tr *tracked, now time.Time, force bool) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	tr.lastSeen = now
	if force || now.Sub(tr.flushed) >= seenFlushEvery {
		tr.flushed = now
		return true
	}
	return false
}

// Tracker observes Processes: it finds each Process from its registry file,
// binds it to its host, watches its exit and records, at that instant, whether
// the registry file is still present. It also keeps the control state directory
// tidy and counts what the control mod wrote there.
type Tracker struct {
	cfg TrackerConfig

	mu           sync.Mutex
	procs        map[pkey]*tracked
	staleStartup int
	startupDone  bool
	closed       bool
	wg           sync.WaitGroup

	endSeen map[string]bool
	reqSeen map[string]bool
	reqCur  reqCursor
}

// NewTracker returns a Tracker.
func NewTracker(cfg TrackerConfig) *Tracker {
	if cfg.Clock == nil {
		cfg.Clock = clock.Real{}
	}
	if cfg.Obs == nil {
		cfg.Obs = obs.New(nil)
	}
	return &Tracker{cfg: cfg, procs: map[pkey]*tracked{}, endSeen: map[string]bool{}, reqSeen: map[string]bool{}}
}

type regFile struct {
	PID       int64  `json:"pid"`
	ProcStart string `json:"procStart"`
	SessionID string `json:"sessionId"`
}

func (t *Tracker) now() int64 { return t.cfg.Clock.Now().UnixNano() }

func (t *Tracker) commit(recs ...*store.Rec) {
	if err := t.cfg.Store.Commit(recs...); err != nil {
		t.cfg.Obs.Log.Warn("process_observation_failed", "err", err.Error())
		return
	}
	if t.cfg.OnChange == nil {
		return
	}
	for _, r := range recs {
		if r.Kind != "seen" {
			t.cfg.OnChange()
			return
		}
	}
}

// Observe reads a registry file the recorder just ingested and starts tracking
// its Process on first sight.
func (t *Tracker) Observe(path string) {
	rel, err := filepath.Rel(filepath.Join(t.cfg.ClaudeDir, "sessions"), path)
	if err != nil || strings.Contains(rel, "/") || !strings.HasSuffix(rel, ".json") {
		return
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var rf regFile
	if json.Unmarshal(b, &rf) != nil || rf.PID <= 0 {
		return
	}
	t.track(pkey{rf.PID, rf.ProcStart}, rf.SessionID)
}

func (t *Tracker) track(k pkey, session string) {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return
	}
	if tr := t.procs[k]; tr != nil {
		old := tr.session
		tr.session = session
		t.mu.Unlock()
		if old != "" && old != session && !tr.exited.Load() {
			// The registry stopped naming the old session while the Process lives: a
			// /clear (or an in-process /resume, which has no end). Count the end once
			// the history entry can have been parsed.
			t.countEndAsync(old)
		}
		return
	}
	tr := &tracked{key: k, session: session}
	t.procs[k] = tr
	t.mu.Unlock()

	row, err := t.cfg.Store.Proc(k.pid, k.ps)
	switch {
	case err == nil && row.ExitNS > 0:
		tr.exited.Store(true)
	case err == nil:
		t.setSeen(tr, time.Unix(0, max(row.LastSeenNS, row.FirstSeenNS)), true)
		t.attach(tr)
	default:
		t.discover(tr)
	}
}

// discover is first sight of a Process: the host binding, and the stale-at-
// discovery fact when the registry file names a dead pid (gone, or a start time
// that differs from procStart: a reused pid is dead).
func (t *Tracker) discover(tr *tracked) {
	now := t.now()
	alive := proc.Alive(t.cfg.Sys, int(tr.key.pid), tr.key.ps)
	d := store.ProcRec("discover", tr.key.pid, tr.key.ps, now)
	seen := store.ProcRec("seen", tr.key.pid, tr.key.ps, now)
	if !alive {
		d.Reason = "stale"
		x := store.ProcRec("exit", tr.key.pid, tr.key.ps, now)
		x.Reason, x.Val = store.ExitUnobserved, "present"
		t.commit(d, x)
		tr.exited.Store(true)
		t.cfg.Obs.Reg.Add(obs.MStaleDiscovery, nil, 1)
		t.cfg.Obs.Event("registry_stale_at_discovery", "pid", strconv.FormatInt(tr.key.pid, 10))
		t.mu.Lock()
		if !t.startupDone {
			t.staleStartup++
		}
		t.mu.Unlock()
		return
	}
	if env, err := t.cfg.Sys.Environ(int(tr.key.pid)); err == nil {
		h := proc.HostOf(env)
		if b, err := json.Marshal(h); err == nil {
			d.Val = string(b)
		}
	}
	t.commit(d, seen)
	t.setSeen(tr, time.Unix(0, now), true)
	t.attach(tr)
}

// attach registers the exit watch. A watch on an exited pid fails (ESRCH): that
// failure is the recorded fact, an unobserved exit.
func (t *Tracker) attach(tr *tracked) {
	if !proc.Alive(t.cfg.Sys, int(tr.key.pid), tr.key.ps) {
		t.recordUnobserved(tr)
		return
	}
	stop, err := t.cfg.Sys.Watch(int(tr.key.pid), func(at time.Time) {
		t.mu.Lock()
		if t.closed {
			t.mu.Unlock()
			return
		}
		t.wg.Add(1)
		t.mu.Unlock()
		go func() {
			defer t.wg.Done()
			t.onExit(tr, at)
		}()
	})
	if err != nil {
		t.recordUnobserved(tr)
		return
	}
	t.mu.Lock()
	tr.stop = stop
	t.mu.Unlock()
}

func (t *Tracker) recordUnobserved(tr *tracked) {
	if !tr.exited.CompareAndSwap(false, true) {
		return
	}
	x := store.ProcRec("exit", tr.key.pid, tr.key.ps, t.now())
	x.Reason, x.Val = store.ExitUnobserved, t.presentVal(tr.key.pid)
	t.commit(x)
	t.removeFifo(tr.key.pid)
}

func (t *Tracker) registryFile(pid int64) string {
	return filepath.Join(t.cfg.ClaudeDir, "sessions", strconv.FormatInt(pid, 10)+".json")
}

func (t *Tracker) presentVal(pid int64) string {
	if _, err := os.Lstat(t.registryFile(pid)); err == nil {
		return "present"
	}
	return "absent"
}

// onExit runs when the kqueue watch fires: one lstat of the registry file right
// after the event (and again after the grace window when it is on and the file
// is still there), then the exit is recorded.
func (t *Tracker) onExit(tr *tracked, at time.Time) {
	if !tr.exited.CompareAndSwap(false, true) {
		return
	}
	t.mu.Lock()
	session := tr.session
	t.mu.Unlock()
	val := t.presentVal(tr.key.pid)
	if val == "present" && t.cfg.GraceEnable && t.cfg.GraceWindow > 0 {
		t.cfg.Clock.Sleep(t.cfg.GraceWindow)
		val = t.presentVal(tr.key.pid)
	}
	x := store.ProcRec("exit", tr.key.pid, tr.key.ps, at.UnixNano())
	x.Reason, x.Val = store.ExitObserved, val
	seen := store.ProcRec("seen", tr.key.pid, tr.key.ps, at.UnixNano())
	t.commit(seen, x)
	t.removeFifo(tr.key.pid)
	t.cfg.Obs.Event(obs.EvProcessExit, "pid", strconv.FormatInt(tr.key.pid, 10), "registry_present", strconv.FormatBool(val == "present"))
	t.countEnd(session, val == "present")
}

func b2s(b bool) string { return strconv.FormatBool(b) }

// countEnd adds one to conversation_end_total with the evidence classes the
// Conversation ended with (the daemon exports evidence-class counts, never
// labels).
func (t *Tracker) countEnd(session string, present bool) {
	if t.cfg.Sync != nil {
		t.cfg.Sync()
	}
	ev := Evidence{ExitObserved: true, RegistryPresentAtExit: present}
	if t.cfg.Engine != nil && session != "" {
		if c, err := t.cfg.Engine.Show(context.Background(), session); err == nil {
			ev = c.Evidence
		}
	}
	t.addEnd(ev)
}

func (t *Tracker) addEnd(ev Evidence) {
	t.cfg.Obs.Reg.Add(obs.MConversationEnd, map[string]string{
		"registry_present": b2s(ev.RegistryPresentAtExit), "exit_observed": b2s(ev.ExitObserved),
		"clear_entry": b2s(ev.ClearEntry), "exit_entry": b2s(ev.ExitEntry),
	}, 1)
}

func (t *Tracker) countEndAsync(old string) {
	t.wg.Add(1)
	go func() {
		defer t.wg.Done()
		if t.cfg.GraceEnable && t.cfg.GraceWindow > 0 {
			t.cfg.Clock.Sleep(t.cfg.GraceWindow)
		}
		if t.cfg.Sync != nil {
			t.cfg.Sync()
		}
		if t.cfg.Engine == nil {
			return
		}
		if c, err := t.cfg.Engine.Show(context.Background(), old); err == nil && c.Ended {
			t.addEnd(c.Evidence)
		}
	}()
}

// Startup finds the Processes named by the registry files already on disk and
// re-registers the watch of every known Process that is still alive; one that
// ended while the recorder was down gets an unobserved exit.
func (t *Tracker) Startup() {
	ents, _ := os.ReadDir(filepath.Join(t.cfg.ClaudeDir, "sessions"))
	for _, e := range ents {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			t.Observe(filepath.Join(t.cfg.ClaudeDir, "sessions", e.Name()))
		}
	}
	rows, err := t.cfg.Store.Procs()
	if err != nil {
		return
	}
	for _, r := range rows {
		if r.ExitNS > 0 {
			continue
		}
		k := pkey{r.Pid, r.ProcStart}
		t.mu.Lock()
		if t.procs[k] != nil || t.closed {
			t.mu.Unlock()
			continue
		}
		tr := &tracked{key: k}
		t.procs[k] = tr
		t.mu.Unlock()
		t.attach(tr)
	}
	t.sweepFifos()
}

// StartupDone logs how many registry files named a dead pid at first discovery
// during the startup scan, so a power loss is visible without running the query.
func (t *Tracker) StartupDone() {
	t.mu.Lock()
	n := t.staleStartup
	t.startupDone = true
	t.mu.Unlock()
	t.cfg.Obs.Event(obs.EvStartupStale, "n", strconv.Itoa(n))
}

// Tick refreshes last-seen-alive for every live Process (a liveness check) and
// journals it at most once a minute.
func (t *Tracker) Tick() {
	now := t.cfg.Clock.Now()
	t.mu.Lock()
	var live []*tracked
	for _, tr := range t.procs {
		if !tr.exited.Load() {
			live = append(live, tr)
		}
	}
	t.mu.Unlock()
	for _, tr := range live {
		if !proc.Alive(t.cfg.Sys, int(tr.key.pid), tr.key.ps) {
			continue
		}
		if t.setSeen(tr, now, false) {
			t.commit(store.ProcRec("seen", tr.key.pid, tr.key.ps, now.UnixNano()))
		}
	}
	t.refreshInbox(live)
}

func (t *Tracker) refreshInbox(live []*tracked) {
	if t.cfg.ControlDir == "" {
		return
	}
	counts := map[string]float64{"present": 0, "gone": 0, "never_loaded": 0}
	without := 0.0
	for _, tr := range live {
		if tr.exited.Load() {
			continue
		}
		_, err := os.Lstat(filepath.Join(t.cfg.ControlDir, "inbox", strconv.FormatInt(tr.key.pid, 10)+".fifo"))
		t.mu.Lock()
		if err == nil {
			tr.sawInbox = true
		}
		saw := tr.sawInbox
		t.mu.Unlock()
		switch {
		case err == nil:
			counts["present"]++
		case saw:
			counts["gone"]++
			without++
		default:
			counts["never_loaded"]++
			without++
		}
	}
	for st, n := range counts {
		t.cfg.Obs.Reg.Set(obs.MControlInbox, map[string]string{"state": st}, n)
	}
	t.cfg.Obs.Reg.Set(obs.MNoInbox, nil, without)
}

// Poll does the control state directory's periodic work: it counts and removes
// end files, counts the mod's requests and removes stale FIFOs.
func (t *Tracker) Poll() {
	if t.cfg.ControlDir == "" {
		return
	}
	t.sweepEnds()
	t.readRequests()
	t.sweepFifos()
}

func (t *Tracker) removeFifo(pid int64) {
	if t.cfg.ControlDir == "" {
		return
	}
	p := filepath.Join(t.cfg.ControlDir, "inbox", strconv.FormatInt(pid, 10)+".fifo")
	if err := os.Remove(p); err == nil {
		t.cfg.Obs.Reg.Add(obs.MStaleFifosRemoved, nil, 1)
	}
}

// sweepFifos removes the FIFOs whose pid is not a live Process with a matching
// start time (the recorder's first write into the control state directory).
func (t *Tracker) sweepFifos() {
	if t.cfg.ControlDir == "" {
		return
	}
	dir := filepath.Join(t.cfg.ControlDir, "inbox")
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	rows, _ := t.cfg.Store.Procs()
	latest := map[int64]store.Proc{}
	for _, r := range rows {
		if cur, ok := latest[r.Pid]; !ok || r.FirstSeenNS >= cur.FirstSeenNS {
			latest[r.Pid] = r
		}
	}
	for _, e := range ents {
		name, ok := strings.CutSuffix(e.Name(), ".fifo")
		if !ok {
			continue
		}
		pid, err := strconv.ParseInt(name, 10, 64)
		if err != nil {
			continue
		}
		stale := false
		if r, ok := latest[pid]; ok {
			stale = !proc.Alive(t.cfg.Sys, int(pid), r.ProcStart)
		} else {
			_, serr := t.cfg.Sys.StartTime(int(pid))
			stale = errors.Is(serr, proc.ErrGone)
		}
		if stale {
			t.removeFifo(pid)
		}
	}
}

// sweepEnds validates each end file once (end_reason_source_files_total) and
// removes one after it has been ingested and a day has passed (the recorder's
// second write into the control state directory).
func (t *Tracker) sweepEnds() {
	dir := filepath.Join(t.cfg.ControlDir, "ends")
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	reg := t.cfg.Obs.Reg
	for _, e := range ents {
		name := e.Name()
		if e.IsDir() || strings.HasPrefix(name, ".") || !strings.HasSuffix(name, ".json") {
			continue
		}
		p := filepath.Join(dir, name)
		if !t.endSeen[p] {
			t.endSeen[p] = true
			b, err := os.ReadFile(p)
			var end claudecontrolend.End
			if err != nil || json.Unmarshal(b, &end) != nil || !end.Valid() {
				reg.Add(obs.MEndFiles, map[string]string{"outcome": "invalid"}, 1)
				continue
			}
			reg.Add(obs.MEndFiles, map[string]string{"outcome": "ingested"}, 1)
		}
		files, err := t.cfg.Store.FilesByCurrentPath(p)
		if err != nil || len(files) == 0 {
			continue
		}
		snap, err := t.cfg.Store.LatestSnapshot(files[0].ID)
		if err != nil || t.cfg.Clock.Now().Sub(time.Unix(0, snap.SeenNS)) < EndFileMaxAge {
			continue
		}
		var n int
		if err := t.cfg.Store.Read().QueryRow(`SELECT COUNT(*) FROM records WHERE file_id=?`, files[0].ID).Scan(&n); err != nil || n == 0 {
			continue // not parsed yet (or invalid): keep it
		}
		if err := os.Remove(p); err == nil {
			reg.Add(obs.MEndFiles, map[string]string{"outcome": "removed"}, 1)
			delete(t.endSeen, p)
		}
	}
}

type requestLine struct {
	TS      json.RawMessage `json:"ts"`
	ID      json.RawMessage `json:"id"`
	Verb    string          `json:"verb"`
	Outcome string          `json:"outcome"`
}

func bucket(v string, known []string) string {
	for _, k := range known {
		if k == v {
			return v
		}
	}
	return obs.ClientOther
}

// reqHashLen is how many bytes the request-window cursor hashes at each end.
const reqHashLen = 64

// reqCursor remembers where the last read of requests.jsonl stopped. The mod
// keeps the last 1,000 lines, so the file is rewritten or truncated as well as
// appended to; the tail path is taken only when every field still matches.
type reqCursor struct {
	valid bool
	ino   uint64
	size  int64
	off   int64
	// headBuf holds the first min(64, off) bytes and tailBuf the up-to-64 bytes
	// before off, as last read; head and tail are their hashes. Keeping the
	// bytes lets a tail read extend the cursor without re-reading them.
	headBuf, tailBuf []byte
	head, tail       uint64
}

func hashBytes(b []byte) uint64 {
	h := fnv.New64a()
	_, _ = h.Write(b)
	return h.Sum64()
}

func fileIno(fi fs.FileInfo) uint64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Ino)
	}
	return 0
}

// extend returns the cursor after consuming consumed (complete lines) that
// start at the old cursor's offset; a zero cursor starts at offset 0. It is
// invalid when nothing has been consumed yet.
func (c reqCursor) extend(ino uint64, size int64, consumed []byte) reqCursor {
	n := reqCursor{ino: ino, size: size, off: c.off + int64(len(consumed))}
	n.headBuf = append([]byte(nil), c.headBuf...)
	if room := reqHashLen - len(n.headBuf); room > 0 {
		if room > len(consumed) {
			room = len(consumed)
		}
		n.headBuf = append(n.headBuf, consumed[:room]...)
	}
	tail := append(append([]byte(nil), c.tailBuf...), consumed...)
	if len(tail) > reqHashLen {
		tail = tail[len(tail)-reqHashLen:]
	}
	n.tailBuf = tail
	n.head, n.tail = hashBytes(n.headBuf), hashBytes(n.tailBuf)
	n.valid = n.off > 0
	return n
}

// matches reports whether the first off bytes of f still hash as they did when
// the cursor was taken: it reads the head window and the window before off.
func (c reqCursor) matches(f fsx.File) bool {
	head := make([]byte, len(c.headBuf))
	if _, err := f.ReadAt(head, 0); err != nil || hashBytes(head) != c.head {
		return false
	}
	tail := make([]byte, len(c.tailBuf))
	if _, err := f.ReadAt(tail, c.off-int64(len(tail))); err != nil {
		return false
	}
	return hashBytes(tail) == c.tail
}

// readRequests counts each line of the mod's request window once, keyed by ts, id
// and verb. It reads beyond the ends/ root (requests.jsonl), may undercount under
// a burst of more than 1,000 requests between two polls (the mod keeps the last
// 1,000), and buckets an unknown verb or outcome as other. An unchanged file is
// not read; a grown file with an intact prefix is read from the cursor only; any
// other change (shrink, new inode, rewritten prefix) is a full read, whose keyed
// dedup keeps a line from being counted twice.
func (t *Tracker) readRequests() {
	path := filepath.Join(t.cfg.ControlDir, "requests.jsonl")
	fi, err := t.reqFS().Stat(path)
	if err != nil {
		t.reqCur = reqCursor{}
		return
	}
	ino, size := fileIno(fi), fi.Size()
	c := t.reqCur
	if c.valid && c.ino == ino && c.size == size {
		return
	}
	f, err := t.reqFS().OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		t.reqCur = reqCursor{}
		return
	}
	defer func() { _ = f.Close() }()
	if c.valid && c.ino == ino && size > c.off && c.matches(f) {
		t.readRequestsTail(f, ino, size, c.off)
		return
	}
	t.readRequestsFull(f, ino, size)
}

func (t *Tracker) readRequestsTail(f fsx.File, ino uint64, size, off int64) {
	buf := make([]byte, size-off)
	n, err := f.ReadAt(buf, off)
	if err != nil && !errors.Is(err, io.EOF) {
		t.reqCur = reqCursor{}
		return
	}
	buf = buf[:n]
	end := bytes.LastIndexByte(buf, '\n') + 1
	t.cfg.Obs.Reg.Add(obs.MRequestsJSONReads, map[string]string{"path": "tail"}, 1)
	t.countRequestLines(buf[:end], t.reqSeen)
	t.reqCur = t.reqCur.extend(ino, size, buf[:end])
}

func (t *Tracker) readRequestsFull(f fsx.File, ino uint64, size int64) {
	buf := make([]byte, size)
	n, err := f.ReadAt(buf, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		t.reqCur = reqCursor{}
		return
	}
	buf = buf[:n]
	t.cfg.Obs.Reg.Add(obs.MRequestsJSONReads, map[string]string{"path": "full"}, 1)
	end := bytes.LastIndexByte(buf, '\n') + 1
	next := map[string]bool{}
	t.countRequestLines(buf[:end], t.reqSeen, next)
	t.reqSeen = next
	t.reqCur = reqCursor{}.extend(ino, int64(n), buf[:end])
}

// countRequestLines counts each complete line whose key is not in seen. A key is
// added to seen, and to every extra map (the rebuilt set of a full read).
func (t *Tracker) countRequestLines(b []byte, seen map[string]bool, extra ...map[string]bool) {
	for _, ln := range bytes.Split(b, []byte("\n")) {
		if len(bytes.TrimSpace(ln)) == 0 {
			continue
		}
		var r requestLine
		if json.Unmarshal(ln, &r) != nil {
			continue
		}
		key := string(r.TS) + "|" + string(r.ID) + "|" + r.Verb
		dup := seen[key]
		seen[key] = true
		for _, m := range extra {
			m[key] = true
		}
		if dup {
			continue
		}
		outcome := r.Outcome
		if strings.HasPrefix(outcome, "refused") {
			outcome = "refused"
		}
		t.cfg.Obs.Reg.Add(obs.MControlRequests, map[string]string{
			"verb": bucket(r.Verb, obs.ControlVerbs), "outcome": bucket(outcome, obs.ControlOutcomes),
		}, 1)
	}
}

func (t *Tracker) reqFS() fsx.FS {
	if t.cfg.FS != nil {
		return t.cfg.FS
	}
	return fsx.OS{}
}

// Close stops the watches and journals each live Process's last-seen-alive time.
func (t *Tracker) Close() {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return
	}
	t.closed = true
	var live []*tracked
	for _, tr := range t.procs {
		if !tr.exited.Load() {
			live = append(live, tr)
		}
	}
	t.mu.Unlock()
	t.wg.Wait()
	for _, tr := range live {
		t.mu.Lock()
		stop, ls := tr.stop, tr.lastSeen
		t.mu.Unlock()
		if stop != nil {
			stop()
		}
		if !ls.IsZero() {
			t.commit(store.ProcRec("seen", tr.key.pid, tr.key.ps, ls.UnixNano()))
		}
	}
}
