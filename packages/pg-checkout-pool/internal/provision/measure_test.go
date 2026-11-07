//go:build measure

package provision

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestMeasureCloneVsWorktree times and (optionally) counts filesystem events
// for slot create, first and warm status, dirty reset and remove, for the
// worktree and clone provisioners on the same synthetic repository. It is a
// measurement, not a unit test: build tag "measure", and set
// PG_CHECKOUT_POOL_MEASURE_FILES (tracked file count, default 20000) and
// optionally PG_CHECKOUT_POOL_FSCOUNT (path to an FSEvents counter that takes
// a directory to watch and prints one line per SIGUSR1).
func TestMeasureCloneVsWorktree(t *testing.T) {
	skipUnlessCloneable(t)
	n := 20000
	if v := os.Getenv("PG_CHECKOUT_POOL_MEASURE_FILES"); v != "" {
		n, _ = strconv.Atoi(v)
	}
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(base, "golden")
	run(t, base, "init", "-q", "-b", "main", repo)
	write(t, filepath.Join(repo, ".gitignore"), "*.o\n")
	body := strings.Repeat("x", 900) + "\n"
	for i := 0; i < n; i++ {
		write(t, filepath.Join(repo, fmt.Sprintf("d%03d", i%(n/10+1)), fmt.Sprintf("f%d.txt", i)), body)
	}
	run(t, repo, "add", ".")
	run(t, repo, "commit", "-q", "-m", "c1")
	c1 := run(t, repo, "rev-parse", "HEAD")
	for i := 0; i < 500; i++ {
		write(t, filepath.Join(repo, fmt.Sprintf("d%03d", i%(n/10+1)), fmt.Sprintf("f%d.txt", i)), "changed\n")
	}
	run(t, repo, "commit", "-aq", "-m", "c2")
	c2 := run(t, repo, "rev-parse", "HEAD")
	run(t, repo, "gc", "-q")

	var counter *exec.Cmd
	var lines *bufio.Scanner
	if bin := os.Getenv("PG_CHECKOUT_POOL_FSCOUNT"); bin != "" {
		counter = exec.Command(bin, base)
		out, _ := counter.StdoutPipe()
		if err := counter.Start(); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = counter.Process.Kill(); _ = counter.Wait() }()
		lines = bufio.NewScanner(out)
		lines.Scan() // "ready"
	}
	events := func() string {
		if counter == nil {
			return "n/a"
		}
		time.Sleep(700 * time.Millisecond)
		_ = counter.Process.Signal(syscall.SIGUSR1)
		if lines.Scan() {
			return lines.Text()
		}
		return "n/a"
	}
	step := func(kind, op string, fn func()) {
		events() // reset
		start := time.Now()
		fn()
		el := time.Since(start).Seconds()
		t.Logf("MEASURE %s | %s | %.2fs | %s", kind, op, el, events())
	}

	ctx := context.Background()
	for _, kind := range []string{"worktree", "clone"} {
		var p Provisioner
		if kind == "worktree" {
			p, _ = NewWorktree(WorktreeConfig{Repo: repo, Env: hermeticEnv, Keep: []string{}})
		} else {
			p, _ = NewClone(WorktreeConfig{Repo: repo, Env: hermeticEnv, Keep: []string{}})
		}
		slot := filepath.Join(base, "slot-"+kind)
		step(kind, "create", func() {
			if err := p.Create(ctx, slot, c1); err != nil {
				t.Fatal(err)
			}
		})
		step(kind, "first status", func() { run(t, slot, "status", "--porcelain") })
		step(kind, "warm status", func() { run(t, slot, "status", "--porcelain") })
		for i := 0; i < 200; i++ {
			write(t, filepath.Join(slot, fmt.Sprintf("d%03d", i%(n/10+1)), fmt.Sprintf("f%d.txt", i)), "dirty\n")
			write(t, filepath.Join(slot, "untracked", fmt.Sprintf("u%d.txt", i)), "u")
			write(t, filepath.Join(slot, "build", fmt.Sprintf("o%d.o", i)), "o")
		}
		events()
		step(kind, "reset dirty (to c2, 500 files differ)", func() {
			if _, err := p.Reset(ctx, slot, c2, "lease/m"); err != nil {
				t.Fatal(err)
			}
		})
		step(kind, "reset clean (to c1)", func() {
			if _, err := p.Reset(ctx, slot, c1, ""); err != nil {
				t.Fatal(err)
			}
		})
		step(kind, "remove", func() {
			if kind == "worktree" {
				run(t, repo, "worktree", "remove", "--force", slot)
			} else if err := os.RemoveAll(slot); err != nil {
				t.Fatal(err)
			}
		})
	}
}
