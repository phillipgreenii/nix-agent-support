package corpus

import (
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/limits"
	"github.com/phillipgreenii/pa-monitor/internal/core/session"
	"github.com/phillipgreenii/pa-monitor/internal/core/transcript"
	"github.com/phillipgreenii/pa-monitor/internal/core/usage"
)

// Monitor is the single owner of corpus discovery, transcript resolution, and
// per-file tailing. One Scan per tick joins the session registry with the project
// transcripts, resolves each session's transcript once (write-once title cache),
// tails the transcript + subagent files each active session needs, AND walks the
// in-window transcript + status-sibling supersets so the UsagePricing and Limits
// observers are fed from the SAME single decode — feeding the registered observers
// and exposing per-session topology (resolved path, mtime, maxActivity) plus the
// pricing/limits projections. Phase 1 runs Scan synchronously from Poller.Snapshot;
// the producer goroutine + DerivedState arrive in phase 3.
type Monitor struct {
	claudeHome string
	disc       *session.Discoverer
	rec        Recorder

	titles *titleCache
	tt     *transcriptTail
	st     *subagentTail
	stat   *statusTail

	observers  []Observer
	sessionObs *SessionSnapshotObserver
	subErrObs  *SubagentErrorObserver
	pricingObs *UsagePricingObserver
	limitsObs  *LimitsObserver

	topo map[string]sessionTopology

	// perf deltas from the last Scan (Monitor-initiated work; proxies for opens,
	// which happen inside transcript.ScanIncremental / transcript.LastAPIError /
	// limits.ReadStatusRecords).
	lastTitleProbes  int
	lastScans        int
	lastReadDirs     int
	lastFileReads    int
	lastStatusReads  int
	lastPricingFiles int
}

// New constructs a Monitor over claudeHome using disc for session discovery.
func New(claudeHome string, disc *session.Discoverer) *Monitor {
	return &Monitor{
		claudeHome: claudeHome,
		disc:       disc,
		titles:     newTitleCache(),
		tt:         newTranscriptTail(),
		st:         newSubagentTail(),
		stat:       newStatusTail(),
		topo:       map[string]sessionTopology{},
	}
}

// Register adds an observer. Call before the first Scan. The Monitor routes folds
// to the concrete observers it recognizes.
func (m *Monitor) Register(o Observer) {
	m.observers = append(m.observers, o)
	switch v := o.(type) {
	case *SessionSnapshotObserver:
		m.sessionObs = v
	case *SubagentErrorObserver:
		m.subErrObs = v
	case *UsagePricingObserver:
		m.pricingObs = v
	case *LimitsObserver:
		m.limitsObs = v
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

// pricingWindow is the mtime window the Monitor opens transcript files within for
// pricing: the current ISO week (so CurrentWeekly sees every this-week record)
// plus a 12h block-anchor safety margin for early-week (a 5h block can span the
// week boundary). Files older than this are never opened (design §2/§8). The only
// resulting divergence from the old unbounded pricer is >1 week of continuous
// activity with no >=5h gap, which re-phases the local block estimate (documented,
// estimate-only — the authoritative server-side 5h% flows through Limits).
func pricingWindow(now time.Time) time.Duration {
	const floor = 12 * time.Hour
	if sinceMonday := now.Sub(usage.MondayAnchor(now)); sinceMonday > floor {
		return sinceMonday
	}
	return floor
}

// Scan discovers sessions, resolves + tails each one's transcript and subagent
// files once, walks the in-window transcript + status-sibling supersets for the
// pricing/limits observers, populates observers + topology, and prunes all
// Monitor-owned state to the active set. It records the "discover" phase around
// ONLY Discover() (resolve/tail cost is reported via RecordScan, so wrapping the
// whole Scan in "discover" would double-count — S1). Returns the discovered
// sessions with TranscriptMTime set from resolution.
func (m *Monitor) Scan(now time.Time) ([]*session.Session, error) {
	titleBase, scanBase := m.titles.opens, m.tt.scans
	readDirBase, readBase := m.st.readDirs, m.st.reads
	statusReadBase := m.stat.reads

	if m.pricingObs != nil {
		m.pricingObs.resetErr()
	}

	discoverStart := time.Now()
	sessions, err := m.disc.Discover()
	recordPhase(m.rec, "discover", time.Since(discoverStart))
	if err != nil {
		return nil, err
	}

	activeIDs := make(map[string]bool, len(sessions))
	activeDirs := make(map[string]bool, len(sessions))
	activePaths := make(map[string]bool)       // transcript paths (sessions ∪ in-window walk)
	activeStatusPaths := make(map[string]bool) // status-sibling paths
	foldedPaths := make(map[string]bool)       // transcript paths already folded in the session loop
	newTopo := make(map[string]sessionTopology, len(sessions))

	for _, s := range sessions {
		activeIDs[s.SessionID] = true
		activeDirs[projDir(m.claudeHome, s)] = true

		path, mtime, ok := m.titles.resolve(m.claudeHome, s)
		if ok {
			s.TranscriptMTime = mtime
		}

		snap, records, ferr := m.tt.fold(path, mtime, m.rec)
		if m.sessionObs != nil && m.sessionObs.Criteria().matches(Transcript, mtime, true, now) {
			m.sessionObs.set(s.SessionID, snap)
		}
		if path != "" {
			activePaths[path] = true
			foldedPaths[path] = true
			// Route the active session's transcript records to pricing from the
			// SAME fold — the file is opened once for both projections.
			if m.pricingObs != nil {
				m.pricingObs.setRecords(path, records)
				m.pricingObs.noteScanErr(ferr)
			}
		}

		subErr, maxSubMtime := m.st.fold(s.SessionID, path)
		maxAct := laterOf(mtime, maxSubMtime)
		if m.subErrObs != nil && m.subErrObs.Criteria().matches(Subagent, maxAct, true, now) {
			m.subErrObs.set(s.SessionID, subErr)
		}

		newTopo[s.SessionID] = sessionTopology{resolvedPath: path, mtime: mtime, maxActivity: maxAct, ok: ok}
	}
	m.topo = newTopo

	// Corpus walk: fold in-window transcripts NOT already folded (pricing
	// superset) + status siblings (limits), each read at most once. Skipped
	// entirely when neither observer is registered (phase-1a Monitors).
	if m.pricingObs != nil || m.limitsObs != nil {
		files, werr := walkCorpus(m.claudeHome, pricingWindow(now), now)
		if werr != nil && m.pricingObs != nil {
			m.pricingObs.noteScanErr(werr)
		}
		for _, wf := range files {
			switch wf.class {
			case Transcript:
				if m.pricingObs == nil || foldedPaths[wf.path] {
					continue // no pricing consumer, or already folded (session loop routed it)
				}
				activePaths[wf.path] = true
				_, records, ferr := m.tt.fold(wf.path, wf.mtime, m.rec)
				m.pricingObs.setRecords(wf.path, records)
				m.pricingObs.noteScanErr(ferr)
			case StatusSibling:
				if m.limitsObs == nil {
					continue
				}
				activeStatusPaths[wf.path] = true
				m.limitsObs.setRecords(wf.path, m.stat.foldFile(wf.path, wf.size, wf.mtime))
			}
		}
	}

	// Prune ALL Monitor-owned state to the active set every Scan — else these maps
	// grow for the daemon's lifetime, the exact defect this epic fixes (S2).
	m.tt.prune(activePaths)
	m.st.prune(activeIDs)
	m.stat.prune(activeStatusPaths)
	m.titles.prune(activeDirs)
	if m.pricingObs != nil {
		m.pricingObs.prunePaths(activePaths)
	}
	if m.limitsObs != nil {
		m.limitsObs.prunePaths(activeStatusPaths)
	}
	for _, o := range m.observers {
		o.Prune(activeIDs)
	}

	m.lastTitleProbes = m.titles.opens - titleBase
	m.lastScans = m.tt.scans - scanBase
	m.lastReadDirs = m.st.readDirs - readDirBase
	m.lastFileReads = m.st.reads - readBase
	m.lastStatusReads = m.stat.reads - statusReadBase
	m.lastPricingFiles = len(activePaths)

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

// Block returns the current 5h block priced from the pricing observer's records
// at now (nil when no observer or no active block).
func (m *Monitor) Block(now time.Time) *usage.Block {
	if m.pricingObs == nil {
		return nil
	}
	return m.pricingObs.Block(now)
}

// Weekly returns the current Monday-anchored week's cost at now (nil when no
// observer or no records this week).
func (m *Monitor) Weekly(now time.Time) *usage.WeeklyEntry {
	if m.pricingObs == nil {
		return nil
	}
	return m.pricingObs.Weekly(now)
}

// CostProbed reports whether pricing has run and the first scan error, if any
// (parity with NativePricer.Probed -> tree.CostProbed / CostProbeErr).
func (m *Monitor) CostProbed() (bool, error) {
	if m.pricingObs == nil {
		return false, nil
	}
	return m.pricingObs.Probed()
}

// Limits returns the account-global current-window rate_limits reading from the
// limits observer (nil when no observer or no records).
func (m *Monitor) Limits() *limits.Limits {
	if m.limitsObs == nil {
		return nil
	}
	return m.limitsObs.Current()
}

// Perf-guard proxies for the last Scan (see the field doc for why these are
// Monitor-initiated counts rather than raw os.Open counts).
func (m *Monitor) TitleProbesLastScan() int       { return m.lastTitleProbes }
func (m *Monitor) TranscriptScansLastScan() int   { return m.lastScans }
func (m *Monitor) SubagentReadDirsLastScan() int  { return m.lastReadDirs }
func (m *Monitor) SubagentFileReadsLastScan() int { return m.lastFileReads }
func (m *Monitor) StatusReadsLastScan() int       { return m.lastStatusReads }
func (m *Monitor) PricingFilesLastScan() int      { return m.lastPricingFiles }
