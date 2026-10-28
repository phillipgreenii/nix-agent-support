package gather

import (
	"context"
	"encoding/json"
	"fmt"
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
// answer. An empty backend passes no --backend. Exit 0, 2 and 3 with a
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
	res, err := g.run(ctx, args, g.feedEnv(entityType))
	if err != nil {
		return CapabilitiesAnswer{}, err
	}
	switch res.exitCode {
	case 0, 2, 3:
	default:
		return CapabilitiesAnswer{}, exitError(args, res.exitCode, res.stdout)
	}
	if wireErrorCode(res.stdout) == "unavailable" {
		return CapabilitiesAnswer{}, exitError(args, res.exitCode, res.stdout)
	}
	var wire struct {
		Sources []struct {
			Source string `json:"source"`
			Status string `json:"status"`
			Reason string `json:"reason"`
		} `json:"sources"`
		Backends []struct {
			Backend    string         `json:"backend"`
			Vocabulary map[string]any `json:"vocabulary"`
			Queries    map[string]struct {
				Watched bool `json:"watched"`
			} `json:"queries"`
		} `json:"backends"`
		Error *wireError `json:"error"`
	}
	if err := json.Unmarshal(res.stdout, &wire); err != nil {
		return CapabilitiesAnswer{}, fmt.Errorf("decode pg-connector %v stdout: %w", args, err)
	}
	if wire.Error != nil {
		return CapabilitiesAnswer{}, exitError(args, res.exitCode, res.stdout)
	}
	out := CapabilitiesAnswer{}
	for _, s := range wire.Sources {
		out.Sources = append(out.Sources, CapSource{Source: s.Source, Status: s.Status, Reason: s.Reason})
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
