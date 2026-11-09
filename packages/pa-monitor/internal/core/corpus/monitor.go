package corpus

import (
	"context"
	"errors"
	"path/filepath"
	"sort"
	"time"

	ct "github.com/phillipgreenii/claude-transcript"

	"github.com/phillipgreenii/pa-monitor/internal/core/limits"
	"github.com/phillipgreenii/pa-monitor/internal/core/session"
	"github.com/phillipgreenii/pa-monitor/internal/core/transcript"
	"github.com/phillipgreenii/pa-monitor/internal/core/usage"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/client"
)

// Monitor is the single owner of the corpus read. It reads Claude's transcripts,
// subagent files and status files from the recorder (the change feed names which
// files changed; the raw bytes of the new lines come from the recorder), never
// from the files themselves. One Scan per tick joins the session registry with
// the transcripts the recorder holds, resolves each session's transcript,
// folds the new lines of every changed file once, and feeds the registered
// observers (the session snapshot, the subagent error, the pricing and the
// limits projections) from that single fold. A recorder that is not answering
// makes Scan return session.ErrDaemonOffline; there is no fallback to files.
type Monitor struct {
	disc session.Discovery
	rec  Recorder
	rd   *reader

	observers  []Observer
	sessionObs *SessionSnapshotObserver
	subErrObs  *SubagentErrorObserver
	pricingObs *UsagePricingObserver
	limitsObs  *LimitsObserver

	topo map[string]sessionTopology

	// perf figures of the last Scan.
	lastQueries int
	lastLines   int
	lastFiles   int
	lastKept    int
}

// New constructs a Monitor that reads files through facts and discovers
// sessions through disc.
func New(facts Facts, disc session.Discovery) *Monitor {
	return &Monitor{
		disc: disc,
		rd:   newReader(facts),
		topo: map[string]sessionTopology{},
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

// pricingWindow is the age of record the Monitor folds transcripts for pricing:
// the current ISO week (so CurrentWeekly sees every this-week record) plus a 12h
// block-anchor safety margin for early-week (a 5h block can span the week
// boundary). Transcripts older than this are not read unless a session needs
// them. The only resulting divergence from the old unbounded pricer is >1 week of
// continuous activity with no >=5h gap, which re-phases the local block estimate
// (documented, estimate-only; the authoritative server-side 5h% flows through
// Limits).
func pricingWindow(now time.Time) time.Duration {
	const floor = 12 * time.Hour
	if sinceMonday := now.Sub(usage.MondayAnchor(now)); sinceMonday > floor {
		return sinceMonday
	}
	return floor
}

// Scan discovers sessions, brings the recorder's changes into the per-file
// folds, resolves each session's transcript, populates the observers and
// topology, and prunes the Monitor's state to what is still wanted. It records
// the "discover" phase around ONLY Discover(). Returns the discovered sessions
// with TranscriptMTime set from resolution (the time of the transcript's last
// record).
func (m *Monitor) Scan(now time.Time) ([]*session.Session, error) {
	if m.pricingObs != nil {
		m.pricingObs.resetErr()
	}

	discoverStart := time.Now()
	sessions, err := m.disc.Discover()
	recordPhase(m.rec, "discover", time.Since(discoverStart))
	if err != nil {
		return nil, err
	}

	ids := make([]string, 0, len(sessions))
	activeIDs := make(map[string]bool, len(sessions))
	for _, s := range sessions {
		ids = append(ids, s.SessionID)
		activeIDs[s.SessionID] = true
	}

	m.rd.queries, m.rd.linesFolded, m.rd.filesFolded = 0, 0, 0
	foldStart := time.Now()
	foldErr, err := m.sync(now, ids)
	if err != nil {
		return nil, err
	}
	recordScan(m.rec, "feed", time.Since(foldStart), int64(m.rd.linesFolded))

	keep := map[string]bool{} // files still wanted (by ref)
	newTopo := make(map[string]sessionTopology, len(sessions))
	cutoff := now.Add(-pricingWindow(now))
	for _, s := range sessions {
		fs := m.resolve(s)
		if fs == nil {
			if m.sessionObs != nil && m.sessionObs.Criteria().matches(Transcript, time.Time{}, true, now) {
				m.sessionObs.set(s.SessionID, transcript.Snapshot{})
			}
			newTopo[s.SessionID] = sessionTopology{}
			continue
		}
		keep[fs.ref] = true
		s.TranscriptMTime = fs.last

		if m.sessionObs != nil && m.sessionObs.Criteria().matches(Transcript, fs.last, true, now) {
			m.sessionObs.set(s.SessionID, fs.snap)
		}
		subErr, maxSub := m.subagents(fs, keep)
		maxAct := laterOf(fs.last, maxSub)
		if m.subErrObs != nil && m.subErrObs.Criteria().matches(Subagent, maxAct, true, now) {
			m.subErrObs.set(s.SessionID, subErr)
		}
		newTopo[s.SessionID] = sessionTopology{resolvedPath: fs.ref, mtime: fs.last, maxActivity: maxAct, ok: true}
	}
	m.topo = newTopo

	// Feed the pricing and limits observers from the same fold, then prune every
	// Monitor-owned map to the files still wanted: a transcript in the pricing
	// window, a session's own files, and every status file.
	for ref, fs := range m.rd.files {
		switch fs.kind {
		case kindStatus:
			keep[ref] = true
		default:
			if fs.last.After(cutoff) || keep[ref] {
				keep[ref] = true
			}
		}
	}
	for ref, fs := range m.rd.files {
		if !keep[ref] {
			delete(m.rd.files, ref)
			continue
		}
		switch fs.kind {
		case kindStatus:
			if m.limitsObs != nil {
				m.limitsObs.setRecords(ref, fs.status)
			}
		default:
			if m.pricingObs != nil {
				m.pricingObs.setRecords(ref, fs.records)
			}
		}
	}
	if m.pricingObs != nil {
		m.pricingObs.noteScanErr(foldErr)
		m.pricingObs.prunePaths(keep)
	}
	if m.limitsObs != nil {
		m.limitsObs.prunePaths(keep)
	}
	for _, o := range m.observers {
		o.Prune(activeIDs)
	}

	m.lastQueries, m.lastLines, m.lastFiles, m.lastKept = m.rd.queries, m.rd.linesFolded, m.rd.filesFolded, len(m.rd.files)
	return sessions, nil
}

// sync brings the per-file folds up to date with the recorder. The error it
// returns is fatal for the Scan (the recorder is not answering); foldErr is the
// first per-file read error, which only degrades pricing.
func (m *Monitor) sync(now time.Time, ids []string) (foldErr, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), readTimeout)
	defer cancel()
	c, err := m.rd.facts.Client(ctx)
	if err != nil {
		return nil, err
	}
	cutoff := now.Add(-pricingWindow(now))
	if m.rd.cursor == "" {
		if err := m.rd.bootstrap(ctx, c, cutoff, ids); err != nil {
			return nil, m.rd.facts.Wrap(err)
		}
	} else if err := m.rd.drain(ctx, c); err != nil {
		if !errors.Is(err, client.ErrCursorExpired) {
			return nil, m.rd.facts.Wrap(err)
		}
		// The cursor fell off the feed's retention: start over from the inventory.
		m.rd.cursor = ""
		if err := m.rd.bootstrap(ctx, c, cutoff, ids); err != nil {
			return nil, m.rd.facts.Wrap(err)
		}
	}
	if err := m.rd.resumeInventory(ctx, c); err != nil {
		return nil, m.rd.facts.Wrap(err)
	}
	if err := m.rd.ensureSessions(ctx, c, ids); err != nil {
		return nil, m.rd.facts.Wrap(err)
	}
	foldErr = m.rd.foldDirty(ctx, c)
	if foldErr != nil && (errors.Is(foldErr, client.ErrDown) || errors.Is(foldErr, client.ErrUnavailable)) {
		return nil, m.rd.facts.Wrap(foldErr)
	}
	return foldErr, nil
}

// resolve picks the transcript file of s: the one whose custom title is the
// session's name, else the file named for the session id, else the newest in
// its project; nil when the recorder holds none.
func (m *Monitor) resolve(s *session.Session) *fileState {
	slug := filepath.Base(filepath.Dir(s.TranscriptPath("")))
	var cands []*fileState
	for _, fs := range m.rd.files {
		if fs.kind == kindTranscript && fs.project == slug {
			cands = append(cands, fs)
		}
	}
	if len(cands) == 0 {
		for _, fs := range m.rd.files {
			if fs.kind == kindTranscript && fs.session == s.SessionID {
				cands = append(cands, fs)
			}
		}
	}
	if len(cands) == 0 {
		return nil
	}
	sort.Slice(cands, func(i, j int) bool {
		if !cands[i].last.Equal(cands[j].last) {
			return cands[i].last.After(cands[j].last)
		}
		return cands[i].ref < cands[j].ref
	})
	if s.Name != "" {
		for _, fs := range cands {
			if fs.title == s.Name {
				return fs
			}
		}
	}
	for _, fs := range cands {
		if fs.session == s.SessionID {
			return fs
		}
	}
	return cands[0]
}

// subagents returns the latest terminal api error among the subagent files of
// main's session (tagged FromSubagent) and the newest record time over them,
// and marks those files as wanted.
func (m *Monitor) subagents(main *fileState, keep map[string]bool) (*transcript.ErrorRecord, time.Time) {
	var best *transcript.ErrorRecord
	var maxLast time.Time
	for ref, fs := range m.rd.files {
		if fs.kind != kindSubagent || fs.session != main.session || fs.project != main.project {
			continue
		}
		keep[ref] = true
		maxLast = laterOf(maxLast, fs.last)
		e := fs.snap.LastError
		if e == nil || e.Kind == "" || !e.IsTerminal {
			continue
		}
		if best == nil || e.At.After(best.At) {
			cp := *e
			best = &cp
		}
	}
	if best == nil {
		return nil, maxLast
	}
	best.IsContextLimit = ct.IsContextLimit(best.Kind, best.Text)
	best.FromSubagent = true
	return best, maxLast
}

// ResolvedPath returns sessionID's resolved transcript (the recorder's file
// reference, an opaque key) and the time of its last record, and whether a
// transcript exists.
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

// Perf-guard figures of the last Scan: the recorder calls it made, the lines
// and files it folded, and the files it holds.
func (m *Monitor) QueriesLastScan() int     { return m.lastQueries }
func (m *Monitor) LinesFoldedLastScan() int { return m.lastLines }
func (m *Monitor) FilesFoldedLastScan() int { return m.lastFiles }
func (m *Monitor) FilesHeldLastScan() int   { return m.lastKept }
