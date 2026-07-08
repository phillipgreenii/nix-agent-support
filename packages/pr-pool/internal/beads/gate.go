package beads

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Gate is the subset of a bd gate pr-pool reads. bd `gate list` returns only
// OPEN (unresolved) gates, so a gate absent from a ListGates result is either
// resolved or was never created.
type Gate struct {
	ID        string `json:"id"`
	IssueType string `json:"issue_type"`
	AwaitType string `json:"await_type"`
	AwaitID   string `json:"await_id"`
}

// ListGates runs `bd gate list --limit 0 --json` and returns the OPEN gates.
func ListGates(ctx context.Context, r Runner) ([]Gate, error) {
	out, err := r.Run(ctx, "gate", "list", "--limit", "0", "--json")
	if err != nil {
		return nil, fmt.Errorf("gate list: %w", err)
	}
	b := unwrapData([]byte(out))
	// Tolerate an empty / null payload (no open gates) the same way decodeMany
	// does, rather than surfacing "unexpected end of JSON input".
	if len(b) == 0 || string(b) == "null" {
		return nil, nil
	}
	var gs []Gate
	if err := json.Unmarshal(b, &gs); err != nil {
		return nil, fmt.Errorf("decode gates: %w", err)
	}
	return gs, nil
}

// CreateGate creates an OPEN gate that blocks the `blocks` bead from `bd ready`
// until it is resolved. awaitType is the custom gate type (e.g.
// "pg-pr:active-pr") and awaitID identifies the fact the gate awaits (e.g.
// "owner/repo#7"). Custom pg-pr:* types have no auto-resolver, so the ACL must
// resolve them from facts each pass. Returns the new gate id.
func CreateGate(ctx context.Context, r Runner, blocks, awaitType, awaitID, reason string) (string, error) {
	args := []string{
		"gate", "create",
		"--type=" + awaitType,
		"--blocks", blocks,
		"--await-id", awaitID,
	}
	if reason != "" {
		args = append(args, "--reason", reason)
	}
	args = append(args, "--json")
	out, err := r.Run(ctx, args...)
	if err != nil {
		return "", fmt.Errorf("gate create %s for %s: %w", awaitType, blocks, err)
	}
	b := unwrapData([]byte(out))
	var g struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(b, &g); err != nil {
		return "", fmt.Errorf("decode created gate: %w", err)
	}
	if g.ID == "" {
		// Fall back to the raw trimmed output for bd builds that print a bare id.
		if id := strings.TrimSpace(out); id != "" {
			return id, nil
		}
		return "", fmt.Errorf("gate create returned no id (out=%q)", out)
	}
	return g.ID, nil
}

// ResolveGate resolves (clears) an open gate by id, unblocking its bead.
func ResolveGate(ctx context.Context, r Runner, id, reason string) error {
	args := []string{"gate", "resolve", id}
	if reason != "" {
		args = append(args, "--reason", reason)
	}
	if _, err := r.Run(ctx, args...); err != nil {
		return fmt.Errorf("gate resolve %s: %w", id, err)
	}
	return nil
}

// FindOpenGate returns the first open gate matching awaitType+awaitID, or nil.
// (ListGates returns only open gates, so a match means the gate still blocks.)
func FindOpenGate(gates []Gate, awaitType, awaitID string) *Gate {
	for i := range gates {
		if gates[i].AwaitType == awaitType && gates[i].AwaitID == awaitID {
			return &gates[i]
		}
	}
	return nil
}
