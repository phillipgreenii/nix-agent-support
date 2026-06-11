package beads

import (
	"context"
	"encoding/json"
	"fmt"
)

// Issue is the subset of a bd issue pr-pool reads. Metadata is left as a generic
// map (bd serializes merge-request fields like author/repo/pr_number into it).
type Issue struct {
	ID       string         `json:"id"`
	Title    string         `json:"title"`
	Status   string         `json:"status"`
	Type     string         `json:"issue_type"`
	Parent   string         `json:"parent"`
	Metadata map[string]any `json:"metadata"`
}

// ShowObj runs `bd show <id> --json` and normalizes bd's array-or-object output
// (mirrors the bash bd_obj helper).
func ShowObj(ctx context.Context, r Runner, id string) (Issue, error) {
	out, err := r.Run(ctx, "show", id, "--json")
	if err != nil {
		return Issue{}, err
	}
	return decodeOne([]byte(out))
}

// Ready runs `bd ready <args...> --json --limit 0` and returns the issues.
// A non-array / null payload yields an empty slice (mirrors the bash
// `if type=="array" then . else []`).
func Ready(ctx context.Context, r Runner, args ...string) ([]Issue, error) {
	full := append(append([]string{"ready"}, args...), "--json", "--limit", "0")
	out, err := r.Run(ctx, full...)
	if err != nil {
		return nil, err
	}
	return decodeMany([]byte(out)), nil
}

// Status returns the issue's current status ("" if unset).
func Status(ctx context.Context, r Runner, id string) (string, error) {
	iss, err := ShowObj(ctx, r, id)
	if err != nil {
		return "", err
	}
	return iss.Status, nil
}

// Unclaim returns a bead to the open pool: `bd update <id> --status=open --assignee=`.
func Unclaim(ctx context.Context, r Runner, id string) error {
	_, err := r.Run(ctx, "update", id, "--status=open", "--assignee=")
	if err != nil {
		return fmt.Errorf("unclaim %s: %w", id, err)
	}
	return nil
}

// AddHuman flags a bead for a human: `bd update <id> --add-label human`.
func AddHuman(ctx context.Context, r Runner, id string) error {
	_, err := r.Run(ctx, "update", id, "--add-label", "human")
	if err != nil {
		return fmt.Errorf("add-human %s: %w", id, err)
	}
	return nil
}

func decodeOne(b []byte) (Issue, error) {
	var arr []Issue
	if err := json.Unmarshal(b, &arr); err == nil {
		if len(arr) > 0 {
			return arr[0], nil
		}
		return Issue{}, nil
	}
	var one Issue
	if err := json.Unmarshal(b, &one); err != nil {
		return Issue{}, fmt.Errorf("decode issue: %w", err)
	}
	return one, nil
}

func decodeMany(b []byte) []Issue {
	var arr []Issue
	if err := json.Unmarshal(b, &arr); err == nil {
		return arr
	}
	return nil
}
