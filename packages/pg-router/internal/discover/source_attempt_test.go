package discover

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router/internal/core"
	"github.com/phillipgreenii/pg-router/internal/event"
	"github.com/phillipgreenii/pg-router/internal/query"
)

// Per-attempt timing and shutdown handling (bead pg2-zdowv, DEC-OBS-8): every
// query attempt is bracketed for the in-flight gauge and duration histogram,
// the give-up records elapsed + argv, and a failure that coincides with the
// router's own context being cancelled is NOT a source failure.

// cancelOnRunQuery cancels ctx while its Run is in flight, then fails the way
// exec.CommandContext does when its context is cancelled mid-run.
type cancelOnRunQuery struct {
	query.Meta
	cancel context.CancelFunc
	err    error
}

func (cancelOnRunQuery) Validate() error        { return nil }
func (cancelOnRunQuery) BackingCommand() string { return "" }
func (c cancelOnRunQuery) Run(context.Context, query.Env) ([]event.Event, error) {
	c.cancel()
	return nil, c.err
}

func TestProduce_attemptBracketedWithElapsed(t *testing.T) {
	obs := &recordingSourceFailureObserver{}
	step := 0
	clock := func() time.Time { step++; return time.Unix(0, 0).Add(time.Duration(step) * 7 * time.Second) }
	_, err := produce(context.Background(), query.Env{}, query.SourceSet{healthySource("ok")}, newQueue(t),
		core.NewBindings("work.ready"), Cadence{}, realSleep, clock, obs, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"ok"}; !equalStrings(obs.starts, want) {
		t.Fatalf("starts = %v, want %v", obs.starts, want)
	}
	if len(obs.ends) != 1 || obs.ends[0].source != "ok" || obs.ends[0].shutdown {
		t.Fatalf("ends = %+v, want one non-shutdown end for ok", obs.ends)
	}
	if obs.ends[0].elapsed <= 0 {
		t.Fatalf("elapsed = %v, want > 0 from the injected clock", obs.ends[0].elapsed)
	}
}

func TestProduce_giveUpRecordsElapsedAndArgv(t *testing.T) {
	obs := &recordingSourceFailureObserver{}
	src := query.Source{Name: "bad", Query: query.CommandQuery{
		Meta:   query.Meta{EmitTypes: []string{"work.ready"}},
		Argv:   []string{"/bin/pg-connector", "changes", "pr"},
		Format: query.FormatJSONL,
	}}
	step := 0
	clock := func() time.Time { step++; return time.Unix(0, 0).Add(time.Duration(step) * 10 * time.Second) }
	rpt, err := produce(context.Background(), query.Env{Cmd: killedCommander{}}, query.SourceSet{src}, newQueue(t),
		core.NewBindings("work.ready"), Cadence{}, realSleep, clock, obs, nil)
	if err != nil {
		t.Fatal(err)
	}
	fi, ok := rpt.Failure["bad"]
	if !ok {
		t.Fatal("Failure[bad] not recorded")
	}
	if fi.Elapsed <= 0 {
		t.Errorf("Failure.Elapsed = %v, want > 0", fi.Elapsed)
	}
	if got, want := fi.Argv, []string{"/bin/pg-connector", "changes", "pr"}; !equalStrings(got, want) {
		t.Errorf("Failure.Argv = %v, want %v", got, want)
	}
}

// killedCommander fails every command the way a timeout kill does.
type killedCommander struct{}

func (killedCommander) Run(context.Context, []string) ([]byte, error) {
	return nil, errors.New("signal: killed")
}

func TestProduce_shutdownCancellationIsNotASourceFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	obs := &recordingSourceFailureObserver{}
	src := query.Source{Name: "cut", Query: cancelOnRunQuery{
		Meta:   query.Meta{EmitTypes: []string{"work.ready"}},
		cancel: cancel,
		err:    errors.New("command query [x]: signal: killed"),
	}}
	rpt, err := produce(ctx, query.Env{}, query.SourceSet{src, healthySource("after")}, newQueue(t),
		core.NewBindings("work.ready"), Cadence{}, realSleep, time.Now, obs, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("produce err = %v, want context.Canceled (the tick stops on shutdown)", err)
	}
	if len(obs.sources) != 0 {
		t.Errorf("OnSourceFailure fired for %v: a shutdown cancellation must not count as a failure", obs.sources)
	}
	if rpt.SourceErrors["cut"] != nil {
		t.Errorf("SourceErrors[cut] = %v, want none", rpt.SourceErrors["cut"])
	}
	if _, ok := rpt.Failure["cut"]; ok {
		t.Errorf("Failure[cut] recorded for a shutdown cancellation")
	}
	if len(obs.ends) != 1 || !obs.ends[0].shutdown {
		t.Fatalf("ends = %+v, want one attempt end flagged shutdown", obs.ends)
	}
	if len(obs.succeeded) != 0 || len(obs.starts) != 1 {
		t.Errorf("the pass must stop at the cancelled source; starts=%v succeeded=%v", obs.starts, obs.succeeded)
	}
}

func TestProduce_timeoutKillWithLiveContextStillCountsAsFailure(t *testing.T) {
	obs := &recordingSourceFailureObserver{}
	rpt, err := produce(context.Background(), query.Env{}, query.SourceSet{failingSource("bad")}, newQueue(t),
		core.NewBindings("work.ready"), Cadence{}, realSleep, time.Now, obs, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"bad"}; !equalStrings(obs.sources, want) {
		t.Fatalf("failures = %v, want %v (a failure with a live ctx is a real failure)", obs.sources, want)
	}
	if rpt.SourceErrors["bad"] == nil {
		t.Fatal("SourceErrors[bad] must be set")
	}
	if len(obs.ends) != 1 || obs.ends[0].shutdown {
		t.Fatalf("ends = %+v, want one non-shutdown end", obs.ends)
	}
}
