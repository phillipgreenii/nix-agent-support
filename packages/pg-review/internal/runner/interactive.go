package runner

import (
	"context"
	"fmt"
	"os"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/ledger"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/prompt"
)

// InteractiveAnalyst is the backend of --analyst interactive. It starts no
// session: it prints the run id, the prompt, the staging path and the
// neutral directory to start the analyst session in, then returns. The run
// stays AwaitingAnalyst, holding its (lens, scope) lock and its staging
// directory, until `agent file --run <id>` files the batch or the reaper
// expires it after interactive_ttl_minutes.
//
// A session started outside the neutral directory is not excluded from later
// evidence (a recorded limitation; it is not detected).
type InteractiveAnalyst struct{}

// Name implements Analyst.
func (InteractiveAnalyst) Name() string { return "interactive" }

// Analyse implements Analyst.
func (InteractiveAnalyst) Analyse(_ context.Context, s *State) error {
	d := s.Deps
	res, err := prompt.Assemble(promptParams(s, s.Run.StagingPath))
	if err != nil {
		return fmt.Errorf("assemble prompt: %w", err)
	}
	neutral := d.Cfg.Ccpool.NeutralDir
	if neutral != "" {
		if err := ensurePrivateDir(neutral); err != nil {
			return fmt.Errorf("neutral directory: %w", err)
		}
	}
	fmt.Fprintf(d.Stdout, "run %d awaiting analyst: file with `pg-review agent file --run %d <findings.json>`\n", s.Run.ID, s.Run.ID)
	fmt.Fprintf(d.Stdout, "%s\n", res.Text)
	fmt.Fprintf(d.Stdout, "staging: %s\n", s.Run.StagingPath)
	fmt.Fprintf(d.Stdout, "start the analyst session in: %s\n", neutral)
	return nil
}

// ensurePrivateDir creates dir (and parents) and makes dir mode 0700.
func ensurePrivateDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.Chmod(dir, 0o700)
}

// FinishInteractive completes an interactive run that `agent file` just
// filed: the staging directory is deleted and cleared from the row and the
// run's lock is released. It does nothing for any other run.
func FinishInteractive(ctx context.Context, l *ledger.Ledger, runID int64, cacheHome string) error {
	rm := defaultRemoveStaging(cacheHome)
	var staging string
	err := l.WithTx(ctx, func(t *ledgerTx) error {
		r, err := t.GetRun(ctx, runID)
		if err != nil {
			return err
		}
		if r.Analyst != "interactive" || r.State != ledger.RunFiled {
			return nil
		}
		staging, r.StagingPath = r.StagingPath, ""
		if err := t.UpdateRun(ctx, r); err != nil {
			return err
		}
		return releaseOwnLock(ctx, t, r)
	})
	if err == nil && staging != "" {
		_ = rm(staging)
	}
	return err
}
