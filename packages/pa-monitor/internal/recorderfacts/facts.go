// Package recorderfacts is pa-monitor's one reader of Claude Code's facts: the
// registrations of the running Claude processes, whether each is alive, and the
// sessions and search results the CLIs print. It reads them through the
// recorder's Go client package and never from Claude's files; while the recorder
// is not answering it reports session.ErrDaemonOffline ("the recorder is not
// available") and does not fall back to guessing from files.
package recorderfacts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/session"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/client"
)

// MinPollInterval is the fastest pa-monitor asks the recorder for facts: the
// design polls the change feed every 5 to 10 seconds (Push or poll).
const MinPollInterval = 5 * time.Second

// maxDeadProcesses bounds how many ended Processes one discovery reads.
const maxDeadProcesses = 500

// Facts reads the recorder. It dials lazily and is safe for concurrent use.
type Facts struct {
	opts client.Options

	mu sync.Mutex
	c  *client.Client

	// scanMu serialises Registrations and guards the carried discovery state
	// below (never read by GC).
	scanMu      sync.Mutex
	endedSeeded bool
	// endedPending holds ended session ids whose registry lookup failed.
	endedPending map[string]bool
	live, ended  []*session.Session

	// The discovery gate (see SetDiscoveryHeartbeat). heartbeat <= 0 means no
	// gate: every Registrations call reads the recorder.
	heartbeat    time.Duration
	now          func() time.Time
	regCursor    string    // position in the session-registry feed
	lastFetch    time.Time // when the last successful full discovery ran
	fetchFailed  bool      // the last full discovery failed: retry on the next call
	haveSnapshot bool
	feedTimeout  time.Duration // 0 means DefaultFeedTimeout
	// pidAlive is the local liveness probe (nil means session.DefaultPidAlive).
	// goneTries counts, per pid, the discoveries the local probe has forced
	// while the recorder still listed the Process alive.
	pidAlive  func(pid int) bool
	goneTries map[int]int
	// emptyPids are the alive Processes the last discovery listed with no
	// session id yet; emptyTries counts, per pid, the discoveries that
	// sessionless state has forced.
	emptyPids  []int
	emptyTries map[int]int

	// rec receives the lag and gate metrics (nil: none). goneSince is when the
	// local probe first found each pid dead; goneExhausted and emptyExhausted
	// remember the pids whose try budget was already reported as used up.
	rec            Recorder
	goneSince      map[int]time.Time
	goneExhausted  map[int]bool
	emptyExhausted map[int]bool
}

// Recorder receives the discovery metrics. *otel.Emitter satisfies it.
//
// Definitions (all from timestamps pa-monitor already has):
//
//   - appear lag: the discovery's clock minus the Process start time the
//     recorder reports (second resolution) for a session that the discovery
//     lists alive for the first time (a new pid; a session id change on a known
//     pid, a resume, is not an appearance). It is the time from the session's
//     start to the first discovery that lists it, so it includes the scan cycle
//     that follows the discovery before a viewer sees it. The first discovery
//     after start or after a recorder outage records nothing.
//   - leave lag: the discovery's clock minus the moment the local kill -0 probe
//     first found the pid dead, for a session the discovery stops listing
//     alive. It is a lower bound (the pid may have died before the first probe)
//     and records nothing for a session the probe never saw dead (the
//     probe runs only on a scan whose registry feed was quiet).
type Recorder interface {
	// RecordSessionLag records one lag; kind is "appear" or "leave".
	RecordSessionLag(kind string, d time.Duration)
	// RecordDiscoveryForced counts a discovery the gate ran only because of the
	// local probe; reason is "gone" (dead pid still listed alive) or "empty"
	// (alive Process without a session id).
	RecordDiscoveryForced(reason string)
	// RecordDiscoveryBudgetExhausted counts, once per pid, a pid that wanted
	// another forced discovery after using its whole try budget; kind is "gone"
	// (maxGoneTries) or "empty" (maxEmptyTries).
	RecordDiscoveryBudgetExhausted(kind string)
}

// SetRecorder wires the metric recorder. Takes any so a caller can pass a value
// that may or may not implement Recorder; one that does not (or nil) disables
// recording. Call it before the first Registrations.
func (f *Facts) SetRecorder(r any) {
	f.scanMu.Lock()
	defer f.scanMu.Unlock()
	if rec, ok := r.(Recorder); ok {
		f.rec = rec
	}
}

// maxEmptyTries bounds how many discoveries one alive Process with no session
// id yet can force (the recorder binds a new session's registry record to its
// Process within a few seconds; a Process that never gets one, such as a
// non-interactive Claude run, must not make every Scan discover).
const maxEmptyTries = 6

// maxGoneTries bounds how many discoveries one pid can force by being dead
// locally while the recorder still lists it alive (the recorder notices an exit
// within a few seconds; a pid the probe cannot judge, such as one in another
// pid domain, must not make every Scan discover).
const maxGoneTries = 4

// DefaultDiscoveryHeartbeat is how long a quiet registry feed may go before
// discovery runs anyway (a process can start or exit without a registry write).
const DefaultDiscoveryHeartbeat = 60 * time.Second

// SetDiscoveryHeartbeat turns on the registry-change gate: Registrations then
// re-runs process.list and the per-session registry lookups only when the
// session-registry feed shows a new record, when d has passed since the last
// successful discovery, or when the previous discovery failed; otherwise it
// returns the previous answer after one cheap feed read. d <= 0 turns the gate
// off. Call it before the first Registrations.
func (f *Facts) SetDiscoveryHeartbeat(d time.Duration) {
	f.scanMu.Lock()
	f.heartbeat = d
	f.scanMu.Unlock()
}

func (f *Facts) clock() time.Time {
	if f.now != nil {
		return f.now()
	}
	return time.Now()
}

// registryFeedPage is how many registry feed entries one gate read asks for.
const registryFeedPage = 1000

// DefaultFeedTimeout bounds one registry-feed read of the gate. It is far
// shorter than the discovery timeout so a slow feed can never use up the
// context discovery still needs.
const DefaultFeedTimeout = 3 * time.Second

// registryChanged reads the session-registry feed (a second cursor, separate
// from the transcript drain: only registry records, no bodies) and reports
// whether anything arrived since the last read. Every read has its own short
// deadline (feedTimeout), so it cannot starve the discovery that follows.
//
// The first read seeds the cursor from the feed's head (one snapshot page of
// one record, never a read of the whole retained feed) and reports true. An
// expired cursor is reseeded from the head and reported as a change. A feed
// read that times out or fails for any reason other than the recorder being
// down is NOT a change: the cursor is kept and the caller relies on its
// heartbeat and failed-fetch retry instead of discovering on every scan.
func (f *Facts) registryChanged(ctx context.Context, c *client.Client) (bool, error) {
	where := &client.Filter{Field: "source", Eq: json.RawMessage(`"session-registry"`)}
	timeout := f.feedTimeout
	if timeout <= 0 {
		timeout = DefaultFeedTimeout
	}
	fctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if f.regCursor == "" {
		snap, err := c.Snapshot(fctx, client.SnapshotRequest{Where: where, Limit: 1})
		if err != nil {
			if isOffline(err) {
				return true, f.wrapScan(err)
			}
			return false, nil
		}
		f.regCursor = snap.Cursor
		return true, nil
	}
	changed := false
	for {
		ans, err := c.FeedPage(fctx, where, f.regCursor, []string{"source"}, registryFeedPage, 0)
		if err != nil {
			switch {
			case isOffline(err):
				f.regCursor = ""
				return true, f.wrapScan(err)
			case errors.Is(err, client.ErrCursorExpired):
				f.regCursor = ""
				return true, nil
			}
			return changed, nil
		}
		if len(ans.Entries) > 0 {
			changed = true
		}
		if ans.NextCursor != "" {
			f.regCursor = ans.NextCursor
		}
		if !ans.More {
			return changed, nil
		}
	}
}

// liveProcessGone reports whether a session the last discovery listed alive has
// a pid that is no longer alive on this host. A Process can exit without any
// session-registry record (the recorder learns of it from its own tracker), so
// the registry feed alone would show the exit only at the next heartbeat. The
// probe is a local kill -0: it sends no request to the recorder. It only
// triggers a discovery, the recorder's answer stays the truth.
func (f *Facts) liveProcessGone() bool {
	probe := f.pidAlive
	if probe == nil {
		probe = session.DefaultPidAlive
	}
	seen := make(map[int]bool, len(f.live))
	gone := false
	for _, s := range f.live {
		if s.PID <= 0 {
			continue
		}
		seen[s.PID] = true
		if probe(s.PID) {
			delete(f.goneTries, s.PID)
			delete(f.goneSince, s.PID)
			delete(f.goneExhausted, s.PID)
			continue
		}
		if f.goneTries == nil {
			f.goneTries = map[int]int{}
		}
		if f.goneSince == nil {
			f.goneSince = map[int]time.Time{}
		}
		if _, ok := f.goneSince[s.PID]; !ok {
			f.goneSince[s.PID] = f.clock()
		}
		if f.goneTries[s.PID] < maxGoneTries {
			f.goneTries[s.PID]++
			gone = true
		} else if !f.goneExhausted[s.PID] {
			if f.goneExhausted == nil {
				f.goneExhausted = map[int]bool{}
			}
			f.goneExhausted[s.PID] = true
			if f.rec != nil {
				f.rec.RecordDiscoveryBudgetExhausted("gone")
			}
		}
	}
	for pid := range f.goneTries {
		if !seen[pid] {
			delete(f.goneTries, pid)
			delete(f.goneExhausted, pid)
		}
	}
	return gone
}

// sessionIDPending reports whether the last discovery listed an alive Process
// whose session id was still empty: a session start writes its registry record
// before the Process fact carries the session id, so the one discovery the
// record triggered saw nothing new and the next would be the heartbeat. Each
// such pid forces at most maxEmptyTries rediscoveries, and none once it has a
// session id, so the steady process.list rate is unchanged.
func (f *Facts) sessionIDPending() bool {
	pending := false
	seen := make(map[int]bool, len(f.emptyPids))
	for _, pid := range f.emptyPids {
		seen[pid] = true
		if f.emptyTries[pid] < maxEmptyTries {
			f.emptyTries[pid]++
			pending = true
		} else if !f.emptyExhausted[pid] {
			if f.emptyExhausted == nil {
				f.emptyExhausted = map[int]bool{}
			}
			f.emptyExhausted[pid] = true
			if f.rec != nil {
				f.rec.RecordDiscoveryBudgetExhausted("empty")
			}
		}
	}
	for pid := range f.emptyTries {
		if !seen[pid] {
			delete(f.emptyTries, pid)
			delete(f.emptyExhausted, pid)
		}
	}
	return pending
}

// cachedRegistrations is the previous discovery answer, copied.
func (f *Facts) cachedRegistrations() []*session.Session {
	out := make([]*session.Session, 0, len(f.live)+len(f.ended))
	for _, l := range [][]*session.Session{f.live, f.ended} {
		for _, s := range l {
			cp := *s
			out = append(out, &cp)
		}
	}
	return out
}

// New returns a reader that discovers the daemon's socket from opts (the
// environment variable, then the default path, when opts.Socket is empty).
func New(opts client.Options) *Facts {
	if opts.Name == "" {
		opts.Name = "pa-monitor"
	}
	return &Facts{opts: opts}
}

// Client returns the connected recorder client, dialing when not yet connected.
// A daemon that is not answering is session.ErrDaemonOffline.
func (f *Facts) Client(ctx context.Context) (*client.Client, error) {
	f.mu.Lock()
	if f.c != nil {
		c := f.c
		f.mu.Unlock()
		return c, nil
	}
	f.mu.Unlock()
	c, err := client.Dial(ctx, f.opts)
	if err != nil {
		return nil, f.wrap(err)
	}
	f.mu.Lock()
	f.c = c
	f.mu.Unlock()
	return c, nil
}

// wrap turns a transport-level "not answering" into session.ErrDaemonOffline and
// drops the cached connection so the next call dials again.
func (f *Facts) wrap(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, client.ErrDown) || errors.Is(err, client.ErrUnavailable) {
		f.mu.Lock()
		f.c = nil
		f.mu.Unlock()
		return session.ErrDaemonOffline
	}
	return err
}

// Socket is the recorder socket this reader dials: the explicit option, else the
// environment variable or default path the client would discover.
func (f *Facts) Socket() string {
	if f.opts.Socket != "" {
		return f.opts.Socket
	}
	return client.DefaultSocket(f.opts.Getenv)
}

// Unavailable words a failed recorder-backed command: when err says the
// recorder is not answering it names what could not be done and the socket,
// "cannot <action>: the recorder is not available (socket <path>)"; any other
// error is returned unchanged.
func (f *Facts) Unavailable(action string, err error) error {
	if errors.Is(err, session.ErrDaemonOffline) {
		return fmt.Errorf("cannot %s: %w (socket %s)", action, err, f.Socket())
	}
	return err
}

// Wrap is wrap for callers that run their own operations on Client.
func (f *Facts) Wrap(err error) error { return f.wrap(err) }

// registryExtra is what only the registry file's own bytes carry.
type registryExtra struct {
	PID             int64    `json:"pid"`
	Kind            string   `json:"kind"`
	Entrypoint      string   `json:"entrypoint"`
	WaitingFor      string   `json:"waitingFor"`
	StatusUpdatedAt *float64 `json:"statusUpdatedAt"`
}

// registryLookupWorkers bounds how many per-session registry lookups one Scan
// has in flight; the recorder serves at most 8 query connections at once.
const registryLookupWorkers = 4

// Registrations implements session.Registry for discovery: one row per session
// the recorder knows a Process for, alive Processes first, with PidAlive from
// the Process facts (no kill -0 probe and no ps start-time guard: the recorder
// keys a Process by pid and start time, so a reused pid is a different
// Process).
//
// Each call reads the alive Processes and then one newest registry version per
// LIVE session (kind, entrypoint, waitingFor and the turn-start marker live
// only in the registry file's bytes). Ended Processes are read once, the first
// time after the recorder is reached (and again after a recorder outage or
// ResyncEnded); after that a session that leaves the alive set is carried
// forward as ended, with the registry fields it last had, so a steady-state
// Scan sends no process.list for ended Processes and no registry read for an
// ended session. GC must not use this snapshot: it asks KnownSessionIDs.
func (f *Facts) Registrations(ctx context.Context) ([]*session.Session, error) {
	f.scanMu.Lock()
	defer f.scanMu.Unlock()
	c, err := f.Client(ctx)
	if err != nil {
		f.resetEnded()
		return nil, err
	}
	if f.heartbeat > 0 {
		changed, err := f.registryChanged(ctx, c)
		if err != nil {
			return nil, err
		}
		if !changed && f.liveProcessGone() {
			changed = true
			if f.rec != nil {
				f.rec.RecordDiscoveryForced("gone")
			}
		}
		if !changed && f.sessionIDPending() {
			changed = true
			if f.rec != nil {
				f.rec.RecordDiscoveryForced("empty")
			}
		}
		if f.haveSnapshot && !f.fetchFailed && !changed && f.clock().Sub(f.lastFetch) < f.heartbeat {
			return f.cachedRegistrations(), nil
		}
	}
	out, err := f.discover(ctx, c)
	if err != nil {
		f.fetchFailed = true
		return nil, err
	}
	f.fetchFailed, f.haveSnapshot, f.lastFetch = false, true, f.clock()
	return out, nil
}

// discover is one full discovery: process.list and the registry lookups.
func (f *Facts) discover(ctx context.Context, c *client.Client) ([]*session.Session, error) {
	yes, no := true, false
	alive, err := c.ProcessList(ctx, client.ProcessFilter{Alive: &yes, Limit: 1000})
	if err != nil {
		return nil, f.wrapScan(err)
	}
	var live []*session.Session
	liveSeen := map[string]bool{}
	var empty []int
	for _, p := range alive.Processes {
		if p.SessionID == "" {
			empty = append(empty, int(p.Pid))
			continue
		}
		if liveSeen[p.SessionID] {
			continue
		}
		liveSeen[p.SessionID] = true
		live = append(live, fromProcess(p))
	}

	f.recordAppear(live)

	var ended []*session.Session // newest first
	if !f.endedSeeded {
		dead, err := c.ProcessList(ctx, client.ProcessFilter{Alive: &no, Limit: maxDeadProcesses})
		if err != nil {
			return nil, f.wrapScan(err)
		}
		for _, p := range dead.Processes {
			if p.SessionID == "" || liveSeen[p.SessionID] || hasID(ended, p.SessionID) {
				continue
			}
			ended = append(ended, fromProcess(p))
		}
		// One registry read per ended session, once, so their stored rows keep
		// kind and entrypoint across a daemon restart.
		// A lookup that fails (a query timeout, the discovery deadline on a long
		// list) is remembered and retried on later Scans, so a failed read does
		// not leave the stored row blank for the life of the daemon.
		f.endedPending = map[string]bool{}
		for i, err := range f.fillRegistry(ctx, c, ended) {
			if err != nil {
				f.endedPending[ended[i].SessionID] = true
			}
		}
	} else {
		ended = f.ended[:0:0]
		var gone []*session.Session
		for _, s := range f.live {
			if !liveSeen[s.SessionID] {
				gone = append(gone, s)
			}
		}
		for _, s := range gone {
			if t, ok := f.goneSince[s.PID]; ok && f.rec != nil {
				f.rec.RecordSessionLag("leave", f.clock().Sub(t))
			}
			// Carried forward as ended with the facts it last had: no recorder read.
			d := *s
			d.PidAlive = false
			ended = append(ended, &d)
		}
		for _, s := range f.ended {
			if !liveSeen[s.SessionID] && !hasID(ended, s.SessionID) {
				ended = append(ended, s)
			}
		}
		f.retryEndedPending(ctx, c, ended)
	}
	if len(ended) > maxDeadProcesses {
		ended = ended[:maxDeadProcesses]
	}

	if err := f.fillRegistryLive(ctx, c, live); err != nil {
		return nil, err
	}
	f.live, f.ended, f.endedSeeded = live, ended, true
	for pid := range f.goneSince {
		if !hasPID(live, pid) {
			delete(f.goneSince, pid)
		}
	}
	f.emptyPids = empty
	if f.emptyTries == nil {
		f.emptyTries = map[int]int{}
	}

	out := make([]*session.Session, 0, len(live)+len(ended))
	for _, s := range live {
		cp := *s
		out = append(out, &cp)
	}
	for _, s := range ended {
		cp := *s
		out = append(out, &cp)
	}
	return out, nil
}

// recordAppear records the appear lag of every session in live that no earlier
// discovery listed (by session id or pid). It records nothing before the first
// discovery or after a recorder outage (endedSeeded false: every session would
// look new), nor for a session whose start time is unknown or in the future.
func (f *Facts) recordAppear(live []*session.Session) {
	if f.rec == nil || !f.endedSeeded {
		return
	}
	now := f.clock()
	for _, s := range live {
		if s.StartedAt.IsZero() || hasID(f.live, s.SessionID) || hasID(f.ended, s.SessionID) || hasPID(f.live, s.PID) {
			continue
		}
		if d := now.Sub(s.StartedAt); d >= 0 {
			f.rec.RecordSessionLag("appear", d)
		}
	}
}

func hasPID(ss []*session.Session, pid int) bool {
	for _, s := range ss {
		if s.PID == pid {
			return true
		}
	}
	return false
}

// endedRetryBatch bounds how many failed ended-session lookups one Scan retries.
const endedRetryBatch = 25

// retryEndedPending re-reads the registry fields of ended sessions whose
// startup lookup failed, newest first, a bounded batch per Scan. It touches
// nothing when no lookup failed, so a healthy steady-state Scan sends no ended
// read.
func (f *Facts) retryEndedPending(ctx context.Context, c *client.Client, ended []*session.Session) {
	if len(f.endedPending) == 0 {
		return
	}
	var batch []*session.Session
	for _, s := range ended {
		if f.endedPending[s.SessionID] {
			batch = append(batch, s)
			if len(batch) == endedRetryBatch {
				break
			}
		}
	}
	for i, err := range f.fillRegistry(ctx, c, batch) {
		if err == nil {
			delete(f.endedPending, batch[i].SessionID)
		}
	}
}

// ResyncEnded makes the next Registrations read the ended Processes again, for
// a caller that knows a Process came and went between two Scans.
func (f *Facts) ResyncEnded() {
	f.scanMu.Lock()
	f.haveSnapshot = false
	f.endedSeeded = false
	f.scanMu.Unlock()
}

func (f *Facts) resetEnded() {
	f.endedSeeded = false
	f.haveSnapshot, f.regCursor = false, ""
	f.live, f.ended = nil, nil
	f.emptyPids, f.emptyTries = nil, nil
	f.goneSince, f.goneExhausted, f.emptyExhausted = nil, nil, nil
}

// wrapScan is wrap for a scan: an outage also forgets the carried state.
func (f *Facts) wrapScan(err error) error {
	if isOffline(err) {
		f.resetEnded()
	}
	return f.wrap(err)
}

func isOffline(err error) bool {
	return errors.Is(err, client.ErrDown) || errors.Is(err, client.ErrUnavailable)
}

func hasID(ss []*session.Session, id string) bool {
	for _, s := range ss {
		if s.SessionID == id {
			return true
		}
	}
	return false
}

func fromProcess(p client.ProcessInfo) *session.Session {
	s := &session.Session{PID: int(p.Pid), SessionID: p.SessionID, Cwd: p.Cwd, Name: p.Name, RegistryStatus: p.Status, PidAlive: p.Alive, ControlInbox: p.ControlInbox, ControlInboxKnown: true}
	if p.StartedAt != nil {
		s.StartedAt = *p.StartedAt
	}
	if p.Host != nil {
		s.Host = &session.Host{Kind: p.Host.Kind, TermProgram: p.Host.TermProgram, CmuxWorkspaceID: p.Host.CmuxWorkspace}
	}
	return s
}

// KnownSessionIDs is what GC keeps: every session id the recorder knows a
// Process for, alive or ended (ended capped at maxDeadProcesses), read fresh on
// every call. It never reads the discovery snapshot, and a failed or degraded
// answer is an error (so the sweep deletes nothing on it).
func (f *Facts) KnownSessionIDs(ctx context.Context) ([]string, error) {
	c, err := f.Client(ctx)
	if err != nil {
		return nil, err
	}
	yes, no := true, false
	alive, err := c.ProcessList(ctx, client.ProcessFilter{Alive: &yes, Limit: 1000})
	if err != nil {
		return nil, f.wrap(err)
	}
	dead, err := c.ProcessList(ctx, client.ProcessFilter{Alive: &no, Limit: maxDeadProcesses})
	if err != nil {
		return nil, f.wrap(err)
	}
	if alive.Degraded || dead.Degraded {
		return nil, errors.New("recorder process list is degraded; not deciding what to keep from a partial answer")
	}
	seen := map[string]bool{}
	var ids []string
	for _, l := range [][]client.ProcessInfo{alive.Processes, dead.Processes} {
		for _, p := range l {
			if p.SessionID != "" && !seen[p.SessionID] {
				seen[p.SessionID] = true
				ids = append(ids, p.SessionID)
			}
		}
	}
	return ids, nil
}

// fillRegistryLive reads the newest registry version of every live session. A
// failed read for one session keeps what that session last had; only a recorder
// that stopped answering fails the scan.
func (f *Facts) fillRegistryLive(ctx context.Context, c *client.Client, live []*session.Session) error {
	prev := map[string]*session.Session{}
	for _, s := range f.live {
		prev[s.SessionID] = s
	}
	errs := f.fillRegistry(ctx, c, live)
	for i, s := range live {
		if errs[i] == nil {
			continue
		}
		if isOffline(errs[i]) {
			return f.wrapScan(errs[i])
		}
		if p := prev[s.SessionID]; p != nil && p.PID == s.PID {
			s.Kind, s.Entrypoint, s.WaitingFor, s.StatusUpdatedAt = p.Kind, p.Entrypoint, p.WaitingFor, p.StatusUpdatedAt
		}
	}
	return nil
}

// fillRegistry runs one limit-1 registry lookup per session and fills the
// fields only the registry bytes carry. It returns each lookup's error.
func (f *Facts) fillRegistry(ctx context.Context, c *client.Client, ss []*session.Session) []error {
	errs := make([]error, len(ss))
	sem := make(chan struct{}, registryLookupWorkers)
	var wg sync.WaitGroup
	for i, s := range ss {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			errs[i] = registryLookup(ctx, c, s)
		}()
	}
	wg.Wait()
	return errs
}

// registryLookup reads the newest registry version of one session, a point
// lookup the (session, source, time) index serves. It skips the registry_deleted
// record an ended session ends with, which has no registry bytes.
func registryLookup(ctx context.Context, c *client.Client, s *session.Session) error {
	id, _ := json.Marshal(s.SessionID)
	ans, err := c.Query(ctx, client.QueryRequest{
		From: "records",
		Where: &client.Filter{All: []client.Filter{
			{Field: "source", Eq: json.RawMessage(`"session-registry"`)},
			// Only a registry version carries bytes: the record written when the
			// registry file is deleted (registry_deleted) is the newest one of an
			// ended session and has none, so without this filter the limit-1
			// lookup lands on it and the session reads back blank.
			{Field: "type", Eq: json.RawMessage(`"registry"`)},
			{Field: "session", Eq: id},
		}},
		Select: []string{"session"},
		Order:  []client.Order{{By: "time", Dir: "desc"}},
		Raw:    true,
		Limit:  1,
	})
	if err != nil {
		return err
	}
	if len(ans.Rows) == 0 || len(ans.Raw) == 0 {
		return nil
	}
	var x registryExtra
	if json.Unmarshal([]byte(ans.Raw[0]), &x) != nil || (x.PID != 0 && int64(s.PID) != x.PID) {
		return nil
	}
	s.Kind, s.Entrypoint, s.WaitingFor = x.Kind, x.Entrypoint, x.WaitingFor
	if x.StatusUpdatedAt != nil && *x.StatusUpdatedAt > 0 {
		s.StatusUpdatedAt = time.UnixMilli(int64(*x.StatusUpdatedAt))
	}
	return nil
}
