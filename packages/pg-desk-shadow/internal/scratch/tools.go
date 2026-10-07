package scratch

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Tool is one executable resolved for the scratch bin directory.
type Tool struct {
	Name string
	// Path is the executable linked into the scratch bin (the UNWRAPPED
	// binary when the installed one is a wrapper that rewrites PATH).
	Path string
	// Wrapper is the installed wrapper script it replaced ("" when none).
	Wrapper string
}

// Resolve finds name on lookPath's PATH, follows symlinks and, when the target
// is a wrapper script, returns the sibling `.<name>-wrapped` binary instead:
// the installed wrappers of pg-desk, pg-connector-pr-github and
// pg-connector-ci-github-actions PREPEND the real gh or pg-connector to PATH,
// which would bypass the scratch shims.
func Resolve(name string, lookPath func(string) (string, error)) (Tool, error) {
	p, err := lookPath(name)
	if err != nil {
		return Tool{}, fmt.Errorf("tool %s not found on PATH: %w", name, err)
	}
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		return Tool{}, err
	}
	head, err := readHead(real)
	if err != nil {
		return Tool{}, err
	}
	if !bytes.HasPrefix(head, []byte("#!")) {
		return Tool{Name: name, Path: real}, nil
	}
	sib := filepath.Join(filepath.Dir(real), "."+filepath.Base(real)+"-wrapped")
	sib, err2 := filepath.EvalSymlinks(sib)
	if err2 != nil {
		return Tool{}, fmt.Errorf("tool %s is a wrapper script (%s) with no unwrapped sibling: %w", name, real, err2)
	}
	return Tool{Name: name, Path: sib, Wrapper: real}, nil
}

func readHead(p string) ([]byte, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	b := make([]byte, 4)
	n, _ := f.Read(b)
	return b[:n], nil
}

// Version runs `<path> --version` and returns the first line.
func Version(ctx context.Context, path string) string {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "--version").Output()
	if err != nil {
		return "unknown"
	}
	line, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	return line
}

// ShimScript is the body of a gh or bd shim: it re-enters pg-desk-shadow, which
// classifies, logs and (for a read) execs the real tool.
func ShimScript(self, tool, real, logPath string, hermeticBD bool) string {
	h := ""
	if hermeticBD {
		h = " --hermetic-bd"
	}
	return fmt.Sprintf("#!/bin/sh\nexec %s shim --tool %s --real %s --log %s%s -- \"$@\"\n",
		shq(self), tool, shq(real), shq(logPath), h)
}

func shq(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// LinkTools fills the scratch bin directory: tools (unwrapped binaries) are
// symlinked, shims are written. Existing entries are replaced.
func (l Layout) LinkTools(tools []Tool, shims map[string]string) error {
	for _, t := range tools {
		dst := filepath.Join(l.BinDir(), t.Name)
		_ = os.Remove(dst)
		if err := os.Symlink(t.Path, dst); err != nil {
			return err
		}
	}
	for name, body := range shims {
		dst := filepath.Join(l.BinDir(), name)
		_ = os.Remove(dst)
		if err := os.WriteFile(dst, []byte(body), 0o755); err != nil {
			return err
		}
	}
	return nil
}
