// ci.go: the "pg-connector ci" CLI verb group, built by the "generic ci
// entity/capability" packet on top of the Tier-1 core's registry/dispatcher
// and outcome-reporting helper. pg-connector remains the only user-facing
// CLI surface — ci is one of its verb groups, never a separate binary
// (interfaces.md's INTF-CLI).
//
// connector.ci is list-valued (multiple simultaneously-registered CI
// backends, matching pr/issue) (INV-REG-1). "ci list" is therefore a
// FAN-OUT op — it queries every registered ci backend and concatenates
// their runs (the design's explicit "runs concatenates" merge strategy for
// the CI fan-out (INV-OUT-1)) — and uses the fan-out exit-code scheme
// (0/2/3) via outcome.go's FanOutOutcome.ExitCode. "ci logs" and
// "ci rerun-failed" are TARGETED, id-keyed ops dispatched via
// dispatch.go's DispatchTargeted, which implements this docket's
// multi-instance resolution policy across every backend registered under
// connector.ci (try each in registration order, stopping at the first
// non-not_found answer) — mirroring pr.go's own targeted-op dispatch —
// and use the targeted exit-code scheme (0/4/1) via outcome.go's
// TargetedExitCode (INV-EXIT-1). This file calls the dispatcher/fan-out
// helpers and hands their raw per-call result/error to outcome.go; it
// never decides the exit code itself.
//
// Every one of these three verbs carries its own --backend flag (bead
// pg2-2j5ac.28.1, design's "id-less op rule": "ci.go/scm.go need the
// flag even though neither gets [the new named-query list] op"). "ci
// list" itself is NOT that new op — it stays the pre-existing, PR-keyed
// list_runs fan-out (this packet's own Contract explicitly keeps it
// unchanged) — --backend on it simply narrows the fan-out to one backend,
// reusing list.go's resolveListBackends (its query_not_recognized-specific
// helpers do not apply here, since list_runs carries no query name at
// all). On "ci logs"/"ci rerun-failed" --backend pins DispatchTargeted
// straight to that one backend, skipping the try-each policy, exactly
// like pr.go's/issue.go's own targeted verbs.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
	"github.com/spf13/cobra"
)

// ciListOutcome is "ci list"'s wire response: every registered ci backend's
// runs concatenated into Runs, with each backend's own health as one row in
// Sources — never collapsed, matching the sources[] convention every
// fan-out uses (INV-OUT-1).
type ciListOutcome struct {
	Runs    []schema.CIRun `json:"runs"`
	Sources []SourceResult `json:"sources"`
}

// exitCode delegates to the shared fan-out scheme (0/2/3) — the exact same
// classification logic auth.go/config_validate.go's fan-outs use, applied
// to ciListOutcome's own Sources rows.
func (o ciListOutcome) exitCode() int {
	return FanOutOutcome{Sources: o.Sources}.ExitCode()
}

// fanOutCIList queries "list_runs" against every backend in backends and
// concatenates their runs, building one sources[] row per backend queried.
// A backend not implementing list_runs (recognized generically via the
// wire-level unknown_op sentinel, matching auth.go's own convention) is
// reported as disabled with reason "not applicable" rather than a
// forced/meaningless answer. reg is threaded through (bead pg2-2j5ac.28.1)
// so each backend's own registered config block (registry.go's
// BackendConfig) travels on the outgoing request the same way every other
// Tier-1 verb's dispatch path already attaches it.
//
// Phase 14 (bead pg2-2j5ac.42.3) restores the stale-fallback behavior
// pg-connector-ci-github-actions lost in phase 7 (its own backend-local
// run-list cache was removed under statelessness, D3) — now generically, in
// the umbrella, using this docket's own entity cache engine (cache.go).
// This is the one cache entry in this whole docket whose Content is a
// LIST rather than one entity: "ci list" is a fan-out keyed by PR id, not
// per-run id (a per-run key would let some of one PR's runs be served
// stale while others are silently dropped — this fan-out's own
// stale-fallback checkpoint is phrased in terms of a whole "backend
// answering unavailable"), so each cache entry is keyed by prID and its
// Content is the JSON-marshaled []schema.CIRun a live list_runs call
// returned for that PR. On a backend answering scriptout.ErrUnavailable,
// a within-max-age cache hit for prID is decoded straight back into
// []schema.CIRun (schema.CIRun is a typed struct, so this is a plain
// json.Unmarshal plus a field-level Stale/AsOf overwrite, not the generic
// decode-into-map/re-marshal technique cache_dispatch.go's markStale uses
// for opaque json.RawMessage bodies) and appended to out.Runs, with that
// backend's own sources[] row reported degraded (never succeeded — this
// docket's own Binding decision, matching fanOutPRList/fanOutIssueList's
// own fallback rows) and a reason noting the fallback. A cache miss,
// opted-out type/backend, or cacheEnabled itself erroring closed (it
// should not, per cacheEnabled's own fail-open contract, but this falls
// through defensively either way) leaves today's unmodified
// SourceDegraded/no-runs behavior in place. A live success instead writes
// that backend's returned run list into its own cache keyed by prID, so
// it stays current for the next unavailable window.
func fanOutCIList(ctx context.Context, reg *Registry, backends []string, prID string) ciListOutcome {
	// Runs and Sources both start as non-nil empty slices so a
	// zero-backend (misconfigured host) result, or a backend that
	// answers with zero runs, still marshals runs[]/sources[] as []
	// rather than null [bug A15].
	out := ciListOutcome{
		Runs:    make([]schema.CIRun, 0),
		Sources: make([]SourceResult, 0, len(backends)),
	}
	for _, b := range backends {
		config, err := reg.BackendConfig(b)
		if err != nil {
			out.Sources = append(out.Sources, SourceResult{Source: b, Status: SourceDegraded, Reason: err.Error()})
			continue
		}
		resp, err := scriptout.Invoke(ctx, b, "list_runs", map[string]string{"pr_id": prID}, config)
		if err != nil {
			if errors.Is(err, scriptout.ErrUnknownOp) {
				out.Sources = append(out.Sources, SourceResult{Source: b, Status: SourceDisabled, Reason: "not applicable"})
				continue
			}
			if errors.Is(err, scriptout.ErrUnavailable) {
				if runs, ok := ciCacheFallback(ctx, reg, b, prID); ok {
					out.Runs = append(out.Runs, runs...)
					out.Sources = append(out.Sources, SourceResult{Source: b, Status: SourceDegraded, Count: len(runs), Reason: cacheFallbackReason})
					continue
				}
			}
			out.Sources = append(out.Sources, SourceResult{Source: b, Status: SourceDegraded, Reason: err.Error()})
			continue
		}
		var runs []schema.CIRun
		if err := scriptout.Decode(resp.Result, &runs); err != nil {
			out.Sources = append(out.Sources, SourceResult{Source: b, Status: SourceDegraded, Reason: err.Error()})
			continue
		}
		out.Runs = append(out.Runs, runs...)
		out.Sources = append(out.Sources, SourceResult{Source: b, Status: SourceSucceeded, Count: len(runs)})
		putCIListCache(ctx, reg, b, prID, runs)
	}
	return out
}

// ciCacheFallback checks whether caching applies to ("ci", backend) and, on
// a within-max-age, non-tombstoned cache hit for prID, returns that cached
// run list with every run's Stale set to true and AsOf overwritten to the
// cached as-of time — ok=true. Any other outcome (opted out, cacheEnabled
// erroring, a cache-load error, a decode failure, or a plain miss) is
// (nil, false), and the caller falls through to reporting the real error
// unchanged, exactly like cache_dispatch.go's tryCacheFallback/
// cacheFallbackEntities own (nil, false) convention.
func ciCacheFallback(ctx context.Context, reg *Registry, backend, prID string) ([]schema.CIRun, bool) {
	enabled, err := cacheEnabled(ctx, reg, "ci", backend)
	if err != nil || !enabled {
		return nil, false
	}
	if err := ensureCacheDirExists(); err != nil {
		return nil, false
	}
	key := CacheKey{Type: "ci", Backend: backend}
	c, err := loadCache(key)
	if err != nil {
		return nil, false
	}
	now := time.Now()
	content, asOf, ok := c.Get(prID, resolveCacheMaxAge(reg), now)
	if !ok {
		return nil, false
	}
	var runs []schema.CIRun
	if err := json.Unmarshal(content, &runs); err != nil {
		return nil, false
	}
	asOfStr := asOf.UTC().Format(time.RFC3339)
	for i := range runs {
		runs[i].Stale = true
		runs[i].AsOf = asOfStr
	}
	c.MarkAccessed(prID, now)
	if saveErr := saveCache(key, c); saveErr != nil {
		// Best-effort persistence of the MarkAccessed bump, mirroring
		// tryCacheFallback's own tolerance — the runs already decoded
		// above are served regardless.
		_ = saveErr
	}
	return runs, true
}

// putCIListCache writes a live "ci list" success's returned run list into
// ("ci", backend)'s cache keyed by prID: the whole slice is marshaled as
// ONE CacheEntry's Content (this is the one place in this docket a single
// cache entry's content is a list rather than one entity — cache.go's
// Cache.Entries is keyed by an arbitrary string id, and prID is exactly
// that). Evicted with a noConsumersTracked consumersPassed
// (cache_dispatch.go) since ci has no ledger/consumer-cursor concept at
// all — a run-list-unavailable answer is never a "run removed," so ci's
// own fan-out never calls Remove and only the size-cap/no-tombstone-ever
// eviction path ever applies here. Errors are swallowed throughout: a
// cache-write problem MUST NOT turn an already-succeeded live read into a
// reported failure, matching this docket's existing best-effort
// Put/Evict/saveCache tolerance elsewhere (cache_dispatch.go's
// putEntityCache).
func putCIListCache(ctx context.Context, reg *Registry, backend, prID string, runs []schema.CIRun) {
	enabled, err := cacheEnabled(ctx, reg, "ci", backend)
	if err != nil || !enabled {
		return
	}
	content, err := json.Marshal(runs)
	if err != nil {
		return
	}
	if err := ensureCacheDirExists(); err != nil {
		return
	}
	key := CacheKey{Type: "ci", Backend: backend}
	c, err := loadCache(key)
	if err != nil {
		return
	}
	now := time.Now()
	c.Put(prID, content, ciListAsOf(runs), now)
	c.Evict(resolveCacheSizeCap(reg), cacheTombstoneRetention, noConsumersTracked, now)
	_ = saveCache(key, c)
}

// ciListAsOf derives the single as-of time stored for one Put call's
// CacheEntry from the live-fetched runs it is caching, mirroring
// putLiveEntity's own convention of trusting the ENTITY's own reported
// as_of field rather than the umbrella's write-time clock (this docket's
// design: a cache entry's as-of time is the moment the cached data was
// itself known-fresh, not the moment it happened to be persisted). Returns
// the first run's own parseable AsOf; falls back to time.Now() when runs
// is empty or every run's AsOf is empty/unparseable (a backend not
// populating AsOf on a live answer, which pkg/schema/ci.go's own doc
// comment says MUST pair with Stale true — this fallback keeps caching a
// degenerate list from crashing rather than claiming a false precision).
func ciListAsOf(runs []schema.CIRun) time.Time {
	for _, r := range runs {
		if r.AsOf == "" {
			continue
		}
		if t, err := time.Parse(time.RFC3339, r.AsOf); err == nil {
			return t
		}
	}
	return time.Now()
}

func newCiCmd() *cobra.Command {
	ciCmd := &cobra.Command{
		Use:   "ci",
		Short: "CI capability commands",
	}
	ciCmd.AddCommand(newCiListCmd())
	ciCmd.AddCommand(newCiLogsCmd())
	ciCmd.AddCommand(newCiRerunFailedCmd())
	return ciCmd
}

func newCiListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list <pr-id>",
		Short: "List CI runs for a PR, fanned out across every registered ci backend",
		Args:  cobra.ExactArgs(1),
	}
	backendFlag := addBackendFlag(cmd, "pin the fan-out to exactly this backend instead of every registered ci backend")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		reg, err := LoadRegistry()
		if err != nil {
			return err
		}
		backends, err := resolveListBackends(reg, "ci", *backendFlag)
		if err != nil {
			return err
		}
		outcome := fanOutCIList(cmd.Context(), reg, backends, args[0])
		return writeFanOutResult(cmd, outcome, outcome.exitCode(), func() string {
			return humanizeCiList(outcome)
		})
	}
	return cmd
}

func newCiLogsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "logs <run-id>",
		Short: "Get the raw logs for a CI run (targeted, id-keyed; multi-instance resolution across every registered ci backend)",
		Args:  cobra.ExactArgs(1),
	}
	backendFlag := addBackendFlag(cmd, "pin to exactly this backend, skipping the multi-instance try-each resolution policy")
	// --repo: since CISchemaVersion's 2 -> 3 bump (bead pg2-2j5ac.28.4,
	// reversing the 2026-09-06 operator ruling on pg2-f327j),
	// ci.Provider.GetLogs takes repo as a caller-supplied argument rather
	// than resolving it internally — the caller here is this CLI verb, so
	// it must supply repo itself. This freedom-boundary choice (a flag,
	// rather than requiring a prior "ci list" call to learn a run's
	// CIRun.Repo) keeps "ci logs" a single, self-contained call; a caller
	// that already has a CIRun from "ci list" passes its own Repo value
	// through this flag.
	var repo string
	cmd.Flags().StringVar(&repo, "repo", "", "the run's owning repo (owner/name), supplied by the caller — e.g. from a prior \"ci list\" result's CIRun.Repo")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		reg, err := LoadRegistry()
		if err != nil {
			return reportCiTargetedOutcome(cmd, nil, err, humanizeCiLogs)
		}
		resp, dispatchErr := DispatchTargeted(cmd.Context(), reg, "ci", "get_logs", map[string]string{"run_id": args[0], "repo": repo}, *backendFlag)
		return reportCiTargetedOutcome(cmd, resp, dispatchErr, humanizeCiLogs)
	}
	return cmd
}

func newCiRerunFailedCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rerun-failed <pr-id>",
		Short: "Rerun a PR's failed CI runs (targeted, id-keyed; multi-instance resolution across every registered ci backend)",
		Args:  cobra.ExactArgs(1),
	}
	backendFlag := addBackendFlag(cmd, "pin to exactly this backend, skipping the multi-instance try-each resolution policy")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		humanize := func(json.RawMessage) (string, error) {
			return fmt.Sprintf("CI rerun triggered for PR %s", args[0]), nil
		}
		reg, err := LoadRegistry()
		if err != nil {
			return reportCiTargetedOutcome(cmd, nil, err, humanize)
		}
		resp, dispatchErr := DispatchTargeted(cmd.Context(), reg, "ci", "rerun_failed", map[string]string{"pr_id": args[0]}, *backendFlag)
		return reportCiTargetedOutcome(cmd, resp, dispatchErr, humanize)
	}
	return cmd
}

// reportCiTargetedOutcome writes resp's outcome to stdout — in the
// default OutputJSON mode, its wire envelope ("result" on success, or
// "error" per the taxonomy on failure) verbatim, matching the wire
// protocol's own "only stdout JSON is the contract" convention; in
// OutputHuman mode, humanize's formatted rendering instead
// [bead pg2-ox1k6] — see output.go's writeTargetedResult, which this
// delegates to. It translates err into pg-connector's own targeted-op
// exit code via outcome.go's TargetedExitCode, never deciding the exit
// code itself (INV-EXIT-1). A nil resp is a Tier-1 CLI-level failure
// before any well-formed wire response was produced (e.g. no backend
// registered, or an ambiguous multi-backend registration) — rather than
// returning a plain error, writeTargetedResult now builds a synthetic
// error envelope for it via scriptout.ErrorResponse and reports it
// through stdout exactly like a backend-reported failure
// [bug pg2-njx27].
func reportCiTargetedOutcome(cmd *cobra.Command, resp *scriptout.Response, err error, humanize humanizeResult) error {
	return writeTargetedResult(cmd, resp, err, humanize)
}

// humanizeCiList formats a "ci list" fan-out outcome (its concatenated
// Runs plus its per-backend Sources rows) for human display — the fan-out
// outcome envelope's own compact rendering this bead names explicitly.
func humanizeCiList(o ciListOutcome) string {
	var b strings.Builder
	if len(o.Runs) == 0 {
		b.WriteString("ci runs: (none)\n")
	} else {
		fmt.Fprintf(&b, "ci runs (%d):\n", len(o.Runs))
		for _, r := range o.Runs {
			fmt.Fprintf(&b, "  [%s] %s: %s/%s (%s) sha=%s pr=%s\n", r.ID, r.Name, r.Status, r.Conclusion, r.Provider, r.HeadSHA, r.PRID)
		}
	}
	b.WriteString("sources:\n")
	b.WriteString(formatSourcesTable(o.Sources))
	return strings.TrimRight(b.String(), "\n")
}

// humanizeCiLogs formats a "ci logs" result: GetLogs' raw log bytes
// (wire-encoded as a base64 JSON string, decoded here via the same
// scriptout.Decode every other targeted op uses) printed as plain text —
// the logs are already human-readable content, so "human" rendering here
// is exactly the decoded bytes with no further reformatting.
func humanizeCiLogs(raw json.RawMessage) (string, error) {
	var logs []byte
	if err := scriptout.Decode(raw, &logs); err != nil {
		return "", err
	}
	return string(logs), nil
}
