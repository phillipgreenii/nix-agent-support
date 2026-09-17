package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// seedFeedbackPR writes an interpretation row whose dispositions blob
// carries the given comment/verdict pairs, mirroring the shape packet 5's
// computeDispositions writes (internal/interpret's own Disposition JSON
// tags: comment_id, verdict, overridden).
func seedFeedbackPR(t *testing.T, st *store.Store, repo, id string, dispositions [][2]string) {
	t.Helper()
	var raw []map[string]any
	for _, d := range dispositions {
		raw = append(raw, map[string]any{"comment_id": d[0], "verdict": d[1]})
	}
	b, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("marshal dispositions: %v", err)
	}
	if err := st.UpsertInterpretation(store.Interpretation{
		Repo: repo, EntityType: entityTypePR, EntityID: id,
		Dispositions: string(b), AsOf: "2026-09-16T00:00:00Z",
	}); err != nil {
		t.Fatalf("upsert interpretation: %v", err)
	}
}

// runFeedbackList and runFeedbackSetCmd bypass cobra's own flag parser
// (this docket's established test convention — see open_test.go's
// runOpenCmd, which sets opFlags directly) by writing feedbackSetFlags
// directly rather than passing "--disposition"/"--actor" as argv strings.
func runFeedbackListCmd(t *testing.T, ref string) (stdout string, err error) {
	t.Helper()
	c, _, ferr := rootCmd.Find([]string{"feedback", "list"})
	if ferr != nil {
		t.Fatalf("rootCmd has no feedback list subcommand: %v", ferr)
	}
	var buf bytes.Buffer
	c.SetContext(context.Background())
	c.SetOut(&buf)
	err = c.RunE(c, []string{ref})
	return buf.String(), err
}

func runFeedbackSetCmd(t *testing.T, ref, commentID, disposition, actor string) error {
	t.Helper()
	origFlags := feedbackSetFlags
	t.Cleanup(func() { feedbackSetFlags = origFlags })
	feedbackSetFlags.disposition = disposition
	feedbackSetFlags.actor = actor

	c, _, ferr := rootCmd.Find([]string{"feedback", "set"})
	if ferr != nil {
		t.Fatalf("rootCmd has no feedback set subcommand: %v", ferr)
	}
	c.SetContext(context.Background())
	return c.RunE(c, []string{ref, commentID})
}

func TestFeedbackListPrintsBaseDispositions(t *testing.T) {
	st, openFresh := openTestStore(t)
	cfg := &config.Config{SelfLogin: "me", Actor: "pg-desk", Repos: []config.RepoConfig{{Remote: "o/r"}}}
	withOpenSeams(t, cfg, openFresh)
	seedFeedbackPR(t, st, "o/r", "42", [][2]string{{"c1", dispositionOpen}, {"c2", dispositionNoAction}})

	stdout, err := runFeedbackListCmd(t, "42")
	if err != nil {
		t.Fatalf("feedback list: %v", err)
	}
	if !strings.Contains(stdout, "c1") || !strings.Contains(stdout, dispositionOpen) {
		t.Errorf("stdout missing c1/open: %s", stdout)
	}
	if !strings.Contains(stdout, "c2") || !strings.Contains(stdout, dispositionNoAction) {
		t.Errorf("stdout missing c2/no-action: %s", stdout)
	}
}

func TestFeedbackSetOverridesSurviveList(t *testing.T) {
	st, openFresh := openTestStore(t)
	cfg := &config.Config{SelfLogin: "me", Actor: "pg-desk", Repos: []config.RepoConfig{{Remote: "o/r"}}}
	withOpenSeams(t, cfg, openFresh)
	seedFeedbackPR(t, st, "o/r", "42", [][2]string{{"c1", dispositionOpen}})

	if err := runFeedbackSetCmd(t, "42", "c1", dispositionWillFix, ""); err != nil {
		t.Fatalf("feedback set: %v", err)
	}

	ann, found, err := st.GetAnnotation("o/r", entityTypePR, "42", "c1")
	if err != nil || !found {
		t.Fatalf("GetAnnotation: found=%v err=%v", found, err)
	}
	if ann.Disposition != dispositionWillFix {
		t.Fatalf("disposition = %q, want %q", ann.Disposition, dispositionWillFix)
	}
	if ann.SetBy != "pg-desk" {
		t.Fatalf("set_by = %q, want the configured actor", ann.SetBy)
	}

	stdout, err := runFeedbackListCmd(t, "42")
	if err != nil {
		t.Fatalf("feedback list: %v", err)
	}
	if !strings.Contains(stdout, dispositionWillFix) {
		t.Errorf("stdout does not show the override: %s", stdout)
	}
	if !strings.Contains(stdout, "overridden") {
		t.Errorf("stdout does not mark the row overridden: %s", stdout)
	}
}

func TestFeedbackSetRejectsInvalidDisposition(t *testing.T) {
	st, openFresh := openTestStore(t)
	cfg := &config.Config{SelfLogin: "me", Actor: "pg-desk", Repos: []config.RepoConfig{{Remote: "o/r"}}}
	withOpenSeams(t, cfg, openFresh)
	seedFeedbackPR(t, st, "o/r", "42", [][2]string{{"c1", dispositionOpen}})

	err := runFeedbackSetCmd(t, "42", "c1", "bogus", "")
	if err == nil {
		t.Fatal("feedback set with a bogus disposition: error = nil, want an error")
	}
}

func TestFeedbackSetRejectsUnknownComment(t *testing.T) {
	st, openFresh := openTestStore(t)
	cfg := &config.Config{SelfLogin: "me", Actor: "pg-desk", Repos: []config.RepoConfig{{Remote: "o/r"}}}
	withOpenSeams(t, cfg, openFresh)
	seedFeedbackPR(t, st, "o/r", "42", [][2]string{{"c1", dispositionOpen}})

	err := runFeedbackSetCmd(t, "42", "unknown-comment", dispositionWillFix, "")
	if err == nil {
		t.Fatal("feedback set on an unknown comment: error = nil, want an error")
	}
	if !strings.Contains(err.Error(), "does not resolve") {
		t.Errorf("error %q does not say the comment does not resolve", err)
	}
}

func TestFeedbackSetExplicitActorOverridesConfig(t *testing.T) {
	st, openFresh := openTestStore(t)
	cfg := &config.Config{SelfLogin: "me", Actor: "pg-desk", Repos: []config.RepoConfig{{Remote: "o/r"}}}
	withOpenSeams(t, cfg, openFresh)
	seedFeedbackPR(t, st, "o/r", "42", [][2]string{{"c1", dispositionOpen}})

	if err := runFeedbackSetCmd(t, "42", "c1", dispositionWontFix, "operator"); err != nil {
		t.Fatalf("feedback set: %v", err)
	}
	ann, found, err := st.GetAnnotation("o/r", entityTypePR, "42", "c1")
	if err != nil || !found {
		t.Fatalf("GetAnnotation: found=%v err=%v", found, err)
	}
	if ann.SetBy != "operator" {
		t.Fatalf("set_by = %q, want the explicit --actor", ann.SetBy)
	}
}

func TestFeedbackSetRequiresAnActor(t *testing.T) {
	st, openFresh := openTestStore(t)
	cfg := &config.Config{SelfLogin: "me", Repos: []config.RepoConfig{{Remote: "o/r"}}}
	withOpenSeams(t, cfg, openFresh)
	seedFeedbackPR(t, st, "o/r", "42", [][2]string{{"c1", dispositionOpen}})

	err := runFeedbackSetCmd(t, "42", "c1", dispositionWillFix, "")
	if err == nil {
		t.Fatal("feedback set with no actor anywhere: error = nil, want an error")
	}
	if !strings.Contains(err.Error(), "actor") {
		t.Errorf("error %q does not mention the missing actor", err)
	}
}
