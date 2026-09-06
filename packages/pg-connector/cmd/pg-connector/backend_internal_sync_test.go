package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// backendSyncGolden reads a two-line "<hexHashA>  <label>\n<hexHashB>
// <label>\n" baseline file (the familiar sha256sum output shape, so a
// human skimming a diff of the baseline can eyeball which hash is which —
// but this parses out only the hash value on each line, ignoring the
// label, so a run from a different absolute path can never matter).
func backendSyncGolden(t *testing.T, path string) (hashA, hashB string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v", path, err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("golden %s: want exactly 2 lines, got %d", path, len(lines))
	}
	fieldsA := strings.Fields(lines[0])
	fieldsB := strings.Fields(lines[1])
	if len(fieldsA) == 0 || len(fieldsB) == 0 {
		t.Fatalf("golden %s: malformed line", path)
	}
	return fieldsA[0], fieldsB[0]
}

func sha256Hex(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// bestEffortDiff shells out to `diff -u` purely to make a failure message
// readable — it is NEVER the pass/fail decision (that is the sha256
// comparison in the caller), so any diff-tool absence or version
// difference can only make the message plainer, never flip the result. (A
// whole-file `diff -u` snapshot was tried as the actual gate first and
// discarded: GNU diffutils and BSD diff pick different, equally valid line
// orderings for a heavily-rewritten file with an ambiguous common anchor
// line, which made a correct, unchanged pair fail depending on which
// `diff` happened to be on PATH. Hashing raw bytes has no such ambiguity.)
func bestEffortDiff(labelA, fileA, labelB, fileB string) string {
	out, err := exec.Command("diff", "-u", "--label", labelA, "--label", labelB, fileA, fileB).Output()
	if len(out) == 0 && err != nil {
		return "(diff unavailable: " + err.Error() + ")"
	}
	return string(out)
}

// TestBackendInternalGitenvAndGithubHelpersSync guards the "inside
// pg-connector itself" duplication bd pg2-sxfwd's Section-B-bullet-5 finding
// names: three gitenv.go copies (pr-github, ci-github-actions, scm-git) and
// two github/{auth,ghexec,token}.go copies (pr-github, ci-github-actions).
// Design §5.2's compiler-enforced backend isolation (independent internal/
// trees per backend — see e.g. cmd/pg-connector-ci-github-actions/internal/
// resolver.go's own doc comment) is why these are copies rather than a
// shared package: only pkg/schema, pkg/provider, and pkg/scriptout are the
// shared, interface-only surface (TestBackendLayoutConvention above), and
// these files are implementation, not interface.
//
// This is a content-hash-pinning guard, not a byte-identity one: the
// copies already, legitimately, differ (re-homed package doc comments
// explaining the copy, ci-github-actions' gitenv.go dropping the
// git-specific Command helper it never needs since it only shells out to
// gh, not git). Each side's sha256 is pinned in a checked-in baseline
// under testdata/backend-sync-drift/. A future change to EITHER side
// shifts that side's hash away from its pinned value and fails here —
// review whether the OTHER side needs the same change, then re-pin both
// hashes in the SAME change (that re-pin is the recorded reason); if the
// other side needed the fix too and didn't get it, port it first.
func TestBackendInternalGitenvAndGithubHelpersSync(t *testing.T) {
	moduleRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve module root: %v", err)
	}

	prGithub := "cmd/pg-connector-pr-github/internal/github"
	prGitenv := "cmd/pg-connector-pr-github/internal/gitenv"
	ciGithub := "cmd/pg-connector-ci-github-actions/internal/github"
	ciGitenv := "cmd/pg-connector-ci-github-actions/internal/gitenv"
	scmGitenv := "cmd/pg-connector-scm-git/internal/gitenv"
	goldenDir := "cmd/pg-connector/testdata/backend-sync-drift"

	type pair struct {
		name, labelA, fileA, labelB, fileB, golden string
	}
	pairs := []pair{
		{
			name:   "gitenv.go (pr-github vs ci-github-actions)",
			labelA: "pg-connector-pr-github/gitenv.go", fileA: filepath.Join(prGitenv, "gitenv.go"),
			labelB: "pg-connector-ci-github-actions/gitenv.go", fileB: filepath.Join(ciGitenv, "gitenv.go"),
			golden: filepath.Join(goldenDir, "gitenv.pr-github.vs.ci-github-actions.sha256pair"),
		},
		{
			name:   "gitenv.go (pr-github vs scm-git)",
			labelA: "pg-connector-pr-github/gitenv.go", fileA: filepath.Join(prGitenv, "gitenv.go"),
			labelB: "pg-connector-scm-git/gitenv.go", fileB: filepath.Join(scmGitenv, "gitenv.go"),
			golden: filepath.Join(goldenDir, "gitenv.pr-github.vs.scm-git.sha256pair"),
		},
		{
			name:   "github/auth.go (pr-github vs ci-github-actions)",
			labelA: "pg-connector-pr-github/auth.go", fileA: filepath.Join(prGithub, "auth.go"),
			labelB: "pg-connector-ci-github-actions/auth.go", fileB: filepath.Join(ciGithub, "auth.go"),
			golden: filepath.Join(goldenDir, "auth.go.sha256pair"),
		},
		{
			name:   "github/ghexec.go (pr-github vs ci-github-actions)",
			labelA: "pg-connector-pr-github/ghexec.go", fileA: filepath.Join(prGithub, "ghexec.go"),
			labelB: "pg-connector-ci-github-actions/ghexec.go", fileB: filepath.Join(ciGithub, "ghexec.go"),
			golden: filepath.Join(goldenDir, "ghexec.go.sha256pair"),
		},
		{
			name:   "github/token.go (pr-github vs ci-github-actions)",
			labelA: "pg-connector-pr-github/token.go", fileA: filepath.Join(prGithub, "token.go"),
			labelB: "pg-connector-ci-github-actions/token.go", fileB: filepath.Join(ciGithub, "token.go"),
			golden: filepath.Join(goldenDir, "token.go.sha256pair"),
		},
	}

	for _, p := range pairs {
		p := p
		t.Run(p.name, func(t *testing.T) {
			absA := filepath.Join(moduleRoot, p.fileA)
			absB := filepath.Join(moduleRoot, p.fileB)
			for _, f := range []string{absA, absB} {
				if _, err := os.Stat(f); err != nil {
					t.Fatalf("stat %s: %v", f, err)
				}
			}

			hashA := sha256Hex(t, absA)
			hashB := sha256Hex(t, absB)
			goldenPath := filepath.Join(moduleRoot, p.golden)
			recordedA, recordedB := backendSyncGolden(t, goldenPath)

			if hashA != recordedA || hashB != recordedB {
				t.Errorf(
					"%s has drifted from its recorded baseline hashes (%s).\n"+
						"Review whether the OTHER side needs the same change; if it does and doesn't have it, port it first.\n"+
						"Then re-pin both hashes in this SAME change (that re-pin is the recorded reason).\n"+
						"recorded: A=%s B=%s\n"+
						"actual:   A=%s B=%s\n"+
						"--- diagnostic diff (informational only; not the pass/fail check) ---\n%s",
					p.name, p.golden, recordedA, recordedB, hashA, hashB,
					bestEffortDiff(p.labelA, absA, p.labelB, absB),
				)
			}
		})
	}
}

// classifyGHErrorFuncPattern bounds the excerpt hashed by
// TestClassifyGHErrorSync below: from classifyGHError's own doc comment
// through the end of isGHNotFound's function body. Both backends define
// these two functions back to back in that order (verified against both
// provider.go files), so one contiguous regexp match captures both.
var classifyGHErrorFuncPattern = regexp.MustCompile(`(?s)// classifyGHError maps.*?\nfunc isGHNotFound\(err error\) bool \{.*?\n\}\n`)

// TestClassifyGHErrorSync guards the other pg2-sxfwd Section-B-bullet-5
// duplication: pg-connector-pr-github's and pg-connector-ci-github-actions'
// provider.go each define their own classifyGHError/isGHNotFound pair,
// mapping a raw `gh` CLI error onto scriptout's closed error taxonomy.
// This hashes only that excerpt (not the whole provider.go, which
// legitimately differs everywhere else — the two backends implement
// different capabilities) against a checked-in baseline, same
// content-hash-pinning pattern and rationale as
// TestBackendInternalGitenvAndGithubHelpersSync above.
func TestClassifyGHErrorSync(t *testing.T) {
	moduleRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve module root: %v", err)
	}

	extract := func(relPath string) string {
		raw, err := os.ReadFile(filepath.Join(moduleRoot, relPath))
		if err != nil {
			t.Fatalf("read %s: %v", relPath, err)
		}
		m := classifyGHErrorFuncPattern.FindString(string(raw))
		if m == "" {
			t.Fatalf("%s: classifyGHError/isGHNotFound excerpt not found (pattern drifted out from under this test)", relPath)
		}
		return m
	}

	prExcerpt := extract("cmd/pg-connector-pr-github/internal/provider.go")
	ciExcerpt := extract("cmd/pg-connector-ci-github-actions/internal/provider.go")

	sumA := sha256.Sum256([]byte(prExcerpt))
	sumB := sha256.Sum256([]byte(ciExcerpt))
	hashA := hex.EncodeToString(sumA[:])
	hashB := hex.EncodeToString(sumB[:])

	goldenPath := filepath.Join(moduleRoot, "cmd/pg-connector/testdata/backend-sync-drift/classify-gh-error.sha256pair")
	recordedA, recordedB := backendSyncGolden(t, goldenPath)

	if hashA != recordedA || hashB != recordedB {
		// The diagnostic diff needs real files, since bestEffortDiff shells
		// out to `diff`; write the two excerpts to temp files for that one
		// purpose only (never used for the pass/fail decision above).
		prFile, ferr := os.CreateTemp("", "classifygherror-pr-*.go")
		if ferr == nil {
			defer os.Remove(prFile.Name())
			_, _ = prFile.WriteString(prExcerpt)
			prFile.Close()
		}
		ciFile, ferr := os.CreateTemp("", "classifygherror-ci-*.go")
		if ferr == nil {
			defer os.Remove(ciFile.Name())
			_, _ = ciFile.WriteString(ciExcerpt)
			ciFile.Close()
		}
		diagnostic := "(diagnostic diff unavailable)"
		if prFile != nil && ciFile != nil {
			diagnostic = bestEffortDiff(
				"pg-connector-pr-github/provider.go#classifyGHError", prFile.Name(),
				"pg-connector-ci-github-actions/provider.go#classifyGHError", ciFile.Name(),
			)
		}
		t.Errorf(
			"classifyGHError/isGHNotFound has drifted from its recorded baseline hashes (cmd/pg-connector/testdata/backend-sync-drift/classify-gh-error.sha256pair).\n"+
				"Review whether the OTHER side needs the same change; if it does and doesn't have it, port it first.\n"+
				"Then re-pin both hashes in this SAME change (that re-pin is the recorded reason).\n"+
				"recorded: A=%s B=%s\n"+
				"actual:   A=%s B=%s\n"+
				"--- diagnostic diff (informational only; not the pass/fail check) ---\n%s",
			recordedA, recordedB, hashA, hashB, diagnostic,
		)
	}
}
