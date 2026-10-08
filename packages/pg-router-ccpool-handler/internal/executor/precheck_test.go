package executor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/beads"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/config"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/eventlog"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/item"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/roles"
)

// fakePR is a scripted PRReader that records every call so a test can prove
// which reads ran (and that none of them wrote anything: the interface has no
// write method).
type fakePR struct {
	merged     bool
	mergedErr  error
	pending    PendingReview
	pendingErr error
	calls      []string
}

func (f *fakePR) PRMerged(_ context.Context, id string) (bool, error) {
	f.calls = append(f.calls, "merged "+id)
	return f.merged, f.mergedErr
}

func (f *fakePR) PendingReview(_ context.Context, id string) (PendingReview, error) {
	f.calls = append(f.calls, "pending "+id)
	return f.pending, f.pendingErr
}

func reviewItem() item.Item {
	return item.Item{ID: "zr-r", Type: "review-pr", Metadata: map[string]any{
		"repo": "o/r", "pr_number": float64(120058), "head_sha": "abc",
	}}
}

func openBead() *beads.Issue   { return &beads.Issue{ID: "zr-r", Status: "open"} }
func closedBead() *beads.Issue { return &beads.Issue{ID: "zr-r", Status: "closed"} }

func TestPrecheckReview_SkipReasons(t *testing.T) {
	cfg := fastCfg()
	role := pinnedHeadReviewRole(cfg)
	tests := []struct {
		name      string
		bead      *beads.Issue
		pr        *fakePR
		want      string
		wantMsg   string
		wantCalls []string
	}{
		{
			name:      "bead closed: no PR read at all",
			bead:      closedBead(),
			pr:        &fakePR{},
			want:      SkipBeadClosed,
			wantMsg:   "review bead already closed",
			wantCalls: nil,
		},
		{
			name:      "PR merged: pending review not read",
			bead:      openBead(),
			pr:        &fakePR{merged: true},
			want:      SkipPRMerged,
			wantMsg:   "PR already merged",
			wantCalls: []string{"merged o/r#120058"},
		},
		{
			name:      "pending review holds content for the head",
			bead:      openBead(),
			pr:        &fakePR{pending: PendingReview{Pending: true, Stale: false}},
			want:      SkipPendingReview,
			wantMsg:   "pending review already covers the head",
			wantCalls: []string{"merged o/r#120058", "pending o/r#120058"},
		},
	}
	seen := map[string]bool{}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			logs := captureLog(t)
			logPath := filepath.Join(t.TempDir(), "events.jsonl")
			w, err := eventlog.New(logPath)
			if err != nil {
				t.Fatal(err)
			}
			defer w.Close()
			deps := Deps{Cfg: cfg, PR: tc.pr, Log: w}

			got, skipped := PrecheckReview(context.Background(), DispatchContext{Role: role, Item: reviewItem()}, deps, tc.bead)

			if !skipped || got.Reason != tc.want {
				t.Fatalf("PrecheckReview = (%+v, %v), want skip with reason %q", got, skipped, tc.want)
			}
			if strings.Join(tc.pr.calls, "|") != strings.Join(tc.wantCalls, "|") {
				t.Fatalf("PR reads = %v, want %v", tc.pr.calls, tc.wantCalls)
			}
			if !strings.Contains(logs.String(), tc.wantMsg) || !strings.Contains(logs.String(), "reason="+tc.want) {
				t.Fatalf("log lacks the distinct message %q / reason %q: %s", tc.wantMsg, tc.want, logs.String())
			}
			rec, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(rec), `"kind":"precheck_skip"`) || !strings.Contains(string(rec), `"reason":"`+tc.want+`"`) {
				t.Fatalf("event log lacks a precheck_skip record for %q: %s", tc.want, rec)
			}
			seen[got.Reason] = true
		})
	}
	if len(seen) != 3 {
		t.Fatalf("the three skip cases must carry three DISTINCT reasons, got %v", seen)
	}
}

// TestPrecheckReview_LaunchPathUnchanged: an open bead, an open PR, and either
// no pending review or a stale one (content only for an older head) all
// proceed to launch.
func TestPrecheckReview_LaunchPathUnchanged(t *testing.T) {
	cfg := fastCfg()
	role := pinnedHeadReviewRole(cfg)
	for name, pending := range map[string]PendingReview{
		"no pending review":            {},
		"pending review stale at head": {Pending: true, Stale: true},
	} {
		t.Run(name, func(t *testing.T) {
			pr := &fakePR{pending: pending}
			got, skipped := PrecheckReview(context.Background(), DispatchContext{Role: role, Item: reviewItem()}, Deps{Cfg: cfg, PR: pr}, openBead())
			if skipped {
				t.Fatalf("must launch, got skip %+v", got)
			}
			if want := "merged o/r#120058|pending o/r#120058"; strings.Join(pr.calls, "|") != want {
				t.Fatalf("PR reads = %v, want %q", pr.calls, want)
			}
		})
	}
}

// TestPrecheckReview_FailsOpen: no read failure ever blocks a launch.
func TestPrecheckReview_FailsOpen(t *testing.T) {
	cfg := fastCfg()
	role := pinnedHeadReviewRole(cfg)
	logs := captureLog(t)
	pr := &fakePR{mergedErr: errors.New("connector down"), pendingErr: errors.New("connector down")}
	if got, skipped := PrecheckReview(context.Background(), DispatchContext{Role: role, Item: reviewItem()}, Deps{Cfg: cfg, PR: pr}, nil); skipped {
		t.Fatalf("read failures must launch, got skip %+v", got)
	}
	if !strings.Contains(logs.String(), "could not read PR merged state") || !strings.Contains(logs.String(), "could not read pending-review state") {
		t.Fatalf("each failed read must be logged, got %s", logs.String())
	}
}

// TestPrecheckReview_OnlyTheReviewRole: another role never runs the precheck,
// even for a closed bead (its dispatch path is unchanged).
func TestPrecheckReview_OnlyTheReviewRole(t *testing.T) {
	cfg := fastCfg()
	role := pinnedHeadReviewRole(cfg)
	role.Name = "worker"
	pr := &fakePR{merged: true}
	if got, skipped := PrecheckReview(context.Background(), DispatchContext{Role: role, Item: reviewItem()}, Deps{Cfg: cfg, PR: pr}, closedBead()); skipped || len(pr.calls) != 0 {
		t.Fatalf("worker role must not be prechecked, got skip=%v %+v calls=%v", skipped, got, pr.calls)
	}
}

// TestPrecheckReview_ItemWithoutPR: a review item that names no well-formed PR
// skips the PR reads (never passes a malformed id to the CLI) and launches.
func TestPrecheckReview_ItemWithoutPR(t *testing.T) {
	cfg := fastCfg()
	role := pinnedHeadReviewRole(cfg)
	for name, md := range map[string]map[string]any{
		"no metadata":        nil,
		"flag-shaped repo":   {"repo": "--help/x", "pr_number": float64(1)},
		"zero pr_number":     {"repo": "o/r", "pr_number": float64(0)},
		"fractional pr":      {"repo": "o/r", "pr_number": 1.5},
		"non-numeric string": {"repo": "o/r", "pr_number": "12; rm"},
	} {
		t.Run(name, func(t *testing.T) {
			pr := &fakePR{merged: true}
			it := item.Item{ID: "zr-r", Metadata: md}
			if _, skipped := PrecheckReview(context.Background(), DispatchContext{Role: role, Item: it}, Deps{Cfg: cfg, PR: pr}, openBead()); skipped || len(pr.calls) != 0 {
				t.Fatalf("skipped=%v calls=%v, want a launch with no PR read", skipped, pr.calls)
			}
		})
	}
}

func TestPRIDFor_acceptsNumericForms(t *testing.T) {
	for _, n := range []any{float64(7), 7, int64(7), "7"} {
		got, ok := prIDFor(item.Item{Metadata: map[string]any{"repo": "o/r", "pr_number": n}})
		if !ok || got != "o/r#7" {
			t.Fatalf("pr_number %#v: got (%q, %v), want o/r#7", n, got, ok)
		}
	}
}

// scriptedCmd is a query.Commander returning a canned stdout per joined argv.
type scriptedCmd struct {
	out   map[string]string
	err   error
	calls []string
}

func (c *scriptedCmd) Run(_ context.Context, argv []string) ([]byte, error) {
	key := strings.Join(argv, " ")
	c.calls = append(c.calls, key)
	if c.err != nil {
		return nil, c.err
	}
	out, ok := c.out[key]
	if !ok {
		return nil, errors.New("scriptedCmd: unscripted call: " + key)
	}
	return []byte(out), nil
}

// TestConnectorReader pins the exact read-only argv and the wire parsing.
func TestConnectorReader(t *testing.T) {
	ctx := context.Background()
	show := "pg-connector pr show o/r#7"
	pend := "pg-connector pr review pending o/r#7"
	tests := []struct {
		name        string
		out         map[string]string
		merged      bool
		mergedErr   bool
		pending     PendingReview
		pendingErr  bool
		binary      string
		wantFirstOf string
	}{
		{name: "open PR, no pending review", out: map[string]string{
			show: `{"result":{"state":"open","merged":false}}`,
			pend: `{"result":{"pending":false}}`,
		}},
		{name: "merged flag", out: map[string]string{
			show: `{"result":{"state":"closed","merged":true}}`,
			pend: `{"result":{"pending":false}}`,
		}, merged: true},
		{name: "merged by state text", out: map[string]string{
			show: `{"result":{"state":"MERGED"}}`,
			pend: `{"result":{"pending":false}}`,
		}, merged: true},
		{name: "fresh pending review", out: map[string]string{
			show: `{"result":{"state":"open"}}`,
			pend: `{"result":{"pending":true,"review":{"stale":false}}}`,
		}, pending: PendingReview{Pending: true}},
		{name: "stale pending review", out: map[string]string{
			show: `{"result":{"state":"open"}}`,
			pend: `{"result":{"pending":true,"review":{"stale":true}}}`,
		}, pending: PendingReview{Pending: true, Stale: true}},
		{name: "pending without a record is an error", out: map[string]string{
			show: `{"result":{"state":"open"}}`,
			pend: `{"result":{"pending":true}}`,
		}, pendingErr: true},
		{name: "error envelope", out: map[string]string{
			show: `{"error":{"code":"unavailable","message":"boom"}}`,
			pend: `{"error":{"code":"unavailable","message":"boom"}}`,
		}, mergedErr: true, pendingErr: true},
		{name: "garbage output", out: map[string]string{show: `not json`, pend: `not json`}, mergedErr: true, pendingErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := ConnectorReader{Cmd: &scriptedCmd{out: tc.out}}
			merged, err := r.PRMerged(ctx, "o/r#7")
			if (err != nil) != tc.mergedErr || merged != tc.merged {
				t.Fatalf("PRMerged = (%v, %v), want (%v, err=%v)", merged, err, tc.merged, tc.mergedErr)
			}
			pending, err := r.PendingReview(ctx, "o/r#7")
			if (err != nil) != tc.pendingErr || pending != tc.pending {
				t.Fatalf("PendingReview = (%+v, %v), want (%+v, err=%v)", pending, err, tc.pending, tc.pendingErr)
			}
		})
	}

	t.Run("process failure is an error", func(t *testing.T) {
		r := ConnectorReader{Cmd: &scriptedCmd{err: errors.New("exit status 4")}}
		if _, err := r.PRMerged(ctx, "o/r#7"); err == nil {
			t.Fatal("want an error")
		}
	})
	t.Run("configured binary is used", func(t *testing.T) {
		c := &scriptedCmd{out: map[string]string{"/opt/pgc pr show o/r#7": `{"result":{"merged":false}}`}}
		if _, err := (ConnectorReader{Cmd: c, Binary: "/opt/pgc"}).PRMerged(ctx, "o/r#7"); err != nil {
			t.Fatalf("err = %v; calls %v", err, c.calls)
		}
	})
}

// TestDepsPRReader_defaultsToConnectorOverCmd: with no PR seam set, the
// precheck reads through the configured Commander and binary.
func TestDepsPRReader_defaultsToConnectorOverCmd(t *testing.T) {
	c := &scriptedCmd{out: map[string]string{
		"/x/pgc pr show o/r#120058":           `{"result":{"merged":true}}`,
		"/x/pgc pr review pending o/r#120058": `{"result":{"pending":false}}`,
	}}
	cfg := fastCfg()
	cfg.ConnectorBinary = "/x/pgc"
	role := pinnedHeadReviewRole(cfg)
	got, skipped := PrecheckReview(context.Background(), DispatchContext{Role: role, Item: reviewItem()}, Deps{Cfg: cfg, Cmd: c}, openBead())
	if !skipped || got.Reason != SkipPRMerged {
		t.Fatalf("got (%+v, %v), want a pr-merged skip; calls %v", got, skipped, c.calls)
	}
	for _, call := range c.calls {
		if !strings.Contains(call, " show ") && !strings.Contains(call, " pending ") {
			t.Fatalf("non-read connector call: %q", call)
		}
	}
}

// TestRefreshItemIssue returns the bead it read (so its status is not re-read)
// and nil when no bead was read.
func TestRefreshItemIssue(t *testing.T) {
	bd := &recordingRunner{out: `{"data":[{"id":"zr-r","status":"closed","metadata":{"head_sha":"new"}}]}`}
	got, iss := RefreshItemIssue(context.Background(), bd, item.Item{ID: "pg2-abc12", Metadata: map[string]any{"head_sha": "old"}})
	if iss == nil || iss.Status != "closed" || got.Metadata["head_sha"] != "new" {
		t.Fatalf("got item %+v, issue %+v", got, iss)
	}
	if len(bd.calls) != 1 {
		t.Fatalf("want exactly one bd call, got %v", bd.calls)
	}
	if _, iss := RefreshItemIssue(context.Background(), failingRunner{}, item.Item{ID: "pg2-abc12"}); iss != nil {
		t.Fatalf("a bd failure must yield no issue, got %+v", iss)
	}
	if _, iss := RefreshItemIssue(context.Background(), bd, item.Item{ID: "o/r#1"}); iss != nil {
		t.Fatalf("a non-bead item must yield no issue, got %+v", iss)
	}
}

// --- the "ready" precheck (INV-CCH-22, bead pg2-nk6th.3) ---

const readyActor = "pgii-pool__drain"

func readyRole(cfg config.Config) roles.Role {
	r := releaseRole(cfg)
	r.CCPool.Actor = readyActor
	r.CCPool.Precheck = roles.PrecheckReady
	return r
}

// groomed is a bead a drain worker may take: open, unassigned, groomed.
func groomed() beads.Issue {
	return beads.Issue{ID: "zr-d", Status: "open", Labels: []string{"has-acceptance-criteria", "agent-support"}}
}

func TestPrecheckReady_SkipReasons(t *testing.T) {
	cfg := fastCfg()
	now := time.Unix(0, 0)
	future := now.Add(time.Hour).UTC().Format(time.RFC3339)
	past := now.Add(-time.Hour).UTC().Format(time.RFC3339)
	with := func(f func(*beads.Issue)) *beads.Issue { i := groomed(); f(&i); return &i }
	tests := []struct {
		name string
		bead *beads.Issue
		want string // "" = proceeds
	}{
		{"unreadable bead fails open", nil, ""},
		{"open, unassigned, groomed proceeds", with(func(*beads.Issue) {}), ""},
		{"closed", with(func(i *beads.Issue) { i.Status = "closed" }), SkipBeadNotOpen},
		{"in_progress by a peer", with(func(i *beads.Issue) { i.Status = "in_progress"; i.Assignee = "someone-else" }), SkipBeadNotOpen},
		{"open but assigned to a peer", with(func(i *beads.Issue) { i.Assignee = "someone-else" }), SkipBeadClaimed},
		{"open assigned to own actor proceeds", with(func(i *beads.Issue) { i.Assignee = readyActor }), ""},
		{"human", with(func(i *beads.Issue) { i.Labels = append(i.Labels, "human") }), SkipBeadHuman},
		{"human-focus-required", with(func(i *beads.Issue) { i.Labels = append(i.Labels, "human-focus-required") }), SkipBeadHuman},
		{"needs-split-review", with(func(i *beads.Issue) { i.Labels = append(i.Labels, "needs-split-review") }), SkipBeadHuman},
		{"escalated", with(func(i *beads.Issue) { i.Labels = append(i.Labels, "escalated") }), SkipBeadHuman},
		{"no longer groomed", with(func(i *beads.Issue) { i.Labels = []string{"agent-support"} }), SkipBeadNotGroomed},
		{"deferred status", with(func(i *beads.Issue) { i.Status = "deferred" }), SkipBeadDeferred},
		{"future defer_until (DEFER-ON-EVENT)", with(func(i *beads.Issue) { i.DeferUntil = future }), SkipBeadDeferred},
		{"past defer_until proceeds", with(func(i *beads.Issue) { i.DeferUntil = past }), ""},
		{"unparseable defer_until fails open", with(func(i *beads.Issue) { i.DeferUntil = "tomorrow-ish" }), ""},
		{"blocked status", with(func(i *beads.Issue) { i.Status = "blocked" }), SkipBeadBlocked},
		{"open blocks dependency (CONVERT)", with(func(i *beads.Issue) {
			i.Dependencies = []beads.Dependency{{ID: "zr-x", Status: "open", DependencyType: "blocks"}}
		}), SkipBeadBlocked},
		{"closed blocker proceeds", with(func(i *beads.Issue) {
			i.Dependencies = []beads.Dependency{{ID: "zr-x", Status: "closed", DependencyType: "blocks"}}
		}), ""},
		{"parent-child edge proceeds", with(func(i *beads.Issue) {
			i.Dependencies = []beads.Dependency{{ID: "zr-e", Status: "open", DependencyType: "parent-child"}}
		}), ""},
		// The restart-absorb path: the live session of an earlier dispatch must
		// reach the absorb path whatever else the bead carries.
		{"own-actor in_progress proceeds", with(func(i *beads.Issue) { i.Status = "in_progress"; i.Assignee = readyActor }), ""},
		{"own-actor in_progress proceeds even if now human", with(func(i *beads.Issue) {
			i.Status = "in_progress"
			i.Assignee = readyActor
			i.Labels = append(i.Labels, "human")
		}), ""},
		{"own-actor in_progress proceeds even if deferred and blocked", with(func(i *beads.Issue) {
			i.Status = "in_progress"
			i.Assignee = readyActor
			i.DeferUntil = future
			i.Dependencies = []beads.Dependency{{ID: "zr-x", Status: "open", DependencyType: "blocks"}}
		}), ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			logs := captureLog(t)
			clk := time.Unix(0, 0)
			deps := Deps{Cfg: cfg, Now: func() time.Time { return clk }}
			got, skipped := PrecheckReady(DispatchContext{Role: readyRole(cfg), Item: item.Item{ID: "zr-d"}}, deps, tc.bead)
			if tc.want == "" {
				if skipped {
					t.Fatalf("must proceed, got skip %+v", got)
				}
				return
			}
			if !skipped || got.Reason != tc.want {
				t.Fatalf("PrecheckReady = (%+v, %v), want skip %q", got, skipped, tc.want)
			}
			if !strings.HasPrefix(got.Reason, "skipped-") {
				t.Errorf("reason %q must start with skipped- (the failure-rate alert excludes only skipped-.+)", got.Reason)
			}
			if !strings.Contains(logs.String(), "reason="+tc.want) || strings.Contains(logs.String(), "dispatch declined: precheck ") {
				t.Errorf("log must carry the reason and its own message: %s", logs.String())
			}
		})
	}
}

// TestPrecheck_selection: the role's precheck setting picks the check; with no
// setting only the role named "review" is prechecked, as before.
func TestPrecheck_selection(t *testing.T) {
	cfg := fastCfg()
	deps := Deps{Cfg: cfg, PR: &fakePR{merged: true}}
	closed := &beads.Issue{ID: "zr-d", Status: "closed"}
	mk := func(name string, p roles.Precheck) DispatchContext {
		r := releaseRole(cfg)
		r.Name = name
		r.CCPool.Precheck = p
		return DispatchContext{Role: r, Item: reviewItem()}
	}
	for _, tc := range []struct {
		name string
		dc   DispatchContext
		want string
	}{
		{"ready on any role name", mk("drain", roles.PrecheckReady), SkipBeadNotOpen},
		{"explicit review on any role name", mk("drain", roles.PrecheckReview), SkipBeadClosed},
		{"no setting, role named review (historical)", mk("review", roles.PrecheckNone), SkipBeadClosed},
		{"no setting, other role: never prechecked", mk("drain", roles.PrecheckNone), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, skipped := Precheck(context.Background(), tc.dc, deps, closed)
			if tc.want == "" {
				if skipped {
					t.Fatalf("must not precheck, got %+v", got)
				}
				return
			}
			if !skipped || got.Reason != tc.want {
				t.Fatalf("Precheck = (%+v, %v), want %q", got, skipped, tc.want)
			}
		})
	}
	if _, skipped := Precheck(context.Background(), DispatchContext{Role: roles.Role{Name: "review"}}, deps, closed); skipped {
		t.Error("a role with no ccpool block is never prechecked")
	}
}
