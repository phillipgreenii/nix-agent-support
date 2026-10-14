package ciops

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/ciprovider"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/github"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/metered"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/ratefeed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// logInner is the raw gh the metered runner runs: it records every invocation.
type logInner struct {
	mu    sync.Mutex
	calls []string
	reply func(args []string) (string, error)
}

func (l *logInner) Run(_ context.Context, args ...string) ([]byte, error) {
	l.mu.Lock()
	l.calls = append(l.calls, strings.Join(args, " "))
	reply := l.reply
	l.mu.Unlock()
	out, err := reply(args)
	return []byte(out), err
}

func (l *logInner) RunStdin(ctx context.Context, _ []byte, args ...string) ([]byte, error) {
	return l.Run(ctx, args...)
}

func (l *logInner) recorded() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.calls...)
}

// logsEnv is the get_logs ops over the real metered runner and a real governor.
type logsEnv struct {
	*env
	inner *logInner
}

func newLogsEnv(t *testing.T, cfg *governor.Config, reply func(args []string) (string, error)) *logsEnv {
	t.Helper()
	inner := &logInner{reply: reply}
	e := newEnv(t, envOptions{govConfig: cfg})
	runner := &metered.Runner{Inner: inner, Governor: e.gov, Feed: ratefeed.New(e.gov, inner, e.clk)}
	e.h = Handlers(Deps{Engine: e.rig.Svc, Logs: ciprovider.NewWithDeps(runner, nil), Clock: e.clk})
	return &logsEnv{env: e, inner: inner}
}

func okLogs(args []string) (string, error) { return "build\tstep one\nbuild\tstep two\n", nil }

func TestGetLogsIsOneMeteredPorcelainCallBookedAsPassthrough(t *testing.T) {
	e := newLogsEnv(t, nil, okLogs)
	got, err := e.call(OpGetLogs, map[string]any{"run_id": "4242", "repo": repo1})
	if err != nil {
		t.Fatal(err)
	}
	if b, ok := got.([]byte); !ok || string(b) != "build\tstep one\nbuild\tstep two\n" {
		t.Fatalf("result = %#v, want the raw log bytes", got)
	}
	if want := []string{"run view --log --repo " + repo1 + " -- 4242"}; strings.Join(e.inner.recorded(), "|") != strings.Join(want, "|") {
		t.Errorf("gh calls = %q, want exactly %q", e.inner.recorded(), want)
	}
	spends := e.spends(governor.BucketRESTCore)
	if len(spends) != 1 {
		t.Fatalf("%d core spends, want 1: %+v", len(spends), spends)
	}
	if s := spends[0]; s.Class != governor.ClassInteractive || s.Purpose != governor.PurposePassthrough || s.Cost != 1 {
		t.Errorf("spend = %+v, want one core request, class 1, purpose passthrough", s)
	}
	if n := len(e.spends(governor.BucketGraphQL)) + len(e.spends(governor.BucketRESTSearch)); n != 0 {
		t.Errorf("%d spends outside the core bucket", n)
	}
}

func TestGetLogsRefusesAnEmptyRunIDAndAnInvalidRepoBeforeAnyCall(t *testing.T) {
	e := newLogsEnv(t, nil, okLogs)
	for _, a := range []map[string]any{
		{"run_id": "", "repo": repo1},
		{"run_id": "  ", "repo": repo1},
		{"repo": repo1},
		{"run_id": "4242", "repo": ""},
		{"run_id": "4242", "repo": "widget"},
	} {
		if _, err := e.call(OpGetLogs, a); code(err) != "invalid_argument" {
			t.Errorf("%v: err = %v (%s), want invalid_argument", a, err, code(err))
		}
	}
	if _, err := e.call(OpGetLogs, "not an object"); code(err) != "invalid_argument" {
		t.Errorf("undecodable arguments: %v", err)
	}
	if n := len(e.inner.recorded()); n != 0 || len(e.spends(governor.BucketRESTCore)) != 0 {
		t.Errorf("%d gh calls and %d spends for refused arguments", n, len(e.spends(governor.BucketRESTCore)))
	}
}

func TestGetLogsKeepsARunIDThatLooksLikeAFlagPositional(t *testing.T) {
	e := newLogsEnv(t, nil, okLogs)
	if _, err := e.call(OpGetLogs, map[string]any{"run_id": "--repo", "repo": repo1}); err != nil {
		t.Fatal(err)
	}
	if got := e.inner.recorded(); len(got) != 1 || got[0] != "run view --log --repo "+repo1+" -- --repo" {
		t.Errorf("gh calls = %q, want the run id after the -- terminator", got)
	}
}

func TestGetLogsIsUnavailableNamingTheCapWhenTheGovernorRefuses(t *testing.T) {
	cfg := governor.Config{
		GraphQL: governor.Limits{Cap: 1000}, RESTCore: governor.Limits{Cap: 1}, RESTSearch: governor.Limits{Cap: 1000},
		BackgroundShare: 1,
	}
	e := newLogsEnv(t, &cfg, okLogs)
	args := map[string]any{"run_id": "4242", "repo": repo1}
	if _, err := e.call(OpGetLogs, args); err != nil {
		t.Fatalf("the first call is inside the cap: %v", err)
	}
	_, err := e.call(OpGetLogs, args)
	if code(err) != "unavailable" || !strings.Contains(err.Error(), "cap reached") {
		t.Fatalf("err = %v, want unavailable naming the cap", err)
	}
	var refusal *governor.Refusal
	if !errors.As(err, &refusal) {
		t.Errorf("err = %v does not keep the refusal", err)
	}
	if n := len(e.inner.recorded()); n != 1 {
		t.Errorf("%d gh calls, want the refused one never to run", n)
	}
}

func TestGetLogsClassifiesWhatGHAnswers(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"missing run", errors.New("gh run view: failed to get run: HTTP 404: Not Found"), "not_found"},
		{"bad credential", github.ErrGHAuthInvalid, "unauthenticated"},
		{"rate limit", errors.New("gh: API rate limit exceeded for user"), "unavailable"},
		{"other failure", errors.New("gh: connection reset"), "unavailable"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newLogsEnv(t, nil, func([]string) (string, error) { return "", c.err })
			_, err := e.call(OpGetLogs, map[string]any{"run_id": "4242", "repo": repo1})
			if code(err) != c.want {
				t.Errorf("err = %v (%s), want %s", err, code(err), c.want)
			}
		})
	}
}

func TestGetLogsStoresNothing(t *testing.T) {
	e := newLogsEnv(t, nil, okLogs)
	// A PR the daemon knows, with its rows, to prove nothing of it moves either.
	e.seedPR(node1)
	runsRow := e.seedRuns(node1, false, ciRun("12", "ci", head1, "completed", "success"))
	before := snapshot(t, e.env)

	if _, err := e.call(OpGetLogs, map[string]any{"run_id": "12", "repo": repo1}); err != nil {
		t.Fatal(err)
	}
	if after := snapshot(t, e.env); after != before {
		t.Errorf("the store changed:\nbefore %s\nafter  %s", before, after)
	}
	// No origin request of the group engine either: only the one porcelain call.
	if e.gh.runsCount() != 0 || e.gh.postCount() != 0 {
		t.Errorf("get_logs touched the REST gateway (%d runs requests, %d posts)", e.gh.runsCount(), e.gh.postCount())
	}
	row, _ := e.rig.Group(node1, "runs")
	if row.Generation != runsRow.Generation || !row.FreshUntil.Equal(runsRow.FreshUntil) {
		t.Errorf("runs row moved: %+v", row)
	}
}

// snapshot renders every row of the store the daemon keeps for the PRs the tests
// use, entities, groups and the job table included, as one string.
func snapshot(t *testing.T, e *env) string {
	t.Helper()
	ctx := context.Background()
	var b strings.Builder
	ents, err := e.rig.Store.ListEntities(ctx, "pr")
	if err != nil {
		t.Fatal(err)
	}
	for _, ent := range ents {
		b.WriteString("entity " + ent.ID + " " + ent.LastAccess.UTC().Format(time.RFC3339Nano) + "\n")
	}
	for _, node := range []string{node1, node2} {
		for _, g := range []string{"summary", "detail", "conversation", "files", "commits", "pending", "runs"} {
			row, ok, err := e.rig.Store.GetGroup(ctx, "pr", node, g)
			if err != nil {
				t.Fatal(err)
			}
			if ok {
				b.WriteString(rowString(row))
			}
		}
	}
	for _, id := range []string{"12", "41"} {
		for attempt := 1; attempt <= 3; attempt++ {
			if j, ok, _ := e.rig.Store.GetJobs(ctx, id, attempt); ok {
				b.WriteString("job " + j.RunID + "\n")
			}
		}
	}
	return b.String()
}

func rowString(r store.GroupRow) string {
	return "group " + r.ID + "/" + r.Group + " gen " + string(rune('0'+r.Generation%10)) + " " + string(r.Content) + " " +
		r.FetchedAt.UTC().Format(time.RFC3339Nano) + " " + r.FreshUntil.UTC().Format(time.RFC3339Nano) + "\n"
}
