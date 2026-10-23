package parity

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
)

// The frozen golden (design 6.7). The parity gate used to run the old sync
// stage beside the decider and diff the two plans. The sync stage is being
// removed, so the old plans were recorded ONCE, from a pinned pre-removal
// pg-desk, as one file per scenario next to its scenario.json:
//
//	testdata/<scenario>/expected-plan.json
//
// The gate compares the decider's normalized plan with the file's
// expected_plan by exact multiset equality (Compare). The file carries three
// lists, so it is a regression snapshot a reviewer can read:
//
//   - old_plan: what the old sync stage planned, normalized (observed creates
//     for three kinds, plus the modelled adoptions). Frozen; never recomputed.
//   - explained: every difference between old_plan and expected_plan, each with
//     its exception id and reason, including the hand-pinned UNOBS entries (the
//     old side exposes no write against an existing bead, so the decider's
//     close, reopen and update entries have no observable counterpart and are
//     pinned instead).
//   - expected_plan: the full normalized set the decider MUST plan, which is
//     old_plan with the old-only differences removed and the new-only ones
//     added (ApplyExplained). A test keeps the three consistent.
//
// An intended behavior change is recorded by editing expected_plan AND adding
// its explained entry, in the same change.

//go:embed testdata
var goldenFS embed.FS

// GoldenFile is the name of a scenario's golden inside its fixture directory.
const GoldenFile = "expected-plan.json"

// MinScenarios is the fewest scenarios the gate may cover. A gate that finds
// fewer has lost fixtures, and an "equal" check over nothing must not pass.
const MinScenarios = 19

// Frozen says which tree the golden was recorded from. Every file of one
// freeze carries the same value.
type Frozen struct {
	// AgentSupportCommit is the agent-support main commit whose pg-desk,
	// pg-connector and pg-decider the old plans were recorded with.
	AgentSupportCommit string `json:"agent_support_commit"`
	// PgDesk names the pg-desk build that planned the old side (its nix store
	// path), as a second witness of the pin.
	PgDesk string `json:"pg_desk"`
}

// Golden is one scenario's frozen expected plan.
type Golden struct {
	Scenario     string     `json:"scenario"`
	FrozenFrom   Frozen     `json:"frozen_from"`
	OldPlan      []Entry    `json:"old_plan"`
	Explained    []Expected `json:"explained"`
	ExpectedPlan []Entry    `json:"expected_plan"`
}

// LoadGolden reads the embedded golden of a scenario.
func LoadGolden(sc Scenario) (*Golden, error) {
	b, err := goldenFS.ReadFile(path.Join(sc.Dir, GoldenFile))
	if err != nil {
		return nil, fmt.Errorf("parity: scenario %q has no golden (%s): %w", sc.Name, GoldenFile, err)
	}
	g, err := ParseGolden(b)
	if err != nil {
		return nil, fmt.Errorf("parity: scenario %q: %w", sc.Name, err)
	}
	if g.Scenario != sc.Name {
		return nil, fmt.Errorf("parity: golden of %q names scenario %q", sc.Name, g.Scenario)
	}
	return g, nil
}

// ParseGolden decodes and validates one expected-plan.json. Unknown members
// are an error, the pin must be present, explained entries are validated like
// any expected difference, and expected_plan must equal old_plan with the
// explained differences applied: a hand edit of one list without the other is
// refused here, not discovered later as a confusing diff.
func ParseGolden(data []byte) (*Golden, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var g Golden
	if err := dec.Decode(&g); err != nil {
		return nil, fmt.Errorf("decode golden: %w", err)
	}
	if g.Scenario == "" {
		return nil, errors.New("golden has no scenario")
	}
	if g.FrozenFrom.AgentSupportCommit == "" || g.FrozenFrom.PgDesk == "" {
		return nil, fmt.Errorf("golden of %s: frozen_from needs agent_support_commit and pg_desk", g.Scenario)
	}
	// Validate explained entries by the same rules as any expected difference.
	raw, err := json.Marshal(map[string]any{"scenarios": map[string]any{g.Scenario: g.Explained}})
	if err != nil {
		return nil, err
	}
	if _, err := ParseExpected(raw); err != nil {
		return nil, err
	}
	for _, list := range [][]Entry{g.OldPlan, g.ExpectedPlan} {
		for _, e := range list {
			if e.Entity == "" || e.Kind == "" || e.Op == "" {
				return nil, fmt.Errorf("golden of %s: an entry has an empty entity, kind or op", g.Scenario)
			}
		}
	}
	want, err := ApplyExplained(g.OldPlan, g.Explained)
	if err != nil {
		return nil, fmt.Errorf("golden of %s: %w", g.Scenario, err)
	}
	have := append([]Entry(nil), g.ExpectedPlan...)
	sortEntries(have)
	if !equalEntries(want, have) {
		return nil, fmt.Errorf("golden of %s: expected_plan is not old_plan with the explained differences applied (want %v, have %v)", g.Scenario, want, have)
	}
	if len(g.OldPlan) == 0 && len(g.ExpectedPlan) == 0 {
		return nil, fmt.Errorf("golden of %s records no entry at all: an empty golden compares 0 with 0 and proves nothing", g.Scenario)
	}
	return &g, nil
}

// ApplyExplained derives the expected plan from the old plan: each old-only
// explained entry removes one occurrence of its entry (and is an error when
// the old plan has none), each new-only one adds one. The result is sorted.
func ApplyExplained(old []Entry, explained []Expected) ([]Entry, error) {
	out := append([]Entry(nil), old...)
	for _, x := range explained {
		e := Entry{x.Entity, x.Kind, x.Op}
		switch x.Side {
		case SideOld:
			i := indexOfEntry(out, e)
			if i < 0 {
				return nil, fmt.Errorf("explained %s %s %s (old-only) is not in old_plan", x.Entity, x.Kind, x.Op)
			}
			out = append(out[:i], out[i+1:]...)
		case SideNew:
			out = append(out, e)
		default:
			return nil, fmt.Errorf("explained %s %s %s has side %q", x.Entity, x.Kind, x.Op, x.Side)
		}
	}
	sortEntries(out)
	return out, nil
}

func indexOfEntry(list []Entry, e Entry) int {
	for i, x := range list {
		if x == e {
			return i
		}
	}
	return -1
}

func equalEntries(a, b []Entry) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// LoadGoldens reads the golden of every embedded scenario, keyed by scenario
// name. A scenario without a golden is an error, so the gate cannot quietly
// cover fewer scenarios than exist. It also refuses a set frozen from more
// than one commit.
func LoadGoldens() (map[string]*Golden, error) {
	scs, err := Scenarios()
	if err != nil {
		return nil, err
	}
	out := map[string]*Golden{}
	var pin Frozen
	for _, sc := range scs {
		g, err := LoadGolden(sc)
		if err != nil {
			return nil, err
		}
		if pin == (Frozen{}) {
			pin = g.FrozenFrom
		} else if g.FrozenFrom != pin {
			return nil, fmt.Errorf("parity: golden of %s was frozen from %+v, the others from %+v: the golden is frozen once", sc.Name, g.FrozenFrom, pin)
		}
		out[sc.Name] = g
	}
	return out, nil
}

// LoadExpected returns the explained differences of every scenario, keyed by
// scenario name, read from the goldens.
func LoadExpected() (map[string][]Expected, error) {
	gs, err := LoadGoldens()
	if err != nil {
		return nil, err
	}
	out := map[string][]Expected{}
	for name, g := range gs {
		out[name] = g.Explained
	}
	return out, nil
}

// Compare is the gate's comparison: the decider's normalized plan against the
// golden's expected plan, exact and counted. An entry the golden lists that the
// decider does not plan is a STALE expected entry and fails (Side old-only: in
// the golden only); an entry the decider plans that the golden does not list
// fails too (Side new-only: the decider only). It lists no exception: the
// golden already holds every explained difference.
func Compare(scenario string, g *Golden, planned []Entry) Report {
	return Diff(scenario, g.ExpectedPlan, planned, nil)
}

// RunScenario compares one scenario's decider plan with its golden. The old
// side is not run: it is the frozen golden.
func RunScenario(ctx context.Context, env Env, sc Scenario, g *Golden) Outcome {
	out := Outcome{Scenario: sc}
	nw, err := RunNew(ctx, env, sc)
	if err != nil {
		out.Err = fmt.Errorf("decider side: %w", err)
		return out
	}
	out.Refused = nw.Refused
	ne, err := NormalizeNew(nw)
	if err != nil {
		out.Err = err
		return out
	}
	out.Report = Compare(sc.Name, g, ne)
	return out
}

// RunGate runs every scenario through the decider and compares each plan with
// its frozen golden. A scenario the real binaries cannot load is reported (Err
// wraps ErrUnsupported), never dropped. Fewer than MinScenarios scenarios is an
// error.
func RunGate(ctx context.Context, env Env) ([]Outcome, error) {
	scs, err := Scenarios()
	if err != nil {
		return nil, err
	}
	if len(scs) < MinScenarios {
		return nil, fmt.Errorf("parity: %d scenarios found, the gate requires at least %d", len(scs), MinScenarios)
	}
	gs, err := LoadGoldens()
	if err != nil {
		return nil, err
	}
	var outs []Outcome
	for _, sc := range scs {
		outs = append(outs, RunScenario(ctx, env, sc, gs[sc.Name]))
	}
	return outs, nil
}

// ---- freeze: the one-time recording of the old plans -----------------------

// Freeze records the golden: it runs every scenario through BOTH sides (the old
// sync stage of the pg-desk named by env, and the decider), requires the diff
// against explained to be clean, and returns one golden per scenario. It exists
// to be run once, before the sync stage is removed; the old-side runner (RunOld)
// and this function are deleted with it. explained is the list of documented
// differences between the two sides, keyed by scenario.
func Freeze(ctx context.Context, env Env, explained map[string][]Expected, from Frozen) ([]*Golden, error) {
	scs, err := Scenarios()
	if err != nil {
		return nil, err
	}
	for name := range explained {
		if _, err := scenarioNamed(scs, name); err != nil {
			return nil, fmt.Errorf("parity: explained differences name scenario %q, which does not exist", name)
		}
	}
	var out []*Golden
	for _, sc := range scs {
		g, err := freezeScenario(ctx, env, sc, explained[sc.Name], from)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, nil
}

func scenarioNamed(scs []Scenario, name string) (Scenario, error) {
	for _, sc := range scs {
		if sc.Name == name {
			return sc, nil
		}
	}
	return Scenario{}, errors.New("no such scenario")
}

func freezeScenario(ctx context.Context, env Env, sc Scenario, explained []Expected, from Frozen) (*Golden, error) {
	fx, err := LoadFixture(sc)
	if err != nil {
		return nil, err
	}
	old, err := RunOld(ctx, env, sc)
	if err != nil {
		return nil, fmt.Errorf("freeze %s: old side: %w", sc.Name, err)
	}
	nw, err := RunNew(ctx, env, sc)
	if err != nil {
		return nil, fmt.Errorf("freeze %s: decider side: %w", sc.Name, err)
	}
	oe, err := NormalizeOld(fx, old)
	if err != nil {
		return nil, err
	}
	ne, err := NormalizeNew(nw)
	if err != nil {
		return nil, err
	}
	rep := Diff(sc.Name, oe, ne, explained)
	if !rep.Clean() {
		return nil, fmt.Errorf("freeze %s: the old and decider plans differ in ways no explained difference covers (unexplained %v, missing %v)", sc.Name, rep.Unexplained, rep.Missing)
	}
	want, err := ApplyExplained(oe, explained)
	if err != nil {
		return nil, fmt.Errorf("freeze %s: %w", sc.Name, err)
	}
	if !equalEntries(want, ne) {
		return nil, fmt.Errorf("freeze %s: the decider plan %v is not the old plan with the explained differences applied %v", sc.Name, ne, want)
	}
	return &Golden{Scenario: sc.Name, FrozenFrom: from, OldPlan: nonNil(oe), Explained: nonNilExpected(explained), ExpectedPlan: nonNil(want)}, nil
}

func nonNil(es []Entry) []Entry {
	if es == nil {
		return []Entry{}
	}
	return es
}

func nonNilExpected(xs []Expected) []Expected {
	if xs == nil {
		return []Expected{}
	}
	return xs
}

// WriteGoldens writes each golden to <dir>/<scenario>/expected-plan.json, where
// dir is the testdata directory. It refuses to overwrite a golden: it is
// frozen once.
func WriteGoldens(dir string, gs []*Golden) error {
	for _, g := range gs {
		p := filepath.Join(dir, g.Scenario, GoldenFile)
		if _, err := os.Stat(p); err == nil {
			return fmt.Errorf("parity: %s already exists: the golden is frozen once and is edited by hand, never refrozen", p)
		}
		b, err := json.MarshalIndent(g, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(p, append(b, '\n'), 0o644); err != nil {
			return fmt.Errorf("parity: write %s: %w", p, err)
		}
	}
	return nil
}

// goldenNames lists the scenario directories that hold a golden, for tests.
func goldenNames() ([]string, error) {
	files, err := fs.Glob(goldenFS, "testdata/*/"+GoldenFile)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, f := range files {
		names = append(names, path.Base(path.Dir(f)))
	}
	sort.Strings(names)
	return names, nil
}
