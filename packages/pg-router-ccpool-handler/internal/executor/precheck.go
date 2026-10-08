package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/beads"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/item"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/query"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/roles"
)

// The three precheck skip reasons (INV-CCH-22, bead pg2-5x29j). Each is the
// busy-decline reason the handler replies with, so pg-router core's
// pg_router_failures{class=declined,reason=...} counts each case separately.
// They share the "skipped-" prefix so one alert matcher can exclude them as
// routine, non-failing declines.
const (
	SkipBeadClosed    = "skipped-bead-closed"
	SkipPRMerged      = "skipped-pr-merged"
	SkipPendingReview = "skipped-pending-review"
)

// The "ready" precheck's skip reasons (INV-CCH-22, bead pg2-nk6th.3). They share
// the "skipped-" prefix with the review reasons so the failure-rate alert, which
// excludes only `skipped-.+`, treats every one as a routine decline.
const (
	SkipBeadNotOpen    = "skipped-bead-not-open"
	SkipBeadClaimed    = "skipped-bead-claimed"
	SkipBeadHuman      = "skipped-bead-human"
	SkipBeadNotGroomed = "skipped-bead-not-groomed"
	SkipBeadDeferred   = "skipped-bead-deferred"
	SkipBeadBlocked    = "skipped-bead-blocked"
)

// labelHasAcceptanceCriteria marks a bead as groomed: the drain query selects
// on it, so a bead that lost it is no longer one a drain worker may take.
const labelHasAcceptanceCriteria = "has-acceptance-criteria"

// readyExcludedLabels are the labels that take a bead out of a drain worker's
// reach (INV-CCH-22): any of them present now declines the dispatch.
var readyExcludedLabels = []string{beads.LabelHuman, "human-focus-required", beads.LabelNeedsSplitReview, "escalated"}

// precheckEventKind is the event-log record kind written for every skip.
const precheckEventKind = "precheck_skip"

// connectorCallTimeout bounds one read-only pg-connector call made by the
// precheck. A call that outlives it is an error, which fails open.
const connectorCallTimeout = 20 * time.Second

// defaultConnectorBinary is the pg-connector executable resolved on PATH when
// config.Config.ConnectorBinary is empty.
const defaultConnectorBinary = "pg-connector"

// reviewRoleName is the role the precheck applies to: the role that works a
// review-pr bead (the same role-name convention INV-CCH-12 uses).
const reviewRoleName = "review"

// Skip is a precheck decision to launch no session for a dispatch.
type Skip struct {
	// Reason is one of the Skip* constants; it is the busy-decline reason tag.
	Reason string
	// Detail is a short human-readable account for the log line.
	Detail string
}

// PendingReview is the part of pg-connector's review_pending record the
// precheck reads.
type PendingReview struct {
	// Pending is false when the acting identity has no pending review.
	Pending bool
	// Stale is the connector's own verdict: true only when nothing in the
	// pending review is anchored to the PR's live head.
	Stale bool
}

// PRReader is the read-only view of a pull request the precheck needs. The
// production implementation (ConnectorReader) goes through pg-connector; tests
// substitute a fake. Every method MUST be read-only.
type PRReader interface {
	// PRMerged reports whether the PR (id "<repo>#<number>") is merged.
	PRMerged(ctx context.Context, prID string) (bool, error)
	// PendingReview reports the acting identity's pending review on the PR.
	PendingReview(ctx context.Context, prID string) (PendingReview, error)
}

// ConnectorReader is the production PRReader: it runs the read-only
// `pg-connector pr show <id>` and `pg-connector pr review pending <id>` verbs
// through the executor's existing argv seam (query.Commander).
type ConnectorReader struct {
	Cmd query.Commander
	// Binary is the pg-connector executable; empty means "pg-connector" on PATH.
	Binary string
}

func (c ConnectorReader) binary() string {
	if c.Binary == "" {
		return defaultConnectorBinary
	}
	return c.Binary
}

// run executes one pg-connector verb and returns its wire envelope's result,
// or an error for a failed process or an error envelope.
func (c ConnectorReader) run(ctx context.Context, args ...string) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, connectorCallTimeout)
	defer cancel()
	out, err := c.Cmd.Run(ctx, append([]string{c.binary()}, args...))
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", c.binary(), strings.Join(args, " "), err)
	}
	var env struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(out, &env); err != nil {
		return nil, fmt.Errorf("%s %s: decode output: %w", c.binary(), strings.Join(args, " "), err)
	}
	if env.Error != nil {
		return nil, fmt.Errorf("%s %s: connector error %q: %s", c.binary(), strings.Join(args, " "), env.Error.Code, env.Error.Message)
	}
	if len(env.Result) == 0 {
		return nil, fmt.Errorf("%s %s: output carries no result", c.binary(), strings.Join(args, " "))
	}
	return env.Result, nil
}

// PRMerged implements PRReader. `pr show` may be answered from the connector's
// read-through cache; that is safe here because merged is monotonic: a cached
// "open" only means the precheck launches as it always did, and a cached
// "merged" is final.
func (c ConnectorReader) PRMerged(ctx context.Context, prID string) (bool, error) {
	raw, err := c.run(ctx, "pr", "show", prID)
	if err != nil {
		return false, err
	}
	var pr struct {
		State  string `json:"state"`
		Merged bool   `json:"merged"`
	}
	if err := json.Unmarshal(raw, &pr); err != nil {
		return false, fmt.Errorf("decode pr show result: %w", err)
	}
	return pr.Merged || strings.EqualFold(pr.State, "merged"), nil
}

// PendingReview implements PRReader.
func (c ConnectorReader) PendingReview(ctx context.Context, prID string) (PendingReview, error) {
	raw, err := c.run(ctx, "pr", "review", "pending", prID)
	if err != nil {
		return PendingReview{}, err
	}
	var res struct {
		Pending bool `json:"pending"`
		Review  *struct {
			Stale bool `json:"stale"`
		} `json:"review"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return PendingReview{}, fmt.Errorf("decode review pending result: %w", err)
	}
	if !res.Pending {
		return PendingReview{}, nil
	}
	if res.Review == nil {
		// A pending result with no record cannot be judged: report an error so
		// the precheck fails open rather than guessing.
		return PendingReview{}, errors.New("review pending result is pending but carries no review record")
	}
	return PendingReview{Pending: true, Stale: res.Review.Stale}, nil
}

func (d Deps) prReader() PRReader {
	if d.PR != nil {
		return d.PR
	}
	return ConnectorReader{Cmd: d.commander(), Binary: d.Cfg.ConnectorBinary}
}

var (
	repoPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*/[A-Za-z0-9][A-Za-z0-9._-]*$`)
	prNumPattern = regexp.MustCompile(`^[1-9][0-9]*$`)
)

// prIDFor builds the pg-connector PR id "<repo>#<number>" from a review item's
// metadata. ok is false when the metadata names no well-formed PR; the strict
// shape also keeps a malformed value from being read as a flag by the CLI.
func prIDFor(it item.Item) (id string, ok bool) {
	repo, _ := it.Metadata["repo"].(string)
	if !repoPattern.MatchString(repo) {
		return "", false
	}
	var num string
	switch v := it.Metadata["pr_number"].(type) {
	case float64:
		if v != float64(int64(v)) {
			return "", false
		}
		num = strconv.FormatInt(int64(v), 10)
	case int:
		num = strconv.Itoa(v)
	case int64:
		num = strconv.FormatInt(v, 10)
	case json.Number:
		num = v.String()
	case string:
		num = v
	}
	if !prNumPattern.MatchString(num) {
		return "", false
	}
	return repo + "#" + num, true
}

// Precheck runs the dispatch precheck the role opted in to (INV-CCH-22): the
// role's Precheck setting when set, else the historical default (the role named
// "review" runs the review precheck, every other role none). It is read-only,
// fails open, and its single caller is cmd/.../dispatch.go.
func Precheck(ctx context.Context, d DispatchContext, deps Deps, iss *beads.Issue) (Skip, bool) {
	if d.Role.CCPool == nil {
		return Skip{}, false
	}
	switch d.Role.CCPool.Precheck {
	case roles.PrecheckReady:
		return PrecheckReady(d, deps, iss)
	case roles.PrecheckReview:
		return precheckReview(ctx, d, deps, iss)
	}
	return PrecheckReview(ctx, d, deps, iss)
}

// PrecheckReady decides, at zero model cost, whether a dispatch of a role that
// works a bead it claims itself (a drain worker) should launch no session at all
// (INV-CCH-22, bead pg2-nk6th.3). It reads only iss, the bead RefreshItemIssue
// just fetched from `bd show` (no extra bd call), and declines when, in this
// order:
//
//  1. the bead is not open (a deferred or blocked status gets its own reason);
//  2. an actor other than the role's own holds or is assigned it;
//  3. it carries human, human-focus-required, needs-split-review or escalated;
//  4. it no longer carries has-acceptance-criteria;
//  5. its defer_until is in the future (the worker's own DEFER-ON-EVENT leaves
//     exactly open plus a future defer_until);
//  6. it has an open blocks dependency (the worker's own CONVERT leaves exactly
//     open plus a blocker edge).
//
// A bead that is in_progress and assigned to the role's own actor always
// proceeds, whatever else it carries: it is the live session of an earlier
// dispatch, which the absorb path re-adopts after a daemon restart. A nil iss
// (the bead could not be read) fails open, and so does an unparseable
// defer_until. A decline writes nothing to the bead.
func PrecheckReady(d DispatchContext, deps Deps, iss *beads.Issue) (Skip, bool) {
	if d.Role.CCPool == nil || iss == nil {
		return Skip{}, false
	}
	actor := d.Role.CCPool.Actor
	ownInProgress := iss.Status == "in_progress" && actor != "" && iss.Assignee == actor
	if ownInProgress {
		return Skip{}, false
	}
	decline := func(reason, detail string) (Skip, bool) {
		return deps.skip(d, Skip{Reason: reason, Detail: detail}, ""), true
	}
	switch iss.Status {
	case "open":
	case "deferred":
		return decline(SkipBeadDeferred, "the bead's status is deferred")
	case "blocked":
		return decline(SkipBeadBlocked, "the bead's status is blocked")
	default:
		return decline(SkipBeadNotOpen, "the bead's status is "+iss.Status+", not open")
	}
	if iss.Assignee != "" && iss.Assignee != actor {
		return decline(SkipBeadClaimed, "the bead is assigned to another actor")
	}
	for _, l := range readyExcludedLabels {
		if iss.HasLabel(l) {
			return decline(SkipBeadHuman, "the bead now carries the "+l+" label")
		}
	}
	if !iss.HasLabel(labelHasAcceptanceCriteria) {
		return decline(SkipBeadNotGroomed, "the bead no longer carries "+labelHasAcceptanceCriteria)
	}
	if iss.DeferredUntilAfter(deps.clock()) {
		return decline(SkipBeadDeferred, "the bead's defer_until is in the future")
	}
	if iss.HasOpenBlocker() {
		return decline(SkipBeadBlocked, "the bead has an open blocks dependency")
	}
	return Skip{}, false
}

// PrecheckReview decides, at zero model cost, whether a review dispatch should
// launch no session at all (INV-CCH-22, bead pg2-5x29j). It is read-only and
// runs only for the review role. It declines when, in this order:
//
//  1. the review bead is already closed (read from iss, the bead
//     RefreshItemIssue just fetched: no extra bd call);
//  2. the PR is merged (pg-connector pr show);
//  3. the acting identity already has a pending review on the PR that is not
//     stale, i.e. holds content for the PR's live head (pg-connector pr review
//     pending).
//
// It fails OPEN: an unreadable bead, a PR id the item does not carry, or any
// connector failure (missing binary, timeout, error envelope) logs a warning
// and launches exactly as before. A decline logs a message distinct per reason,
// writes a precheck_skip event-log record, and (via the busy-decline reason the
// caller replies with) lands in pg-router core's declined metric by reason.
func PrecheckReview(ctx context.Context, d DispatchContext, deps Deps, iss *beads.Issue) (Skip, bool) {
	if d.Role.Name != reviewRoleName || d.Role.CCPool == nil {
		return Skip{}, false
	}
	return precheckReview(ctx, d, deps, iss)
}

// precheckReview is PrecheckReview without the role-name gate, for a role that
// opted in with precheck = "review".
func precheckReview(ctx context.Context, d DispatchContext, deps Deps, iss *beads.Issue) (Skip, bool) {
	if d.Role.CCPool == nil {
		return Skip{}, false
	}
	if iss != nil && iss.Status == "closed" {
		return deps.skip(d, Skip{Reason: SkipBeadClosed, Detail: "the review bead is already closed"}, ""), true
	}
	prID, ok := prIDFor(d.Item)
	if !ok {
		slog.Debug("dispatch precheck: item names no well-formed PR; skipping the PR checks", "role", d.Role.Name, "bead", d.Item.ID)
		return Skip{}, false
	}
	pr := deps.prReader()
	merged, err := pr.PRMerged(ctx, prID)
	switch {
	case err != nil:
		slog.Warn("dispatch precheck: could not read PR merged state; launching anyway", "role", d.Role.Name, "bead", d.Item.ID, "pr", prID, "err", err)
	case merged:
		return deps.skip(d, Skip{Reason: SkipPRMerged, Detail: "the PR is already merged"}, prID), true
	}
	pending, err := pr.PendingReview(ctx, prID)
	switch {
	case err != nil:
		slog.Warn("dispatch precheck: could not read pending-review state; launching anyway", "role", d.Role.Name, "bead", d.Item.ID, "pr", prID, "err", err)
	case pending.Pending && !pending.Stale:
		return deps.skip(d, Skip{Reason: SkipPendingReview, Detail: "a pending review already holds content for the PR's live head"}, prID), true
	}
	return Skip{}, false
}

// skip logs a precheck decline with a message distinct per reason and records
// it in the event log, then returns s. pr is "" for the bead-closed case.
func (d Deps) skip(dc DispatchContext, s Skip, pr string) Skip {
	var msg string
	switch s.Reason {
	case SkipBeadClosed:
		msg = "dispatch declined: review bead already closed"
	case SkipPRMerged:
		msg = "dispatch declined: PR already merged"
	case SkipPendingReview:
		msg = "dispatch declined: pending review already covers the head"
	case SkipBeadNotOpen:
		msg = "dispatch declined: bead is not open"
	case SkipBeadClaimed:
		msg = "dispatch declined: bead is held by another actor"
	case SkipBeadHuman:
		msg = "dispatch declined: bead is marked for a human"
	case SkipBeadNotGroomed:
		msg = "dispatch declined: bead is no longer groomed"
	case SkipBeadDeferred:
		msg = "dispatch declined: bead is deferred"
	case SkipBeadBlocked:
		msg = "dispatch declined: bead is blocked"
	default:
		msg = "dispatch declined: precheck"
	}
	slog.Info(msg, "role", dc.Role.Name, "bead", dc.Item.ID, "pr", pr, "reason", s.Reason)
	if d.Log != nil {
		_ = d.Log.Emit("info", precheckEventKind, msg, map[string]any{
			"role": dc.Role.Name, "bead": dc.Item.ID, "pr": pr, "reason": s.Reason, "detail": s.Detail,
		})
	}
	return s
}
