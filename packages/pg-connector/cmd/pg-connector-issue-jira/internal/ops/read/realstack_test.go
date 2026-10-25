package read

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/changes"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/detail"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/origin"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/summary"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/scheduler"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// These tests run show over the REAL summary and detail services, scheduler,
// governor, origin runner and change engine, with only pjira faked, so the
// number of origin calls a read makes is the real one.

var quotedKey = regexp.MustCompile(`"([^"]+)"`)

// keyPjira answers `pjira search --jql 'key in ("A-1", ...)'` from canned
// issues and records every call.
type keyPjira struct {
	mu     sync.Mutex
	issues map[string]string // key -> description text of its first comment
	calls  int
	err    error
}

func (k *keyPjira) Binary() string { return "/abs/pjira" }

func (k *keyPjira) Run(_ context.Context, args ...string) (string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.calls++
	if k.err != nil {
		return "", k.err
	}
	if len(args) < 3 || args[0] != "search" {
		return "", fmt.Errorf("keyPjira: unexpected args %v", args)
	}
	var items []string
	for _, m := range quotedKey.FindAllStringSubmatch(args[2], -1) {
		body, ok := k.issues[m[1]]
		if !ok {
			continue
		}
		b, _ := json.Marshal(map[string]any{
			"key": m[1], "summary": "title of " + m[1], "status": "Some Status",
			"status_category": "indeterminate", "issuetype": "Task", "project": "ABC",
			"updated": "2026-10-05T10:11:12.000+0000", "created": "2026-09-01T00:00:00.000+0000",
			"comments":  []map[string]any{{"id": "1", "author": map[string]any{"display_name": "Pat"}, "body": body, "created": "2026-10-04T10:00:00.000+0000"}},
			"changelog": []any{},
		})
		items = append(items, string(b))
	}
	return fmt.Sprintf(`{"items":[%s],"truncated":false}`, strings.Join(items, ",")), nil
}

func (k *keyPjira) count() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.calls
}

type neverRun struct{}

func (neverRun) Binary() string { return "/abs/pjira" }
func (neverRun) Run(context.Context, ...string) (string, error) {
	return "", errors.New("the operator facts made a pjira call")
}

type realRig struct {
	t    *testing.T
	st   *store.Store
	clk  *governor.ManualClock
	fake *keyPjira
	ops  map[string]scriptout.OpHandler
}

func newRealRig(t *testing.T) *realRig {
	t.Helper()
	st, _, err := store.Open(filepath.Join(t.TempDir(), "store.db"), store.Options{Synchronous: "OFF"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	clk := governor.NewManualClock(t0)
	fake := &keyPjira{issues: map[string]string{}}
	auth := origin.NewAuthState()

	cfg := config.Defaults()
	cfg.RequestRatePerSecond = 100
	gov, err := governor.New(origin.GovernorConfig(cfg), governor.Options{Clock: clk})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := origin.New(origin.Options{
		Inner: fake, Governor: gov, Bucket: origin.Bucket, Concurrency: 2,
		DefaultPause: time.Minute, Clock: clk, Jitter: func() float64 { return 0 }, Auth: auth,
	})
	if err != nil {
		t.Fatal(err)
	}
	chg, err := changes.New(changes.Config{Clock: clk})
	if err != nil {
		t.Fatal(err)
	}
	settings := groups.Settings{SummaryTTL: time.Minute, DetailTTL: 10 * time.Minute, DetailMaxAge: 30 * time.Minute, TerminalTTL: 24 * time.Hour}
	rules := groups.Rules(settings)
	pers := &summary.Persister{Store: st, Rules: rules, Clock: clk, Hook: chg.DetectSummary, OnGone: chg.DetectGone}
	sched := scheduler.New(scheduler.Options{Clock: clk, Gate: gov, Workers: 1})
	keep := summary.KeepAlive{Store: st, Clock: clk, TerminalTTL: settings.TerminalTTL}
	sum, err := summary.NewService(summary.ServiceConfig{
		Store: st, Persister: pers, Fetcher: runner, Scheduler: sched, KeepAlive: keep, Bucket: origin.Bucket, Clock: clk,
	})
	if err != nil {
		t.Fatal(err)
	}
	be := internal.New(neverRun{}).WithGetenv(func(string) string { return "" })
	det, err := detail.NewService(detail.Config{
		Store: st, Scheduler: sched, Rules: rules, Aliases: summary.NewAliases(st), KeepAlive: keep,
		Governor: gov, Auth: auth, Origin: runner, Chg: chg, Facts: be.OperatorFacts, Clock: clk,
	})
	if err != nil {
		t.Fatal(err)
	}
	pers.AfterCommit = det.Revalidate

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { sched.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })

	return &realRig{t: t, st: st, clk: clk, fake: fake, ops: Handlers(Deps{
		Auth: auth, Summary: sum, Detail: det, Chg: chg, Store: st, Pause: runner, Clock: clk,
	})}
}

func (r *realRig) show(args string) *schema.Issue {
	r.t.Helper()
	out, err := r.ops[OpShow].Handle(context.Background(), json.RawMessage(args))
	if err != nil {
		r.t.Fatalf("show %s: %v", args, err)
	}
	return out.(*schema.Issue)
}

func TestShowOverTheRealServicesReadsBothGroupsThroughTheOrigin(t *testing.T) {
	r := newRealRig(t)
	r.fake.issues["ABC-1"] = "hello"

	iss := r.show(`{"id":"abc-1"}`)
	if iss.ID != "ABC-1" || iss.Title != "title of ABC-1" || iss.ServedFrom != "origin" || iss.Stale {
		t.Errorf("issue = %+v", iss)
	}
	if len(iss.Groups) != 2 || iss.Groups[0].Group != "summary" || iss.Groups[1].Group != "detail" {
		t.Errorf("groups = %+v", iss.Groups)
	}
	ent, ok, err := r.st.GetEntity(context.Background(), groups.EntityKind, "ABC-1")
	if err != nil || !ok || iss.Version != ent.Version {
		t.Errorf("version = %d, want the stored entity version %d (%v, %v)", iss.Version, ent.Version, ok, err)
	}
	n := r.fake.count()

	// Every group is now inside its window: a local hit, no origin call.
	r.clk.Advance(5 * time.Second)
	iss = r.show(`{"id":"ABC-1"}`)
	if r.fake.count() != n {
		t.Errorf("origin calls after a local hit = %d, want %d", r.fake.count(), n)
	}
	if iss.ServedFrom != "cache" || iss.AgeSeconds == nil || *iss.AgeSeconds != 5 {
		t.Errorf("served_from %q age %v, want cache and 5", iss.ServedFrom, iss.AgeSeconds)
	}
}

func TestShowWithFreshOverTheRealServicesFetchesEachGroupOnce(t *testing.T) {
	r := newRealRig(t)
	r.fake.issues["ABC-1"] = "hello"
	r.show(`{"id":"ABC-1"}`)
	before := r.fake.count()

	iss := r.show(`{"id":"ABC-1","fresh":true}`)
	if got := r.fake.count() - before; got != 2 {
		t.Errorf("a fresh read made %d origin calls, want 2 (the summary search and the detail search, one each)", got)
	}
	if iss.ServedFrom != "origin" {
		t.Errorf("served_from = %q, want origin", iss.ServedFrom)
	}
}

func TestShowWithWatchOverTheRealServicesWatchesAnUnknownIssueWithoutARow(t *testing.T) {
	r := newRealRig(t)
	r.fake.issues["ABC-1"] = "hello"
	r.show(`{"id":"ABC-1","watch":true}`)
	ent, ok, err := r.st.GetEntity(context.Background(), groups.EntityKind, "ABC-1")
	if err != nil || !ok || ent.WatchedAt.IsZero() {
		t.Fatalf("entity = %+v (%v, %v), want watched", ent, ok, err)
	}
	rows, err := r.st.LastChanges(context.Background(), groups.EntityKind, "ABC-1", 10)
	if err != nil || len(rows) != 0 {
		t.Errorf("rows = %+v, %v, want none for the first sighting of a watched issue", rows, err)
	}
}
