// landerstale.go: the "lander-stale" verb. A stopped pg-git-lander operation
// whose owner died stays stopped forever and nothing notices; this verb asks
// `pg-git-lander status --json --stale-after <threshold>` in each canonical
// clone it is given and files ONE escalated bead per stale operation, carrying
// the operation's own resume commands (its `next` list).
//
// It reuses this probe's existing conventions rather than adding new ones:
// the pg-connector wrappers in connector.go, the "escalated" label, the
// pg_router_escalation_fingerprint metadata key and matchExisting/renderBody.
// Dedup is by fingerprint "lander-stale:<op_id>" against the every-non-closed
// escalated query (defaultDedupQuery): a stale operation that already has a
// bead gets its metadata refreshed (last-seen time and age) and nothing else;
// an operation that is resolved no longer appears in `.stale`, so its bead is
// simply no longer refreshed (this verb never closes beads, like `run`).
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/phillipgreenii/x/cmdrun"
	"github.com/spf13/cobra"
)

const (
	kindLanderStale findingKind = "lander-stale"

	landerStaleFingerprintPrefix = "lander-stale:"
	metaLanderSeenAt             = "pg_router_lander_stale_seen_at"
	metaLanderAgeS               = "pg_router_lander_stale_age_s"

	defaultLanderStaleAfter = 2 * time.Hour
)

// landerStaleOp is one entry of `pg-git-lander status --json`'s `stale` array
// (the fields this verb uses).
type landerStaleOp struct {
	OpID      string   `json:"op_id"`
	Operation string   `json:"operation"`
	Branch    string   `json:"branch"`
	Target    string   `json:"target"`
	Reason    string   `json:"reason"`
	Stage     string   `json:"stage"`
	Since     string   `json:"since"`
	Worktree  string   `json:"worktree"`
	Log       string   `json:"log"`
	Next      []string `json:"next"`
	AgeS      int64    `json:"age_s"`
}

// parseLanderStale extracts `.stale` from a status --json document. A
// document with no `stale` key yields no operations (the field is present
// only when --stale-after was passed, and an empty list is the clean case).
func parseLanderStale(raw []byte) ([]landerStaleOp, error) {
	var rep struct {
		Stale []landerStaleOp `json:"stale"`
	}
	if err := json.Unmarshal(raw, &rep); err != nil {
		return nil, fmt.Errorf("parse pg-git-lander status json: %w", err)
	}
	return rep.Stale, nil
}

// landerStaleFinding turns one stale operation in repo into a finding.
func landerStaleFinding(repo string, op landerStaleOp) finding {
	age := (time.Duration(op.AgeS) * time.Second).Round(time.Minute)
	var ev strings.Builder
	fmt.Fprintf(&ev, "repo=%s\nop_id=%s\noperation=%s\nbranch=%s\ntarget=%s\nreason=%s\nstage=%s\nstopped_since=%s\nage=%s\nworktree=%s\nlog=%s\nresume commands (run one in the repo):\n",
		repo, op.OpID, op.Operation, op.Branch, op.Target, op.Reason, op.Stage, op.Since, age, op.Worktree, op.Log)
	for _, n := range op.Next {
		fmt.Fprintf(&ev, "  %s\n", n)
	}
	return finding{
		Kind:        kindLanderStale,
		Fingerprint: landerStaleFingerprintPrefix + op.OpID,
		Summary:     fmt.Sprintf("pg-git-lander %s of %s stopped %s ago (owner likely dead)", op.Operation, op.Branch, age),
		Evidence:    strings.TrimRight(ev.String(), "\n"),
		State:       "stopped",
	}
}

// landerStaleOptions is every lander-stale flag value.
type landerStaleOptions struct {
	repos              []string
	staleAfter         time.Duration
	landerPath         string
	statusTimeout      time.Duration
	pgConnectorTimeout time.Duration
	dedupQuery         string
	connectorBackend   string
}

// landerStaleDeps is the side-effect surface; tests swap pieces. The process
// boundary itself (pg-git-lander and pg-connector) is the runner seam.
type landerStaleDeps struct {
	now            func() time.Time
	fetchStale     func(ctx context.Context, opts landerStaleOptions, repo string) ([]landerStaleOp, error)
	listEscalated  func(ctx context.Context, query string, warn func(string)) ([]connectorIssue, error)
	createIssue    func(ctx context.Context, title string, labels []string, metadata map[string]string, description string, warn func(string)) (connectorIssue, error)
	updateMetadata func(ctx context.Context, id string, metadata map[string]string, warn func(string)) error
}

func defaultLanderStaleDeps(backend string) landerStaleDeps {
	rd := defaultRunDeps(backend)
	return landerStaleDeps{
		now:            time.Now,
		fetchStale:     fetchLanderStale,
		listEscalated:  rd.listEscalated,
		createIssue:    rd.createIssue,
		updateMetadata: rd.updateMetadata,
	}
}

// fetchLanderStale runs `pg-git-lander status --json --stale-after <d>` in
// repo under its own explicit timeout.
func fetchLanderStale(ctx context.Context, opts landerStaleOptions, repo string) ([]landerStaleOp, error) {
	sctx, cancel := context.WithTimeout(ctx, opts.statusTimeout)
	defer cancel()
	//cmdrun:unbounded sctx carries the status timeout
	r := runnerOrExec()(sctx, cmdrun.Call{
		Name: opts.landerPath,
		Args: []string{"status", "--json", "--stale-after", opts.staleAfter.String()},
		Dir:  repo,
	})
	switch {
	case r.StartErr != nil:
		return nil, fmt.Errorf("pg-git-lander status: %w", r.StartErr)
	case r.Signal != "" || r.CtxErr != nil:
		return nil, fmt.Errorf("pg-git-lander status: killed (timeout %s)", opts.statusTimeout)
	case r.Code != 0:
		return nil, fmt.Errorf("pg-git-lander status: exit status %d: %s", r.Code, strings.TrimSpace(string(r.Stderr)))
	}
	return parseLanderStale(r.Stdout)
}

func newLanderStaleCmd() *cobra.Command {
	opts := landerStaleOptions{
		staleAfter:         defaultLanderStaleAfter,
		landerPath:         "pg-git-lander",
		statusTimeout:      30 * time.Second,
		pgConnectorTimeout: 30 * time.Second,
		dedupQuery:         defaultDedupQuery,
		connectorBackend:   defaultPgConnectorBackend,
	}
	cmd := &cobra.Command{
		Use:   "lander-stale",
		Short: "File/refresh one escalated bead per pg-git-lander operation stopped longer than --stale-after",
		Args:  cobra.NoArgs,
	}
	cmd.Flags().StringSliceVar(&opts.repos, "repo", nil, "canonical clone to check (repeatable, required)")
	cmd.Flags().DurationVar(&opts.staleAfter, "stale-after", opts.staleAfter, "a stopped operation older than this is stale")
	cmd.Flags().StringVar(&opts.landerPath, "pg-git-lander-path", opts.landerPath, "pg-git-lander binary")
	cmd.Flags().DurationVar(&opts.statusTimeout, "status-timeout", opts.statusTimeout, "explicit timeout for each `pg-git-lander status` call")
	cmd.Flags().DurationVar(&opts.pgConnectorTimeout, "pg-connector-timeout", opts.pgConnectorTimeout, "explicit timeout for each pg-connector call")
	cmd.Flags().StringVar(&opts.dedupQuery, "dedup-query", opts.dedupQuery, "named pg-connector query listing every non-closed escalated bead")
	cmd.Flags().StringVar(&opts.connectorBackend, "connector-backend", opts.connectorBackend, "pg-connector backend instance name passed as --backend")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if len(opts.repos) == 0 {
			return usageErrorf("lander-stale: at least one --repo is required")
		}
		if opts.staleAfter <= 0 {
			return usageErrorf("lander-stale: --stale-after must be positive")
		}
		if opts.connectorBackend == "" {
			return usageErrorf("lander-stale: --connector-backend must not be empty")
		}
		return runLanderStale(cmd, opts, defaultLanderStaleDeps(opts.connectorBackend))
	}
	return cmd
}

// runLanderStale checks every repo, then files or refreshes one bead per
// stale operation. Exit: 0 clean; 3 when every repo's status call failed;
// 5 when some status call or bead write failed.
func runLanderStale(cmd *cobra.Command, opts landerStaleOptions, deps landerStaleDeps) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	warn := func(msg string) { fmt.Fprintln(cmd.ErrOrStderr(), "pg-router-probe: "+msg) }

	type staleEntry struct {
		repo string
		op   landerStaleOp
	}
	var stale []staleEntry
	var degraded []string
	okRepos := 0
	for _, repo := range opts.repos {
		ops, err := deps.fetchStale(ctx, opts, repo)
		if err != nil {
			degraded = append(degraded, fmt.Sprintf("%s: %v", repo, err))
			continue
		}
		okRepos++
		for _, op := range ops {
			stale = append(stale, staleEntry{repo, op})
		}
	}
	if okRepos == 0 {
		return totalFailureErrorf("lander-stale: every repo failed (%s)", strings.Join(degraded, "; "))
	}

	if len(stale) > 0 {
		existing, err := deps.listEscalated(ctx, opts.dedupQuery, warn)
		if err != nil {
			// Without the dedup list filing could duplicate; write nothing.
			degraded = append(degraded, fmt.Sprintf("dedup-query: %v", err))
			return partialErrorf("lander-stale: partial (%s)", strings.Join(degraded, "; "))
		}
		sort.SliceStable(stale, func(i, j int) bool { return stale[i].op.OpID < stale[j].op.OpID })
		now := deps.now().UTC()
		for _, s := range stale {
			f := landerStaleFinding(s.repo, s.op)
			meta := map[string]string{
				metaFingerprint:  f.Fingerprint,
				metaState:        f.State,
				metaLanderSeenAt: now.Format(time.RFC3339),
				metaLanderAgeS:   strconv.FormatInt(s.op.AgeS, 10),
			}
			if match := matchExisting(f, existing); match != nil {
				if err := deps.updateMetadata(ctx, match.ID, meta, warn); err != nil {
					degraded = append(degraded, fmt.Sprintf("refresh %s: %v", match.ID, err))
				}
				continue
			}
			created, err := deps.createIssue(ctx, escalationTitle(f), escalationLabels(f), meta, renderBody(f, now, "", ""), warn)
			if err != nil {
				degraded = append(degraded, fmt.Sprintf("create %s: %v", f.Fingerprint, err))
				continue
			}
			// Guard against two entries with the same op id in one run.
			existing = append(existing, connectorIssue{ID: created.ID, Metadata: meta})
		}
	}
	if len(degraded) > 0 {
		return partialErrorf("lander-stale: partial (%s)", strings.Join(degraded, "; "))
	}
	return nil
}
