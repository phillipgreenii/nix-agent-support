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

// beadCreateEnvelope tolerates both envelope shapes bd has emitted for
// `create --json`: {"data":{"id":...}} and {"data":[{"id":...}]}.
type beadCreateEnvelope struct {
	Data json.RawMessage `json:"data"`
}

// readyEnvelope's Data is a POINTER so a missing or null `data` key is
// distinguishable from a legitimately empty queue: presence of the key is the
// positive control the prose procedure implemented as "non-empty bd ready".
type readyEnvelope struct {
	Data *[]struct {
		ID string `json:"id"`
	} `json:"data"`
}

// CreateBead creates a bead titled title (born deferred until deferUntil when
// non-empty, with deps such as "discovered-from:<id>") and returns the new id.
func (c Client) CreateBead(ctx context.Context, dir, title, deferUntil, deps, actor string) (string, error) {
	args := []string{"-C", dir, "create", title}
	if deferUntil != "" {
		args = append(args, "--defer", deferUntil)
	}
	if deps != "" {
		args = append(args, "--deps", deps)
	}
	args = append(args, "--actor", actor, "--json")
	res, err := c.R.Run(ctx, "bd", args, run.Options{Env: bdEnv()})
	if err != nil {
		return "", fmt.Errorf("bd create: %w", err)
	}
	return parseCreatedBeadID(res.Stdout)
}

func parseCreatedBeadID(out string) (string, error) {
	var env beadCreateEnvelope
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		return "", fmt.Errorf("parse bd create json: %w", err)
	}
	var obj struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(env.Data, &obj); err == nil && obj.ID != "" {
		return obj.ID, nil
	}
	var arr []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(env.Data, &arr); err == nil && len(arr) == 1 && arr[0].ID != "" {
		return arr[0].ID, nil
	}
	return "", fmt.Errorf("bd create returned no id: %s", out)
}

// ReadyIDs returns the ids of ALL ready beads in the DB at dir. -n 0 is
// load-bearing: bd ready caps its rows by default, and a capped absence check
// proves nothing.
func (c Client) ReadyIDs(ctx context.Context, dir string) ([]string, error) {
	res, err := c.R.Run(ctx, "bd", []string{"-C", dir, "ready", "--json", "-n", "0"},
		run.Options{Env: bdEnv()})
	if err != nil {
		return nil, fmt.Errorf("bd ready in %q: %w", dir, err)
	}
	var env readyEnvelope
	if err := json.Unmarshal([]byte(res.Stdout), &env); err != nil {
		return nil, fmt.Errorf("parse bd ready json: %w", err)
	}
	if env.Data == nil {
		return nil, fmt.Errorf("bd ready returned no data envelope (positive control failed): %s", res.Stdout)
	}
	ids := make([]string, 0, len(*env.Data))
	for _, d := range *env.Data {
		ids = append(ids, d.ID)
	}
	return ids, nil
}

// UpdateDefer sets (or, with deferUntil == "", clears) the defer on issue id.
func (c Client) UpdateDefer(ctx context.Context, dir, id, deferUntil, actor string) error {
	_, err := c.R.Run(ctx, "bd",
		[]string{"-C", dir, "update", id, "--defer", deferUntil, "--actor", actor},
		run.Options{Env: bdEnv()})
	if err != nil {
		return fmt.Errorf("bd update --defer: %w", err)
	}
	return nil
}

// Comment appends a comment to issue id.
func (c Client) Comment(ctx context.Context, dir, id, text, actor string) error {
	_, err := c.R.Run(ctx, "bd",
		[]string{"-C", dir, "comment", id, text, "--actor", actor},
		run.Options{Env: bdEnv()})
	if err != nil {
		return fmt.Errorf("bd comment: %w", err)
	}
	return nil
}
