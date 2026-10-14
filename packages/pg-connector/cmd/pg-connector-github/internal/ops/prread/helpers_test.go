package prread

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/conversation"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/detail"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groupsvc"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/summary"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

var t0 = time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)

// fakeEngine answers Ensure from a programmed result and records the calls.
type fakeEngine struct {
	mu    sync.Mutex
	res   groupsvc.EnsureResult
	err   error
	calls []engineCall
}

type engineCall struct {
	id   string
	want []string
	opt  groupsvc.EnsureOptions
}

func (f *fakeEngine) Ensure(_ context.Context, id string, want []string, opt groupsvc.EnsureOptions) (groupsvc.EnsureResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, engineCall{id: id, want: append([]string(nil), want...), opt: opt})
	return f.res, f.err
}

func (f *fakeEngine) last(t *testing.T) engineCall {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		t.Fatal("the engine was not asked")
	}
	return f.calls[len(f.calls)-1]
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// cannedSummary is the generic canned PR the golden answer was produced from.
func cannedSummary() summary.Summary {
	return summary.Summary{
		NodeID: "PR_kwDOExample1", Repo: "octo/widgets", Number: 42, Title: "Add widget cache",
		URL: "https://github.com/octo/widgets/pull/42", State: "OPEN", Body: "Adds a cache in front of the widget lookup.",
		UpdatedAt: "2026-10-01T10:00:00Z", ReviewDecision: "APPROVED", Mergeable: "MERGEABLE", Author: "phillipgreenii",
		LabelCount: 2, Labels: []string{"bug", "docs"},
		// Counts the show path must NOT trust: the conversation is the source.
		CommentCount: 99, ReviewCount: 99, ReviewThreadCount: 7,
		HeadRefOid: "1111111111111111111111111111111111111111", HeadRefName: "feature/widget-cache",
		BaseRefName: "main", BaseRefOid: "2222222222222222222222222222222222222222", ChecksState: "SUCCESS",
	}
}

func cannedDetail() detail.Detail {
	return detail.Detail{
		MergeStateStatus: "CLEAN", ReviewRequests: []string{"teammate", "reviewers"},
		Additions: 10, Deletions: 2, ChangedFiles: 3,
	}
}

func cannedConversation() conversation.Content {
	commit := "abc123"
	empty := ""
	return conversation.Content{
		Comments: []schema.PRComment{
			{ID: "IC_1", Author: "phillipgreenii", Body: "top-level one"},
			{ID: "IC_2", Author: "review-bot", Body: "top-level two"},
			{ID: "RC_4", Author: "teammate", Body: "inline, review not joined", Path: "util.go", Line: 12, ThreadID: "RC_4", ReviewThreadID: "PRRT_2"},
		},
		Reviews: []schema.PRReview{
			{
				ID: "PRR_1", Author: "teammate", State: "CHANGES_REQUESTED", Body: "please fix",
				Comments: []schema.PRComment{{
					ID: "RC_3", Author: "teammate", Body: "inline nested", Path: "main.go", Line: 10, ThreadID: "RC_3",
					ReviewThreadID: "PRRT_1", Resolved: true, ThreadOutdated: true,
				}},
				SubmittedAt: "2026-10-01T09:00:00Z", CommitOID: &commit,
			},
			{ID: "PRR_2", Author: "review-bot", State: "APPROVED", SubmittedAt: "2026-10-01T09:30:00Z", CommitOID: &empty},
		},
		Connections: schema.PRConnections{
			Reviews:  schema.PRConnectionReport{Total: 2, Returned: 2},
			Threads:  schema.PRConnectionReport{Total: 2, Returned: 2},
			Comments: schema.PRConnectionReport{Truncated: true, Total: 1001, Returned: 1000},
		},
	}
}

// row builds a stored group row fetched age ago and fresh for ttl more.
func row(t *testing.T, group string, content any, age, freshFor time.Duration) store.GroupRow {
	t.Helper()
	var b []byte
	switch c := content.(type) {
	case []byte:
		b = c
	default:
		b = mustJSON(t, content)
	}
	return store.GroupRow{
		Kind: "pr", ID: "PR_kwDOExample1", Group: group, Content: b,
		FetchedAt: t0.Add(-age), FreshUntil: t0.Add(-age + freshFor),
	}
}

func fresh(t *testing.T, group string, content any) groupsvc.GroupResult {
	return groupsvc.GroupResult{Group: group, Row: row(t, group, content, 30*time.Second, 5*time.Minute), Status: groupsvc.Fresh}
}

// showResult is every group of show fresh and answered from the store.
func showResult(t *testing.T) groupsvc.EnsureResult {
	return groupsvc.EnsureResult{Summary: cannedSummary(), Groups: []groupsvc.GroupResult{
		fresh(t, summary.GroupName, cannedSummary()),
		fresh(t, groups.Detail, cannedDetail()),
		fresh(t, groups.Conversation, cannedConversation()),
	}}
}

func newHandlers(eng *fakeEngine) map[string]func(context.Context, string) (any, error) {
	hs := Handlers(Deps{Engine: eng, Clock: governor.NewManualClock(t0)})
	out := map[string]func(context.Context, string) (any, error){}
	for name, h := range hs {
		out[name] = func(ctx context.Context, args string) (any, error) { return h.Handle(ctx, json.RawMessage(args)) }
	}
	return out
}

func args(id string, fresh bool) string {
	return fmt.Sprintf(`{"id":%q,"fresh":%t}`, id, fresh)
}

// asJSON marshals an answer and decodes it into a generic map.
func asJSON(t *testing.T, v any) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(mustJSON(t, v), &m); err != nil {
		t.Fatal(err)
	}
	return m
}
