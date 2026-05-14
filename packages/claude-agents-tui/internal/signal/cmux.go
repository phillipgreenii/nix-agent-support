package signal

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"time"
)

// CmuxSignaler sends keys to the cmux surface hosting a process.
// RunCmd and LookupEnv are injectable for tests; nil values fall back to
// exec.CommandContext and os.LookupEnv respectively.
type CmuxSignaler struct {
	RunCmd    func(ctx context.Context, name string, args ...string) ([]byte, error)
	LookupEnv func(key string) (string, bool)
}

// surfaceLoc identifies a cmux surface by its enclosing workspace and surface ref.
type surfaceLoc struct {
	workspaceRef string
	surfaceRef   string
}

// cmuxTopOutput models the subset of `cmux --json top --processes` fields used
// to map a pid to its hosting surface. Fields not consumed here are deliberately
// omitted; encoding/json ignores unknown keys, so any unrelated schema additions
// in cmux are non-breaking.
type cmuxTopOutput struct {
	Windows []struct {
		Workspaces []struct {
			Ref   string `json:"ref"`
			Panes []struct {
				Surfaces []struct {
					Ref            string `json:"ref"`
					Type           string `json:"type"`
					Tty            string `json:"tty"`
					TtyProcessPids []int  `json:"tty_process_pids"`
				} `json:"surfaces"`
			} `json:"panes"`
		} `json:"workspaces"`
	} `json:"windows"`
}

func (c *CmuxSignaler) Name() string { return "cmux" }

func (c *CmuxSignaler) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if c.RunCmd != nil {
		return c.RunCmd(ctx, name, args...)
	}
	return exec.CommandContext(ctx, name, args...).Output()
}

func (c *CmuxSignaler) lookupEnv(key string) (string, bool) {
	if c.LookupEnv != nil {
		return c.LookupEnv(key)
	}
	return os.LookupEnv(key)
}

// Detect returns true when claude-agents-tui is itself running inside cmux.
// Outside cmux the signaler is silently inert.
func (c *CmuxSignaler) Detect(pid int) bool {
	_ = pid // cmux's socket is instance-global; reachability depends on the caller, not the target.
	v, _ := c.lookupEnv("CMUX_WORKSPACE_ID")
	return v != ""
}

// Send injects text followed by Enter into the cmux surface hosting pid.
//
// Steps:
//  1. One shot `cmux --json top --processes` to enumerate every surface.
//  2. Match pid directly into each surface's tty_process_pids.
//  3. cmux send + cmux send-key enter against the matched workspace+surface.
func (c *CmuxSignaler) Send(pid int, text string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	locs, err := c.enumerateSurfaces(ctx)
	if err != nil {
		return fmt.Errorf("cmux enumerate: %w", err)
	}
	loc, ok := locs[pid]
	if !ok {
		return fmt.Errorf("signal: no cmux surface found for pid %d", pid)
	}
	if _, err := c.run(ctx, "cmux", "send", "--workspace", loc.workspaceRef, "--surface", loc.surfaceRef, text); err != nil {
		return fmt.Errorf("cmux send: %w", err)
	}
	if _, err := c.run(ctx, "cmux", "send-key", "--workspace", loc.workspaceRef, "--surface", loc.surfaceRef, "enter"); err != nil {
		return fmt.Errorf("cmux send-key: %w", err)
	}
	return nil
}

// enumerateSurfaces returns a flat map keyed by every pid in any surface's
// tty_process_pids, so a target pid resolves directly to the surface that
// hosts it. Non-terminal surfaces and surfaces without a tty are skipped.
func (c *CmuxSignaler) enumerateSurfaces(ctx context.Context) (map[int]surfaceLoc, error) {
	out, err := c.run(ctx, "cmux", "--json", "top", "--processes")
	if err != nil {
		return nil, fmt.Errorf("cmux --json top --processes: %w", err)
	}
	var parsed cmuxTopOutput
	if err := json.Unmarshal(out, &parsed); err != nil {
		return nil, fmt.Errorf("parse cmux top: %w", err)
	}
	result := map[int]surfaceLoc{}
	for _, w := range parsed.Windows {
		for _, ws := range w.Workspaces {
			for _, p := range ws.Panes {
				for _, s := range p.Surfaces {
					if s.Type != "terminal" || s.Tty == "" {
						continue
					}
					for _, pid := range s.TtyProcessPids {
						result[pid] = surfaceLoc{workspaceRef: ws.Ref, surfaceRef: s.Ref}
					}
				}
			}
		}
	}
	return result, nil
}
