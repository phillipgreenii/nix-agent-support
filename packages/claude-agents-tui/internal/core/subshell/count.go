package subshell

import (
	"fmt"
	"os/exec"
	"strings"
)

type Counter struct {
	// RunPs is injectable for tests.
	RunPs func(parent int) (string, error)
}

// DefaultRunPs uses `pgrep -P <parent> -l` to list direct children.
// An exit code of 1 (no matches) is translated to an empty list, not an error.
func DefaultRunPs(parent int) (string, error) {
	out, err := exec.Command("pgrep", "-P", fmt.Sprintf("%d", parent), "-l").Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return "", nil
		}
		return "", err
	}
	return string(out), nil
}

func (c *Counter) Count(parent int) (int, error) {
	run := c.RunPs
	if run == nil {
		run = DefaultRunPs
	}
	out, err := run(parent)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		name := fields[len(fields)-1]
		if i := strings.LastIndex(name, "/"); i >= 0 {
			name = name[i+1:]
		}
		switch name {
		case "zsh", "bash", "sh":
			n++
		}
	}
	return n, nil
}
