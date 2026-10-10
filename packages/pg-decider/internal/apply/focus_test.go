package apply

// Tests of the focus apply path (daily-focus design section 8): the update
// argv for status and defer clearing, the live re-read before a hold or a
// release, the children read, the post-hold claim restoration, skipped-stale,
// and the dedup lookup over focus_beads_query. Every helper here is prefixed
// "focus" because sibling packets add test files to this package.

import (
	"bytes"
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-decider/internal/action"
	"github.com/phillipgreenii/pg-decider/internal/config"
	"github.com/phillipgreenii/pg-decider/internal/view"
	"github.com/phillipgreenii/pg-decider/internal/workitem"
)

const focusBeadID = "bd-focus-1"

func focusHoldAction(transition string) action.Action {
	return action.Action{
		Op: action.OpUpdate, Kind: "focus-item", Target: str(focusBeadID), Rule: "focus.item",
		Fields: action.Fields{Status: "deferred", Metadata: map[string]string{"focus_hold": "struck"}},
		Facts:  map[string]any{"transition": transition},
	}
}

func focusReleaseAction() action.Action {
	return action.Action{
		Op: action.OpUpdate, Kind: "focus-item", Target: str(focusBeadID), Rule: "focus.item",
		Fields: action.Fields{Status: "open", ClearDefer: true, Metadata: map[string]string{"focus_hold": "released"}},
		Facts:  map[string]any{"transition": "release"},
	}
}

func focusMarkerOnlyAction() action.Action {
	return action.Action{
		Op: action.OpUpdate, Kind: "focus-item", Target: str(focusBeadID), Rule: "focus.item",
		Fields: action.Fields{Metadata: map[string]string{"focus_hold": "released"}},
		Facts:  map[string]any{"transition": "release", "marker_only": true},
	}
}

// focusLive is the stdout of `issue show <id> --fresh` for a bead in the given
// shape, served from the origin.
func focusLive(state, assignee, marker string) string {
	md := ""
	if marker != "" {
		md = `,"metadata":{"focus_hold":"` + marker + `"}`
	}
	return fmt.Sprintf(`{"result":{"id":%q,"state":%q,"assignee":%q%s,"served_from":"origin","stale":false,"as_of":"2026-10-10T00:00:00Z"}}`,
		focusBeadID, state, assignee, md)
}

const focusNoChildren = `{"result":{"children":[]}}`

func focusShowRule(stdout string) respRule {
	return respRule{Match: "pg-connector issue show " + focusBeadID, Stdout: stdout}
}

func focusChildrenRule(stdout string) respRule {
	return respRule{Match: "pg-connector issue children " + focusBeadID, Stdout: stdout}
}

func focusRun(t *testing.T, d *double, cfg *config.Config, errBuf *bytes.Buffer, v *view.View, acts ...action.Action) Result {
	t.Helper()
	env := testEnv(d, cfg)
	if errBuf != nil {
		env.Stderr = errBuf
	}
	if v == nil {
		v = prView()
	}
	return Run(context.Background(), Input{Type: "pr", ID: prID, View: v, Actions: acts, Env: env})
}

func focusUpdates(d *double) []string {
	var out []string
	for _, l := range d.lines() {
		if strings.HasPrefix(l, "pg-connector issue update") || strings.HasPrefix(l, "pg-connector issue comment") {
			out = append(out, l)
		}
	}
	return out
}

func focusLines(d *double, prefix string) []string {
	var out []string
	for _, l := range d.lines() {
		if strings.HasPrefix(l, prefix) {
			out = append(out, l)
		}
	}
	return out
}

// ---- argv ---------------------------------------------------------------------

func TestUpdateArgsRendersStatusAndClearDeferInOrder(t *testing.T) {
	cases := []struct {
		name   string
		env    Env
		f      action.Fields
		reopen bool
		want   []string
	}{
		{
			name: "hold",
			f:    action.Fields{Status: "deferred", Metadata: map[string]string{"focus_hold": "struck"}},
			want: []string{"issue", "update", "b1", "--status", "deferred", "--metadata", "focus_hold=struck"},
		},
		{
			name: "release with the backend flag last",
			env:  Env{Config: &config.Config{AgentTrackerBackend: "trk"}},
			f: action.Fields{
				Status: "open", ClearDefer: true, Metadata: map[string]string{"focus_hold": "released"},
				Priority: "P1", Title: "t", Description: "d", AddLabels: []string{"x"},
			},
			want: []string{
				"issue", "update", "b1", "--status", "open", "--clear-defer", "--metadata", "focus_hold=released",
				"--add-label", "x", "--priority", "P1", "--title", "t", "--description", "d", "--backend", "trk",
			},
		},
		{
			name: "marker only update has neither flag",
			f:    action.Fields{Metadata: map[string]string{"focus_hold": "released"}},
			want: []string{"issue", "update", "b1", "--metadata", "focus_hold=released"},
		},
		{
			name:   "a reopen keeps its argv",
			f:      action.Fields{Title: "t"},
			reopen: true,
			want:   []string{"issue", "update", "b1", "--status", "open", "--clear-assignee", "--clear-defer", "--title", "t"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := updateArgs(c.env, "b1", c.f, c.reopen); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("argv\n got %q\nwant %q", got, c.want)
			}
		})
	}
}

// ---- the live read precedes the write -------------------------------------------

func TestFocusApplyLiveReadPrecedesWrite(t *testing.T) {
	cfg := &config.Config{AgentTrackerBackend: "trk"}
	cases := []struct {
		name string
		act  action.Action
		live string
		want []string
	}{
		{
			name: "hold",
			act:  focusHoldAction("hold"),
			live: focusLive("open", "", ""),
			want: []string{
				"pg-connector issue show bd-focus-1 --fresh --backend trk",
				"pg-connector issue children bd-focus-1 --backend trk",
				"pg-connector issue update bd-focus-1 --status deferred --metadata focus_hold=struck --backend trk",
				"pg-connector issue show bd-focus-1 --fresh --backend trk",
				"pg-desk issue refresh bd-focus-1",
			},
		},
		{
			name: "terminal hold",
			act:  focusHoldAction("hold_terminal"),
			live: focusLive("open", "", "released"),
			want: []string{
				"pg-connector issue show bd-focus-1 --fresh --backend trk",
				"pg-connector issue children bd-focus-1 --backend trk",
				"pg-connector issue update bd-focus-1 --status deferred --metadata focus_hold=struck --backend trk",
				"pg-connector issue show bd-focus-1 --fresh --backend trk",
				"pg-desk issue refresh bd-focus-1",
			},
		},
		{
			name: "release reads no children and no second time",
			act:  focusReleaseAction(),
			live: focusLive("deferred", "", "struck"),
			want: []string{
				"pg-connector issue show bd-focus-1 --fresh --backend trk",
				"pg-connector issue update bd-focus-1 --status open --clear-defer --metadata focus_hold=released --backend trk",
				"pg-desk issue refresh bd-focus-1",
			},
		},
		{
			name: "marker-only release",
			act:  focusMarkerOnlyAction(),
			live: focusLive("open", "", "struck"),
			want: []string{
				"pg-connector issue show bd-focus-1 --fresh --backend trk",
				"pg-connector issue update bd-focus-1 --metadata focus_hold=released --backend trk",
				"pg-desk issue refresh bd-focus-1",
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := newDouble(t, focusShowRule(c.live), focusChildrenRule(focusNoChildren))
			r := focusRun(t, d, cfg, nil, nil, c.act)
			if got := d.lines(); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("call log\n got %q\nwant %q", got, c.want)
			}
			if r.ExitCode != 0 || !reflect.DeepEqual(outcomes(r), []Outcome{OutcomeApplied}) || r.Events[0].WorkItemID != focusBeadID {
				t.Fatalf("result %+v", r)
			}
		})
	}
}

// ---- abandoned writes -----------------------------------------------------------

func TestFocusApplyAbandonsWhenLiveClaimed(t *testing.T) {
	cases := []struct {
		name     string
		act      action.Action
		show     string
		children string
		wantErr  string
	}{
		{"hold live-claimed", focusHoldAction("hold"), focusLive("open", "worker-1", ""), focusNoChildren, "claimed by worker-1"},
		{"hold live-in-progress", focusHoldAction("hold"), focusLive("in_progress", "", ""), focusNoChildren, "in_progress"},
		{"hold live-closed", focusHoldAction("hold"), focusLive("closed", "", ""), focusNoChildren, "now closed"},
		{"hold live-held", focusHoldAction("hold"), focusLive("deferred", "", "struck"), focusNoChildren, "now deferred"},
		{"terminal hold live-claimed", focusHoldAction("hold_terminal"), focusLive("open", "worker-1", ""), focusNoChildren, "claimed"},
		{"hold live-children", focusHoldAction("hold"), focusLive("open", "", ""), `{"result":{"children":[{"id":"bd-focus-1.1"},{"id":"bd-focus-1.2"}]}}`, "open children (bd-focus-1.1, bd-focus-1.2)"},
		{"release live-claimed", focusReleaseAction(), focusLive("deferred", "worker-1", "struck"), focusNoChildren, "claimed"},
		{"release live-closed", focusReleaseAction(), focusLive("closed", "", "struck"), focusNoChildren, "now closed"},
		{"release live-already-open", focusReleaseAction(), focusLive("open", "", "struck"), focusNoChildren, "not deferred"},
		{"release live-marker-gone", focusReleaseAction(), focusLive("deferred", "", ""), focusNoChildren, "marker"},
		{"marker-only live-claimed", focusMarkerOnlyAction(), focusLive("open", "worker-1", "struck"), focusNoChildren, "claimed"},
		{"marker-only live-already-released", focusMarkerOnlyAction(), focusLive("open", "", "released"), focusNoChildren, "marker"},
		{"marker-only live-deferred", focusMarkerOnlyAction(), focusLive("deferred", "", "struck"), focusNoChildren, "not open"},
		{"live-error", focusHoldAction("hold"), `{"error":{"code":"unavailable","message":"down"}}`, focusNoChildren, "live read"},
		{"live-not-from-origin", focusHoldAction("hold"), strings.Replace(focusLive("open", "", ""), `"origin"`, `"cache"`, 1), focusNoChildren, `"cache", not from the origin`},
		{"live-marked-stale", focusHoldAction("hold"), strings.Replace(focusLive("open", "", ""), `"stale":false`, `"stale":true`, 1), focusNoChildren, "stale"},
		{"live-no-served-from", focusHoldAction("hold"), `{"result":{"id":"bd-focus-1","state":"open"}}`, focusNoChildren, "not from the origin"},
		{"live-undecodable", focusHoldAction("hold"), `not json`, focusNoChildren, "decode"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			show := focusShowRule(c.show)
			if strings.Contains(c.show, `"error"`) {
				show.Exit = 1
			}
			d := newDouble(t, show, focusChildrenRule(c.children))
			r := focusRun(t, d, nil, nil, nil, c.act)

			if got := focusUpdates(d); len(got) != 0 {
				t.Fatalf("an abandoned write must write no update and no comment, got %q", got)
			}
			if !reflect.DeepEqual(outcomes(r), []Outcome{OutcomeSkippedStale}) {
				t.Fatalf("outcomes %v, want skipped-stale (events %+v)", outcomes(r), r.Events)
			}
			if err := r.Events[0].Err; err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("error %v, want it to mention %q", err, c.wantErr)
			}
			if r.ExitCode != 0 {
				t.Errorf("a skipped-stale outcome is not a failure, exit %d", r.ExitCode)
			}
			lines := d.lines()
			if last := lines[len(lines)-1]; last != "pg-desk issue refresh "+focusBeadID {
				t.Errorf("an issue refresh must follow the abandoned write, last exec %q", last)
			}
		})
	}
}

func TestFocusApplyChildrenReadFailsClosed(t *testing.T) {
	for _, c := range []struct{ name, stdout, want string }{
		{"error", `{"error":{"code":"unknown_op","message":"no children"}}`, "children"},
		{"no children member", `{"result":{}}`, "no children member"},
	} {
		t.Run(c.name, func(t *testing.T) {
			ch := focusChildrenRule(c.stdout)
			if strings.Contains(c.stdout, `"error"`) {
				ch.Exit = 1
			}
			d := newDouble(t, focusShowRule(focusLive("open", "", "")), ch)
			r := focusRun(t, d, nil, nil, nil, focusHoldAction("hold"))
			if got := focusUpdates(d); len(got) != 0 {
				t.Fatalf("the hold must not be written: %q", got)
			}
			if !reflect.DeepEqual(outcomes(r), []Outcome{OutcomeFailed}) || r.ExitCode != 2 {
				t.Fatalf("a failed children read counts failed (exit 2): %v exit %d", outcomes(r), r.ExitCode)
			}
			if err := r.Events[0].Err; err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error %v", err)
			}
		})
	}
}

func TestFocusSkippedStaleBreaksALaterRequiresPriorAction(t *testing.T) {
	d := newDouble(t, focusShowRule(focusLive("closed", "", "")))
	later := action.Action{Op: action.OpUpdate, Kind: "anchor", Rule: "focus.item", Target: str("wb-9"), RequiresPrior: true}
	r := focusRun(t, d, nil, nil, nil, focusHoldAction("hold"), later)
	if !reflect.DeepEqual(outcomes(r), []Outcome{OutcomeSkippedStale, OutcomeSkippedDependency}) {
		t.Fatalf("outcomes %v", outcomes(r))
	}
}

func TestFocusUpdateWithoutATransitionIsAPlainWrite(t *testing.T) {
	d := newDouble(t)
	a := action.Action{Op: action.OpUpdate, Kind: "focus-item", Rule: "other", Target: str(focusBeadID), Fields: action.Fields{Title: "t"}}
	r := focusRun(t, d, nil, nil, nil, a)
	want := []string{"pg-connector issue update bd-focus-1 --title t", "pg-desk issue refresh bd-focus-1"}
	if got := d.lines(); !reflect.DeepEqual(got, want) || !reflect.DeepEqual(outcomes(r), []Outcome{OutcomeApplied}) {
		t.Fatalf("execs %q outcomes %v", got, outcomes(r))
	}
}

func TestFocusHoldOrReleaseWithoutATargetFails(t *testing.T) {
	d := newDouble(t)
	a := focusHoldAction("hold")
	a.Target = nil
	r := focusRun(t, d, nil, nil, nil, a)
	if !reflect.DeepEqual(outcomes(r), []Outcome{OutcomeFailed}) || len(d.calls()) != 0 {
		t.Fatalf("outcomes %v calls %q", outcomes(r), d.lines())
	}
}

func TestFocusWriteFailureIsAFailedActionWithNoRestore(t *testing.T) {
	d := newDouble(
		t,
		focusShowRule(focusLive("open", "", "")), focusChildrenRule(focusNoChildren),
		respRule{Match: "pg-connector issue update", Exit: 1, Stdout: `{"error":{"code":"unavailable","message":"down"}}`},
	)
	r := focusRun(t, d, nil, nil, nil, focusHoldAction("hold"))
	if !reflect.DeepEqual(outcomes(r), []Outcome{OutcomeFailed}) || r.ExitCode != 2 {
		t.Fatalf("outcomes %v exit %d", outcomes(r), r.ExitCode)
	}
	if got := focusLines(d, "pg-connector issue show"); len(got) != 1 {
		t.Fatalf("a failed hold must not read the bead again: %q", d.lines())
	}
}

// ---- the post-hold read -----------------------------------------------------------

func TestFocusHoldReReadsAfterWriteAndRestoresClaim(t *testing.T) {
	t.Run("a claim landed in the window", func(t *testing.T) {
		d := newDouble(
			t,
			respRule{Match: "pg-connector issue show", Nth: 1, Stdout: focusLive("open", "", "")},
			respRule{Match: "pg-connector issue show", Nth: 2, Stdout: focusLive("deferred", "worker-1", "struck")},
			focusChildrenRule(focusNoChildren),
		)
		var stderr bytes.Buffer
		r := focusRun(t, d, nil, &stderr, nil, focusHoldAction("hold"))
		want := []string{
			"pg-connector issue show bd-focus-1 --fresh",
			"pg-connector issue children bd-focus-1",
			"pg-connector issue update bd-focus-1 --status deferred --metadata focus_hold=struck",
			"pg-connector issue show bd-focus-1 --fresh",
			"pg-connector issue update bd-focus-1 --status in_progress",
			"pg-desk issue refresh bd-focus-1",
		}
		if got := d.lines(); !reflect.DeepEqual(got, want) {
			t.Fatalf("call log\n got %q\nwant %q", got, want)
		}
		if !strings.Contains(stderr.String(), "claimed, left running") {
			t.Errorf("stderr %q lacks the claimed, left running report", stderr.String())
		}
		if !reflect.DeepEqual(outcomes(r), []Outcome{OutcomeApplied}) || r.ExitCode != 0 {
			t.Fatalf("the hold write itself was applied: %v exit %d", outcomes(r), r.ExitCode)
		}
		// The restore touches neither the marker nor the assignee.
		restore := d.lines()[4]
		for _, banned := range []string{"--metadata", "--clear-assignee", "--clear-defer"} {
			if strings.Contains(restore, banned) {
				t.Errorf("restore %q must not carry %s", restore, banned)
			}
		}
	})

	t.Run("no claim in the window issues no restore", func(t *testing.T) {
		d := newDouble(
			t,
			respRule{Match: "pg-connector issue show", Nth: 1, Stdout: focusLive("open", "", "")},
			respRule{Match: "pg-connector issue show", Nth: 2, Stdout: focusLive("deferred", "", "struck")},
			focusChildrenRule(focusNoChildren),
		)
		var stderr bytes.Buffer
		r := focusRun(t, d, nil, &stderr, nil, focusHoldAction("hold"))
		if got := focusLines(d, "pg-connector issue update"); len(got) != 1 {
			t.Fatalf("exactly one update expected, got %q", got)
		}
		if strings.Contains(stderr.String(), "left running") || !reflect.DeepEqual(outcomes(r), []Outcome{OutcomeApplied}) {
			t.Fatalf("stderr %q outcomes %v", stderr.String(), outcomes(r))
		}
	})

	t.Run("the restore itself fails", func(t *testing.T) {
		d := newDouble(
			t,
			respRule{Match: "pg-connector issue show", Nth: 1, Stdout: focusLive("open", "", "")},
			respRule{Match: "pg-connector issue show", Nth: 2, Stdout: focusLive("deferred", "worker-1", "struck")},
			focusChildrenRule(focusNoChildren),
			respRule{Match: "--status in_progress", Exit: 1, Stdout: `{"error":{"code":"unavailable","message":"down"}}`},
		)
		r := focusRun(t, d, nil, nil, nil, focusHoldAction("hold"))
		if !reflect.DeepEqual(outcomes(r), []Outcome{OutcomeFailed}) || r.ExitCode != 2 {
			t.Fatalf("a stranded claim that cannot be restored is a failure: %v exit %d", outcomes(r), r.ExitCode)
		}
		if err := r.Events[0].Err; err == nil || !strings.Contains(err.Error(), "stranded a claim") {
			t.Fatalf("error %v", err)
		}
	})

	t.Run("the post-write read fails", func(t *testing.T) {
		d := newDouble(
			t,
			respRule{Match: "pg-connector issue show", Nth: 1, Stdout: focusLive("open", "", "")},
			respRule{Match: "pg-connector issue show", Nth: 2, Exit: 1, Stdout: `{"error":{"code":"unavailable","message":"down"}}`},
			focusChildrenRule(focusNoChildren),
		)
		var stderr bytes.Buffer
		r := focusRun(t, d, nil, &stderr, nil, focusHoldAction("hold"))
		if !reflect.DeepEqual(outcomes(r), []Outcome{OutcomeApplied}) || r.ExitCode != 0 {
			t.Fatalf("the hold stands: %v exit %d", outcomes(r), r.ExitCode)
		}
		if !strings.Contains(stderr.String(), "read after the write failed") {
			t.Errorf("stderr %q", stderr.String())
		}
	})
}

// ---- the dedup lookup for the focus kind --------------------------------------------

const focusKey = "pr:" + prID + ":focus-item"

func focusMint() action.Action {
	return action.Action{
		Op: action.OpCreate, Kind: "focus-item", Rule: "focus.item",
		Fields: action.Fields{
			Title: "Focus " + prID, IssueType: "task", Labels: []string{"focus-item"},
			Metadata: map[string]string{"dedup_key": focusKey, "source_type": "pr", "source_id": prID},
		},
		Facts: map[string]any{"transition": "mint"},
	}
}

// focusListing is `issue list --query focus-beads` answering with the given
// beads (id, state), each carrying the focus dedup key of the PR.
func focusListing(key string, beads ...[2]string) string {
	var ents []string
	for _, b := range beads {
		ents = append(ents, fmt.Sprintf(`{"id":%q,"state":%q,"metadata":{"dedup_key":%q}}`, b[0], b[1], key))
	}
	return `{"entities":[` + strings.Join(ents, ",") + `],"present_ids":[],"sources":[]}`
}

func focusLinkedView() *view.View {
	v := prView()
	v.Links = []view.Link{{
		Type: "issue", ID: "bd-focus-9", Relation: "source", State: "open", Labels: []string{"focus-item"},
		Metadata: map[string]string{"dedup_key": focusKey, "source_type": "pr", "source_id": prID},
	}}
	return v
}

func TestFocusDedupMatrix(t *testing.T) {
	cfg := &config.Config{FocusBeadsQuery: "focus-beads"}
	for _, linked := range []bool{false, true} {
		for _, lists := range []bool{false, true} {
			for _, state := range []string{"open", "in_progress", "blocked", "deferred", "closed"} {
				name := fmt.Sprintf("view-link=%v/lookup-lists=%v/status=%s", linked, lists, state)
				t.Run(name, func(t *testing.T) {
					var beads [][2]string
					if lists {
						beads = append(beads, [2]string{"bd-focus-9", state})
					}
					d := newDouble(t, respRule{Match: "pg-connector issue list --query focus-beads", Stdout: focusListing(focusKey, beads...)})
					v := prView()
					if linked {
						v = focusLinkedView()
					}
					r := focusRun(t, d, cfg, nil, v, focusMint())

					creates := focusLines(d, "pg-connector issue create")
					refreshes := focusLines(d, "pg-desk issue refresh")
					switch {
					case lists:
						// A hit in ANY status, closed included, is never recreated.
						if len(creates) != 0 || !reflect.DeepEqual(outcomes(r), []Outcome{OutcomeDeduped}) || r.Events[0].WorkItemID != "bd-focus-9" {
							t.Fatalf("a listed bead must dedup: creates %q outcomes %v", creates, outcomes(r))
						}
						wantRefresh := 1
						if linked {
							wantRefresh = 0 // the view already shows the bead
						}
						if len(refreshes) != wantRefresh || (wantRefresh == 1 && refreshes[0] != "pg-desk issue refresh bd-focus-9") {
							t.Fatalf("refreshes %q, want %d", refreshes, wantRefresh)
						}
					default:
						if len(creates) != 1 || !reflect.DeepEqual(outcomes(r), []Outcome{OutcomeApplied}) {
							t.Fatalf("an unlisted bead is minted once: creates %q outcomes %v", creates, outcomes(r))
						}
					}
					// A dedup hit writes nothing else: no update, no comment, no close.
					if lists {
						for _, l := range d.lines() {
							if strings.HasPrefix(l, "pg-connector issue") && !strings.HasPrefix(l, "pg-connector issue list") {
								t.Errorf("a dedup hit wrote %q", l)
							}
						}
					}
				})
			}
		}
	}
}

func TestFocusDedupUsesFocusBeadsQueryNotWorkBeads(t *testing.T) {
	d := newDouble(
		t,
		respRule{Match: "issue list --query work-beads", Stdout: focusListing(focusKey, [2]string{"wb-WRONG", "open"})},
		respRule{Match: "issue list --query focus-beads", Stdout: focusListing(focusKey, [2]string{"bd-focus-9", "closed"})},
	)
	cfg := &config.Config{FocusBeadsQuery: "focus-beads", AgentTrackerBackend: "trk"}
	r := focusRun(t, d, cfg, nil, nil, focusMint())
	if got := focusLines(d, "pg-connector issue list"); !reflect.DeepEqual(got, []string{"pg-connector issue list --query focus-beads --backend trk"}) {
		t.Fatalf("lists %q", got)
	}
	if r.Events[0].WorkItemID != "bd-focus-9" || r.Events[0].Outcome != OutcomeDeduped {
		t.Fatalf("event %+v", r.Events[0])
	}
}

func TestFocusCreateWithoutAFocusBeadsQueryFailsClosed(t *testing.T) {
	for _, cfg := range []*config.Config{nil, {}, {AgentTrackerBackend: "trk"}} {
		d := newDouble(t)
		r := focusRun(t, d, cfg, nil, nil, focusMint())
		if len(d.calls()) != 0 {
			t.Fatalf("nothing may be exec'd, not even a work-beads fallback: %q", d.lines())
		}
		if !reflect.DeepEqual(outcomes(r), []Outcome{OutcomeFailed}) || r.ExitCode != 2 {
			t.Fatalf("outcomes %v exit %d", outcomes(r), r.ExitCode)
		}
		if err := r.Events[0].Err; err == nil || !strings.Contains(err.Error(), "focus_beads_query") {
			t.Fatalf("error %v", err)
		}
	}
}

func TestFocusCreateFailsClosedOnAFailedOrTruncatedLookup(t *testing.T) {
	cfg := &config.Config{FocusBeadsQuery: "focus-beads"}
	for name, rule := range map[string]respRule{
		"list failed":    {Match: "issue list", Exit: 1, Stdout: `{"error":{"code":"unavailable","message":"down"}}`},
		"list truncated": {Match: "issue list", Stdout: `{"entities":[],"present_ids":[],"sources":[],"truncated":true}`},
	} {
		t.Run(name, func(t *testing.T) {
			d := newDouble(t, rule)
			r := focusRun(t, d, cfg, nil, nil, focusMint())
			if got := focusLines(d, "pg-connector issue create"); len(got) != 0 {
				t.Fatalf("created without a trustworthy lookup: %q", got)
			}
			if !reflect.DeepEqual(outcomes(r), []Outcome{OutcomeFailed}) {
				t.Fatalf("outcomes %v", outcomes(r))
			}
		})
	}
}

func TestFocusCreateWithoutADedupKeyFailsClosed(t *testing.T) {
	d := newDouble(t)
	a := focusMint()
	a.Fields.Metadata = map[string]string{"source_id": prID}
	r := focusRun(t, d, &config.Config{FocusBeadsQuery: "focus-beads"}, nil, nil, a)
	if len(d.calls()) != 0 || !reflect.DeepEqual(outcomes(r), []Outcome{OutcomeFailed}) {
		t.Fatalf("calls %q outcomes %v", d.lines(), outcomes(r))
	}
}

func TestFocusMintCreatesAndRefreshesWhenNothingIsListed(t *testing.T) {
	d := newDouble(t)
	cfg := &config.Config{FocusBeadsQuery: "focus-beads"}
	r := focusRun(t, d, cfg, nil, nil, focusMint())
	want := []string{
		"pg-connector issue list --query focus-beads",
		"pg-connector issue create --title Focus " + prID + " --issue-type task --labels focus-item --metadata dedup_key=" + focusKey + " --metadata source_id=" + prID + " --metadata source_type=pr",
		"pg-desk issue refresh wb-2",
	}
	if got := d.lines(); !reflect.DeepEqual(got, want) {
		t.Fatalf("execs\n got %q\nwant %q", got, want)
	}
	if !reflect.DeepEqual(outcomes(r), []Outcome{OutcomeApplied}) {
		t.Fatalf("outcomes %v", outcomes(r))
	}
}

func TestFocusDedupLookupOfOtherKindsStillUsesWorkBeads(t *testing.T) {
	d := newDouble(t)
	cfg := &config.Config{FocusBeadsQuery: "focus-beads"}
	focusRun(t, d, cfg, nil, nil, create("review-pr", "t", "pr:"+prID+":review-pr", nil))
	if got := focusLines(d, "pg-connector issue list"); !reflect.DeepEqual(got, []string{"pg-connector issue list --query work-beads"}) {
		t.Fatalf("lists %q", got)
	}
}

func TestFocusKindConstantMatchesTheContract(t *testing.T) {
	// apply restates the rule's transition strings; the kind it keys on must be
	// the contract's.
	if string(workitem.KindFocusItem) != "focus-item" {
		t.Fatalf("kind %q", workitem.KindFocusItem)
	}
}
