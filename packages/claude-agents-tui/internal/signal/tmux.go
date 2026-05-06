package signal

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// TmuxSignaler sends keys to the tmux pane hosting a process.
// RunCmd is injectable for tests; nil defaults to exec.CommandContext.
type TmuxSignaler struct {
	RunCmd func(ctx context.Context, name string, args ...string) ([]byte, error)
}

func (t *TmuxSignaler) Name() string { return "tmux" }

func (t *TmuxSignaler) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if t.RunCmd != nil {
		return t.RunCmd(ctx, name, args...)
	}
	return exec.CommandContext(ctx, name, args...).Output()
}

// Detect returns true if any ancestor process of pid is named "tmux".
func (t *TmuxSignaler) Detect(pid int) bool {
	seen := map[int]bool{}
	for {
		if pid < 1 || seen[pid] {
			return false
		}
		seen[pid] = true
		out, err := t.run(context.Background(), "ps", "-o", "ppid=,comm=", "-p", strconv.Itoa(pid))
		if err != nil {
			return false
		}
		fields := strings.Fields(strings.TrimSpace(string(out)))
		if len(fields) < 2 {
			return false
		}
		if strings.HasPrefix(fields[1], "tmux") {
			return true
		}
		ppid, err := strconv.Atoi(fields[0])
		if err != nil || ppid <= 1 {
			return false
		}
		pid = ppid
	}
}

// Send injects text + Enter into the tmux pane that contains pid.
func (t *TmuxSignaler) Send(pid int, text string) error {
	ctx := context.Background()
	out, err := t.run(ctx, "tmux", "list-panes", "-a", "-F",
		"#{pane_pid} #{session_name}:#{window_index}.#{pane_index}")
	if err != nil {
		return fmt.Errorf("tmux list-panes: %w", err)
	}
	paneID := t.findPaneForPID(string(out), pid)
	if paneID == "" {
		return fmt.Errorf("signal: no tmux pane found for pid %d", pid)
	}
	_, err = t.run(ctx, "tmux", "send-keys", "-t", paneID, text, "Enter")
	return err
}

// findPaneForPID walks up the process tree from targetPID until it finds a pid
// that matches a tmux pane's shell pid from listOutput.
func (t *TmuxSignaler) findPaneForPID(listOutput string, targetPID int) string {
	panePIDs := map[int]string{}
	for _, line := range strings.Split(strings.TrimSpace(listOutput), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		ppid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		panePIDs[ppid] = fields[1]
	}
	seen := map[int]bool{}
	pid := targetPID
	for {
		if pid < 1 || seen[pid] {
			return ""
		}
		seen[pid] = true
		if paneID, ok := panePIDs[pid]; ok {
			return paneID
		}
		out, err := t.run(context.Background(), "ps", "-o", "ppid=,comm=", "-p", strconv.Itoa(pid))
		if err != nil {
			return ""
		}
		fields := strings.Fields(strings.TrimSpace(string(out)))
		if len(fields) < 1 {
			return ""
		}
		ppid, err := strconv.Atoi(fields[0])
		if err != nil || ppid < 1 {
			return ""
		}
		pid = ppid
	}
}
