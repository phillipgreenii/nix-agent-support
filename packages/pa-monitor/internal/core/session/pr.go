package session

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
)

type PRInfo struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	// State is the PR state as gh reports it: OPEN, CLOSED, or MERGED. Threaded
	// through the gRPC proto (Directory.pr_info.state) so remote clients (TUI)
	// can color the PR link by state without a second lookup.
	State string `json:"state"`
	URL   string `json:"url"`
}

// LookupPR calls `gh pr view --head <branch> --json number,title,state,url` in cwd.
// Returns (PRInfo, true, nil) when a PR is found, (PRInfo{}, false, nil) when
// gh exits non-zero (no PR), and (PRInfo{}, false, err) only on unexpected failures.
func LookupPR(ctx context.Context, cwd, branch string) (PRInfo, bool, error) {
	cmd := exec.CommandContext(ctx, "gh", "pr", "view", "--head", branch, "--json", "number,title,state,url")
	cmd.Dir = cwd
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return PRInfo{}, false, nil // gh exited non-zero: no PR found
		}
		return PRInfo{}, false, err // unexpected failure: surface it
	}
	var info PRInfo
	if err := json.Unmarshal(out, &info); err != nil {
		return PRInfo{}, false, err
	}
	return info, true, nil
}
