package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// captureInject replaces focusInject with a recorder for one test.
func captureInject(t *testing.T, fail error) *[]string {
	t.Helper()
	var got []string
	orig := focusInject
	focusInject = func(_ context.Context, ev string) error {
		got = append(got, ev)
		return fail
	}
	t.Cleanup(func() { focusInject = orig })
	return &got
}

// Bead pg2-e5g9gx: a PR newly selected is announced to the router as one
// pr.changed event shaped like the PR source's item, so the pr-change role
// runs for a PR that never changes again.
func TestSelectApplyAnnouncesEachNewPRRowOnce(t *testing.T) {
	f := newSelFixture(t)
	f.pr(t, 1, "alpha", "2026-10-09T08:00:00Z")
	f.pr(t, 2, "bravo", "2026-10-09T09:00:00Z")
	f.showDraft(t)
	got := captureInject(t, nil)

	_, _, err := sel(t, "ok", "--apply")
	mustOK(t, err)
	if len(*got) != 2 {
		t.Fatalf("announced %d events, want 2: %v", len(*got), *got)
	}
	var ev struct {
		ID      string `json:"id"`
		Type    string `json:"type"`
		At      string `json:"at"`
		Expires string `json:"expiresAt"`
		Payload struct {
			ID       string `json:"id"`
			Type     string `json:"type"`
			Metadata struct {
				EntityID string `json:"entity_id"`
			} `json:"metadata"`
		} `json:"payload"`
	}
	if err := json.Unmarshal([]byte((*got)[0]), &ev); err != nil {
		t.Fatal(err)
	}
	if ev.Type != "pr.changed" || ev.Payload.Type != "pr" || ev.Payload.Metadata.EntityID != "o/r#1" ||
		!strings.HasPrefix(ev.Payload.ID, "o/r#1@") || len(ev.Payload.ID) != len("o/r#1@")+12 ||
		ev.At == "" || ev.Expires == "" || !strings.Contains(ev.ID, "o/r#1") {
		t.Errorf("event = %s", (*got)[0])
	}

	// A re-select inserts nothing new, so it announces nothing (idempotent).
	*got = nil
	_, _, err = sel(t, "ok", "--apply")
	mustOK(t, err)
	if len(*got) != 0 {
		t.Errorf("re-select announced %v, want none", *got)
	}
}

func TestSelectDryRunAnnouncesNothing(t *testing.T) {
	f := newSelFixture(t)
	f.pr(t, 1, "alpha", "2026-10-09T08:00:00Z")
	f.showDraft(t)
	got := captureInject(t, nil)
	_, _, err := sel(t, "ok", "--dry-run")
	mustOK(t, err)
	if len(*got) != 0 {
		t.Errorf("dry run announced %v", *got)
	}
}

// A failed injection warns and never changes the lock's exit code.
func TestSelectAnnounceFailureWarnsAndKeepsExitOK(t *testing.T) {
	f := newSelFixture(t)
	f.pr(t, 1, "alpha", "2026-10-09T08:00:00Z")
	f.showDraft(t)
	captureInject(t, errors.New("no running core"))
	_, errOut, err := sel(t, "ok", "--apply")
	mustOK(t, err)
	mustContain(t, errOut, "selection not announced to the decider", "pg-pr-change apply o/r#1")
}
