// Package bd is a thin client over the `bd` CLI for pn:applied gates. It always
// sets BD_JSON_ENVELOPE=1 (pb pins the envelope rather than relying on the
// ambient default, which flips in bd v2.0) and parses the {data, schema_version}
// envelope. DB targeting is via `bd -C <dir>` so gates resolve in their own DB.
package bd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/phillipgreenii/pb/internal/run"
)

type Gate struct {
	ID        string            `json:"id"`
	IssueType string            `json:"issue_type"`
	AwaitType string            `json:"await_type"`
	AwaitID   string            `json:"await_id"`
	CreatedAt string            `json:"created_at"` // RFC3339; used for stale-age (check)
	Metadata  map[string]string `json:"metadata"`
}

type Client struct {
	R run.Runner
}

func bdEnv() []string {
	return append(os.Environ(), "BD_JSON_ENVELOPE=1")
}

type listEnvelope struct {
	Data []Gate `json:"data"`
}

type createEnvelope struct {
	Data struct {
		ID string `json:"id"`
	} `json:"data"`
}

// ListGates returns all open gates in the DB at dir.
func (c Client) ListGates(ctx context.Context, dir string) ([]Gate, error) {
	res, err := c.R.Run(ctx, "bd",
		[]string{"-C", dir, "gate", "list", "--limit", "0", "--json"},
		run.Options{Env: bdEnv()})
	if err != nil {
		return nil, fmt.Errorf("bd gate list in %q: %w", dir, err)
	}
	var env listEnvelope
	if err := json.Unmarshal([]byte(res.Stdout), &env); err != nil {
		return nil, fmt.Errorf("parse gate list json: %w", err)
	}
	return env.Data, nil
}

// CreateGate creates a gate of awaitType blocking `blocks`, returning the gate id.
func (c Client) CreateGate(ctx context.Context, dir, blocks, awaitType, awaitID, reason string) (string, error) {
	args := []string{"-C", dir, "gate", "create", "--type=" + awaitType, "--blocks", blocks, "--await-id", awaitID}
	if reason != "" {
		args = append(args, "--reason", reason)
	}
	args = append(args, "--json")
	res, err := c.R.Run(ctx, "bd", args, run.Options{Env: bdEnv()})
	if err != nil {
		return "", fmt.Errorf("bd gate create: %w", err)
	}
	var env createEnvelope
	if err := json.Unmarshal([]byte(res.Stdout), &env); err != nil {
		return "", fmt.Errorf("parse gate create json: %w", err)
	}
	if env.Data.ID == "" {
		return "", fmt.Errorf("bd gate create returned no id: %s", res.Stdout)
	}
	return env.Data.ID, nil
}

// SetMetadata sets metadata.<key>=<value> on issue id.
func (c Client) SetMetadata(ctx context.Context, dir, id, key, value string) error {
	_, err := c.R.Run(ctx, "bd",
		[]string{"-C", dir, "update", id, "--set-metadata", key + "=" + value},
		run.Options{Env: bdEnv()})
	if err != nil {
		return fmt.Errorf("bd update --set-metadata: %w", err)
	}
	return nil
}

// ResolveGate closes (resolves) gate id.
func (c Client) ResolveGate(ctx context.Context, dir, id, reason string) error {
	args := []string{"-C", dir, "gate", "resolve", id}
	if reason != "" {
		args = append(args, "--reason", reason)
	}
	_, err := c.R.Run(ctx, "bd", args, run.Options{Env: bdEnv()})
	if err != nil {
		return fmt.Errorf("bd gate resolve %s: %w", id, err)
	}
	return nil
}

// HasBead reports whether a bead with id exists in the DB at dir (used by gate
// create to co-locate the gate in the bead's OWN DB).
func (c Client) HasBead(ctx context.Context, dir, id string) bool {
	_, err := c.R.Run(ctx, "bd", []string{"-C", dir, "show", id, "--json"}, run.Options{Env: bdEnv()})
	return err == nil
}

// AddLabel adds a label to issue id (convert-to-human stale action: label "human"
// → surfaces in `bd human list`).
func (c Client) AddLabel(ctx context.Context, dir, id, label string) error {
	_, err := c.R.Run(ctx, "bd",
		[]string{"-C", dir, "update", id, "--add-label", label},
		run.Options{Env: bdEnv()})
	if err != nil {
		return fmt.Errorf("bd update --add-label: %w", err)
	}
	return nil
}
