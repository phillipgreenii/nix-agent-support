package runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/ledger"
)

// MetricsRefreshHook is the metrics refresh step of maintain. It is a no-op
// until the metrics work registers one.
var MetricsRefreshHook func(ctx context.Context, d *Deps) error

// MaintainOptions are the seams of Maintain.
type MaintainOptions struct {
	// Filer files Adopted work beads (the tracker seam); nil skips the step
	// with a failure, since maintain cannot retry without a tracker.
	Filer ledger.BeadFiler
}

// StepResult is the outcome of one maintain step.
type StepResult struct {
	Name   string
	Err    error
	Detail string
}

// MaintainResult lists every step in the order it ran.
type MaintainResult struct {
	Steps []StepResult
}

// Failed returns the steps that failed.
func (r MaintainResult) Failed() []StepResult {
	var out []StepResult
	for _, s := range r.Steps {
		if s.Err != nil {
			out = append(out, s)
		}
	}
	return out
}

// Maintain runs the periodic housekeeping steps. Each step is independent: a
// failing (or panicking) step is recorded and the others still run. The
// heartbeat is written last, whatever the steps did.
func Maintain(ctx context.Context, d *Deps, o MaintainOptions) MaintainResult {
	d.defaults()
	var res MaintainResult
	step := func(name string, fn func() (string, error)) {
		sr := StepResult{Name: name}
		func() {
			defer func() {
				if r := recover(); r != nil {
					sr.Err = fmt.Errorf("panic: %v", r)
				}
			}()
			sr.Detail, sr.Err = fn()
		}()
		res.Steps = append(res.Steps, sr)
	}
	l := d.Ledger
	step("outbox", func() (string, error) {
		if o.Filer == nil {
			return "", errors.New("no tracker configured")
		}
		r, err := ledger.FileAdoptedBeads(ctx, l, o.Filer)
		return fmt.Sprintf("filed %d, failed %d, skipped %d", r.Filed, r.Failed, r.Skipped), err
	})
	step("auto-defer", func() (string, error) {
		ids, err := l.AutoDefer(ctx, d.Cfg.Limits.ItemsAutoDeferDays)
		return fmt.Sprintf("%d deferred", len(ids)), err
	})
	step("promote-waiting", func() (string, error) {
		ids, err := l.PromoteWaiting(ctx, d.Cfg.Limits.ItemsMaxPending)
		return fmt.Sprintf("%d promoted", len(ids)), err
	})
	step("metrics", func() (string, error) {
		if MetricsRefreshHook == nil {
			return "no hook", nil
		}
		return "", MetricsRefreshHook(ctx, d)
	})
	step("export", func() (string, error) { return "", exportLedger(ctx, d) })
	step("backup", func() (string, error) { return backupLedger(ctx, d) })
	step("evidence-ttl", func() (string, error) { return sweepEvidence(ctx, d) })
	step("reaper", func() (string, error) {
		reaped, err := Reap(ctx, d)
		return fmt.Sprintf("%d reaped", len(reaped)), err
	})
	step("heartbeat", func() (string, error) {
		return "", l.WithTx(ctx, func(t *ledgerTx) error { return t.SetHeartbeat(ctx, d.Now().UTC()) })
	})
	return res
}

func stateDir(d *Deps) string { return filepath.Join(d.Cfg.Env.StateHome, "pg-review") }

func exportLedger(ctx context.Context, d *Deps) error {
	dir := stateDir(d)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return d.Ledger.ExportFile(ctx, filepath.Join(dir, "export.jsonl"))
}

// backupLedger writes one SQLite copy per UTC day.
func backupLedger(ctx context.Context, d *Deps) (string, error) {
	dir := filepath.Join(stateDir(d), "backups")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	dest := filepath.Join(dir, d.Now().UTC().Format("2006-01-02")+".db")
	if _, err := os.Stat(dest); err == nil {
		return "already written today", nil
	}
	return "", d.Ledger.Backup(ctx, dest)
}

// sweepEvidence removes the evidence files of runs that ended more than
// evidence_ttl_days ago and clears the run's pointers to them. Only paths
// under the evidence cache root are deleted. Topics, rulings and the run rows
// themselves stay.
func sweepEvidence(ctx context.Context, d *Deps) (string, error) {
	ttl := d.Cfg.Limits.EvidenceTTLDays
	if ttl <= 0 {
		return "disabled", nil
	}
	root := evidenceRoot(d.Cfg.Env.CacheHome)
	runs, err := d.Ledger.RunsInStates(ctx, ledger.RunFiled, ledger.RunSkipped, ledger.RunFailed)
	if err != nil {
		return "", err
	}
	n := 0
	var errs []string
	for _, r := range runs {
		if r.EvidenceDir == "" && r.StagingPath == "" {
			continue
		}
		base := r.EndedAt
		if base.IsZero() {
			base = r.CreatedAt
		}
		if d.Now().Before(base.AddDate(0, 0, ttl)) {
			continue
		}
		ok := true
		for _, p := range []string{r.EvidenceDir, r.StagingPath} {
			if p == "" {
				continue
			}
			// removeStaging decides: inside the evidence root, or a real
			// /tmp/pg-review-* directory. EvidenceDir has no /tmp option.
			if p == r.EvidenceDir && !within(root, p) {
				ok = false
				continue
			}
			// removeStaging re-checks after symlink resolution, so a link
			// that escapes the root is refused and reported.
			if err := removeStaging(root, p); err != nil {
				ok = false
				errs = append(errs, err.Error())
			}
		}
		if !ok {
			continue
		}
		r.EvidenceDir, r.StagingPath = "", ""
		if err := d.Ledger.WithTx(ctx, func(t *ledgerTx) error { return t.UpdateRun(ctx, r) }); err != nil {
			return "", err
		}
		n++
	}
	detail := fmt.Sprintf("%d runs swept", n)
	if len(errs) > 0 {
		return detail, errors.New(strings.Join(errs, "; "))
	}
	return detail, nil
}

// within reports whether p is strictly inside root.
func within(root, p string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(p))
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

var _ = time.Second
