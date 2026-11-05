// Package settle is the recorder's settle stage: it moves a Conversation whose
// Processes are gone and whose files have been quiet for settle.after from
// "quiet" to "settled" (or "held" when the check fails). A Conversation settles
// only when the check passes for every one of its files; in clone mode settling
// re-clones each file from the Claude original, which re-shares the blocks the
// copy diverged on, and in byte mode it compresses the copies into the
// Conversation's archive, in segments, and then deletes the bytes the archive
// covers. Settling never writes into or removes anything from the Claude
// directory.
//
// The order that makes a crash at any point lose nothing:
//
//  1. write the archive segment (or the clone) to a temporary file, flush it,
//     read it back, flush the directory, rename it into place;
//  2. commit "settled" with the archive's ranges (journal and database, with a
//     full flush);
//  3. delete the uncompressed copy bytes the archive covers.
//
// A restart (Recover) removes a segment that was renamed but never committed
// and finishes a delete the crash cut short.
package settle

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cespare/xxhash/v2"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/clock"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/copier"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/faults"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/fsx"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/scope"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
)

// The stages as the settle stage names them (the lifecycle package carries the
// same words for conversation.show).
const (
	StageLive    = "live"
	StageQuiet   = "quiet"
	StageSettled = store.StageSettled
	StageHeld    = store.StageHeld
)

// Held reason codes: the enumerated list held_conversations{reason} and the
// held command expose. Each check clause is its own reason.
const (
	// ReasonCopyOffset: the copy offset differs from the size of the file in the
	// Claude directory.
	ReasonCopyOffset = "copy_offset_mismatch"
	// ReasonCopyTail: the tail hash of the copy differs from the original's.
	ReasonCopyTail = "copy_tail_mismatch"
	// ReasonParseBehind: the parse offset is not at the offset after the last
	// newline of the copy.
	ReasonParseBehind = "parse_behind"
	// ReasonUnparsed: a file of a parsed kind has never been parsed.
	ReasonUnparsed = "file_unparsed"
	// ReasonTornTooLong: the torn tail is at least parse.maxLineBytes long.
	ReasonTornTooLong = "torn_tail_too_long"
	// ReasonRestoreConflict: removal could not put a quarantined file back because
	// a new file appeared at its path; both are kept.
	ReasonRestoreConflict = "restore_conflict"
)

// Reasons lists every held reason code.
var Reasons = []string{ReasonCopyOffset, ReasonCopyTail, ReasonParseBehind, ReasonUnparsed, ReasonTornTooLong, ReasonRestoreConflict}

// Failure is one failing clause for one file.
type Failure struct {
	File   int64  `json:"file"`
	Path   string `json:"path"`
	Reason string `json:"reason"`
	Detail string `json:"detail,omitempty"`
}

// Parser is the part of the parse stage the settle check needs.
type Parser interface {
	HasSource(name string) bool
	// ParseTail parses a torn tail as a complete final line when it is valid JSON.
	ParseTail(file int64, gen int) (bool, error)
	// Drain parses every pending file.
	Drain() error
}

// FreeSpace is the free-space seam (low-space mode reads it).
type FreeSpace interface {
	// Free returns the bytes available to the recorder on the volume holding path.
	Free(path string) (int64, error)
}

// FreeFunc adapts a function to FreeSpace.
type FreeFunc func(path string) (int64, error)

// Free calls f.
func (f FreeFunc) Free(path string) (int64, error) { return f(path) }

// Hooks are the pause seams tests use: a write that lands between the check and
// the clone, or between the check and the delete.
type Hooks struct {
	BeforeClone  func(file int64)
	BeforeDelete func(session string)
	// BeforeQuarantine runs after the pre-removal check and before a file's
	// quarantine rename; AfterQuarantine right after the rename (a write through
	// a descriptor opened earlier lands here); BeforeUnlink after the re-hash and
	// before the final size and modification-time check.
	BeforeQuarantine func(session, path string)
	AfterQuarantine  func(session, path string)
	BeforeUnlink     func(session, path string)
	// InFormatScan runs at the start of each per-source format-hold scan (a test
	// makes the scan slow here); AfterFormatPrescan runs in Remove after the
	// early scan and before the run lock is requested.
	InFormatScan       func(source string)
	AfterFormatPrescan func()
}

// Config configures a Settler.
type Config struct {
	Store  *store.Store
	Copier *copier.Copier
	Parser Parser
	// Live returns the sessions a live Process holds now.
	Live  func() (map[string]bool, error)
	Clock clock.Clock
	Obs   *obs.Obs
	// ClaudeDir is the Claude directory (file paths in manifests are relative to it).
	ClaudeDir string
	// After is settle.after, Recheck held.recheck, HeldAlertAfter held.alertAfter.
	After, Recheck, HeldAlertAfter time.Duration
	MinFreeBytes                   int64
	MaxLineBytes                   int
	Free                           FreeSpace
	// Mtime reads a file's modification time (os.Lstat when nil).
	Mtime func(path string) (time.Time, error)
	Hooks Hooks

	// RemoveEnable is remove.enable; RemoveAfter remove.after (30 days);
	// MaxPerRun and MaxPerHour are remove.maxPerRun (50) and remove.maxPerHour
	// (200); HoldRate is format.holdRate (0.01).
	RemoveEnable bool
	RemoveAfter  time.Duration
	MaxPerRun    int
	MaxPerHour   int
	HoldRate     float64
	// Holders lists the processes that hold path open (the real one asks lsof).
	Holders func(path string) ([]int, error)
	// FS is the file system removal and extract act on in the Claude directory
	// (the store's when nil), so the recording wrapper sees the order.
	FS fsx.FS
	// Timer, when set, runs the removal pass of every Tick through it (the
	// removal_pass loop; its time is nested inside the tick's). It returns
	// step's error. The operator remove operation does not use it.
	Timer func(step func() error) error

	// Mode selects the settle loop: ModeScan (the default; the whole-store Tick
	// every minute) or ModeEvent (Wake on dirty and due Conversations, Tick as the
	// reconcile). Reconcile is how often the event loop runs a full Tick (default
	// 1 h); MinWake is the shortest time between two wakes (default 5 s). The
	// daemon reads all three; the settler uses Mode to decide whether a Tick
	// rebuilds the due-time state.
	Mode      string
	Reconcile time.Duration
	MinWake   time.Duration

	// The rolling doctor (see doctor_rolling.go): DoctorInterval is
	// doctor.interval, DoctorBytesPerSlice doctor.bytes_per_slice (0 selects the
	// old synchronous run), DoctorSliceEvery doctor.slice_every, and
	// DoctorLagBound the copy_lag_bytes above which a slice is deferred (1 GiB
	// when 0).
	DoctorInterval      time.Duration
	DoctorBytesPerSlice int64
	DoctorSliceEvery    time.Duration
	DoctorLagBound      int64
}

// Settler runs the settle stage.
type Settler struct {
	cfg Config
	st  *store.Store
	fs  fsx.FS

	run sync.Mutex // one pass at a time

	// doctorMu is the single flight of the doctor cursor: a rolling slice and a
	// CLI doctor run never overlap.
	doctorMu     sync.Mutex
	doctorIdle   func() error
	doctorSeamMu sync.Mutex
	doctorSeam   doctorSeam

	// The incrementally kept file-to-Conversation map (see groups); guarded by run.
	trk    *store.FileTracker
	gcache map[string]*conv
	owner  map[int64]string // file id to the session it is placed in

	// df is the dirty-set feed (see dirty.go); it has its own lock.
	df       *dirtyFeed
	feedOnce sync.Once

	// seen holds, during Tick's evaluate phase, the newest recorded change
	// time of every file about to be evaluated (see prefetchLastSeen); nil
	// otherwise. Guarded by run.
	seen map[int64]int64

	// The event loop's state (see wake.go); guarded by run.
	due      *DueHeap
	liveSeen map[string]bool
	lowSeen  bool
	seeded   bool

	mu        sync.Mutex
	nextAt    time.Time // earliest due deadline after the last pass; guarded by mu
	nextOK    bool
	phases    TickPhases // the last Tick's phase durations
	removedAt []time.Time
	last      map[string]string
	reasons   map[string]string
	checkedAt map[string]time.Time
	low       bool

	// pubCounts and pubAt are the stage counts the last refresh derived and when
	// (see LastCounts); guarded by mu.
	pubCounts map[string]int
	pubAt     time.Time

	// gauges are the incrementally kept stage counts and gauge inputs (see
	// counts.go); guarded by mu.
	gauges *gaugeState

	// The quarantine directory list the gauge refresh reuses between ticks.
	qmu            sync.Mutex
	qdirs          []string
	qdirsValid     bool
	qdirsAge       int
	walkQuarantine func() []string // test seam; nil walks projects/
}

// New returns a Settler.
func New(cfg Config) *Settler {
	if cfg.Clock == nil {
		cfg.Clock = clock.Real{}
	}
	if cfg.Obs == nil {
		cfg.Obs = obs.New(nil)
	}
	if cfg.After <= 0 {
		cfg.After = 24 * time.Hour
	}
	if cfg.Recheck <= 0 {
		cfg.Recheck = time.Hour
	}
	if cfg.MaxLineBytes <= 0 {
		cfg.MaxLineBytes = 64 << 20
	}
	if cfg.Mtime == nil {
		cfg.Mtime = func(p string) (time.Time, error) {
			fi, err := os.Lstat(p)
			if err != nil {
				return time.Time{}, err
			}
			return fi.ModTime(), nil
		}
	}
	if cfg.Mode == "" {
		cfg.Mode = ModeScan
	}
	if cfg.Reconcile <= 0 {
		cfg.Reconcile = time.Hour
	}
	if cfg.MinWake <= 0 {
		cfg.MinWake = 5 * time.Second
	}
	if cfg.RemoveAfter <= 0 {
		cfg.RemoveAfter = 30 * 24 * time.Hour
	}
	if cfg.MaxPerRun <= 0 {
		cfg.MaxPerRun = 50
	}
	if cfg.MaxPerHour <= 0 {
		cfg.MaxPerHour = 200
	}
	if cfg.HoldRate <= 0 {
		cfg.HoldRate = 0.01
	}
	if cfg.Holders == nil {
		cfg.Holders = LsofHolders
	}
	s := &Settler{cfg: cfg, st: cfg.Store, fs: cfg.Store.FS(), last: map[string]string{}, reasons: map[string]string{}, checkedAt: map[string]time.Time{}, gauges: newGaugeState()}
	if cfg.Copier != nil {
		// Every file-state change the copier commits (a backfill batch, a
		// tombstone, a new generation, the settler's and doctor's own) feeds the
		// dirty set.
		cfg.Copier.SetOnFileChange(s.Dirty)
	}
	if rows, err := s.st.Stages(); err == nil {
		for _, r := range rows {
			s.last[r.Session] = r.Stage
			if r.Stage == store.StageRemoved && cfg.Clock.Now().Sub(time.Unix(0, r.SinceNS)) < time.Hour {
				s.removedAt = append(s.removedAt, time.Unix(0, r.SinceNS))
			}
		}
	}
	return s
}

// LowSpace reports whether the last pass found the state directory below minFreeBytes.
func (s *Settler) LowSpace() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.low
}

var uuidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// SessionOf returns the Conversation a path relative to the Claude directory
// belongs to, and false for the files that belong to no single Conversation
// (history.jsonl, project-level files, memory/, the registry).
func SessionOf(rel string) (string, bool) {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) < 3 || parts[0] != "projects" {
		return "", false
	}
	cand := parts[2]
	if len(parts) == 3 {
		for _, suf := range []string{".status.jsonl", ".session-mode.json", ".compaction.json", ".jsonl"} {
			if strings.HasSuffix(cand, suf) {
				cand = strings.TrimSuffix(cand, suf)
				break
			}
		}
	}
	if !uuidRe.MatchString(cand) {
		return "", false
	}
	return cand, true
}

type conv struct {
	session string
	files   []store.File
	rel     map[int64]string
}

// place returns the Conversation a file belongs to and its path relative to the
// Claude directory: the first of the file's paths that lies under root and
// names a Conversation. ok is false for a file that belongs to none.
func (s *Settler) place(root string, f store.File, paths []string) (session, rel string, ok bool) {
	if f.Mode != "append" && f.Mode != "snapshot" {
		return "", "", false
	}
	for _, p := range paths {
		if !strings.HasPrefix(p, root) {
			continue
		}
		rel := p[len(root):]
		// A recorded file the ingest allowlist now rejects (for example a
		// scratchpad file an older version recorded) is stale: it is never
		// re-ingested, so verifying or removing it would hold the Conversation
		// forever. It belongs to no Conversation here and is left in place.
		if !s.scope().Match(p).Ingest {
			continue
		}
		if session, ok := SessionOf(rel); ok {
			return session, rel, true
		}
	}
	return "", "", false
}

func (s *Settler) scope() *scope.Scope { return scope.New(s.cfg.ClaudeDir, "") }

func (s *Settler) claudeRoot() string { return strings.TrimRight(s.cfg.ClaudeDir, "/") + "/" }

// groupsFull builds the file-to-Conversation map from every file and path. It
// is the definition of the map; groups keeps a copy of it up to date.
func (s *Settler) groupsFull() (map[string]*conv, map[int64]string, error) {
	files, err := s.st.AllFiles()
	if err != nil {
		return nil, nil, err
	}
	paths, err := s.st.AllPaths()
	if err != nil {
		return nil, nil, err
	}
	root := s.claudeRoot()
	out := map[string]*conv{}
	owner := map[int64]string{}
	for _, f := range files {
		session, rel, ok := s.place(root, f, paths[f.ID])
		if !ok {
			continue
		}
		c := out[session]
		if c == nil {
			c = &conv{session: session, rel: map[int64]string{}}
			out[session] = c
		}
		c.files = append(c.files, f)
		c.rel[f.ID] = rel
		owner[f.ID] = session
	}
	return out, owner, nil
}

// groups returns the file-to-Conversation map. The caller holds the run lock.
//
// The map is kept between calls and only the files the store's FileTracker
// reports as changed (a journal record named them since the last call) are
// reread and re-placed. The first call, and any call after the tracker reports
// full (a write was rolled back, so a reported change may have been undone), or
// after a failed incremental read, rebuilds it whole with groupsFull. Each
// touched Conversation gets a fresh conv, so one returned earlier is never
// mutated; the returned map is a copy the caller may keep.
func (s *Settler) groups() (map[string]*conv, error) {
	if err := s.syncGroups(); err != nil {
		return nil, err
	}
	out := make(map[string]*conv, len(s.gcache))
	for k, v := range s.gcache {
		out[k] = v
	}
	return out, nil
}

// syncGroups brings the kept map up to date without copying it; the event loop
// reads s.gcache directly so a wake costs nothing per Conversation it skips.
func (s *Settler) syncGroups() error {
	if s.trk == nil {
		s.trk = s.st.TrackFiles()
	}
	ids, full := s.trk.Take()
	if full || s.gcache == nil {
		c, owner, err := s.groupsFull()
		if err != nil {
			s.gcache, s.owner = nil, nil
			s.syncFileSession(nil, true)
			return err
		}
		s.gcache, s.owner = c, owner
		s.syncFileSession(nil, true)
	} else if len(ids) > 0 {
		if err := s.applyChanged(ids); err != nil {
			s.gcache, s.owner = nil, nil
			s.syncFileSession(nil, true)
			return err
		}
		s.syncFileSession(ids, false)
	}
	return nil
}

// applyChanged rereads the given files and their paths and moves each to the
// Conversation it now belongs to (or out of the map).
func (s *Settler) applyChanged(ids []int64) error {
	var files []store.File
	var paths map[int64][]string
	if err := s.st.ReadTx(func(rd *store.Reader) (err error) {
		if files, err = rd.FilesByIDs(ids); err != nil {
			return err
		}
		paths, err = rd.PathsByFiles(ids)
		return err
	}); err != nil {
		return err
	}
	root := s.claudeRoot()
	dirty := make(map[int64]bool, len(ids))
	touched := map[string]bool{}
	for _, id := range ids {
		dirty[id] = true
		if old, ok := s.owner[id]; ok {
			touched[old] = true
			delete(s.owner, id)
		}
	}
	type placed struct {
		f   store.File
		rel string
	}
	arrivals := map[string][]placed{}
	for _, f := range files {
		session, rel, ok := s.place(root, f, paths[f.ID])
		if !ok {
			continue
		}
		touched[session] = true
		s.owner[f.ID] = session
		arrivals[session] = append(arrivals[session], placed{f, rel})
	}
	for session := range touched {
		var nc *conv
		if old := s.gcache[session]; old != nil {
			nc = &conv{session: session, rel: map[int64]string{}}
			for _, f := range old.files {
				if !dirty[f.ID] {
					nc.files = append(nc.files, f)
					nc.rel[f.ID] = old.rel[f.ID]
				}
			}
		}
		for _, a := range arrivals[session] {
			if nc == nil {
				nc = &conv{session: session, rel: map[int64]string{}}
			}
			nc.files = append(nc.files, a.f)
			nc.rel[a.f.ID] = a.rel
		}
		if nc == nil || len(nc.files) == 0 {
			delete(s.gcache, session)
			continue
		}
		sort.Slice(nc.files, func(i, j int) bool { return nc.files[i].ID < nc.files[j].ID })
		s.gcache[session] = nc
	}
	return nil
}

// slowRunLockWait is the wait for the run lock above which a warning is logged.
const slowRunLockWait = 5 * time.Second

// lockRun takes the run lock for op and returns its unlock. The time spent
// waiting is counted (settle_run_lock_wait_total, settle_run_lock_wait_seconds_total,
// label op) and a wait of slowRunLockWait or more is logged, so a long holder that
// starves the tick shows up as the waiter's wait, not only as a missing tick.
func (s *Settler) lockRun(op string) func() {
	t0 := time.Now()
	s.run.Lock()
	w := time.Since(t0)
	lbl := map[string]string{"op": op}
	s.cfg.Obs.Reg.Add(obs.MSettleRunLockWaits, lbl, 1)
	s.cfg.Obs.Reg.Add(obs.MSettleRunLockWaitSeconds, lbl, w.Seconds())
	if w >= slowRunLockWait {
		s.cfg.Obs.Log.Warn("settle_run_lock_wait_slow", "op", op, "wait_ms", w.Milliseconds())
	}
	return s.run.Unlock
}

// Outcome is what one evaluation of a Conversation did.
type Outcome struct {
	Session  string
	Stage    string // the stage after the evaluation (quiet, held or settled)
	Reason   string
	Failures []Failure
	Skipped  string // why nothing was evaluated, when nothing was
}

// TickStats is what one Tick did, for the loop's metrics and log line.
type TickStats struct {
	Examined    int // conversations evaluated or skipped (every group)
	Unchanged   int // already settled and unchanged
	SettledNow  int // settled by this tick
	Transitions int // stage changes made by this tick
}

// TickPhases is how long each part of the last Tick took, for the slow-tick log
// line: Reads is the live-process and file-group reads, Prefilter the one
// transaction that skips settled Conversations, Evaluate the per-Conversation
// loop (its reads and writes), Removal the removal pass and Refresh the final
// stage and gauge refresh.
type TickPhases struct {
	Reads, Prefilter, Evaluate, Removal, Refresh time.Duration
}

// LastPhases returns the phase durations of the most recent Tick (zero before
// the first).
func (s *Settler) LastPhases() TickPhases {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.phases
}

// Tick runs one pass: it evaluates every Conversation that is not live, moves
// the ones whose Process came back to Live, and refreshes the gauges.
func (s *Settler) Tick() (TickStats, error) {
	defer s.lockRun("tick")()
	return s.tickLocked()
}

// tickLocked is the Tick body, run with the run lock held.
func (s *Settler) tickLocked() (TickStats, error) {
	var stats TickStats
	var ph TickPhases
	defer func() {
		s.mu.Lock()
		s.phases = ph
		s.mu.Unlock()
	}()
	now := s.cfg.Clock.Now()
	t0 := now
	lap := func(into *time.Duration) {
		n := s.cfg.Clock.Now()
		*into = n.Sub(t0)
		t0 = n
	}
	s.checkLow()
	live, err := s.cfg.Live()
	if err != nil {
		return stats, err
	}
	groups, err := s.groups()
	if err != nil {
		return stats, err
	}
	stats.Examined = len(groups)
	lap(&ph.Reads)
	before, unchanged := s.settledPrefilter(groups, live)
	lap(&ph.Prefilter)
	sessions := make([]string, 0, len(groups))
	for k := range groups {
		sessions = append(sessions, k)
	}
	sort.Strings(sessions)
	var first error
	s.prefetchLastSeen(groups, live, unchanged)
	for _, session := range sessions {
		if unchanged[session] {
			// Already settled, no live process, nothing written since: what
			// evaluate would answer, without its per-Conversation reads.
			stats.add(Outcome{Session: session, Stage: StageSettled, Skipped: "settled"}, before[session])
			continue
		}
		out, err := s.evaluate(groups[session], live[session], now, false)
		if err != nil && first == nil {
			first = fmt.Errorf("settle %s: %w", session, err)
		}
		stats.add(out, before[session])
	}
	s.seen = nil
	lap(&ph.Evaluate)
	if s.cfg.RemoveEnable {
		removal := func() error {
			_, err := s.removeLocked(RemoveOptions{}, now, nil)
			return err
		}
		if s.cfg.Timer != nil {
			err = s.cfg.Timer(removal)
		} else {
			err = removal()
		}
		if err != nil && first == nil {
			first = fmt.Errorf("removal pass: %w", err)
		}
		if groups, err = s.groups(); err == nil {
			live, _ = s.cfg.Live()
		}
	}
	// A persisted stage whose Conversation has no files any more is dropped from view.
	lap(&ph.Removal)
	s.refresh(groups, live, now)
	if s.cfg.Mode == ModeEvent {
		s.seedLocked(groups, live, now)
		s.cfg.Obs.Reg.Add(obs.MSettleWakes, map[string]string{"reason": "reconcile"}, 1)
	}
	lap(&ph.Refresh)
	return stats, first
}

// settledPrefilter reads the persisted stages and, for every settled
// Conversation without a live process, whether it changed since its settle, all
// inside ONE read transaction (one lock round trip and one snapshot, instead of
// a transaction per statement for each of thousands of Conversations). The
// unchanged set is exactly the Conversations evaluate would skip as "settled";
// every other Conversation still goes through evaluate, so the stage machine is
// untouched. When the transaction cannot complete the unchanged set is empty
// and the pass falls back to evaluating every Conversation.
func (s *Settler) settledPrefilter(groups map[string]*conv, live map[string]bool) (before map[string]string, unchanged map[string]bool) {
	before, unchanged = map[string]string{}, map[string]bool{}
	err := s.st.ReadTx(func(rd *store.Reader) error {
		rows, err := rd.Stages()
		if err != nil {
			return err
		}
		for _, r := range rows {
			before[r.Session] = r.Stage
		}
		// Two bulk reads replace a settled-row read per Conversation and a
		// generation read per file (thousands of statements, each taking the
		// shared-memory lock, which kept the tick near the read deadline).
		settled, err := rd.AllSettled()
		if err != nil {
			return err
		}
		offsets, err := rd.CopyOffsets()
		if err != nil {
			return err
		}
		byGen := func(f store.File) (int64, bool) { return offsets[[2]int64{f.ID, int64(f.CurGen)}], true }
		for session, c := range groups {
			if live[session] || before[session] != StageSettled {
				continue
			}
			st, ok := settled[session]
			if !ok || !changedAgainst(st, c, byGen) {
				unchanged[session] = true
			}
		}
		return nil
	})
	if err != nil {
		unchanged = map[string]bool{}
		before = map[string]string{}
		if rows, err := s.st.Stages(); err == nil {
			for _, r := range rows {
				before[r.Session] = r.Stage
			}
		}
	}
	return before, unchanged
}

// prefetchLastSeen loads, in one statement, the last-seen time of every file
// of the Conversations the evaluate loop will examine (not live, not skipped by
// the prefilter), so lastSeen costs no statement per file. Files with no
// commit, snapshot or tombstone are recorded as zero, which lastSeen reads as
// "FirstSeen". On a read error the cache stays empty and lastSeen reads per
// file as before. The caller holds the run lock and clears s.seen afterwards.
func (s *Settler) prefetchLastSeen(groups map[string]*conv, live map[string]bool, unchanged map[string]bool) {
	s.seen = nil
	var ids []int64
	for session, c := range groups {
		if live[session] || unchanged[session] {
			continue
		}
		for _, f := range c.files {
			ids = append(ids, f.ID)
		}
	}
	if len(ids) == 0 {
		return
	}
	var got map[int64]int64
	if err := s.st.ReadTx(func(rd *store.Reader) (err error) {
		got, err = rd.LastSeenByFile(ids)
		return err
	}); err != nil {
		return
	}
	for _, id := range ids {
		if _, ok := got[id]; !ok {
			got[id] = 0
		}
	}
	s.seen = got
}

// add folds one evaluation Outcome into the stats. prior is the persisted
// stage before the tick ("" when the Conversation had no row, which reads as
// quiet or live).
func (t *TickStats) add(out Outcome, prior string) {
	settledNow := out.Stage == StageSettled && out.Skipped == ""
	switch {
	case settledNow:
		t.SettledNow++
	case out.Stage == StageSettled && out.Skipped == "settled":
		t.Unchanged++
	}
	if prior == "" && (out.Stage == StageQuiet || out.Stage == StageLive) {
		return
	}
	if out.Stage != prior || settledNow {
		t.Transitions++
	}
}

// Recheck is the on-demand re-check entry point (held --retry): it evaluates
// one Conversation now, without waiting for held.recheck.
func (s *Settler) Recheck(session string) (Outcome, error) {
	defer s.lockRun("recheck")()
	now := s.cfg.Clock.Now()
	s.checkLow()
	live, err := s.cfg.Live()
	if err != nil {
		return Outcome{}, err
	}
	groups, err := s.groups()
	if err != nil {
		return Outcome{}, err
	}
	c := groups[session]
	if c == nil {
		return Outcome{}, store.ErrNotFound
	}
	out, err := s.evaluate(c, live[session], now, true)
	s.refresh(groups, live, now)
	return out, err
}

// RecheckHeld re-checks every held Conversation now.
func (s *Settler) RecheckHeld() ([]Outcome, error) {
	rows, err := s.st.Stages()
	if err != nil {
		return nil, err
	}
	var outs []Outcome
	for _, r := range rows {
		if r.Stage != StageHeld {
			continue
		}
		o, err := s.Recheck(r.Session)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return outs, err
		}
		outs = append(outs, o)
	}
	return outs, nil
}

func (s *Settler) checkLow() {
	if s.cfg.Free == nil || s.cfg.MinFreeBytes <= 0 {
		return
	}
	free, err := s.cfg.Free.Free(s.st.Dir())
	if err != nil {
		return
	}
	low := free < s.cfg.MinFreeBytes
	s.mu.Lock()
	was := s.low
	s.low = low
	s.mu.Unlock()
	if low {
		s.cfg.Obs.Reg.Set(obs.MLowSpace, nil, 1)
		if !was {
			s.cfg.Obs.Event(obs.EvStateDirLow, "free_bytes", strconv.FormatInt(free, 10), "min_free_bytes", strconv.FormatInt(s.cfg.MinFreeBytes, 10))
		}
	} else {
		s.cfg.Obs.Reg.Set(obs.MLowSpace, nil, 0)
	}
}

func (s *Settler) setReason(session, reason string) {
	s.mu.Lock()
	s.reasons[session] = reason
	s.mu.Unlock()
}

// newest is the newest modification time among the Conversation's files: the
// original's when it exists, else the last time the recorder saw the file
// change.
func (s *Settler) newest(c *conv) time.Time {
	var best time.Time
	for _, f := range c.files {
		var t time.Time
		if p := s.cfg.Copier.ReadOriginalPath(f); p != "" {
			if mt, err := s.cfg.Mtime(p); err == nil {
				t = mt
			}
		}
		if t.IsZero() {
			t = s.lastSeen(f)
		}
		if t.After(best) {
			best = t
		}
	}
	return best
}

func (s *Settler) lastSeen(f store.File) time.Time {
	ns := f.FirstSeen
	if v, ok := s.seen[f.ID]; ok {
		return time.Unix(0, max(ns, v))
	}
	if cc, err := s.st.CopyCommits(f.ID); err == nil {
		for _, c := range cc {
			if c.SeenNS > ns {
				ns = c.SeenNS
			}
		}
	}
	if sn, err := s.st.Snapshots(f.ID); err == nil {
		for _, x := range sn {
			if x.SeenNS > ns {
				ns = x.SeenNS
			}
		}
	}
	if ts, err := s.st.Tombstones(f.ID); err == nil {
		for _, t := range ts {
			if t > ns {
				ns = t
			}
		}
	}
	return time.Unix(0, ns)
}

func (s *Settler) clearStage(session, reason string, now time.Time) error {
	s.setReason(session, reason)
	return s.st.Commit(&store.Rec{T: store.TStage, At: now.UnixNano(), Session: session, State: "", Reason: reason})
}

// evaluate runs one Conversation through the stage machine. force skips the
// held.recheck wait.
func (s *Settler) evaluate(c *conv, live bool, now time.Time, force bool) (Outcome, error) {
	out := Outcome{Session: c.session, Stage: StageQuiet}
	row, hasRow, err := s.st.Stage(c.session)
	if err != nil {
		return out, err
	}
	if live {
		// Settled to Live and Held to Live: a Process holds it again.
		out.Stage, out.Skipped = StageLive, "live process"
		if hasRow {
			return out, s.clearStage(c.session, "process", now)
		}
		return out, nil
	}
	switch {
	case hasRow && row.Stage == store.StageRemoved:
		// Removed to Quiet: a removed path reappeared (the Conversation has files
		// with current paths again).
		if err := s.clearStage(c.session, "path_reappeared", now); err != nil {
			return out, err
		}
		hasRow = false
	case hasRow && (row.Stage == store.StageRemovalPending || row.Stage == store.StageExtracting):
		out.Stage, out.Skipped = row.Stage, "removal or extract in progress"
		return out, nil
	}
	if hasRow && row.Stage == StageSettled {
		if !s.changedSinceSettle(c) {
			out.Stage, out.Skipped = StageSettled, "settled"
			return out, nil
		}
		// Settled to Quiet: new writes.
		if err := s.clearStage(c.session, "new_writes", now); err != nil {
			return out, err
		}
		hasRow = false
	}
	newest := s.newest(c)
	if hasRow && row.Stage == StageHeld {
		if newest.After(time.Unix(0, row.SinceNS)) {
			// Held to Quiet: a file was written after the hold.
			if err := s.clearStage(c.session, "new_writes", now); err != nil {
				return out, err
			}
			hasRow = false
		} else {
			s.mu.Lock()
			at := s.checkedAt[c.session]
			s.mu.Unlock()
			if !force && !at.IsZero() && now.Sub(at) < s.cfg.Recheck {
				out.Stage, out.Skipped, out.Reason = StageHeld, "held, recheck not due", row.Reason
				return out, nil
			}
		}
	}
	gate := now.Sub(newest) >= s.cfg.After
	if !gate && !s.LowSpace() {
		out.Skipped = "quiet age below settle.after"
		return out, nil
	}
	s.mu.Lock()
	s.checkedAt[c.session] = now
	s.mu.Unlock()
	_ = s.cfg.Parser.Drain()
	var res Outcome
	err = s.cfg.Copier.WithLock(func() error {
		var e error
		res, e = s.settleLocked(c, now, hasRow && row.Stage == StageHeld)
		return e
	})
	if res.Session == "" {
		res.Session = c.session
	}
	return res, err
}

// changedSinceSettle reports whether any file of the Conversation has bytes the
// settled manifest does not describe (a write after the settle, or a new file).
func (s *Settler) changedSinceSettle(c *conv) bool {
	return changedSinceSettleFrom(&s.st.Reader, c)
}

// changedSinceSettleFrom is changedSinceSettle over a Reader, so a pass can
// ask for every Conversation inside one read transaction.
func changedSinceSettleFrom(rd *store.Reader, c *conv) bool {
	st, ok, err := rd.SettledOf(c.session)
	if err != nil || !ok {
		return false
	}
	// One primary-key read per file: the work is bounded by this
	// Conversation, not by the size of the generations table.
	return changedAgainst(st, c, func(f store.File) (int64, bool) {
		g, err := rd.Generation(f.ID, f.CurGen)
		switch {
		case err == nil:
			return g.CopyOffset, true
		case errors.Is(err, store.ErrNotFound):
			return 0, true
		}
		return 0, false
	})
}

// changedAgainst decides whether c changed since the settle record st. offset
// returns the copy offset of the file's current generation (0 when it has
// none) and false when it cannot be read, which answers "unchanged".
func changedAgainst(st store.Settled, c *conv, offset func(store.File) (int64, bool)) bool {
	var m store.Manifest
	if json.Unmarshal(st.Manifest, &m) != nil {
		return true
	}
	byID := map[int64]store.ManifestFile{}
	for _, f := range m.Files {
		byID[f.File] = f
	}
	for _, f := range c.files {
		mf, ok := byID[f.ID]
		if !ok {
			return true
		}
		if f.Mode == "snapshot" {
			if f.CurGen != mf.Gen || f.LastSize != mf.Size {
				return true
			}
			continue
		}
		if f.CurGen != mf.Gen {
			return true
		}
		off, ok := offset(f)
		if !ok {
			return false
		}
		if off != mf.Size {
			return true
		}
	}
	return false
}

func hashReader(r io.ReaderAt, n int64) (uint64, error) {
	h := xxhash.New()
	buf := make([]byte, 1<<20)
	for off := int64(0); off < n; {
		m := int64(len(buf))
		if n-off < m {
			m = n - off
		}
		got, err := r.ReadAt(buf[:m], off)
		if int64(got) != m {
			if err == nil {
				err = io.ErrUnexpectedEOF
			}
			return 0, err
		}
		_, _ = h.Write(buf[:m])
		off += m
	}
	return h.Sum64(), nil
}

func hashFile(path string) (uint64, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return 0, 0, err
	}
	h, err := hashReader(f, fi.Size())
	return h, fi.Size(), err
}

// lastNewline returns the offset after the last newline in the first n bytes of
// the pointer (0 when there is none).
func lastNewline(p *store.Pointer, n int64) (int64, error) {
	buf := make([]byte, 64<<10)
	for end := n; end > 0; {
		start := end - int64(len(buf))
		if start < 0 {
			start = 0
		}
		b := buf[:end-start]
		if m, err := p.ReadAt(b, start); int64(m) != int64(len(b)) {
			if err == nil {
				err = io.ErrUnexpectedEOF
			}
			return 0, err
		}
		for i := len(b) - 1; i >= 0; i-- {
			if b[i] == '\n' {
				return start + int64(i) + 1, nil
			}
		}
		end = start
	}
	return 0, nil
}

func (s *Settler) parsedState(file int64, gen int) bool {
	var v int
	return s.st.Read().QueryRow(`SELECT version FROM parse_state WHERE file_id=? AND gen=?`, file, gen).Scan(&v) == nil
}

// check runs the settle check over every file. Each clause is an independent
// failing condition. It may parse a torn tail or record a torn-tail fact, and
// it reports the files whose full hash differs from the original's in clone
// mode (the caller starts a new generation for them).
func (s *Settler) check(c *conv, quiet bool, now time.Time) (fails []Failure, regen []store.File, err error) {
	for _, f := range c.files {
		rel := c.rel[f.ID]
		switch f.Mode {
		case "snapshot":
			fl, err := s.checkSnapshot(f, rel)
			if err != nil {
				return nil, nil, err
			}
			fails = append(fails, fl...)
		default:
			fl, rg, err := s.checkAppend(f, rel, quiet, now)
			if err != nil {
				return nil, nil, err
			}
			fails = append(fails, fl...)
			if rg {
				regen = append(regen, f)
			}
		}
	}
	return fails, regen, nil
}

func (s *Settler) checkSnapshot(f store.File, rel string) ([]Failure, error) {
	sn, err := s.st.LatestSnapshot(f.ID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	orig := s.cfg.Copier.ReadOriginalPath(f)
	if orig == "" {
		return nil, nil
	}
	h, size, err := hashFile(orig)
	if err != nil {
		return nil, nil
	}
	switch {
	case size != sn.Size:
		return []Failure{{File: f.ID, Path: rel, Reason: ReasonCopyOffset, Detail: fmt.Sprintf("stored %d bytes, original %d", sn.Size, size)}}, nil
	case h != sn.Hash:
		return []Failure{{File: f.ID, Path: rel, Reason: ReasonCopyTail, Detail: "stored version differs from the original"}}, nil
	}
	return nil, nil
}

func (s *Settler) checkAppend(f store.File, rel string, quiet bool, now time.Time) (fails []Failure, regen bool, err error) {
	if f.CurGen == 0 {
		return nil, false, nil
	}
	g, err := s.st.Generation(f.ID, f.CurGen)
	if err != nil {
		return nil, false, err
	}
	add := func(reason, detail string) {
		fails = append(fails, Failure{File: f.ID, Path: rel, Reason: reason, Detail: detail})
	}
	orig := s.cfg.Copier.ReadOriginalPath(f)
	if orig != "" {
		if fi, err := os.Stat(orig); err == nil {
			switch {
			case fi.Size() != g.CopyOffset:
				add(ReasonCopyOffset, fmt.Sprintf("copy offset %d, original %d bytes", g.CopyOffset, fi.Size()))
			case g.TailLen > 0:
				if !tailMatches(orig, g) {
					add(ReasonCopyTail, "tail hash differs from the original")
				}
			}
		}
	}
	if s.cfg.Parser.HasSource(f.Source) && g.CopyOffset > 0 {
		ptr, err := s.st.OpenPointer(f.ID, g.Gen)
		if err != nil {
			return nil, false, err
		}
		nl, err := lastNewline(ptr, g.CopyOffset)
		_ = ptr.Close()
		if err != nil {
			return nil, false, err
		}
		if fact, ok := s.st.TornTailOf(f.ID, g.Gen); ok && (fact.Off != nl || fact.Off+fact.Len != g.CopyOffset) {
			// Later growth invalidates the fact.
			if err := s.st.Commit(&store.Rec{T: store.TTorn, At: now.UnixNano(), File: f.ID, Gen: g.Gen, Reset: true}); err != nil {
				return nil, false, err
			}
			s.Dirty(f.ID, g.CopyOffset)
		}
		tornLen := g.CopyOffset - nl
		if tornLen > 0 && g.ParseOffset < g.CopyOffset {
			s.cfg.Obs.Event(obs.EvTornTailPending, "file", strconv.FormatInt(f.ID, 10), "source", f.Source, "offset", strconv.FormatInt(nl, 10), "length", strconv.FormatInt(tornLen, 10))
			switch {
			case tornLen >= int64(s.cfg.MaxLineBytes):
				add(ReasonTornTooLong, fmt.Sprintf("torn tail of %d bytes at %d", tornLen, nl))
			case quiet:
				parsed, err := s.cfg.Parser.ParseTail(f.ID, g.Gen)
				if err != nil {
					return nil, false, err
				}
				if parsed {
					if g, err = s.st.Generation(f.ID, g.Gen); err != nil {
						return nil, false, err
					}
				} else if _, ok := s.st.TornTailOf(f.ID, g.Gen); !ok {
					if err := s.st.Commit(&store.Rec{T: store.TTorn, At: now.UnixNano(), File: f.ID, Gen: g.Gen, From: nl, To: g.CopyOffset}); err != nil {
						return nil, false, err
					}
					s.Dirty(f.ID, g.CopyOffset)
				}
			}
		}
		if tornLen < int64(s.cfg.MaxLineBytes) || g.ParseOffset >= g.CopyOffset {
			if g.ParseOffset != nl && g.ParseOffset != g.CopyOffset {
				add(ReasonParseBehind, fmt.Sprintf("parse offset %d, last newline at %d", g.ParseOffset, nl))
			}
		}
		if !s.parsedState(f.ID, g.Gen) {
			add(ReasonUnparsed, "no record of a parse of this generation")
		}
	}
	if len(fails) == 0 && s.cfg.Copier.CloneMode() && orig != "" {
		ptr, err := s.st.OpenPointer(f.ID, g.Gen)
		if err != nil {
			return nil, false, err
		}
		ch, cerr := hashReader(ptr, g.CopyOffset)
		_ = ptr.Close()
		oh, osize, oerr := hashFile(orig)
		if cerr == nil && oerr == nil && (oh != ch || osize != g.CopyOffset) {
			regen = true
		}
	}
	return fails, regen, nil
}

func tailMatches(orig string, g store.Generation) bool {
	f, err := os.Open(orig)
	if err != nil {
		return true
	}
	defer func() { _ = f.Close() }()
	b := make([]byte, g.TailLen)
	if m, err := f.ReadAt(b, g.CopyOffset-int64(g.TailLen)); m != len(b) && err != nil {
		return false
	}
	return xxhash.Sum64(b) == g.TailHash
}

// settleLocked checks and settles one Conversation. The copier's lock is held.
func (s *Settler) settleLocked(c *conv, now time.Time, wasHeld bool) (Outcome, error) {
	out := Outcome{Session: c.session, Stage: StageQuiet}
	newest := s.newest(c)
	quiet := now.Sub(newest) >= s.cfg.After || s.LowSpace()
	fails, regen, err := s.check(c, quiet, now)
	if err != nil {
		return out, err
	}
	if len(fails) > 0 {
		return s.hold(c, fails, now, wasHeld)
	}
	if len(regen) > 0 {
		// A full-hash mismatch at settle starts a new generation from byte 0 and
		// keeps the old one (a reset entry follows); the Conversation stays Quiet.
		for _, f := range regen {
			orig := s.cfg.Copier.ReadOriginalPath(f)
			if err := s.cfg.Copier.NewGenerationLocked(f, orig, "full_hash_mismatch"); err != nil {
				return out, err
			}
		}
		out.Skipped = "full hash mismatch: new generation started"
		return out, nil
	}
	if wasHeld {
		if err := s.clearStage(c.session, "check_passed", now); err != nil {
			return out, err
		}
	}
	var settled bool
	if s.cfg.Copier.CloneMode() {
		settled, err = s.settleClone(c, now)
	} else {
		settled, err = s.settleBytes(c, now)
	}
	if err != nil {
		return out, err
	}
	if settled {
		out.Stage = StageSettled
		s.setReason(c.session, "quiet")
		s.cfg.Obs.Reg.Add(obs.MSettled, nil, 1)
	} else {
		out.Skipped = "settle discarded"
	}
	return out, nil
}

func (s *Settler) hold(c *conv, fails []Failure, now time.Time, wasHeld bool) (Outcome, error) {
	out := Outcome{Session: c.session, Stage: StageHeld, Reason: fails[0].Reason, Failures: fails}
	detail, _ := json.Marshal(fails)
	row, has, _ := s.st.Stage(c.session)
	since := now.UnixNano()
	if has && row.Stage == StageHeld {
		since = row.SinceNS
	}
	if has && row.Stage == StageHeld && row.Reason == out.Reason && row.Detail == string(detail) {
		return out, nil
	}
	if err := s.st.Commit(&store.Rec{T: store.TStage, At: since, Session: c.session, State: StageHeld, Reason: out.Reason, Val: string(detail)}); err != nil {
		return out, err
	}
	s.setReason(c.session, out.Reason)
	s.cfg.Obs.Event(obs.EvSessionHeld, "session", c.session, "reason", out.Reason, "files", strconv.Itoa(len(fails)))
	return out, nil
}

// registryVersions returns the registry file versions whose records name the
// session.
func (s *Settler) registryVersions(session string) ([]store.ManifestRegistry, error) {
	rows, err := s.st.Read().Query(pick(s.st.SessionRefReady(), registryVersionsQuery, registryVersionsQueryRef), session)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []store.ManifestRegistry
	for rows.Next() {
		var x store.ManifestRegistry
		if err := rows.Scan(&x.File, &x.Version, &x.Size); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

func (s *Settler) manifest(c *conv, method string, now time.Time) (store.Manifest, error) {
	m := store.Manifest{Session: c.session, SettledAt: now.UnixNano(), Method: method}
	if rv, err := s.registryVersions(c.session); err == nil {
		m.Registry = rv
	}
	ids := map[string]bool{}
	files := append([]store.File(nil), c.files...)
	sort.Slice(files, func(i, j int) bool { return c.rel[files[i].ID] < c.rel[files[j].ID] })
	for _, f := range files {
		mf := store.ManifestFile{Path: c.rel[f.ID], File: f.ID, Mode: f.Mode, Gen: f.CurGen}
		if s.cfg.Parser.HasSource(f.Source) {
			mf.Parsed, mf.ParsedBy = true, f.Source
		}
		if sess, err := s.st.SessionsOfFile(f.ID); err == nil {
			mf.SessionIDs = sess
			for _, x := range sess {
				ids[x] = true
			}
		}
		switch f.Mode {
		case "snapshot":
			sns, err := s.st.Snapshots(f.ID)
			if err != nil {
				return m, err
			}
			for _, sn := range sns {
				mf.Generations = append(mf.Generations, store.ManifestGen{Gen: sn.Version, Size: sn.Size})
			}
			if sn, err := s.st.LatestSnapshot(f.ID); err == nil {
				mf.Size, mf.Gen = sn.Size, sn.Version
				mf.Hash = strconv.FormatUint(sn.Hash, 16)
			}
			mf.Size = f.LastSize
		default:
			gens, err := s.st.Generations(f.ID)
			if err != nil {
				return m, err
			}
			for _, g := range gens {
				mf.Generations = append(mf.Generations, store.ManifestGen{Gen: g.Gen, Size: g.CopyOffset})
				if g.Gen == f.CurGen {
					mf.Size = g.CopyOffset
					ptr, err := s.st.OpenPointer(f.ID, g.Gen)
					if err != nil {
						if g.CopyOffset == 0 {
							continue
						}
						return m, err
					}
					h, herr := hashReader(ptr, g.CopyOffset)
					_ = ptr.Close()
					if herr != nil {
						return m, herr
					}
					mf.Hash = strconv.FormatUint(h, 16)
				}
			}
		}
		m.Files = append(m.Files, mf)
	}
	for k := range ids {
		m.SessionID = append(m.SessionID, k)
	}
	sort.Strings(m.SessionID)
	return m, nil
}

// writeManifestFile durably writes archives/<session>/manifest.json.
func (s *Settler) writeManifestFile(session string, b []byte) error {
	dir := s.st.SegDir(session)
	if err := s.fs.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	final := filepath.Join(dir, "manifest.json")
	tmp := final + ".tmp"
	f, err := s.fs.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(b)
	if err == nil {
		err = fsx.FullSync(f)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if err := s.fs.Rename(tmp, final); err != nil {
		return err
	}
	return fsx.FullSyncDir(s.fs, dir)
}

// settleBytes compresses the Conversation's copies into a new archive segment,
// commits "settled" and deletes the covered copy bytes.
func (s *Settler) settleBytes(c *conv, now time.Time) (bool, error) {
	m, err := s.manifest(c, "bytes", now)
	if err != nil {
		return false, err
	}
	mb, err := json.Marshal(m)
	if err != nil {
		return false, err
	}
	prev, _, err := s.st.SettledOf(c.session)
	if err != nil {
		return false, err
	}
	seg := store.SegName(prev.Seq + 1)
	var items []store.SegItem
	var ranges []store.RangeRec
	var closers []*store.Pointer
	defer func() {
		for _, p := range closers {
			_ = p.Close()
		}
	}()
	add := func(f store.File, gen int, size int64) error {
		from := s.st.ArchivedTo(f.ID, gen)
		if size <= from {
			return nil
		}
		p, err := s.st.OpenPointer(f.ID, gen)
		if err != nil {
			return err
		}
		closers = append(closers, p)
		items = append(items, store.SegItem{File: f.ID, Gen: gen, From: from, To: size, Src: p})
		ranges = append(ranges, store.RangeRec{File: f.ID, Gen: gen, From: from, To: size, Seg: seg})
		return nil
	}
	for _, f := range c.files {
		if f.Mode == "snapshot" {
			sns, err := s.st.Snapshots(f.ID)
			if err != nil {
				return false, err
			}
			for _, sn := range sns {
				if err := add(f, sn.Version, sn.Size); err != nil {
					return false, err
				}
			}
			continue
		}
		gens, err := s.st.Generations(f.ID)
		if err != nil {
			return false, err
		}
		for _, g := range gens {
			if err := add(f, g.Gen, g.CopyOffset); err != nil {
				return false, err
			}
		}
	}
	// Registry versions are keyed by session and go into that session's archive;
	// their stored bytes stay where they are (the registry file is no one's).
	for _, rv := range m.Registry {
		from := s.st.ArchivedTo(rv.File, rv.Version)
		if rv.Size <= from {
			continue
		}
		p, err := s.st.OpenPointer(rv.File, rv.Version)
		if err != nil {
			return false, err
		}
		closers = append(closers, p)
		items = append(items, store.SegItem{File: rv.File, Gen: rv.Version, From: from, To: rv.Size, Src: p})
		ranges = append(ranges, store.RangeRec{File: rv.File, Gen: rv.Version, From: from, To: rv.Size, Seg: seg})
	}
	if err := s.writeManifestFile(c.session, mb); err != nil {
		return false, err
	}
	if len(items) > 0 {
		res, err := store.WriteSegmentFile(s.fs, s.st.SegDir(c.session), seg, mb, items)
		if err != nil {
			return false, err
		}
		s.cfg.Obs.Event(obs.EvArchiveWritten, "session", c.session, "segment", seg, "bytes", strconv.FormatInt(res.Size, 10), "ranges", strconv.Itoa(len(ranges)))
	}
	val, _ := json.Marshal(store.SettleRec{Manifest: mb, Ranges: ranges})
	if err := s.st.CommitFull(&store.Rec{T: store.TSettle, At: now.UnixNano(), Session: c.session, Method: "bytes", Val: string(val)}); err != nil {
		return false, err
	}
	faults.At("settle.after-commit")
	if h := s.cfg.Hooks.BeforeDelete; h != nil {
		h(c.session)
	}
	return true, s.reclaim(c, ranges)
}

func (s *Settler) reclaim(c *conv, ranges []store.RangeRec) error {
	byID := map[int64]store.File{}
	for _, f := range c.files {
		byID[f.ID] = f
	}
	for i, r := range ranges {
		f, ok := byID[r.File]
		if !ok {
			continue
		}
		if err := s.cfg.Copier.ReclaimLocked(f, r.Gen); err != nil {
			return err
		}
		if i == 0 {
			faults.At("settle.partial-delete")
		}
	}
	return nil
}

// settleClone re-clones each append file from its Claude original after the
// full-hash check, re-hashes the clone before it replaces the preserved copy,
// and commits "settled". A write that lands between the check and the clone
// (the clone is longer than the committed offset, or the original no longer
// matches) discards the clones and leaves the Conversation Quiet.
func (s *Settler) settleClone(c *conv, now time.Time) (bool, error) {
	type cl struct {
		f   store.File
		g   store.Generation
		tmp string
	}
	var clones []cl
	discard := func(why string, file int64) {
		for _, x := range clones {
			_ = s.fs.Remove(x.tmp)
		}
		s.cfg.Obs.Event(obs.EvSettleDiscarded, "session", c.session, "file", strconv.FormatInt(file, 10), "reason", why)
	}
	policy := s.cfg.Copier.ClonePolicy()
	for _, f := range c.files {
		if f.Mode != "append" || f.CurGen == 0 {
			continue
		}
		orig := s.cfg.Copier.ReadOriginalPath(f)
		if orig == "" {
			continue
		}
		g, err := s.st.Generation(f.ID, f.CurGen)
		if err != nil {
			return false, err
		}
		if g.CopyOffset == 0 {
			continue
		}
		if h := s.cfg.Hooks.BeforeClone; h != nil {
			h(f.ID)
		}
		ptr, err := s.st.OpenPointer(f.ID, g.Gen)
		if err != nil {
			return false, err
		}
		want, herr := hashReader(ptr, g.CopyOffset)
		_ = ptr.Close()
		if herr != nil {
			return false, herr
		}
		tmp := s.st.CopyPath(f.ID, g.Gen) + ".tmp"
		_ = s.fs.Remove(tmp)
		if err := policy.Clone(orig, tmp); err != nil {
			discard("clone failed: "+err.Error(), f.ID)
			return false, nil
		}
		clones = append(clones, cl{f, g, tmp})
		fi, err := os.Stat(tmp)
		if err != nil {
			return false, err
		}
		if fi.Size() != g.CopyOffset {
			discard(fmt.Sprintf("clone is %d bytes, committed offset %d", fi.Size(), g.CopyOffset), f.ID)
			return false, nil
		}
		got, _, herr := hashFile(tmp)
		if herr != nil {
			return false, herr
		}
		if got != want {
			discard("clone differs from the preserved copy", f.ID)
			return false, nil
		}
		tf, err := s.fs.OpenFile(tmp, os.O_RDWR, 0)
		if err != nil {
			return false, err
		}
		err = fsx.FullSync(tf)
		_ = tf.Close()
		if err != nil {
			return false, err
		}
		fsx.Mark(s.fs, "readback")
		faults.At("settle.after-temp")
	}
	if len(clones) > 0 {
		if err := fsx.FullSyncDir(s.fs, filepath.Dir(clones[0].tmp)); err != nil {
			return false, err
		}
	}
	for _, x := range clones {
		if err := s.cfg.Copier.ReplaceCopyLocked(x.f, x.g.Gen, x.tmp); err != nil {
			return false, err
		}
	}
	faults.At("settle.after-rename")
	m, err := s.manifest(c, "clone", now)
	if err != nil {
		return false, err
	}
	mb, err := json.Marshal(m)
	if err != nil {
		return false, err
	}
	if err := s.writeManifestFile(c.session, mb); err != nil {
		return false, err
	}
	val, _ := json.Marshal(store.SettleRec{Manifest: mb})
	if err := s.st.CommitFull(&store.Rec{T: store.TSettle, At: now.UnixNano(), Session: c.session, Method: "clone", Val: string(val)}); err != nil {
		return false, err
	}
	faults.At("settle.after-commit")
	return true, nil
}

// Recover runs on start, after the copier's own recovery. It removes archive
// segments that were renamed into place but never committed, removes leftover
// temporary files, and finishes the delete a crash after "settled" cut short.
func (s *Settler) Recover() error {
	defer s.lockRun("recover")()
	root := filepath.Join(s.st.Dir(), store.ArchivesDir)
	ents, err := s.fs.ReadDir(root)
	if err != nil {
		return err
	}
	removed := 0
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		session := e.Name()
		row, ok, err := s.st.SettledOf(session)
		if err != nil {
			return err
		}
		dir := filepath.Join(root, session)
		segs, err := s.fs.ReadDir(dir)
		if err != nil {
			return err
		}
		for _, sg := range segs {
			name := sg.Name()
			orphan := strings.HasSuffix(name, ".tmp")
			if strings.HasSuffix(name, ".seg") {
				n, perr := strconv.Atoi(strings.TrimSuffix(name, ".seg"))
				orphan = perr != nil || !ok || n > row.Seq
			}
			if orphan {
				if err := s.fs.Remove(filepath.Join(dir, name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
					return err
				}
				removed++
			}
		}
	}
	files, err := s.st.AllFiles()
	if err != nil {
		return err
	}
	byID := map[int64]store.File{}
	for _, f := range files {
		byID[f.ID] = f
	}
	ranges, err := s.st.AllRanges()
	if err != nil {
		return err
	}
	finished := 0
	err = s.cfg.Copier.WithLock(func() error {
		seen := map[[2]int64]bool{}
		for _, r := range ranges {
			k := [2]int64{r.File, int64(r.Gen)}
			if seen[k] {
				continue
			}
			seen[k] = true
			f, ok := byID[r.File]
			if !ok || f.Source == "session-registry" {
				continue
			}
			if err := s.cfg.Copier.ReclaimLocked(f, r.Gen); err != nil {
				return err
			}
			finished++
		}
		return nil
	})
	if err != nil {
		return err
	}
	if removed > 0 {
		s.cfg.Obs.Event(obs.EvSettleCrashRecovery, "orphans_removed", strconv.Itoa(removed), "deletes_finished", strconv.Itoa(finished))
	}
	return s.recoverRemoval()
}

// gaugeStages are the stages the conversations_by_stage gauge and the published
// counts carry, in order.
var gaugeStages = []string{StageLive, StageQuiet, StageHeld, StageSettled, "removal_pending", "removed"}

// LastCounts returns a copy of the stage counts the most recent refresh derived
// (the same figures it set on the conversations_by_stage gauge) and the time of
// that refresh. ok is false before the first refresh.
func (s *Settler) LastCounts() (counts map[string]int, at time.Time, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pubCounts == nil {
		return nil, time.Time{}, false
	}
	counts = make(map[string]int, len(s.pubCounts))
	for k, v := range s.pubCounts {
		counts[k] = v
	}
	return counts, s.pubAt, true
}

// refresh derives every Conversation's stage from scratch, emits the stage
// changes through transition, rebuilds the kept counts and gauge inputs, and sets
// the gauges. It is the reconciliation's half of the incremental bookkeeping: in
// event mode it compares the stage counts kept since the last reconciliation
// with what the store says now, logs a difference once and meters it.
func (s *Settler) refresh(groups map[string]*conv, live map[string]bool, now time.Time) {
	rows, _ := s.st.Stages()
	persisted := map[string]store.StageRow{}
	for _, r := range rows {
		persisted[r.Session] = r
	}
	offsets, _ := s.st.CopyOffsets()
	arch, _ := s.st.AllRanges()
	archived := map[[2]int64]int64{}
	for _, r := range arch {
		k := [2]int64{r.File, int64(r.Gen)}
		if r.From <= archived[k] && r.To > archived[k] {
			archived[k] = r.To
		}
	}
	pendingByFile := map[int64]int64{}
	for k, off := range offsets {
		if p := off - archived[k]; p > 0 {
			pendingByFile[k[0]] += p
		}
	}
	checkDrift := s.cfg.Mode == ModeEvent && s.seeded
	kept := s.keptCounts()
	// What the kept counts should equal: the stage each Conversation was last
	// published in (the ones this settler has never published are not in them).
	expect := map[string]int{}
	ng := newGaugeState()
	fileSession := map[int64]string{}
	var rescued float64
	for session, c := range groups {
		stage := StageQuiet
		switch r, ok := persisted[session]; {
		case live[session]:
			stage = StageLive
		case ok:
			stage = r.Stage
		}
		s.mu.Lock()
		prev, known := s.last[session]
		s.mu.Unlock()
		if known {
			expect[prev]++
			if prev != stage {
				rescued++
			}
		}
		var pend int64
		for _, f := range c.files {
			pend += pendingByFile[f.ID]
			fileSession[f.ID] = session
		}
		s.mu.Lock()
		ng.setStage(session, stage)
		var held *heldFact
		if r, ok := persisted[session]; ok && stage == StageHeld {
			held = &heldFact{reason: r.Reason, sinceNS: r.SinceNS}
		}
		ng.setFacts(session, held, pend, nil)
		s.mu.Unlock()
		s.transition(session, s.priorStage(session, stage), stage)
	}
	for session, r := range persisted {
		if _, ok := groups[session]; ok {
			continue
		}
		if r.Stage == store.StageRemoved || r.Stage == store.StageRemovalPending {
			expect[r.Stage]++
			ng.setStage(session, r.Stage)
		}
	}
	if facts, err := s.st.TornTails(); err == nil {
		ids := make([]int64, 0, len(facts))
		for _, t := range facts {
			ids = append(ids, t.File)
		}
		var files []store.File
		if len(ids) > 0 {
			files, _ = s.st.FilesByIDs(ids)
		}
		src := map[int64]string{}
		for _, f := range files {
			src[f.ID] = f.Source
		}
		for _, t := range facts {
			owner := fileSession[t.File]
			m := ng.torn[owner]
			if m == nil {
				m = map[string]int64{}
				ng.torn[owner] = m
			}
			if old, ok := m[src[t.File]]; !ok || t.AtNS < old {
				m[src[t.File]] = t.AtNS
			}
		}
	}
	if checkDrift {
		reg := s.cfg.Obs.Reg
		if !sameCounts(kept, expect) {
			s.cfg.Obs.Log.Warn("settle_counts_drift", "kept", fmtCounts(kept), "scratch", fmtCounts(expect))
			reg.Add(obs.MSettleCountsDrift, nil, 1)
		}
		if rescued > 0 {
			reg.Add(obs.MSettleReconcileRescued, nil, rescued)
		}
	}
	s.mu.Lock()
	s.gauges = ng
	s.mu.Unlock()
	s.refreshRemovalGauges(groups, live, now)
	s.publishGauges(now)
}
