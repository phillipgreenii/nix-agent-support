package main

import (
	"context"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// seedBareEntity writes just an entity row (no interpretation) for
// hide/unhide/wip's own "does the entity exist at all" resolution check.
func seedBareEntity(t *testing.T, st *store.Store, repo, id string) {
	t.Helper()
	if err := st.UpsertEntity(store.Entity{
		Repo: repo, EntityType: entityTypePR, EntityID: id,
		Facts: `{}`, AsOf: "2026-09-16T00:00:00Z",
	}); err != nil {
		t.Fatalf("seed entity: %v", err)
	}
}

func runCmdArgs(t *testing.T, name string, args []string) (err error) {
	t.Helper()
	c, _, ferr := rootCmd.Find([]string{name})
	if ferr != nil {
		t.Fatalf("rootCmd has no %s subcommand: %v", name, ferr)
	}
	c.SetContext(context.Background())
	return c.RunE(c, args)
}

func TestHideUnhideRoundTrip(t *testing.T) {
	st, openFresh := openTestStore(t)
	cfg := &config.Config{SelfLogin: "me", Repos: []config.RepoConfig{{Remote: "o/r"}}}
	withOpenSeams(t, cfg, openFresh)
	seedBareEntity(t, st, "o/r", "42")

	if err := runCmdArgs(t, "hide", []string{"42", "noisy PR"}); err != nil {
		t.Fatalf("hide: %v", err)
	}
	ann, found, err := st.GetPRAnnotation("o/r", entityTypePR, "42")
	if err != nil || !found {
		t.Fatalf("GetPRAnnotation after hide: found=%v err=%v", found, err)
	}
	if ann.Hidden == nil || !*ann.Hidden {
		t.Fatalf("hidden = %v, want true", ann.Hidden)
	}
	if ann.HiddenReason != "noisy PR" {
		t.Fatalf("hidden_reason = %q, want %q", ann.HiddenReason, "noisy PR")
	}

	if err := runCmdArgs(t, "unhide", []string{"42"}); err != nil {
		t.Fatalf("unhide: %v", err)
	}
	ann, found, err = st.GetPRAnnotation("o/r", entityTypePR, "42")
	if err != nil || !found {
		t.Fatalf("GetPRAnnotation after unhide: found=%v err=%v", found, err)
	}
	if ann.Hidden == nil || *ann.Hidden {
		t.Fatalf("hidden after unhide = %v, want false", ann.Hidden)
	}
}

func TestHideUnknownPRFails(t *testing.T) {
	_, openFresh := openTestStore(t)
	cfg := &config.Config{SelfLogin: "me", Repos: []config.RepoConfig{{Remote: "o/r"}}}
	withOpenSeams(t, cfg, openFresh)

	err := runCmdArgs(t, "hide", []string{"999"})
	if err == nil {
		t.Fatal("hide unknown PR: error = nil, want an error")
	}
	if !strings.Contains(err.Error(), "does not resolve") {
		t.Errorf("error %q does not say the PR does not resolve", err)
	}
}

func TestHideAcceptsPRURLAndHashForm(t *testing.T) {
	st, openFresh := openTestStore(t)
	cfg := &config.Config{SelfLogin: "me", Repos: []config.RepoConfig{{Remote: "o/r"}}}
	withOpenSeams(t, cfg, openFresh)
	seedBareEntity(t, st, "o/r", "7")

	for _, ref := range []string{"7", "o/r#7", "https://example.test/o/r/pull/7"} {
		t.Run(ref, func(t *testing.T) {
			if err := runCmdArgs(t, "hide", []string{ref}); err != nil {
				t.Fatalf("hide %q: %v", ref, err)
			}
			if err := runCmdArgs(t, "unhide", []string{ref}); err != nil {
				t.Fatalf("unhide %q: %v", ref, err)
			}
		})
	}
}

func TestWIPOnOffRoundTrip(t *testing.T) {
	st, openFresh := openTestStore(t)
	cfg := &config.Config{SelfLogin: "me", Repos: []config.RepoConfig{{Remote: "o/r"}}}
	withOpenSeams(t, cfg, openFresh)
	seedBareEntity(t, st, "o/r", "42")

	if err := runCmdArgs(t, "wip", []string{"on", "42"}); err != nil {
		t.Fatalf("wip on: %v", err)
	}
	ann, found, err := st.GetPRAnnotation("o/r", entityTypePR, "42")
	if err != nil || !found || ann.WIP == nil || !*ann.WIP {
		t.Fatalf("after wip on: found=%v ann=%+v err=%v", found, ann, err)
	}

	if err := runCmdArgs(t, "wip", []string{"off", "42"}); err != nil {
		t.Fatalf("wip off: %v", err)
	}
	ann, found, err = st.GetPRAnnotation("o/r", entityTypePR, "42")
	if err != nil || !found || ann.WIP == nil || *ann.WIP {
		t.Fatalf("after wip off: found=%v ann=%+v err=%v", found, ann, err)
	}
}

func TestWIPRejectsInvalidState(t *testing.T) {
	_, openFresh := openTestStore(t)
	cfg := &config.Config{SelfLogin: "me", Repos: []config.RepoConfig{{Remote: "o/r"}}}
	withOpenSeams(t, cfg, openFresh)

	err := runCmdArgs(t, "wip", []string{"maybe", "42"})
	if err == nil {
		t.Fatal("wip maybe: error = nil, want an error")
	}
}

// TestHideDoesNotClobberWIP proves setHiddenAnnotation preserves an
// existing wip flag (store-schema.md: annotation rows carry both fields
// independently, and a hide/unhide write must not clobber wip).
func TestHideDoesNotClobberWIP(t *testing.T) {
	st, openFresh := openTestStore(t)
	cfg := &config.Config{SelfLogin: "me", Repos: []config.RepoConfig{{Remote: "o/r"}}}
	withOpenSeams(t, cfg, openFresh)
	seedBareEntity(t, st, "o/r", "42")

	if err := runCmdArgs(t, "wip", []string{"on", "42"}); err != nil {
		t.Fatalf("wip on: %v", err)
	}
	if err := runCmdArgs(t, "hide", []string{"42"}); err != nil {
		t.Fatalf("hide: %v", err)
	}
	ann, found, err := st.GetPRAnnotation("o/r", entityTypePR, "42")
	if err != nil || !found {
		t.Fatalf("GetPRAnnotation: found=%v err=%v", found, err)
	}
	if ann.WIP == nil || !*ann.WIP {
		t.Errorf("hide clobbered wip: %+v", ann)
	}
	if ann.Hidden == nil || !*ann.Hidden {
		t.Errorf("hide did not set hidden: %+v", ann)
	}
}
