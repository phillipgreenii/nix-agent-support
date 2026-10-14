package status

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/identity"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/proc"
)

// fakeChanges is a ChangeSource whose answers (and failures) the test sets.
type fakeChanges struct {
	head, oldest int64
	pending      []PendingRow
	stats        SettleStats
	consumers    []Consumer
	err          map[string]error
}

func (f fakeChanges) ChangeLog(context.Context) (int64, int64, error) {
	return f.head, f.oldest, f.err["log"]
}

func (f fakeChanges) PendingRows(context.Context) ([]PendingRow, error) {
	return f.pending, f.err["pending"]
}

func (f fakeChanges) SettleStats(context.Context) (SettleStats, error) {
	return f.stats, f.err["stats"]
}

func (f fakeChanges) Consumers(context.Context) ([]Consumer, error) {
	return f.consumers, f.err["consumers"]
}

// busyChanges is a log with two pending rows (one by reference, one by node
// id), two settle timeouts and a failure in the last hour and two consumer keys.
func busyChanges() fakeChanges {
	return fakeChanges{
		head: 120, oldest: 7,
		pending: []PendingRow{
			{ID: "example-org/example-repo#42", Awaited: []string{"files", "commits"}, DetectedAt: t0.Add(-20 * time.Second), Deadline: t0.Add(10 * time.Second)},
			{ID: "PR_node_unaliased", Awaited: []string{"conversation"}, DetectedAt: t0.Add(-5 * time.Second), Deadline: t0.Add(25 * time.Second)},
		},
		stats: SettleStats{Timeouts: 2, Failures: 1},
		consumers: []Consumer{
			{Consumer: "consumer-a", Kind: "pr", Query: "mine", AckedSeq: 100, LagRows: 20, OldestUnackedAgeSeconds: 300, SeenAt: t0.Add(-time.Minute)},
			{Consumer: "consumer-b", Kind: "ci", Query: "team", AckedSeq: 120, LagRows: 0, SeenAt: t0.Add(-2 * time.Hour)},
		},
	}
}

func TestDaemonReportCarriesTheChangesSection(t *testing.T) {
	cfg, _ := testConfig(t)
	cfg.Changes = busyChanges()
	r := build(t, cfg)
	golden(t, "status_daemon_changes.golden.json", r)

	c := r.Changes
	if c == nil || c.HeadSeq != 120 || c.OldestSeq != 7 || c.SettleTimeoutsLastHour != 2 || c.SettleFailuresLastHour != 1 {
		t.Fatalf("changes %+v", c)
	}
	if len(c.PendingRows) != 2 || c.PendingRows[0].ID != "example-org/example-repo#42" || strings.Join(c.PendingRows[0].Awaited, ",") != "files,commits" ||
		!c.PendingRows[0].DetectedAt.Equal(t0.Add(-20*time.Second)) || !c.PendingRows[0].Deadline.Equal(t0.Add(10*time.Second)) || c.PendingRows[1].ID != "PR_node_unaliased" {
		t.Errorf("pending rows %+v", c.PendingRows)
	}
	if len(c.Consumers) != 2 || c.Consumers[0].AckedSeq != 100 || c.Consumers[0].LagRows != 20 || c.Consumers[0].OldestUnackedAgeSeconds != 300 {
		t.Errorf("consumers %+v", c.Consumers)
	}
	if r.Health != Healthy || r.Health.ExitCode() != 0 || len(r.Reasons) != 0 {
		t.Errorf("the changes section is reported, not judged: %q %v", r.Health, r.Reasons)
	}
}

func TestNoChangesSourceOrAnEmptyOneOmitsTheSectionAndTheGoldenDocumentsHold(t *testing.T) {
	cfg, _ := testConfig(t)
	r := build(t, cfg) // no source at all
	if r.Changes != nil {
		t.Fatalf("changes %+v", r.Changes)
	}
	golden(t, "status_daemon.golden.json", r) // byte-for-byte what main's document was

	cfg.Changes = fakeChanges{}
	r = build(t, cfg)
	if r.Changes != nil {
		t.Fatalf("an engine with nothing to say has no section: %+v", r.Changes)
	}
	golden(t, "status_daemon.golden.json", r)
	var js bytes.Buffer
	if err := WriteJSON(&js, r); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(js.String(), `"changes"`) {
		t.Errorf("the empty section must be omitted:\n%s", js.String())
	}
	var text bytes.Buffer
	if err := WriteText(&text, r); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(text.String(), "changes") {
		t.Errorf("the text form omits it too:\n%s", text.String())
	}
}

func TestEachPartOfTheChangesSectionAloneMakesItAppear(t *testing.T) {
	for name, src := range map[string]fakeChanges{
		"head only":    {head: 3},
		"pending only": {pending: []PendingRow{{ID: "x", DetectedAt: t0, Deadline: t0}}},
		"timeouts":     {stats: SettleStats{Timeouts: 1}},
		"failures":     {stats: SettleStats{Failures: 1}},
		"consumer":     {consumers: []Consumer{{Consumer: "a", Kind: "pr", Query: "q", SeenAt: t0}}},
	} {
		t.Run(name, func(t *testing.T) {
			cfg, _ := testConfig(t)
			cfg.Changes = src
			if r := build(t, cfg); r.Changes == nil {
				t.Error("the section is missing")
			}
		})
	}
}

func TestAFailingChangeSourceCostsTheSectionAndOneReasonNotTheVerdict(t *testing.T) {
	for _, step := range []string{"log", "pending", "stats", "consumers"} {
		t.Run(step, func(t *testing.T) {
			cfg, _ := testConfig(t)
			src := busyChanges()
			src.err = map[string]error{step: errors.New("database is locked")}
			cfg.Changes = src
			r := build(t, cfg)
			if r.Changes != nil {
				t.Errorf("a failed source leaves the section out: %+v", r.Changes)
			}
			if r.Health != Healthy || r.Health.ExitCode() != 0 {
				t.Errorf("the verdict must not change: %q", r.Health)
			}
			if len(r.Reasons) != 1 || !strings.Contains(r.Reasons[0], "changes unavailable") || !strings.Contains(r.Reasons[0], "database is locked") {
				t.Errorf("reasons %v", r.Reasons)
			}
		})
	}

	// A degraded report stays degraded with the same reasons plus the one line.
	cfg, _ := testConfig(t)
	cfg.Identity = fakeIdentity{identity.Snapshot{Login: "phillipgreenii", Unauthenticated: true, AuthError: "bad credentials"}}
	src := busyChanges()
	src.err = map[string]error{"log": errors.New("boom")}
	cfg.Changes = src
	r := build(t, cfg)
	if r.Health != Degraded || r.Health.ExitCode() != 2 || len(r.Reasons) != 2 {
		t.Errorf("%q %v", r.Health, r.Reasons)
	}
}

func TestTheChangesSectionDoesNotChangeTheVerdictOrTheExitCode(t *testing.T) {
	for _, tc := range []struct {
		name string
		mut  func(*Config)
		want Health
	}{
		{"healthy", func(*Config) {}, Healthy},
		{"degraded by a query error", func(c *Config) {
			q := okQueries()
			row := q["team"]
			row.LastError = "HTTP 502"
			q["team"] = row
			c.Queries = q
		}, Degraded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, _ := testConfig(t)
			tc.mut(&cfg)
			without := build(t, cfg)
			cfg.Changes = busyChanges()
			with := build(t, cfg)
			if with.Health != tc.want || with.Health != without.Health || with.Health.ExitCode() != without.Health.ExitCode() {
				t.Errorf("with %q (%d), without %q (%d)", with.Health, with.Health.ExitCode(), without.Health, without.Health.ExitCode())
			}
			if strings.Join(with.Reasons, "|") != strings.Join(without.Reasons, "|") {
				t.Errorf("reasons differ: %v vs %v", with.Reasons, without.Reasons)
			}
		})
	}
}

func TestTheDegradedLocalReportHasNoChangesSection(t *testing.T) {
	r := Local(testStatic(proc.SupervisorRegistered, KindLaunchd), t0, "the daemon is not reachable")
	if r.Changes != nil || r.Health != Down || r.Health.ExitCode() != 3 {
		t.Errorf("%+v", r)
	}
	var js bytes.Buffer
	if err := WriteJSON(&js, r); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(js.String(), "changes") {
		t.Errorf("the local report carries no changes:\n%s", js.String())
	}
	if u := Unhealthy(testStatic(proc.SupervisorRunning, KindLaunchd), t0, "migrating"); u.Changes != nil || u.Health.ExitCode() != 2 {
		t.Errorf("%+v", u)
	}
}

func TestTextAndJSONCarryTheSameChangeValues(t *testing.T) {
	cfg, _ := testConfig(t)
	cfg.Changes = busyChanges()
	r := build(t, cfg)

	var js, text bytes.Buffer
	if err := WriteJSON(&js, r); err != nil {
		t.Fatal(err)
	}
	if err := WriteText(&text, r); err != nil {
		t.Fatal(err)
	}
	// Decode the JSON the way a client does, then check every leaf of the
	// changes object appears in the text.
	var doc struct {
		Changes json.RawMessage `json:"changes"`
	}
	if err := json.Unmarshal(js.Bytes(), &doc); err != nil || len(doc.Changes) == 0 {
		t.Fatalf("no changes in the JSON: %v\n%s", err, js.String())
	}
	var changes any
	if err := json.Unmarshal(doc.Changes, &changes); err != nil {
		t.Fatal(err)
	}
	var leaves []string
	collectLeaves(changes, &leaves)
	if len(leaves) < 25 {
		t.Fatalf("only %d leaves", len(leaves))
	}
	for _, leaf := range leaves {
		if !strings.Contains(text.String(), leaf) {
			t.Errorf("the text form lacks the JSON value %q\n%s", leaf, text.String())
		}
	}
	// The labels the renderer prints, parsed back into numbers.
	for _, want := range []string{
		"head_seq: 120", "oldest_seq: 7", "settle_timeouts_last_hour: 2", "settle_failures_last_hour: 1",
		"example-org/example-repo#42: awaited=files,commits", "acked_seq=100 lag_rows=20 oldest_unacked_age_seconds=300",
	} {
		if !strings.Contains(text.String(), want) {
			t.Errorf("text lacks %q:\n%s", want, text.String())
		}
	}
}

func TestSettleCountsSaySinceStartWhileTheDaemonIsYoungerThanAnHour(t *testing.T) {
	young, _ := testConfig(t)
	young.Started = t0.Add(-10 * time.Minute)
	young.Changes = busyChanges()
	old, _ := testConfig(t) // up for just over three hours
	old.Changes = busyChanges()

	render := func(cfg Config) string {
		var b bytes.Buffer
		if err := WriteText(&b, build(t, cfg)); err != nil {
			t.Fatal(err)
		}
		return b.String()
	}
	if got := render(young); !strings.Contains(got, "settle_timeouts_last_hour: 2 (since start)") || !strings.Contains(got, "settle_failures_last_hour: 1 (since start)") {
		t.Errorf("a young daemon says since start:\n%s", got)
	}
	if got := render(old); strings.Contains(got, "since start") {
		t.Errorf("an old daemon does not:\n%s", got)
	}
}

func TestRegisterServesTheChangesSectionThroughTheOp(t *testing.T) {
	cfg, _ := testConfig(t)
	cfg.Changes = busyChanges()
	b, err := NewBuilder(cfg)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := b.Handler(1).Handle(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	enc, _ := json.Marshal(raw)
	var back Report
	if err := json.Unmarshal(enc, &back); err != nil {
		t.Fatal(err)
	}
	if back.Changes == nil || back.Changes.HeadSeq != 120 || len(back.Changes.Consumers) != 2 || !back.Changes.PendingRows[0].Deadline.Equal(t0.Add(10*time.Second)) {
		t.Errorf("the section must survive the wire: %+v", back.Changes)
	}
}
