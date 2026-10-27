// health.go: the "pg-connector <type> health" verb (INTF-WIRE-HEALTH-1), the
// caller-facing read of a daemon-backed backend's "status" document. Until
// this verb the status op reached an operator only as the one-line health rows
// of "auth status" and "config validate" (backend_status.go); a caller that
// must learn a query's baseline time or age (queries[].baseline_at once the
// baseline is stored, queries[].baseline_open_age_seconds while it is not) had
// no way to read it.
//
// It is a fan-out READ like capabilities: every backend registered for the
// type is asked once (--backend pins it to one), the exit codes follow the
// fan-out scheme (0 all succeeded, 2 partial, 3 total failure) and nothing is
// written to the ledger or the cache. A backend that offers status
// contributes a succeeded sources[] row and one backends[] entry whose status
// member is the backend's own document, never re-encoded or summarised. A
// backend that does not offer the op (it answers unknown_op) contributes a
// disabled row and no backends entry; a backend whose status call failed
// contributes a degraded row with its reason and no backends entry. The Jira
// client answers status with the daemon down (its local report), so an outage
// is visible as the document's health, not as a failed read.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// healthNotOfferedReason is the reason of the disabled row of a backend that
// does not offer the status op.
const healthNotOfferedReason = "status op not offered"

// healthBackend is one answering backend's entry of the verb's answer; Status
// is the backend's own document, byte for byte as the backend sent it.
type healthBackend struct {
	Backend string          `json:"backend"`
	Status  json.RawMessage `json:"status"`
}

// healthOutcome is the verb's whole JSON answer.
type healthOutcome struct {
	Sources  []SourceResult  `json:"sources"`
	Backends []healthBackend `json:"backends"`
}

// newHealthCmd builds the "health" verb for entityType; every type that fans
// out MAY register it next to its other fan-out verbs.
func newHealthCmd(entityType string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "health",
		Short: fmt.Sprintf("Report each %s backend's own status document (daemon health, per-query baseline); --backend pins one backend", entityType),
		Args:  cobra.NoArgs,
	}
	backendFlag := addBackendFlag(cmd, "pin the fan-out to exactly this backend instead of every registered "+entityType+" backend")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		report := func(err error) error {
			return writeTargetedResult(cmd, nil, err, func(json.RawMessage) (string, error) { return "", nil })
		}
		reg, err := LoadRegistry()
		if err != nil {
			return report(err)
		}
		backends, err := resolveListBackends(reg, entityType, *backendFlag)
		if err != nil {
			return report(scriptout.WrapError(scriptout.ErrInvalidArgument, err.Error()))
		}
		if len(backends) == 0 {
			return report(scriptout.WrapError(scriptout.ErrInvalidArgument, fmt.Sprintf("health: no backend registered for connector.%s", entityType)))
		}
		outcome := fanOutHealth(cmd.Context(), reg, backends)
		return writeFanOutResult(cmd, outcome, listExitCode(outcome.Sources), func() string {
			return humanizeHealthOutcome(outcome)
		})
	}
	return cmd
}

// fanOutHealth asks every backend for its status document, in parallel, and
// assembles the answer in registration order.
func fanOutHealth(ctx context.Context, reg *Registry, backends []string) healthOutcome {
	type probe struct {
		raw json.RawMessage
		err error
	}
	probes := fanOutEach(ctx, reg, backends, func(ctx context.Context, b string) probe {
		config, err := reg.BackendConfig(b)
		if err != nil {
			return probe{err: err}
		}
		resp, err := reg.Invoke(ctx, b, opStatus, nil, config)
		if err != nil {
			return probe{err: err}
		}
		if resp == nil || len(resp.Result) == 0 {
			return probe{err: fmt.Errorf("health: backend %q returned no status document", b)}
		}
		return probe{raw: resp.Result}
	})
	out := healthOutcome{Sources: make([]SourceResult, 0, len(backends)), Backends: []healthBackend{}}
	for i, b := range backends {
		p := probes[i]
		err := p.Err
		if err == nil {
			err = p.Val.err
		}
		switch {
		case err == nil:
			out.Sources = append(out.Sources, SourceResult{Source: b, Status: SourceSucceeded, Count: 1})
			out.Backends = append(out.Backends, healthBackend{Backend: b, Status: p.Val.raw})
		case errors.Is(err, scriptout.ErrUnknownOp):
			out.Sources = append(out.Sources, SourceResult{Source: b, Status: SourceDisabled, Reason: healthNotOfferedReason})
		default:
			out.Sources = append(out.Sources, SourceResult{Source: b, Status: SourceDegraded, Reason: err.Error()})
		}
	}
	return out
}

// humanizeHealthOutcome renders the verb's answer for --output human: the
// sources table, then each answering backend's health and its queries' baseline
// state (the document is decoded only for this rendering).
func humanizeHealthOutcome(o healthOutcome) string {
	var b strings.Builder
	b.WriteString(formatSourcesTable(o.Sources))
	for _, be := range o.Backends {
		var doc struct {
			Health  string `json:"health"`
			Queries []struct {
				Name                   string `json:"name"`
				BaselineAt             string `json:"baseline_at"`
				BaselineOpenAgeSeconds *int64 `json:"baseline_open_age_seconds"`
			} `json:"queries"`
		}
		if err := json.Unmarshal(be.Status, &doc); err != nil {
			fmt.Fprintf(&b, "\n%s: status document not decodable for display", be.Backend)
			continue
		}
		fmt.Fprintf(&b, "\n%s: health %s", be.Backend, doc.Health)
		for _, q := range doc.Queries {
			switch {
			case q.BaselineAt != "":
				fmt.Fprintf(&b, "\n  query %s: baseline at %s", q.Name, q.BaselineAt)
			case q.BaselineOpenAgeSeconds != nil:
				fmt.Fprintf(&b, "\n  query %s: baseline open for %ds", q.Name, *q.BaselineOpenAgeSeconds)
			default:
				fmt.Fprintf(&b, "\n  query %s", q.Name)
			}
		}
	}
	return b.String()
}
