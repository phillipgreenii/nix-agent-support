package beads

import (
	"context"
	"encoding/json"
	"fmt"
)

// Issue is the subset of a bd issue pr-pool reads. Metadata is left as a generic
// map (bd serializes merge-request fields like author/repo/pr_number into it).
type Issue struct {
	ID        string         `json:"id"`
	Title     string         `json:"title"`
	Status    string         `json:"status"`
	Type      string         `json:"issue_type"`
	Parent    string         `json:"parent"`
	Labels    []string       `json:"labels"`
	Metadata  map[string]any `json:"metadata"`
	CreatedBy string         `json:"created_by"` // bd attributes creation to BEADS_ACTOR; lets a dispatch claim only its own new beads
}

// HasLabel reports whether the issue carries the given label.
func (i Issue) HasLabel(label string) bool {
	for _, l := range i.Labels {
		if l == label {
			return true
		}
	}
	return false
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

// List runs `bd list <args...> --json --limit 0` and returns the issues. Used to
// snapshot the store before/after a dispatch; pass `--all` to include closed so a
// bead the worker created and then closed is still seen. A non-array / null
// payload yields an empty slice (mirrors Ready).
func List(ctx context.Context, r Runner, args ...string) ([]Issue, error) {
	full := append(append([]string{"list"}, args...), "--json", "--limit", "0")
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

// AddLabel adds an arbitrary label: `bd update <id> --add-label <label>`.
func AddLabel(ctx context.Context, r Runner, id, label string) error {
	_, err := r.Run(ctx, "update", id, "--add-label", label)
	if err != nil {
		return fmt.Errorf("add-label %s %s: %w", id, label, err)
	}
	return nil
}

// RemoveLabel removes an arbitrary label: `bd update <id> --remove-label <label>`.
// Removing a label the bead does not carry is a no-op on bd's side.
func RemoveLabel(ctx context.Context, r Runner, id, label string) error {
	_, err := r.Run(ctx, "update", id, "--remove-label", label)
	if err != nil {
		return fmt.Errorf("remove-label %s %s: %w", id, label, err)
	}
	return nil
}

// HasLabel reads the bead via `bd show <id> --json` and reports whether it
// carries the given label.
func HasLabel(ctx context.Context, r Runner, id, label string) (bool, error) {
	iss, err := ShowObj(ctx, r, id)
	if err != nil {
		return false, err
	}
	return iss.HasLabel(label), nil
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

// Comment adds a comment to a bead: `bd comment <id> <text>`.
func Comment(ctx context.Context, r Runner, id, text string) error {
	_, err := r.Run(ctx, "comment", id, text)
	if err != nil {
		return fmt.Errorf("comment %s: %w", id, err)
	}
	return nil
}

func decodeMany(b []byte) []Issue {
	var arr []Issue
	if err := json.Unmarshal(b, &arr); err == nil {
		return arr
	}
	return nil
}
