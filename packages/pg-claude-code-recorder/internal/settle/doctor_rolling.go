package settle

// The rolling doctor: the same comparison as doctorFull, cut into slices that
// each hash a bounded number of bytes of the copy, with the copier lock taken
// only to snapshot a file's committed prefix and to re-check before a repair.
// A cursor file in the state directory carries the position and the findings so
// far, so a restart or a CLI run loses nothing. A cycle ends when the cursor
// has passed the last file, the journal and the archives; doctor.json is
// written then and only then.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
)

// DoctorCursorFile is the name of the cursor file in the state directory.
const DoctorCursorFile = "doctor-cursor.json"

// Cycle phases.
const (
	phaseFiles    = "files"
	phaseArchives = "archives"
)

// minExpectedCycle is the floor of the expected cycle length that decides when
// the next cycle starts.
const minExpectedCycle = time.Hour

// defaultLagBound is the copy_lag_bytes above which a slice is deferred.
const defaultLagBound = 1 << 30

// DoctorCursor is the persisted state of the rolling doctor.
type DoctorCursor struct {
	// Active is true while a cycle is open.
	Active         bool   `json:"active"`
	CycleStartedNS int64  `json:"cycle_started_ns"`
	UpdatedNS      int64  `json:"updated_ns"`
	Phase          string `json:"phase,omitempty"`
	NextFileID     int64  `json:"next_file_id"`
	TotalFiles     int    `json:"total_files"`
	// Processed counts the files the cursor has passed (checked or skipped).
	Processed int   `json:"processed"`
	FilesDone int   `json:"files_done"`
	BytesDone int64 `json:"bytes_done"`
	// Retry lists files requeued once (a race or a reclaimed copy); Retried
	// lists those already requeued, which are dropped the second time.
	Retry   []int64 `json:"retry,omitempty"`
	Retried []int64 `json:"retried,omitempty"`
	// ArchiveAfter is the last session of the archive phase that was checked.
	ArchiveAfter string `json:"archive_after,omitempty"`
	ArchivesDone int    `json:"archives_done"`
	// Mismatches are the findings so far (with Repaired), so a restart loses none.
	Mismatches []DoctorMismatch `json:"mismatches,omitempty"`
	// The last completed cycle.
	LastEndNS         int64 `json:"last_end_ns,omitempty"`
	LastDurationNS    int64 `json:"last_duration_ns,omitempty"`
	LastMismatchFiles int   `json:"last_mismatch_files,omitempty"`
}

// DoctorCycleState describes a rolling cycle that was mid-way when a CLI doctor
// run took it over.
type DoctorCycleState struct {
	StartedNS  int64  `json:"started_ns"`
	Phase      string `json:"phase"`
	Processed  int    `json:"processed"`
	TotalFiles int    `json:"total_files"`
	Findings   int    `json:"findings"`
}

// DoctorAnswer is the doctor operation's answer: the result, and the state of
// the rolling cycle the run took over (nil when none was open).
type DoctorAnswer struct {
	DoctorResult
	TookOver *DoctorCycleState `json:"took_over,omitempty"`
}

// doctorSeam is the test hook of the slice loop.
type doctorSeam struct {
	// hashing is called, with no lock held, before a file's bytes are hashed.
	hashing func(file int64)
	// recheck is called before a finding is re-checked under the lock.
	recheck func(file int64)
}

// SetDoctorIdleStep sets the step an idle slice (a rolling tick with no cycle
// open and none due) runs, at most once per slice. Call it before the daemon's
// doctor loop starts.
func (s *Settler) SetDoctorIdleStep(fn func() error) { s.doctorIdle = fn }

func (s *Settler) doctorBudget() int64 { return s.cfg.DoctorBytesPerSlice }

// Doctor runs the full comparison and returns its result.
func (s *Settler) Doctor() (DoctorResult, error) {
	a, err := s.DoctorRun()
	return a.DoctorResult, err
}

// DoctorRun runs doctor to completion: the old synchronous comparison when
// bytes_per_slice is 0, else it takes the cursor and completes the open cycle
// (or a new one) slice by slice, with no lock held across files.
func (s *Settler) DoctorRun() (DoctorAnswer, error) { return s.DoctorRunCtx(s.detached()) }

// DoctorRunCtx is DoctorRun under ctx. The run itself is not cut by a cancelled
// ctx (the caller is a request that may disconnect), so ctx only carries values.
func (s *Settler) DoctorRunCtx(ctx context.Context) (DoctorAnswer, error) {
	ctx = context.WithoutCancel(ctx)
	if s.doctorBudget() <= 0 {
		res, err := s.doctorFull(ctx)
		return DoctorAnswer{DoctorResult: res}, err
	}
	s.doctorMu.Lock()
	defer s.doctorMu.Unlock()
	cur := s.loadCursor()
	var ans DoctorAnswer
	if cur.Active {
		ans.TookOver = &DoctorCycleState{StartedNS: cur.CycleStartedNS, Phase: cur.Phase, Processed: cur.Processed, TotalFiles: cur.TotalFiles, Findings: len(cur.Mismatches)}
	} else if err := s.startCycle(&cur); err != nil {
		return ans, err
	}
	for {
		finished, res, err := s.runSlice(ctx, &cur, s.doctorBudget())
		if err != nil {
			return ans, err
		}
		if finished {
			ans.DoctorResult = res
			return ans, nil
		}
	}
}

// DoctorTick is one wake of the rolling loop: start a cycle when one is due,
// run one slice of the open cycle (deferred while the copier is behind), or,
// with nothing to do, run the idle step. It does nothing while a CLI run holds
// the cursor, and when bytes_per_slice is 0.
func (s *Settler) DoctorTick() error {
	ctx := s.detached()
	if s.stopping() {
		return nil
	}
	if s.doctorBudget() <= 0 || !s.doctorMu.TryLock() {
		return nil
	}
	defer s.doctorMu.Unlock()
	now := s.cfg.Clock.Now()
	cur := s.loadCursor()
	if !cur.Active {
		s.cfg.Obs.Reg.Set(obs.MDoctorCursorAge, nil, 0)
		if !s.cycleDue(cur, now) {
			if s.doctorIdle != nil {
				return s.doctorIdle()
			}
			return nil
		}
	}
	if lag, ok := s.cfg.Obs.Reg.Gauge(obs.MCopyLagBytes, nil); ok && int64(lag) > s.lagBound() {
		s.cfg.Obs.Reg.Add(obs.MDoctorSlices, map[string]string{"outcome": "deferred"}, 1)
		s.setCursorAge(cur, now)
		return nil
	}
	if !cur.Active {
		if err := s.startCycle(&cur); err != nil {
			return err
		}
	}
	_, _, err := s.runSlice(ctx, &cur, s.doctorBudget())
	return err
}

func (s *Settler) lagBound() int64 {
	if s.cfg.DoctorLagBound > 0 {
		return s.cfg.DoctorLagBound
	}
	return defaultLagBound
}

func (s *Settler) setCursorAge(cur DoctorCursor, now time.Time) {
	age := 0.0
	if cur.Active && cur.UpdatedNS > 0 {
		age = now.Sub(time.Unix(0, cur.UpdatedNS)).Seconds()
	}
	s.cfg.Obs.Reg.Set(obs.MDoctorCursorAge, nil, age)
}

// cycleDue reports whether a new cycle starts: the last completed one is older
// than doctor.interval minus the expected cycle length (the previous cycle's
// duration, one hour at least). A store that never completed a cycle is due.
func (s *Settler) cycleDue(cur DoctorCursor, now time.Time) bool {
	interval := s.cfg.DoctorInterval
	if interval <= 0 {
		return false
	}
	last := cur.LastEndNS
	if last == 0 {
		if r, ok := LoadDoctor(s.st.Dir()); ok {
			last = r.AtNS
		}
	}
	if last == 0 {
		return true
	}
	expected := time.Duration(cur.LastDurationNS)
	if expected < minExpectedCycle {
		expected = minExpectedCycle
	}
	return now.Sub(time.Unix(0, last)) >= interval-expected
}

func (s *Settler) cursorPath() string { return filepath.Join(s.st.Dir(), DoctorCursorFile) }

func (s *Settler) loadCursor() DoctorCursor {
	var c DoctorCursor
	b, err := os.ReadFile(s.cursorPath())
	if err != nil || json.Unmarshal(b, &c) != nil {
		return DoctorCursor{}
	}
	return c
}

func (s *Settler) saveCursor(c *DoctorCursor) error {
	c.UpdatedNS = s.cfg.Clock.Now().UnixNano()
	b, err := json.Marshal(*c)
	if err != nil {
		return err
	}
	tmp := s.cursorPath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.cursorPath())
}

// startCycle opens a cycle over the files that exist now (a file created
// during it is covered by the next cycle) and refreshes the projection gauge.
func (s *Settler) startCycle(cur *DoctorCursor) error {
	files, err := s.st.AllFiles()
	if err != nil {
		return err
	}
	*cur = DoctorCursor{
		Active: true, CycleStartedNS: s.cfg.Clock.Now().UnixNano(), Phase: phaseFiles, TotalFiles: len(files),
		LastEndNS: cur.LastEndNS, LastDurationNS: cur.LastDurationNS, LastMismatchFiles: cur.LastMismatchFiles,
	}
	if _, _, err := s.DoctorProjection(); err != nil {
		return err
	}
	s.cfg.Obs.Reg.Set(obs.MDoctorCycleProgress, nil, 0)
	return s.saveCursor(cur)
}

// DoctorProjection returns the copy bytes of the file set and the cycle length
// they project to: bytes over the slice budget (rounded up), times the slice
// interval. It sets doctor_projected_cycle_seconds. The budget counts bytes of
// one side, the copy.
func (s *Settler) DoctorProjection() (int64, time.Duration, error) {
	files, err := s.st.AllFiles()
	if err != nil {
		return 0, 0, err
	}
	var total int64
	for _, f := range files {
		switch f.Mode {
		case "append":
			if f.CurGen == 0 {
				continue
			}
			if g, err := s.st.Generation(f.ID, f.CurGen); err == nil {
				total += g.CopyOffset
			}
		case "snapshot":
			if sn, err := s.st.LatestSnapshot(f.ID); err == nil {
				total += sn.Size
			}
		}
	}
	var projected time.Duration
	if b := s.doctorBudget(); b > 0 {
		slices := (total + b - 1) / b
		if slices < 1 {
			slices = 1
		}
		projected = time.Duration(slices) * s.cfg.DoctorSliceEvery
	}
	s.cfg.Obs.Reg.Set(obs.MDoctorProjectedCycle, nil, projected.Seconds())
	return total, projected, nil
}

func countBad(ms []DoctorMismatch) int {
	bad := map[int64]bool{}
	for i, m := range ms {
		if IsNotice(m.Kind) {
			continue
		}
		if m.File != 0 {
			bad[m.File] = true
		} else {
			bad[-int64(i+1)] = true
		}
	}
	return len(bad)
}

func hasID(ids []int64, id int64) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}

// runSlice runs one slice of the open cycle against budget bytes of the copy
// and saves the cursor. finished is true, with the result, when the slice
// completed the cycle.
func (s *Settler) runSlice(ctx context.Context, cur *DoctorCursor, budget int64) (finished bool, res DoctorResult, err error) {
	reg := s.cfg.Obs.Reg
	start := s.cfg.Clock.Now()
	defer func() {
		outcome := "ok"
		if err != nil {
			outcome = "error"
		}
		reg.Add(obs.MDoctorSlices, map[string]string{"outcome": outcome}, 1)
		reg.Observe(obs.MDoctorSliceDuration, s.cfg.Clock.Now().Sub(start).Seconds())
	}()
	add := func(m DoctorMismatch) { cur.Mismatches = append(cur.Mismatches, m) }
	spent := int64(0)
	if cur.Phase == phaseFiles {
		files, ferr := s.st.AllFiles()
		if ferr != nil {
			return false, res, ferr
		}
		for {
			f, retried, ok := nextDoctorFile(cur, files)
			if !ok {
				break
			}
			if (spent > 0 && spent >= budget) || s.stopping() {
				return false, res, s.sliceEnd(cur)
			}
			n, perr := s.doctorOne(ctx, cur, f, retried, add)
			if perr != nil {
				_ = s.sliceEnd(cur)
				return false, res, perr
			}
			spent += n
			cur.BytesDone += n
			if retried {
				cur.Retry = cur.Retry[1:]
			} else {
				cur.NextFileID = f.ID + 1
				cur.Processed++
			}
		}
		// The journal check runs once per cycle; the archives follow.
		s.doctorJournal(ctx, add)
		cur.Phase = phaseArchives
	}
	sessions, serr := s.settledSessions()
	if serr != nil {
		_ = s.sliceEnd(cur)
		return false, res, serr
	}
	for _, session := range sessions {
		if session <= cur.ArchiveAfter {
			continue
		}
		if (spent > 0 && spent >= budget) || s.stopping() {
			return false, res, s.sliceEnd(cur)
		}
		checked, n := s.doctorArchive(session, add)
		if checked {
			cur.ArchivesDone++
		}
		spent += n
		cur.ArchiveAfter = session
	}
	res, err = s.finishCycle(cur)
	return err == nil, res, err
}

// sliceEnd saves the cursor and updates the per-slice gauges.
func (s *Settler) sliceEnd(cur *DoctorCursor) error {
	reg := s.cfg.Obs.Reg
	bad := float64(countBad(cur.Mismatches))
	if prev := float64(cur.LastMismatchFiles); prev > bad {
		bad = prev // a finding already alerting does not clear until the cycle ends
	}
	reg.Set(obs.MDoctorMismatchFiles, nil, bad)
	if cur.TotalFiles > 0 {
		p := float64(cur.Processed) / float64(cur.TotalFiles)
		if p > 1 {
			p = 1
		}
		reg.Set(obs.MDoctorCycleProgress, nil, p)
	}
	err := s.saveCursor(cur)
	s.setCursorAge(*cur, s.cfg.Clock.Now())
	return err
}

// finishCycle writes doctor.json, sets the gauges and closes the cycle.
func (s *Settler) finishCycle(cur *DoctorCursor) (DoctorResult, error) {
	end := s.cfg.Clock.Now()
	crit, notices := splitNotices(cur.Mismatches)
	res := DoctorResult{
		AtNS: end.UnixNano(), FilesChecked: cur.FilesDone, ArchivesCheck: cur.ArchivesDone,
		MismatchFiles: countBad(crit), Mismatches: crit, Notices: notices,
	}
	sort.SliceStable(res.Mismatches, func(i, j int) bool { return res.Mismatches[i].File < res.Mismatches[j].File })
	if err := s.writeDoctor(res); err != nil {
		return res, err
	}
	dur := end.Sub(time.Unix(0, cur.CycleStartedNS))
	reg := s.cfg.Obs.Reg
	reg.Set(obs.MDoctorLastRun, nil, float64(end.UnixNano())/1e9)
	reg.Set(obs.MDoctorMismatchFiles, nil, float64(res.MismatchFiles))
	reg.Set(obs.MDoctorCycleDuration, nil, dur.Seconds())
	reg.Set(obs.MDoctorCycleProgress, nil, 1)
	s.cfg.Obs.Event(obs.EvDoctorDone, "files", fmt.Sprint(res.FilesChecked), "mismatch_files", fmt.Sprint(res.MismatchFiles))
	*cur = DoctorCursor{LastEndNS: end.UnixNano(), LastDurationNS: int64(dur), LastMismatchFiles: res.MismatchFiles}
	err := s.saveCursor(cur)
	s.setCursorAge(*cur, end)
	return res, err
}

// nextDoctorFile returns the next file of the cycle: the files from the cursor
// in id order, then the requeued ones.
func nextDoctorFile(cur *DoctorCursor, files []store.File) (f store.File, retried, ok bool) {
	for _, x := range files {
		if x.ID >= cur.NextFileID {
			return x, false, true
		}
	}
	for len(cur.Retry) > 0 {
		for _, x := range files {
			if x.ID == cur.Retry[0] {
				return x, true, true
			}
		}
		cur.Retry = cur.Retry[1:] // the file is gone
	}
	return store.File{}, false, false
}

// requeue puts f back once; the second time it is dropped.
func (cur *DoctorCursor) requeue(id int64, retried bool) {
	if retried || hasID(cur.Retried, id) {
		return
	}
	cur.Retried = append(cur.Retried, id)
	cur.Retry = append(cur.Retry, id)
}

// doctorOne checks one file and returns the copy bytes it hashed.
func (s *Settler) doctorOne(ctx context.Context, cur *DoctorCursor, f store.File, retried bool, add func(DoctorMismatch)) (int64, error) {
	reg := s.cfg.Obs.Reg
	race := func() {
		reg.Add(obs.MDoctorRaceSkips, nil, 1)
		cur.requeue(f.ID, retried)
	}
	switch f.Mode {
	case "append":
		var (
			fc   store.File
			g    store.Generation
			orig string
		)
		err := s.cfg.Copier.WithLock(func() error {
			var err error
			if fc, err = s.st.File(f.ID); err != nil || fc.CurGen == 0 {
				return err
			}
			if orig = s.cfg.Copier.ReadOriginalPath(fc); orig == "" {
				return nil
			}
			g, err = s.st.Generation(fc.ID, fc.CurGen)
			return err
		})
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return 0, nil
			}
			return 0, err
		}
		if fc.CurGen == 0 || orig == "" {
			return 0, nil
		}
		if s.doctorSeam.hashing != nil {
			s.doctorSeam.hashing(f.ID)
		}
		cur.FilesDone++
		ok, detail, err := s.prefixMatches(fc, g, orig)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) || errors.Is(err, fs.ErrNotExist) {
				reg.Add(obs.MDoctorSlices, map[string]string{"outcome": "skipped_gone"}, 1)
				cur.requeue(f.ID, retried)
				return 0, nil
			}
			return 0, err
		}
		if ok {
			return g.CopyOffset, nil
		}
		if s.doctorSeam.recheck != nil {
			s.doctorSeam.recheck(f.ID)
		}
		err = s.cfg.Copier.WithLock(func() error {
			f2, err := s.st.File(f.ID)
			if err != nil {
				return err
			}
			g2, err := s.st.Generation(f2.ID, f2.CurGen)
			if err != nil {
				return err
			}
			if f2.CurGen != g.Gen || g2.CopyOffset != g.CopyOffset {
				race()
				return nil
			}
			// The mismatch is always re-hashed under the lock before a repair.
			ok2, detail2, err := s.prefixMatches(f2, g2, orig)
			if err != nil {
				return err
			}
			if ok2 {
				race()
				return nil
			}
			m := DoctorMismatch{Kind: MismatchCopy, File: f.ID, Path: orig, Detail: detail2}
			if detail2 == "" {
				m.Detail = detail
			}
			if err := s.cfg.Copier.NewGenerationLocked(f2, orig, "full_hash_mismatch"); err != nil {
				m.Detail += "; new generation failed: " + err.Error()
			} else {
				m.Repaired = true
			}
			add(m)
			return nil
		})
		return g.CopyOffset, err
	case "snapshot":
		var (
			sn   store.Snapshot
			have bool
			orig string
		)
		err := s.cfg.Copier.WithLock(func() error {
			var err error
			if sn, err = s.st.LatestSnapshot(f.ID); err != nil {
				return nil // no snapshot yet
			}
			have = true
			orig = s.cfg.Copier.ReadOriginalPath(f)
			return nil
		})
		if err != nil || !have || orig == "" {
			return 0, err
		}
		if s.doctorSeam.hashing != nil {
			s.doctorSeam.hashing(f.ID)
		}
		cur.FilesDone++
		h, size, err := hashFile(orig)
		if err != nil {
			return sn.Size, nil
		}
		if (h != sn.Hash || size != sn.Size) && !s.lastSeenFromStore(ctx, f).Before(s.mtimeOf(orig)) {
			if s.doctorSeam.recheck != nil {
				s.doctorSeam.recheck(f.ID)
			}
			err = s.cfg.Copier.WithLock(func() error {
				sn2, err := s.st.LatestSnapshot(f.ID)
				if err != nil || sn2.Hash != sn.Hash || sn2.Size != sn.Size {
					race()
					return nil
				}
				add(DoctorMismatch{Kind: MismatchSnapshot, File: f.ID, Path: orig, Detail: fmt.Sprintf("stored %d bytes, original %d", sn.Size, size)})
				return nil
			})
		}
		return sn.Size, err
	}
	return 0, nil
}
