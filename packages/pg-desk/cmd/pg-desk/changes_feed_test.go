package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
)

const feedTestInstance = "owning-demo"

// feedConnector installs a scripted pg-connector for the owning-backend feed
// on top of a changesFixture: `issue changes` answers feed.json, `issue
// changes-ack` echoes the acknowledged position and `issue show` answers
// show-<id>.json; every call is appended to calls.log as its argument string.
func feedConnector(t *testing.T, f *changesFixture, feedBody string) {
	t.Helper()
	f.write("feed.json", feedBody)
	installFakePGConnector(t, fmt.Sprintf(`D=%q
echo "$*" >> "$D/calls.log"
v="$2"
if [ "$v" = changes ]; then
  cat "$D/feed.json"
  if [ -f "$D/feed.exit" ]; then exit "$(cat "$D/feed.exit")"; fi
  exit 0
fi
if [ "$v" = changes-ack ]; then
  n=""; prev=""
  for a in "$@"; do if [ "$prev" = "--next-seq" ]; then n="$a"; fi; prev="$a"; done
  echo "{\"consumer\":\"pg-desk\",\"acked_seq\":$n,\"store_identity\":\"s1\"}"
  exit 0
fi
if [ "$v" = health ]; then
  echo '{"sources":[{"source":"owning-demo","status":"succeeded","count":1}],"backends":[{"backend":"owning-demo","status":{"queries":[]}}]}'
  exit 0
fi
if [ "$v" = list ]; then
  echo '{"entities":[],"present_ids":[],"truncated":false}'
  exit 0
fi
if [ "$v" = show ]; then
  if [ -f "$D/show-$3.json" ]; then cat "$D/show-$3.json"; exit 0; fi
  echo '{"error":{"code":"boom","message":"no fixture"}}'
  exit 1
fi
exit 99`, f.dir))
}

func owningFeedFixture(t *testing.T) *changesFixture {
	t.Helper()
	f := newChangesFixture(t, "issue", "jira-q")
	f.cfg.Watch.Issue.Backends = map[string]config.WatchBackend{
		"jira-q": {Instance: feedTestInstance, OwnsChanges: true},
	}
	return f
}

const oneRowFeed = `{"sources":[{"backend":"owning-demo","status":"succeeded","version":1,"truncated":false,"next_seq":6,"store_identity":"s1"}],` +
	`"changes":[{"change":"c1","source":"owning-demo","entity":{"id":"J-1"},"seq":5,"kinds":["status_changed"],"fields":{}}],` +
	`"sources_freshness":[{"query":"jira-q","last_success_at":"2026-10-01T11:59:00Z"}]}`

func callIndex(calls []string, needle string) int {
	for i, c := range calls {
		if strings.Contains(c, needle) {
			return i
		}
	}
	return -1
}

// The end-to-end trigger: `pg-desk issue changes --consumer router --json`
// against a scripted pg-connector serving one feed row prints one record for
// the entity, carrying the written change row's seq, and acknowledges the
// feed afterwards.
func TestChangesFeedEndToEndOneRow(t *testing.T) {
	f := owningFeedFixture(t)
	feedConnector(t, f, oneRowFeed)
	f.showIssue("J-1", "2026-09-30T00:00:00Z")

	env, err := f.envOf("--consumer", "router")
	if err != nil {
		t.Fatalf("changes: %v", err)
	}
	if len(env.Records) != 1 || env.Records[0].ID != "J-1" {
		t.Fatalf("records = %+v", env.Records)
	}
	rows, lerr := f.seed.ListChangesAfter("issue", 0, 0)
	if lerr != nil || len(rows) != 1 {
		t.Fatalf("change rows = %+v, %v", rows, lerr)
	}
	if env.Records[0].Seq != rows[0].Seq || env.Records[0].Seq == 0 {
		t.Errorf("record seq = %d, written change row seq = %d", env.Records[0].Seq, rows[0].Seq)
	}
	if got := strings.Join(env.Records[0].Kinds, ","); got != "status_changed" {
		t.Errorf("kinds = %q, want the feed row's own kinds", got)
	}
	if len(env.Sources) != 1 || env.Sources[0].Query != "jira-q" || env.Sources[0].Status != "ok" {
		t.Errorf("sources = %+v", env.Sources)
	}

	calls := f.calls()
	feed := callIndex(calls, "issue changes ")
	show := callIndex(calls, "issue show J-1")
	ack := callIndex(calls, "issue changes-ack ")
	if feed < 0 || show < 0 || ack < 0 || !(feed < show && show < ack) {
		t.Fatalf("calls = %v; want changes, then show, then changes-ack", calls)
	}
	if c := calls[feed]; !strings.Contains(c, "--backend "+feedTestInstance) || !strings.Contains(c, "--no-ack") ||
		!strings.Contains(c, "--consumer pg-desk") || strings.Contains(c, "--query") {
		t.Errorf("feed call = %q", c)
	}
	if c := calls[ack]; !strings.Contains(c, "--next-seq 6") || !strings.Contains(c, "--backend "+feedTestInstance) || strings.Contains(c, "--query") {
		t.Errorf("ack call = %q", c)
	}
	if c := calls[show]; !strings.Contains(c, "--backend "+feedTestInstance) {
		t.Errorf("hydration call = %q, want it pinned to the backend", c)
	}
	// The owning query is never list-and-diffed: the only listing is the
	// unflagged-member read, pinned to the backend, and no membership is kept.
	for _, c := range calls {
		if strings.Contains(c, "issue list") && !strings.Contains(c, "--backend "+feedTestInstance) {
			t.Errorf("a listing was made: %q", c)
		}
	}
	if callIndex(calls, "issue list --query jira-q --backend "+feedTestInstance) < 0 || callIndex(calls, "issue health --backend "+feedTestInstance) < 0 {
		t.Errorf("calls = %v; want the unflagged-member read (health, then list of jira-q)", calls)
	}
	if v, found, _ := f.seed.GetMeta("change_flow.unflagged." + feedTestInstance); !found || !strings.Contains(v, `"count":0`) {
		t.Errorf("unflagged record = %q %v, want a stored count of 0", v, found)
	}
	if v, found, _ := f.seed.GetMeta("change_flow.feed_observed." + feedTestInstance + ".jira-q"); !found || v != "2026-10-01T11:59:00Z" {
		t.Errorf("feed_observed = %q %v", v, found)
	}
}

// An unavailable daemon commits and acknowledges nothing and exits as a total
// failure (the one owning source is the only source).
func TestChangesFeedUnavailableDaemonAcknowledgesNothing(t *testing.T) {
	f := owningFeedFixture(t)
	feedConnector(t, f, `{"error":{"code":"unavailable","message":"daemon is down"}}`)
	f.write("feed.exit", "1")

	env, err := f.envOf("--consumer", "router")
	if exitCodeFor(err) != exitTotal {
		t.Fatalf("exit = %d (%v), want %d", exitCodeFor(err), err, exitTotal)
	}
	if len(env.Sources) != 1 || env.Sources[0].Status != "degraded" || !strings.Contains(env.Sources[0].Reason, "unavailable") {
		t.Errorf("sources = %+v", env.Sources)
	}
	if callIndex(f.calls(), "changes-ack") >= 0 || f.logLen() != 0 {
		t.Errorf("calls = %v, log = %d", f.calls(), f.logLen())
	}
}

// A beads query and an owning query are served in ONE call: the first
// list-and-diffs, the second reads the feed.
func TestChangesFeedMixedWatchListDoesBothInOnePoll(t *testing.T) {
	f := newChangesFixture(t, "issue", "beads-q", "jira-q")
	f.cfg.Watch.Issue.Backends = map[string]config.WatchBackend{
		"beads-q": {Instance: "beads"},
		"jira-q":  {Instance: feedTestInstance, OwnsChanges: true},
	}
	feedConnector(t, f, oneRowFeed)
	// Listing answers are served by the same script's fallthrough: extend it.
	f.listing("beads-q", 0, okSource, [2]string{"added", "bd-1"})
	f.showIssue("J-1", "2026-09-30T00:00:00Z")
	f.showIssue("bd-1", "2026-09-30T00:00:00Z")
	installFakePGConnector(t, fmt.Sprintf(`D=%q
echo "$*" >> "$D/calls.log"
t="$1"; v="$2"
if [ "$v" = list ]; then
  case "$*" in
  *"--backend owning-demo"*) echo '{"entities":[],"present_ids":[],"truncated":false}' ;;
  *) cat "$D/issue-list-beads-q.json" ;;
  esac
  exit 0
fi
if [ "$v" = health ]; then echo '{"sources":[],"backends":[{"backend":"owning-demo","status":{"queries":[]}}]}'; exit 0; fi
if [ "$v" = changes ]; then cat "$D/feed.json"; exit 0; fi
if [ "$v" = changes-ack ]; then echo '{"consumer":"pg-desk","acked_seq":6}'; exit 0; fi
if [ "$v" = show ]; then id=$(echo "$3" | tr '/#' '__'); cat "$D/show-$id.json"; exit 0; fi
exit 99`, f.dir))

	env, err := f.envOf("--consumer", "router")
	if err != nil {
		t.Fatalf("changes: %v", err)
	}
	ids := envKindsByID(env)
	if _, ok := ids["bd-1"]; !ok || len(ids) != 2 {
		t.Fatalf("records = %v, want bd-1 (listed) and J-1 (feed)", ids)
	}
	if _, ok := ids["J-1"]; !ok {
		t.Fatalf("records = %v", ids)
	}
	if len(env.Sources) != 2 || env.Sources[0].Query != "beads-q" || env.Sources[1].Query != "jira-q" {
		t.Errorf("sources = %+v", env.Sources)
	}
	calls := f.calls()
	for _, c := range calls {
		if strings.Contains(c, " changes ") && !strings.Contains(c, "--backend") {
			t.Errorf("feed call without --backend: %q", c)
		}
		// The owning query is never list-and-diffed; only the backend-pinned
		// unflagged-member read lists it.
		if strings.Contains(c, "list") && strings.Contains(c, "jira-q") && !strings.Contains(c, "--backend "+feedTestInstance) {
			t.Errorf("owning query was listed: %q", c)
		}
	}
	// The beads query keeps its persisted watched set; the owning one has none.
	if _, found, _ := f.seed.GetMeta("change_flow.watchset.issue.beads-q"); !found {
		t.Error("beads watch set missing")
	}
	if _, found, _ := f.seed.GetMeta("change_flow.watchset.issue.jira-q"); found {
		t.Error("an owning query kept a watch set")
	}
}
