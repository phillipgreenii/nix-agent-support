package main

import (
	"bytes"
	"context"
	"fmt"
	"go/build"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// The Linux cross-compile gate (docket pg2-z5fax.2, "Platforms" and "Testing").
//
// Linux cannot be built or run on the operator's current machine and the repo has
// no CI, so the whole module MUST at least cross-compile for Linux from darwin:
// `GOOS=linux go vet ./...` and `GOOS=linux go test -c` for every package, with
// CGO_ENABLED=0 (the module is pure Go). A Linux-only file (a `_linux.go` seam
// implementation or its `_linux_test.go`) that no longer compiles fails this
// gate on the machine the change is made on, not on the first Linux host.
// Linux-only TESTS are only COMPILED here; pg2-opk5g first RUNS them.
//
// It lives in cmd/pg-connector next to the other module-wide guards
// (dependency_direction_test.go and friends), so it runs inside
// checks.<system>.pg-connector-go-tests' `go test ./...`.

// crossCompileFailure is one step of the gate that failed.
type crossCompileFailure struct {
	// Step is "go list", "go vet ./..." or "go test -c <package>".
	Step string
	// Package is the import path the step names ("" for module-wide steps).
	Package string
	Output  string
}

func (f crossCompileFailure) String() string {
	return fmt.Sprintf("%s failed:\n%s", f.Step, strings.TrimSpace(f.Output))
}

// crossCompileLinux runs the Linux cross-compile gate over the Go module rooted
// at dir and returns every failing step. workers bounds how many `go test -c`
// run at once (one `go vet ./...` covers the whole module, so the module-wide
// cost is a single vet plus one link per package with tests).
func crossCompileLinux(ctx context.Context, goBin, dir string, workers int) []crossCompileFailure {
	env := append(os.Environ(), "GOOS=linux", "CGO_ENABLED=0")
	run := func(args ...string) (string, error) {
		// Niced and capped at 2 compile jobs: this gate shares the machine with
		// the module's other tests (several are timing-sensitive) and must not
		// starve them.
		name, argv := goBin, args
		if nice, err := exec.LookPath("nice"); err == nil {
			name, argv = nice, append([]string{"-n", "10", goBin}, args...)
		}
		cmd := exec.CommandContext(ctx, name, argv...)
		cmd.Dir = dir
		cmd.Env = env
		var out bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &out
		err := cmd.Run()
		return out.String(), err
	}

	// Enumerate under GOOS=linux, so a package whose files are all Linux-only
	// is included.
	listed, err := run("list", "./...")
	if err != nil {
		return []crossCompileFailure{{Step: "go list ./...", Output: listed}}
	}
	var pkgs []string
	for _, line := range strings.Split(listed, "\n") {
		if p := strings.TrimSpace(line); p != "" {
			pkgs = append(pkgs, p)
		}
	}
	sort.Strings(pkgs)

	var (
		mu       sync.Mutex
		failures []crossCompileFailure
	)
	add := func(f crossCompileFailure) {
		mu.Lock()
		failures = append(failures, f)
		mu.Unlock()
	}

	if out, err := run("vet", "-p", "2", "./..."); err != nil {
		add(crossCompileFailure{Step: "go vet ./...", Package: vetPackage(out), Output: out})
	}

	jobs := make(chan string)
	var wg sync.WaitGroup
	if workers < 1 {
		workers = 1
	}
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for pkg := range jobs {
				// -o os.DevNull: compile and link the test binary, keep nothing.
				// A package without test files is a clean no-op.
				if out, err := run("test", "-p", "2", "-c", "-o", os.DevNull, pkg); err != nil {
					add(crossCompileFailure{Step: "go test -c " + pkg, Package: pkg, Output: out})
				}
			}
		}()
	}
	for _, p := range pkgs {
		jobs <- p
	}
	close(jobs)
	wg.Wait()

	sort.Slice(failures, func(i, j int) bool { return failures[i].Step < failures[j].Step })
	return failures
}

// vetPackage names the first package a failed vet run reported ("# <package>"
// header lines), or "" when it named none.
func vetPackage(out string) string {
	for _, line := range strings.Split(out, "\n") {
		if rest, ok := strings.CutPrefix(line, "# "); ok {
			return strings.Fields(rest)[0]
		}
	}
	return ""
}

func crossCompileGo(t *testing.T) string {
	t.Helper()
	if p, err := exec.LookPath("go"); err == nil {
		return p
	}
	cand := filepath.Join(build.Default.GOROOT, "bin", "go")
	if _, err := os.Stat(cand); err == nil {
		return cand
	}
	t.Skip("no go toolchain to cross-compile with")
	return ""
}

// TestCrossCompileLinuxWholeModule is the gate itself: every package of this
// module vets and builds its test binary for GOOS=linux.
func TestCrossCompileLinuxWholeModule(t *testing.T) {
	if testing.Short() {
		t.Skip("the Linux cross-compile gate compiles the whole module for another GOOS")
	}
	goBin := crossCompileGo(t)
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("the module root %s has no go.mod: %v", root, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	for _, f := range crossCompileLinux(ctx, goBin, root, 2) {
		t.Errorf("linux cross-compile: %s", f)
	}
}

// TestCrossCompileLinuxGateCatchesALinuxOnlyBreak is the gate's negative control,
// kept in the repo: a throwaway module whose only defect is a Linux-only file
// that does not compile MUST fail the gate and the failure MUST name the package,
// while the same module without the defect passes. Both halves matter: without
// the second, a gate that always fails would pass this test.
func TestCrossCompileLinuxGateCatchesALinuxOnlyBreak(t *testing.T) {
	goBin := crossCompileGo(t)
	mk := func(broken bool) string {
		dir := t.TempDir()
		write := func(rel, body string) {
			p := filepath.Join(dir, rel)
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		write("go.mod", "module example.test/xc\n\ngo 1.21\n")
		write("seam/seam.go", "package seam\n\nfunc Name() string { return \"portable\" }\n")
		write("seam/seam_linux.go", "//go:build linux\n\npackage seam\n\nfunc linuxOnly() int { return 1 }\n")
		write("seam/seam_linux_test.go", "//go:build linux\n\npackage seam\n\nimport \"testing\"\n\nfunc TestLinuxOnly(t *testing.T) { _ = linuxOnly() }\n")
		if broken {
			write("seam/zz_probe_linux.go", "//go:build linux\n\npackage seam\n\nfunc probe() int { return \"not an int\" }\n")
		}
		return dir
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	if fails := crossCompileLinux(ctx, goBin, mk(false), 2); len(fails) != 0 {
		t.Fatalf("a module that compiles for Linux failed the gate: %v", fails)
	}

	fails := crossCompileLinux(ctx, goBin, mk(true), 2)
	if len(fails) == 0 {
		t.Fatal("a Linux-only compile error passed the cross-compile gate")
	}
	var named bool
	for _, f := range fails {
		if strings.Contains(f.Output, "zz_probe_linux.go") && strings.Contains(f.Output, "seam") {
			named = true
		}
	}
	if !named {
		t.Errorf("the failure does not name the broken package and file: %v", fails)
	}
}
