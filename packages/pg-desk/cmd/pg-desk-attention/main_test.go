package main

import (
	"context"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout/conformance"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/attention"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/interpret"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// runDeadline is a hang guard, not a performance assertion.
const runDeadline = 2 * time.Minute

const fixtureRepo = "acme/api"

var fixedNow = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

// fixturePR is one stored PR for the plugin tests.
type fixturePR struct {
	n         int
	ownership string
	panel     string
	facts     string
}

func (p fixturePR) id() string { return fmt.Sprintf("%s#%d", fixtureRepo, p.n) }

// failingCIFacts is an own PR whose CI rollup is `failure`, so
// pr.own-ci-failing raises a high item.
func failingCIFacts(n int) string {
	return fmt.Sprintf(`{"pr_show":{"number":%d,"state":"open","author":"me","head_sha":"h1"},"ci":{"runs":[{"id":"1","name":"build","status":"completed","conclusion":"failure","head_sha":"h1","attempt":1}]}}`, n)
}

// fixtureStore writes a migrated store holding prs and returns its path. The
// standard fixture set: #1 team review requested (medium), #2 team awaiting
// the team (no item), #3 own PR with failing CI (high), #4 team review
// requested (medium).
func fixtureStore(t *testing.T, prs ...fixturePR) string {
	t.Helper()
	store.SetSynchronousForTests("OFF")
	path := filepath.Join(t.TempDir(), "pg-desk", "store.db")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range prs {
		facts := p.facts
		if facts == "" {
			facts = fmt.Sprintf(`{"pr_show":{"number":%d,"state":"open","author":"me","head_sha":"h1"}}`, p.n)
		}
		if err := st.UpsertEntityForTest(store.Entity{Repo: fixtureRepo, EntityType: "pr", EntityID: p.id(), Facts: facts, AsOf: "2026-10-06T11:00:00Z"}); err != nil {
			t.Fatal(err)
		}
		// The stored waiting-on decision a row of that panel carries.
		approvals := "{}"
		switch p.panel {
		case interpret.PanelTeamAwaitingMe:
			approvals = `{"waiting_on":"me","reason":"review-requested"}`
		case interpret.PanelMineAwaitingMe:
			approvals = `{"waiting_on":"me","reason":"ci-failing"}`
		}
		if err := st.UpsertInterpretation(store.Interpretation{
			Repo: fixtureRepo, EntityType: "pr", EntityID: p.id(), Ownership: p.ownership, Panel: p.panel,
			Approvals: approvals, MatchReasons: "[]", Enrichment: "{}", Urgency: "{}", Dispositions: "[]", AsOf: "2026-10-06T11:00:00Z",
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func standardFixture(t *testing.T) string {
	t.Helper()
	return fixtureStore(
		t,
		fixturePR{n: 1, ownership: "team", panel: interpret.PanelTeamAwaitingMe},
		fixturePR{n: 2, ownership: "team", panel: interpret.PanelTeamAwaitingTeam},
		fixturePR{n: 3, ownership: "mine", panel: interpret.PanelMineAwaitingMe, facts: failingCIFacts(3)},
		fixturePR{n: 4, ownership: "team", panel: interpret.PanelTeamAwaitingMe},
	)
}

func testProvider(path string) *provider {
	return &provider{
		loadConfig: func(context.Context) (*config.Config, error) {
			return &config.Config{SelfLogin: "me", Repos: []config.RepoConfig{{Remote: fixtureRepo}}}, nil
		},
		openStore: func() (*store.Store, error) { return store.OpenReadOnly(path) },
		clock:     interpret.FixedClock(fixedNow),
	}
}

// evaluate runs the evaluator directly over the same fixture, the oracle the
// plugin's output is compared with.
func evaluate(t *testing.T, path string) attention.Result {
	t.Helper()
	st, err := store.OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	cfg := &config.Config{SelfLogin: "me", Repos: []config.RepoConfig{{Remote: fixtureRepo}}}
	res, err := attention.Evaluate(attention.Inputs{Store: st, Repo: fixtureRepo, Config: cfg, Clock: interpret.FixedClock(fixedNow)})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestListAttention_OneItemPerEntityInEvaluatorOrder(t *testing.T) {
	path := standardFixture(t)
	got, err := testProvider(path).ListAttention(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := evaluate(t, path)
	if len(want.Items) != 3 {
		t.Fatalf("fixture should raise three items, evaluator says %+v", want.Items)
	}
	if len(got) != len(want.Items) {
		t.Fatalf("got %d items, want %d (one per entity)", len(got), len(want.Items))
	}
	seen := map[string]bool{}
	for i, w := range want.Items {
		g := got[i]
		if g.Type != w.Type || g.Type != "pr" || g.ID != w.ID || g.Summary != w.Summary || string(g.Severity) != string(w.Severity) {
			t.Errorf("item %d = %+v, want type/id/summary/severity of evaluator item %+v", i, g, w)
		}
		if g.Group == nil || g.Group.Key != w.Group || g.Group.Label == "" {
			t.Errorf("item %d group = %+v, want key %q and a non-empty label", i, g.Group, w.Group)
		}
		if g.URL != "" {
			t.Errorf("item %d carries url %q; the plugin has no page of its own to name", i, g.URL)
		}
		if seen[g.Type+":"+g.ID] {
			t.Errorf("entity %s:%s appears twice (INV-ATTNEVAL-3)", g.Type, g.ID)
		}
		seen[g.Type+":"+g.ID] = true
		if !g.Severity.IsValid() {
			t.Errorf("item %d severity %q is not a valid attention severity", i, g.Severity)
		}
	}
	if got[0].ID != fixtureRepo+"#3" || got[0].Severity != schema.SeverityHigh {
		t.Errorf("first item = %+v, want the failing own PR (high) first", got[0])
	}
}

func TestListAttention_NothingNeedsTheOperatorIsAnEmptyNonNilList(t *testing.T) {
	path := fixtureStore(t, fixturePR{n: 2, ownership: "team", panel: interpret.PanelTeamAwaitingTeam})
	got, err := testProvider(path).ListAttention(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || len(got) != 0 {
		t.Fatalf("got %#v, want an empty non-nil slice (marshals as [], not null)", got)
	}
}

func TestListAttention_UnreadableInputsAreErrorsNeverAnEmptyList(t *testing.T) {
	path := standardFixture(t)
	cases := map[string]func(p *provider){
		"config cannot be loaded": func(p *provider) {
			p.loadConfig = func(context.Context) (*config.Config, error) { return nil, errors.New("no config") }
		},
		"no repository configured": func(p *provider) {
			p.loadConfig = func(context.Context) (*config.Config, error) { return &config.Config{}, nil }
		},
		"store cannot be opened": func(p *provider) {
			p.openStore = func() (*store.Store, error) { return nil, errors.New("no store") }
		},
		"unknown rule kind in the config": func(p *provider) {
			p.loadConfig = func(context.Context) (*config.Config, error) {
				c := &config.Config{Repos: []config.RepoConfig{{Remote: fixtureRepo}}}
				c.Attention.Rules = map[string]config.AttentionRuleConfig{"pr.no-such-rule": {}}
				return c, nil
			}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			p := testProvider(path)
			mutate(p)
			got, err := p.ListAttention(context.Background())
			if err == nil {
				t.Fatalf("got %#v and no error; a source that cannot be read must say so (INV-ATTNEVAL-6)", got)
			}
			if !errors.Is(err, scriptout.ErrUnavailable) {
				t.Errorf("err = %v, want it to wrap ErrUnavailable so the umbrella reports the source degraded", err)
			}
		})
	}
}

func TestCapabilities_AdvertisesAttentionOnly(t *testing.T) {
	table := newDispatchTable(testProvider(standardFixture(t)))
	res, err := table[scriptout.OpCapabilities].Handle(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	caps, ok := res.(scriptout.CapabilitiesResponse)
	if !ok {
		t.Fatalf("capabilities result is %T", res)
	}
	if want := []string{scriptout.OpCapabilities, "list_attention"}; !sameSet(caps.Ops, want) {
		t.Errorf("ops = %v, want exactly %v (no auth_status: nothing to authenticate against)", caps.Ops, want)
	}
	if caps.SchemaVersions["attention"] != schema.AttentionSchemaVersion || len(caps.SchemaVersions) != 1 {
		t.Errorf("schemaVersions = %v, want only attention=%d", caps.SchemaVersions, schema.AttentionSchemaVersion)
	}
}

func sameSet(a, b []string) bool {
	x, y := append([]string(nil), a...), append([]string(nil), b...)
	sort.Strings(x)
	sort.Strings(y)
	return reflect.DeepEqual(x, y)
}

// TestConformance_InProcessTable runs the shared wire-conformance suite
// against the plugin's own dispatch table.
func TestConformance_InProcessTable(t *testing.T) {
	table := newDispatchTable(testProvider(standardFixture(t)))
	requireConformant(t, conformance.TableBackend{Table: table})
}

func requireConformant(t *testing.T, b conformance.Backend) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), runDeadline)
	defer cancel()
	results := conformance.Run(ctx, b)
	if len(results) == 0 {
		t.Fatal("conformance.Run produced no results")
	}
	for _, r := range results {
		if r.Err != nil {
			t.Errorf("%s: %v", r.Name, r.Err)
		}
	}
}

// TestPluginSourceImportsNoExecOrNetwork is the structural half of the "must exec nothing"
// requirement: no source file of this command imports os/exec or any net
// package. The tripwire test below is the behavioral half.
func TestPluginSourceImportsNoExecOrNetwork(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	scanned := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		scanned++
		af, err := parser.ParseFile(token.NewFileSet(), f, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", f, err)
		}
		for _, imp := range af.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			if path == "os/exec" || path == "net" || strings.HasPrefix(path, "net/") || strings.HasPrefix(path, "syscall") {
				t.Errorf("%s imports %q: the plugin must not exec or open a network connection", f, path)
			}
		}
	}
	if scanned == 0 {
		t.Fatal("scanned no source files; the guard is vacuous")
	}
}
