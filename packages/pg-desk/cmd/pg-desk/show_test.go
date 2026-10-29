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

func runShowCmd(t *testing.T, ref string, f showCmdFlags) (stdout string, err error) {
	t.Helper()
	origFlags := showFlags
	t.Cleanup(func() { showFlags = origFlags })
	showFlags = f

	c, _, ferr := rootCmd.Find([]string{"show"})
	if ferr != nil {
		t.Fatalf("rootCmd has no show subcommand: %v", ferr)
	}
	var buf bytes.Buffer
	c.SetContext(context.Background())
	c.SetOut(&buf)
	err = c.RunE(c, []string{ref})
	return buf.String(), err
}

func TestShowPrintsStoredInterpretation(t *testing.T) {
	st, openFresh := openTestStore(t)
	cfg := &config.Config{SelfLogin: "me", Repos: []config.RepoConfig{{Remote: "o/r"}}}
	withOpenSeams(t, cfg, openFresh)
	// Seeded with the qualified "o/r#42" entity id — the real convention
	// gather/pg-connector write (pg2-276sg) — not the bare "42" a pre-fix
	// resolvePRRef would have queried with.
	if err := st.UpsertInterpretation(store.Interpretation{
		Repo: "o/r", EntityType: entityTypePR, EntityID: "o/r#42",
		Ownership: "mine", Category: "bugfix", Panel: panelMineAwaitingMe,
		AsOf: "2026-09-16T00:00:00Z",
	}); err != nil {
		t.Fatalf("seed interpretation: %v", err)
	}

	stdout, err := runShowCmd(t, "42", showCmdFlags{})
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	if !strings.Contains(stdout, "mine") || !strings.Contains(stdout, "bugfix") {
		t.Errorf("stdout missing stored fields: %s", stdout)
	}
}

func TestShowJSONIncludesWIP(t *testing.T) {
	st, openFresh := openTestStore(t)
	cfg := &config.Config{SelfLogin: "me", Repos: []config.RepoConfig{{Remote: "o/r"}}}
	withOpenSeams(t, cfg, openFresh)
	if err := st.UpsertInterpretation(store.Interpretation{
		Repo: "o/r", EntityType: entityTypePR, EntityID: "o/r#42",
		Ownership: "mine", AsOf: "2026-09-16T00:00:00Z",
	}); err != nil {
		t.Fatalf("seed interpretation: %v", err)
	}
	w := true
	if err := st.UpsertAnnotation(store.Annotation{
		Repo: "o/r", EntityType: entityTypePR, EntityID: "o/r#42",
		WIP: &w, SetBy: "operator", SetAt: "2026-09-16T00:00:00Z",
	}); err != nil {
		t.Fatalf("seed annotation: %v", err)
	}

	stdout, err := runShowCmd(t, "42", showCmdFlags{jsonOut: true})
	if err != nil {
		t.Fatalf("show --json: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("show --json output is not valid JSON: %v\n%s", err, stdout)
	}
	wip, present := payload["wip"]
	if !present {
		t.Fatalf("show --json payload missing \"wip\": %+v", payload)
	}
	if wip != true {
		t.Errorf(`payload["wip"] = %v, want true`, wip)
	}
}

// TestShowPlainTextEntityIDNotDoubled is pg2-5mnbo's regression test: the
// plain-text formatter must print entity_id exactly once, matching the
// --json form, rather than prepending "repo#" onto an EntityID that already
// carries the full qualified "owner/repo#N" form (resolvePRRef in desk.go
// always rebuilds it that way). The existing show tests above all use the
// single-slash "o/r" remote and never assert on the exact plain-text
// prefix, so they passed even with the doubling bug present.
func TestShowPlainTextEntityIDNotDoubled(t *testing.T) {
	st, openFresh := openTestStore(t)
	cfg := &config.Config{SelfLogin: "me", Repos: []config.RepoConfig{{Remote: "owner/repo"}}}
	withOpenSeams(t, cfg, openFresh)
	if err := st.UpsertInterpretation(store.Interpretation{
		Repo: "owner/repo", EntityType: entityTypePR, EntityID: "owner/repo#105204",
		Ownership: "mine", Category: "bugfix", Panel: panelMineAwaitingMe,
		AsOf: "2026-09-16T00:00:00Z",
	}); err != nil {
		t.Fatalf("seed interpretation: %v", err)
	}

	stdout, err := runShowCmd(t, "105204", showCmdFlags{})
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	if strings.Contains(stdout, "owner/repo#owner/repo#105204") {
		t.Fatalf("plain-text output double-prints the repo prefix: %s", stdout)
	}
	if !strings.HasPrefix(stdout, "owner/repo#105204\t") {
		t.Fatalf("plain-text output does not start with the qualified entity_id exactly once: %s", stdout)
	}

	jsonOut, err := runShowCmd(t, "105204", showCmdFlags{jsonOut: true})
	if err != nil {
		t.Fatalf("show --json: %v", err)
	}
	var payload showPayload
	if err := json.Unmarshal([]byte(jsonOut), &payload); err != nil {
		t.Fatalf("decode show --json: %v", err)
	}
	if !strings.HasPrefix(stdout, payload.EntityID+"\t") {
		t.Errorf("plain-text entity_id prefix %q does not match --json entity_id %q", stdout, payload.EntityID)
	}
}

func TestShowUnresolvedPRFails(t *testing.T) {
	_, openFresh := openTestStore(t)
	cfg := &config.Config{SelfLogin: "me", Repos: []config.RepoConfig{{Remote: "o/r"}}}
	withOpenSeams(t, cfg, openFresh)

	_, err := runShowCmd(t, "999", showCmdFlags{})
	if err == nil {
		t.Fatal("show unresolved PR: error = nil, want an error")
	}
	if !strings.Contains(err.Error(), "does not resolve") {
		t.Errorf("error %q does not say the PR does not resolve", err)
	}
}

// TestShowHasNoRefreshFlag pins that the legacy `show` verb no longer runs
// the gather pipeline: its --refresh flag went with the sync stage (PR lane
// refactor, packet P9a-2); `pg-desk pr refresh` is the way to re-read a PR.
func TestShowHasNoRefreshFlag(t *testing.T) {
	if f := showCmd.Flags().Lookup("refresh"); f != nil {
		t.Fatalf("show still has a --refresh flag: %+v", f)
	}
}
