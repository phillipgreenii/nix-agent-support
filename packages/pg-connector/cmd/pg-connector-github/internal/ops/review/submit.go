// Package review is the pr instance's review_submit op, run through the daemon:
// the write that puts a request's content into the acting identity's PENDING
// review. It never deletes, replaces or submits anything. It is the old backend's
// Backend.SubmitReview (cmd/pg-connector-pr-github/internal/review_submit.go)
// copied with its behavior, result shape, caps and error messages unchanged, and
// run under the daemon's write rules.
//
// # The run
//
// The handler parses and validates the request (the old validations and
// messages), then calls Engine.SerializeWrite with the WHOLE remainder of the run,
// so two writes to one PR never overlap inside the daemon while writes to
// different PRs run in parallel. Inside it takes the posted-review sidecar's
// cross-process flock (the old backend's lock file, at its current path, so a
// rollback to the old binary and an operator editing a record by hand both stay
// safe), runs the old steps (read the live head and every pending review, classify
// every comment by its hidden fingerprint, create or append in failure-isolated
// batches, re-read to decide what landed, record what landed in the sidecar) and
// releases the lock before the closure returns.
//
// The lock wait is the locker's own wait cut to the request context's remaining
// time, so a PR held by another process makes the call unavailable inside the
// caller's deadline instead of outliving it.
//
// # Reads and spend
//
// The run reads origin for its own preconditions (the live head INV-REVHEAD-3
// requires, the existing pending comments) outside the cache. Every origin call
// is made under metered.WithCall as governor.ClassInvalidated (class 2) with
// governor.PurposeWrite, so the governor admits it as a write; a refusal answers
// unavailable naming the cap, the reserve or the pause. The old backend's
// rate_reserve_points probe is not ported: the governor's reserve replaces it.
//
// # After the write
//
// When the run changed the pending review (status posted or append, or a partial
// run in which some comment landed or may have landed), the handler marks the
// pending group stale and queues its refresh at class 2
// (Engine.InvalidateAndQueue), after the in-daemon lock is released. A refresh
// that began before the write cannot overwrite what is stored after it (the
// group's generation number, groups.Invalidate), so it stores nothing and logs no
// change. A run that wrote nothing (no_change, or a failure before any write)
// invalidates nothing.
//
// # Retries
//
// The per-comment fingerprints make every retry idempotent: replaying the
// identical request after an unavailable answer, a crash or a partial landing
// skips what is on the host and writes only the rest; a replay after full success
// is no_change.
package review

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/github"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groupsvc"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/metered"
	pgposted "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/posted"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/ratefeed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/router"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/pr"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// OpName is the wire name of the op, on the pr instance.
const OpName = "review_submit"

// Ops lists the ops this package provides, for the assembly that registers them
// under instance pr.
var Ops = []string{OpName}

// servedFromOrigin is the router annotation of an answer that went to origin.
const servedFromOrigin = "origin"

const (
	// maxRequestComments caps the comments of one request.
	//
	// Ruling (bead pg2-m79ch, 2026-10-07; options: lower the cap, extend the
	// verb's exec timeout, or document retry-on-kill): the cap is lowered so
	// that a request at the cap always completes inside scriptout's
	// DefaultBackendTimeout (pkg/scriptout/limits.go, 30s), which bounds the whole
	// run: the lookup, the sequential write documents (10 mutations each, see
	// github.maxWriteAliasesPerDocument) and the re-read. The previous cap of
	// 200 (20 documents) was not reachable in 30s: live, a 105-comment request
	// (11 documents) was killed ("signal: killed") after 70 comments had
	// landed, i.e. 7 documents plus the first read fit in under 30s, so one
	// document costs at most about 4s. 40 comments is 4 documents, about 16s
	// of writes; with a generous 8s for the two reads the worst case is about 24s,
	// under the 30s timeout. Raising the timeout was rejected
	// because it would also lengthen the bound on a genuinely hung gh for
	// every other op; retry-on-kill was rejected because the first call
	// would still fail mid-way. A caller with more comments splits them
	// across requests: the replay is idempotent (already_present), so
	// sequential requests converge on the same review.
	//
	// The daemon keeps the cap and the arithmetic: it passes the client's
	// deadline to the handler (the backend deadline less a margin).
	maxRequestComments = 40
	// maxOlderHeadComments caps the comments of a request that saves at an
	// EARLIER head of the PR (INV-REVHEAD-3). That path reads two more things
	// inside the same 30s run, the PR's commit list and the compared patches
	// (one or more REST pages), so the cap is lowered from the 40 above to 20:
	// two write documents (about 8s at the measured 4s each), the two reads
	// the live-head path has (about 8s, generous) and the two extra reads
	// (about 8s, generous) come to about 24s, under the 30s timeout with the
	// same 20% margin the live-head arithmetic keeps. A caller with more
	// comments splits them across requests; the replay is idempotent.
	maxOlderHeadComments = 20
	// maxFailuresListed caps the failures an error message lists.
	maxFailuresListed = 20
	// maxReviewBody is GitHub's limit on a review body.
	maxReviewBody = 65536
	// maxSubmitAttempts bounds how often a run starts over after the host
	// changed under it (a pending review appeared or went away between its
	// read and its write).
	maxSubmitAttempts = 3
)

// Engine is the part of the group refresh engine the op uses.
// *groupsvc.Service satisfies it.
type Engine interface {
	// SerializeWrite runs fn while holding the in-daemon lock of the PR, and
	// canonicalizes owner/repo#n through the alias table with no origin call.
	SerializeWrite(ctx context.Context, id string, fn func(ctx context.Context) error) error
	// InvalidateAndQueue marks the group stale and queues its refresh at class 2.
	InvalidateAndQueue(ctx context.Context, id, group string) error
}

var _ Engine = (*groupsvc.Service)(nil)

// Provider is the part of the GitHub Provider the run uses: its reads and its
// write primitives. *github.Provider, built over the metered runner
// (metered.NewProvider), satisfies it.
type Provider interface {
	GetPendingReview(ctx context.Context, repo string, number int) (*github.PendingReviewData, error)
	CreateBodyOnlyPendingReview(ctx context.Context, repo string, number int, commitID, body string) (*github.CreatedReview, error)
	WriteReviewItems(ctx context.Context, reviewID string, items []github.ReviewWriteItem) ([]github.ReviewWriteResult, error)
	UpdateReviewBody(ctx context.Context, reviewID, body string) error
	// GetPRHistory and GetComparedFiles are the reads review_submit needs to
	// save a review at an EARLIER head of the PR (INV-REVHEAD-1..3): the PR's
	// commit list with its base tip, and the patches of the files that differ
	// between that base tip and the earlier head.
	GetPRHistory(ctx context.Context, repo string, number int) (*github.PRHistory, error)
	GetComparedFiles(ctx context.Context, repo, base, head string, wanted []string) ([]github.ComparedFile, error)
}

var _ Provider = (*github.Provider)(nil)

// Deps are what the handler needs.
type Deps struct {
	Engine   Engine
	Provider Provider
	// Store and Locker are the posted-review sidecar, from sidecar.Open at daemon
	// start. The zero Locker has no directory, in which case the op answers
	// unavailable rather than writing without the lock (as the old backend does).
	Store  pgposted.Store
	Locker pgposted.Locker
	// Clock is the time source for the result's as_of; nil means the system clock.
	Clock governor.Clock
}

// Handler returns the op handler of review_submit. The assembly registers it
// under instance pr (router.Register("pr", OpName, h)).
func Handler(deps Deps) scriptout.OpHandler {
	if deps.Clock == nil {
		deps.Clock = governor.SystemClock{}
	}
	h := &handler{deps: deps}
	return scriptout.OpHandler{SchemaVersion: schema.PRSchemaVersion, Handle: h.handle}
}

type handler struct {
	deps Deps
}

func (h *handler) handle(ctx context.Context, args json.RawMessage) (any, error) {
	var a pr.ReviewSubmitRequest
	if err := scriptout.Decode(args, &a); err != nil {
		return nil, scriptout.WrapError(scriptout.ErrInvalidArgument, "decode review_submit args: "+err.Error())
	}
	if h.deps.Engine == nil || h.deps.Provider == nil {
		return nil, scriptout.WrapError(scriptout.ErrUnavailable, "review_submit: the daemon has no review writer wired")
	}
	res, err := h.submit(ctx, a)
	router.Annotate(ctx, servedFromOrigin, false, 0, 0)
	return res, err
}

// submit implements the op: validate, then the whole run inside the PR's write
// lock, then invalidate the pending group when the run changed it.
func (h *handler) submit(ctx context.Context, req pr.ReviewSubmitRequest) (pr.ReviewSubmitResult, error) {
	repo, number, err := parsePRID(req.ID)
	if err != nil {
		return pr.ReviewSubmitResult{}, scriptout.WrapError(scriptout.ErrInvalidArgument, err.Error())
	}
	if strings.TrimSpace(req.HeadSHA) == "" {
		return pr.ReviewSubmitResult{}, scriptout.WrapError(scriptout.ErrInvalidArgument, "review_submit: head_sha is required")
	}
	if !github.IsFullSHA(req.HeadSHA) {
		return pr.ReviewSubmitResult{}, scriptout.WrapError(scriptout.ErrInvalidArgument,
			fmt.Sprintf("review_submit: head_sha %q is not a full 40-character commit sha (an abbreviated sha is not accepted); nothing was written", req.HeadSHA))
	}
	items, err := buildSubmitItems(req.Comments)
	if err != nil {
		return pr.ReviewSubmitResult{}, err
	}
	bodyText := pgposted.NormalizeBody(req.Body)
	if bodyText != "" {
		if strings.Contains(bodyText, pgposted.SectionClose) || strings.Contains(bodyText, "<!-- pg-section") {
			return pr.ReviewSubmitResult{}, scriptout.WrapError(scriptout.ErrInvalidArgument,
				"review_submit: body must not contain a review body section delimiter")
		}
	}
	owner, name, _ := strings.Cut(repo, "/")

	// Every origin call of the run is a class 2 write: reads for its own
	// preconditions and the mutations alike.
	ctx = metered.WithCall(ctx, ratefeed.Meta{
		Class: governor.ClassInvalidated, Purpose: governor.PurposeWrite, Group: groups.Pending,
	})

	run := &submitRun{
		gh: h.deps.Provider, store: h.deps.Store, clock: h.deps.Clock,
		req: req, repo: repo, owner: owner, name: name, number: number, items: items, bodyText: bodyText,
	}
	var (
		res    pr.ReviewSubmitResult
		runErr error
	)
	err = h.deps.Engine.SerializeWrite(ctx, req.ID, func(ctx context.Context) error {
		res, runErr = h.locked(ctx, run)
		return nil
	})
	if err != nil {
		// The in-daemon lock was not obtained (the request's deadline passed
		// while another write to the PR ran) or the PR could not be keyed.
		return pr.ReviewSubmitResult{}, scriptout.WrapError(scriptout.ErrUnavailable,
			fmt.Sprintf("review_submit: could not start the write to %s: %v; nothing was written", req.ID, err))
	}

	if run.touched {
		// The write may have used up the request's own time; the invalidation is
		// the daemon's bookkeeping and must not be lost to it. It cannot fail
		// the request: the content is on the host and a retry is a no_change,
		// which invalidates nothing. A failure leaves the pending group to its
		// TTL.
		_ = h.deps.Engine.InvalidateAndQueue(context.WithoutCancel(ctx), req.ID, groups.Pending)
	}
	return res, runErr
}

// locked is the run under the in-daemon lock: it takes the sidecar's
// cross-process lock, runs the old steps (starting over a bounded number of times
// when the host changed under it) and releases the lock before returning.
func (h *handler) locked(ctx context.Context, run *submitRun) (pr.ReviewSubmitResult, error) {
	lock, err := h.acquire(ctx, run)
	if err != nil {
		return pr.ReviewSubmitResult{}, scriptout.WrapError(scriptout.ErrUnavailable,
			fmt.Sprintf("review_submit: could not take the per-PR lock for %s: %v", run.req.ID, err))
	}
	defer func() { _ = lock.Release() }()

	for range maxSubmitAttempts {
		res, again, err := run.once(ctx)
		if !again {
			return res, err
		}
	}
	return pr.ReviewSubmitResult{}, scriptout.WrapError(scriptout.ErrUnavailable,
		fmt.Sprintf("review_submit: the pending reviews of %s kept changing during the run; retry", run.req.ID))
}

// acquire takes the sidecar's cross-process lock, waiting no longer than the
// request context allows: the locker's own wait cut to ctx's remaining time. The
// locker is a value, so the copy's Wait is set without touching the shared one.
func (h *handler) acquire(ctx context.Context, run *submitRun) (*pgposted.Lock, error) {
	l := h.deps.Locker
	wait := l.Wait
	if wait <= 0 {
		wait = pgposted.LockWait
	}
	if dl, ok := ctx.Deadline(); ok {
		remaining := time.Until(dl)
		if remaining <= 0 {
			return nil, fmt.Errorf("%w: the request's deadline passed before the lock was tried", pgposted.ErrUnavailable)
		}
		wait = min(wait, remaining)
	}
	l.Wait = wait
	return l.Acquire(run.owner, run.name, run.number)
}

// parsePRID parses the op's id convention "<owner>/<repo>#<number>" into repo and
// number. Its messages are the old backend's.
func parsePRID(id string) (repo string, number int, err error) {
	i := strings.LastIndex(id, "#")
	if i < 0 {
		return "", 0, fmt.Errorf("pg-connector-pr-github: id %q is not in \"<owner>/<repo>#<number>\" form", id)
	}
	repo = id[:i]
	if !strings.Contains(repo, "/") {
		return "", 0, fmt.Errorf("pg-connector-pr-github: id %q's repo part %q is not in owner/name form", id, repo)
	}
	n, convErr := strconv.Atoi(id[i+1:])
	if convErr != nil || n <= 0 {
		return "", 0, fmt.Errorf("pg-connector-pr-github: id %q's number part is not a positive integer", id)
	}
	return repo, n, nil
}

// classifyReviewSubmitError maps a host failure onto INV-ERR-1. An HTTP 422 is
// invalid_argument (the host rejected what was sent); a governor refusal, a rate
// limit, an authentication failure and a missing PR go through metered.WireError
// (the daemon's one mapping); everything else is unavailable and is not retried
// inside the call.
func classifyReviewSubmitError(err error) error {
	if strings.Contains(strings.ToLower(err.Error()), "http 422") {
		return scriptout.WrapError(scriptout.ErrInvalidArgument, "review_submit: GitHub rejected the request (HTTP 422): "+err.Error())
	}
	err = metered.WireError(err)
	for _, s := range []error{scriptout.ErrNotFound, scriptout.ErrUnauthenticated, scriptout.ErrInvalidArgument, scriptout.ErrUnavailable} {
		if errors.Is(err, s) {
			return err
		}
	}
	return scriptout.WrapError(scriptout.ErrUnavailable, err.Error())
}
