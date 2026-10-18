// backend_status.go: the optional "status" op as the health rows of
// "pg-connector auth status" and "pg-connector config validate" (bead
// pg2-z5fax.5.6, spec "Caller experience" and "Consumer changes at cutover").
//
// A daemon-backed backend (pg-connector-github's client) answers a status op
// even when its daemon is down, by degrading to a local report. The umbrella
// asks every registered backend for it and, for a backend that answers,
// shows that backend's health as one extra row beside its auth row, so the
// operator learns WHY an auth_status of unavailable happened without leaving
// the umbrella.
//
// Conventions chosen where the design is silent (recorded in the bead's close
// reason):
//
//   - One extra row per answering backend, directly AFTER the backend's own
//     row, in BOTH verbs (withHealthRows is the one place that builds them).
//   - The row's source is "<backend>/health" (healthRowSuffix); its count is 0.
//   - healthy is a succeeded row (its reasons, if any, ride along as the
//     reason); degraded and down are degraded rows whose reason is
//     "<health>: <reasons joined by "; ">" followed, for a local report, by
//     "; supervisor: <state>; socket: <path>; store: <age or absent>".
//   - A backend that does not offer status contributes NO row, so its output
//     and the exit code stay byte-identical to before. The exit code is the
//     existing FanOutOutcome.ExitCode over all rows.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// opStatus is the optional status op of a daemon-backed backend.
const opStatus = "status"

// healthRowSuffix is appended to a backend's name to name its health row.
const healthRowSuffix = "/health"

// Values of backendStatusDoc.Health.
const (
	healthHealthy  = "healthy"
	healthDegraded = "degraded"
	healthDown     = "down"
)

// backendStatusDoc is the part of the daemon client's status document the
// umbrella decodes (field names per cmd/pg-connector-github/internal/ops/
// status/report.go, which the umbrella may not import).
type backendStatusDoc struct {
	Health  string   `json:"health"`
	Reasons []string `json:"reasons"`
	Source  string   `json:"source"`
	Socket  string   `json:"socket"`
	Store   struct {
		Present    bool  `json:"present"`
		AgeSeconds int64 `json:"age_seconds"`
	} `json:"store"`
	Supervisor struct {
		State string `json:"state"`
	} `json:"supervisor"`
}

// backendStatusRow asks backend for its status and maps the answer to its
// health row. ok is false (no row, no effect) when the backend does not offer
// status: it answers unknown_op, fails in any way, or answers something that
// does not decode to a document whose health is healthy, degraded or down.
func backendStatusRow(ctx context.Context, reg *Registry, backend string) (SourceResult, bool) {
	config, err := reg.BackendConfig(backend)
	if err != nil {
		return SourceResult{}, false
	}
	resp, err := reg.Invoke(ctx, backend, opStatus, nil, config)
	if err != nil || resp == nil {
		return SourceResult{}, false
	}
	var doc backendStatusDoc
	if err := json.Unmarshal(resp.Result, &doc); err != nil {
		return SourceResult{}, false
	}
	row := SourceResult{Source: backend + healthRowSuffix}
	switch doc.Health {
	case healthHealthy:
		row.Status = SourceSucceeded
		row.Reason = strings.Join(doc.Reasons, "; ")
	case healthDegraded, healthDown:
		row.Status = SourceDegraded
		row.Reason = healthReason(doc)
	default:
		return SourceResult{}, false
	}
	return row, true
}

// healthReason composes a degraded or down row's reason: the health word,
// all of the report's reasons in order and, for a local report (the daemon
// did not answer), the supervisor probe's answer, the socket path and the
// store file's age.
func healthReason(doc backendStatusDoc) string {
	reason := doc.Health
	if len(doc.Reasons) > 0 {
		reason += ": " + strings.Join(doc.Reasons, "; ")
	}
	if doc.Source == "local" {
		store := "absent"
		if doc.Store.Present {
			store = "age " + (time.Duration(doc.Store.AgeSeconds) * time.Second).String()
		}
		reason += fmt.Sprintf("; supervisor: %s; socket: %s; store: %s", doc.Supervisor.State, doc.Socket, store)
	}
	return reason
}

// withHealthRows runs one (the verb's own per-backend row) for every backend
// and, in the same per-backend call, the optional status call; the rows come
// back in registration order, each backend's health row (when it has one)
// directly after its own row. A backend whose call panicked becomes a
// degraded row carrying that error.
func withHealthRows(ctx context.Context, reg *Registry, backends []string, one func(ctx context.Context, backend string) SourceResult) []SourceResult {
	results := fanOutEach(ctx, reg, backends, func(ctx context.Context, b string) []SourceResult {
		rows := []SourceResult{one(ctx, b)}
		if health, ok := backendStatusRow(ctx, reg, b); ok {
			rows = append(rows, health)
		}
		return rows
	})
	out := make([]SourceResult, 0, len(results))
	for i, r := range results {
		if r.Err != nil {
			out = append(out, SourceResult{Source: backends[i], Status: SourceDegraded, Reason: r.Err.Error()})
			continue
		}
		out = append(out, r.Val...)
	}
	return out
}
