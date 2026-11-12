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
	"sync"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/clock"
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
	// lastSeenBefore and lastSeenAfter bracket the doctor's read of a file's
	// last-seen time (see Doctor.lastSeen); nil costs one comparison.
	lastSeenBefore func()
	lastSeenAfter  func()
}

// DoctorCopier is the part of the copier the doctor uses.
type DoctorCopier interface {
	WithLock(fn func() error) error
	ReadOriginalPath(f store.File) string
	NewGenerationLocked(f store.File, path, reason string) error
	RestoreSnapshot(file int64, version int, data []byte) error
}

// DoctorDeps are the doctor's dependencies; NewDoctor requires Store, Copier,
// Clock, Obs and Mtime.
type DoctorDeps struct {
	Store  *store.Store
	Copier DoctorCopier
	Clock  clock.Clock
	Obs    *obs.Obs
	// ClaudeDir is the Claude directory (manifest paths are relative to it).
	ClaudeDir string
	// Mtime reads a file's modification time.
	Mtime func(path string) (time.Time, error)
	// Interval is doctor.interval, BytesPerSlice doctor.bytes_per_slice (0 selects
	// the old synchronous run), SliceEvery doctor.slice_every, LagBound the
	// copy_lag_bytes above which a slice is deferred (1 GiB when 0).
	Interval      time.Duration
	BytesPerSlice int64
	SliceEvery    time.Duration
	LagBound      int64
	// Stopping reports that the daemon is shutting down (never stopping when nil).
	Stopping func() bool
	// OnFullRun runs after a synchronous (bytes_per_slice = 0) run wrote its
	// result; the settler marks its removal gauges stale there. Nil does nothing.
	OnFullRun func()
}

// Doctor is the doctor job: the synchronous comparison and the rolling cycle.
// It owns the cursor's single flight (mu) and takes no run lock; the settler
// forwards its operations.
type Doctor struct {
	st            *store.Store
	copier        DoctorCopier
	clock         clock.Clock
	obs           *obs.Obs
	claudeDir     string
	mtime         func(path string) (time.Time, error)
	interval      time.Duration
	bytesPerSlice int64
	sliceEvery    time.Duration
	lagLimit      int64
	stopFn        func() bool
	onFullRun     func()

	// mu is the single flight of the doctor cursor: a rolling slice and a CLI
	// doctor run never overlap.
	mu   sync.Mutex
	idle func() error
	seam doctorSeam
}

// NewDoctor returns a Doctor over deps.
func NewDoctor(deps DoctorDeps) *Doctor {
	return &Doctor{
		st: deps.Store, copier: deps.Copier, clock: deps.Clock, obs: deps.Obs,
		claudeDir: deps.ClaudeDir, mtime: deps.Mtime, interval: deps.Interval,
		bytesPerSlice: deps.BytesPerSlice, sliceEvery: deps.SliceEvery, lagLimit: deps.LagBound,
		stopFn: deps.Stopping, onFullRun: deps.OnFullRun,
	}
}

func (d *Doctor) stopping() bool { return d.stopFn != nil && d.stopFn() }

// lastSeen is the file's last-seen time, read straight from the store. It MUST
// NOT go through the Settler's per-tick cache (Settler.seen): the doctor holds
// no run lock, so that cache belongs to Tick alone. The seam hooks only let a
// test hold the read open; they take no lock.
func (d *Doctor) lastSeen(ctx context.Context, f store.File) time.Time {
	if d.seam.lastSeenBefore != nil {
		d.seam.lastSeenBefore()
	}
	t := lastSeenFromStore(ctx, d.st, f)
	if d.seam.lastSeenAfter != nil {
		d.seam.lastSeenAfter()
	}
	return t
}

// SetIdleStep sets the step an idle slice (a rolling tick with no cycle open
// and none due) runs, at most once per slice. Call it before the daemon's
// doctor loop starts.
func (d *Doctor) SetIdleStep(fn func() error) { d.idle = fn }

func (d *Doctor) doctorBudget() int64 { return d.bytesPerSlice }

// Run runs doctor to completion: the old synchronous comparison when
// bytes_per_slice is 0, else it takes the cursor and completes the open cycle
// (or a new one) slice by slice, with no lock held across files. The run itself
// is not cut by a cancelled ctx (the caller is a request that may disconnect),
// so ctx only carries values.
func (d *Doctor) Run(ctx context.Context) (DoctorAnswer, error) {
	ctx = context.WithoutCancel(ctx)
	if d.doctorBudget() <= 0 {
		res, err := d.doctorFull(ctx)
		return DoctorAnswer{DoctorResult: res}, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	cur := d.loadCursor()
	var ans DoctorAnswer
	if cur.Active {
		ans.TookOver = &DoctorCycleState{StartedNS: cur.CycleStartedNS, Phase: cur.Phase, Processed: cur.Processed, TotalFiles: cur.TotalFiles, Findings: len(cur.Mismatches)}
	} else if err := d.startCycle(&cur); err != nil {
		return ans, err
	}
	for {
		finished, res, err := d.runSlice(ctx, &cur, d.doctorBudget())
		if err != nil {
			return ans, err
		}
		if finished {
			ans.DoctorResult = res
			return ans, nil
		}
	}
}

// Tick is one wake of the rolling loop: start a cycle when one is due,
// run one slice of the open cycle (deferred while the copier is behind), or,
// with nothing to do, run the idle step. It does nothing while a CLI run holds
// the cursor, and when bytes_per_slice is 0.
func (d *Doctor) Tick(ctx context.Context) error {
	if d.stopping() {
		return nil
	}
	if d.doctorBudget() <= 0 || !d.mu.TryLock() {
		return nil
	}
	defer d.mu.Unlock()
	now := d.clock.Now()
	cur := d.loadCursor()
	if !cur.Active {
		d.obs.Reg.Set(obs.MDoctorCursorAge, nil, 0)
		if !d.cycleDue(cur, now) {
			if d.idle != nil {
				return d.idle()
			}
			return nil
		}
	}
	if lag, ok := d.obs.Reg.Gauge(obs.MCopyLagBytes, nil); ok && int64(lag) > d.lagBound() {
		d.obs.Reg.Add(obs.MDoctorSlices, map[string]string{"outcome": "deferred"}, 1)
		d.setCursorAge(cur, now)
		return nil
	}
	if !cur.Active {
		if err := d.startCycle(&cur); err != nil {
			return err
		}
	}
	_, _, err := d.runSlice(ctx, &cur, d.doctorBudget())
	return err
}

func (d *Doctor) lagBound() int64 {
	if d.lagLimit > 0 {
		return d.lagLimit
	}
	return defaultLagBound
}

func (d *Doctor) setCursorAge(cur DoctorCursor, now time.Time) {
	age := 0.0
	if cur.Active && cur.UpdatedNS > 0 {
		age = now.Sub(time.Unix(0, cur.UpdatedNS)).Seconds()
	}
	d.obs.Reg.Set(obs.MDoctorCursorAge, nil, age)
}

// cycleDue reports whether a new cycle starts: the last completed one is older
// than doctor.interval minus the expected cycle length (the previous cycle's
// duration, one hour at least). A store that never completed a cycle is due.
func (d *Doctor) cycleDue(cur DoctorCursor, now time.Time) bool {
	interval := d.interval
	last := cur.LastEndNS
	if last == 0 && interval > 0 {
		if r, ok := LoadDoctor(d.st.Dir()); ok {
			last = r.AtNS
		}
	}
	return cycleDueAt(interval, last, cur.LastDurationNS, now)
}

func (d *Doctor) cursorPath() string { return filepath.Join(d.st.Dir(), DoctorCursorFile) }

func (d *Doctor) loadCursor() DoctorCursor {
	var c DoctorCursor
	b, err := os.ReadFile(d.cursorPath())
	if err != nil || json.Unmarshal(b, &c) != nil {
		return DoctorCursor{}
	}
	return c
}

func (d *Doctor) saveCursor(c *DoctorCursor) error {
	c.UpdatedNS = d.clock.Now().UnixNano()
	b, err := json.Marshal(*c)
	if err != nil {
		return err
	}
	tmp := d.cursorPath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, d.cursorPath())
}

// startCycle opens a cycle over the files that exist now (a file created
// during it is covered by the next cycle) and refreshes the projection gauge.
func (d *Doctor) startCycle(cur *DoctorCursor) error {
	files, err := d.st.AllFiles()
	if err != nil {
		return err
	}
	*cur = DoctorCursor{
		Active: true, CycleStartedNS: d.clock.Now().UnixNano(), Phase: phaseFiles, TotalFiles: len(files),
		LastEndNS: cur.LastEndNS, LastDurationNS: cur.LastDurationNS, LastMismatchFiles: cur.LastMismatchFiles,
	}
	if _, _, err := d.Projection(); err != nil {
		return err
	}
	d.obs.Reg.Set(obs.MDoctorCycleProgress, nil, 0)
	return d.saveCursor(cur)
}

// Projection returns the copy bytes of the file set and the cycle length
// they project to: bytes over the slice budget (rounded up), times the slice
// interval. It sets doctor_projected_cycle_seconds. The budget counts bytes of
// one side, the copy.
func (d *Doctor) Projection() (int64, time.Duration, error) {
	files, err := d.st.AllFiles()
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
			if g, err := d.st.Generation(f.ID, f.CurGen); err == nil {
				total += g.CopyOffset
			}
		case "snapshot":
			if sn, err := d.st.LatestSnapshot(f.ID); err == nil {
				total += sn.Size
			}
		}
	}
	var projected time.Duration
	if b := d.doctorBudget(); b > 0 {
		slices := (total + b - 1) / b
		if slices < 1 {
			slices = 1
		}
		projected = time.Duration(slices) * d.sliceEvery
	}
	d.obs.Reg.Set(obs.MDoctorProjectedCycle, nil, projected.Seconds())
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
func (d *Doctor) runSlice(ctx context.Context, cur *DoctorCursor, budget int64) (finished bool, res DoctorResult, err error) {
	reg := d.obs.Reg
	start := d.clock.Now()
	defer func() {
		outcome := "ok"
		if err != nil {
			outcome = "error"
		}
		reg.Add(obs.MDoctorSlices, map[string]string{"outcome": outcome}, 1)
		reg.Observe(obs.MDoctorSliceDuration, d.clock.Now().Sub(start).Seconds())
	}()
	add := func(m DoctorMismatch) { cur.Mismatches = append(cur.Mismatches, m) }
	spent := int64(0)
	if cur.Phase == phaseFiles {
		files, ferr := d.st.AllFiles()
		if ferr != nil {
			return false, res, ferr
		}
		for {
			f, retried, ok := nextDoctorFile(cur, files)
			if !ok {
				break
			}
			if (spent > 0 && spent >= budget) || d.stopping() {
				return false, res, d.sliceEnd(cur)
			}
			n, perr := d.doctorOne(ctx, cur, f, retried, add)
			if perr != nil {
				_ = d.sliceEnd(cur)
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
		d.doctorJournal(ctx, add)
		cur.Phase = phaseArchives
	}
	sessions, serr := d.settledSessions()
	if serr != nil {
		_ = d.sliceEnd(cur)
		return false, res, serr
	}
	for _, session := range sessions {
		if session <= cur.ArchiveAfter {
			continue
		}
		if (spent > 0 && spent >= budget) || d.stopping() {
			return false, res, d.sliceEnd(cur)
		}
		checked, n := d.doctorArchive(session, add)
		if checked {
			cur.ArchivesDone++
		}
		spent += n
		cur.ArchiveAfter = session
	}
	res, err = d.finishCycle(cur)
	return err == nil, res, err
}

// sliceEnd saves the cursor and updates the per-slice gauges.
func (d *Doctor) sliceEnd(cur *DoctorCursor) error {
	reg := d.obs.Reg
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
	err := d.saveCursor(cur)
	d.setCursorAge(*cur, d.clock.Now())
	return err
}

// finishCycle writes doctor.json, sets the gauges and closes the cycle.
func (d *Doctor) finishCycle(cur *DoctorCursor) (DoctorResult, error) {
	end := d.clock.Now()
	crit, notices := splitNotices(cur.Mismatches)
	res := DoctorResult{
		AtNS: end.UnixNano(), FilesChecked: cur.FilesDone, ArchivesCheck: cur.ArchivesDone,
		MismatchFiles: countBad(crit), Mismatches: crit, Notices: notices,
	}
	sort.SliceStable(res.Mismatches, func(i, j int) bool { return res.Mismatches[i].File < res.Mismatches[j].File })
	if err := d.writeDoctor(res); err != nil {
		return res, err
	}
	dur := end.Sub(time.Unix(0, cur.CycleStartedNS))
	reg := d.obs.Reg
	reg.Set(obs.MDoctorLastRun, nil, float64(end.UnixNano())/1e9)
	reg.Set(obs.MDoctorMismatchFiles, nil, float64(res.MismatchFiles))
	reg.Set(obs.MDoctorCycleDuration, nil, dur.Seconds())
	reg.Set(obs.MDoctorCycleProgress, nil, 1)
	d.obs.Event(obs.EvDoctorDone, "files", fmt.Sprint(res.FilesChecked), "mismatch_files", fmt.Sprint(res.MismatchFiles))
	*cur = DoctorCursor{LastEndNS: end.UnixNano(), LastDurationNS: int64(dur), LastMismatchFiles: res.MismatchFiles}
	err := d.saveCursor(cur)
	d.setCursorAge(*cur, end)
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
func (d *Doctor) doctorOne(ctx context.Context, cur *DoctorCursor, f store.File, retried bool, add func(DoctorMismatch)) (int64, error) {
	reg := d.obs.Reg
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
		err := d.copier.WithLock(func() error {
			var err error
			if fc, err = d.st.File(f.ID); err != nil || fc.CurGen == 0 {
				return err
			}
			if orig = d.copier.ReadOriginalPath(fc); orig == "" {
				return nil
			}
			g, err = d.st.Generation(fc.ID, fc.CurGen)
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
		if d.seam.hashing != nil {
			d.seam.hashing(f.ID)
		}
		cur.FilesDone++
		ok, detail, err := d.prefixMatches(fc, g, orig)
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
		if d.seam.recheck != nil {
			d.seam.recheck(f.ID)
		}
		err = d.copier.WithLock(func() error {
			f2, err := d.st.File(f.ID)
			if err != nil {
				return err
			}
			g2, err := d.st.Generation(f2.ID, f2.CurGen)
			if err != nil {
				return err
			}
			if f2.CurGen != g.Gen || g2.CopyOffset != g.CopyOffset {
				race()
				return nil
			}
			// The mismatch is always re-hashed under the lock before a repair.
			ok2, detail2, err := d.prefixMatches(f2, g2, orig)
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
			if err := d.copier.NewGenerationLocked(f2, orig, "full_hash_mismatch"); err != nil {
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
		err := d.copier.WithLock(func() error {
			var err error
			if sn, err = d.st.LatestSnapshot(f.ID); err != nil {
				return nil // no snapshot yet
			}
			have = true
			orig = d.copier.ReadOriginalPath(f)
			return nil
		})
		if err != nil || !have || orig == "" {
			return 0, err
		}
		if d.seam.hashing != nil {
			d.seam.hashing(f.ID)
		}
		cur.FilesDone++
		h, size, err := hashFile(orig)
		if err != nil {
			return sn.Size, nil
		}
		if (h != sn.Hash || size != sn.Size) && !d.lastSeen(ctx, f).Before(d.mtimeOf(orig)) {
			if d.seam.recheck != nil {
				d.seam.recheck(f.ID)
			}
			err = d.copier.WithLock(func() error {
				sn2, err := d.st.LatestSnapshot(f.ID)
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
