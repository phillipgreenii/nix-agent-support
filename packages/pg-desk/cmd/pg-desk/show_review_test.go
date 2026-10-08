package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/interpret"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// Synthetic pending-review states of a PR, as the PR hydration stores them
// (facts key review_pending, from the pg-connector
// `pr review pending` record) and as `pr show`
// renders them. All names, ids and commits are placeholders.

const (
	reviewHead = "9f3c1e2aaaa"
	reviewOld  = "4b1d7aabbbb"
)

// reviewFacts is PR facts for o/r#<n> carrying the given review state.
func reviewFacts(n int, pending string) string {
	pr := fmt.Sprintf(`"pr_show":{"id":"o/r#%d","repo":"o/r","number":%d,"title":"Change %d","state":"open","draft":false,"head_sha":%q}`, n, n, n, reviewHead)
	out := "{" + pr + `,"head_sha":"` + reviewHead + `"`
	if pending != "" {
		out += `,"review_pending":` + pending
	}
	return out + "}"
}

// reviewNowInstant is the fixed instant last_append ages are measured from.
var reviewNowInstant = time.Date(2026, 9, 29, 16, 3, 10, 0, time.UTC)

// fixReviewClock pins the clock the review line's age reads.
func fixReviewClock(t *testing.T) {
	t.Helper()
	orig := reviewNow
	t.Cleanup(func() { reviewNow = orig })
	reviewNow = interpret.FixedClock(reviewNowInstant)
}

// pendingRecord is a stored review_pending fact for a pending review. extra
// is spliced into the review object (e.g. last_append, extra_pending_reviews).
func pendingRecordWith(commit string, total, atHead int, stale bool, extra string) string {
	return fmt.Sprintf(`{"result":{"pending":true,"head_sha":%q,"as_of":"2026-09-29T14:03:10Z","review":{"review_id":"PRR_1","url":"https://code.example/o/r/pull/5#pullrequestreview-1","commit_sha":%q,"comments_total":%d,"comments_at_head":%d,"reviewed_head":false,"stale":%t%s}}}`,
		reviewHead, commit, total, atHead, stale, extra)
}

// pendingRecord is a pending review with two comments, both at the head
// unless it is stale (five comments, none at the head).
func pendingRecord(commit string, stale bool) string {
	if stale {
		return pendingRecordWith(commit, 5, 0, true, "")
	}
	return pendingRecordWith(commit, 2, 2, false, "")
}

const (
	pendingNoneRecord = `{"result":{"pending":false,"head_sha":"` + reviewHead + `","as_of":"2026-09-29T14:03:10Z"}}`
	// A record written before the per-head counts: no comments_at_head, so
	// it can be neither stale nor current. It still carries the retired
	// digest_state key.
	legacyPendingRecord = `{"result":{"pending":true,"head_sha":"` + reviewHead + `","review":{"review_id":"PRR_1","commit_sha":"` + reviewOld + `","stale":true,"digest_state":"unmarked"}}}`
	lookupFailedFacts   = `{"error":"pg-connector [pr review pending o/r#15]: exit 1: unavailable: review_pending: detection_failed: more than one pending review"}`
	reviewViewAsOfLine  = "as_of=2026-09-29T14:03:10Z (fresh)"
)

// the review states, one PR each.
var reviewStateCases = []struct {
	name     string
	n        int
	pending  string
	wantLine string
	state    string
	pendingP string // JSON of review.pending
	stale    string // JSON of review.stale
}{
	{
		"none", 11, pendingNoneRecord,
		"review: pending=no", "none", "false", "null",
	},
	{
		"current", 12, pendingRecord(reviewHead, false),
		"review: pending=yes  commit=9f3c1e2  head=9f3c1e2  comments=2 at_head=2  stale=no", "current", "true", "false",
	},
	{
		"stale", 13, pendingRecord(reviewOld, true),
		"review: pending=yes  commit=4b1d7aa  head=9f3c1e2  comments=5 at_head=0  stale=yes", "stale", "true", "true",
	},
	{
		// The review was created at an older head and extended to this one
		// 5 minutes ago: current, however old its review-level commit.
		"current-extended-with-last-append", 16, pendingRecordWith(reviewOld, 5, 2, false, `,"last_append":{"at":"2026-09-29T15:58:10Z","added":2,"head":"`+reviewHead+`"}`),
		"review: pending=yes  commit=4b1d7aa  head=9f3c1e2  comments=5 at_head=2  stale=no  last_append=5m (+2)", "current", "true", "false",
	},
	{
		"stale-with-last-append", 17, pendingRecordWith(reviewOld, 5, 0, true, `,"last_append":{"at":"2026-09-29T14:03:10Z","added":3,"head":"`+reviewOld+`"}`),
		"review: pending=yes  commit=4b1d7aa  head=9f3c1e2  comments=5 at_head=0  stale=yes  last_append=2h (+3)", "stale", "true", "true",
	},
	{
		"current-with-extra-pending-reviews", 18, pendingRecordWith(reviewHead, 5, 2, false, `,"extra_pending_reviews":1`),
		"review: pending=yes  commit=9f3c1e2  head=9f3c1e2  comments=5 at_head=2  stale=no  extra=1", "current", "true", "false",
	},
	{
		// The connector's verdict is shown, never recomputed: a different
		// review-level commit does not make a stale: false record stale.
		"verdict-not-recomputed-from-commits", 19, pendingRecordWith(reviewOld, 3, 1, false, ""),
		"review: pending=yes  commit=4b1d7aa  head=9f3c1e2  comments=3 at_head=1  stale=no", "current", "true", "false",
	},
	{
		// ...and the same commit does not make a stale: true record current.
		"verdict-not-recomputed-same-commit", 20, pendingRecordWith(reviewHead, 3, 0, true, ""),
		"review: pending=yes  commit=9f3c1e2  head=9f3c1e2  comments=3 at_head=0  stale=yes", "stale", "true", "true",
	},
	{
		"legacy-record-without-comments-at-head", 22, legacyPendingRecord,
		"review: pending=unknown (the stored pending-review record predates the per-head comment counts; run show --refresh)",
		"unknown", "null", "null",
	},
	{
		"lookup-failed", 15, lookupFailedFacts,
		"review: pending=unknown (pg-connector [pr review pending o/r#15]: exit 1: unavailable: review_pending: detection_failed: more than one pending review)",
		"unknown", "null", "null",
	},
}

func seedReviewStates(t *testing.T) {
	t.Helper()
	fixReviewClock(t)
	f := newViewFixture(t)
	for _, c := range reviewStateCases {
		f.entity("pr", fmt.Sprintf("o/r#%d", c.n), reviewFacts(c.n, c.pending), "2026-09-29T14:03:10Z", reviewHead)
	}
}

func TestTypedShowPendingReviewStatesHuman(t *testing.T) {
	seedReviewStates(t)
	for _, c := range reviewStateCases {
		t.Run(c.name, func(t *testing.T) {
			out, _, err := runTypedShowCmd(t, "pr", fmt.Sprintf("o/r#%d", c.n))
			if err != nil {
				t.Fatal(err)
			}
			want := fmt.Sprintf("pr o/r#%[1]d  Change %[1]d\n", c.n) +
				"-  open  ready  head=9f3c1e2  ci=none  " + reviewViewAsOfLine + "\n" +
				"annotations: hidden=no  wip=no  suppress=[]\n" +
				c.wantLine + "\n" +
				"links: none\n"
			if out != want {
				t.Errorf("human output:\n got: %q\nwant: %q", out, want)
			}
		})
	}
}

func TestTypedShowPendingReviewStatesJSON(t *testing.T) {
	seedReviewStates(t)
	for _, c := range reviewStateCases {
		t.Run(c.name, func(t *testing.T) {
			out, _, err := runTypedShowCmd(t, "pr", fmt.Sprintf("o/r#%d", c.n), "--json")
			if err != nil {
				t.Fatal(err)
			}
			var v struct {
				Review map[string]json.RawMessage `json:"review"`
			}
			if err := json.Unmarshal([]byte(out), &v); err != nil || v.Review == nil {
				t.Fatalf("no review object: %v\n%s", err, out)
			}
			get := func(k string) string { return strings.TrimSpace(string(v.Review[k])) }
			if get("state") != fmt.Sprintf("%q", c.state) || get("pending") != c.pendingP || get("stale") != c.stale {
				t.Errorf("review = %s", out)
			}
			if _, ok := v.Review["escalation"]; ok {
				t.Errorf("the review object carries a retired escalation field: %s", out)
			}
			switch c.state {
			case "current", "stale":
				if get("comments_total") == "" || get("comments_at_head") == "" || get("extra_pending_reviews") == "" {
					t.Errorf("a pending review must carry its comment counts and extra count: %s", out)
				}
				if get("anchored_commit") == "" || get("head_sha") != fmt.Sprintf("%q", reviewHead) || get("review_id") == "" {
					t.Errorf("a pending review must carry its commit, the head and its id: %s", out)
				}
			case "unknown":
				if get("error") == "" || (c.name != "legacy-record-without-comments-at-head" && !strings.Contains(get("error"), "detection_failed")) {
					t.Errorf("an unknown state must carry the reason: %s", out)
				}
			}
		})
	}
}

// A lookup that failed MUST NOT read as "no pending review": it is the one
// state whose pending value is neither true nor false.
func TestTypedShowFailedLookupIsNeverNone(t *testing.T) {
	seedReviewStates(t)
	out, _, err := runTypedShowCmd(t, "pr", "o/r#15")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "pending=no") || !strings.Contains(out, "pending=unknown") {
		t.Errorf("failed lookup rendered as none:\n%s", out)
	}
}

// Facts hydrated before the lookup existed are unknown, never none.
func TestTypedShowUnhydratedIsUnknown(t *testing.T) {
	newViewFixture(t)
	fixReviewClock(t)
	out, _, err := runTypedShowCmd(t, "pr", "5") // seeded without any review keys
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "review: pending=unknown (pending-review state was not looked up; run show --refresh)\n") {
		t.Errorf("unhydrated facts:\n%s", out)
	}
}

// A stored record written while the escalation fact still existed carries
// review_escalations and digest_state keys. It decodes without error and
// neither key is rendered, for every review state: the human line and the
// JSON are exactly those of a record without them, with no escalation field
// or segment anywhere.
func TestTypedShowRetiredEscalationFactIsIgnored(t *testing.T) {
	fixReviewClock(t)
	f := newViewFixture(t)
	const retired = `,"review_escalations":{"open":[{"id":"esc-77","kind":"pr","head":"` + reviewHead + `"}],"error":"issue list: exit 1"},"digest_state":"unmarked"`
	for _, c := range reviewStateCases {
		// The same PR number would collide, so the retired-key copy is n+100.
		f.entity("pr", fmt.Sprintf("o/r#%d", c.n+100), strings.TrimSuffix(reviewFacts(c.n+100, c.pending), "}")+retired+"}", "2026-09-29T14:03:10Z", reviewHead)
	}
	for _, c := range reviewStateCases {
		t.Run(c.name, func(t *testing.T) {
			human, _, err := runTypedShowCmd(t, "pr", fmt.Sprintf("o/r#%d", c.n+100))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(human, "\n"+c.wantLine+"\n") {
				t.Errorf("human output lacks %q:\n%s", c.wantLine, human)
			}
			js, _, err := runTypedShowCmd(t, "pr", fmt.Sprintf("o/r#%d", c.n+100), "--json")
			if err != nil {
				t.Fatal(err)
			}
			for _, retiredText := range []string{"escalation", "esc-77", "digest_state", "unmarked"} {
				if strings.Contains(human, retiredText) || strings.Contains(js, retiredText) {
					t.Errorf("%q leaked into the view:\nhuman:\n%s\njson:\n%s", retiredText, human, js)
				}
			}
		})
	}
}

func TestTypedShowReviewIsPROnly(t *testing.T) {
	newViewFixture(t)
	for _, tc := range [][2]string{{"issue", "bd-1"}, {"thread", "C1/1.5"}} {
		out, _, err := runTypedShowCmd(t, tc[0], tc[1], "--json")
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out, `"review"`) {
			t.Errorf("%s view carries a review object:\n%s", tc[0], out)
		}
	}
}

// Displaying the state is read-only: no pg-connector call at all without
// --refresh, and the stored entity is byte-for-byte unchanged.
func TestTypedShowReviewMakesNoStateChange(t *testing.T) {
	f := newViewFixture(t)
	f.entity("pr", "o/r#14", reviewFacts(14, pendingRecord(reviewOld, true)), "2026-09-29T14:03:10Z", reviewHead)
	dir := t.TempDir()
	installFakePGConnector(t, fmt.Sprintf(`echo "$@" >> %q/calls.log; exit 99`, dir))
	before, found, err := f.seed.GetEntity("o/r", "pr", "o/r#14")
	if err != nil || !found {
		t.Fatal(err, found)
	}
	for _, args := range [][]string{{"o/r#14"}, {"o/r#14", "--json"}} {
		if _, _, err := runTypedShowCmd(t, "pr", args...); err != nil {
			t.Fatal(err)
		}
	}
	if calls, _ := os.ReadFile(filepath.Join(dir, "calls.log")); len(calls) != 0 {
		t.Errorf("show without --refresh called pg-connector:\n%s", calls)
	}
	after, _, err := f.seed.GetEntity("o/r", "pr", "o/r#14")
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Errorf("show changed the stored entity:\n before %+v\n after  %+v", before, after)
	}
}

// ---- through the hydration seam: show --refresh with a fake pg-connector ----

// reviewConnector installs a fake pg-connector that answers one PR's
// hydration from files in dir and records every call.
type reviewConnector struct{ dir string }

func newReviewConnector(t *testing.T) *reviewConnector {
	t.Helper()
	dir := t.TempDir()
	installFakePGConnector(t, fmt.Sprintf(`D=%q
echo "$@" >> "$D/calls.log"
case "$1 $2" in
"pr show") cat "$D/pr-show.json"; exit 0;;
"pr files") echo '{"protocolVersion":1,"schemaVersion":4,"result":{"id":"PR5","files":[]}}'; exit 0;;
"pr commits") echo '{"protocolVersion":1,"schemaVersion":4,"result":{"id":"PR5","commits":[]}}'; exit 0;;
"ci list") echo '{"runs":[],"sources":[]}'; exit 0;;
"pr review") cat "$D/pending.json"; exit "$(cat "$D/pending.exit")";;
"issue list") cat "$D/issues.json"; exit 0;;
esac
exit 99`, dir))
	c := &reviewConnector{dir: dir}
	c.write("pr-show.json", fmt.Sprintf(`{"protocolVersion":1,"schemaVersion":4,"result":{"id":"PR5","repo":"o/r","number":5,"title":"Add retry","state":"open","draft":false,"head_sha":%q,"as_of":"2026-09-30T00:00:00Z"}}`, reviewHead))
	c.write("issues.json", `{"entities":[],"present_ids":[],"sources":[]}`)
	return c
}

func (c *reviewConnector) write(name, body string) {
	if err := os.WriteFile(filepath.Join(c.dir, name), []byte(body), 0o644); err != nil {
		panic(err)
	}
}

func (c *reviewConnector) pending(body string, exit int) {
	c.write("pending.json", body)
	c.write("pending.exit", fmt.Sprint(exit))
}

func (c *reviewConnector) calls() string {
	b, _ := os.ReadFile(filepath.Join(c.dir, "calls.log"))
	return string(b)
}

func refreshReviewView(t *testing.T) (human string, stderr string) {
	t.Helper()
	open, seed := seedLinkStore(t)
	withOpenSeams(t, openTestConfig("o/r"), open)
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	if _, err := seed.WriteEntityWithLog(store.Entity{Repo: "o/r", EntityType: "pr", EntityID: "o/r#5", Facts: `{}`, AsOf: "2026-09-01T00:00:00Z"},
		0, []string{"created"}, "sync", "2026-09-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	out, errOut, err := runTypedShowCmd(t, "pr", "o/r#5", "--refresh")
	if err != nil {
		t.Fatalf("show --refresh: %v (stderr %q)", err, errOut)
	}
	return out, errOut
}

func TestTypedShowRefreshHydratesPendingReviewThroughTheConnector(t *testing.T) {
	cases := []struct {
		name    string
		pending string
		exit    int
		want    string
	}{
		{
			"none", `{"protocolVersion":1,"result":{"pending":false,"head_sha":"` + reviewHead + `","as_of":"2026-09-30T00:00:00Z"}}`, 0,
			"review: pending=no",
		},
		{
			"stale", `{"protocolVersion":1,` + strings.TrimPrefix(pendingRecord(reviewOld, true), "{"), 0,
			"review: pending=yes  commit=4b1d7aa  head=9f3c1e2  comments=5 at_head=0  stale=yes",
		},
		{
			"lookup-failed", `{"protocolVersion":1,"error":{"code":"unavailable","message":"review_pending: detection_failed: truncated"}}`, 1,
			"review: pending=unknown (pg-connector [pr review pending o/r#5]: exit 1: unavailable: review_pending: detection_failed: truncated)",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newReviewConnector(t)
			c.pending(tc.pending, tc.exit)
			out, _ := refreshReviewView(t)
			if !strings.Contains(out, tc.want+"\n") {
				t.Errorf("view after refresh:\n%s\nwant line %q", out, tc.want)
			}
			// The lookup is the connector's own verb, called once; nothing is written.
			calls := c.calls()
			if got := strings.Count(calls, "pr review pending o/r#5"); got != 1 {
				t.Errorf("pr review pending called %d times:\n%s", got, calls)
			}
			if strings.Contains(calls, "pending-review-escalations") {
				t.Errorf("the retired escalation query was made:\n%s", calls)
			}
			for _, bad := range []string{"review submit", "issue create", "issue update", "issue comment", "issue close"} {
				if strings.Contains(calls, bad) {
					t.Errorf("display made a state change (%s):\n%s", bad, calls)
				}
			}
		})
	}
}

// The view's JSON carries the connector's counts, last_append and extra count
// as stored, and tolerates the retired digest_state and all_marked keys.
func TestTypedShowReviewJSONFieldsAndRetiredKeys(t *testing.T) {
	f := newViewFixture(t)
	fixReviewClock(t)
	rec := pendingRecordWith(reviewOld, 5, 2, false,
		`,"extra_pending_reviews":2,"digest_state":"unmarked","all_marked":false,"last_append":{"at":"2026-09-29T15:58:10Z","added":2,"head":"`+reviewHead+`"}`)
	f.entity("pr", "o/r#31", reviewFacts(31, rec), "2026-09-29T14:03:10Z", reviewHead)
	out, _, err := runTypedShowCmd(t, "pr", "o/r#31", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Review struct {
			State               string `json:"state"`
			CommentsTotal       *int   `json:"comments_total"`
			CommentsAtHead      *int   `json:"comments_at_head"`
			Stale               *bool  `json:"stale"`
			ExtraPendingReviews *int   `json:"extra_pending_reviews"`
			LastAppend          *struct {
				At    string `json:"at"`
				Added int    `json:"added"`
				Head  string `json:"head"`
			} `json:"last_append"`
		} `json:"review"`
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatal(err)
	}
	r := v.Review
	if r.State != "current" || r.CommentsTotal == nil || *r.CommentsTotal != 5 || r.CommentsAtHead == nil || *r.CommentsAtHead != 2 ||
		r.Stale == nil || *r.Stale || r.ExtraPendingReviews == nil || *r.ExtraPendingReviews != 2 ||
		r.LastAppend == nil || r.LastAppend.At != "2026-09-29T15:58:10Z" || r.LastAppend.Added != 2 || r.LastAppend.Head != reviewHead {
		t.Errorf("review = %s", out)
	}
	if strings.Contains(out, "digest_state") || strings.Contains(out, "all_marked") {
		t.Errorf("retired keys leaked into the view:\n%s", out)
	}
}

// A pending review's last_append age reads in the largest whole unit.
func TestAppendAge(t *testing.T) {
	now := reviewNowInstant
	for at, want := range map[string]string{
		"2026-09-29T16:02:40Z": "30s",
		"2026-09-29T15:58:10Z": "5m",
		"2026-09-29T14:03:10Z": "2h",
		"2026-09-26T16:03:10Z": "3d",
		"2026-09-29T17:00:00Z": "0s", // in the future
		"not-a-time":           "?",
	} {
		if got := appendAge(at, now); got != want {
			t.Errorf("appendAge(%q) = %q, want %q", at, got, want)
		}
	}
}
