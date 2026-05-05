package ccusage

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
)

// ParseActiveBlock returns the first block with isActive=true, or nil.
func ParseActiveBlock(body []byte) (*Block, error) {
	var r BlocksResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("ccusage: parse blocks: %w", err)
	}
	for i := range r.Blocks {
		if r.Blocks[i].IsActive {
			return &r.Blocks[i], nil
		}
	}
	return nil, nil
}

// Runner invokes `ccusage blocks --active --json --offline` and returns raw stdout.
type Runner struct {
	RunCmd func(ctx context.Context, name string, args ...string) ([]byte, error)
}

func DefaultRun(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}

func (r *Runner) ActiveBlock(ctx context.Context) (*Block, error) {
	run := r.RunCmd
	if run == nil {
		run = DefaultRun
	}
	out, err := run(ctx, "ccusage", "blocks", "--active", "--json", "--offline")
	if err != nil {
		return nil, fmt.Errorf("ccusage: exec: %w", err)
	}
	return ParseActiveBlock(out)
}
