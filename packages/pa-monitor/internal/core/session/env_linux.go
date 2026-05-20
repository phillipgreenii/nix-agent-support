//go:build linux

package session

import (
	"fmt"
	"os"
	"strings"
)

// readProcessEnv reads /proc/<pid>/environ on Linux. The file is a
// single buffer of NUL-separated KEY=VALUE pairs. May return permission
// denied if the process belongs to another user.
func readProcessEnv(pid int) (map[string]string, error) {
	path := fmt.Sprintf("/proc/%d/environ", pid)
	data, err := os.ReadFile(path)
	if err != nil {
		return map[string]string{}, err
	}
	out := map[string]string{}
	for _, pair := range strings.Split(string(data), "\x00") {
		if pair == "" {
			continue
		}
		eq := strings.IndexByte(pair, '=')
		if eq <= 0 {
			continue
		}
		out[pair[:eq]] = pair[eq+1:]
	}
	return out, nil
}
