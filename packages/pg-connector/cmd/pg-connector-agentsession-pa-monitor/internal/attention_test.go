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
	// INV-ATTN-URL-1: an agent session has no page, so url is omitted.
	if items[0].URL != "" {
		t.Errorf("URL = %q, want empty", items[0].URL)
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

// INV-AGS-2: a hit usage cap (5h block or 7-day week) is covered by Grafana
// alerts, so ListAttention MUST NOT emit an item for it.
func TestListAttention_CapHitRaisesNothing(t *testing.T) {
	cases := map[string]string{
		"block cap hit": `{"sessions":[],"active_block":{"id":"b1","cost_usd":140,"cap_hit_at":"2026-09-18T12:00:00Z"}}`,
		"week cap hit":  `{"sessions":[],"active_week":{"id":"w1","cost_usd":900,"cap_hit_at":"2026-09-18T12:00:00Z"}}`,
		"both cap hit":  `{"sessions":[],"active_block":{"id":"b1","cap_hit_at":"2026-09-18T12:00:00Z"},"active_week":{"id":"w1","cap_hit_at":"2026-09-18T12:00:00Z"}}`,
	}
	for name, js := range cases {
		t.Run(name, func(t *testing.T) {
			b := New(&fakeRunner{statusJSON: js})
			items, err := b.ListAttention(context.Background())
			if err != nil {
				t.Fatalf("ListAttention: %v", err)
			}
			if len(items) != 0 {
				t.Errorf("got %+v, want no items (cap hits are Grafana's)", items)
			}
		})
	}
}

// A hit cap MUST NOT suppress or alter the per-session items alongside it.
func TestListAttention_CapHitLeavesSessionItemsUnchanged(t *testing.T) {
	r := &fakeRunner{statusJSON: `{"sessions":[` +
		`{"session_id":"s1","status":"blocked","blocker":"human_input"},` +
		`{"session_id":"s2","status":"idle","long_idle":true}],` +
		`"active_block":{"id":"b1","cap_hit_at":"2026-09-18T12:00:00Z"},` +
		`"active_week":{"id":"w1","cap_hit_at":"2026-09-18T12:00:00Z"}}`}
	items, err := New(r).ListAttention(context.Background())
	if err != nil {
		t.Fatalf("ListAttention: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("got %+v, want exactly the blocked and long-idle items", items)
	}
	if items[0].ID != "s1" || items[0].Severity != schema.SeverityHigh {
		t.Errorf("items[0] = %+v, want s1 High", items[0])
	}
	if items[1].ID != "s2" || items[1].Severity != schema.SeverityLow {
		t.Errorf("items[1] = %+v, want s2 Low", items[1])
	}
	for _, it := range items {
		if it.Type != "agentsession" {
			t.Errorf("item %+v has type %q, want agentsession", it, it.Type)
		}
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
