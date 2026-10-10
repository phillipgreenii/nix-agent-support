// Package degraded implements work-report's degraded-source path (design 7.7):
// after a pull, every degraded source is backed by exactly one open escalation
// bead in the personal tracker, reached only through `pg-connector issue`.
//
// The lifecycle per source is ensure / append / close:
//
//   - a degraded outcome with no open bead creates one, titled
//     "work-report: <source> degraded" and labeled "escalated" and
//     "work-report", whose body carries the reason, the range, the exact
//     re-pull command and pg-connector's `config validate` row for the backend;
//   - a degraded outcome with an open bead appends the new outcome as a comment
//     (nothing is created);
//   - a succeeded outcome with an open bead closes it with a reason.
//
// The existing-bead lookup MUST use the dedicated non-ready dedup query
// "escalated-all", never the ready-only "escalated-work" (which hides a bead
// once it is claimed, human-labeled or deferred, and so duplicates it). The
// tracker is selected by the registered pg-connector backend instance every
// call passes as --backend: WORK_REPORT_ISSUE_BACKEND when set, otherwise the
// one registered issue backend that defines the dedup query (found by the
// unpinned lookup itself; see pickTracker). No backend name is hard-coded, so
// a registration rename or split cannot leave the lifecycle querying a name
// that no longer exists. This package never sets PG_CONNECTOR_ISSUE_BEADS_DIR
// (an instance without its own --beads-dir falls back to the inherited
// environment).
package degraded

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/work-report/internal/pgconn"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/work-report/internal/pull"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/work-report/internal/rangespec"
)

const (
	// EnvBackend names the pg-connector backend instance (a suffixed
	// registration of the beads backend, pg2-91y12) that every call passes as
	// --backend. Unset or empty means the tracker is discovered from the
	// registered issue backends (pg2-sqc5v).
	EnvBackend = "WORK_REPORT_ISSUE_BACKEND"
	// DedupQuery lists every non-closed escalated bead (open, in_progress,
	// blocked, deferred, human-labeled).
	DedupQuery = "escalated-all"
	// ItemType is the pg-router item type of an escalated bead.
	ItemType = "issue"

	labelEscalated = "escalated"
	labelWorkRep   = "work-report"
	expiryGrace    = 6 * time.Hour
)

// Item is one pg-router item: a degraded source's escalated bead.
type Item struct {
	ID        string          `json:"id"`
	Type      string          `json:"type"`
	Title     string          `json:"title"`
	ExpiresAt time.Time       `json:"expiresAt"`
	Metadata  pull.OutcomeRow `json:"metadata"`
}

// Title is the title key of a source's degraded bead.
func Title(source string) string { return "work-report: " + source + " degraded" }

// Reconcile performs the ensure / append / close lifecycle for every row of a
// pull and returns one Item per degraded row whose bead could be ensured. now
// MUST be expressed in the configured zone: an Item expires at the end of the
// local day of now.Location() plus six hours. repull is the exact
// "work-report pull ..." command line printed in a created bead's body. backend
// is the pg-connector issue backend instance every call passes as --backend;
// empty means discover it from the registered issue backends (pickTracker).
//
// A failure to reach the tracker never aborts the whole reconcile: the items
// that could be built are returned together with a non-nil (joined) error.
func Reconcile(ctx context.Context, conn pgconn.Runner, backend string, rows []pull.OutcomeRow, rng rangespec.Range, now time.Time, repull string) ([]Item, error) {
	items := []Item{}
	if !needsTracker(rows) {
		return items, nil
	}

	open, backend, err := listOpen(ctx, conn, backend)
	if err != nil {
		return items, fmt.Errorf("look up open degraded-source beads: %w", err)
	}

	var errs []error
	var validate validateCache
	for _, row := range rows {
		title := Title(row.Source)
		existing, found := open[title]
		switch row.Status {
		case pull.StatusDegraded:
			id := existing.ID
			if found {
				if err := comment(ctx, conn, backend, id, commentBody(row, rng, now)); err != nil {
					errs = append(errs, fmt.Errorf("%s: append to %s: %w", row.Source, id, err))
					continue
				}
			} else {
				created, err := create(ctx, conn, backend, title, createBody(ctx, conn, &validate, row, rng, now, repull))
				if err != nil {
					errs = append(errs, fmt.Errorf("%s: create bead: %w", row.Source, err))
					continue
				}
				id = created
				open[title] = entity{ID: id, Title: title}
			}
			items = append(items, Item{
				ID: id, Type: ItemType, Title: title,
				ExpiresAt: endOfLocalDay(now).Add(expiryGrace),
				Metadata:  row,
			})
		case pull.StatusSucceeded:
			if !found {
				continue
			}
			if err := closeBead(ctx, conn, backend, existing.ID, closeReason(row, now)); err != nil {
				errs = append(errs, fmt.Errorf("%s: close %s: %w", row.Source, existing.ID, err))
				continue
			}
			delete(open, title)
		}
	}
	return items, errors.Join(errs...)
}

// needsTracker reports whether any row can require a tracker call.
func needsTracker(rows []pull.OutcomeRow) bool {
	for _, r := range rows {
		if r.Status == pull.StatusDegraded || r.Status == pull.StatusSucceeded {
			return true
		}
	}
	return false
}

// endOfLocalDay is the start of the next local day of t's own location.
func endOfLocalDay(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d+1, 0, 0, 0, 0, t.Location())
}

// ---- body text -------------------------------------------------------------

func rangeText(rng rangespec.Range, loc *time.Location) string {
	before := rng.Before.In(loc).Format(time.RFC3339)
	if rng.OpenStart {
		return "beginning of time .. " + before
	}
	return rng.Since.In(loc).Format(time.RFC3339) + " .. " + before
}

func outcomeLine(row pull.OutcomeRow) string {
	s := fmt.Sprintf("status %s (%d stored, %d unchanged, %d rejected", row.Status, row.Count, row.Unchanged, row.Rejected)
	if row.Truncated {
		s += ", truncated"
	}
	s += ")"
	if row.Reason != "" {
		s += ": " + row.Reason
	}
	return s
}

func commentBody(row pull.OutcomeRow, rng rangespec.Range, now time.Time) string {
	return fmt.Sprintf("Still degraded at %s.\n\nRange: %s\nOutcome: %s\n",
		now.Format(time.RFC3339), rangeText(rng, now.Location()), outcomeLine(row))
}

func closeReason(row pull.OutcomeRow, now time.Time) string {
	return fmt.Sprintf("work-report pull for %s succeeded again at %s", row.Source, now.Format(time.RFC3339))
}

func createBody(ctx context.Context, conn pgconn.Runner, vc *validateCache, row pull.OutcomeRow, rng rangespec.Range, now time.Time, repull string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "work-report could not pull activity from the %q source.\n\n", row.Source)
	fmt.Fprintf(&b, "Reason: %s\n", outcomeLine(row))
	fmt.Fprintf(&b, "Range: %s\n", rangeText(rng, now.Location()))
	fmt.Fprintf(&b, "First seen: %s\n\n", now.Format(time.RFC3339))
	fmt.Fprintf(&b, "Re-pull command:\n\n    %s\n\n", repull)
	fmt.Fprintf(&b, "pg-connector config validate row for this backend:\n\n    %s\n\n", vc.row(ctx, conn, row.Source))
	b.WriteString("Later degraded pulls append to this bead; it is closed automatically when the source succeeds again.\n")
	return b.String()
}

// validateCache fetches `pg-connector config validate` at most once per
// Reconcile and serves the row for a backend.
type validateCache struct {
	done bool
	rows map[string]json.RawMessage
	err  error
}

func (v *validateCache) row(ctx context.Context, conn pgconn.Runner, source string) string {
	if !v.done {
		v.done = true
		v.rows, v.err = fetchValidate(ctx, conn)
	}
	switch {
	case v.err != nil:
		return fmt.Sprintf("(unavailable: %v)", v.err)
	case v.rows[source] == nil:
		return "(pg-connector config validate reported no row for this backend)"
	}
	return string(v.rows[source])
}

func fetchValidate(ctx context.Context, conn pgconn.Runner) (map[string]json.RawMessage, error) {
	out, err := call(ctx, conn, okFanOut, "config", "validate", "--output", "json")
	if err != nil {
		return nil, err
	}
	var doc struct {
		Sources []json.RawMessage `json:"sources"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		return nil, fmt.Errorf("decode config validate document: %w", err)
	}
	rows := map[string]json.RawMessage{}
	for _, raw := range doc.Sources {
		var head struct {
			Source string `json:"source"`
		}
		if json.Unmarshal(raw, &head) == nil && head.Source != "" {
			compact, err := compactJSON(raw)
			if err != nil {
				continue
			}
			rows[head.Source] = compact
		}
	}
	return rows, nil
}

func compactJSON(raw json.RawMessage) (json.RawMessage, error) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	return json.Marshal(v)
}

// ---- pg-connector issue calls ---------------------------------------------

var (
	okTargeted = map[int]bool{0: true}
	// okFanOut accepts the fan-out scheme: 0 ok, 2 degraded but usable.
	okFanOut = map[int]bool{0: true, 2: true}
)

// call runs pg-connector and returns stdout when the exit code is in ok. A
// failure includes a stderr excerpt when the runner can report one.
func call(ctx context.Context, conn pgconn.Runner, ok map[int]bool, args ...string) ([]byte, error) {
	var stdout, stderr []byte
	var exit int
	var err error
	if dr, isDetailed := conn.(pgconn.DetailedRunner); isDetailed {
		var o pgconn.Output
		o, err = dr.RunDetailed(ctx, args...)
		stdout, stderr, exit = o.Stdout, o.Stderr, o.ExitCode
	} else {
		stdout, exit, err = conn.Run(ctx, args...)
	}
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", pgconn.Binary, args[0], err)
	}
	if !ok[exit] {
		msg := fmt.Sprintf("%s %s exited %d", pgconn.Binary, strings.Join(args[:2], " "), exit)
		if ex := strings.Join(strings.Fields(string(stderr)), " "); ex != "" {
			if len(ex) > 200 {
				ex = ex[:200] + "..."
			}
			msg += ": " + ex
		}
		return nil, errors.New(msg)
	}
	return stdout, nil
}

type entity struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// listOpen runs the dedup query and indexes the open beads by title. The first
// bead with a given title wins. A non-empty backend pins the lookup to it; an
// empty one fans out across every registered issue backend and returns the
// tracker the fan-out identified (pickTracker) together with its beads.
func listOpen(ctx context.Context, conn pgconn.Runner, backend string) (map[string]entity, string, error) {
	args := []string{"issue", "list", "--query", DedupQuery, "--output", "json"}
	if backend != "" {
		args = append(args, "--backend", backend)
	}
	out, err := call(ctx, conn, okFanOut, args...)
	if err != nil {
		return nil, "", err
	}
	var doc struct {
		Entities []entity    `json:"entities"`
		Sources  []sourceRow `json:"sources"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		return nil, "", fmt.Errorf("decode issue list document: %w", err)
	}
	if backend == "" {
		if backend, err = pickTracker(doc.Sources); err != nil {
			return nil, "", err
		}
	}
	open := map[string]entity{}
	for _, e := range doc.Entities {
		if _, dup := open[e.Title]; !dup {
			open[e.Title] = e
		}
	}
	return open, backend, nil
}

// sourceRow is one sources[] row of a fan-out document.
type sourceRow struct {
	Source string `json:"source"`
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

// statusDisabled is the fan-out status of a backend the call does not apply
// to, e.g. one that does not define the requested named query.
const statusDisabled = "disabled"

// pickTracker names the backend that holds the degraded-source beads: the one
// registered issue backend the unpinned dedup lookup did not report as
// disabled, because a backend that does not define DedupQuery reports
// "disabled: query not recognized". Zero or several such backends, or a single
// one that is itself degraded, is an error naming the candidates and the
// EnvBackend remedy, never a guess.
func pickTracker(sources []sourceRow) (string, error) {
	var candidates []sourceRow
	for _, s := range sources {
		if s.Status != statusDisabled {
			candidates = append(candidates, s)
		}
	}
	switch len(candidates) {
	case 0:
		var registered []string
		for _, s := range sources {
			registered = append(registered, s.Source)
		}
		return "", fmt.Errorf("no registered issue backend defines the %q query (registered: %v); define it on the tracker's backend or set %s",
			DedupQuery, registered, EnvBackend)
	case 1:
		if c := candidates[0]; c.Status != "succeeded" {
			return "", fmt.Errorf("tracker backend %s is %s: %s", c.Source, c.Status, c.Reason)
		}
		return candidates[0].Source, nil
	}
	names := make([]string, len(candidates))
	for i, c := range candidates {
		names[i] = c.Source
	}
	return "", fmt.Errorf("several registered issue backends define the %q query (%v); set %s to the tracker's backend",
		DedupQuery, names, EnvBackend)
}

func create(ctx context.Context, conn pgconn.Runner, backend, title, body string) (string, error) {
	out, err := call(ctx, conn, okTargeted, "issue", "create",
		"--title", title,
		"--description", body,
		"--labels", labelEscalated,
		"--labels", labelWorkRep,
		"--backend", backend,
		"--output", "json")
	if err != nil {
		return "", err
	}
	var env struct {
		Result struct {
			ID string `json:"id"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out, &env); err != nil {
		return "", fmt.Errorf("decode issue create response: %w", err)
	}
	if env.Result.ID == "" {
		return "", errors.New("issue create returned no id")
	}
	return env.Result.ID, nil
}

func comment(ctx context.Context, conn pgconn.Runner, backend, id, body string) error {
	_, err := call(ctx, conn, okTargeted, "issue", "comment", id, "--body", body, "--backend", backend, "--output", "json")
	return err
}

func closeBead(ctx context.Context, conn pgconn.Runner, backend, id, reason string) error {
	_, err := call(ctx, conn, okTargeted, "issue", "close", id, "--reason", reason, "--backend", backend, "--output", "json")
	return err
}
