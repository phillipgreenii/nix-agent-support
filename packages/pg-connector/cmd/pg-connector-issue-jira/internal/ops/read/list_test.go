package read

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/origin"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	lq "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/query"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

func (r *rig) list(args string) (*schema.IssueListResult, error) {
	r.t.Helper()
	out, err := r.call(OpList, args)
	if err != nil {
		return nil, err
	}
	res, ok := out.(*schema.IssueListResult)
	if !ok {
		r.t.Fatalf("list answered %T, not *schema.IssueListResult", out)
	}
	return res, nil
}

func (r *rig) mustList(args string) *schema.IssueListResult {
	r.t.Helper()
	res, err := r.list(args)
	if err != nil {
		r.t.Fatalf("list %s: %v", args, err)
	}
	return res
}

// seedMembers stores two issues and a fresh complete row of query "mine" over them.
func (r *rig) seedMembers() {
	r.t.Helper()
	r.putSummary(issueOf("ABC-1", "first"), r.clk.Now())
	r.putSummary(issueOf("ABC-2", "second"), r.clk.Now())
	r.putQuery("mine", true, r.clk.Now(), "ABC-1", "ABC-2")
}

func TestListOfAnUndefinedQueryIsQueryNotRecognizedAndTheRequestConfigIsNeverConsulted(t *testing.T) {
	r := newRig(t)
	r.seedMembers()
	for _, args := range []string{
		`{"query":"nope"}`,
		`{"query":""}`,
		`{"query":"other","config":{"queries":{"other":"project = X"}}}`,
	} {
		if _, err := r.list(args); !errors.Is(err, scriptout.ErrQueryNotRecognized) {
			t.Errorf("list %s = %v, want query_not_recognized", args, err)
		}
	}
	if n := r.queries.fetchCount(); n != 0 {
		t.Errorf("an undefined query reached the runner %d times", n)
	}
}

func TestListResolvesTheNameAgainstTheDaemonsOwnSettingsWhenGiven(t *testing.T) {
	r := newRig(t)
	r.seedMembers()
	r.ops = Handlers(Deps{Runner: r.queries, Store: r.st, Clock: r.clk, Settings: onlySettings{"settled"}})
	if _, err := r.list(`{"query":"mine"}`); !errors.Is(err, scriptout.ErrQueryNotRecognized) {
		t.Errorf("a name only the runner defines = %v, want query_not_recognized", err)
	}
	r.putQuery("settled", true, r.clk.Now(), "ABC-1")
	r.queries.defined["settled"] = true
	if res := r.mustList(`{"query":"settled"}`); len(res.PresentIDs) != 1 {
		t.Errorf("present_ids = %v", res.PresentIDs)
	}
}

type onlySettings struct{ name string }

func (s onlySettings) Expr(name string) ([]string, bool) { return []string{"jql"}, name == s.name }
func (s onlySettings) Watched(string) bool               { return false }
func (s onlySettings) Names() []string                   { return []string{s.name} }
func (s onlySettings) TTL(lq.Key) time.Duration          { return time.Minute }

func TestListWithAWatchArgumentIsInvalid(t *testing.T) {
	r := newRig(t)
	r.seedMembers()
	for _, args := range []string{`{"query":"mine","watch":true}`, `{"query":"mine","watch":"yes"}`, `{"query":"mine","watch":1}`} {
		if _, err := r.list(args); !errors.Is(err, scriptout.ErrInvalidArgument) {
			t.Errorf("list %s = %v, want invalid_argument", args, err)
		}
	}
	if _, err := r.list(`{"query":"mine","watch":false}`); err != nil {
		t.Errorf("list with watch false = %v, want an answer: no watch was asked for", err)
	}
	if _, err := r.list(`[1]`); !errors.Is(err, scriptout.ErrInvalidArgument) {
		t.Errorf("list of malformed args = %v, want invalid_argument", err)
	}
	if n := r.queries.fetchCount(); n != 0 {
		t.Errorf("a refused list reached the runner %d times", n)
	}
}

func TestListServesTheStoredMembersAndTheirSummariesWithTheAnnotations(t *testing.T) {
	r := newRig(t)
	r.seedMembers()
	r.clk.Advance(20 * time.Second)

	res := r.mustList(`{"query":"mine","cursor":{"updated_since":"x"},"fresh":true}`)
	if n := r.queries.fetchCount(); n != 0 {
		t.Fatalf("a stored row inside its ttl was re-run %d times (fresh and cursor are ignored)", n)
	}
	if got := strings.Join(res.PresentIDs, ","); got != "ABC-1,ABC-2" {
		t.Errorf("present_ids = %q", got)
	}
	if len(res.Entities) != 2 || res.Entities[0].Title != "first" || res.Entities[1].Title != "second" {
		t.Fatalf("entities = %+v", res.Entities)
	}
	for _, e := range res.Entities {
		if e.AsOf != t0.Format(time.RFC3339) || e.Stale || e.Version != 1 {
			t.Errorf("entity %s: as_of %q stale %v version %d", e.ID, e.AsOf, e.Stale, e.Version)
		}
		if e.StatusCategory != "new" {
			t.Errorf("entity %s lost its stored content: %+v", e.ID, e)
		}
	}
	if res.Truncated {
		t.Error("truncated, want false for a complete row")
	}
	if res.ServedFrom != "cache" || res.Stale == nil || *res.Stale || res.AgeSeconds == nil || *res.AgeSeconds != 20 {
		t.Errorf("annotations: served_from %q stale %v age %v", res.ServedFrom, res.Stale, res.AgeSeconds)
	}
	if len(res.Groups) != 2 || res.Groups[0].Group != "query" || res.Groups[1].Group != "summary" {
		t.Errorf("groups = %+v, want the query row then the summaries", res.Groups)
	}
	if g := res.Groups[0]; g.FetchedAt != t0.Format(time.RFC3339) || g.FreshUntil != t0.Add(5*time.Minute).Format(time.RFC3339) {
		t.Errorf("query group = %+v", g)
	}

	// The cursor is always JSON null.
	if res.Cursor != nil {
		t.Errorf("cursor = %s, want nil", res.Cursor)
	}
	body, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(body, &wire); err != nil {
		t.Fatal(err)
	}
	if string(wire["cursor"]) != "null" {
		t.Errorf("wire cursor = %s, want null", wire["cursor"])
	}
}

func TestListWithIDsOnlyAnswersPresentIDsAndNoEntities(t *testing.T) {
	r := newRig(t)
	r.seedMembers()
	res := r.mustList(`{"query":"mine","ids_only":true}`)
	if got := strings.Join(res.PresentIDs, ","); got != "ABC-1,ABC-2" {
		t.Errorf("present_ids = %q", got)
	}
	if len(res.Entities) != 0 {
		t.Errorf("entities = %+v, want none", res.Entities)
	}
	if len(res.Groups) != 1 || res.Groups[0].Group != "query" {
		t.Errorf("groups = %+v, want the query row only (no summary was read)", res.Groups)
	}
}

func TestListTruncatedFollowsTheStoredRowAndIDsOnlyDoesNotChangeIt(t *testing.T) {
	r := newRig(t)
	r.seedMembers()
	r.putQuery("mine", false, r.clk.Now(), "ABC-1", "ABC-2")
	for _, args := range []string{`{"query":"mine"}`, `{"query":"mine","ids_only":true}`} {
		if res := r.mustList(args); !res.Truncated {
			t.Errorf("list %s: truncated = false, want true for an incomplete row", args)
		}
	}
}

func TestListLeavesOutAMemberWithNoUsableStoredSummary(t *testing.T) {
	r := newRig(t)
	r.seedMembers()
	r.putQuery("mine", true, r.clk.Now(), "ABC-1", "ABC-2", "ABC-3") // ABC-3 has nothing stored
	res := r.mustList(`{"query":"mine"}`)
	if got := strings.Join(res.PresentIDs, ","); got != "ABC-1,ABC-2" || len(res.Entities) != 2 {
		t.Errorf("present_ids %q entities %d, want the two stored ones", got, len(res.Entities))
	}
	if res := r.mustList(`{"query":"mine","ids_only":true}`); len(res.PresentIDs) != 3 {
		t.Errorf("ids_only present_ids = %v, want all three row ids", res.PresentIDs)
	}
}

func TestListReRunsAStaleRowThroughTheRunner(t *testing.T) {
	r := newRig(t)
	r.seedMembers()
	r.clk.Advance(10 * time.Minute) // past the row's five minute ttl
	r.queries.result = []string{"ABC-2"}

	res := r.mustList(`{"query":"mine"}`)
	if n := r.queries.fetchCount(); n != 1 {
		t.Fatalf("fetches = %d, want 1", n)
	}
	if res.ServedFrom != "origin" || res.Stale == nil || *res.Stale {
		t.Errorf("served_from %q stale %v, want origin and not stale", res.ServedFrom, res.Stale)
	}
	if got := strings.Join(res.PresentIDs, ","); got != "ABC-2" {
		t.Errorf("present_ids = %q, want the re-run's members", got)
	}
}

func TestListDuringAPauseServesTheStoredRowStaleAndWithNothingStoredIsUnavailable(t *testing.T) {
	r := newRig(t)
	r.seedMembers()
	until := t0.Add(time.Hour)
	r.queries.err = &origin.PausedError{Until: until, Reason: "rate limited"}
	r.clk.Advance(10 * time.Minute)

	res := r.mustList(`{"query":"mine"}`)
	if res.Stale == nil || !*res.Stale || res.ServedFrom != "cache" {
		t.Errorf("stale %v served_from %q, want stale cache", res.Stale, res.ServedFrom)
	}
	if res.AgeSeconds == nil || *res.AgeSeconds != 600 {
		t.Errorf("age_seconds = %v, want 600", res.AgeSeconds)
	}
	if len(res.Entities) != 2 || !res.Entities[0].Stale {
		t.Errorf("entities = %+v, want the stored ones marked stale", res.Entities)
	}

	r.queries.defined["fresh-name"] = true
	_, err := r.list(`{"query":"fresh-name"}`)
	if !errors.Is(err, scriptout.ErrUnavailable) || !strings.Contains(err.Error(), "paused until "+until.Format(time.RFC3339)) {
		t.Errorf("list with no stored row during a pause = %v, want unavailable naming the pause", err)
	}
}

func TestListWithARowPastTheExpiryIsUnavailableWhenTheRunFails(t *testing.T) {
	r := newRig(t)
	r.seedMembers()
	r.queries.err = errors.New("origin down")
	r.clk.Advance(testExpiry + time.Hour)
	_, err := r.list(`{"query":"mine"}`)
	if !errors.Is(err, scriptout.ErrUnavailable) || !strings.Contains(err.Error(), "origin down") {
		t.Errorf("list = %v, want unavailable naming the failure", err)
	}
}

func TestListNamesAGovernorRefusal(t *testing.T) {
	r := newRig(t)
	r.queries.err = &governor.Refusal{Reason: "the request budget is spent"}
	_, err := r.list(`{"query":"mine"}`)
	if !errors.Is(err, scriptout.ErrUnavailable) || !strings.Contains(err.Error(), "the request budget is spent") {
		t.Errorf("list = %v, want unavailable naming the refusal", err)
	}
	r.queries.err = lq.ErrPending
	if _, err = r.list(`{"query":"mine"}`); !errors.Is(err, scriptout.ErrUnavailable) || !strings.Contains(err.Error(), "deadline") {
		t.Errorf("a run pending at the deadline = %v, want unavailable", err)
	}
}

func TestListWhileUnauthenticatedServesStoredContentOrAnswersUnauthenticated(t *testing.T) {
	r := newRig(t)
	r.seedMembers()
	r.auth.MarkUnauthenticated(errors.New("pjira: status 401 Unauthorized"))
	r.clk.Advance(10 * time.Minute)

	res := r.mustList(`{"query":"mine"}`)
	if res.Stale == nil || !*res.Stale || res.AgeSeconds == nil || *res.AgeSeconds != 600 || len(res.Entities) != 2 {
		t.Errorf("stored content while unauthenticated: stale %v age %v entities %d", res.Stale, res.AgeSeconds, len(res.Entities))
	}
	r.queries.defined["other"] = true
	_, err := r.list(`{"query":"other"}`)
	if !errors.Is(err, scriptout.ErrUnauthenticated) || !strings.Contains(err.Error(), "401") {
		t.Errorf("list with nothing stored while unauthenticated = %v, want unauthenticated", err)
	}
	if n := r.queries.fetchCount(); n != 0 {
		t.Errorf("the runner was asked %d times while unauthenticated", n)
	}
}

func TestListIsUnavailableWithoutARunnerOrAStore(t *testing.T) {
	r := newRig(t)
	r.ops = Handlers(Deps{Clock: r.clk})
	if _, err := r.list(`{"query":"mine"}`); !errors.Is(err, scriptout.ErrUnavailable) {
		t.Errorf("list with no runner = %v, want unavailable", err)
	}
}
