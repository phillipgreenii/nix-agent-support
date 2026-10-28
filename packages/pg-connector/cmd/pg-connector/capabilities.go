// capabilities.go: the "pg-connector <type> capabilities" verb
// (INTF-WIRE-CAPS-1, INTF-WIRE-CAPS-2), the caller-facing surface over the
// backend capabilities op the umbrella otherwise sends only internally.
//
// It is a fan-out READ like list: every backend registered for the type is
// asked once (--backend pins it to one), the exit codes follow the fan-out
// scheme (0 all succeeded, 2 partial, 3 total failure), and nothing is written
// to the ledger or the cache. Each succeeded backend contributes one backends[]
// entry carrying exactly what it answered (protocolVersion, schemaVersions, ops
// and vocabulary are never re-encoded) plus queries: one member for EVERY query
// configured for that backend, {"watched": <bool>}. The members a backend
// declares under vocabulary.queries are copied across as given; a configured
// query the backend did not list reads the watched value of its own configured
// definition (false unless it carries watched = true). A backend whose
// capabilities call failed contributes a degraded sources[] row with its reason
// and NO backends entry. The op is answered without a daemon (INTF-WIRE-CAPS-2),
// so the verb makes exactly one capabilities request per backend and no other.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// capabilitiesBackend is one succeeded backend's entry of the verb's answer.
type capabilitiesBackend struct {
	Backend         string         `json:"backend"`
	ProtocolVersion int            `json:"protocolVersion"`
	SchemaVersions  map[string]int `json:"schemaVersions"`
	Ops             []string       `json:"ops"`
	Vocabulary      map[string]any `json:"vocabulary,omitempty"`
	Queries         map[string]any `json:"queries"`
}

// capabilitiesOutcome is the verb's whole JSON answer.
type capabilitiesOutcome struct {
	Sources  []SourceResult        `json:"sources"`
	Backends []capabilitiesBackend `json:"backends"`
}

// newCapabilitiesCmd builds the "capabilities" verb for entityType; every type
// that fans out registers it next to its other fan-out verbs.
func newCapabilitiesCmd(entityType string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "capabilities",
		Short: fmt.Sprintf("Report each %s backend's capabilities, with every configured query's watched value; --backend pins one backend", entityType),
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
			return report(scriptout.WrapError(scriptout.ErrInvalidArgument, fmt.Sprintf("capabilities: no backend registered for connector.%s", entityType)))
		}
		outcome := fanOutCapabilities(cmd.Context(), reg, backends)
		return writeFanOutResult(cmd, outcome, listExitCode(outcome.Sources), func() string {
			return humanizeCapabilitiesOutcome(outcome)
		})
	}
	return cmd
}

// fanOutCapabilities asks every backend for its capabilities, in parallel, and
// assembles the answer in registration order.
func fanOutCapabilities(ctx context.Context, reg *Registry, backends []string) capabilitiesOutcome {
	type probe struct {
		resp *scriptout.CapabilitiesResponse
		err  error
	}
	probes := fanOutEach(ctx, reg, backends, func(ctx context.Context, b string) probe {
		resp, err := reg.InvokeCapabilities(ctx, b)
		return probe{resp: resp, err: err}
	})
	out := capabilitiesOutcome{Sources: make([]SourceResult, 0, len(backends)), Backends: []capabilitiesBackend{}}
	for i, b := range backends {
		p := probes[i]
		err := p.Err
		if err == nil {
			err = p.Val.err
		}
		if err == nil && p.Val.resp == nil {
			err = fmt.Errorf("capabilities: backend %q returned no answer", b)
		}
		if err != nil {
			out.Sources = append(out.Sources, SourceResult{Source: b, Status: SourceDegraded, Reason: err.Error()})
			continue
		}
		queries, qErr := capabilitiesQueries(reg, b, p.Val.resp)
		if qErr != nil {
			out.Sources = append(out.Sources, SourceResult{Source: b, Status: SourceDegraded, Reason: qErr.Error()})
			continue
		}
		resp := p.Val.resp
		out.Sources = append(out.Sources, SourceResult{Source: b, Status: SourceSucceeded, Count: 1})
		out.Backends = append(out.Backends, capabilitiesBackend{
			Backend:         b,
			ProtocolVersion: resp.ProtocolVersion,
			SchemaVersions:  resp.SchemaVersions,
			Ops:             resp.Ops,
			Vocabulary:      resp.Vocabulary,
			Queries:         queries,
		})
	}
	return out
}

// capabilitiesQueries builds a backend's queries member: every member of the
// backend's own vocabulary.queries as given (a backend's own declaration
// wins), then {"watched": <configured watched>} for each configured query (the
// names under backends.<name>.queries) it did not list. The backend cannot see
// its configuration on a capabilities call, so the umbrella fills the value
// from the backends.<name>.queries definition it holds (a bare string, a list
// or an object without watched reads false).
func capabilitiesQueries(reg *Registry, backend string, resp *scriptout.CapabilitiesResponse) (map[string]any, error) {
	queries := map[string]any{}
	if declared, ok := resp.Vocabulary["queries"].(map[string]any); ok {
		for name, v := range declared {
			queries[name] = v
		}
	}
	names, err := reg.BackendQueryNames(backend)
	if err != nil {
		return nil, err
	}
	config, err := reg.BackendConfig(backend)
	if err != nil {
		return nil, err
	}
	for _, name := range names {
		if _, ok := queries[name]; ok {
			continue
		}
		def, _ := schema.ResolveQueryDef(config, name)
		queries[name] = map[string]any{"watched": def.Watched}
	}
	return queries, nil
}

// humanizeCapabilitiesOutcome renders the verb's answer for --output human.
func humanizeCapabilitiesOutcome(o capabilitiesOutcome) string {
	var b strings.Builder
	b.WriteString(formatSourcesTable(o.Sources))
	for _, be := range o.Backends {
		fmt.Fprintf(&b, "\n%s: protocol %d, ops %s", be.Backend, be.ProtocolVersion, strings.Join(be.Ops, ","))
		names := make([]string, 0, len(be.Queries))
		for name := range be.Queries {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			watched := false
			if m, ok := be.Queries[name].(map[string]any); ok {
				watched, _ = m["watched"].(bool)
			}
			fmt.Fprintf(&b, "\n  query %s: watched=%t", name, watched)
		}
	}
	return b.String()
}
