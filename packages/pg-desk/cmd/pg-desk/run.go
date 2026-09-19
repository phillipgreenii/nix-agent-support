package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/beadref"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/pipeline"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/ticketkey"
)

// runFlags holds parsed CLI flags for `pg-desk run`.
type runFlags struct {
	change  string
	verbose bool
}

var runF runFlags

// runConfigLoad and runStoreOpen are package-level vars — mirroring
// import_pg_pr_annotations.go's importPgPrDeskStoreOpen convention — so
// tests can inject a fixture config/store without touching the real
// $PG_DESK_CONFIG / $XDG_STATE_HOME/pg-desk/store.db.
var runConfigLoad = func(ctx context.Context) (*config.Config, error) { return config.Load(ctx) }

var runStoreOpen = func() (*store.Store, error) { return store.Open(store.DefaultPath()) }

// runResolveBeadPR is the "issue" case's own injectable seam — mirroring
// runConfigLoad/runStoreOpen's identical convention — so tests can inject
// a fixture bead-to-PR resolution without a real pg-connector subprocess
// on $PATH. Production wraps beadref.Resolver.ResolvePR (Phase 10, docket
// pg2-2j5ac.34): given a beads-tracker issue-type entity's id, resolve the
// (repo, entityID) of the PR it is about, by the SAME title/metadata keys
// the sync stage's forward direction uses [design: 7.2, 7.5].
var runResolveBeadPR = func(ctx context.Context, cfg *config.Config, beadID string) (repo, entityID string, err error) {
	return beadref.NewResolver(cfg).ResolvePR(ctx, beadID)
}

// runCmd implements `pg-desk run <type> <id> --change
// added|changed|removed|sweep`: the three-stage gather/interpret/store
// pipeline for one entity (internal/pipeline), for <type>=pr; and, for
// <type>=issue, a resolve-then-interpret-ONLY re-run
// (pipeline.Pipeline.RunInterpretOnly) — never a second gather, and never
// the sync stage [design: 7.2, 6.1; Binding decisions] — split two ways by
// the incoming id's own SHAPE (checked first, via ticketkey.MatchesShape
// against config.Config.TicketPatterns; never try one resolution and fall
// back to the other [Binding decisions]):
//
//   - A beads id (Phase 10, the beads backend): resolved to its linked PR
//     via internal/beadref (bead-to-PR resolution).
//   - A Jira ticket key (Phase 13, docket pg2-2j5ac.40): every PR already
//     cross-referenced to it (internal/store's xref table, populated by
//     internal/gather's own ticket-key scan) is re-interpreted — see
//     runJiraIssue below. An id matching NEITHER shape is this dispatch's
//     own well-formed error, never a silent no-op [Binding decisions].
//
// run thread (Phase 13, Slack half, docket pg2-2j5ac.40, this docket's own
// packet): the id is a Slack thread id, fetched via `pg-connector thread
// show`, scanned for PR permalinks and Jira ticket keys, cross-referenced
// into the xref table, then every PR currently xref'd to this thread is
// re-interpreted — see runThread below. Like run issue, this never invokes
// gather or sync.
var runCmd = &cobra.Command{
	Use:   "run <type> <id>",
	Short: "Run the gather/interpret/store pipeline once for one entity",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		entityType, entityID := args[0], args[1]
		switch entityType {
		case "issue", "pr", "thread":
			// implemented below
		default:
			return fmt.Errorf("run: unknown entity type %q (want pr, issue, or thread)", entityType)
		}

		cfg, err := runConfigLoad(cmd.Context())
		if err != nil {
			return fmt.Errorf("run: load config: %w", err)
		}
		st, err := runStoreOpen()
		if err != nil {
			return fmt.Errorf("run: open store: %w", err)
		}
		defer func() { _ = st.Close() }()

		p := pipeline.New(cfg, st, pipeline.WithVerbose(runF.verbose), pipeline.WithLogWriter(cmd.ErrOrStderr()))
		change := gather.ChangeKind(runF.change)

		switch entityType {
		case "issue":
			if ticketkey.MatchesShape(entityID, cfg.TicketPatterns) {
				return runJiraIssue(cmd.Context(), p, cfg, st, entityID, change)
			}
			_, prEntityID, err := runResolveBeadPR(cmd.Context(), cfg, entityID)
			if err != nil {
				return fmt.Errorf("run issue: resolve bead %s to PR: %w", entityID, err)
			}
			return p.RunInterpretOnly(cmd.Context(), entityTypePR, prEntityID, change)
		case "thread":
			return runThread(cmd.Context(), p, cfg, st, entityID, change)
		}

		return p.Run(cmd.Context(), entityType, entityID, change)
	},
}

// runJiraIssue implements run issue's Jira half (docket pg2-2j5ac.40,
// Phase 13, [design: 7.2, 8]): resolve every PR xref'd to ticketKey via the
// store's own reverse xref lookup (Store.ListXrefsByTo, to_type="issue"),
// and re-run interpret-only for each — never gather, never sync, never a
// bead write [Binding decisions: "added/changed/removed/sweep all resolve
// the SAME triggering bead's linked PR the same way ... re-run interpret
// ONLY"]. A ticket key with no currently xref'd PR is a no-op (exit 0):
// there is nothing to re-interpret yet — mirrors pipeline.Run's own "an id
// the store does not know is a no-op" convention for --change removed. If
// re-interpreting more than one linked PR, every one is attempted (a
// failure on one does not skip the rest); any failures are joined into a
// single returned error.
func runJiraIssue(ctx context.Context, p *pipeline.Pipeline, cfg *config.Config, st *store.Store, ticketKey string, change gather.ChangeKind) error {
	repo := runRepo(cfg)
	xrefs, err := st.ListXrefsByTo(repo, "issue", ticketKey)
	if err != nil {
		return fmt.Errorf("run issue %s: list xrefs: %w", ticketKey, err)
	}

	var errs []error
	for _, x := range xrefs {
		if runErr := p.RunInterpretOnly(ctx, entityTypePR, x.FromID, change); runErr != nil {
			errs = append(errs, fmt.Errorf("re-interpret %s: %w", x.FromID, runErr))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("run issue %s: %w", ticketKey, errors.Join(errs...))
	}
	return nil
}

// runRepo returns the single Phase-9 configured repository's remote, or ""
// if none is configured — mirrors internal/pipeline's own identical
// p.repo() helper and internal/gather's own identical g.repo() helper
// (each package keeps its own private copy rather than a shared one, per
// this codebase's established precedent), so runJiraIssue's own
// ListXrefsByTo call is keyed by the SAME repo value the entity/
// interpretation/xref tables use.
func runRepo(cfg *config.Config) string {
	if cfg == nil || len(cfg.Repos) == 0 {
		return ""
	}
	return cfg.Repos[0].Remote
}

// runThread implements run thread's Slack half (docket pg2-2j5ac.40, Phase
// 13, [design: 7.2, 8]): fetch the triggering thread via a single
// `pg-connector thread show` call (runThreadShow, this file's own
// injectable seam — mirroring runResolveBeadPR's identical convention),
// scan its text for PR permalinks (scanThreadPermalinks) and Jira ticket
// keys (ticketkey.Parse, reused verbatim from this docket's Jira-half
// sibling packet — never a second, parallel extractor), write/confirm an
// xref row for every match, then re-interpret every PR CURRENTLY xref'd to
// this thread (Store.ListXrefsByTo, never just the matches this one scan
// found — a previously-matched PR whose reference the thread's current
// text no longer repeats stays linked, mirroring runJiraIssue's own
// identical store-driven rule [design: 7.2]) — never gather, never sync,
// never a bead write.
//
// xref row direction [Binding decisions > "xref row DIRECTION"]: every row
// this function writes is (repo, from_type="pr", from_id=<pr-entity-id>,
// to_type="thread", to_id=threadID) — the SAME "pr" anchors the "from" side
// convention runJiraIssue's own sibling packet already landed for the Jira
// half (from_type="pr", to_type="issue"). A permalink match resolves
// DIRECTLY to a PR entity id (the URL itself names one). A ticket-key match
// resolves INDIRECTLY: the thread's text names a Jira ticket key, not a PR,
// so it is resolved via whichever PR(s) internal/gather's own Jira scan
// (gatherJiraXrefs) has ALREADY cross-referenced to that same ticket key
// (Store.ListXrefsByTo(repo, "issue", key) — the same reverse lookup
// runJiraIssue itself uses). A ticket key with no linked PR yet writes
// nothing for that key — a no-op, mirroring runJiraIssue's own "an id the
// store does not know is a no-op" convention, never a speculative or
// partial xref.
//
// A thread `pg-connector` cannot fetch at all (a hard error, or a
// not_found answer) fails this call outright — unlike gather's own `pr
// show` re-read, there is no `--change removed` special case here: this
// command has no stored "is this thread already known" state of its own to
// consult, so every change kind treats an unfetchable thread the same way
// [freedom boundary].
func runThread(ctx context.Context, p *pipeline.Pipeline, cfg *config.Config, st *store.Store, threadID string, change gather.ChangeKind) error {
	repo := runRepo(cfg)

	fields, notFound, err := runThreadShow(ctx, threadID)
	if notFound {
		return fmt.Errorf("run thread %s: fetch thread: pg-connector reports not_found", threadID)
	}
	if err != nil {
		return fmt.Errorf("run thread %s: fetch thread: %w", threadID, err)
	}

	now := fields.AsOf

	for _, prEntityID := range scanThreadPermalinks(fields.Text, repo) {
		if err := st.UpsertXref(store.Xref{
			Repo: repo, FromType: entityTypePR, FromID: prEntityID,
			ToType: "thread", ToID: threadID,
			Evidence:      "permalink",
			FirstSeen:     now,
			LastConfirmed: now,
		}); err != nil {
			return fmt.Errorf("run thread %s: upsert permalink xref for %s: %w", threadID, prEntityID, err)
		}
	}

	for _, key := range ticketkey.Parse(fields.Text, "", "", cfg.TicketPatterns) {
		linkedToKey, err := st.ListXrefsByTo(repo, "issue", key)
		if err != nil {
			return fmt.Errorf("run thread %s: list xrefs for ticket %s: %w", threadID, key, err)
		}
		for _, x := range linkedToKey {
			if x.FromType != entityTypePR {
				continue
			}
			if err := st.UpsertXref(store.Xref{
				Repo: repo, FromType: entityTypePR, FromID: x.FromID,
				ToType: "thread", ToID: threadID,
				Evidence:      "ticket-key",
				FirstSeen:     now,
				LastConfirmed: now,
			}); err != nil {
				return fmt.Errorf("run thread %s: upsert ticket-key xref for %s: %w", threadID, x.FromID, err)
			}
		}
	}

	linked, err := st.ListXrefsByTo(repo, "thread", threadID)
	if err != nil {
		return fmt.Errorf("run thread %s: list linked PRs: %w", threadID, err)
	}

	var errs []error
	for _, x := range linked {
		if x.FromType != entityTypePR {
			continue
		}
		if runErr := p.RunInterpretOnly(ctx, entityTypePR, x.FromID, change); runErr != nil {
			errs = append(errs, fmt.Errorf("re-interpret %s: %w", x.FromID, runErr))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("run thread %s: %w", threadID, errors.Join(errs...))
	}
	return nil
}

// threadShowResult is the minimal subset of `pg-connector thread show`'s
// result payload this file decodes for itself (Text for the permalink/
// ticket-key scan, AsOf for the xref rows' first_seen/last_confirmed) —
// mirrors internal/gather's own prShowFields convention: this package
// imports no pkg/schema type, staying decoupled from pg-connector's
// internal Go API and talking to it only over the CLI/wire surface
// (schema.Thread's own field set — pkg/schema/thread.go — is the source of
// truth for what "text"/"as_of" mean on the wire).
type threadShowResult struct {
	Text string `json:"text"`
	AsOf string `json:"as_of"`
}

// runThreadWireEnvelope/runThreadWireError mirror internal/gather's own
// wireEnvelope/wireError types (that package's own doc comment explains why
// this hand-decodes rather than importing pkg/scriptout) — a private copy
// in this file rather than a shared type, per this repo's own per-package
// "keep its own copy of this kind of guard/helper" precedent (see e.g.
// composition_test.go's moduleRoot doc comment).
type runThreadWireEnvelope struct {
	Result json.RawMessage        `json:"result"`
	Error  *runThreadWireErrorMsg `json:"error"`
}

type runThreadWireErrorMsg struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// runThreadShow is run thread's own injectable seam (mirroring
// runResolveBeadPR's identical convention) for fetching a thread's current
// state. Production execs `pg-connector thread show <id>` as a subprocess
// — the pg-connector CLI dispatch, never a Go import of
// pkg/provider/thread (Contract: "run thread calls Show via the
// pg-connector CLI dispatch"), and the only literal binary this call
// names, satisfying the composition rule (D10,
// cmd/pg-desk/composition_test.go) — classified per pg-connector's own
// 0/4/1 targeted exit-code scheme (mirrors internal/gather's own
// targetedCall). Tests override this var directly with a fake, so no real
// pg-connector subprocess is ever needed on a test's $PATH.
var runThreadShow = func(ctx context.Context, threadID string) (result threadShowResult, notFound bool, err error) {
	cmd := exec.CommandContext(ctx, "pg-connector", "thread", "show", threadID)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	exitCode := 0
	if runErr := cmd.Run(); runErr != nil {
		var exitErr *exec.ExitError
		if !errors.As(runErr, &exitErr) {
			return threadShowResult{}, false, fmt.Errorf("exec pg-connector thread show %s: %w", threadID, runErr)
		}
		exitCode = exitErr.ExitCode()
	}

	switch exitCode {
	case 0:
		var env runThreadWireEnvelope
		if decErr := json.Unmarshal(stdout.Bytes(), &env); decErr != nil {
			return threadShowResult{}, false, fmt.Errorf("decode pg-connector thread show %s stdout: %w", threadID, decErr)
		}
		if decErr := json.Unmarshal(env.Result, &result); decErr != nil {
			return threadShowResult{}, false, fmt.Errorf("decode pg-connector thread show %s result: %w", threadID, decErr)
		}
		return result, false, nil
	case 4:
		return threadShowResult{}, true, nil
	default:
		msg := strings.TrimSpace(stdout.String())
		var env runThreadWireEnvelope
		if decErr := json.Unmarshal(stdout.Bytes(), &env); decErr == nil && env.Error != nil {
			msg = env.Error.Code + ": " + env.Error.Message
		}
		return threadShowResult{}, false, fmt.Errorf("pg-connector thread show %s: exit %d: %s", threadID, exitCode, msg)
	}
}

// threadPermalinkRE finds a GitHub PR URL anywhere in free-form text — this
// packet's own new permalink scanner [design 7.2, 8: "cross-references
// threads to PRs and issues by permalinks and ticket keys"]. Unlike
// desk.go's own prURLNumberRE (anchored to the END of a whole PR-reference
// string, for parsing a `<pr>` command-line argument), a thread's text is
// free-form prose that may embed a permalink anywhere, with trailing
// punctuation or more sentence after it — so this pattern is deliberately
// NOT end-anchored, and stops at the number rather than requiring a
// trailing slash or end-of-string.
var threadPermalinkRE = regexp.MustCompile(`/pull/(\d+)\b`)

// scanThreadPermalinks returns the deduplicated set of PR entity ids
// ("<repo>#<n>", the SAME entityID form used everywhere else in this
// codebase — desk.go's resolvePRRef, internal/gather's matchWorkBeads,
// internal/sync's prNumberFromEntityID) named by a GitHub PR URL anywhere
// in text. Phase 9 supports exactly one configured repository
// (docs/behavior/pg-desk/README.md's "Scope"), so — mirroring desk.go's own
// parsePRNumber/resolvePRRef precedent — whatever owner/repo the URL itself
// names is not otherwise consulted; every match resolves against repo
// (the caller's own single configured repository). Returns nil (never an
// error) when text contains no PR-URL-shaped substring.
func scanThreadPermalinks(text, repo string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, m := range threadPermalinkRE.FindAllStringSubmatch(text, -1) {
		id := repo + "#" + m[1]
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

func init() {
	// An absent --change means sweep [design 7.2].
	runCmd.Flags().StringVar(&runF.change, "change", "sweep", "Change kind: added|changed|removed|sweep")
	runCmd.Flags().BoolVar(&runF.verbose, "verbose", false, "Print the three-stage timeline")
	rootCmd.AddCommand(runCmd)
}
