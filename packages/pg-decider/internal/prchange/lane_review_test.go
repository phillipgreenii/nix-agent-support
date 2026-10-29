package prchange

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-decider/internal/parity"
	"github.com/phillipgreenii/pg-decider/internal/workitem"
)

// The review-rule scenarios of the lane suite (docket pg2-6mo45.3, packet
// P4c-2; the runs are described in lane_world_test.go). Each one runs the real
// composed command over the real pg-desk, pg-decider and pg-connector against
// the tracker (the fake bd, or a real one), with a PR whose
// head the test moves (the fake forge answers from the world's fixture, which
// the test rewrites between runs), and reads the answer from the tracker's own
// listing: the review items of the PR, in every state, as bd lists them.

// sha is a 40-character head sha made of one letter, so its short form
// (workitem.ShortHead) differs from every other letter's.
func sha(c byte) string { return strings.Repeat(string(c), workitem.ShortHeadLen*5+5) }

const (
	bdShimWritesFile = "writes"
	bdShimLogFile    = "bd-writes.log"
	bdShimFailFile   = "fail.nth"
)

// writeBDShim writes a passthrough `bd` into a new directory and returns the
// directory (to be put first on the connector's PATH). Every write verb (create,
// update, close, comment, reopen) is counted and logged to <ctl>/bd-writes.log as
// "<n> <verb>"; when <ctl>/fail.nth holds n, write number n is NOT run: the shim
// prints a diagnostic and exits 1, the way a bd that failed its write would. Every
// other call, and every other write, is delegated to the bd under it (real or fake) unchanged.
func writeBDShim(t *testing.T, tools e2eTools, ctl string) string {
	t.Helper()
	dir := t.TempDir()
	script := `#!/bin/sh
CTL=` + shq(ctl) + `
verb=""
skip=0
for a in "$@"; do
  if [ "$skip" -eq 1 ]; then skip=0; continue; fi
  case "$a" in -C|--actor) skip=1; continue;; esac
  verb=$a
  break
done
case "$verb" in
create|update|close|comment|reopen)
  n=0
  if [ -f "$CTL/` + bdShimWritesFile + `" ]; then read -r n < "$CTL/` + bdShimWritesFile + `"; fi
  n=$((n + 1))
  printf '%s\n' "$n" > "$CTL/` + bdShimWritesFile + `"
  if [ -f "$CTL/` + bdShimFailFile + `" ] && [ "$(cat "$CTL/` + bdShimFailFile + `")" = "$n" ]; then
    printf '%s %s FAILED\n' "$n" "$verb" >> "$CTL/` + bdShimLogFile + `"
    echo "bd shim: write $n ($verb) failed on request" >&2
    exit 1
  fi
  printf '%s %s\n' "$n" "$verb" >> "$CTL/` + bdShimLogFile + `";;
esac
exec ` + shq(tools.bd) + ` "$@"
`
	must(t, os.WriteFile(filepath.Join(dir, "bd"), []byte(script), 0o755))
	return dir
}

// failWrite makes the Nth bd write from now on fail (see writeBDShim); 0 clears
// the injection. The write counter restarts at 0.
func (w *e2eWorld) failWrite(n int) {
	w.t.Helper()
	if w.shimCtl == "" {
		w.t.Fatal("this world has no bd passthrough")
	}
	for _, f := range []string{bdShimWritesFile, bdShimLogFile, bdShimFailFile} {
		if err := os.Remove(filepath.Join(w.shimCtl, f)); err != nil && !os.IsNotExist(err) {
			w.t.Fatal(err)
		}
	}
	if n > 0 {
		must(w.t, os.WriteFile(filepath.Join(w.shimCtl, bdShimFailFile), []byte(fmt.Sprintf("%d\n", n)), 0o644))
	}
}

// bdWrites is the shim's log of bd writes since the last failWrite.
func (w *e2eWorld) bdWrites() []string {
	w.t.Helper()
	b, err := os.ReadFile(filepath.Join(w.shimCtl, bdShimLogFile))
	if os.IsNotExist(err) {
		return nil
	}
	must(w.t, err)
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

// ---- reading the tracker -----------------------------------------------------

type reviewItem struct {
	id, status, assignee, head, title, key string
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

// reviews is every review-pr item of the tracker, whatever its state, by id.
func (w *e2eWorld) reviews() []reviewItem {
	w.t.Helper()
	var out []reviewItem
	for _, it := range w.tr.items() {
		if itemKind(it) != "review-pr" {
			continue
		}
		md := metaOf(it)
		out = append(out, reviewItem{
			id: str(it["id"]), status: str(it["status"]), assignee: str(it["assignee"]),
			head: str(md["head_sha"]), title: str(it["title"]), key: str(md["dedup_key"]),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].id < out[j].id })
	return out
}

// reviewByID is the review item with this id.
func (w *e2eWorld) reviewByID(id string) reviewItem {
	w.t.Helper()
	for _, r := range w.reviews() {
		if r.id == id {
			return r
		}
	}
	w.t.Fatalf("no review item %s in the tracker", id)
	return reviewItem{}
}

// onlyReview is the one review item of the tracker; it fails when there are
// not exactly one.
func (w *e2eWorld) onlyReview() reviewItem {
	w.t.Helper()
	rs := w.reviews()
	if len(rs) != 1 {
		w.t.Fatalf("the tracker holds %d review items, want exactly one: %+v", len(rs), rs)
	}
	return rs[0]
}

// anchors is every anchor (merge-request) bead of the tracker.
func (w *e2eWorld) anchors() []map[string]any {
	w.t.Helper()
	var out []map[string]any
	for _, it := range w.tr.items() {
		if itemKind(it) == "anchor" {
			out = append(out, it)
		}
	}
	return out
}

// keysAreUnique fails when two beads carry the same dedup_key.
func (w *e2eWorld) keysAreUnique() {
	w.t.Helper()
	seen := map[string]string{}
	for _, it := range w.tr.items() {
		k := str(metaOf(it)["dedup_key"])
		if k == "" {
			continue
		}
		if other, dup := seen[k]; dup {
			w.t.Errorf("the beads %s and %s carry the same dedup_key %q", other, str(it["id"]), k)
		}
		seen[k] = str(it["id"])
	}
}

// wantReviewAt checks one review item is at head, with the title and the dedup
// key suffix of that head.
func (w *e2eWorld) wantReviewAt(r reviewItem, head string) {
	w.t.Helper()
	if r.head != head {
		w.t.Errorf("review item %s is at head %q, want %q", r.id, r.head, head)
	}
	if want := " @ " + workitem.ShortHead(head); !strings.HasSuffix(r.title, want) {
		w.t.Errorf("review item %s has the title %q, want one ending %q", r.id, r.title, want)
	}
	if _, _, k, suffix, ok := workitem.ParseKey(r.key); !ok || k != workitem.KindReviewPR || suffix != ":"+head {
		w.t.Errorf("review item %s has the dedup key %q, want a review-pr key with the suffix %q", r.id, r.key, ":"+head)
	}
}

// ---- changing the tracker and the forge ------------------------------------

// The tracker changes below are what a reviewer or an operator does to a work
// item between two runs. Each is followed by the refresh of that item in
// pg-desk's store that the issue feed makes in production (the composed command
// itself refreshes the PR and lists the tracker, but the decider reads a linked
// item's state from the store), so the next run sees the change; the scenario
// of a change that lands DURING a run does not call them.

func (w *e2eWorld) feed(id string) {
	w.t.Helper()
	w.r.mustDesk("issue", "refresh", id)
}

func (w *e2eWorld) claim(id string) {
	w.t.Helper()
	w.tr.run("update", id, "--status", "in_progress", "--assignee", "reviewer")
	w.feed(id)
}

func (w *e2eWorld) setStatus(id, status string) {
	w.t.Helper()
	w.tr.run("update", id, "--status", status)
	w.feed(id)
}

func (w *e2eWorld) closeItem(id string) {
	w.t.Helper()
	w.tr.run("close", id, "--reason", "reviewed", "--force")
	w.feed(id)
}

func (w *e2eWorld) setMeta(id, k, v string) {
	w.t.Helper()
	w.tr.run("update", id, "--set-metadata", k+"="+v)
	w.feed(id)
}

// bdLine is the shell command that runs the throwaway tracker's bd with args,
// as the hook of the forge shim needs it.
func (tr *tracker) bdLine(args ...string) string {
	return "cd " + shq(tr.dir) + " && env " + strings.Join(quoteAll(tr.env), " ") + " " + shq(tr.bd) + " " + strings.Join(quoteAll(args), " ")
}

// setPR rewrites the forge fake's answers for the PR with the given id after
// change mutates its fixture.
func (w *e2eWorld) setPR(id string, change func(p *parity.PRFixture)) {
	w.t.Helper()
	found := false
	for i := range w.r.fx.PRs {
		if fmt.Sprintf("%s#%d", "acme/api", w.r.fx.PRs[i].Number) == id {
			change(&w.r.fx.PRs[i])
			found = true
		}
	}
	if !found {
		w.t.Fatalf("no fixture PR %s", id)
	}
	script := parity.FakeConnectorScript(w.r.fx, filepath.Join(w.r.dir, "base-calls.log"))
	base := filepath.Join(w.r.dir, "pg-connector-base")
	tmp := base + ".new"
	must(w.t, os.WriteFile(tmp, []byte(script), 0o755))
	must(w.t, os.Rename(tmp, base))
}

func (w *e2eWorld) setHead(id, head string) {
	w.setPR(id, func(p *parity.PRFixture) { p.HeadSHA = head })
}

// applyRetrying runs the composed command in apply mode and returns the result,
// running it once more when the busy host made the tracker listing overrun.
func (w *e2eWorld) applyRetrying(args ...string) result {
	w.t.Helper()
	res := w.r.change(args...)
	// The connector gives one backend listing 40 s; on a host that is busy the
	// real bd can overrun it, and the run then fails closed having written
	// nothing (the fail-closed scenarios prove that), so it is run once more.
	// The same overrun ends a run as transient (exit 32, "unavailable": the router
	// would re-run the delivery), which is what a retry here stands for.
	if (res.code == ExitDecideFailed && strings.Contains(res.stderr, "work-item lookup is not reachable")) ||
		(res.code == ExitDecideUnavailable && strings.Contains(res.stderr, "work-item lookup could not be read")) {
		w.t.Logf("%v: the tracker listing overran the connector's deadline, running it once more:\n%s", args, res.stderr)
		res = w.r.change(args...)
	}
	return res
}

// apply runs the composed command in apply mode and requires success.
func (w *e2eWorld) apply(id string, extra ...string) result {
	w.t.Helper()
	res := w.applyRetrying(append([]string{"apply", id}, extra...)...)
	if res.code != 0 {
		w.t.Fatalf("apply %s exited %d:\n%s%s", id, res.code, res.stdout, res.stderr)
	}
	return res
}

// creates is the tracker `issue create` calls for review items logged since
// call number from (the anchor is created lazily, by the first rule that needs
// it, so a run may create it as well).
func (w *e2eWorld) creates(from int) int {
	n := 0
	for _, l := range w.writes(from) {
		if strings.HasPrefix(l, "issue create --title review-pr:") {
			n++
		}
	}
	return n
}

// forced reads the PR's force_review annotation and the head it was requested at.
func (w *e2eWorld) forced(id string) (set bool, sha string) {
	w.t.Helper()
	res := w.r.mustDesk("pr", "show", id, "--json")
	var v struct {
		Annotations struct {
			ForceReview    bool    `json:"force_review"`
			ForceReviewSHA *string `json:"force_review_sha"`
		} `json:"annotations"`
	}
	must(w.t, json.Unmarshal([]byte(res.stdout), &v))
	if v.Annotations.ForceReviewSHA != nil {
		sha = *v.Annotations.ForceReviewSHA
	}
	return v.Annotations.ForceReview, sha
}

// freshWorld is one PR (acme/api#301, mine, open) at head, an empty tracker and a
// hydrated view; the first apply creates the anchor and the review item.
func freshWorld(t *testing.T, tools e2eTools, head string, shim bool) (*e2eWorld, string) {
	t.Helper()
	w := newE2EWorldWith(t, tools, e2eOpts{n: 1, head: head, shim: shim})
	return w, w.ids[0]
}

// reviewScenarios are the review-rule scenarios of the gate (design section 10,
// "Review rule (real tracker)").
func reviewScenarios(l *laneRun) {
	tools, run := l.tools, l.run
	run("a-burst-of-pushes-yields-one-review-item-for-the-head-the-burst-ended-on", func(t *testing.T) {
		w, id := freshWorld(t, tools, sha('a'), false)
		w.apply(id)
		first := w.onlyReview()
		w.wantReviewAt(first, sha('a'))
		start := w.callCount()
		for _, c := range []byte{'b', 'c', 'd'} {
			w.setHead(id, sha(c))
			w.apply(id)
		}
		if n := w.creates(start); n != 0 {
			t.Errorf("the burst made %d tracker creates, want none", n)
		}
		last := w.onlyReview()
		if last.id != first.id {
			t.Errorf("the burst replaced the item %s with %s, want one item refreshed in place", first.id, last.id)
		}
		w.wantReviewAt(last, sha('d'))
		if last.status != "open" {
			t.Errorf("the refreshed item is %s, want open", last.status)
		}
		w.keysAreUnique()
	})

	run("a-claimed-review-item-at-the-current-head-blocks-a-second", func(t *testing.T) {
		w, id := freshWorld(t, tools, sha('a'), false)
		w.apply(id)
		item := w.onlyReview()
		w.claim(item.id)
		before := w.tr.snapshot()
		start := w.callCount()
		w.apply(id)
		if got := w.reviews(); len(got) != 1 {
			t.Fatalf("a claimed item at the current head did not block a second: %+v", got)
		}
		if got := w.tr.snapshot(); sameSnapshot(before, got) != "" {
			t.Errorf("the run wrote to the tracker: %s", sameSnapshot(before, got))
		}
		if wr := w.writes(start); len(wr) != 0 {
			t.Errorf("the run made tracker writes: %v", wr)
		}
	})

	run("an-item-created-at-one-head-updated-to-another-and-closed-blocks-a-re-create", func(t *testing.T) {
		w, id := freshWorld(t, tools, sha('a'), false)
		w.apply(id)
		item := w.onlyReview()
		w.setHead(id, sha('b'))
		w.apply(id)
		w.wantReviewAt(w.reviewByID(item.id), sha('b'))
		// The update rewrote head_sha and the key together. Put the key back at
		// the first head, so that a rule that tested the KEY would see no item for
		// the current head and create one; the metadata still says the second.
		w.setMeta(item.id, "dedup_key", w.reviewByID(item.id).key[:strings.LastIndex(w.reviewByID(item.id).key, ":")+1]+sha('a'))
		w.closeItem(item.id)
		start := w.callCount()
		w.apply(id)
		if n := w.creates(start); n != 0 {
			t.Errorf("the closed item at the current head did not block a re-create: %d creates", n)
		}
		got := w.reviews()
		if len(got) != 1 || got[0].status != "closed" || got[0].head != sha('b') {
			t.Errorf("review items after the run: %+v, want the one closed item at the second head", got)
		}
	})

	run("legacy-suffix-less-open-and-closed-review-items-are-recognized", func(t *testing.T) {
		w, id := freshWorld(t, tools, sha('b'), false)
		legacy := func(head string) string {
			// Written before the dedup key carried a head suffix and the title a
			// short head.
			return w.tr.create("review-pr: "+id, "task", "", map[string]string{
				"repo": "acme/api", "pr_number": "301", "head_sha": head, "ownership": "mine",
				"dedup_key": "pr:" + id + ":review-pr",
			})
		}
		old := legacy(sha('a'))
		w.closeItem(old)
		current := legacy(sha('b'))
		before := w.tr.snapshot()
		for i := 0; i < 2; i++ {
			w.apply(id)
		}
		if n := len(w.reviews()); n != 2 {
			t.Fatalf("a legacy open item at the current head did not block a create: %d review items", n)
		}
		for itemID, snap := range before {
			if strings.Contains(snap, `"review-pr`) {
				if after := w.tr.snapshot()[itemID]; after != snap {
					t.Errorf("legacy review item %s was rewritten: %s", itemID, after)
				}
			}
		}
		// The open one is reviewed and closed: a legacy CLOSED item at the current
		// head is just as much a review of that head.
		w.closeItem(current)
		w.apply(id)
		if n := len(w.reviews()); n != 2 {
			t.Fatalf("a legacy closed item at the current head did not block a create: %d review items", n)
		}
		// The head moves: neither closed legacy item is reopened, and one new
		// item, with a suffixed key and title, is created for it.
		w.setHead(id, sha('c'))
		start := w.callCount()
		w.apply(id)
		if n := w.creates(start); n != 1 {
			t.Errorf("the new head made %d creates, want one: %v", n, w.writes(start))
		}
		rs := w.reviews()
		if len(rs) != 3 {
			t.Fatalf("review items: %+v, want the two legacy ones and one new", rs)
		}
		for _, r := range rs {
			switch r.id {
			case old, current:
				if r.status != "closed" {
					t.Errorf("legacy item %s is %s, want it left closed", r.id, r.status)
				}
			default:
				w.wantReviewAt(r, sha('c'))
				if r.status != "open" {
					t.Errorf("the new item is %s, want open", r.status)
				}
			}
		}
	})

	run("force-review-with-only-a-claimed-or-closed-same-head-item-creates-one-new-item-and-consumes-the-flag-only-on-success", func(t *testing.T) {
		for _, state := range []string{"claimed", "closed"} {
			t.Run(state, func(t *testing.T) {
				if l.parallel {
					t.Parallel() // each case builds a world of its own
				}
				w, id := freshWorld(t, tools, sha('a'), true)
				w.apply(id)
				item := w.onlyReview()
				if state == "claimed" {
					w.claim(item.id)
				} else {
					w.closeItem(item.id)
				}
				w.r.mustDesk("pr", "force-review", id)
				if set, at := w.forced(id); !set || at != sha('a') {
					t.Fatalf("setup: force_review is %v at %q", set, at)
				}
				// The create fails (the first write of this run is the review
				// item's create): the run is partial and the flag stays.
				w.failWrite(1)
				res := w.r.change("apply", id)
				if res.code != ExitDecidePartial {
					t.Fatalf("a run whose create failed exited %d, want %d:\n%s", res.code, ExitDecidePartial, res.stderr)
				}
				if log := w.bdWrites(); len(log) != 1 || !strings.HasSuffix(log[0], "create FAILED") {
					t.Fatalf("the injected failure did not hit the review create: %v\nstderr: %s\ncalls:\n%s", log, res.stderr, w.r.ctlRead("calls.log"))
				}
				if got := w.reviews(); len(got) != 1 {
					t.Fatalf("a failed create left %d review items, want the original one", len(got))
				}
				if set, _ := w.forced(id); !set {
					t.Error("the flag was consumed although the create failed")
				}
				// The rerun succeeds: exactly one new item, and only now the flag goes.
				w.failWrite(0)
				w.apply(id)
				rs := w.reviews()
				if len(rs) != 2 {
					t.Fatalf("the forced review made %d review items, want the original and one new: %+v", len(rs), rs)
				}
				for _, r := range rs {
					if r.id != item.id {
						w.wantReviewAtForced(r, sha('a'))
					}
				}
				if set, _ := w.forced(id); set {
					t.Error("the flag is still set after the create succeeded")
				}
				if got := w.r.annotation(id, "force_review_consumed"); got != sha('a') {
					t.Errorf("the consumed head is %q, want %q", got, sha('a'))
				}
				// And it was one-shot: a further run creates nothing.
				start := w.callCount()
				w.apply(id)
				if n := w.creates(start); n != 0 || len(w.reviews()) != 2 {
					t.Errorf("a run after the flag was consumed made %d creates (%d review items)", n, len(w.reviews()))
				}
				w.keysAreUnique()
			})
		}
	})

	run("a-claim-that-lands-between-the-listing-and-the-update-leaves-the-item-unchanged", func(t *testing.T) {
		w, id := freshWorld(t, tools, sha('a'), false)
		w.apply(id)
		item := w.onlyReview()
		w.setHead(id, sha('b'))
		// The claim lands when the apply step first reads the item live: after the
		// decider listed (and planned the refresh), before it writes. The hook
		// waits for the decider's apply run so that no earlier read triggers it.
		w.r.ctlWrite("hook.issue-show", "#!/bin/sh\ngrep -q '^apply ' "+shq(filepath.Join(w.r.ctl, "decider.log"))+" || exit 0\nrm -f "+shq(filepath.Join(w.r.ctl, "hook.issue-show"))+"\n"+w.tr.bdLine("update", item.id, "--status", "in_progress", "--assignee", "reviewer")+" >/dev/null\n")
		start := w.callCount()
		res := w.r.change("apply", id)
		if _, err := os.Stat(filepath.Join(w.r.ctl, "hook.issue-show")); err == nil {
			t.Fatalf("the claim never landed (the hook did not run); the run exited %d:\n%s", res.code, res.stderr)
		}
		got := w.reviewByID(item.id)
		if got.status != "in_progress" || got.assignee != "reviewer" {
			t.Fatalf("setup: the claim is not on the item: %+v", got)
		}
		if got.head != sha('a') || !strings.HasSuffix(got.title, " @ "+workitem.ShortHead(sha('a'))) || got.key != item.key {
			t.Errorf("the claimed item was rewritten under its reviewer: %+v, was %+v", got, item)
		}
		if n := len(w.reviews()); n != 1 {
			t.Errorf("the abandoned refresh left %d review items", n)
		}
		for _, l := range w.writes(start) {
			if strings.HasPrefix(l, "issue update "+item.id) {
				t.Errorf("the run wrote to the claimed item: %s", l)
			}
		}
		// The next run sees the claimed item at an older head and starts the next
		// reviewer on the new one.
		w.apply(id)
		rs := w.reviews()
		if len(rs) != 2 {
			t.Fatalf("review items after the next run: %+v, want the claimed one and one for the new head", rs)
		}
		for _, r := range rs {
			if r.id == item.id {
				if r.head != sha('a') || r.status != "in_progress" {
					t.Errorf("the claimed item changed: %+v", r)
				}
			} else {
				w.wantReviewAt(r, sha('b'))
			}
		}
	})

	run("a-reopened-pr-with-several-closed-review-items-reopens-none-and-creates-none-for-a-reviewed-head", func(t *testing.T) {
		w, id := freshWorld(t, tools, sha('a'), false)
		w.apply(id)
		first := w.onlyReview()
		w.claim(first.id)
		w.closeItem(first.id)
		w.setHead(id, sha('b'))
		w.apply(id)
		var second reviewItem
		for _, r := range w.reviews() {
			if r.id != first.id {
				second = r
			}
		}
		if second.id == "" || second.head != sha('b') {
			t.Fatalf("setup: no review item for the second head: %+v", w.reviews())
		}
		w.closeItem(second.id)
		// The PR is closed (the anchor goes with it) and then reopened.
		w.setPR(id, func(p *parity.PRFixture) { p.State = "closed" })
		w.apply(id)
		if a := w.anchors(); len(a) != 1 || str(a[0]["status"]) != "closed" {
			t.Fatalf("setup: the closed PR's anchor is %v, want closed", a)
		}
		w.setPR(id, func(p *parity.PRFixture) { p.State = "open" })
		start := w.callCount()
		w.apply(id)
		if a := w.anchors(); len(a) != 1 || str(a[0]["status"]) != "open" {
			t.Errorf("the reopened PR's anchor is %v, want open (the reopen rule ran)", a)
		}
		if n := w.creates(start); n != 0 {
			t.Errorf("the reopened PR made %d creates for a reviewed head", n)
		}
		rs := w.reviews()
		if len(rs) != 2 {
			t.Fatalf("review items after the reopen: %+v", rs)
		}
		for _, r := range rs {
			if r.status != "closed" {
				t.Errorf("review item %s was reopened: %s", r.id, r.status)
			}
		}
	})

	run("a-deferred-review-item-is-left-alone-and-does-not-block-a-create", func(t *testing.T) {
		w, id := freshWorld(t, tools, sha('a'), false)
		w.apply(id)
		item := w.onlyReview()
		w.setStatus(item.id, "deferred")
		w.setHead(id, sha('b'))
		deferred := w.tr.snapshot()[item.id]
		w.apply(id)
		rs := w.reviews()
		if len(rs) != 2 {
			t.Fatalf("a deferred item blocked the create or was refreshed in place: %+v", rs)
		}
		for _, r := range rs {
			if r.id == item.id {
				if r.status != "deferred" || r.head != sha('a') {
					t.Errorf("the deferred item changed: %+v", r)
				}
			} else {
				w.wantReviewAt(r, sha('b'))
			}
		}
		if after := w.tr.snapshot()[item.id]; after != deferred {
			t.Errorf("the deferred item was written to:\nbefore %s\nafter  %s", deferred, after)
		}
		start := w.callCount()
		w.apply(id)
		if wr := w.writes(start); len(wr) != 0 {
			t.Errorf("a further run made tracker writes: %v", wr)
		}
	})
}

// wantReviewAtForced checks a review item a forced review created at head: its
// head, and a key that names the head and carries a request nonce after it.
func (w *e2eWorld) wantReviewAtForced(r reviewItem, head string) {
	w.t.Helper()
	if r.head != head {
		w.t.Errorf("the forced review item %s is at head %q, want %q", r.id, r.head, head)
	}
	if _, _, k, suffix, ok := workitem.ParseKey(r.key); !ok || k != workitem.KindReviewPR || !strings.HasPrefix(suffix, ":"+head+":") {
		w.t.Errorf("the forced review item %s has the dedup key %q, want a review-pr key with the suffix %q and a nonce", r.id, r.key, ":"+head+":<nonce>")
	}
	if r.status != "open" {
		w.t.Errorf("the forced review item %s is %s, want open", r.id, r.status)
	}
}
