//go:build integration

package patchid

// Integration-tagged regression test for bead pg2-bgh35: git show / git log -p
// must never let a diff driver's rendering leak into the bytes `git patch-id`
// hashes. See Compute's doc comment and
// docs/adr/0018-pb-tool-and-pn-applied-contract.md's "Patch-id inputs must be
// driver-free" note.
//
// Corrupting vector, verified empirically against this machine's real git
// (2.54.0) and its real `diff.external=difft` config (~/.config/git/config):
// `git show`/`git log -p` do NOT invoke diff.external/GIT_EXTERNAL_DIFF by
// default. git-log(1)'s own manual page says a gitattributes-scoped external
// diff driver "need[s]... [--ext-diff]... with git-log(1) and friends", and the
// same restriction was confirmed (by direct experiment against this repo's real
// diff.external config) to apply to a non-attribute-scoped diff.external too --
// `git show HEAD` with no flags produced an UNCHANGED, correct diff despite the
// real difftastic diff.external config being active. A diff.*.textconv driver
// wired through a .gitattributes `diff=` attribute is different: git-log(1)
// documents textconv as "enabled by default only for git-diff(1) and
// git-log(1)" -- and this was confirmed to actually corrupt `git show`'s output
// with no extra flag needed. So this test poisons the repo with BOTH a
// textconv driver (the vector proven to fire unconditionally, which drives the
// discriminating "pre-fix differs from reference" assertion below) and
// GIT_EXTERNAL_DIFF in the environment (matching the bug report, and as a
// defensive check against a git version/config where ext-diff becomes
// default-on for the log/show family).

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/pb/internal/run"
)

// writeGarbageDriver writes an executable shell script at path that echoes a
// fixed marker plus its first argument (the temp blob path git-log(1) invokes
// diff/textconv drivers with) and ignores everything else. Including the
// argument keeps old-side and new-side output distinct (git supplies a
// different temp path per side), so the driver still produces a genuine hunk
// instead of collapsing the diff to "no changes".
func writeGarbageDriver(t *testing.T, path, marker string) {
	t.Helper()
	script := "#!/bin/sh\necho \"" + marker + "-for-$1\"\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

// rawShowPatchID runs `git -C dir show <showArgs...> HEAD | git -C dir
// patch-id --stable` directly (bypassing Client), so the caller can include or
// omit --no-ext-diff/--no-textconv/--no-color to simulate Compute's fixed
// invocation or the pre-fix invocation shape (the literal pre-fix argv was
// []string{"-C", repoPath, "show", commitish}, i.e. showArgs == nil).
func rawShowPatchID(t *testing.T, dir string, showArgs, env []string) string {
	t.Helper()
	r := run.CLIRunner{}
	args := append([]string{"-C", dir, "show"}, showArgs...)
	args = append(args, "HEAD")
	show, err := r.Run(context.Background(), "git", args, run.Options{Env: env})
	if err != nil {
		t.Fatalf("git show: %v", err)
	}
	res, err := r.Run(context.Background(), "git", []string{"-C", dir, "patch-id", "--stable"},
		run.Options{Env: env, Stdin: show.Stdout})
	if err != nil {
		t.Fatalf("git patch-id: %v", err)
	}
	trimmed := strings.TrimSpace(res.Stdout)
	if trimmed == "" {
		return ""
	}
	return firstField(strings.SplitN(trimmed, "\n", 2)[0])
}

// poisonedRunner is a hermeticCLIRunner variant (see patchid_test.go) that
// falls back to env (expected to already carry the hermetic base vars plus
// GIT_EXTERNAL_DIFF) whenever the caller leaves opts.Env nil -- exactly how
// Compute's own git invocations leave it.
type poisonedRunner struct{ env []string }

func (p poisonedRunner) Run(ctx context.Context, name string, args []string, opts run.Options) (run.Result, error) {
	if opts.Env == nil {
		opts.Env = p.env
	}
	return run.CLIRunner{}.Run(ctx, name, args, opts)
}

func TestCompute_ignoresExternalDiffAndTextconvDrivers(t *testing.T) {
	dir := initRepo(t)

	// Wire a diff.*.textconv driver via repo-local config + a .gitattributes
	// `diff=` assignment -- the vector git-log(1) applies BY DEFAULT (unlike
	// ext-diff, which needs an explicit --ext-diff for the log/show family;
	// see the package doc comment above).
	textconvDriver := filepath.Join(dir, "garbage-textconv.sh")
	writeGarbageDriver(t, textconvDriver, "TEXTCONV-GARBAGE")
	runGit(t, dir, "config", "diff.corruptor.textconv", textconvDriver)
	commit(t, dir, ".gitattributes", "*.txt diff=corruptor\n", "wire textconv driver")

	commit(t, dir, "a.txt", "hello\n", "add a")
	commit(t, dir, "a.txt", "hello there\n", "modify a")

	// GIT_EXTERNAL_DIFF poisoning per the bug report's testable claim.
	extDiffDriver := filepath.Join(dir, "garbage-ext-diff.sh")
	writeGarbageDriver(t, extDiffDriver, "EXT-DIFF-GARBAGE")
	poisonedEnv := append(append([]string{}, hermeticEnviron()...), "GIT_EXTERNAL_DIFF="+extDiffDriver)

	// Reference: Compute's own anti-driver flags, computed directly in a
	// clean (unpoisoned) environment -- the ground truth this bug must not
	// deviate from.
	refID := rawShowPatchID(t, dir, []string{"--no-ext-diff", "--no-textconv", "--no-color"}, hermeticEnviron())
	if refID == "" {
		t.Fatalf("reference computation produced no id")
	}

	// Control: the EXACT pre-fix invocation shape (no anti-driver flags at
	// all), run directly in the poisoned environment, must differ from the
	// reference -- otherwise the poisoning failed to corrupt anything and this
	// test cannot discriminate fixed from unfixed code.
	preFixID := rawShowPatchID(t, dir, nil, poisonedEnv)
	if preFixID == refID {
		t.Fatalf("pre-fix invocation id %q unexpectedly matches the reference %q; "+
			"the poisoning failed to corrupt the diff, so this test cannot "+
			"discriminate fixed from unfixed code", preFixID, refID)
	}

	// Experiment: Client.Compute (the fixed code) in the SAME poisoned
	// environment must still equal the reference -- its own
	// --no-ext-diff/--no-textconv/--no-color must override both drivers.
	c := Client{R: poisonedRunner{env: poisonedEnv}}
	gotID, err := c.Compute(context.Background(), dir, "HEAD")
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if gotID != refID {
		t.Errorf("Compute id = %q with textconv/GIT_EXTERNAL_DIFF drivers poisoning the "+
			"environment, want reference %q (--no-ext-diff/--no-textconv/--no-color must "+
			"make Compute immune to both drivers)", gotID, refID)
	}
}
