package replay

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/ccpool"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/validate"
)

// liveNote tells the live analyst what this harness differs in from a run: it
// has no query tool, so the evidence below is all it gets, and the reply must
// be the findings block alone.
const liveNote = "\nIn this session you have no tools and cannot run `pg-review agent q`; " +
	"the evidence rows below are all the evidence there is. Reply once, with the single fenced JSON block as your final message.\n"

// SessionAnalyst is an Analyst (and Repairer) that sends the assembled prompt,
// with the evidence rows appended as data, to ccpool sessions. It opens one
// session per Analyse and records every session id it attempted to open, so
// CloseAll can close all of them: a session whose Start failed may still
// exist, and a failed Close of one session must not stop the others closing.
type SessionAnalyst struct {
	Client ccpool.Client
	Cwd    string
	Model  string
	Effort string
	// PollInterval is the wait between Result polls (default 5s).
	PollInterval time.Duration
	// Deadline bounds the wait for one final message (default 20 minutes).
	Deadline time.Duration

	n      int
	id     string
	opened []string
}

// Opened returns the ids of every session this analyst attempted to open and
// has not yet closed successfully.
func (a *SessionAnalyst) Opened() []string { return append([]string(nil), a.opened...) }

// CloseAll closes every session still open, including those of earlier
// attempts. It is detached from ctx on purpose: sessions must still be closed
// when ctx was cancelled (a timeout or an interrupted replay). Sessions that
// fail to close stay recorded; the returned error joins every failure.
func (a *SessionAnalyst) CloseAll(ctx context.Context) error {
	var errs []error
	var left []string
	for _, id := range a.opened {
		if err := a.Client.Close(context.WithoutCancel(ctx), id, "replay"); err != nil {
			errs = append(errs, fmt.Errorf("close %s: %w", id, err))
			left = append(left, id)
		}
	}
	a.opened, a.id = left, ""
	return errors.Join(errs...)
}

// Analyse implements Analyst. A previous attempt's session is closed first.
func (a *SessionAnalyst) Analyse(ctx context.Context, promptText string, ev []validate.EvidenceRow) (string, error) {
	_ = a.CloseAll(ctx) // failures stay recorded and are retried by the final CloseAll
	a.n++
	id := fmt.Sprintf("replay-%d-%d", os.Getpid(), a.n)
	a.opened = append(a.opened, id)
	if err := a.Client.Start(ctx, ccpool.StartOpts{
		ExternalID: id, Cwd: a.Cwd, PermissionMode: "dontAsk",
		Model: a.Model, Effort: a.Effort,
	}); err != nil {
		return "", err
	}
	a.id = id
	var rows string
	for _, r := range ev {
		rows += fmt.Sprintf("%s | %s | %s | %v | %s\n", r.ID, r.SubjectKey, r.Kind, r.Metrics, r.DefinedIn)
	}
	return a.send(ctx, promptText+liveNote+"\nEVIDENCE (data)\n"+rows)
}

// Repair implements Repairer.
func (a *SessionAnalyst) Repair(ctx context.Context, repairText string) (string, error) {
	return a.send(ctx, repairText)
}

func (a *SessionAnalyst) send(ctx context.Context, text string) (string, error) {
	turn, err := a.Client.Reply(ctx, a.id, text)
	if err != nil {
		return "", err
	}
	poll, limit := a.PollInterval, a.Deadline
	if poll == 0 {
		poll = 5 * time.Second
	}
	if limit == 0 {
		limit = 20 * time.Minute
	}
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		text, st, err := a.Client.Result(ctx, turn)
		if err != nil {
			return "", err
		}
		if st == ccpool.ResultDone {
			return text, nil
		}
		time.Sleep(poll)
	}
	return "", fmt.Errorf("replay: no final message within the deadline")
}
