package internal

import (
	"context"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

func TestListAttention_BlockedHumanInput(t *testing.T) {
	r := &fakeRunner{statusJSON: `{"sessions":[{"session_id":"s1","status":"blocked","blocker":"human_input"}]}`}
	b := New(r)
	items, err := b.ListAttention(context.Background())
	if err != nil {
		t.Fatalf("ListAttention: %v", err)
	}
	if len(items) != 1 || items[0].Severity != schema.SeverityHigh || items[0].ID != "s1" {
		t.Errorf("got %+v", items)
	}
}

func TestListAttention_BlockedUsageLimitIsMedium(t *testing.T) {
	r := &fakeRunner{statusJSON: `{"sessions":[{"session_id":"s1","status":"blocked","blocker":"usage_limit"}]}`}
	b := New(r)
	items, _ := b.ListAttention(context.Background())
	if len(items) != 1 || items[0].Severity != schema.SeverityMedium {
		t.Errorf("got %+v", items)
	}
}

func TestListAttention_LongIdleIsLow(t *testing.T) {
	r := &fakeRunner{statusJSON: `{"sessions":[{"session_id":"s1","status":"idle","long_idle":true}]}`}
	b := New(r)
	items, _ := b.ListAttention(context.Background())
	if len(items) != 1 || items[0].Severity != schema.SeverityLow {
		t.Errorf("got %+v", items)
	}
}

func TestListAttention_WorkingSessionRaisesNothing(t *testing.T) {
	r := &fakeRunner{statusJSON: `{"sessions":[{"session_id":"s1","status":"working"}]}`}
	b := New(r)
	items, _ := b.ListAttention(context.Background())
	if len(items) != 0 {
		t.Errorf("got %+v, want no items", items)
	}
}

func TestListAttention_BlockCapHit(t *testing.T) {
	r := &fakeRunner{statusJSON: `{"sessions":[],"active_block":{"id":"b1","cost_usd":140,"cap_hit_at":"2026-09-18T12:00:00Z"}}`}
	b := New(r)
	items, _ := b.ListAttention(context.Background())
	if len(items) != 1 || items[0].Severity != schema.SeverityCritical || items[0].Type != "agentsession-usage-limit" {
		t.Errorf("got %+v", items)
	}
}

func TestListAttention_WeekNotHit(t *testing.T) {
	r := &fakeRunner{statusJSON: `{"sessions":[],"active_week":{"id":"w1","cost_usd":10}}`}
	b := New(r)
	items, _ := b.ListAttention(context.Background())
	if len(items) != 0 {
		t.Errorf("got %+v, want no items (cap not hit)", items)
	}
}

// TestListAttention_BlockedErrorIsMedium pins a design gap found during
// decomposition (advisory, not fixed here): schema.AgentSession.Blocker's
// own enum lists "error" as a valid blocker value alongside human_input/
// human_authn/usage_limit, but neither Part A's severity table nor
// blockerSeverity assigns "error" its own severity — it falls into
// blockerSeverity's default case, the same Medium bucket as the
// self-recovering usage_limit blocker. This test exists so that behavior
// reads as a DECIDED default (pin the current, correct code), not an
// oversight nobody noticed — flag it for a human to decide, in a later
// design revision, whether "error" deserves its own (arguably High, since
// unlike usage_limit it does not self-recover) severity tier. Do not
// invent that reassignment here.
func TestListAttention_BlockedErrorIsMedium(t *testing.T) {
	r := &fakeRunner{statusJSON: `{"sessions":[{"session_id":"s1","status":"blocked","blocker":"error"}]}`}
	b := New(r)
	items, _ := b.ListAttention(context.Background())
	if len(items) != 1 || items[0].Severity != schema.SeverityMedium {
		t.Errorf("got %+v, want one Medium item (documented default, see this test's own doc comment)", items)
	}
}
