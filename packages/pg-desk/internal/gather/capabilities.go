package gather

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/client"
)

// CapQuery is one query's entry of a backend's capabilities answer: its
// watched value (false for a query that declares nothing, never absent).
type CapQuery struct {
	Watched bool
}

// CapBackend is one backends[] row of a capabilities answer: the backend's
// registered name, its vocabulary (for example owns_changes) and its
// configured queries keyed by name.
type CapBackend struct {
	Backend    string
	Vocabulary map[string]any
	Queries    map[string]CapQuery
}

// OwnsChanges reports the vocabulary's owns_changes declaration (false when
// absent or not a boolean).
func (b CapBackend) OwnsChanges() bool {
	v, _ := b.Vocabulary["owns_changes"].(bool)
	return v
}

// CapSource is one sources[] row of a capabilities answer; a backend whose
// capabilities call failed has a degraded row here and no CapBackend.
type CapSource struct {
	Source string
	Status string
	Reason string
}

// CapabilitiesAnswer is what one Capabilities call returns.
type CapabilitiesAnswer struct {
	Sources  []CapSource
	Backends []CapBackend
}

// Backend returns the row naming backend.
func (a CapabilitiesAnswer) Backend(backend string) (CapBackend, bool) {
	for _, b := range a.Backends {
		if b.Backend == backend {
			return b, true
		}
	}
	return CapBackend{}, false
}

// Reason returns the reason of backend's sources[] row ("" when it has none).
func (a CapabilitiesAnswer) Reason(backend string) string {
	for _, s := range a.Sources {
		if s.Source == backend && s.Reason != "" {
			return s.Reason
		}
	}
	return ""
}

// Capabilities runs `pg-connector <type> capabilities [--backend <backend>]
// --output json` and decodes the umbrella's caller-facing capabilities
// answer. An empty backend passes no --backend. Exit 0, 5 (degraded) and 3 (unavailable) with a
// parseable document are answers (a backend whose call failed is a degraded
// source and has no backends row); an unparseable answer, any other exit, or
// the wire error unavailable (wrapping ErrUnavailable) is an error.
func (g *Gatherer) Capabilities(ctx context.Context, entityType, backend string) (CapabilitiesAnswer, error) {
	if entityType == "" {
		return CapabilitiesAnswer{}, fmt.Errorf("gather: capabilities: type is required")
	}
	args := []string{entityType, "capabilities"}
	if backend != "" {
		args = append(args, "--backend", backend)
	}
	args = append(args, "--output", "json")
	stdout, err := g.fanOutDocStdout(ctx, args, g.feedEnv(entityType))
	if err != nil {
		return CapabilitiesAnswer{}, err
	}
	if wireErrorCode(stdout) == "unavailable" {
		return CapabilitiesAnswer{}, exitError(args, client.ExitOK, stdout)
	}
	var wire struct {
		client.FanOutOutcome
		Backends []struct {
			Backend    string         `json:"backend"`
			Vocabulary map[string]any `json:"vocabulary"`
			Queries    map[string]struct {
				Watched bool `json:"watched"`
			} `json:"queries"`
		} `json:"backends"`
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(stdout, &wire); err != nil {
		return CapabilitiesAnswer{}, fmt.Errorf("decode pg-connector %v stdout: %w", args, err)
	}
	if hasWireError(wire.Error) {
		return CapabilitiesAnswer{}, exitError(args, client.ExitOK, stdout)
	}
	out := CapabilitiesAnswer{}
	for _, s := range wire.Sources {
		out.Sources = append(out.Sources, CapSource{Source: s.Source, Status: string(s.Status), Reason: s.Reason})
	}
	for _, b := range wire.Backends {
		cb := CapBackend{Backend: b.Backend, Vocabulary: b.Vocabulary, Queries: map[string]CapQuery{}}
		for name, q := range b.Queries {
			cb.Queries[name] = CapQuery{Watched: q.Watched}
		}
		out.Backends = append(out.Backends, cb)
	}
	return out, nil
}

// fanOutDocStdout runs a fan-out verb that answers with a document even on total failure through the typed client and
// returns its stdout on exit 0 or 5, and also on exit 3 when the stdout is a
// plain document rather than a wire error envelope (a backend whose call
// failed is a degraded sources[] row there). Any other exit is an error.
func (g *Gatherer) fanOutDocStdout(ctx context.Context, args []string, env []string) ([]byte, error) {
	c := client.Client{Run: g.clientRunner, Env: env, Timeout: callTimeout}
	raw, err := c.FanOutStdout(ctx, args...)
	if err == nil {
		return raw, nil
	}
	var ce *client.Error
	if errors.As(err, &ce) && ce.ExitCode == client.ExitUnavailable && ce.Cause == nil && ce.Code == "" && json.Valid([]byte(ce.Message)) {
		return []byte(ce.Message), nil
	}
	return nil, g.callError(args, err)
}
