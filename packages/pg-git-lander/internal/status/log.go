package status

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/gitexec"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/oplog"
)

// ErrNoLog means no log matches the OP_ID or BRANCH.
var ErrNoLog = errors.New("no log")

var opIDPattern = regexp.MustCompile(`^[0-9]{8}T[0-9]{6}Z-[0-9a-f]{6}$`)

// ResolveLog finds the log file of arg, an op id or a BRANCH (the current or
// most recent operation's log). It returns the op id and the file path.
func ResolveLog(ctx context.Context, g *gitexec.Git, dir, arg string) (string, string, error) {
	common, err := g.CommonDir(ctx, dir)
	if err != nil {
		return "", "", fmt.Errorf("%w: %s", ErrNotARepo, dir)
	}
	if opIDPattern.MatchString(arg) {
		p := oplog.LogPath(common, arg)
		if _, err := os.Stat(p); err == nil {
			return arg, p, nil
		}
	}
	id, err := oplog.FindByBranch(common, arg)
	if err != nil {
		return "", "", err
	}
	if id == "" {
		return "", "", fmt.Errorf("%w for %q: it is neither the op id of a retained log nor a branch with one", ErrNoLog, arg)
	}
	return id, oplog.LogPath(common, id), nil
}

// PrintLog writes the log at path to w; tail >= 0 limits it to the last tail
// lines.
func PrintLog(w io.Writer, path string, tail int) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if tail < 0 {
		_, err = io.Copy(w, f)
		return err
	}
	var ring []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		ring = append(ring, sc.Text())
		if len(ring) > tail {
			ring = ring[1:]
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	if len(ring) > 0 {
		_, err = io.WriteString(w, strings.Join(ring, "\n")+"\n")
	}
	return err
}
