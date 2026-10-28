package changes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
)

// fakeCaps answers Capabilities per backend and counts the calls.
type fakeCaps struct {
	answers map[string]gather.CapabilitiesAnswer
	errs    map[string]error
	calls   []string
}

func (f *fakeCaps) Capabilities(_ context.Context, typ, backend string) (gather.CapabilitiesAnswer, error) {
	f.calls = append(f.calls, typ+" "+backend)
	if err := f.errs[backend]; err != nil {
		return gather.CapabilitiesAnswer{}, err
	}
	return f.answers[backend], nil
}

func capsFor(inst string, owns bool, queries map[string]bool) gather.CapabilitiesAnswer {
	b := gather.CapBackend{Backend: inst, Vocabulary: map[string]any{"owns_changes": owns}, Queries: map[string]gather.CapQuery{}}
	for q, w := range queries {
		b.Queries[q] = gather.CapQuery{Watched: w}
	}
	return gather.CapabilitiesAnswer{Backends: []gather.CapBackend{b}}
}

func dispatchCfg() *config.Config {
	cfg := &config.Config{Repos: []config.RepoConfig{{Remote: "o/r"}}}
	cfg.Watch.Issue.Queries = []string{"mine", "team", "beads-q"}
	cfg.Watch.Issue.Backends = map[string]config.WatchBackend{
		"mine":    {Instance: feedInst, OwnsChanges: true},
		"team":    {Instance: feedInst, OwnsChanges: true},
		"beads-q": {Instance: "beads"},
	}
	return cfg
}

// beadsCapsOK is the answer of the non-owning "beads" instance of dispatchCfg
// when it agrees with the configuration.
func beadsCapsOK() gather.CapabilitiesAnswer {
	return capsFor("beads", false, map[string]bool{"beads-q": true})
}

func TestCheckDispatchAgreeingConfigurationHasNoMismatchAndAsksEveryInstance(t *testing.T) {
	g := &fakeCaps{answers: map[string]gather.CapabilitiesAnswer{
		feedInst: capsFor(feedInst, true, map[string]bool{"mine": true, "team": true}),
		"beads":  beadsCapsOK(),
	}}
	if got := CheckDispatch(context.Background(), dispatchCfg(), g); len(got) != 0 {
		t.Fatalf("mismatches = %+v, want none", got)
	}
	if !reflect.DeepEqual(g.calls, []string{"issue " + feedInst, "issue beads"}) {
		t.Errorf("calls = %v, want the owning instance then the non-owning one asked", g.calls)
	}
}

func TestCheckDispatchNonOwningInstanceMismatches(t *testing.T) {
	owningOK := capsFor(feedInst, true, map[string]bool{"mine": true, "team": true})
	cases := []struct {
		name  string
		beads *fakeCaps
		kind  string
		qry   string
	}{
		{
			"watched differs", &fakeCaps{answers: map[string]gather.CapabilitiesAnswer{
				feedInst: owningOK, "beads": capsFor("beads", false, map[string]bool{"beads-q": false}),
			}},
			MismatchWatchedDiffers, "beads-q",
		},
		{
			"query not listed", &fakeCaps{answers: map[string]gather.CapabilitiesAnswer{
				feedInst: owningOK, "beads": capsFor("beads", false, map[string]bool{}),
			}},
			MismatchQueryMissing, "beads-q",
		},
		{
			"call fails", &fakeCaps{
				answers: map[string]gather.CapabilitiesAnswer{feedInst: owningOK},
				errs:    map[string]error{"beads": errors.New("daemon is down")},
			},
			MismatchInstanceUnanswered, "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := CheckDispatch(context.Background(), dispatchCfg(), tc.beads)
			if len(got) != 1 || got[0].Kind != tc.kind || got[0].Instance != "beads" || got[0].Query != tc.qry {
				t.Fatalf("mismatches = %+v, want one %s naming beads and %q", got, tc.kind, tc.qry)
			}
		})
	}
}

// A non-owning backend reports owns_changes false and that is not a mismatch.
func TestCheckDispatchNonOwningInstanceNeverChecksOwnsChanges(t *testing.T) {
	g := &fakeCaps{answers: map[string]gather.CapabilitiesAnswer{
		feedInst: capsFor(feedInst, true, map[string]bool{"mine": true, "team": true}),
		"beads":  capsFor("beads", false, map[string]bool{"beads-q": true}),
	}}
	for _, m := range CheckDispatch(context.Background(), dispatchCfg(), g) {
		if m.Kind == MismatchOwnsChangesDiffers {
			t.Fatalf("owns_changes mismatch for %+v", m)
		}
	}
}

func TestCheckDispatchReportsEachKindOfMismatch(t *testing.T) {
	cases := []struct {
		name string
		g    *fakeCaps
		kind string
		inst string
		qry  string
	}{
		{
			"instance does not answer", &fakeCaps{errs: map[string]error{feedInst: errors.New("spawn failed")}},
			MismatchInstanceUnanswered, feedInst, "",
		},
		{
			"answer without a row", &fakeCaps{answers: map[string]gather.CapabilitiesAnswer{feedInst: {
				Sources: []gather.CapSource{{Source: feedInst, Status: "degraded", Reason: "capabilities exploded"}},
			}}},
			MismatchInstanceUnanswered, feedInst, "",
		},
		{
			"owns_changes differs", &fakeCaps{answers: map[string]gather.CapabilitiesAnswer{
				feedInst: capsFor(feedInst, false, map[string]bool{"mine": true, "team": true}),
			}},
			MismatchOwnsChangesDiffers, feedInst, "",
		},
		{
			"query missing", &fakeCaps{answers: map[string]gather.CapabilitiesAnswer{
				feedInst: capsFor(feedInst, true, map[string]bool{"mine": true}),
			}},
			MismatchQueryMissing, feedInst, "team",
		},
		{
			"watched differs", &fakeCaps{answers: map[string]gather.CapabilitiesAnswer{
				feedInst: capsFor(feedInst, true, map[string]bool{"mine": true, "team": false}),
			}},
			MismatchWatchedDiffers, feedInst, "team",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.g.answers == nil {
				tc.g.answers = map[string]gather.CapabilitiesAnswer{}
			}
			tc.g.answers["beads"] = beadsCapsOK()
			got := CheckDispatch(context.Background(), dispatchCfg(), tc.g)
			if len(got) != 1 {
				t.Fatalf("mismatches = %+v, want exactly one", got)
			}
			m := got[0]
			if m.Kind != tc.kind || m.Instance != tc.inst || m.Query != tc.qry {
				t.Errorf("mismatch = %+v, want kind %s instance %s query %q", m, tc.kind, tc.inst, tc.qry)
			}
			if s := m.String(); !strings.Contains(s, tc.kind) || !strings.Contains(s, tc.inst) || !strings.Contains(s, tc.qry) {
				t.Errorf("text = %q lacks kind, instance or query", s)
			}
		})
	}
}

func TestCheckDispatchTwoOwningInstancesAreEachChecked(t *testing.T) {
	cfg := dispatchCfg()
	cfg.Watch.Issue.Queries = append(cfg.Watch.Issue.Queries, "other-q")
	cfg.Watch.Issue.Backends["other-q"] = config.WatchBackend{Instance: "other-demo", OwnsChanges: true}
	g := &fakeCaps{answers: map[string]gather.CapabilitiesAnswer{
		feedInst:     capsFor(feedInst, true, map[string]bool{"mine": true, "team": true}),
		"other-demo": capsFor("other-demo", true, map[string]bool{"other-q": false}),
		"beads":      beadsCapsOK(),
	}}
	got := CheckDispatch(context.Background(), cfg, g)
	if len(got) != 1 || got[0].Instance != "other-demo" || got[0].Kind != MismatchWatchedDiffers {
		t.Fatalf("mismatches = %+v, want one watched_differs naming other-demo", got)
	}
}

func TestStartupDispatchCheckLogsAndRecordsOncePerProcess(t *testing.T) {
	ResetStartupDispatchCheck()
	t.Cleanup(ResetStartupDispatchCheck)
	f := newFeedFixture(t, []string{"jira-q"}, nil, feedAnswer(feedIdentity, 1))
	g := &fakeCaps{answers: map[string]gather.CapabilitiesAnswer{
		feedInst: capsFor(feedInst, true, map[string]bool{"jira-q": false}),
	}}
	var warn bytes.Buffer
	f.eng.Capabilities = g
	f.eng.Warn = &warn

	if _, _, err := f.run("router"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(warn.String(), "dispatch check: watched_differs: backend "+feedInst+" query jira-q") {
		t.Errorf("warning output lacks the mismatch line:\n%s", warn.String())
	}
	v, found, err := f.st.GetMeta(MetaDispatchCheckPrefix + feedInst)
	if err != nil || !found {
		t.Fatalf("meta = %q %v %v, want the dispatch_check record", v, found, err)
	}
	var rec struct {
		At         string   `json:"at"`
		Mismatches []string `json:"mismatches"`
	}
	if err := json.Unmarshal([]byte(v), &rec); err != nil {
		t.Fatalf("meta %q is not the pinned JSON: %v", v, err)
	}
	if rec.At != "2026-10-01T12:00:00Z" || len(rec.Mismatches) != 1 || !strings.Contains(rec.Mismatches[0], "watched_differs") {
		t.Errorf("record = %+v", rec)
	}

	// The second poll of the same process does not check again.
	warn.Reset()
	if _, _, err := f.run("router"); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if len(g.calls) != 1 || strings.Contains(warn.String(), "dispatch check") {
		t.Errorf("second poll re-ran the check: calls = %v, warn = %q", g.calls, warn.String())
	}
}

func TestStartupDispatchCheckAgreementRecordsAnEmptyList(t *testing.T) {
	ResetStartupDispatchCheck()
	t.Cleanup(ResetStartupDispatchCheck)
	f := newFeedFixture(t, []string{"jira-q"}, nil, feedAnswer(feedIdentity, 1))
	f.eng.Capabilities = &fakeCaps{answers: map[string]gather.CapabilitiesAnswer{
		feedInst: capsFor(feedInst, true, map[string]bool{"jira-q": true}),
	}}
	var warn bytes.Buffer
	f.eng.Warn = &warn
	if _, _, err := f.run("router"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if strings.Contains(warn.String(), "dispatch check") {
		t.Errorf("warning for an agreeing configuration:\n%s", warn.String())
	}
	if v, _, _ := f.st.GetMeta(MetaDispatchCheckPrefix + feedInst); !strings.Contains(v, `"mismatches":[]`) {
		t.Errorf("record = %q, want an empty mismatches list", v)
	}
}

func TestStartupDispatchCheckSkipsWithoutInstancesOrReader(t *testing.T) {
	ResetStartupDispatchCheck()
	t.Cleanup(ResetStartupDispatchCheck)
	// No Capabilities reader: no call, no record, the poll still works.
	f := newFeedFixture(t, []string{"jira-q"}, nil, feedAnswer(feedIdentity, 1))
	if _, _, err := f.run("router"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if _, found, _ := f.st.GetMeta(MetaDispatchCheckPrefix + feedInst); found {
		t.Error("a record was written without a Capabilities reader")
	}
	// A watched query with no backends entry names no instance: the reader is
	// never asked.
	f = newFeedFixture(t, nil, []string{"beads-q"})
	f.eng.Cfg.Watch.Issue.Backends = map[string]config.WatchBackend{}
	g := &fakeCaps{}
	f.eng.Capabilities = g
	if _, _, err := f.run("router"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(g.calls) != 0 {
		t.Errorf("calls = %v, want none without a configured backend", g.calls)
	}
}

// A beads-only configuration is checked at start-up like an owning one: the
// mismatch is a warning and the outcome lands under the instance's meta key.
func TestStartupDispatchCheckCoversNonOwningInstances(t *testing.T) {
	ResetStartupDispatchCheck()
	t.Cleanup(ResetStartupDispatchCheck)
	f := newFeedFixture(t, nil, []string{"beads-q"})
	g := &fakeCaps{answers: map[string]gather.CapabilitiesAnswer{
		"beads": capsFor("beads", false, map[string]bool{"beads-q": false}),
	}}
	var warn bytes.Buffer
	f.eng.Capabilities = g
	f.eng.Warn = &warn
	if _, _, err := f.run("router"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !reflect.DeepEqual(g.calls, []string{"issue beads"}) {
		t.Errorf("calls = %v, want the beads instance asked", g.calls)
	}
	if !strings.Contains(warn.String(), "dispatch check: watched_differs: backend beads query beads-q") {
		t.Errorf("warning output lacks the mismatch line:\n%s", warn.String())
	}
	v, found, err := f.st.GetMeta(MetaDispatchCheckPrefix + "beads")
	if err != nil || !found || !strings.Contains(v, "watched_differs") {
		t.Errorf("meta = %q %v %v, want the recorded mismatch", v, found, err)
	}
}
