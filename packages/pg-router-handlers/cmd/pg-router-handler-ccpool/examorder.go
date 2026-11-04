package main

import (
	"encoding/json"
	"log/slog"
	"os"
	"sort"
	"time"
)

// Last-examined record (design D5). worktree_sweep stops after
// worktreeSweepMaxExamined git examinations, so a run of kept worktrees that
// sort first by name would starve a removable one behind them. Each candidate
// the sweep examined with git gets one small file beside the keep cache, one per
// worktree and shared by every role (not a cursor per pass or per role):
//
//	<lock dir>/worktree-exam--<bead>.exam
//
// The sweep takes candidates oldest-examined-first (never examined first, ties
// by name), so every candidate is reached within a bounded number of runs.
// sweepKeepCache removes a record once its worktree is gone.
const (
	examFilePrefix = "worktree-exam--"
	examFileSuffix = ".exam"
)

type examEntry struct {
	Path string `json:"path"`
	At   int64  `json:"examined_unix_nano"`
}

func examFile(dir, bead string) string {
	return keepLikeFile(dir, examFilePrefix, examFileSuffix, bead)
}

// readExam parses a last-examined file; torn or foreign content is absent.
func readExam(path string) (examEntry, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return examEntry{}, false
	}
	var e examEntry
	if err := json.Unmarshal(raw, &e); err != nil || e.Path == "" || e.At == 0 {
		return examEntry{}, false
	}
	return e, true
}

// lastExamined returns when c was last examined with git, or 0 when unknown.
func lastExamined(lockDir string, c leakedWorktree) int64 {
	if lockDir == "" {
		return 0
	}
	if e, ok := readExam(examFile(lockDir, c.bead)); ok && e.Path == c.path {
		return e.At
	}
	return 0
}

// noteExamined records that c was examined with git at now. Best effort.
func noteExamined(lockDir string, c leakedWorktree, now time.Time) {
	if lockDir == "" {
		return
	}
	raw, err := json.Marshal(examEntry{Path: c.path, At: now.UnixNano()})
	if err != nil {
		return
	}
	if err := writeFileAtomic(examFile(lockDir, c.bead), raw); err != nil {
		slog.Warn("worktree sweep: cannot write the last-examined record", "bead", c.bead, "err", err)
	}
}

// orderOldestExamined sorts candidates in place: never examined first, then the
// longest-ago examined, ties by bead name.
func orderOldestExamined(lockDir string, candidates []leakedWorktree) {
	at := make(map[string]int64, len(candidates))
	for _, c := range candidates {
		at[c.bead] = lastExamined(lockDir, c)
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if at[a.bead] != at[b.bead] {
			return at[a.bead] < at[b.bead]
		}
		return a.bead < b.bead
	})
}
