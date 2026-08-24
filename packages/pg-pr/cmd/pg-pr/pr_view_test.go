package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/provider/vcs"
)

// setViewStateHome points XDG_STATE_HOME at a fresh temp dir for the
// duration of the test, mirroring the pattern already used throughout this
// package (e.g. pr_list_test.go's setListStateHome, the removed
// TestPRInfo_* tests). withStoreDir additionally creates the "pg-pr"
// subdirectory store.DefaultPath() expects, so a caller that wants to open
// the store can do so; a caller that wants to exercise the "no store file"
// path should pass withStoreDir=false.
func setViewStateHome(t *testing.T, withStoreDir bool) string {
	t.Helper()
	tmp := t.TempDir()
	if withStoreDir {
		if err := os.MkdirAll(filepath.Join(tmp, "pg-pr"), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	t.Setenv("XDG_STATE_HOME", tmp)
	return tmp
}

// TestPRView_HumanOutput_NoStore verifies `pr view` renders identity fields
// from the (repo, number) it was invoked with even when no store file exists
// at all, and that reading it never creates one (mirrors the stat-guard
// contract listOpenPRItems/the removed appendEnrichment used to enforce).
func TestPRView_HumanOutput_NoStore(t *testing.T) {
	resetPRFlags()
	setViewStateHome(t, false)

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"pr", "view", "7", "--repo", "foo/bar"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute: %v (stderr=%s)", err, stderr.String())
	}
	got := stdout.String()
	for _, want := range []string{"  repo: foo/bar", "  number: 7"} {
		if !strings.Contains(got, want) {
			t.Errorf("expected %q in output: %q", want, got)
		}
	}
	if _, statErr := os.Stat(store.DefaultPath()); statErr == nil {
		t.Errorf("pr view created a store file at %s; it must not", store.DefaultPath())
	}
}

// TestPRView_JSONOutput_NoStore verifies the --json mode emits a single valid
// JSON document, with the identity axis populated and the store-backed axes
// (ownership, enrichment) present as explicit JSON null, never omitted keys
// (internal/prview's own contract; this test only proves the CLI wiring
// reaches it, not the marshal contract itself).
func TestPRView_JSONOutput_NoStore(t *testing.T) {
	resetPRFlags()
	setViewStateHome(t, false)

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"pr", "view", "7", "--repo", "foo/bar", "--json"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute: %v (stderr=%s)", err, stderr.String())
	}
	var doc map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &doc); err != nil {
		t.Fatalf("pr view --json output is not valid JSON: %v\noutput:\n%s", err, stdout.String())
	}
	identity, ok := doc["identity"].(map[string]any)
	if !ok {
		t.Fatalf("expected identity object in output: %v", doc)
	}
	if identity["repo"] != "foo/bar" {
		t.Errorf("identity.repo = %v, want foo/bar", identity["repo"])
	}
	if identity["number"] != float64(7) {
		t.Errorf("identity.number = %v, want 7", identity["number"])
	}
	for _, key := range []string{"ownership", "enrichment"} {
		v, present := doc[key]
		if !present {
			t.Errorf("expected key %q to be present (as explicit null), got omitted", key)
		}
		if v != nil {
			t.Errorf("expected doc[%q] = null with no store row, got %v", key, v)
		}
	}
}

// TestPRView_StoreRowPopulatesOwnershipAndEnrichment seeds the store with a
// PR row plus enrichment and confirms `pr view` surfaces both — the
// consolidation's whole point (pg2-4dz88.5's "one command shows everything
// known") — through the real CLI path, not just internal/prview's own
// direct-Assemble tests.
func TestPRView_StoreRowPopulatesOwnershipAndEnrichment(t *testing.T) {
	resetPRFlags()
	setViewStateHome(t, true)

	db, err := store.Open(store.DefaultPath())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	ctx := context.Background()
	if _, err := db.UpsertPR(ctx, store.PullRequest{
		Repo: "foo/bar", Number: 7, Ownership: "mine", State: "open",
		Author: "phillipg", Branch: "feat/x", Base: "main",
		URL: "https://github.com/foo/bar/pull/7", HeadSHA: "abc123",
	}); err != nil {
		t.Fatalf("upsert pr: %v", err)
	}
	if err := db.SetEnrichment(ctx, "foo/bar", 7, store.Enrichment{
		Kind: "bugfix", Size: "M", Urgency: "high",
		Languages:      []string{"Go"},
		UrgencyReasons: []string{"label:p0"},
	}); err != nil {
		t.Fatalf("set enrichment: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"pr", "view", "7", "--repo", "foo/bar"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute: %v (stderr=%s)", err, stderr.String())
	}
	got := stdout.String()
	for _, want := range []string{
		"  repo: foo/bar", "  number: 7", "  author: phillipg",
		"  ownership: mine",
		"  kind: bugfix", "  size: M", "high (label:p0)", "  languages: Go",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("expected %q in output: %q", want, got)
		}
	}
}

// TestPRView_NoNetworkCall proves `pr view`'s default read path never calls
// the VCS provider (INV-READ-1's "no network call", already ruled and tested
// at the assembler level by pg2-4dz88.5.3 — this test proves this bead's own
// CLI wiring actually reaches that contract rather than accidentally
// round-tripping live like the old `pr show` did).
func TestPRView_NoNetworkCall(t *testing.T) {
	resetPRFlags()
	setViewStateHome(t, false)

	prev := vcsProviderFor
	t.Cleanup(func() { vcsProviderFor = prev })
	vcsProviderFor = func(string) vcs.Provider {
		t.Fatal("pr view must not call vcsProviderFor: its default read path is store-only, no network call")
		return nil
	}

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"pr", "view", "7", "--repo", "foo/bar"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute: %v (stderr=%s)", err, stderr.String())
	}
}

func TestPRView_InvalidNumber(t *testing.T) {
	resetPRFlags()
	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"pr", "view", "abc"})

	if err := rootCmd.Execute(); err == nil {
		t.Fatalf("expected error for non-numeric PR id")
	}
}

// TestRetiredNames_RemovedOutright is the pg2-4dz88.5.7 compatibility test for
// the operator ruling on pg2-4dz88.5.2: "surviving name is 'view'
// (pg-pr pr view); retired names show/info/pr-info are removed outright with
// cobra's plain default unknown-command error (no custom replacement-naming
// message, overriding this bead's own suggested constraint)."
//
// The parent umbrella's design field asked for the OPPOSITE assertion
// ("Explicitly assert the user does NOT see a bare cobra unknown-command
// string") — written before the ruling landed, for whichever disposition got
// picked. The actual ruling picked plain removal and explicitly overrides
// that constraint, so this test asserts cobra's OWN default behavior, not a
// bespoke error/alias/deprecation path.
//
// The precise mechanics (probed empirically, not assumed — cobra
// spf13/cobra@v1.10.2's args.go legacyArgs comment: "subcommands will always
// accept arbitrary arguments"; the classic `unknown command "x" for "y"`
// STRING is only synthesized for a ROOT command with no parent, which `pr`
// is not): every real historical caller passes `--repo` and/or `--json`
// (see the bead's own caller list), and because `show`/`info`/`pr-info` no
// longer match any subcommand of `pr`, cobra parses `--repo`/`--json`
// against `pr`'s OWN flag set (which has none — only its children register
// flags) and fails with cobra's own generic flag-parsing error
// (`unknown flag: --repo`), printed as "Error: ..." on stderr, with `pr`'s
// help/usage on stdout. That IS cobra's "plain default" for this shape — no
// bespoke code was added to produce it, and no replacement-naming message
// appears anywhere, exactly as ruled.
func TestRetiredNames_RemovedOutright(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		wantFlag string
	}{
		// pg-pr pr show <n> --repo <repo> — historical caller shape
		// (e.g. claude-marketplace/pg-pr/commands/check-my-pr.md:31).
		{name: "show", args: []string{"pr", "show", "7", "--repo", "foo/bar"}, wantFlag: "--repo"},
		// pg-pr pr info <n> --json — historical caller shape
		// (e.g. claude-marketplace/pg-pr/agents/pg-pr-review-jira-alignment.md:44).
		{name: "info", args: []string{"pr", "info", "7", "--json"}, wantFlag: "--json"},
		// pg-pr pr pr-info <n> --repo <repo> — the separate alias token
		// `info` used to register (cmd/pg-pr/pr.go:150, pre-removal); it
		// gets its own case because it is a distinct resolvable token from
		// "info", per the umbrella design field's explicit callout.
		{name: "pr-info", args: []string{"pr", "pr-info", "7", "--repo", "foo/bar"}, wantFlag: "--repo"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetPRFlags()
			var stdout, stderr bytes.Buffer
			rootCmd.SetOut(&stdout)
			rootCmd.SetErr(&stderr)
			rootCmd.SetArgs(tc.args)

			err := rootCmd.Execute()
			if err == nil {
				t.Fatalf("expected a cobra flag-parsing error for a retired, now-unregistered subcommand token, got success; stdout=%q", stdout.String())
			}
			wantErr := "unknown flag: " + tc.wantFlag
			if err.Error() != wantErr {
				t.Fatalf("err = %q, want cobra's own generic %q", err.Error(), wantErr)
			}
			if got := stderr.String(); got != "Error: "+wantErr+"\n" {
				t.Errorf("stderr = %q, want cobra's plain default %q", got, "Error: "+wantErr+"\n")
			}
			got := stdout.String()

			// The retired command's OLD behavior must be gone: none of the
			// old success markers (PR metadata/enrichment) appear.
			for _, mustNotContain := range []string{"number: 7", "author:", "Kind:"} {
				if strings.Contains(got, mustNotContain) {
					t.Errorf("%s: retired name still produced old output %q: %q", tc.name, mustNotContain, got)
				}
			}
			// No bespoke replacement-naming message was added (the ruling
			// explicitly forbids one) — only cobra's own generic fallback.
			lower := strings.ToLower(got + stderr.String())
			for _, forbidden := range []string{"deprecat", "renamed", "use `pr view`", "use 'pr view'"} {
				if strings.Contains(lower, forbidden) {
					t.Errorf("%s: found a custom replacement-naming message (%q) the ruling forbids: %q", tc.name, forbidden, got)
				}
			}
			// It IS cobra's own fallback (the `pr` parent's help/usage),
			// not a silently empty command.
			if !strings.Contains(got, "Available Commands:") {
				t.Errorf("%s: expected cobra's default help fallback in output, got: %q", tc.name, got)
			}
			// The retired names no longer appear as registered subcommands.
			for _, retired := range []string{"\n  show ", "\n  info ", "\n  pr-info "} {
				if strings.Contains(got, retired) {
					t.Errorf("%s: retired subcommand %q still listed as available: %q", tc.name, retired, got)
				}
			}
			// The surviving name IS listed.
			if !strings.Contains(got, "\n  view ") {
				t.Errorf("%s: expected surviving `view` subcommand listed in fallback help: %q", tc.name, got)
			}
		})
	}
}

// TestPRView_SiblingSubcommandsUnaffected proves `pr list`, `pr files` and
// `pr commits` still resolve after `pr view` runs in the same process, and
// that `pr files --base <ref>` still honors its OWN --base flag when run
// immediately after `pr view` WITHOUT an intervening resetPRFlags() — the
// shared prF flag-bleed hazard resetPRFlags() exists for (pr.go's prFlags is
// one struct shared by every `pr` subcommand).
func TestPRView_SiblingSubcommandsUnaffected(t *testing.T) {
	resetPRFlags()
	setViewStateHome(t, false)
	// pr files/commits below shell out to `git diff`/`git log` against the
	// process's actual cwd (gitlocal.ChangedFiles/Commits), which is NOT a git
	// checkout when this test runs inside a `nix build` sandbox (the flake
	// source is copied without .git). Give it one via the package's own
	// temp-repo helper (branch_test.go's initRepoForCLI) so the test is
	// hermetic under both a plain `go test` and the sandboxed nix check.
	tmp := t.TempDir()
	initRepoForCLI(t, tmp)
	t.Chdir(tmp)

	var stdout1, stderr1 bytes.Buffer
	rootCmd.SetOut(&stdout1)
	rootCmd.SetErr(&stderr1)
	rootCmd.SetArgs([]string{"pr", "view", "7", "--repo", "foo/bar", "--json"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("pr view: %v (stderr=%s)", err, stderr1.String())
	}

	// Deliberately NO resetPRFlags() here: prove `pr list` still resolves
	// (and, since it registers its own --repo, isn't left stuck on `view`'s
	// leftover --repo value in a way that breaks it) even though `view` just
	// set prF.jsonOutput=true and prF.repo="foo/bar" on the shared struct.
	var stdout2, stderr2 bytes.Buffer
	rootCmd.SetOut(&stdout2)
	rootCmd.SetErr(&stderr2)
	rootCmd.SetArgs([]string{"pr", "list", "--repo", "foo/bar"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("pr list after pr view: %v (stderr=%s)", err, stderr2.String())
	}

	// pr files --base <ref>: the base value must be THIS invocation's flag,
	// not any leftover state — files doesn't share a `base` default with
	// view (view registers no --base at all), so this also proves view's
	// flag registration didn't clobber prFilesCmd's.
	var stdout3, stderr3 bytes.Buffer
	rootCmd.SetOut(&stdout3)
	rootCmd.SetErr(&stderr3)
	rootCmd.SetArgs([]string{"pr", "files", "--base", "HEAD"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("pr files --base after pr view: %v (stderr=%s)", err, stderr3.String())
	}
	if prF.base != "HEAD" {
		t.Errorf("prF.base = %q, want %q (flag bleed from a prior `pr` subcommand)", prF.base, "HEAD")
	}

	var stdout4, stderr4 bytes.Buffer
	rootCmd.SetOut(&stdout4)
	rootCmd.SetErr(&stderr4)
	rootCmd.SetArgs([]string{"pr", "commits", "--base", "HEAD"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("pr commits after pr view: %v (stderr=%s)", err, stderr4.String())
	}
}
