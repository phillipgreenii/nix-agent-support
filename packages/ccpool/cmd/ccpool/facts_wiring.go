package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/phillipgreenii/ccpool/internal/facts"
	"github.com/phillipgreenii/ccpool/internal/session"
)

// factReader is the recorder-backed fact source shared by every command in this
// process. A test swaps it for a reader on an in-process recorder.
var factReader = facts.New("ccpool")

// transcriptAdapter satisfies session.Transcript (and session.FeedWaiter) from
// the recorder instead of re-reading transcript files. It is zero-size so the
// existing `transcriptAdapter{}` call sites keep working.
type transcriptAdapter struct{}

func (transcriptAdapter) LastAssistantText(p string) (string, error) {
	return factReader.LastAssistantText(p)
}

func (transcriptAdapter) LastAssistantTextSince(p string, since time.Time) (string, error) {
	return factReader.LastAssistantTextSince(p, since)
}

func (transcriptAdapter) IsAwaitingInput(p string) (bool, error) {
	return factReader.IsAwaitingInput(p)
}

func (transcriptAdapter) FirstMessageActivity(p string) (time.Time, bool) {
	return factReader.FirstMessageActivity(p)
}

func (transcriptAdapter) OutputTokens(p string) (int64, error) { return factReader.OutputTokens(p) }

func (transcriptAdapter) Mark(ctx context.Context) (string, error) { return factReader.Mark(ctx) }

func (transcriptAdapter) WaitMessage(ctx context.Context, p, mark string, window time.Duration) (bool, error) {
	return factReader.WaitMessage(ctx, p, mark, window)
}

// recorderExister is session.SessionExister (and CheckedExister) from the
// recorder's "originals present" fact. Exists folds an outage into false only for
// callers that predate the checked form; the service uses ExistsChecked.
type recorderExister struct{}

func (recorderExister) Exists(p string) bool {
	ok, _ := factReader.OriginalsPresent(p)
	return ok
}

func (recorderExister) ExistsChecked(p string) (bool, error) { return factReader.OriginalsPresent(p) }

var (
	_ session.Transcript     = transcriptAdapter{}
	_ session.FeedWaiter     = transcriptAdapter{}
	_ session.CheckedExister = recorderExister{}
)

// requireFacts fails a fact-reading command, naming the recorder and what could
// not be done, when the recorder does not answer.
func requireFacts(cmd string) int {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := factReader.Ping(ctx); err != nil {
		if errors.Is(err, facts.ErrDaemonOffline) {
			fmt.Fprintf(os.Stderr, "%s: cannot read session facts: %v\n", cmd, err)
		} else {
			fmt.Fprintf(os.Stderr, "%s: %v\n", cmd, err)
		}
		return 1
	}
	return 0
}

// processGate is the session.Gate every service shares: it asks the recorder at
// most once per process (facts.Reader.PingOnce), so a multi-pool capacity run
// that builds a service per pool sends one hello per run, not one per pool. It
// reads factReader at call time so a test that swaps the reader is honoured.
type processGate struct{}

func (processGate) Ping(ctx context.Context) error { return factReader.PingOnce(ctx) }

var _ session.Gate = processGate{}
