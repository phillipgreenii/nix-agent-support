package changes

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/pipeline"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// Hydration origins [design 9.3].
const (
	OriginPGConnector = "pg-connector"
	OriginReset       = "reset"
)

// ErrTotalFailure is wrapped by Run's error when every watched query failed:
// nothing was hydrated or logged and the consumer's cursor did not move
// (exit 3, design 9.12).
var ErrTotalFailure = errors.New("changes: every watched query failed")

// Lister lists one watched query's complete current listing with the
// connector's per-entity fingerprints (no cursor, no consumer: every call
// returns the whole state of the query). *gather.Gatherer satisfies it.
type Lister interface {
	ListFingerprints(ctx context.Context, entityType, query string) (gather.ListFingerprintsResult, error)
}

// FingerprintSupported reports whether pg-connector can fingerprint the
// entities of entityType in a listing. A type without support (thread: a Slack
// list cannot be fingerprinted) has no list-and-diff changes flow.
func FingerprintSupported(entityType string) bool {
	switch entityType {
	case "pr", "issue":
		return true
	}
	return false
}

// ErrUnsupportedType is wrapped by Run's error when the entity type has no
// fingerprint support; nothing was called or written.
var ErrUnsupportedType = errors.New("changes: entity type has no list fingerprint support")

// Hydrator hydrates one entity, classifies the change and logs it.
// *pipeline.Pipeline satisfies it.
type Hydrator interface {
	RunEntityChange(ctx context.Context, entityType, entityID string, change gather.ChangeKind, opts pipeline.EntityChangeOptions) (pipeline.EntityChangeResult, error)
}

// Options are one changes call's inputs.
type Options struct {
	EntityType string
	Consumer   string
	// Query restricts a real call to one watched query ("" means all).
	Query string
	// Cached makes the call a read-only peek: no pg-connector call, no
	// hydration, no seen_at stamp, no cursor advance.
	Cached bool
	// Reset replays every active entity of the type to the consumer as
	// reconcile (origin reset). It cannot be combined with Cached.
	Reset bool
	// Limit caps records per call; <= 0 means no cap.
	Limit int
}

// Engine runs the list-and-diff `changes` flow. Zero Now means time.Now; a
// nil Warn discards non-fatal diagnostics.
type Engine struct {
	Cfg      *config.Config
	Store    *store.Store
	Lister   Lister
	Hydrator Hydrator
	Now      func() time.Time
	Warn     io.Writer
}

// Outcome tells the caller how a call that delivered an envelope ended.
// Partial is true when at least one watched query or hydration failed or
// degraded (exit 2).
type Outcome struct {
	Partial bool
}

// SelectQueries returns the watched queries a real call consults: all of the
// type's configured queries, or just query when it is non-empty (it MUST name
// a configured one). A real call needs at least one configured query.
func SelectQueries(cfg *config.Config, entityType, query string) ([]string, error) {
	configured := cfg.WatchQueries(entityType)
	if len(configured) == 0 {
		return nil, fmt.Errorf("changes: no watched queries configured for %s: set watch.%s.queries", entityType, entityType)
	}
	if query == "" {
		return configured, nil
	}
	for _, q := range configured {
		if q == query {
			return []string{q}, nil
		}
	}
	return nil, fmt.Errorf("changes: --query %q is not a configured watched query for %s (watch.%s.queries: %s)",
		query, entityType, entityType, strings.Join(configured, ", "))
}

// Run performs one changes call. emit is the flush: it receives the envelope
// and MUST write it out; the consumer's cursor is advanced only after emit
// returned nil, and --cached never advances it. When every watched query
// failed Run still emits an envelope (failed sources, no records, cursor
// unchanged) and returns an error wrapping ErrTotalFailure.
func (e *Engine) Run(ctx context.Context, opts Options, emit func(Envelope) error) (Outcome, error) {
	if !FingerprintSupported(opts.EntityType) {
		return Outcome{}, fmt.Errorf("%w: %s changes is not available (its list cannot be fingerprinted)", ErrUnsupportedType, opts.EntityType)
	}
	if opts.Cached && opts.Reset {
		return Outcome{}, errors.New("changes: --reset cannot be combined with --cached")
	}
	if opts.Cached {
		return e.runCached(opts, emit)
	}
	queries, err := SelectQueries(e.Cfg, opts.EntityType, opts.Query)
	if err != nil {
		return Outcome{}, err
	}
	// Liveness: every real call stamps the consumer's seen_at, even one that
	// ends in total failure [design 6.9].
	if err := e.Store.RegisterConsumer(opts.Consumer, opts.EntityType, e.now()); err != nil {
		return Outcome{}, fmt.Errorf("changes: %w", err)
	}

	// Phase A: list every watched query, whole and fingerprinted.
	runs := make([]queryRun, 0, len(queries))
	failedQueries := 0
	for _, q := range queries {
		if err := ctx.Err(); err != nil {
			return Outcome{}, fmt.Errorf("changes: %w", err)
		}
		res, lerr := e.Lister.ListFingerprints(ctx, opts.EntityType, q)
		if lerr != nil {
			failedQueries++
		}
		runs = append(runs, queryRun{query: q, res: res, err: lerr})
	}
	total := failedQueries == len(queries)

	st := &pollState{hydrated: map[string]error{}, notes: map[string][]string{}}

	if !total {
		// Phase A2: watched-set membership. An entity no configured query
		// lists any more becomes removed/inactive [design 6.1].
		st.side = append(st.side, e.applyMembershipCtx(ctx, opts.EntityType, runs)...)

		// Phase B: --reset replays every active entity as reconcile. The
		// replay is exempt from hydration.max_per_poll (see the behavior
		// doc): it is an explicit operator request and nothing is deferred.
		if opts.Reset {
			ids, err := e.activeEntityIDs(opts.EntityType)
			if err != nil {
				return Outcome{}, err
			}
			for _, id := range ids {
				if err := ctx.Err(); err != nil {
					return Outcome{}, fmt.Errorf("changes: %w", err)
				}
				herr := e.hydrate(ctx, opts.EntityType, id, gather.ChangeChanged, OriginReset, true, nil)
				st.hydrated[id] = herr
				if herr != nil {
					st.side = append(st.side, fmt.Sprintf("reset %s: %s", id, firstErrLine(herr.Error())))
				}
			}
		}

		// Phase C: hydrate what the list diff found added or changed, then the
		// rolling sweep, all within hydration.max_per_poll with changed/added
		// served first [design 8.4, 8.5]. Nothing is queued: what the cap or a
		// failure leaves is found again by the next tick's diff.
		bud := &budget{left: e.Cfg.HydrationMaxPerPoll()}
		queued, err := e.hydrateListed(ctx, opts, runs, st, bud)
		if err != nil {
			return Outcome{}, err
		}
		if err := e.sweep(ctx, opts.EntityType, st, bud, queued); err != nil {
			return Outcome{}, err
		}
	}

	sources := make([]Source, 0, len(runs))
	for _, r := range runs {
		if r.err != nil {
			sources = append(sources, Source{Query: r.query, Status: StatusFailed, Reason: firstErrLine(r.err.Error())})
			continue
		}
		reasons := degradedBackendReasons(r.res.Sources)
		if r.res.Truncated {
			reasons = appendUnique(reasons, ReasonListTruncated)
		}
		reasons = appendUnique(reasons, st.notes[r.query]...)
		reasons = appendUnique(reasons, st.all...)
		if len(reasons) > 0 {
			sources = append(sources, Source{Query: r.query, Status: StatusDegraded, Reason: strings.Join(reasons, "; ")})
		} else {
			sources = append(sources, Source{Query: r.query, Status: StatusOK})
		}
	}
	for _, f := range st.side {
		e.warnf("%s\n", f)
	}
	partial := len(st.side) > 0
	for _, s := range sources {
		if s.Status != StatusOK {
			partial = true
		}
	}

	if total {
		cur, err := e.consumerCursor(opts.Consumer, opts.EntityType)
		if err != nil {
			return Outcome{}, err
		}
		env := Envelope{
			Contract: Contract, Type: opts.EntityType, Consumer: opts.Consumer,
			Cursor: Cursor{From: cur, To: cur}, Sources: sources, Records: []Record{},
		}
		if err := emit(env); err != nil {
			return Outcome{}, fmt.Errorf("changes: write output: %w", err)
		}
		return Outcome{Partial: true}, fmt.Errorf("%w (%s)", ErrTotalFailure, joinReasons(sources))
	}

	// Phase D: read, flush and advance under the per-(type, consumer) lock so
	// two concurrent calls for one consumer serialize [design 6.8].
	unlock, err := e.Store.LockConsumer(opts.EntityType, opts.Consumer)
	if err != nil {
		return Outcome{}, fmt.Errorf("changes: lock consumer %s/%s: %w", opts.EntityType, opts.Consumer, err)
	}
	func() {
		defer unlock()
		err = e.deliver(opts, sources, emit)
	}()
	if err != nil {
		return Outcome{}, err
	}

	// Prune after a real call: rows past retention that every non-stale
	// consumer has passed [design 6.6, 6.9]. A failure here is not fatal: the
	// output was already delivered and the cursor advanced.
	if _, perr := e.Store.PruneChangeLog(e.now(), e.Cfg.ChangeLogRetention(), e.Cfg.ConsumerStaleAfter()); perr != nil {
		e.warnf("changes: prune change_log: %v\n", perr)
	}
	return Outcome{Partial: partial}, nil
}

// deliver reads the consumer's records, emits the envelope and, only after
// the emit succeeded, advances the cursor. The caller holds the consumer lock.
func (e *Engine) deliver(opts Options, sources []Source, emit func(Envelope) error) error {
	from, err := e.consumerCursor(opts.Consumer, opts.EntityType)
	if err != nil {
		return err
	}
	rows, err := e.Store.ReadChanges(opts.Consumer, opts.EntityType, opts.Limit, e.now())
	if err != nil {
		return fmt.Errorf("changes: %w", err)
	}
	env := e.envelope(opts, from, sources, rows)
	if err := emit(env); err != nil {
		return fmt.Errorf("changes: write output: %w", err)
	}
	if env.Cursor.To > from {
		if err := e.Store.AdvanceCursor(opts.Consumer, opts.EntityType, env.Cursor.To); err != nil {
			return fmt.Errorf("changes: %w", err)
		}
	}
	return nil
}

// runCached is the --cached peek: it reads the records a real call would
// return and touches nothing (no register, no seen_at, no cursor).
func (e *Engine) runCached(opts Options, emit func(Envelope) error) (Outcome, error) {
	from, err := e.consumerCursor(opts.Consumer, opts.EntityType)
	if err != nil {
		return Outcome{}, err
	}
	rows, err := e.Store.PeekChanges(opts.Consumer, opts.EntityType, opts.Limit)
	if err != nil {
		return Outcome{}, fmt.Errorf("changes: %w", err)
	}
	if err := emit(e.envelope(opts, from, []Source{}, rows)); err != nil {
		return Outcome{}, fmt.Errorf("changes: write output: %w", err)
	}
	return Outcome{}, nil
}

func (e *Engine) envelope(opts Options, from int64, sources []Source, rows []store.ChangeRecord) Envelope {
	titles := map[[2]string]string{}
	records := make([]Record, 0, len(rows))
	to := from
	for _, r := range rows {
		k := [2]string{r.Repo, r.EntityID}
		title, ok := titles[k]
		if !ok {
			if ent, found, err := e.Store.GetEntity(r.Repo, r.EntityType, r.EntityID); err == nil && found {
				title = TitleFromFacts(r.EntityType, ent.Facts)
			}
			titles[k] = title
		}
		kinds := r.Kinds
		if kinds == nil {
			kinds = []string{}
		}
		records = append(records, Record{
			Seq: r.Seq, Type: r.EntityType, ID: r.EntityID, Title: title,
			Version: r.Version, Kinds: kinds, Origin: r.Origin, At: r.At,
		})
		if r.Seq > to {
			to = r.Seq
		}
	}
	return Envelope{
		Contract: Contract, Type: opts.EntityType, Consumer: opts.Consumer,
		Cursor: Cursor{From: from, To: to}, Sources: sources, Records: records,
	}
}

// hydrate runs one entity through the pipeline and records the outcome. It
// returns the failure (error or degraded note) or nil. listFP is the list
// fingerprint observed this tick for a hydration driven by a listed entity,
// nil for every read that did not come from a list (reset replay, sweep, the
// removal confirmation read).
func (e *Engine) hydrate(ctx context.Context, entityType, id string, change gather.ChangeKind, origin string, forceReconcile bool, listFP *string) error {
	_, err := e.hydrateResult(ctx, entityType, id, change, origin, forceReconcile, listFP)
	return err
}

// hydrateResult is hydrate that also returns the pipeline's result, for the
// caller that must tell a not_found read from one that wrote a snapshot.
func (e *Engine) hydrateResult(ctx context.Context, entityType, id string, change gather.ChangeKind, origin string, forceReconcile bool, listFP *string) (pipeline.EntityChangeResult, error) {
	res, err := e.Hydrator.RunEntityChange(ctx, entityType, id, change, pipeline.EntityChangeOptions{
		Origin:             origin,
		ThreadActiveWindow: e.Cfg.ThreadActiveWindow(),
		ForceReconcile:     forceReconcile,
		ListFP:             listFP,
	})
	RecordHydration(e.Store, entityType, id, res, err)
	if err != nil {
		return res, err
	}
	if res.Degraded != "" {
		return res, fmt.Errorf("%w: %s", errDegraded, res.Degraded)
	}
	if res.NotFound {
		// Nothing was written: the stored facts say nothing new, so the
		// caller (the removal confirmation) decides what to log.
		return res, nil
	}
	// Spec 6.1 (a): an entity terminal in its source system stops being
	// active so the sweep leaves it alone. A failure here is not a hydration
	// failure: the entity stays active and the next sweep retries.
	if derr := e.deactivateIfTerminal(entityType, id); derr != nil {
		e.warnf("changes: deactivate %s %s: %v\n", entityType, id, derr)
	}
	return res, nil
}

// activeEntityIDs lists the ids of the type's active entities, sorted.
func (e *Engine) activeEntityIDs(entityType string) ([]string, error) {
	all, err := e.Store.ListEntities()
	if err != nil {
		return nil, fmt.Errorf("changes: list entities: %w", err)
	}
	var ids []string
	for _, ent := range all {
		if ent.EntityType == entityType && !ent.Inactive {
			ids = append(ids, ent.EntityID)
		}
	}
	sort.Strings(ids)
	return ids, nil
}

// consumerCursor is the consumer's current cursor, 0 when it is unregistered.
func (e *Engine) consumerCursor(name, entityType string) (int64, error) {
	cs, err := e.Store.ListConsumers()
	if err != nil {
		return 0, fmt.Errorf("changes: %w", err)
	}
	for _, c := range cs {
		if c.Name == name && c.Type == entityType {
			return c.Cursor, nil
		}
	}
	return 0, nil
}

func (e *Engine) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now().UTC()
}

func (e *Engine) warnf(format string, args ...any) {
	if e.Warn != nil {
		_, _ = fmt.Fprintf(e.Warn, format, args...)
	}
}

// degradedBackendReasons returns one reason per pg-connector backend row that
// is neither succeeded nor disabled (disabled counts as healthy).
func degradedBackendReasons(rows []gather.ListChangesSource) []string {
	var out []string
	for _, s := range rows {
		if s.Status == "succeeded" || s.Status == "disabled" {
			continue
		}
		switch {
		case s.Reason != "":
			out = append(out, s.Reason)
		default:
			out = append(out, fmt.Sprintf("%s %s", s.Backend, s.Status))
		}
	}
	return out
}

func joinReasons(sources []Source) string {
	var parts []string
	for _, s := range sources {
		if s.Status != StatusOK {
			parts = append(parts, fmt.Sprintf("%s: %s", s.Query, s.Reason))
		}
	}
	return strings.Join(parts, "; ")
}

func firstErrLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}
