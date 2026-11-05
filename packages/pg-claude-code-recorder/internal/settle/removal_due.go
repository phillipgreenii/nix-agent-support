package settle

// Removal due-ness. Removal is an operator-run command on a roughly fixed period, so the
// recorder says when it is time to run it: removal_quiet_share is the share of settled
// Conversations quiet longer than remove.after (the Conversations the removal backlog
// gauge counts, over all settled ones), removal_last_run_timestamp is when a removal run
// last completed, and removal_due is 1 when the share is over remove.dueShare or quiet
// Conversations exist and no run completed within remove.dueEvery. The quiet count reuses
// the removal backlog gauge's data (backlogFacts), so there is no new read.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
)

// RemovalRunFile is the name of the last-removal-run file in the state directory.
const RemovalRunFile = "removal.json"

// RemovalRun is the content of RemovalRunFile.
type RemovalRun struct {
	AtNS int64 `json:"at_ns"`
}

// LoadRemovalRun reads the last-run file of dir (ok is false when none exists).
func LoadRemovalRun(dir string) (RemovalRun, bool) {
	var r RemovalRun
	b, err := os.ReadFile(filepath.Join(dir, RemovalRunFile))
	if err != nil || json.Unmarshal(b, &r) != nil || r.AtNS <= 0 {
		return RemovalRun{}, false
	}
	return r, true
}

// RemovalDue reports whether removal is due: settled is the number of settled Conversations
// and quiet how many of them have been quiet longer than remove.after. lastRun is the end of
// the last completed run (zero when none has).
func RemovalDue(settled, quiet int, lastRun, now time.Time, dueShare float64, dueEvery time.Duration) (share float64, due bool) {
	if settled > 0 {
		share = float64(quiet) / float64(settled)
	}
	if quiet == 0 {
		return share, false
	}
	if share > dueShare {
		return share, true
	}
	return share, lastRun.IsZero() || now.Sub(lastRun) > dueEvery
}

// recordRemovalRun stores the time a removal run completed, so a restart keeps it.
func (s *Settler) recordRemovalRun(now time.Time) {
	b, err := json.Marshal(RemovalRun{AtNS: now.UnixNano()})
	if err == nil {
		final := filepath.Join(s.st.Dir(), RemovalRunFile)
		tmp := final + ".tmp"
		if err = os.WriteFile(tmp, b, 0o600); err == nil {
			err = os.Rename(tmp, final)
		}
	}
	if err != nil {
		s.cfg.Obs.Log.Warn("removal_run_record_failed", "err", err.Error())
	}
	s.mu.Lock()
	s.lastRemovalRun = now
	s.mu.Unlock()
	s.cfg.Obs.Reg.Set(obs.MRemovalLastRun, nil, float64(now.UnixNano())/1e9)
	s.markRemovalGauges()
}

// setRemovalDue sets the share and due gauges from the counts of one gauge refresh.
func (s *Settler) setRemovalDue(settled, quiet int, now time.Time) {
	s.mu.Lock()
	last := s.lastRemovalRun
	s.mu.Unlock()
	if last.IsZero() {
		if r, ok := LoadRemovalRun(s.st.Dir()); ok {
			last = time.Unix(0, r.AtNS)
			s.mu.Lock()
			s.lastRemovalRun = last
			s.mu.Unlock()
			s.cfg.Obs.Reg.Set(obs.MRemovalLastRun, nil, float64(r.AtNS)/1e9)
		}
	}
	share, due := RemovalDue(settled, quiet, last, now, s.cfg.DueShare, s.cfg.DueEvery)
	reg := s.cfg.Obs.Reg
	reg.Set(obs.MRemovalQuietShare, nil, share)
	d := 0.0
	if due {
		d = 1
	}
	reg.Set(obs.MRemovalDue, nil, d)
}
