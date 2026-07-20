package provider

import (
	"context"
	"sync"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/session"
)

// Recorder receives the subprocess timings the providers emit. It is defined
// HERE (not imported from internal/otel or internal/core/poller) so provider has
// no dependency on either. A nil Recorder disables recording. *otel.Emitter
// satisfies it at wiring time; the poller fans its PhaseRecorder in via
// SetRecorder.
type Recorder interface {
	RecordSubprocess(kind string, d time.Duration)
}

// Cache is the nested provider cache. It is accessed only from the tick
// goroutine in Phase 2 (confinement); the mutex + "never hold c.mu across a
// ps/gh backend" discipline is present so Phase 3 can move it behind the
// producer goroutine unchanged.
type Cache struct {
	now func() time.Time
	mu  sync.Mutex // guards bySession, byCwd, prLiveKeys; never held across a ps/gh backend
	rec Recorder   // nil-safe

	bySession  map[string]*sessionNode // env + terminal-host + subshell (PID/session lifecycle)
	byCwd      map[string]*cwdNode     // git-branch + repo-label (workspace lifecycle; refcount-evicted)
	prLiveKeys map[string]bool         // (cwd,branch) keys touched this scan; drives PR prune

	// Injectable fetch boundaries (nil → the documented default / no-op). Set at
	// the composition root (buildPoller) or in tests.
	PidAlive          func(pid int) bool
	FetchEnv          func(pid int) (map[string]string, error)
	FetchGitBranch    func(cwd string) (branch, headPath string, ok bool)
	FetchSubshell     func(pid int) (int, error)
	FetchTerminalHost func(pid int) string
	FetchRepoLabel    func(cwd string) (string, bool)
	PRBackend         func(ctx context.Context, cwd, branch string) (*session.PRInfo, error)
	PRPrune           func(live map[string]bool)
}

// sessionNode holds the PID/session-lifecycle lookups for one session-id.
type sessionNode struct {
	pid          int
	env          map[string]string // cached-while-alive (nil until first alive fetch)
	envFetched   bool
	terminalHost string // "" = not yet detected
	subPath      string
	subMtime     time.Time
	subCount     int
	subValid     bool
}

// cwdNode holds the workspace-lifecycle lookups for one cwd.
type cwdNode struct {
	headPath    string // resolved ancestor .git/HEAD ("" = not cached / negative)
	branch      string
	branchMtime time.Time
	branchValid bool // true only for a POSITIVE resolution (repo found)
	repoLabel   string
	repoKnown   bool
}

// New returns a Cache. A nil now defaults to time.Now.
func New(now func() time.Time) *Cache {
	if now == nil {
		now = time.Now
	}
	return &Cache{
		now:        now,
		bySession:  map[string]*sessionNode{},
		byCwd:      map[string]*cwdNode{},
		prLiveKeys: map[string]bool{},
	}
}

// SetRecorder wires a metrics recorder. Takes any so the daemon can pass an
// *otel.Emitter through an anonymous interface. A value not satisfying Recorder
// (including *Cache itself — Cache exposes Record, not RecordSubprocess) is
// ignored, so SetRecorder(cache) can never cause Record to self-recurse.
func (c *Cache) SetRecorder(r any) {
	if rec, ok := r.(Recorder); ok {
		c.rec = rec
	}
}

// Record forwards a subprocess timing to the wired Recorder (nil-safe). Named
// Record — NOT RecordSubprocess — so *Cache does not satisfy Recorder. The
// PRCache.LookupFn timing wrapper (buildPoller) calls this so pr_lookup fires
// only on a real gh spawn.
func (c *Cache) Record(kind string, d time.Duration) {
	if c.rec != nil {
		c.rec.RecordSubprocess(kind, d)
	}
}

// record times a fetch started at start and forwards it under kind.
func (c *Cache) record(kind string, start time.Time) {
	c.Record(kind, c.now().Sub(start))
}

// BeginScan resets the per-scan live-key set. The poller calls it once per tick,
// immediately after Monitor.Scan and BEFORE the per-session loop's PR calls.
func (c *Cache) BeginScan() {
	c.mu.Lock()
	c.prLiveKeys = map[string]bool{}
	c.mu.Unlock()
}

// Reconcile evicts nodes whose lifecycle ended. sessions is the full current set
// (alive + dead-PID pre-GC), so a dead-not-GC'd session's node is KEPT (its
// frozen terminal-host survives; env still returns empty for it via Env's alive
// gate). Called once per tick AFTER the per-session loop + PR calls. Evicts
// bySession ids not in sessions and byCwd cwds referenced by no session (which
// cascades git-branch + repo-label), then — after releasing c.mu — prunes the PR
// backend's vanished (cwd,branch) keys.
func (c *Cache) Reconcile(sessions []*session.Session) {
	live := make(map[string]bool, len(sessions))
	cwdRef := make(map[string]int)
	for _, s := range sessions {
		live[s.SessionID] = true
		if s.Cwd != "" {
			cwdRef[s.Cwd]++
		}
	}

	c.mu.Lock()
	for id := range c.bySession {
		if !live[id] {
			delete(c.bySession, id)
		}
	}
	for cwd := range c.byCwd {
		if cwdRef[cwd] == 0 {
			delete(c.byCwd, cwd)
		}
	}
	prune := c.PRPrune
	var keys map[string]bool
	if prune != nil {
		keys = make(map[string]bool, len(c.prLiveKeys))
		for k := range c.prLiveKeys {
			keys[k] = true
		}
	}
	c.mu.Unlock()

	// Never hold c.mu across the PR backend (its own lock + file I/O).
	if prune != nil {
		prune(keys)
	}
}
