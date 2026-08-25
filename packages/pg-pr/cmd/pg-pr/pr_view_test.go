package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/beadsbridge"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/prlock"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/sync"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
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

// TestRetainedInvocationsStillResolve is pg2-4dz88.5.8's compatibility proof:
// every in-repo doc/skill/agent caller that used to invoke the retired
// `pr show`/`pr info` spellings (removed outright by pg2-4dz88.5.7 — see
// TestRetiredNames_RemovedOutright above) has been repointed at the
// surviving `pr view` command, and this table proves each caller's exact
// argv — mechanically renamed only (`show`/`info` -> `view`; no flag added
// or removed) — still resolves through rootCmd. Two or more callers sharing
// the identical argv shape are collapsed into one row; each row's comment
// cites every caller file:line that maps to it.
//
// The caller list was re-grepped against the working tree immediately
// before writing this test (this workspace's premise-freshness convention —
// the list this bead was handed was compiled 2026-08-21 and had drifted by
// one citation: pg-pr-write-pr-description/SKILL.md:58 is a prose
// reference to `pr show`'s return shape with no `pg-pr` prefix, so the
// original `pg-pr pr show` grep pattern missed it even though it names the
// same retired command).
func TestRetainedInvocationsStillResolve(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		wantJSON bool
	}{
		{
			// Bare form: a PR-number placeholder, no flags at all.
			// Historical callers:
			//   claude-marketplace/pg-pr/skills/pg-pr-workflow/SKILL.md:15
			//     ("pg-pr pr show <n>")
			//   claude-marketplace/pg-pr/skills/pg-pr-process-feedback/SKILL.md:84
			//     ("pg-pr pr show" mentioned with no PR-number placeholder at
			//     all — not independently a distinct, runnable argv, so it is
			//     folded into this row rather than given its own).
			name: "bare_no_flags",
			args: []string{"pr", "view", "7"},
		},
		{
			// `--json` only, no `--repo`. Historical callers (all identical
			// after the show/info -> view rename):
			//   claude-marketplace/pg-pr/commands/check-my-pr.md:31
			//   claude-marketplace/pg-pr/commands/check-my-pr.md:51
			//   claude-marketplace/pg-pr/commands/checkout-pr.md:21
			//   claude-marketplace/pg-pr/agents/pg-pr-review-jira-alignment.md:44
			//   claude-marketplace/pg-pr/agents/pg-pr-review-pr-structure.md:29
			name:     "json_no_repo",
			args:     []string{"pr", "view", "7", "--json"},
			wantJSON: true,
		},
		{
			// `--repo` + `--json`. Historical caller:
			//   claude-marketplace/pg-pr/skills/pg-pr-write-pr-description/SKILL.md:49
			// (The body-field claim this same skill made about this exact
			// invocation is checked separately —
			// TestPRView_WriteDescriptionCaller_BodyFieldCarried below —
			// because it needs its own assertions, not just "did it
			// resolve".)
			name:     "repo_and_json",
			args:     []string{"pr", "view", "7", "--repo", "foo/bar", "--json"},
			wantJSON: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetPRFlags()
			setViewStateHome(t, false)

			// The bare and json_no_repo shapes carry no --repo, so `pr view`
			// must fall back to auto-detecting the repo from the cwd's git
			// remote (resolveRepo) — give it one, exactly like
			// TestPRView_SiblingSubcommandsUnaffected does above.
			hasRepoFlag := false
			for _, a := range tc.args {
				if a == "--repo" {
					hasRepoFlag = true
				}
			}
			if !hasRepoFlag {
				tmp := t.TempDir()
				initRepoForCLI(t, tmp)
				t.Chdir(tmp)
			}

			var stdout, stderr bytes.Buffer
			rootCmd.SetOut(&stdout)
			rootCmd.SetErr(&stderr)
			rootCmd.SetArgs(tc.args)

			if err := rootCmd.Execute(); err != nil {
				t.Fatalf("execute %v: %v (stderr=%s)", tc.args, err, stderr.String())
			}

			if tc.wantJSON {
				var doc map[string]any
				if err := json.Unmarshal(stdout.Bytes(), &doc); err != nil {
					t.Fatalf("%v --json output is not valid JSON: %v\noutput:\n%s", tc.args, err, stdout.String())
				}
			}
		})
	}
}

// TestPRView_WriteDescriptionCaller_BodyFieldCarried is the CLI round-trip
// check for the pg-pr-write-pr-description caller (SKILL.md:49): confirm,
// through the real CLI path rather than trusting pkg/api.PR's Body field's
// json tag alone, that `pr view --json`'s output carries the PR's own
// description text the way the retired `pr show` used to.
//
// This test used to be named …_BodyFieldNotCarried and documented the
// opposite: pg2-4dz88.5.7's `pr show` -> `pr view` consolidation had dropped
// api.PR.Body entirely (Assemble never copied it onto View.Identity, and
// storeRowToAPIPR never copied it out of the store row either) — a real
// regression, since the retired `pr show` used to marshal the live-provider
// api.PR (which always carries Body) directly. pg2-1o1dp closed that gap:
// internal/store.PullRequest now persists Body (schema v15) alongside the
// sibling host-derived columns (author/branch/base/url/head_sha),
// cmd/pg-pr/pr_view.go's storeRowToAPIPR copies it back out, and
// internal/prview.Assemble copies it onto View.Identity — so this test now
// asserts the CORRECT (carried-through) behavior instead of the gap.
// pg-pr-write-pr-description/SKILL.md's `gh pr view --json body` workaround
// is reverted in the same change; the skill relies on `pr view` again.
func TestPRView_WriteDescriptionCaller_BodyFieldCarried(t *testing.T) {
	resetPRFlags()
	setViewStateHome(t, true)

	db, err := store.Open(store.DefaultPath())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	ctx := context.Background()
	const wantBody = "## Summary\nThis PR does the thing.\n"
	if _, err := db.UpsertPR(ctx, store.PullRequest{
		Repo: "foo/bar", Number: 7, Ownership: "mine", State: "open",
		Author: "phillipg", Branch: "feat/x", Base: "main",
		URL: "https://github.com/foo/bar/pull/7", HeadSHA: "abc123",
		Body: wantBody,
	}); err != nil {
		t.Fatalf("upsert pr: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"pr", "view", "7", "--repo", "foo/bar", "--json"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute: %v (stderr=%s)", err, stderr.String())
	}

	var doc map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &doc); err != nil {
		t.Fatalf("output is not valid JSON: %v\noutput:\n%s", err, stdout.String())
	}
	identity, ok := doc["identity"].(map[string]any)
	if !ok {
		t.Fatalf("expected identity object in output: %v", doc)
	}
	got, present := identity["body"]
	if !present {
		t.Fatalf("identity.body is missing from the output: %v", doc)
	}
	if got != wantBody {
		t.Errorf("identity.body = %q, want %q", got, wantBody)
	}
}

// dropTable removes a table from the store file via a SECOND raw connection,
// opened and closed after the seeding store.Open/Close has already run the
// schema up to schemaVersion. Because migrate() only compares the stored
// user_version against schemaVersion (it does not re-run any CREATE TABLE
// statement once the two already match), the next store.Open call inside
// loadPRView migrates cleanly and only the dropped table's own queries fail —
// letting a test fault-inject exactly one of GetPR/ListRevisions/ListFeedback
// without disturbing the others. Mirrors internal/sync/prevents_test.go's
// breakOutbox (a second raw connection observing/mutating the same file).
func dropTable(t *testing.T, path, table string) {
	t.Helper()
	raw, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("open raw sqlite: %v", err)
	}
	defer func() { _ = raw.Close() }()
	if _, err := raw.Exec("DROP TABLE " + table); err != nil {
		t.Fatalf("drop table %s: %v", table, err)
	}
}

// errWriter always fails. It pins pr_view.go's bare
// `_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s\n", b); return err` — there is
// no branch to take here, so the mutation under test (error_nilify: replace
// `return err` with `return nil`) can only be caught by a writer that
// actually fails; a bytes.Buffer (every other test in this file) never does.
type errWriter struct{ err error }

func (w errWriter) Write([]byte) (int, error) { return 0, w.err }

// TestPRView_ResolveRepoErrorPropagates kills the pr_view.go:46-47 survivors
// (`repo, err := resolveRepo(ctx, prF.repo); if err != nil { return err }`).
// With no --repo flag and a cwd outside any git worktree, resolveRepo's own
// branch.Detect call fails ("not in a git repository"), resolveRepo wraps it
// as "auto-detect repo: ...; pass --repo owner/name", and this test proves
// prViewCmd.RunE actually returns that error rather than swallowing it.
// Mirrors branch_test.go's TestBranchDetectOutsideGitRepoFails.
func TestPRView_ResolveRepoErrorPropagates(t *testing.T) {
	resetPRFlags()
	tmp := t.TempDir() // not a git repo
	t.Chdir(tmp)
	t.Setenv("PATH", filepath.Dir(mustLookPath(t, "git")))

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"pr", "view", "7"})

	err := rootCmd.Execute()
	if err == nil {
		t.Fatalf("expected error outside git repo with no --repo, got stdout=%q", stdout.String())
	}
	if !strings.Contains(err.Error(), "auto-detect repo") {
		t.Fatalf("err = %v, want it to contain %q", err, "auto-detect repo")
	}
}

// TestPRView_JSONWriteErrorPropagates kills the pr_view.go:59 survivor (the
// bare `return err` after the JSON Fprintf). It gives `pr view --json` a
// writer that always fails and confirms rootCmd.Execute() surfaces that exact
// error — with a bytes.Buffer (which never fails) this line is unreachable in
// every other test in this file.
func TestPRView_JSONWriteErrorPropagates(t *testing.T) {
	resetPRFlags()
	setViewStateHome(t, false)

	wantErr := errors.New("boom: stdout write failed")
	var stderr bytes.Buffer
	rootCmd.SetOut(errWriter{err: wantErr})
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"pr", "view", "7", "--repo", "foo/bar", "--json"})

	err := rootCmd.Execute()
	if !errors.Is(err, wantErr) {
		t.Fatalf("execute error = %v, want %v", err, wantErr)
	}
}

// TestPRView_StoreRowNotFound kills the pr_view.go:89 survivor
// (`if row == nil { return prview.Assemble(in), nil }`). Unlike
// TestPRView_HumanOutput_NoStore (no store FILE at all, so loadPRView never
// calls GetPR), this test opens a real store — migrated, schema present —
// with no matching PR row, so db.GetPR itself runs and returns (nil, nil).
// Ungapping the `row == nil` guard would dereference that nil *PullRequest at
// pr_view.go:92 (`storeRowToAPIPR(*row)`) and panic; the correct code instead
// falls back to the same identity-only Assemble(in) rendering as the no-store
// case, which this test asserts.
func TestPRView_StoreRowNotFound(t *testing.T) {
	resetPRFlags()
	setViewStateHome(t, true)

	db, err := store.Open(store.DefaultPath())
	if err != nil {
		t.Fatalf("open store: %v", err)
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
	for _, want := range []string{"  repo: foo/bar", "  number: 7"} {
		if !strings.Contains(got, want) {
			t.Errorf("expected %q in output: %q", want, got)
		}
	}
}

// TestPRView_StoreOpenErrorPropagates kills the pr_view.go:80-81 survivors
// (`db, err := store.Open(...); if err != nil { return prview.View{}, err }`).
// os.Stat sees a file (so loadPRView proceeds past its no-store fast path),
// but the file's bytes are not a SQLite database at all, so the first real
// query inside store.Open's migrate() (`PRAGMA user_version`) fails to even
// connect. A zero-length file would NOT reproduce this — SQLite treats an
// empty file as a brand-new, valid database — so the corruption must be
// non-empty bytes with the wrong header.
func TestPRView_StoreOpenErrorPropagates(t *testing.T) {
	resetPRFlags()
	setViewStateHome(t, true)

	if err := os.WriteFile(store.DefaultPath(), bytes.Repeat([]byte{0xFF}, 512), 0o644); err != nil {
		t.Fatalf("write garbage store file: %v", err)
	}

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"pr", "view", "7", "--repo", "foo/bar"})

	err := rootCmd.Execute()
	if err == nil {
		t.Fatalf("expected error opening a corrupt store file, got stdout=%q", stdout.String())
	}
	if !strings.Contains(err.Error(), "store:") {
		t.Fatalf("err = %v, want it to contain %q", err, "store:")
	}
}

// TestPRView_GetPRErrorPropagates kills the pr_view.go:86-87 survivors
// (`row, err := db.GetPR(...); if err != nil { return prview.View{}, err }`).
// The store is migrated (so store.Open itself succeeds) and then the
// pull_request table is dropped out from under it via a second connection —
// GetPR's own SELECT then fails with "no such table", independent of whether
// any row would have matched.
func TestPRView_GetPRErrorPropagates(t *testing.T) {
	resetPRFlags()
	setViewStateHome(t, true)

	db, err := store.Open(store.DefaultPath())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	dropTable(t, store.DefaultPath(), "pull_request")

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"pr", "view", "7", "--repo", "foo/bar"})

	err = rootCmd.Execute()
	if err == nil {
		t.Fatalf("expected error with pull_request table missing, got stdout=%q", stdout.String())
	}
	if !strings.Contains(err.Error(), "get pr") {
		t.Fatalf("err = %v, want it to contain %q", err, "get pr")
	}
}

// TestPRView_ListRevisionsErrorPropagates kills the pr_view.go:96-97
// survivors (`revs, err := db.ListRevisions(...); if err != nil { return
// prview.View{}, err }`). A real PR row is seeded first (so GetPR succeeds
// and the row != nil path is taken), then only the pr_revision table is
// dropped — leaving pull_request and feedback intact — so ListRevisions is
// the one call that fails. Without the `err != nil` check, loadPRView would
// silently continue with revs == nil and go on to call ListFeedback (which
// still succeeds), so the test also proves the CLI call as a whole fails
// rather than merely checking loadPRView in isolation.
func TestPRView_ListRevisionsErrorPropagates(t *testing.T) {
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
	if err := db.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	dropTable(t, store.DefaultPath(), "pr_revision")

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"pr", "view", "7", "--repo", "foo/bar"})

	err = rootCmd.Execute()
	if err == nil {
		t.Fatalf("expected error with pr_revision table missing, got stdout=%q", stdout.String())
	}
	if !strings.Contains(err.Error(), "list revisions") {
		t.Fatalf("err = %v, want it to contain %q", err, "list revisions")
	}
}

// TestPRView_ListFeedbackErrorPropagates kills the pr_view.go:102-103
// survivors (`fb, err := db.ListFeedback(...); if err != nil { return
// prview.View{}, err }`). Same shape as
// TestPRView_ListRevisionsErrorPropagates, but the seeded row is left with
// pr_revision intact (so ListRevisions succeeds first) and only the feedback
// table is dropped, isolating this one call.
func TestPRView_ListFeedbackErrorPropagates(t *testing.T) {
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
	if err := db.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	dropTable(t, store.DefaultPath(), "feedback")

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"pr", "view", "7", "--repo", "foo/bar"})

	err = rootCmd.Execute()
	if err == nil {
		t.Fatalf("expected error with feedback table missing, got stdout=%q", stdout.String())
	}
	if !strings.Contains(err.Error(), "list feedback") {
		t.Fatalf("err = %v, want it to contain %q", err, "list feedback")
	}
}

// ----------------------------------------------------------------------
// --force-reload (pg2-4dz88.6.4)
// ----------------------------------------------------------------------

// setViewSyncStubs overrides loadConfigForCLI and newSyncEngineForCLI for a
// `pr view --force-reload` test, mirroring sync_test.go's setStubsForSync.
// It deliberately leaves sync.Deps.StateDir UNSET — unlike setStubsForSync,
// which sets it to t.TempDir() for its own tests — so the constructed
// engine's store file resolves through defaultStoreFile()'s own
// XDG_STATE_HOME lookup: the SAME resolution store.DefaultPath() uses. That
// is load-bearing here: a SyncPR write through this engine must be visible
// to loadPRView's own subsequent store.Open(store.DefaultPath()) re-read, so
// the caller must have pointed XDG_STATE_HOME at the same temp dir first
// (setViewStateHome does this).
//
// calledPtr, when non-nil, is set true the first time newSyncEngineForCLI
// runs — proving --force-reload actually reached engine construction rather
// than the wiring silently no-op'ing.
func setViewSyncStubs(t *testing.T, vcsProv sync.VCSProvider, cfg *config.Config, now func() time.Time, calledPtr *bool) func() {
	t.Helper()
	prevCfg := loadConfigForCLI
	prevEng := newSyncEngineForCLI
	loadConfigForCLI = func(_ context.Context) (*config.Config, error) { return cfg, nil }
	newSyncEngineForCLI = func(c *config.Config) (*sync.Engine, error) {
		if calledPtr != nil {
			*calledPtr = true
		}
		return sync.New(sync.Deps{
			Cfg: c,
			VCS: map[string]sync.VCSProvider{"github": vcsProv},
			Now: now,
		})
	}
	return func() {
		loadConfigForCLI = prevCfg
		newSyncEngineForCLI = prevEng
	}
}

// TestPRView_ForceReload_CallsSyncEngine proves `pr view --force-reload`
// reaches newSyncEngineForCLI/Engine.SyncPR — this bead's own wiring — the
// counterpart to TestPRView_NoNetworkCall's proof that the default (no-flag)
// path never calls the VCS provider at all.
func TestPRView_ForceReload_CallsSyncEngine(t *testing.T) {
	resetPRFlags()
	setViewStateHome(t, false)

	cfg := minimalCLICfg()
	vcs := &stubVCS{prs: map[string][]api.PR{"foo/bar": {samplePR(7)}}}
	var called bool
	defer setViewSyncStubs(t, vcs, cfg, nil, &called)()

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"pr", "view", "7", "--repo", "foo/bar", "--force-reload"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute: %v (stderr=%s)", err, stderr.String())
	}
	if !called {
		t.Error("expected --force-reload to call newSyncEngineForCLI (Engine.SyncPR)")
	}
}

// TestPRView_NoForceReload_NoSyncEngineCall proves `pr view`'s default (no
// --force-reload) path never reaches newSyncEngineForCLI/Engine.SyncPR — the
// higher CLI-wiring counterpart to TestPRView_NoNetworkCall above, which
// pins the lower vcsProviderFor seam instead.
func TestPRView_NoForceReload_NoSyncEngineCall(t *testing.T) {
	resetPRFlags()
	setViewStateHome(t, false)

	prev := newSyncEngineForCLI
	t.Cleanup(func() { newSyncEngineForCLI = prev })
	newSyncEngineForCLI = func(*config.Config) (*sync.Engine, error) {
		t.Fatal("pr view without --force-reload must not call newSyncEngineForCLI")
		return nil, nil
	}

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"pr", "view", "7", "--repo", "foo/bar"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute: %v (stderr=%s)", err, stderr.String())
	}
}

// TestPRView_ForceReload_AsOfReflectsPostRefresh proves INV-ASOF-1 for the
// new flag: with --force-reload, the rendered as-of time comes from the
// POST-refresh store row Engine.SyncPR just wrote (stamped with this test's
// own fixed engine clock), not the pre-existing, easily-distinguishable
// LastSyncedAt the row was seeded with before the refresh ran.
func TestPRView_ForceReload_AsOfReflectsPostRefresh(t *testing.T) {
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
		LastSyncedAt: "2000-01-01T00:00:00Z",
	}); err != nil {
		t.Fatalf("upsert pr: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	cfg := minimalCLICfg()
	vcs := &stubVCS{prs: map[string][]api.PR{"foo/bar": {samplePR(7)}}}
	fixedNow := time.Date(2030, 6, 15, 12, 0, 0, 0, time.UTC)
	defer setViewSyncStubs(t, vcs, cfg, func() time.Time { return fixedNow }, nil)()

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"pr", "view", "7", "--repo", "foo/bar", "--force-reload", "--json"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("execute: %v (stderr=%s)", err, stderr.String())
	}
	var doc map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &doc); err != nil {
		t.Fatalf("output is not valid JSON: %v\noutput:\n%s", err, stdout.String())
	}
	wantAsOf := fixedNow.UTC().Format(time.RFC3339)
	if got := doc["as_of"]; got != wantAsOf {
		t.Errorf("as_of = %v, want %v (post-refresh, not the pre-refresh 2000-01-01 row)", got, wantAsOf)
	}
}

// TestPRView_ForceReloadError_Propagates proves a SyncPR failure (here: the
// stub VCS provider has no PR #7 configured, so provider.GetPR fails)
// surfaces as `pr view --force-reload`'s own command error rather than being
// silently swallowed and falling through to a (stale/wrong) rendered view.
func TestPRView_ForceReloadError_Propagates(t *testing.T) {
	resetPRFlags()
	setViewStateHome(t, false)

	cfg := minimalCLICfg()
	vcs := &stubVCS{} // no PRs configured -> GetPR always "not found"
	defer setViewSyncStubs(t, vcs, cfg, nil, nil)()

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"pr", "view", "7", "--repo", "foo/bar", "--force-reload"})

	err := rootCmd.Execute()
	if err == nil {
		t.Fatalf("expected the SyncPR fetch error to propagate, got stdout=%q", stdout.String())
	}
	if !strings.Contains(err.Error(), "sync PR") {
		t.Fatalf("err = %v, want it to contain %q", err, "sync PR")
	}
	// No view was rendered from stale/absent data: the only stdout content
	// on this error path is cobra's own default usage text (unrelated to
	// prview.RenderHuman/MarshalView), never the "repo:"/"number:" markers
	// loadPRView's rendering would have produced had it run.
	for _, mustNotContain := range []string{"repo: foo/bar", "number: 7"} {
		if strings.Contains(stdout.String(), mustNotContain) {
			t.Errorf("expected no rendered view output on a propagated SyncPR error, found %q in %q", mustNotContain, stdout.String())
		}
	}
}

// TestPRView_ForceReload_LockGiveUpSurfacesBusyExit is pr_view's counterpart
// to sync_test.go's TestSyncCommand_SinglePR_LockGiveUpSurfacesBusyExit: it
// proves Engine.SyncPR's call path, wired through forceReloadPR the same way
// syncCmd.RunE wires its own one-shot --pr path, reaches the SAME registered
// beadsbridge handler that carries the cross-process per-PR lock
// (pg2-4dz88.6.3) — so --force-reload inherits that protection with no new
// locking code in pr_view.go, and a give-up surfaces through `pr view`'s own
// RunE return, classified by main's existing exitCodeFor as exitBusy.
func TestPRView_ForceReload_LockGiveUpSurfacesBusyExit(t *testing.T) {
	resetPRFlags()
	setViewStateHome(t, false)

	cfg := minimalCLICfg()
	cfg.Repos[0].Path = t.TempDir() // required for newBeadsBridgeHandler's repo->path index
	vcs := &stubVCS{prs: map[string][]api.PR{"foo/bar": {samplePR(9)}}}
	defer setViewSyncStubs(t, vcs, cfg, nil, nil)()

	fake := &fakeBridgeBeads{
		findUncachedErr: fmt.Errorf("beadsbridge: await cross-process projection lock for foo/bar#9: %w", prlock.ErrTimeout),
	}
	prevClient := newBeadClientForRepo
	newBeadClientForRepo = func(string) beadsbridge.BeadClient { return fake }
	defer func() { newBeadClientForRepo = prevClient }()

	var stdout, stderr bytes.Buffer
	rootCmd.SetOut(&stdout)
	rootCmd.SetErr(&stderr)
	rootCmd.SetArgs([]string{"pr", "view", "9", "--repo", "foo/bar", "--force-reload"})

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected the lock give-up to surface as the command's error")
	}
	if !errors.Is(err, prlock.ErrTimeout) {
		t.Fatalf("execute error = %v, want an error wrapping prlock.ErrTimeout", err)
	}
	if got := exitCodeFor(err); got != exitBusy {
		t.Errorf("exitCodeFor(err) = %d, want exitBusy (%d)", got, exitBusy)
	}
}
