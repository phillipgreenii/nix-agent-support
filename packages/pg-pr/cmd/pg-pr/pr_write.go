package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/branch"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/output"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/prlock"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/sync"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/beads"
	"github.com/spf13/cobra"
)

// prWriteFlags holds the parsed CLI flags for the `pg-pr pr` write
// subcommands (create / update / close / ready / draft / merge / automerge).
//
// All write subcommands share these flags. They live in their own struct so
// resetting between tests doesn't clobber the read-side prF flags.
type prWriteFlags struct {
	jsonOutput bool
	repo       string

	// create-only.
	title     string
	head      string
	base      string
	reviewers string
	labels    string
	noDraft   bool

	// body sources (shared by create + update).
	body      string
	bodyFile  string
	bodyStdin bool

	// LLM-driven description generation (shared by create + update).
	// When generateDesc is true, the CLI shells out to an agent CLI
	// (default: `zr-agent` on PATH) which loads the
	// pg-pr-write-pr-description SKILL and emits the body on stdout.
	// Mutually exclusive with --body / --body-file / --body-stdin.
	generateDesc bool
	agentCLI     string
	skillPath    string

	// LLM-driven title generation (create-only; pg2-4dz88.8.4). When
	// generateTitle is true, the CLI shells out to an agent CLI (same
	// resolution as generateDesc's agentCLI/skillPath -- --agent-cli is
	// shared verbatim; --skill-path is shared too, but resolves against a
	// DIFFERENT default relative path when left unset, since the title
	// generator loads pg-pr-write-pr-title's own SKILL.md, not the
	// description skill's) which loads the pg-pr-write-pr-title SKILL and
	// emits the title on stdout. Mutually exclusive with --title. Never
	// registered on `pr update` (see addGenerateTitleFlags).
	generateTitle bool
}

var prWF prWriteFlags

// beadsClientForPR is a var so tests can swap in an in-memory client.
//
// The factory takes the absolute monorepo root the bd Client should target
// (so bd discovers the right .beads/ workspace). Production callers resolve
// the path via resolveRepoPath; tests typically ignore the argument and
// return a shared in-memory fake.
var beadsClientForPR = func(dir string) beadsMergeRequestClient {
	return beads.NewClientForRepo(dir)
}

// mergeRequestLock is the cross-process per-PR-identity lock `pr create` takes
// around its own EnsureMergeRequest call, guarding the SAME race
// internal/beadsbridge.Handler.Handle guards for the sync/daemon path (bead
// pg2-4dz88.6.3, INV-MR-1): this command and `pg-pr sync`/the daemon are
// separate OS processes that can both try to create the merge-request bead for
// the SAME PR, and an in-process mutex cannot serialize separate processes —
// only a shared flock file can. Both sides resolve to the same on-disk lock
// file because both construct a Locker with the DEFAULT LockDir
// (internal/prlock.Options.LockDir's default, $XDG_RUNTIME_DIR/pg-pr/locks);
// the key is "owner/name#<number>", the exact shape internal/beadsbridge
// derives (see its prIdentityKey).
//
// Tests MUST override this with a Locker pointed at a t.TempDir() LockDir —
// this package's TestMain (main_test.go) already does so for every test in
// this package; never exercise the real default path from a test.
var mergeRequestLock = prlock.New(prlock.Options{})

// beadsMergeRequestClient narrows the beads.Client API to the methods used
// here; tests can satisfy it with an in-memory fake.
type beadsMergeRequestClient interface {
	EnsureMergeRequest(ctx context.Context, userTitle string, fields beads.MergeRequestFields) (string, bool, error)
	CloseMergeRequest(ctx context.Context, id, reason string) error
	FindByRepoAndNumber(ctx context.Context, repo string, prNumber int) (*beads.MergeRequest, error)
}

// resolveBody picks the PR body from at most one of --body, --body-file,
// --body-stdin. Returns an error if more than one is provided. An empty
// return string is allowed (caller decides if that's fatal).
func resolveBody(cmd *cobra.Command, body, bodyFile string, bodyStdin bool) (string, error) {
	count := 0
	if body != "" {
		count++
	}
	if bodyFile != "" {
		count++
	}
	if bodyStdin {
		count++
	}
	if count > 1 {
		return "", errors.New("specify at most one of --body, --body-file, --body-stdin")
	}
	if body != "" {
		return body, nil
	}
	if bodyFile != "" {
		if bodyFile == "-" {
			raw, err := io.ReadAll(cmd.InOrStdin())
			if err != nil {
				return "", fmt.Errorf("read stdin: %w", err)
			}
			return string(raw), nil
		}
		raw, err := os.ReadFile(bodyFile)
		if err != nil {
			return "", fmt.Errorf("read --body-file: %w", err)
		}
		return string(raw), nil
	}
	if bodyStdin {
		raw, err := io.ReadAll(cmd.InOrStdin())
		if err != nil {
			return "", fmt.Errorf("read stdin: %w", err)
		}
		return string(raw), nil
	}
	return "", nil
}

// ----------------------------------------------------------------------
// --generate-description plumbing
// ----------------------------------------------------------------------

// defaultSkillRelPath is the local-marketplace path where pg-pr-plugin
// installs the SKILL.md (see home/programs/pg-pr-plugin/default.nix).
const defaultSkillRelPath = ".local/share/pgii-local-plugins/pg-pr/skills/pg-pr-write-pr-description/SKILL.md"

// defaultTitleSkillRelPath is the sibling install path for the
// pg-pr-write-pr-title SKILL.md (pg2-4dz88.8.4), under the SAME
// pgii-local-plugins root defaultSkillRelPath resolves against.
const defaultTitleSkillRelPath = ".local/share/pgii-local-plugins/pg-pr/skills/pg-pr-write-pr-title/SKILL.md"

// agentCLIEnv is the env var that overrides the agent-CLI binary used
// by --generate-description / --generate-title.
const agentCLIEnv = "PG_PR_AGENT_CLI"

// skillPathEnv lets callers point at an alternative SKILL.md (mostly
// for tests and for non-standard plugin install locations). Shared by
// --generate-description and --generate-title; when unset, each falls
// back to its OWN default (defaultSkillRelPath vs
// defaultTitleSkillRelPath) via resolveSkillPathWithDefault.
const skillPathEnv = "PG_PR_SKILL_PATH"

// generateDescriptionConflictMsg is the error returned when
// --generate-description is combined with any --body* flag. Kept as a
// constant so tests can assert on it.
const generateDescriptionConflictMsg = "--generate-description is mutually exclusive with --body, --body-file, and --body-stdin"

// generateTitleConflictMsg is the error returned when --generate-title
// is combined with --title (pg2-4dz88.8.4 Fork 2, resolved as a hard
// conflict matching generateDescriptionConflictMsg's precedent exactly).
// Kept as a constant so tests can assert on it.
const generateTitleConflictMsg = "--generate-title is mutually exclusive with --title"

// missingAgentCLIMsg is the error returned when no agent CLI can be
// resolved for --generate-description. Kept as a constant so tests can
// assert on it.
const missingAgentCLIMsg = "--generate-description requires an agent CLI: set --agent-cli <path>, the PG_PR_AGENT_CLI env var, or place 'zr-agent' on your PATH (the SKILL at %s can also be invoked directly from a claude session)"

// missingAgentCLITitleMsg is missingAgentCLIMsg's sibling for
// --generate-title. Kept as a constant so tests can assert on it.
const missingAgentCLITitleMsg = "--generate-title requires an agent CLI: set --agent-cli <path>, the PG_PR_AGENT_CLI env var, or place 'zr-agent' on your PATH (the SKILL at %s can also be invoked directly from a claude session)"

// runAgentCLIPipingSkill is the shared subprocess-invocation shape used
// by both generateDescription and generateTitle: it pipes the SKILL.md
// bytes on the agent CLI's stdin and captures stdout. The SKILL itself
// instructs the agent to call back into `pg-pr` for diff/PR context, so
// stdin only ever carries the prompt.
func runAgentCLIPipingSkill(ctx context.Context, agentCLI, skillPath string) (string, error) {
	skillBytes, err := os.ReadFile(skillPath)
	if err != nil {
		return "", fmt.Errorf("read skill: %w", err)
	}
	cmd := exec.CommandContext(ctx, agentCLI)
	cmd.Stdin = bytes.NewReader(skillBytes)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("agent CLI %s failed: %w; stderr=%s",
			agentCLI, err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

// generateDescription shells out to the configured agent CLI, passing
// the SKILL.md path on stdin and capturing the body on stdout. Exposed
// as a package-level var so tests can inject a fake.
var generateDescription = func(ctx context.Context, agentCLI, skillPath string) (string, error) {
	return runAgentCLIPipingSkill(ctx, agentCLI, skillPath)
}

// generateTitle is generateDescription's sibling for --generate-title
// (pg2-4dz88.8.4): same agent-CLI shell-out shape, pointed at the
// pg-pr-write-pr-title SKILL instead. Exposed as a package-level var so
// tests can inject a fake -- no unit test may resolve a real agent CLI
// or make a network call.
var generateTitle = func(ctx context.Context, agentCLI, skillPath string) (string, error) {
	return runAgentCLIPipingSkill(ctx, agentCLI, skillPath)
}

// resolveAgentCLI returns the agent CLI binary path. Priority:
//  1. --agent-cli flag.
//  2. PG_PR_AGENT_CLI env var.
//  3. `zr-agent` on PATH.
//
// Returns an empty string when none can be resolved; the caller turns
// that into a user-facing error mentioning the SKILL path.
func resolveAgentCLI(flag string) string {
	if flag != "" {
		return flag
	}
	if v := os.Getenv(agentCLIEnv); v != "" {
		return v
	}
	if p, err := exec.LookPath("zr-agent"); err == nil {
		return p
	}
	return ""
}

// resolveSkillPathWithDefault returns the path to a SKILL.md, given the
// default relative path (under $HOME) to fall back to. Priority:
//  1. --skill-path flag.
//  2. PG_PR_SKILL_PATH env var.
//  3. ~/<defaultRelPath>.
//
// Shared by resolveSkillPath (description) and resolveTitleSkillPath
// (title, pg2-4dz88.8.4) -- both flag and env var are shared verbatim
// between --generate-description and --generate-title; only the
// fallback DEFAULT differs, since each generator loads a different
// SKILL.md when no override is given.
func resolveSkillPathWithDefault(flag, defaultRelPath string) (string, error) {
	if flag != "" {
		return flag, nil
	}
	if v := os.Getenv(skillPathEnv); v != "" {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(home, defaultRelPath), nil
}

// resolveSkillPath returns the path to the pg-pr-write-pr-description
// SKILL.md. Priority:
//  1. --skill-path flag.
//  2. PG_PR_SKILL_PATH env var.
//  3. ~/.local/share/pgii-local-plugins/pg-pr/skills/.../SKILL.md.
func resolveSkillPath(flag string) (string, error) {
	return resolveSkillPathWithDefault(flag, defaultSkillRelPath)
}

// resolveTitleSkillPath is resolveSkillPath's sibling for the
// pg-pr-write-pr-title SKILL.md (pg2-4dz88.8.4). Same flag/env-var
// priority; the fallback default is defaultTitleSkillRelPath instead of
// defaultSkillRelPath.
func resolveTitleSkillPath(flag string) (string, error) {
	return resolveSkillPathWithDefault(flag, defaultTitleSkillRelPath)
}

// runGenerateDescription is the single entry point for both `pr
// create --generate-description` and `pr update --generate-description`.
// It validates flag combos, resolves the agent + skill, executes, and
// returns the captured body.
func runGenerateDescription(ctx context.Context, f prWriteFlags) (string, error) {
	if f.body != "" || f.bodyFile != "" || f.bodyStdin {
		return "", errors.New(generateDescriptionConflictMsg)
	}
	skillPath, err := resolveSkillPath(f.skillPath)
	if err != nil {
		return "", err
	}
	if _, statErr := os.Stat(skillPath); statErr != nil {
		return "", fmt.Errorf("skill file not found at %s: %w", skillPath, statErr)
	}
	agentCLI := resolveAgentCLI(f.agentCLI)
	if agentCLI == "" {
		return "", fmt.Errorf(missingAgentCLIMsg, skillPath)
	}
	body, err := generateDescription(ctx, agentCLI, skillPath)
	if err != nil {
		return "", err
	}
	body = strings.TrimSpace(body)
	if body == "" {
		return "", fmt.Errorf("agent CLI %s produced empty body", agentCLI)
	}
	return body, nil
}

// runGenerateTitle is runGenerateDescription's sibling for `pr create
// --generate-title` (pg2-4dz88.8.4). It is create-only -- `pr update`
// never registers --generate-title (see addGenerateTitleFlags), so this
// is never called from the update path. It validates the --title
// conflict, resolves the agent + pg-pr-write-pr-title SKILL, executes,
// and returns the captured title.
func runGenerateTitle(ctx context.Context, f prWriteFlags) (string, error) {
	if f.title != "" {
		return "", errors.New(generateTitleConflictMsg)
	}
	skillPath, err := resolveTitleSkillPath(f.skillPath)
	if err != nil {
		return "", err
	}
	if _, statErr := os.Stat(skillPath); statErr != nil {
		return "", fmt.Errorf("skill file not found at %s: %w", skillPath, statErr)
	}
	agentCLI := resolveAgentCLI(f.agentCLI)
	if agentCLI == "" {
		return "", fmt.Errorf(missingAgentCLITitleMsg, skillPath)
	}
	title, err := generateTitle(ctx, agentCLI, skillPath)
	if err != nil {
		return "", err
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return "", fmt.Errorf("agent CLI %s produced empty title", agentCLI)
	}
	return title, nil
}

// detectCurrentBranch returns the current branch name when cwd is inside a
// git repository. Used by `pr create` when --head isn't passed.
func detectCurrentBranch(ctx context.Context) (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	info, err := branch.Detect(ctx, cwd, branch.Options{})
	if err != nil {
		return "", err
	}
	return info.Branch, nil
}

// splitCSV is a small helper for --reviewers / --labels.
func splitCSV(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if v := strings.TrimSpace(p); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// ----------------------------------------------------------------------
// pr create
// ----------------------------------------------------------------------

var prCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Open a new pull request (defaults to draft)",
	Long: `Open a new pull request via the configured VCS provider.

The PR is created in DRAFT state by default. Pass --no-draft to open a
ready-for-review PR directly. The PR body may be supplied via --body,
--body-file <path> (use - for stdin), --body-stdin, or
--generate-description (LLM-driven via the pg-pr-write-pr-description
SKILL; shells out to an agent CLI such as zr-agent). The PR title may be
supplied via --title, or via --generate-title (LLM-driven via the
pg-pr-write-pr-title SKILL, the same shell-out shape). --generate-title
works standalone or together with --generate-description; passing both
invokes the agent CLI twice, once per generator.

On success, a corresponding merge-request bead is created via
beads.EnsureMergeRequest so subsequent pg-pr sync runs treat the PR as
known.

--reviewers and --labels are pushed directly to the upstream PR via
gh's --reviewer/--label flags (one repeated flag per entry).`,
	Args: cobra.NoArgs,
	RunE: runPRCreate,
}

func runPRCreate(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()

	title := prWF.title
	if prWF.generateTitle {
		generated, terr := runGenerateTitle(ctx, prWF)
		if terr != nil {
			return terr
		}
		title = generated
	}
	if strings.TrimSpace(title) == "" {
		return errors.New("pr create: --title is required")
	}

	var body string
	var err error
	if prWF.generateDesc {
		body, err = runGenerateDescription(ctx, prWF)
		if err != nil {
			return err
		}
	} else {
		body, err = resolveBody(cmd, prWF.body, prWF.bodyFile, prWF.bodyStdin)
		if err != nil {
			return err
		}
	}

	repo, err := resolveRepo(ctx, prWF.repo)
	if err != nil {
		return err
	}

	headBranch := prWF.head
	if headBranch == "" {
		headBranch, err = detectCurrentBranch(ctx)
		if err != nil {
			return fmt.Errorf("auto-detect head branch: %w; pass --head <branch>", err)
		}
	}
	base := prWF.base
	if base == "" {
		base = "origin/main"
	}
	// gh expects a bare branch name for --base; trim the origin/ prefix.
	base = strings.TrimPrefix(base, "origin/")

	draft := !prWF.noDraft
	reviewers := splitCSV(prWF.reviewers)
	labels := splitCSV(prWF.labels)
	provider := vcsProviderFor(repo)
	pr, err := provider.CreatePR(ctx, repo, draft, title, body, headBranch, base, reviewers, labels)
	if err != nil {
		return err
	}

	// Best-effort: record the merge-request bead. An ORDINARY bd failure here
	// doesn't fail the command since the PR is already created upstream — but
	// giving up on the cross-process merge-request lock below is NOT
	// best-effort: it means another pg-pr process may be creating (or already
	// created) the SAME PR's bead right now (INV-MR-1), so it is surfaced as
	// this command's own error (mapped to exitBusy by main.go's exitCodeFor)
	// instead of a warning.
	//
	// The bd Client is scoped to the resolved repo's monorepo root so the
	// bead lands in that monorepo's .beads/ workspace — not whichever
	// workspace happens to match the process cwd.
	beadID := ""
	var lockErr error
	bdc := beadsClientForPR(resolveRepoPath(ctx, repo))
	if bdc != nil {
		key := fmt.Sprintf("%s#%d", repo, pr.Number)
		release, aerr := mergeRequestLock.Acquire(ctx, key)
		if aerr != nil {
			lockErr = fmt.Errorf("pr create: await merge-request lock for %s: %w", key, aerr)
		} else {
			defer release()
			id, _, berr := bdc.EnsureMergeRequest(ctx, title, beads.MergeRequestFields{
				Repo:     repo,
				PRNumber: pr.Number,
				State:    "open",
				Branch:   pr.Branch,
				Base:     pr.Base,
				Author:   pr.Author,
				URL:      pr.URL,
				Draft:    draft,
			})
			if berr == nil {
				beadID = id
			} else {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "WARNING: failed to record merge-request bead: %v\n", berr)
			}
		}
	}

	if output.Resolve(prWF.jsonOutput) {
		if werr := writeJSON(cmd.OutOrStdout(), map[string]any{
			"pr":      pr,
			"bead_id": beadID,
		}); werr != nil {
			return werr
		}
		return lockErr
	}
	state := "draft"
	if !draft {
		state = "ready"
	}
	if _, werr := fmt.Fprintf(cmd.OutOrStdout(),
		"ok Opened PR #%d (%s) on %s: %s\n", pr.Number, state, pr.Repo, pr.URL); werr != nil {
		return werr
	}
	return lockErr
}

// ----------------------------------------------------------------------
// pr update
// ----------------------------------------------------------------------

var prUpdateCmd = &cobra.Command{
	Use:   "update <pr>",
	Short: "Update an existing PR's body",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		num, err := parsePR(args[0])
		if err != nil {
			return err
		}
		ctx := cmd.Context()
		var body string
		if prWF.generateDesc {
			body, err = runGenerateDescription(ctx, prWF)
			if err != nil {
				return err
			}
		} else {
			body, err = resolveBody(cmd, prWF.body, prWF.bodyFile, prWF.bodyStdin)
			if err != nil {
				return err
			}
			if body == "" {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(),
					"pr update: no body source provided (--body / --body-file / --body-stdin / --generate-description); nothing to do")
				return nil
			}
		}
		repo, err := resolveRepo(ctx, prWF.repo)
		if err != nil {
			return err
		}
		if err := vcsProviderFor(repo).UpdatePR(ctx, repo, num, body); err != nil {
			return err
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "ok Updated PR #%d on %s\n", num, repo)
		return err
	},
}

// ----------------------------------------------------------------------
// pr close
// ----------------------------------------------------------------------

var prCloseCmd = &cobra.Command{
	Use:   "close <pr>",
	Short: "Close a PR without merging",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		num, err := parsePR(args[0])
		if err != nil {
			return err
		}
		ctx := cmd.Context()
		repo, err := resolveRepo(ctx, prWF.repo)
		if err != nil {
			return err
		}
		if err := vcsProviderFor(repo).Close(ctx, repo, num); err != nil {
			return err
		}

		// Best-effort: close the corresponding merge-request bead. The
		// cascade rule on close also closes children (processing-cycle /
		// feedback / action) under that bead. If no bead is found, we
		// silently skip — the next sync will reconcile.
		//
		// The bd Client is scoped to the repo's monorepo root so the
		// lookup + close hit the correct .beads/ workspace.
		beadID := ""
		bdc := beadsClientForPR(resolveRepoPath(ctx, repo))
		if bdc != nil {
			mr, ferr := bdc.FindByRepoAndNumber(ctx, repo, num)
			if ferr != nil {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(),
					"WARNING: failed to look up merge-request bead for %s#%d: %v\n",
					repo, num, ferr)
			} else if mr != nil && mr.Status != "closed" {
				if cerr := bdc.CloseMergeRequest(ctx, mr.ID,
					"closed via pg-pr pr close"); cerr != nil {
					_, _ = fmt.Fprintf(cmd.ErrOrStderr(),
						"WARNING: failed to close merge-request bead %s: %v\n",
						mr.ID, cerr)
				} else {
					beadID = mr.ID
				}
			}
		}

		_, err = fmt.Fprintf(cmd.OutOrStdout(), "ok Closed PR #%d on %s\n", num, repo)
		if err != nil {
			return err
		}
		if beadID != "" {
			_, err = fmt.Fprintf(cmd.OutOrStdout(),
				"ok Closed merge-request bead %s\n", beadID)
		}
		return err
	},
}

// ----------------------------------------------------------------------
// pr ready / pr draft
// ----------------------------------------------------------------------

var prReadyCmd = &cobra.Command{
	Use:   "ready <pr>",
	Short: "Mark a draft PR as ready for review",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		num, err := parsePR(args[0])
		if err != nil {
			return err
		}
		ctx := cmd.Context()
		repo, err := resolveRepo(ctx, prWF.repo)
		if err != nil {
			return err
		}
		if err := clearWIPOverrideIfSet(cmd, ctx, repo, num); err != nil {
			return err
		}
		if err := vcsProviderFor(repo).SetDraft(ctx, repo, num, false); err != nil {
			return err
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(),
			"ok PR #%d marked ready for review.\n", num)
		return err
	},
}

// clearWIPOverrideIfSet implements the operator ruling resolving fork #2
// (pg2-4dz88.4.4, 2026-08-24): `pr ready` on a PR whose store-recorded WIP
// flag is true CLEARS WIP as an explicit, logged override, then proceeds to
// call SetDraft(false) exactly as it does today. This SUPERSEDES the
// grooming review's earlier, non-binding recommendation to refuse.
//
// A PR pg-pr has never observed — no store file at all, or a store file
// with no row for this (repo, number) — has no WIP to clear, so this is
// silently a no-op, matching this CLI's existing store-optional read paths
// (cmd/pg-pr/pr_view.go's loadPRView).
func clearWIPOverrideIfSet(cmd *cobra.Command, ctx context.Context, repo string, num int) error {
	if _, statErr := os.Stat(store.DefaultPath()); statErr != nil {
		return nil
	}
	db, err := store.Open(store.DefaultPath())
	if err != nil {
		return fmt.Errorf("pr ready: open store: %w", err)
	}
	defer func() { _ = db.Close() }()

	pr, err := db.GetPR(ctx, repo, num)
	if err != nil {
		return fmt.Errorf("pr ready: read store: %w", err)
	}
	if pr == nil || !pr.WIP {
		return nil
	}
	if err := db.SetWIP(ctx, repo, num, false); err != nil {
		return fmt.Errorf("pr ready: clear wip override: %w", err)
	}
	_, err = fmt.Fprintf(cmd.ErrOrStderr(),
		"OVERRIDE: PR #%d was marked WIP; clearing WIP because it is being marked ready.\n", num)
	return err
}

var prDraftCmd = &cobra.Command{
	Use:   "draft <pr>",
	Short: "Convert a PR back to draft state",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		num, err := parsePR(args[0])
		if err != nil {
			return err
		}
		ctx := cmd.Context()
		repo, err := resolveRepo(ctx, prWF.repo)
		if err != nil {
			return err
		}
		if err := vcsProviderFor(repo).SetDraft(ctx, repo, num, true); err != nil {
			return err
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(),
			"ok PR #%d converted to draft.\n", num)
		return err
	},
}

// ----------------------------------------------------------------------
// pr wip {on, off}
// ----------------------------------------------------------------------

// prWipCmd is the WIP toggle's command surface (pg2-4dz88.4.4, fork #3
// ruling, operator 2026-08-24: a `pr wip` command group with `on`/`off`
// subcommands). WIP is a store-only flag — it is never synced to beads.
var prWipCmd = &cobra.Command{
	Use:   "wip",
	Short: "Toggle the store-only WIP (work-in-progress) suppression flag on a PR",
	Long: `Toggle the WIP flag (pg2-4dz88.4). WIP defaults false, is store-only,
and is never synced to beads.

Turning WIP on converts the PR to draft upstream immediately, even if it is
currently ready for review, by calling SetDraft(true) exactly once.

Turning WIP off does NOT itself return the PR to ready — no upstream call
is made at toggle time. The eventual return to ready is the rebuilt
draft-promotion predicate's job on its next evaluation.`,
}

var prWipOnCmd = &cobra.Command{
	Use:   "on <pr>",
	Short: "Turn WIP on: converts a currently-ready PR to draft",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		num, err := parsePR(args[0])
		if err != nil {
			return err
		}
		ctx := cmd.Context()
		repo, err := resolveRepo(ctx, prWF.repo)
		if err != nil {
			return err
		}
		return runPRWipOn(cmd, ctx, repo, num)
	},
}

var prWipOffCmd = &cobra.Command{
	Use:   "off <pr>",
	Short: "Turn WIP off (does not itself return the PR to ready)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		num, err := parsePR(args[0])
		if err != nil {
			return err
		}
		ctx := cmd.Context()
		repo, err := resolveRepo(ctx, prWF.repo)
		if err != nil {
			return err
		}
		return runPRWipOff(cmd, ctx, repo, num)
	},
}

// runPRWipOn fetches the PR's live state, persists WIP=true via the
// store's dedicated setter (pg2-4dz88.4.2's SetWIP — errors against an
// unknown PR, per that leaf's fork #6 ruling), and applies the WIP-ON
// transition (sync.ApplyWIP): a currently-ready PR is converted to draft
// upstream exactly once; an already-draft or merged/closed PR is left
// alone.
func runPRWipOn(cmd *cobra.Command, ctx context.Context, repo string, num int) error {
	provider := vcsProviderFor(repo)
	pr, err := provider.GetPR(ctx, repo, num)
	if err != nil {
		return fmt.Errorf("pr wip on: fetch PR: %w", err)
	}

	db, err := store.Open(store.DefaultPath())
	if err != nil {
		return fmt.Errorf("pr wip on: open store: %w", err)
	}
	defer func() { _ = db.Close() }()
	if err := db.SetWIP(ctx, repo, num, true); err != nil {
		return fmt.Errorf("pr wip on: %w", err)
	}

	converted, err := sync.ApplyWIP(ctx, provider, repo, *pr, true)
	if err != nil {
		return fmt.Errorf("pr wip on: %w", err)
	}

	if _, err := fmt.Fprintf(cmd.OutOrStdout(), "ok PR #%d marked WIP.\n", num); err != nil {
		return err
	}
	if converted {
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "ok PR #%d converted to draft.\n", num)
	}
	return err
}

// runPRWipOff clears the store-only WIP flag. It makes NO upstream call —
// the eventual return to ready is the rebuilt draft-promotion predicate's
// job on its next evaluation (sibling leaf pg2-4dz88.4.5), not an
// immediate effect of this toggle.
func runPRWipOff(cmd *cobra.Command, ctx context.Context, repo string, num int) error {
	db, err := store.Open(store.DefaultPath())
	if err != nil {
		return fmt.Errorf("pr wip off: open store: %w", err)
	}
	defer func() { _ = db.Close() }()
	if err := db.SetWIP(ctx, repo, num, false); err != nil {
		return fmt.Errorf("pr wip off: %w", err)
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(),
		"ok PR #%d WIP cleared. This does not itself return the PR to ready "+
			"-- that happens on the promotion predicate's next evaluation.\n", num)
	return err
}

// ----------------------------------------------------------------------
// pr hide / pr unhide (pg2-4dz88.4.3)
// ----------------------------------------------------------------------

// prHideCmd sets the store-only USER_HIDDEN flag (+ optional reason) via
// store.SetHidden (pg2-4dz88.4.2). Hiding is DISPLAY-LAYER ONLY: it never
// affects ingestion, and it stays hidden until explicitly unhidden — no
// auto-expiry (operator ruling on the parent bead pg2-4dz88.4). It suppresses
// the PR from every HUMAN-facing surface (pg-pr open, the dashboard's
// Mine/Team rows) unless that surface's --include-hidden is passed; the
// machine seam (`pg-pr pr list --json`) never filters on it at all (fork #1
// ruling, 2026-08-24) — it always reports the flag + reason as fields and
// leaves the judgement to its consumer.
var prHideCmd = &cobra.Command{
	Use:   "hide <pr> [reason]",
	Short: "Hide a PR from human-facing surfaces (pg-pr open, the dashboard)",
	Long: `Hide a PR: sets the store-only USER_HIDDEN flag (+ optional reason).

Hidden PRs are excluded by default from pg-pr open and the dashboard's
Mine/Team rows; pass --include-hidden on those commands to see them anyway,
with the reason displayed. There is no auto-expiry -- a hidden PR stays
hidden until 'pg-pr pr unhide' clears it.

Hiding is DISPLAY-LAYER ONLY: it never affects ingestion. The PR continues to
be synced exactly as before -- its revisions and feedback keep being
recorded, and it is never mistaken for closed/merged because of the hide
alone.

'pg-pr pr list --json' (the machine seam pr-pool's ACL consumes) NEVER
filters on this flag -- it always includes the PR, carrying "hidden" and
"reason" fields so that consumer can make its own judgement.`,
	Args: cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		num, err := parsePR(args[0])
		if err != nil {
			return err
		}
		reason := ""
		if len(args) == 2 {
			reason = args[1]
		}
		ctx := cmd.Context()
		repo, err := resolveRepo(ctx, prWF.repo)
		if err != nil {
			return err
		}
		db, err := store.Open(store.DefaultPath())
		if err != nil {
			return fmt.Errorf("pr hide: open store: %w", err)
		}
		defer func() { _ = db.Close() }()
		if err := db.SetHidden(ctx, repo, num, true, reason); err != nil {
			return fmt.Errorf("pr hide: %w", err)
		}
		if reason != "" {
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "ok PR #%d hidden (reason: %s).\n", num, reason)
		} else {
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "ok PR #%d hidden.\n", num)
		}
		return err
	},
}

// prUnhideCmd clears the store-only USER_HIDDEN flag via store.SetHidden,
// which also ALWAYS clears the recorded reason regardless of what a caller
// might pass (SetHidden's own fork #5 ruling) -- there is no reason argument
// here because unhiding always discards it.
var prUnhideCmd = &cobra.Command{
	Use:   "unhide <pr>",
	Short: "Unhide a previously hidden PR",
	Long: `Unhide a PR: clears the store-only USER_HIDDEN flag and its recorded
reason, restoring it to every human-facing surface (pg-pr open, the
dashboard's Mine/Team rows). 'pg-pr pr list --json' was never affected by the
hide in the first place.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		num, err := parsePR(args[0])
		if err != nil {
			return err
		}
		ctx := cmd.Context()
		repo, err := resolveRepo(ctx, prWF.repo)
		if err != nil {
			return err
		}
		db, err := store.Open(store.DefaultPath())
		if err != nil {
			return fmt.Errorf("pr unhide: open store: %w", err)
		}
		defer func() { _ = db.Close() }()
		if err := db.SetHidden(ctx, repo, num, false, ""); err != nil {
			return fmt.Errorf("pr unhide: %w", err)
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "ok PR #%d unhidden.\n", num)
		return err
	},
}

// ----------------------------------------------------------------------
// pr automerge {on, off}
// ----------------------------------------------------------------------

// humanOnlyWarning is printed to stderr before any human-only mutation
// (automerge on/off, merge). It is constant text so callers (humans + tests)
// can assert on it.
const (
	humanOnlyWarning      = "WARNING: automerge is a human-only verb. Agents must not invoke this.\n"
	humanOnlyMergeWarning = "WARNING: merge is a human-only verb. Agents must not invoke this.\n"
)

var prAutomergeCmd = &cobra.Command{
	Use:   "automerge",
	Short: "Enable or disable PR automerge (HUMAN-ONLY)",
	Long: `Enable or disable automerge on a PR. This is a human-only verb;
agents are forbidden from invoking it. Each subcommand prints a prominent
warning to stderr.`,
}

var prAutomergeOnCmd = &cobra.Command{
	Use:   "on <pr>",
	Short: "Enable automerge for a PR",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		_, _ = fmt.Fprint(cmd.ErrOrStderr(), humanOnlyWarning)
		num, err := parsePR(args[0])
		if err != nil {
			return err
		}
		ctx := cmd.Context()
		repo, err := resolveRepo(ctx, prWF.repo)
		if err != nil {
			return err
		}
		if err := vcsProviderFor(repo).SetAutomerge(ctx, repo, num, true); err != nil {
			return err
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(),
			"ok Automerge enabled for PR #%d.\n", num)
		return err
	},
}

var prAutomergeOffCmd = &cobra.Command{
	Use:   "off <pr>",
	Short: "Disable automerge for a PR",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		_, _ = fmt.Fprint(cmd.ErrOrStderr(), humanOnlyWarning)
		num, err := parsePR(args[0])
		if err != nil {
			return err
		}
		ctx := cmd.Context()
		repo, err := resolveRepo(ctx, prWF.repo)
		if err != nil {
			return err
		}
		if err := vcsProviderFor(repo).SetAutomerge(ctx, repo, num, false); err != nil {
			return err
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(),
			"ok Automerge disabled for PR #%d.\n", num)
		return err
	},
}

// ----------------------------------------------------------------------
// pr merge
// ----------------------------------------------------------------------

var prMergeCmd = &cobra.Command{
	Use:   "merge <pr>",
	Short: "Merge a PR immediately (HUMAN-ONLY)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		_, _ = fmt.Fprint(cmd.ErrOrStderr(), humanOnlyMergeWarning)
		num, err := parsePR(args[0])
		if err != nil {
			return err
		}
		ctx := cmd.Context()
		repo, err := resolveRepo(ctx, prWF.repo)
		if err != nil {
			return err
		}
		if err := vcsProviderFor(repo).Merge(ctx, repo, num); err != nil {
			return err
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(),
			"ok Merged PR #%d on %s\n", num, repo)
		return err
	},
}

// ----------------------------------------------------------------------
// Cobra wiring
// ----------------------------------------------------------------------

// resetPRWriteFlags clears mutable state between cobra tests; flag values
// persist across rootCmd.Execute() calls.
func resetPRWriteFlags() {
	prWF = prWriteFlags{}
}

// addRepoFlag attaches the --repo flag to a command.
func addRepoFlag(c *cobra.Command) {
	c.Flags().StringVar(&prWF.repo, "repo", "",
		"Repository in owner/name form (defaults to auto-detected remote)")
}

// addJSONFlag attaches the --json flag to a command.
func addJSONFlag(c *cobra.Command) {
	c.Flags().BoolVar(&prWF.jsonOutput, "json", false,
		"Emit machine-readable JSON instead of human-readable output")
}

// addBodyFlags attaches body-source flags to a command.
func addBodyFlags(c *cobra.Command) {
	c.Flags().StringVar(&prWF.body, "body", "",
		"PR body (literal string)")
	c.Flags().StringVar(&prWF.bodyFile, "body-file", "",
		"Read PR body from file (use - for stdin)")
	c.Flags().BoolVar(&prWF.bodyStdin, "body-stdin", false,
		"Read PR body from stdin until EOF")
}

// addGenerateDescriptionFlags attaches the --generate-description flag
// (and its --agent-cli / --skill-path overrides) to a command.
func addGenerateDescriptionFlags(c *cobra.Command) {
	c.Flags().BoolVar(&prWF.generateDesc, "generate-description", false,
		"Generate the PR body via the pg-pr-write-pr-description SKILL "+
			"(shells out to an agent CLI; mutually exclusive with --body / --body-file / --body-stdin)")
	c.Flags().StringVar(&prWF.agentCLI, "agent-cli", "",
		"Override the agent CLI used by --generate-description "+
			"(default: $PG_PR_AGENT_CLI, else 'zr-agent' on PATH)")
	c.Flags().StringVar(&prWF.skillPath, "skill-path", "",
		"Override the SKILL.md path used by --generate-description "+
			"(default: $PG_PR_SKILL_PATH, else "+defaultSkillRelPath+" under $HOME)")
}

// addGenerateTitleFlags attaches the --generate-title flag to a command.
// Create-only (see Fork 4's resolution, pg2-4dz88.8.4): `pr update` never
// registers this flag at all -- it has no --title parameter to piggyback
// on, unlike --generate-description's body-only update path. This
// deliberately does NOT re-register --agent-cli / --skill-path: those are
// shared verbatim with --generate-description via
// addGenerateDescriptionFlags, which prCreateCmd already installs, and
// registering a flag twice on the same command panics.
func addGenerateTitleFlags(c *cobra.Command) {
	c.Flags().BoolVar(&prWF.generateTitle, "generate-title", false,
		"Generate the PR title via the pg-pr-write-pr-title SKILL "+
			"(shells out to an agent CLI; mutually exclusive with --title; "+
			"shares --agent-cli / --skill-path with --generate-description, "+
			"default: "+defaultTitleSkillRelPath+" under $HOME)")
}

func init() {
	// create
	prCreateCmd.Flags().StringVar(&prWF.title, "title", "", "PR title (required, unless --generate-title is set)")
	prCreateCmd.Flags().StringVar(&prWF.head, "head", "", "Head branch (defaults to current branch)")
	prCreateCmd.Flags().StringVar(&prWF.base, "base", "origin/main", "Base branch")
	prCreateCmd.Flags().StringVar(&prWF.reviewers, "reviewers", "", "Comma-separated list of reviewers to assign on the GitHub PR")
	prCreateCmd.Flags().StringVar(&prWF.labels, "labels", "", "Comma-separated list of labels to apply on the GitHub PR")
	prCreateCmd.Flags().BoolVar(&prWF.noDraft, "no-draft", false, "Open the PR ready-for-review instead of as a draft")
	addRepoFlag(prCreateCmd)
	addBodyFlags(prCreateCmd)
	addGenerateDescriptionFlags(prCreateCmd)
	addGenerateTitleFlags(prCreateCmd)
	addJSONFlag(prCreateCmd)

	// update
	addRepoFlag(prUpdateCmd)
	addBodyFlags(prUpdateCmd)
	addGenerateDescriptionFlags(prUpdateCmd)
	addJSONFlag(prUpdateCmd)

	// close / ready / draft / wip / hide / unhide / merge / automerge children
	for _, c := range []*cobra.Command{prCloseCmd, prReadyCmd, prDraftCmd, prWipOnCmd, prWipOffCmd, prHideCmd, prUnhideCmd, prMergeCmd, prAutomergeOnCmd, prAutomergeOffCmd} {
		addRepoFlag(c)
	}

	prWipCmd.AddCommand(prWipOnCmd, prWipOffCmd)
	prAutomergeCmd.AddCommand(prAutomergeOnCmd, prAutomergeOffCmd)
	prCmd.AddCommand(prCreateCmd, prUpdateCmd, prCloseCmd, prReadyCmd, prDraftCmd, prWipCmd, prHideCmd, prUnhideCmd, prAutomergeCmd, prMergeCmd)
}

// avoid unused-warning if splitCSV is ever inlined out.
var _ = splitCSV
