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
	"strings"
	"time"
	"unicode/utf8"

	"github.com/phillipgreenii/pb/internal/run"
	"github.com/phillipgreenii/pb/internal/unstick"
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

// MaxTitleLen is bd 1.3.1's cap on a bead title ("title must be 500 characters
// or less (got N)").
const MaxTitleLen = 500

// ValidateTitle rejects a title bd would refuse, so the caller fails BEFORE any
// bd call. Length is counted in RUNES: bd's own unit (bytes vs runes) was not
// verified, and if bd counts bytes a multibyte title near the cap still fails
// at bd, whose message wrapErr now surfaces.
func ValidateTitle(title string) error {
	if n := utf8.RuneCountInString(title); n > MaxTitleLen {
		return fmt.Errorf("title is %d characters; bd allows at most %d. Keep the title a short summary and put the long text in a bd comment --file or the description", n, MaxTitleLen)
	}
	return nil
}

// wrapErr wraps a failed bd call as "<op>: <runner error>" and guarantees bd's
// own reason is present: under --json bd reports errors on STDOUT, which an
// stderr-only runner error drops (pg2-cjakt). It appends run.Detail unless the
// runner error already carries it.
func wrapErr(op string, res run.Result, err error) error {
	d := run.Detail("bd", res)
	if strings.Contains(err.Error(), d) {
		return fmt.Errorf("%s: %w", op, err)
	}
	return fmt.Errorf("%s: %w [bd: %s]", op, err, d)
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
		return nil, wrapErr(fmt.Sprintf("bd gate list in %q", dir), res, err)
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
		return "", wrapErr("bd gate create", res, err)
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
	res, err := c.R.Run(ctx, "bd",
		[]string{"-C", dir, "update", id, "--set-metadata", key + "=" + value},
		run.Options{Env: bdEnv()})
	if err != nil {
		return wrapErr("bd update --set-metadata", res, err)
	}
	return nil
}

// SetAwaitID rewrites gate id's await_id (the "<wsid>:<repo>:<patch-id>" gate key).
// Used by Check's raw-SHA repair (tc-htcum): a gate created OUTSIDE `pb gate
// create` may have been pinned to a raw commit SHA instead of a patch-id, and a
// raw SHA is one rebase away from becoming permanently unreachable. Once Check
// recovers the patch-id for such a SHA, it rewrites the gate in place so it is
// immune to the SHA being rewritten or pruned later.
func (c Client) SetAwaitID(ctx context.Context, dir, id, awaitID string) error {
	res, err := c.R.Run(ctx, "bd",
		[]string{"-C", dir, "update", id, "--await-id", awaitID},
		run.Options{Env: bdEnv()})
	if err != nil {
		return wrapErr("bd update --await-id", res, err)
	}
	return nil
}

// ResolveGate closes (resolves) gate id.
func (c Client) ResolveGate(ctx context.Context, dir, id, reason string) error {
	args := []string{"-C", dir, "gate", "resolve", id}
	if reason != "" {
		args = append(args, "--reason", reason)
	}
	res, err := c.R.Run(ctx, "bd", args, run.Options{Env: bdEnv()})
	if err != nil {
		return wrapErr(fmt.Sprintf("bd gate resolve %s", id), res, err)
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
	res, err := c.R.Run(ctx, "bd",
		[]string{"-C", dir, "update", id, "--add-label", label},
		run.Options{Env: bdEnv()})
	if err != nil {
		return wrapErr("bd update --add-label", res, err)
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
	if err := ValidateTitle(title); err != nil {
		return "", err
	}
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
		return "", wrapErr("bd create", res, err)
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
		return nil, wrapErr(fmt.Sprintf("bd ready in %q", dir), res, err)
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
	res, err := c.R.Run(ctx, "bd",
		[]string{"-C", dir, "update", id, "--defer", deferUntil, "--actor", actor},
		run.Options{Env: bdEnv()})
	if err != nil {
		return wrapErr("bd update --defer", res, err)
	}
	return nil
}

// Comment appends a comment to issue id.
func (c Client) Comment(ctx context.Context, dir, id, text, actor string) error {
	res, err := c.R.Run(ctx, "bd",
		[]string{"-C", dir, "comment", id, text, "--actor", actor},
		run.Options{Env: bdEnv()})
	if err != nil {
		return wrapErr("bd comment", res, err)
	}
	return nil
}

// Timeouts for the whole-workspace reads used by `pb unstick` (L-1: a hung bd
// must not hang the sweep). Export dumps every bead, hence the larger bound.
const (
	ExportTimeout = 5 * time.Minute
	ReadyTimeout  = 2 * time.Minute
)

// Export writes every bead in the DB at dir to outPath as JSONL via
// `bd -C dir export -o outPath`. -o is always used (never stdout capture), so
// a large workspace is not held in memory twice.
func (c Client) Export(ctx context.Context, dir, outPath string) error {
	res, err := c.R.Run(ctx, "bd", []string{"-C", dir, "export", "-o", outPath},
		run.Options{Env: bdEnv(), Timeout: ExportTimeout})
	if err != nil {
		return wrapErr(fmt.Sprintf("bd export in %q", dir), res, err)
	}
	return nil
}

// Ready returns ALL ready beads in the DB at dir (-n 0 is load-bearing, as in
// ReadyIDs) decoded for the sweep, plus bd's raw stdout so the caller can
// persist it as ready.json. The decode tolerates a bare array in place of the
// {data, schema_version} envelope.
func (c Client) Ready(ctx context.Context, dir string) ([]unstick.ReadyRow, []byte, error) {
	res, err := c.R.Run(ctx, "bd", []string{"-C", dir, "ready", "-n", "0", "--json"},
		run.Options{Env: bdEnv(), Timeout: ReadyTimeout})
	if err != nil {
		return nil, nil, wrapErr(fmt.Sprintf("bd ready in %q", dir), res, err)
	}
	rows, err := unstick.ParseReady([]byte(res.Stdout))
	if err != nil {
		return nil, nil, err
	}
	return rows, []byte(res.Stdout), nil
}
