package unstick

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// Names inside a sweep work directory.
const (
	ExportFile      = "export.jsonl"
	ExportPostFile  = "export.post.jsonl"
	ReadyFile       = "ready.json"
	PrepareFile     = "prepare.json"
	ProgressFile    = "progress.txt"
	FollowupsFile   = "followups.txt"
	BatchesDir      = "batches"
	ResultsDir      = "results"
	FactsDir        = "facts"
	ProbesDir       = "probes"
	WorkDir         = "work"
	GateCheckFile   = "gate-check.json" // inside ProbesDir
	workdirPrefix   = "bead-unstick-"
	maxAllocAttempt = 10000
)

// Workdir is a freshly created sweep work directory.
type Workdir struct {
	// Path is the absolute directory path.
	Path string
}

// Join returns Path joined with elem.
func (w Workdir) Join(elem ...string) string {
	return filepath.Join(append([]string{w.Path}, elem...)...)
}

// AllocateWorkdir creates a FRESH <base>/bead-unstick-<YYYY-MM-DD>[-N]
// directory (date from now in UTC; N starts at 2) using an exclusive mkdir
// loop, so concurrent sweeps never share or reuse a directory. base must
// already exist. It then lays out the standard contents (see InitWorkdir).
func AllocateWorkdir(base string, now time.Time) (Workdir, error) {
	stem := filepath.Join(base, workdirPrefix+now.UTC().Format(dateFmt))
	for n := 1; n <= maxAllocAttempt; n++ {
		p := stem
		if n > 1 {
			p = fmt.Sprintf("%s-%d", stem, n)
		}
		err := os.Mkdir(p, 0o700)
		if err == nil {
			return initWorkdir(p, now)
		}
		if !errors.Is(err, fs.ErrExist) {
			return Workdir{}, fmt.Errorf("allocate workdir: %w", err)
		}
	}
	return Workdir{}, fmt.Errorf("allocate workdir: no free name under %s after %d attempts", stem, maxAllocAttempt)
}

// CreateWorkdir creates exactly path (parent must exist) with an exclusive
// mkdir: an existing path is an error, never reused. Used for --workdir.
func CreateWorkdir(path string, now time.Time) (Workdir, error) {
	if err := os.Mkdir(path, 0o700); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return Workdir{}, fmt.Errorf("workdir %s already exists; refusing to reuse it", path)
		}
		return Workdir{}, fmt.Errorf("create workdir: %w", err)
	}
	return initWorkdir(path, now)
}

// initWorkdir creates batches/ results/ facts/ probes/ work/, progress.txt
// (line 1 = start time, UTC Z-form) and an empty followups.txt.
func initWorkdir(path string, now time.Time) (Workdir, error) {
	w := Workdir{Path: path}
	for _, d := range []string{BatchesDir, ResultsDir, FactsDir, ProbesDir, WorkDir} {
		if err := os.Mkdir(w.Join(d), 0o700); err != nil {
			return Workdir{}, fmt.Errorf("init workdir: %w", err)
		}
	}
	if err := os.WriteFile(w.Join(ProgressFile), []byte(FormatTime(now)+"\n"), 0o600); err != nil {
		return Workdir{}, fmt.Errorf("init workdir: %w", err)
	}
	if err := os.WriteFile(w.Join(FollowupsFile), nil, 0o600); err != nil {
		return Workdir{}, fmt.Errorf("init workdir: %w", err)
	}
	return w, nil
}
