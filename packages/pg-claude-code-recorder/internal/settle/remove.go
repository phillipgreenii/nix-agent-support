package settle

// Removal: the stage that takes a settled Conversation's files out of the Claude
// directory once it has been quiet for remove.after. Off unless remove.enable is
// true. The order, so a crash at any point loses nothing:
//
//  1. re-run the check against the Claude files and, in clone mode, write and
//     verify the archive from the clone; compare each file's full hash;
//  2. commit "removal pending", listing each file with its inode, size and hash;
//  3. per file: require no registry file naming the session and no live Process,
//     rename it into the quarantine, list the processes that hold the quarantined
//     inode (restoring the file if there is one), re-hash it against the archive,
//     flush the archive, check size and modification time once more, unlink it;
//  4. commit "removed".
//
// A restart finds a pending removal and finishes or undoes it (recoverRemoval).

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/faults"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/fsx"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/scope"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
)

// Errors a caller maps to the exit-8 preconditions.
var (
	ErrRemovalDisabled = errors.New("removal is disabled (remove.enable = false)")
	ErrTargetExists    = errors.New("the target already exists")
	ErrHeldByLive      = errors.New("a live Process holds the Conversation")
)

// The stable removal blockers (blocked_by of where, and the dry-run skip reasons).
const (
	BlockRemovalDisabled = "removal_disabled"
	BlockLive            = "live_process"
	BlockRegistry        = "registry_names_session"
	BlockHeld            = "held"
	BlockNotSettled      = "not_settled"
	BlockInProgress      = "removal_in_progress"
	BlockRemoved         = "already_removed"
	BlockQuietAge        = "quiet_age"
	BlockFormat          = "format_hold"
	BlockNoFiles         = "no_files"
	BlockRunCap          = "run_cap"
	BlockHourCap         = "hour_cap"
	BlockNewWrites       = "new_writes"
	BlockNoArchive       = "no_archive"
	BlockBarrier         = "durability_barrier"
	BlockUnavailable     = obs.ReasonQuarantineUnavl
)

type pendingFile struct {
	File    int64  `json:"file"`
	Rel     string `json:"rel"`
	Path    string `json:"path"`
	Dev     uint64 `json:"dev"`
	Ino     uint64 `json:"ino"`
	Size    int64  `json:"size"`
	MtimeNS int64  `json:"mtime_ns"`
	Hash    string `json:"hash"`
	State   string `json:"state"` // listed, quarantined or unlinked
	Q       string `json:"q,omitempty"`
}

type pendingList struct {
	Session string        `json:"session"`
	Files   []pendingFile `json:"files"`
}

const (
	fsListed      = "listed"
	fsQuarantined = "quarantined"
	fsUnlinked    = "unlinked"
)

// RemoveOptions selects what Remove acts on.
type RemoveOptions struct {
	// Sessions names the Conversations; none means every Conversation that is
	// settled and older than remove.after. A named one never bypasses the age.
	Sessions []string
	DryRun   bool
}

// RemoveSkip is one Conversation removal did not touch, and why.
type RemoveSkip struct {
	Session string   `json:"session"`
	Reasons []string `json:"reasons"`
}

// RemoveCandidate is a Conversation that is (or, on a dry run, would be) removed.
type RemoveCandidate struct {
	Session string `json:"session"`
	Files   int    `json:"files"`
	Bytes   int64  `json:"bytes"`
}

// RemoveReport is the answer of Remove.
type RemoveReport struct {
	Enabled    bool              `json:"enabled"`
	DryRun     bool              `json:"dry_run"`
	Candidates []RemoveCandidate `json:"candidates"`
	Removed    []string          `json:"removed"`
	Skipped    []RemoveSkip      `json:"skipped"`
	// SkipCounts counts the skipped Conversations by reason.
	SkipCounts map[string]int `json:"skip_counts"`
	Bytes      int64          `json:"bytes"`
}

func (s *Settler) cfs() fsx.FS {
	if s.cfg.FS != nil {
		return s.cfg.FS
	}
	return s.fs
}

// Remove removes the selected Conversations (or reports what it would remove on
// a dry run, which works while removal is disabled and changes nothing).
//
// The format-hold scan (a read over the last day's commits and records) runs
// BEFORE the run lock is taken, so a slow scan never makes the settle tick wait
// behind a remove. The verdicts it produced may be stale by the time the lock is
// held, so removeLocked re-runs the scan for the sources of the Conversations it
// is about to remove, under the lock and right before the removal (see
// formatHeldNow); the early verdicts only decide what a dry run reports and
// which Conversations are worth considering.
func (s *Settler) Remove(opt RemoveOptions) (RemoveReport, error) {
	early := s.prescanFormat(s.cfg.Clock.Now())
	if h := s.cfg.Hooks.AfterFormatPrescan; h != nil {
		h()
	}
	defer s.lockRun("remove")()
	rep, err := s.removeLocked(opt, s.cfg.Clock.Now(), early)
	if err == nil && !opt.DryRun {
		// An operator run that completed counts even when it removed nothing.
		s.recordRemovalRun(s.cfg.Clock.Now())
	}
	return rep, err
}

func (s *Settler) hourCount(now time.Time) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.removedAt[:0]
	for _, t := range s.removedAt {
		if now.Sub(t) < time.Hour {
			kept = append(kept, t)
		}
	}
	s.removedAt = kept
	return len(kept)
}

// removeLocked runs the removal pass with the run lock held. early holds the
// format verdicts computed before the lock (nil: the caller holds the lock for
// the whole pass, as the tick does, and the verdicts are computed on first use).
func (s *Settler) removeLocked(opt RemoveOptions, now time.Time, early *formatCache) (rep RemoveReport, err error) {
	defer func() {
		if !opt.DryRun && len(rep.Removed) > 0 {
			s.markRemovalGauges()
			if err == nil {
				s.recordRemovalRun(now)
			}
		}
	}()
	return s.removeLockedBody(opt, now, early)
}

func (s *Settler) removeLockedBody(opt RemoveOptions, now time.Time, early *formatCache) (RemoveReport, error) {
	fc := early
	if fc == nil {
		fc = newFormatCache(false)
	}
	rep := RemoveReport{
		Enabled: s.cfg.RemoveEnable, DryRun: opt.DryRun,
		Candidates: []RemoveCandidate{}, Removed: []string{}, Skipped: []RemoveSkip{}, SkipCounts: map[string]int{},
	}
	if !s.cfg.RemoveEnable && !opt.DryRun {
		return rep, ErrRemovalDisabled
	}
	live, err := s.cfg.Live()
	if err != nil {
		return rep, err
	}
	groups, err := s.groups()
	if err != nil {
		return rep, err
	}
	rows, err := s.st.Stages()
	if err != nil {
		return rep, err
	}
	stages := map[string]store.StageRow{}
	for _, r := range rows {
		stages[r.Session] = r
	}
	sessions := opt.Sessions
	if len(sessions) == 0 {
		for k := range groups {
			sessions = append(sessions, k)
		}
		for k, r := range stages {
			if r.Stage == store.StageRemoved && groups[k] == nil {
				sessions = append(sessions, k)
			}
		}
	}
	sort.Strings(sessions)
	skip := func(session string, reasons ...string) {
		rep.Skipped = append(rep.Skipped, RemoveSkip{Session: session, Reasons: reasons})
		for _, r := range reasons {
			rep.SkipCounts[r]++
		}
	}
	var todo []*conv
	for _, session := range sessions {
		if !uuidRe.MatchString(session) {
			skip(session, "not_found")
			continue
		}
		c := groups[session]
		row, hasRow := stages[session]
		if c == nil {
			if hasRow && row.Stage == store.StageRemoved {
				// A removed Conversation is not a candidate; only a named one is reported.
				if len(opt.Sessions) > 0 {
					skip(session, BlockRemoved)
				}
				continue
			}
			skip(session, BlockNoFiles)
			continue
		}
		blocked, _ := s.blockers(c, row, hasRow, live[session], now, fc)
		if len(blocked) > 0 {
			if len(opt.Sessions) == 0 && onlyAgeOrState(blocked) {
				// A Conversation that is simply not due is not worth a line in the
				// automatic pass; a dry run and a named remove still report it.
				if !opt.DryRun {
					continue
				}
			}
			skip(session, blocked...)
			continue
		}
		todo = append(todo, c)
		cand := RemoveCandidate{Session: session, Files: len(c.files)}
		for _, f := range c.files {
			cand.Bytes += s.fileBytes(f)
		}
		rep.Candidates = append(rep.Candidates, cand)
		rep.Bytes += cand.Bytes
	}
	if opt.DryRun {
		return rep, nil
	}
	runCount := 0
	var first error
	// Verdicts from before the lock are re-read once per source, here, under the
	// lock and ahead of the first removal: a bad-line rate that rose since the
	// early scan must still keep the files.
	fresh := newFormatCache(false)
	for _, c := range todo {
		switch {
		case runCount >= s.cfg.MaxPerRun:
			skip(c.session, BlockRunCap)
			continue
		case s.hourCount(now) >= s.cfg.MaxPerHour:
			skip(c.session, BlockHourCap)
			continue
		}
		if fc.early {
			if held, _ := s.formatHeld(c, now, fresh); held {
				skip(c.session, BlockFormat)
				continue
			}
		}
		var removed bool
		var reason string
		err := s.cfg.Copier.WithLock(func() error {
			var e error
			removed, reason, e = s.removeConversation(c, now)
			return e
		})
		switch {
		case err != nil:
			if first == nil {
				first = fmt.Errorf("remove %s: %w", c.session, err)
			}
			skip(c.session, "error")
		case removed:
			runCount++
			rep.Removed = append(rep.Removed, c.session)
			s.mu.Lock()
			s.removedAt = append(s.removedAt, now)
			s.mu.Unlock()
		default:
			skip(c.session, reason)
		}
	}
	return rep, first
}

func onlyAgeOrState(blocked []string) bool {
	for _, b := range blocked {
		if b != BlockQuietAge && b != BlockNotSettled && b != BlockRemoved && b != BlockHeld {
			return false
		}
	}
	return true
}

func (s *Settler) fileBytes(f store.File) int64 {
	if f.Mode == "snapshot" {
		if sn, err := s.st.LatestSnapshot(f.ID); err == nil {
			return sn.Size
		}
		return 0
	}
	if f.CurGen == 0 {
		return 0
	}
	if g, err := s.st.Generation(f.ID, f.CurGen); err == nil {
		return g.CopyOffset
	}
	return 0
}

// blockers lists why the Conversation is not removable now (the stable
// blocked_by list; removal_disabled is reported by the caller, not here) and the
// earliest time the age gate opens.
func (s *Settler) blockers(c *conv, row store.StageRow, hasRow, live bool, now time.Time, fc *formatCache) ([]string, time.Time) {
	var out []string
	var next time.Time
	if live {
		out = append(out, BlockLive)
	}
	if s.registryNames(c.session) {
		out = append(out, BlockRegistry)
	}
	switch {
	case !hasRow:
		out = append(out, BlockNotSettled)
	case row.Stage == StageHeld:
		out = append(out, BlockHeld)
	case row.Stage == store.StageRemovalPending:
		out = append(out, BlockInProgress)
	case row.Stage == store.StageRemoved:
		out = append(out, BlockRemoved)
	case row.Stage != StageSettled:
		out = append(out, BlockNotSettled)
	}
	newest := s.newest(c)
	if due := newest.Add(s.cfg.RemoveAfter); now.Before(due) {
		out = append(out, BlockQuietAge)
		next = due
	}
	if held, _ := s.formatHeld(c, now, fc); held {
		out = append(out, BlockFormat)
	}
	return out, next
}

// registryNames reports whether a registry file that is still present names the
// session (a Process may still be running it, or just left it behind).
func (s *Settler) registryNames(session string) bool {
	var one int
	err := s.st.Read().QueryRow(pick(s.st.SessionRefReady(), registryNamesQuery, registryNamesQueryRef), session).Scan(&one)
	return err == nil
}

// formatVerdict is the format-hold answer for one source.
type formatVerdict struct {
	held bool
	why  string
}

// formatCache memoizes the format-hold verdict per source for one pass: the
// answer depends on the source and the cutoff only, never on the Conversation,
// so a pass over thousands of Conversations needs one scan per source. early
// marks verdicts computed before the run lock was taken.
type formatCache struct {
	early bool
	m     map[string]formatVerdict
}

func newFormatCache(early bool) *formatCache {
	return &formatCache{early: early, m: map[string]formatVerdict{}}
}

// prescanFormat computes the verdict of every source the index has files for,
// without the run lock.
func (s *Settler) prescanFormat(now time.Time) *formatCache {
	fc := newFormatCache(true)
	rows, err := s.st.Read().Query(`SELECT DISTINCT source FROM files`)
	if err != nil {
		return fc // verdicts are computed on first use, under the lock
	}
	var sources []string
	for rows.Next() {
		var src string
		if rows.Scan(&src) == nil {
			sources = append(sources, src)
		}
	}
	_ = rows.Close()
	for _, src := range sources {
		if s.cfg.Parser.HasSource(src) {
			fc.m[src] = s.sourceFormatHeld(src, now)
		}
	}
	return fc
}

// The three counts behind the format hold. Each is driven by the small table
// (files of the source, then their commits through the primary key) and reaches
// records only through records_ident, by file, generation and an offset range,
// so none of them scans records (TestFormatHeldQueryPlansUseIndexes guards it).
const (
	formatLinesQuery = `SELECT COUNT(*) FROM (SELECT DISTINCT r.file_id, r.gen, r.off
FROM files f CROSS JOIN commits c ON c.file_id=f.id
CROSS JOIN records r ON r.file_id=c.file_id AND r.gen=c.gen AND r.off>=c.from_off AND r.off<c.to_off
WHERE f.source=? AND c.seen_ns>=?)`
	formatBadQuery = `SELECT COUNT(*) FROM files f CROSS JOIN bad_lines b ON b.file_id=f.id
CROSS JOIN commits c ON c.file_id=b.file_id AND c.gen=b.gen AND c.from_off<=b.off AND b.off<c.to_off
WHERE f.source=? AND c.seen_ns>=?`
	formatUnknownQuery = `SELECT COALESCE(SUM(u.count),0) FROM unknown_keys u JOIN files f ON f.id=u.file_id
WHERE u.source=? AND EXISTS(SELECT 1 FROM commits c WHERE c.file_id=u.file_id AND c.gen=u.gen AND c.seen_ns>=?)`
)

// formatHeld reports whether a source of the Conversation has a bad-line or
// unknown-key rate over the last day above format.holdRate, so files are never
// removed on the strength of a parse that was wrong for a new format. fc may be
// nil (nothing is remembered).
func (s *Settler) formatHeld(c *conv, now time.Time, fc *formatCache) (bool, string) {
	seen := map[string]bool{}
	for _, f := range c.files {
		if seen[f.Source] || !s.cfg.Parser.HasSource(f.Source) {
			continue
		}
		seen[f.Source] = true
		var v formatVerdict
		var ok bool
		if fc != nil {
			v, ok = fc.m[f.Source]
		}
		if !ok {
			v = s.sourceFormatHeld(f.Source, now)
			if fc != nil {
				fc.m[f.Source] = v
			}
		}
		if v.held {
			return true, v.why
		}
	}
	return false, ""
}

// sourceFormatHeld scans one source's last day.
func (s *Settler) sourceFormatHeld(source string, now time.Time) formatVerdict {
	if h := s.cfg.Hooks.InFormatScan; h != nil {
		h(source)
	}
	cutoff := now.Add(-24 * time.Hour).UnixNano()
	var lines, bad, unknown int64
	_ = s.st.Read().QueryRow(formatLinesQuery, source, cutoff).Scan(&lines)
	_ = s.st.Read().QueryRow(formatBadQuery, source, cutoff).Scan(&bad)
	_ = s.st.Read().QueryRow(formatUnknownQuery, source, cutoff).Scan(&unknown)
	total := lines + bad
	if total == 0 {
		return formatVerdict{}
	}
	if float64(bad)/float64(total) > s.cfg.HoldRate {
		return formatVerdict{true, fmt.Sprintf("%s: %d bad lines of %d over the last day", source, bad, total)}
	}
	if float64(unknown)/float64(total) > s.cfg.HoldRate {
		return formatVerdict{true, fmt.Sprintf("%s: %d unknown keys over %d lines in the last day", source, unknown, total)}
	}
	return formatVerdict{}
}

func (s *Settler) holdsSession(session string) string {
	live, err := s.cfg.Live()
	if err != nil || live[session] {
		return BlockLive
	}
	if s.registryNames(session) {
		return BlockRegistry
	}
	return ""
}

func hexHash(h uint64) string { return strconv.FormatUint(h, 16) }

func (s *Settler) skipFile(reason string) {
	s.cfg.Obs.Reg.Add(obs.MSkippedFiles, map[string]string{"reason": reason}, 1)
}

// removeConversation runs the removal of one Conversation. The copier's lock is
// held. It returns whether the Conversation is now removed and, when it is not,
// the stable reason.
func (s *Settler) removeConversation(c *conv, now time.Time) (bool, string, error) {
	c.hm = map[hashKey]uint64{}
	defer func() { c.hm = nil }()
	fails, regen, err := s.check(c, true, now)
	if err != nil {
		return false, "", err
	}
	if len(fails) > 0 {
		_, err := s.hold(c, fails, now, false)
		return false, BlockHeld, err
	}
	if len(regen) > 0 {
		for _, f := range regen {
			if err := s.cfg.Copier.NewGenerationLocked(f, s.cfg.Copier.ReadOriginalPath(f), "full_hash_mismatch"); err != nil {
				return false, "", err
			}
		}
		return false, "full_hash_mismatch", nil
	}
	if s.cfg.Copier.CloneMode() {
		// A clone alone never stands in for the archive: compress it first.
		if _, err := s.settleBytes(c, now); err != nil {
			return false, "", err
		}
	} else if s.changedSinceSettle(c) {
		return false, BlockNewWrites, nil
	}
	pl, reason, err := s.listFiles(c)
	if err != nil || pl == nil {
		return false, reason, err
	}
	// The durability barrier: nothing leaves Claude's directories until the
	// archive and every index commit so far are on disk. A failure refuses the
	// removal and changes nothing.
	if err := s.durabilityBarrier(pl); err != nil {
		s.cfg.Obs.Log.Warn("removal_barrier_failed", "session", pl.Session, "err", err.Error())
		return false, BlockBarrier, nil
	}
	if err := s.commitPending(pl, now, true); err != nil {
		return false, "", err
	}
	faults.At("remove.after-pending")
	out, err := s.runPending(pl, now, false)
	if err != nil {
		return false, "", err
	}
	return out == "", out, nil
}

// listFiles builds the pending list after the full-hash comparison of every
// Claude file with the archive. It returns a nil list and a reason when the
// Conversation is not removable now.
func (s *Settler) listFiles(c *conv) (*pendingList, string, error) {
	pl := &pendingList{Session: c.session}
	files := append([]store.File(nil), c.files...)
	sort.Slice(files, func(i, j int) bool { return c.rel[files[i].ID] < c.rel[files[j].ID] })
	unavailable := false
	for _, f := range files {
		orig := s.cfg.Copier.ReadOriginalPath(f)
		if orig == "" {
			continue // nothing left in the Claude directory to remove
		}
		st, err := lstat(orig)
		if err != nil {
			continue
		}
		var gen int
		var size int64
		var want uint64
		if f.Mode == "snapshot" {
			sn, err := s.st.LatestSnapshot(f.ID)
			if err != nil {
				return nil, BlockNoArchive, nil
			}
			gen, size, want = sn.Version, sn.Size, sn.Hash
		} else {
			if f.CurGen == 0 {
				return nil, BlockNoArchive, nil
			}
			g, err := s.st.Generation(f.ID, f.CurGen)
			if err != nil {
				return nil, "", err
			}
			gen, size = g.Gen, g.CopyOffset
		}
		if st.Size != size {
			return nil, BlockNewWrites, nil
		}
		if s.st.ArchivedTo(f.ID, gen) < size {
			return nil, BlockNoArchive, nil
		}
		ptr, err := s.st.OpenPointer(f.ID, gen)
		if err != nil {
			return nil, "", err
		}
		ph, herr := hashReader(ptr, size)
		_ = ptr.Close()
		if herr != nil {
			return nil, "", herr
		}
		if f.Mode != "snapshot" {
			want = ph
		}
		oh, _, err := hashFile(orig)
		if err != nil {
			return nil, BlockNewWrites, nil
		}
		if oh != want || ph != want {
			if f.Mode != "snapshot" {
				if err := s.cfg.Copier.NewGenerationLocked(f, orig, "full_hash_mismatch"); err != nil {
					return nil, "", err
				}
			}
			return nil, "full_hash_mismatch", nil
		}
		if !s.quarantineAvailable(orig, st) {
			s.skipFile(obs.ReasonQuarantineUnavl)
			unavailable = true
			continue
		}
		pl.Files = append(pl.Files, pendingFile{
			File: f.ID, Rel: c.rel[f.ID], Path: orig, Dev: st.Dev, Ino: st.Ino,
			Size: size, MtimeNS: st.MtimeNS, Hash: hexHash(want), State: fsListed,
		})
	}
	if unavailable {
		return nil, BlockUnavailable, nil
	}
	if len(pl.Files) == 0 {
		return nil, BlockNoFiles, nil
	}
	return pl, "", nil
}

// quarantineAvailable reports whether orig can be quarantined: a regular file
// with one link, no symlink under its path, in a directory whose quarantine
// directory lies on the same device.
func (s *Settler) quarantineAvailable(orig string, st fileStat) bool {
	if !st.Mode.IsRegular() || st.Nlink != 1 {
		return false
	}
	if r, err := filepath.EvalSymlinks(orig); err != nil || r != orig {
		return false
	}
	q := filepath.Join(filepath.Dir(orig), scope.QuarantineDir)
	s.invalidateQuarantineDirs()
	if err := s.cfs().MkdirAll(q, 0o700); err != nil {
		return false
	}
	qs, err := lstat(q)
	if err != nil || qs.Dev != st.Dev || !qs.Mode.IsDir() {
		return false
	}
	return true
}

func (s *Settler) commitPending(pl *pendingList, now time.Time, full bool) error {
	b, err := json.Marshal(pl)
	if err != nil {
		return err
	}
	rec := &store.Rec{T: store.TStage, At: now.UnixNano(), Session: pl.Session, State: store.StageRemovalPending, Val: string(b)}
	if full {
		return s.st.CommitFull(rec)
	}
	return s.st.Commit(rec)
}

func qName(pf pendingFile) string {
	return fmt.Sprintf("%d-%s", pf.Ino, filepath.Base(pf.Path))
}

func qDir(pf pendingFile) string { return filepath.Join(filepath.Dir(pf.Path), scope.QuarantineDir) }

// findQuarantined scans the file's quarantine directory for an entry with its
// inode (the mapping a restart uses).
func (s *Settler) findQuarantined(pf pendingFile) string {
	ents, err := s.cfs().ReadDir(qDir(pf))
	if err != nil {
		return ""
	}
	for _, e := range ents {
		p := filepath.Join(qDir(pf), e.Name())
		if st, err := lstat(p); err == nil && st.Ino == pf.Ino && st.Dev == pf.Dev {
			return p
		}
	}
	return ""
}

// runPending carries the pending list through step 3 and commits "removed". It
// decides each file's step from the file system (a restart cannot trust the
// persisted per-file state), so one function serves the first run and recovery.
// It returns "" when the Conversation is removed, else the reason it went back
// to Quiet (or "restore_conflict" when it is held).
func (s *Settler) runPending(pl *pendingList, now time.Time, resume bool) (string, error) {
	cfs := s.cfs()
	for i := range pl.Files {
		pf := &pl.Files[i]
		st, statErr := lstat(pf.Path)
		atPath := statErr == nil && st.Ino == pf.Ino && st.Dev == pf.Dev
		q := s.findQuarantined(*pf)
		switch {
		case !atPath && q == "":
			pf.State = fsUnlinked
			continue
		case atPath:
			if why := s.holdsSession(pl.Session); why != "" {
				s.skipFile(why)
				return s.abortPending(pl, now, why)
			}
			if h := s.cfg.Hooks.BeforeQuarantine; h != nil {
				h(pl.Session, pf.Path)
				st, statErr = lstat(pf.Path)
			}
			if statErr != nil || st.Size != pf.Size || st.MtimeNS != pf.MtimeNS || st.Nlink != 1 {
				return s.abortPending(pl, now, BlockNewWrites)
			}
			if resume {
				if h, _, err := hashFile(pf.Path); err != nil || hexHash(h) != pf.Hash {
					return s.abortPending(pl, now, BlockNewWrites)
				}
			}
			q = filepath.Join(qDir(*pf), qName(*pf))
			if err := cfs.MkdirAll(qDir(*pf), 0o700); err != nil {
				return "", err
			}
			if err := cfs.Rename(pf.Path, q); err != nil {
				if errors.Is(err, syscallEXDEV) {
					s.skipFile(obs.ReasonQuarantineUnavl)
					return s.abortPending(pl, now, BlockUnavailable)
				}
				return "", err
			}
			if err := cfs.SyncDir(filepath.Dir(pf.Path)); err != nil {
				return "", err
			}
			s.cfg.Obs.Event(obs.EvQuarantined, "session", pl.Session, "path", pf.Path)
			faults.At("remove.after-rename")
			if h := s.cfg.Hooks.AfterQuarantine; h != nil {
				h(pl.Session, pf.Path)
			}
			pf.State, pf.Q = fsQuarantined, q
			if err := s.commitPending(pl, now, false); err != nil {
				return "", err
			}
			faults.At("remove.after-quarantined")
		default:
			// A quarantined file with no file at its path: the registry and live
			// check again, then the same holder listing and re-hash.
			if why := s.holdsSession(pl.Session); why != "" {
				s.skipFile(why)
				return s.abortPending(pl, now, why)
			}
			pf.State, pf.Q = fsQuarantined, q
		}
		// Holder listing.
		fsx.Mark(cfs, "holders")
		pids, herr := s.cfg.Holders(q)
		own := os.Getpid()
		held := herr != nil
		for _, p := range pids {
			if p != own {
				held = true
			}
		}
		if held {
			s.skipFile("holder_found")
			return s.abortPending(pl, now, "holder_found")
		}
		// Re-hash against the archive (a write through a descriptor opened before the
		// rename shows up as a changed size or hash).
		fsx.Mark(cfs, "rehash")
		before, _ := lstat(q)
		h, size, err := hashFile(q)
		if err != nil {
			return "", err
		}
		if hexHash(h) != pf.Hash || size != pf.Size {
			s.cfg.Obs.Reg.Add(obs.MQuarantineMismatch, nil, 1)
			s.skipFile("write_after_rename")
			return s.abortPending(pl, now, "write_after_rename")
		}
		if hk := s.cfg.Hooks.BeforeUnlink; hk != nil {
			hk(pl.Session, pf.Path)
		}
		if err := s.flushArchive(pl.Session); err != nil {
			return "", err
		}
		// The size and modification time once more, immediately before the unlink:
		// a write that lands here is lost to the archive and counted as a race.
		after, err := lstat(q)
		if err != nil {
			return "", err
		}
		if after.Size != before.Size || after.MtimeNS != before.MtimeNS {
			if after.Size > before.Size {
				s.cfg.Obs.Reg.Add(obs.MDroppedBytes, map[string]string{"reason": obs.ReasonRemovalRace}, float64(after.Size-before.Size))
			} else {
				s.cfg.Obs.Reg.Add(obs.MDroppedBytes, map[string]string{"reason": obs.ReasonRemovalRace}, 0)
			}
			s.cfg.Obs.Reg.Add(obs.MQuarantineMismatch, nil, 1)
			s.skipFile("write_after_rename")
			return s.abortPending(pl, now, "write_after_rename")
		}
		if err := cfs.Remove(q); err != nil {
			s.skipFile("unlink_failed")
			return "", err
		}
		if err := cfs.SyncDir(filepath.Dir(q)); err != nil {
			return "", err
		}
		faults.At("remove.after-unlink")
		pf.State, pf.Q = fsUnlinked, ""
		if err := s.commitPending(pl, now, false); err != nil {
			return "", err
		}
	}
	faults.At("remove.before-removed")
	s.tidy(pl)
	recs := []*store.Rec{{T: store.TStage, At: now.UnixNano(), Session: pl.Session, State: store.StageRemoved, Reason: "removed", Val: mustJSON(pl)}}
	for _, pf := range pl.Files {
		paths, err := s.st.CurrentPaths(pf.File)
		if err != nil {
			return "", err
		}
		for _, p := range paths {
			recs = append(recs, &store.Rec{T: store.TPathClose, At: now.UnixNano(), File: pf.File, Path: p})
		}
	}
	if err := s.st.CommitFull(recs...); err != nil {
		return "", err
	}
	for _, pf := range pl.Files {
		s.dirtyRecorded(pf.File)
	}
	faults.At("remove.after-removed")
	s.cfg.Obs.Reg.Add(obs.MRemovedTotal, nil, 1)
	s.cfg.Obs.Event(obs.EvRemoved, "session", pl.Session, "files", strconv.Itoa(len(pl.Files)))
	return "", nil
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// durabilityBarrier runs before any original is quarantined. listFiles has just
// read every archive segment back and proved it byte-equal (by hash and size) to
// its original; the barrier makes that archive durable (F_FULLFSYNC of every
// segment, the manifest and the directory) and then the index: the journal, a
// TRUNCATE checkpoint under synchronous=FULL with fullfsync, and a flush of
// index.db itself. The index runs at synchronous=NORMAL otherwise, so without
// this the "removal pending" decision could rest on commits that a power loss
// would roll back.
func (s *Settler) durabilityBarrier(pl *pendingList) error {
	if err := s.flushArchive(pl.Session); err != nil {
		return err
	}
	return s.st.Barrier()
}

// flushArchive flushes the archive that holds the Conversation's bytes (the
// segments and the manifest) with F_FULLFSYNC before any unlink.
func (s *Settler) flushArchive(session string) error {
	dir := s.st.SegDir(session)
	ents, err := s.cfs().ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range ents {
		if e.IsDir() || strings.HasSuffix(e.Name(), ".tmp") {
			continue
		}
		f, err := s.cfs().OpenFile(filepath.Join(dir, e.Name()), os.O_RDWR, 0)
		if err != nil {
			return err
		}
		err = fsx.FullSync(f)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
	}
	return fsx.FullSyncDir(s.cfs(), dir)
}

// restoreOne puts a quarantined file back with an exclusive rename (a hard link,
// then the quarantine name is dropped): a new file at the path is never
// overwritten.
func (s *Settler) restoreOne(pf pendingFile, q string) (conflict bool, err error) {
	if err := os.Link(q, pf.Path); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return true, nil
		}
		return false, err
	}
	if err := s.cfs().Remove(q); err != nil {
		return false, err
	}
	if err := s.cfs().SyncDir(filepath.Dir(pf.Path)); err != nil {
		return false, err
	}
	s.markRemovalGauges()
	s.cfg.Obs.Event(obs.EvQuarantineRestored, "session", pf.Rel, "path", pf.Path)
	return false, nil
}

// abortPending restores every quarantined file and returns the Conversation to
// Quiet (its copy continues), or holds it when a restore found a new file at the
// path.
func (s *Settler) abortPending(pl *pendingList, now time.Time, reason string) (string, error) {
	conflict := ""
	for _, pf := range pl.Files {
		q := s.findQuarantined(pf)
		if q == "" {
			continue
		}
		c, err := s.restoreOne(pf, q)
		if err != nil {
			return "", err
		}
		if c {
			s.skipFile("restore_conflict")
			conflict = fmt.Sprintf("%s: a new file appeared at %s; the original is kept at %s", pf.Rel, pf.Path, q)
		}
	}
	s.tidyQuarantine(pl)
	if conflict != "" {
		fails := []Failure{{File: pl.Files[0].File, Path: pl.Files[0].Rel, Reason: ReasonRestoreConflict, Detail: conflict}}
		d, _ := json.Marshal(fails)
		err := s.st.Commit(&store.Rec{T: store.TStage, At: now.UnixNano(), Session: pl.Session, State: StageHeld, Reason: ReasonRestoreConflict, Val: string(d)})
		s.cfg.Obs.Event(obs.EvSessionHeld, "session", pl.Session, "reason", ReasonRestoreConflict, "files", "1")
		return ReasonRestoreConflict, err
	}
	s.setReason(pl.Session, reason)
	err := s.st.Commit(&store.Rec{T: store.TStage, At: now.UnixNano(), Session: pl.Session, State: "", Reason: reason})
	return reason, err
}

// tidyQuarantine removes the quarantine directories of the list when empty.
func (s *Settler) tidyQuarantine(pl *pendingList) {
	for _, pf := range pl.Files {
		_ = os.Remove(qDir(pf)) // only succeeds when empty
	}
}

// tidy removes the quarantine, session and project directories a removal left
// empty, never above the Claude directory's projects/ root.
func (s *Settler) tidy(pl *pendingList) {
	s.tidyQuarantine(pl)
	root := filepath.Join(s.cfg.ClaudeDir, "projects")
	for _, pf := range pl.Files {
		for d := filepath.Dir(pf.Path); strings.HasPrefix(d, root+string(filepath.Separator)); d = filepath.Dir(d) {
			if err := os.Remove(d); err != nil { // fails while the directory is not empty
				break
			}
		}
	}
}

// recoverRemoval finishes or undoes every pending removal and logs the
// quarantine entries no pending list names. It runs at start, before the watcher.
func (s *Settler) recoverRemoval() error {
	rows, err := s.st.Stages()
	if err != nil {
		return err
	}
	now := s.cfg.Clock.Now()
	for _, r := range rows {
		if r.Stage != store.StageRemovalPending {
			continue
		}
		var pl pendingList
		if err := json.Unmarshal([]byte(r.Detail), &pl); err != nil {
			return fmt.Errorf("pending removal of %s: %w", r.Session, err)
		}
		var out string
		err := s.cfg.Copier.WithLock(func() error {
			var e error
			out, e = s.runPending(&pl, now, true)
			return e
		})
		if err != nil {
			return err
		}
		s.cfg.Obs.Event(obs.EvRemovalRecovered, "session", r.Session, "outcome", map[bool]string{true: "removed", false: "returned:" + out}[out == ""])
	}
	return s.logOrphans()
}

// quarantineRewalkTicks is how many ticks the gauge refresh reuses its list of
// quarantine directories before walking projects/ again, to pick up a
// directory created outside this process.
const quarantineRewalkTicks = 30

// gaugeQuarantineDirs returns the quarantine directories for the gauge refresh.
// It walks projects/ only on the first call, after a removal may have created a
// directory, and every quarantineRewalkTicks ticks.
func (s *Settler) gaugeQuarantineDirs() []string {
	s.qmu.Lock()
	defer s.qmu.Unlock()
	if s.qdirsValid && s.qdirsAge+1 < quarantineRewalkTicks {
		s.qdirsAge++
		return s.qdirs
	}
	walk := s.quarantineDirs
	if s.walkQuarantine != nil {
		walk = s.walkQuarantine
	}
	s.qdirs, s.qdirsValid, s.qdirsAge = walk(), true, 0
	return s.qdirs
}

// invalidateQuarantineDirs makes the next gauge refresh walk projects/ again.
func (s *Settler) invalidateQuarantineDirs() {
	s.qmu.Lock()
	s.qdirsValid = false
	s.qmu.Unlock()
}

// quarantineDirs lists every quarantine directory under projects/. It walks
// only the directories the scope allowlist descends into (a quarantine
// directory sits beside an ingested file, so it is always inside one), never
// the trees the recorder does not ingest, such as a project's spikes-* tree.
func (s *Settler) quarantineDirs() []string {
	var out []string
	root := filepath.Join(s.cfg.ClaudeDir, "projects")
	sc := scope.New(s.cfg.ClaudeDir, "")
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		if d.Name() == scope.QuarantineDir {
			out = append(out, p)
			return filepath.SkipDir
		}
		if p != root && !sc.DescendDir(p) {
			return filepath.SkipDir
		}
		return nil
	})
	return out
}

func (s *Settler) logOrphans() error {
	for _, d := range s.quarantineDirs() {
		ents, err := os.ReadDir(d)
		if err != nil {
			continue
		}
		for _, e := range ents {
			s.cfg.Obs.Event(obs.EvQuarantineOrphan, "path", filepath.Join(d, e.Name()))
		}
	}
	return nil
}

// rgInterval is how often the event-driven wake refreshes the removal gauges
// when nothing marked them stale.
const rgInterval = 5 * time.Minute

// markRemovalGauges notes that a removal, quarantine restore or doctor run
// changed what the removal gauges report; the next wake refreshes them.
func (s *Settler) markRemovalGauges() { s.rgDirty.Store(true) }

// refreshRemovalGaugesIfDue is the wake path's refresh: it runs only after a
// mark or when rgInterval has passed since the last refresh. Run lock held.
func (s *Settler) refreshRemovalGaugesIfDue(groups map[string]*conv, live map[string]bool, now time.Time) {
	if !s.rgDirty.Swap(false) && !s.rgAt.IsZero() && now.Sub(s.rgAt) < rgInterval {
		return
	}
	s.refreshRemovalGauges(groups, live, now)
	s.rgAt = now
}

// backlogFacts returns, for each of the given Conversations, the newest time
// the recorder saw one of its files change and the bytes it holds for them. It
// reads only the database, in two batched statements however many Conversations
// there are, and never touches an original file. The removal backlog gauges are
// approximate by design: the value stands in for newest(), which prefers the
// original's modification time. The accepted difference is that the original's
// mtime can be newer than the last-seen time (a change the recorder has not
// ingested yet) or, rarely, older (the recorder saw the file after it was last
// written), so a Conversation can count as ready slightly early or late. Remove
// keeps the exact check (blockers and newest per Conversation) and refuses a
// Conversation whose original changed recently, whatever this gauge says. On a
// read error the maps are empty and the gauge reports zero until the next refresh.
func (s *Settler) backlogFacts(cs []*conv) (newest map[string]time.Time, bytes map[string]int64) {
	newest, bytes = map[string]time.Time{}, map[string]int64{}
	var ids []int64
	for _, c := range cs {
		for _, f := range c.files {
			ids = append(ids, f.ID)
		}
	}
	if len(ids) == 0 {
		return
	}
	var seen, size map[int64]int64
	if err := s.st.ReadTx(func(rd *store.Reader) (err error) {
		if seen, err = rd.LastSeenByFile(ids); err != nil {
			return err
		}
		size, err = rd.FileBytesByID(ids)
		return err
	}); err != nil {
		return
	}
	for _, c := range cs {
		var best, total int64
		for _, f := range c.files {
			best = max(best, f.FirstSeen, seen[f.ID])
			total += size[f.ID]
		}
		newest[c.session] = time.Unix(0, best)
		bytes[c.session] = total
	}
	return
}

// refreshRemovalGauges sets the quarantine gauges and the removal backlog.
func (s *Settler) refreshRemovalGauges(groups map[string]*conv, live map[string]bool, now time.Time) {
	s.removalGaugeRefreshes.Add(1)
	reg := s.cfg.Obs.Reg
	var files int
	var bytes int64
	var oldest float64
	for _, d := range s.gaugeQuarantineDirs() {
		ents, err := os.ReadDir(d)
		if err != nil {
			continue
		}
		for _, e := range ents {
			fi, err := e.Info()
			if err != nil {
				continue
			}
			files++
			bytes += fi.Size()
			if age := now.Sub(fi.ModTime()).Seconds(); age > oldest {
				oldest = age
			}
		}
	}
	reg.Set(obs.MQuarantineFiles, nil, float64(files))
	reg.Set(obs.MQuarantineBytes, nil, float64(bytes))
	reg.Set(obs.MQuarantineOldestAge, nil, oldest)
	var convs int
	var back int64
	rows, _ := s.st.Stages()
	var settled []*conv
	for _, r := range rows {
		c := groups[r.Session]
		if r.Stage != StageSettled || c == nil || live[r.Session] {
			continue
		}
		settled = append(settled, c)
	}
	newest, size := s.backlogFacts(settled)
	defer func() { s.setRemovalDue(len(settled), convs, now) }()
	for _, c := range settled {
		if t, ok := newest[c.session]; !ok || now.Sub(t) < s.cfg.RemoveAfter {
			continue
		}
		convs++
		back += size[c.session]
	}
	reg.Set(obs.MRemovalBacklogConvs, nil, float64(convs))
	reg.Set(obs.MRemovalBacklogBytes, nil, float64(back))
}
