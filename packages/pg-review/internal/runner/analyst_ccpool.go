package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/ccpool"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/ledger"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/prompt"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/validate"
)

// DefaultPollInterval is how often the ccpool analyst polls for the final
// message.
const DefaultPollInterval = 5 * time.Second

// AgentQTool is the one shell tool the analyst may run.
const AgentQTool = "Bash(pg-review agent q:*)"

// defaultMinutes bounds a run whose profile sets no minutes.
const defaultMinutes = 30

// RunEnvVar names the run in the analyst session's environment.
const RunEnvVar = "PG_REVIEW_RUN"

// ScratchEnvVar names the scratch worktree the runner verifies after the run.
const ScratchEnvVar = "PG_REVIEW_SCRATCH"

// CcpoolAnalyst is the backend of --analyst ccpool: it starts a locked-down
// analyst session through the ccpool seam, sends the prompt, waits for the
// final message, validates and files it, and closes the session on every exit
// path. The analyst never calls `agent file`: this runner files its final
// message.
type CcpoolAnalyst struct {
	Client ccpool.Client
	// Poll is the poll interval (default DefaultPollInterval).
	Poll time.Duration
	// Sleep waits for d or until ctx ends; tests inject one that advances
	// their clock.
	Sleep func(ctx context.Context, d time.Duration) error
}

// RegisterCcpool adds the real ccpool analyst (and the session-close hook the
// reaper and failure paths use) to d, unless d already has them.
func RegisterCcpool(d *Deps) {
	cli := ccpool.CLI{PoolDir: d.Cfg.Ccpool.PoolDir}
	if d.Analysts == nil {
		d.Analysts = map[string]Analyst{}
	}
	if _, ok := d.Analysts["ccpool"]; !ok {
		d.Analysts["ccpool"] = &CcpoolAnalyst{Client: cli}
	}
	if d.CloseSession == nil {
		d.CloseSession = func(ctx context.Context, id string) error { return cli.Close(ctx, id, "handler") }
	}
}

// Name implements Analyst.
func (*CcpoolAnalyst) Name() string { return "ccpool" }

var fenceRe = regexp.MustCompile("(?s)```[A-Za-z]*[ \t]*\r?\n(.*?)\r?\n?```")

// extractFindings returns the LAST fenced block of the final message and
// requires its sentinel field end: true. It salvages nothing partial.
func extractFindings(text string) ([]byte, error) {
	m := fenceRe.FindAllStringSubmatch(text, -1)
	if len(m) == 0 {
		return nil, errors.New("the final message has no fenced JSON block")
	}
	raw := []byte(strings.TrimSpace(m[len(m)-1][1]))
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, fmt.Errorf("the last fenced block is not a JSON object: %v", err)
	}
	if string(probe["end"]) != "true" {
		return nil, errors.New("the last fenced block lacks the sentinel field end: true")
	}
	return raw, nil
}

func parseFailure(err error) []validate.Failure {
	return []validate.Failure{{Index: -1, Finding: "#document", Field: "document", Rule: "parse", Token: err.Error()}}
}

// Analyse implements Analyst.
func (a *CcpoolAnalyst) Analyse(ctx context.Context, s *State) (err error) {
	d := s.Deps
	cl := a.Client
	runID := s.Run.ID

	capa, err := cl.Capacity(ctx)
	if err != nil {
		return fmt.Errorf("ccpool capacity: %w", err)
	}
	if capa.UsageLimit != nil {
		return usageSkip(runID, capa.UsageLimit)
	}
	if capa.Free <= 0 {
		return &SkippedError{RunID: runID, Reason: "ccpool busy: no free session slot"}
	}

	res, err := prompt.Assemble(promptParams(s, s.Run.StagingPath))
	if err != nil {
		return fmt.Errorf("assemble prompt: %w", err)
	}
	if err := updateRun(ctx, s, func(r *ledger.Run) {
		r.PromptHash, r.Model = res.Hash, s.Profile.Model
	}); err != nil {
		return err
	}
	neutral := d.Cfg.Ccpool.NeutralDir
	if neutral != "" {
		if err := ensurePrivateDir(neutral); err != nil {
			return fmt.Errorf("neutral directory: %w", err)
		}
	}
	scratch, err := makeScratch()
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(scratch) }()
	g, err := newGuard(scratch, d.Cfg.Env.Dir(), s.Run.StagingPath)
	if err != nil {
		return err
	}

	extID := strconv.FormatInt(runID, 10)
	// The session id is stored before the session exists so a kill between
	// the two leaves the reaper something to close.
	if err := updateRun(ctx, s, func(r *ledger.Run) { r.SessionID = extID }); err != nil {
		return err
	}
	closed := false
	closeSession := func(ctx context.Context) {
		if closed {
			return
		}
		// Detached: the session must be closed even when ctx is already cancelled.
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if cl.Close(cctx, extID, "handler") == nil {
			closed = true
			_ = updateRun(cctx, s, func(r *ledger.Run) { r.SessionID = "" })
		}
	}
	defer closeSession(ctx)

	e := d.Cfg.Env
	startErr := cl.Start(ctx, ccpool.StartOpts{
		ExternalID: extID, Cwd: neutral, Model: s.Profile.Model, Effort: s.Profile.Effort,
		PermissionMode: "dontAsk",
		AllowedTools:   append(append([]string{}, d.Cfg.Ccpool.ReadTools...), AgentQTool),
		Env: []string{
			RunEnvVar + "=" + extID, ScratchEnvVar + "=" + scratch,
			"XDG_CONFIG_HOME=" + e.ConfigHome, "XDG_STATE_HOME=" + e.StateHome, "XDG_CACHE_HOME=" + e.CacheHome,
		},
	})
	if startErr != nil {
		if sk := refusal(runID, startErr); sk != nil {
			return sk
		}
		return fmt.Errorf("ccpool new: %w", startErr)
	}

	minutes := s.Profile.Minutes
	if minutes <= 0 {
		minutes = defaultMinutes
	}
	deadline := d.Now().Add(time.Duration(minutes) * time.Minute)

	turn, err := a.send(ctx, s, extID, res.Text)
	if err != nil {
		return err
	}
	var failures []validate.Failure
	for attempt := 0; ; attempt++ {
		text, werr := a.await(ctx, s, extID, turn, deadline, minutes)
		if werr != nil {
			return werr
		}
		if err := g.verify(ctx, d.Ledger, runID); err != nil {
			return err
		}
		raw, perr := extractFindings(text)
		if perr != nil {
			failures = parseFailure(perr)
		} else {
			_, ferr := ledger.FileFindings(ctx, d.Ledger, d.Cfg, runID, raw, true)
			var ve *validate.Error
			switch {
			case ferr == nil:
				if _, ferr = ledger.FileFindings(ctx, d.Ledger, d.Cfg, runID, raw, false); ferr != nil {
					return ferr
				}
				return nil
			case errors.As(ferr, &ve):
				failures = ve.Failures
			default:
				return ferr
			}
		}
		if attempt == 1 {
			return fmt.Errorf("analyst findings rejected after one repair: %s", summarize(failures))
		}
		if err := updateRun(ctx, s, func(r *ledger.Run) { r.RepairCount++ }); err != nil {
			return err
		}
		if turn, err = a.send(ctx, s, extID, prompt.Repair(failures)); err != nil {
			return err
		}
	}
}

func summarize(fs []validate.Failure) string {
	var parts []string
	for _, f := range fs {
		parts = append(parts, f.String())
	}
	return strings.Join(parts, "; ")
}

// send delivers a prompt without waiting; a refusal becomes a Skipped run.
func (a *CcpoolAnalyst) send(ctx context.Context, s *State, id, text string) (string, error) {
	turn, err := a.Client.Reply(ctx, id, text)
	if err != nil {
		if sk := refusal(s.Run.ID, err); sk != nil {
			return "", sk
		}
		return "", fmt.Errorf("ccpool reply: %w", err)
	}
	return turn, nil
}

// refusal maps a usage-limit (exit 8) or busy (exit 5) refusal to a Skipped
// run, and anything else to nil.
func refusal(runID int64, err error) *SkippedError {
	switch {
	case ccpool.IsUsageLimited(err):
		return &SkippedError{RunID: runID, Reason: "ccpool usage limit reached: " + err.Error()}
	case ccpool.IsBusy(err):
		return &SkippedError{RunID: runID, Reason: "ccpool busy"}
	}
	return nil
}

func usageSkip(runID int64, l *ccpool.UsageLimit) *SkippedError {
	return &SkippedError{RunID: runID, Reason: fmt.Sprintf("ccpool usage limit reached: %s window at limit, resets at %s",
		l.Window, l.ResetsAt.UTC().Format(time.RFC3339))}
}

// await polls for the turn's final message until the wall-clock deadline.
// `result` exit 1 alone is not terminal (it can be a daemon outage); the
// session's own state decides whether the turn can still finish.
func (a *CcpoolAnalyst) await(ctx context.Context, s *State, id, turn string, deadline time.Time, minutes int) (string, error) {
	cl := a.Client
	poll := a.Poll
	if poll <= 0 {
		poll = DefaultPollInterval
	}
	sleep := a.Sleep
	if sleep == nil {
		sleep = sleepCtx
	}
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if !s.Deps.Now().Before(deadline) {
			// Detached: the cancel must go out even when ctx is already cancelled.
			cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			_ = cl.Cancel(cctx, id)
			cancel()
			return "", fmt.Errorf("time limit of %d minutes reached", minutes)
		}
		text, st, err := cl.Result(ctx, turn)
		if err == nil && st == ccpool.ResultDone {
			return text, nil
		}
		if state, serr := cl.State(ctx, id); serr == nil {
			switch state {
			case ccpool.StateError:
				return "", errors.New("the analyst turn errored")
			case ccpool.StateWaitingForHuman:
				return "", errors.New("the analyst session is waiting for input")
			case ccpool.StateNotLive:
				return "", errors.New("the analyst session is no longer live")
			}
		}
		if err := sleep(ctx, poll); err != nil {
			return "", err
		}
	}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

var _ = filepath.Join
