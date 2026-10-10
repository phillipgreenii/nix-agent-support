package focus

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// fixtureNow is the injected reading of the clock.
var fixtureNow = time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)

const (
	fixtureAt   = "2026-10-10T08:00:00Z"
	fixtureSelf = "me-login"
)

// fixtureConfig is a configuration with a self login, two operator identities
// (the Jira connector's display name and email) and a bead id pattern.
func fixtureConfig() *config.Config {
	return &config.Config{
		SelfLogin:     fixtureSelf,
		BeadIDPattern: `^bd-`,
		Focus: config.FocusConfig{
			OperatorIdentities: []string{"Pat Example", "pat@example.test", "Mary Jane Watson"},
		},
	}
}

// fixture is a real new-schema store plus the configuration the candidate
// computation reads, with helpers that write the entity shapes the gather
// stage stores.
type fixture struct {
	t       *testing.T
	st      *store.Store
	cfg     *config.Config
	derived map[Key][]store.XrefLink
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	return &fixture{t: t, st: store.OpenNewSchemaForTest(t), cfg: fixtureConfig(), derived: map[Key][]store.XrefLink{}}
}

// prSpec describes a stored PR entity.
type prSpec struct {
	state     string // default open
	merged    bool
	requests  []string
	ownership string // "" stores no interpretation row
	inactive  bool
}

func (f *fixture) writeEntity(k Key, facts string, active bool) {
	f.t.Helper()
	e := store.Entity{Repo: testRepo, EntityType: k.Type, EntityID: k.ID, Facts: facts, AsOf: fixtureAt, ContentHash: "h" + k.ID}
	if _, err := f.st.WriteEntityStateWithLog(e, 0, fixtureAt, active, []string{"created"}, "sync", fixtureAt); err != nil {
		f.t.Fatalf("write entity %v: %v", k, err)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// pr stores a PR entity and returns its key.
func (f *fixture) pr(id string, s prSpec) Key {
	f.t.Helper()
	if s.state == "" {
		s.state = "open"
	}
	k := Key{"pr", id}
	f.writeEntity(k, mustJSON(f.t, map[string]any{"pr_show": map[string]any{
		"state": s.state, "merged": s.merged, "review_requests": s.requests, "author": "someone",
	}}), !s.inactive)
	if s.ownership != "" {
		if err := f.st.UpsertInterpretation(store.Interpretation{
			Repo: testRepo, EntityType: "pr", EntityID: id, Ownership: s.ownership,
			Enrichment: "{}", Urgency: "{}", Dispositions: "{}", Approvals: "{}", MatchReasons: "[]",
			AsOf: fixtureAt,
		}); err != nil {
			f.t.Fatalf("interpretation of %v: %v", k, err)
		}
	}
	return k
}

// issueSpec describes a stored issue entity (a Jira issue or a bead).
type issueSpec struct {
	state     string
	category  string
	labels    []string
	assignee  string
	owner     string
	issueType string
	metadata  map[string]string
	inactive  bool
}

func (f *fixture) issue(id string, s issueSpec) Key {
	f.t.Helper()
	k := Key{"issue", id}
	show := map[string]any{"id": id, "state": s.state}
	if s.category != "" {
		show["status_category"] = s.category
	}
	if len(s.labels) > 0 {
		show["labels"] = s.labels
	}
	if s.assignee != "" {
		show["assignee"] = s.assignee
	}
	if s.owner != "" {
		show["owner"] = s.owner
	}
	if s.issueType != "" {
		show["issue_type"] = s.issueType
	}
	if len(s.metadata) > 0 {
		show["metadata"] = s.metadata
	}
	f.writeEntity(k, mustJSON(f.t, map[string]any{"issue_show": show}), !s.inactive)
	return k
}

// bead stores a pg-focus-planable labelled open bead.
func (f *fixture) labelledBead(id string) Key {
	return f.issue(id, issueSpec{state: "open", labels: []string{PlanableLabel}})
}

// link records a derived link; the fixture writes derived links per source at
// load time (a derived write replaces the whole derived set of its source).
func (f *fixture) link(from, to Key, relation string) {
	f.derived[from] = append(f.derived[from], store.XrefLink{
		Repo: testRepo, FromType: from.Type, FromID: from.ID, ToType: to.Type, ToID: to.ID,
		Relation: relation, Origin: "derived:test", FirstSeen: fixtureAt, LastConfirmed: fixtureAt,
	})
}

// external records an externally managed link now.
func (f *fixture) external(from, to Key, relation, reason string) {
	f.t.Helper()
	if err := f.st.AddExternalXref(store.XrefLink{
		Repo: testRepo, FromType: from.Type, FromID: from.ID, ToType: to.Type, ToID: to.ID,
		Relation: relation, Actor: "operator", ActedAt: fixtureAt, Reason: reason,
		FirstSeen: fixtureAt, LastConfirmed: fixtureAt,
	}); err != nil {
		f.t.Fatalf("external link: %v", err)
	}
}

func (f *fixture) hide(k Key) {
	f.t.Helper()
	if err := f.st.SetAnnotation(store.KVAnnotation{
		Repo: testRepo, EntityType: k.Type, EntityID: k.ID, Key: store.AnnotationHidden,
		Value: `{"value":true,"reason":null}`, Origin: "pg-desk", SetBy: "operator", SetAt: fixtureAt,
	}); err != nil {
		f.t.Fatalf("hide %v: %v", k, err)
	}
}

func (f *fixture) inputs() Inputs {
	f.t.Helper()
	for from, ls := range f.derived {
		if err := f.st.ReplaceDerivedXrefs(testRepo, from.Type, from.ID, ls); err != nil {
			f.t.Fatalf("derived links of %v: %v", from, err)
		}
	}
	f.derived = map[Key][]store.XrefLink{}
	in, err := Load(f.st, f.cfg, testRepo, fixtureNow)
	if err != nil {
		f.t.Fatalf("Load: %v", err)
	}
	return in
}

func (f *fixture) set() CandidateSet { return Candidates(f.inputs()) }

// byKey indexes a set's candidates.
func byKey(s CandidateSet) map[Key]Candidate {
	out := map[Key]Candidate{}
	for _, c := range s.Candidates {
		out[c.Key] = c
	}
	return out
}

func wantCandidates(t *testing.T, s CandidateSet, want ...Key) {
	t.Helper()
	got := byKey(s)
	for _, k := range want {
		if _, ok := got[k]; !ok {
			t.Errorf("%v is not a candidate; set = %v", k, s.Candidates)
		}
	}
	if len(got) != len(want) {
		t.Errorf("candidate count = %d, want %d: %v", len(got), len(want), s.Candidates)
	}
}

func wantNone(t *testing.T, s CandidateSet) {
	t.Helper()
	if len(s.Candidates) != 0 {
		t.Errorf("candidates = %v, want none", s.Candidates)
	}
}

func hasNotice(s CandidateSet, code string) bool {
	for _, n := range s.Notices {
		if n.Code == code {
			return true
		}
	}
	return false
}
