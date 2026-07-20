package corpus

import (
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/session"
	"github.com/phillipgreenii/pa-monitor/internal/core/transcript"
)

// Monitor is the single owner of corpus discovery, transcript resolution, and
// per-file tailing. One Scan per tick joins the session registry with the
// project transcripts, resolves each session's transcript once (write-once title
// cache), and tails the transcript + subagent files at most as much as their
// change warrants — feeding the registered observers and exposing per-session
// topology (resolved path, mtime, maxActivity). Phase 1a runs Scan synchronously
// from Poller.Snapshot; the producer goroutine + DerivedState arrive in phase 3.
type Monitor struct {
	claudeHome string
	disc       *session.Discoverer
	rec        Recorder

	titles *titleCache
	tt     *transcriptTail
	st     *subagentTail

	observers  []Observer
	sessionObs *SessionSnapshotObserver
	subErrObs  *SubagentErrorObserver

	topo map[string]sessionTopology

	// perf deltas from the last Scan (Monitor-initiated work; proxies for opens,
	// which happen inside transcript.ScanIncremental / transcript.LastAPIError).
	lastTitleProbes int
	lastScans       int
	lastReadDirs    int
	lastFileReads   int
}

// New constructs a Monitor over claudeHome using disc for session discovery.
func New(claudeHome string, disc *session.Discoverer) *Monitor {
	return &Monitor{
		claudeHome: claudeHome,
		disc:       disc,
		titles:     newTitleCache(),
		tt:         newTranscriptTail(),
		st:         newSubagentTail(),
		topo:       map[string]sessionTopology{},
	}
}

// Register adds an observer. Call before the first Scan. 1a routes folds to the
// concrete SessionSnapshot / SubagentError observers it recognizes.
func (m *Monitor) Register(o Observer) {
	m.observers = append(m.observers, o)
	switch v := o.(type) {
	case *SessionSnapshotObserver:
		m.sessionObs = v
	case *SubagentErrorObserver:
		m.subErrObs = v
	}
}

// SetPhaseRecorder wires a metrics recorder. Takes any so the daemon can pass an
// *otel.Emitter through an anonymous interface; a value not satisfying Recorder
// (or nil) disables recording.
func (m *Monitor) SetPhaseRecorder(r any) {
	if rec, ok := r.(Recorder); ok {
		m.rec = rec
	}
}

// Scan discovers sessions, resolves + tails each one's transcript and subagent
// files once, populates observers + topology, and prunes all Monitor-owned state
// to the active set. It records the "discover" phase around ONLY Discover()
// (resolve/tail cost is reported via RecordScan, so wrapping the whole Scan in
// "discover" would double-count — S1). Returns the discovered sessions with
// TranscriptMTime set from resolution.
func (m *Monitor) Scan(now time.Time) ([]*session.Session, error) {
	titleBase, scanBase := m.titles.opens, m.tt.scans
	readDirBase, readBase := m.st.readDirs, m.st.reads

	discoverStart := time.Now()
	sessions, err := m.disc.Discover()
	recordPhase(m.rec, "discover", time.Since(discoverStart))
	if err != nil {
		return nil, err
	}

	activeIDs := make(map[string]bool, len(sessions))
	activeDirs := make(map[string]bool, len(sessions))
	newTopo := make(map[string]sessionTopology, len(sessions))

	for _, s := range sessions {
		activeIDs[s.SessionID] = true
		activeDirs[projDir(m.claudeHome, s)] = true

		path, mtime, ok := m.titles.resolve(m.claudeHome, s)
		if ok {
			s.TranscriptMTime = mtime
		}

		snap := m.tt.fold(s.SessionID, path, mtime, m.rec)
		if m.sessionObs != nil && m.sessionObs.Criteria().matches(Transcript, mtime, true, now) {
			m.sessionObs.set(s.SessionID, snap)
		}

		subErr, maxSubMtime := m.st.fold(s.SessionID, path)
		maxAct := laterOf(mtime, maxSubMtime)
		if m.subErrObs != nil && m.subErrObs.Criteria().matches(Subagent, maxAct, true, now) {
			m.subErrObs.set(s.SessionID, subErr)
		}

		newTopo[s.SessionID] = sessionTopology{resolvedPath: path, mtime: mtime, maxActivity: maxAct, ok: ok}
	}
	m.topo = newTopo

	// Prune ALL Monitor-owned state to the active set every Scan — else these
	// maps grow for the daemon's lifetime, the exact defect this epic fixes (S2).
	m.tt.prune(activeIDs)
	m.st.prune(activeIDs)
	m.titles.prune(activeDirs)
	for _, o := range m.observers {
		o.Prune(activeIDs)
	}

	m.lastTitleProbes = m.titles.opens - titleBase
	m.lastScans = m.tt.scans - scanBase
	m.lastReadDirs = m.st.readDirs - readDirBase
	m.lastFileReads = m.st.reads - readBase

	return sessions, nil
}

// ResolvedPath returns sessionID's resolved transcript path + mtime, and whether
// a transcript exists (ok=false when the project dir is missing/empty).
func (m *Monitor) ResolvedPath(sessionID string) (string, time.Time, bool) {
	t, ok := m.topo[sessionID]
	if !ok {
		return "", time.Time{}, false
	}
	return t.resolvedPath, t.mtime, t.ok
}

// MaxActivity returns max(transcript mtime, newest subagent agent-*.jsonl mtime)
// for sessionID (zero when unknown).
func (m *Monitor) MaxActivity(sessionID string) time.Time {
	return m.topo[sessionID].maxActivity
}

// SessionSnapshot returns sessionID's folded transcript Snapshot.
func (m *Monitor) SessionSnapshot(sessionID string) (transcript.Snapshot, bool) {
	if m.sessionObs == nil {
		return transcript.Snapshot{}, false
	}
	return m.sessionObs.Snapshot(sessionID)
}

// SubagentError returns sessionID's latest terminal subagent error (nil, false
// when none).
func (m *Monitor) SubagentError(sessionID string) (*transcript.ErrorRecord, bool) {
	if m.subErrObs == nil {
		return nil, false
	}
	return m.subErrObs.LastTerminal(sessionID)
}

// Perf-guard proxies for the last Scan (see the field doc for why these are
// Monitor-initiated counts rather than raw os.Open counts).
func (m *Monitor) TitleProbesLastScan() int       { return m.lastTitleProbes }
func (m *Monitor) TranscriptScansLastScan() int   { return m.lastScans }
func (m *Monitor) SubagentReadDirsLastScan() int  { return m.lastReadDirs }
func (m *Monitor) SubagentFileReadsLastScan() int { return m.lastFileReads }
