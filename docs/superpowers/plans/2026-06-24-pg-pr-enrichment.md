# pg-pr #2 PR Enrichment Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Compute four deterministic, no-LLM enrichment fields (kind, languages, size, urgency) on each PR during sync and persist them on the `pull_request` store row, surfaced via `pg-pr pr info`.

**Architecture:** A pure `internal/enrich` package turns PR data into the four fields. The sync engine computes them per observed PR and persists them via a dedicated `store.SetEnrichment` UPDATE that is _decoupled_ from the lifecycle `emitPREvent` write — enrichment self-heals each tick, so it needn't be atomic, and its columns are deliberately absent from `UpsertPR` so neither the lifecycle emit nor `ingest.go`'s upsert can clobber them. Extra raw data (PR body, labels, file paths, commit messages) is added to the existing single GraphQL enrich query.

**Tech Stack:** Go, `modernc.org/sqlite` (no-CGO), `github.com/go-enry/go-enry/v2` (language detection), gomod2nix.

**Bead:** `pg2-4c5i.10`. **Branch:** `pg-pr-enrichment` (already created off the #17 work). **Spec:** `docs/superpowers/specs/2026-06-24-pg-pr-enrichment-design.md`.

**Module root for all commands:** `packages/pg-pr` (run `go` from there). Repo root: `phillipgreenii-nix-agent-support`.

**Conventions:** TDD (watch each test fail first). Conventional-commit messages with a `Refs pg2-4c5i.10` trailer. If a commit aborts on a treefmt/prettier reformat, `git add -A` the reformatted files and re-commit. Final gates: `go test ./...`, then `nix build .#pg-pr`.

---

### Task 1: enrich — size bucketing + package skeleton

**Files:**

- Create: `packages/pg-pr/internal/enrich/enrich.go`
- Test: `packages/pg-pr/internal/enrich/enrich_test.go`

- [ ] **Step 1: Write the failing test**

```go
package enrich

import "testing"

func TestBucketSize(t *testing.T) {
	cases := []struct {
		total int
		want  string
	}{
		{0, "XS"}, {9, "XS"}, {10, "S"}, {29, "S"}, {30, "M"},
		{99, "M"}, {100, "L"}, {499, "L"}, {500, "XL"}, {5000, "XL"},
	}
	for _, c := range cases {
		if got := bucketSize(c.total); got != c.want {
			t.Errorf("bucketSize(%d) = %q; want %q", c.total, got, c.want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/enrich/ -run TestBucketSize -v`
Expected: FAIL — `undefined: bucketSize`.

- [ ] **Step 3: Write minimal implementation**

```go
// Package enrich computes deterministic, LLM-free enrichment fields for a PR:
// kind, languages, size, and urgency. All functions are pure (no I/O, no clock,
// no network) so they are fully table-testable.
package enrich

// bucketSize maps a total changed-line count (additions+deletions) to a
// coarse size bucket.
func bucketSize(total int) string {
	switch {
	case total < 10:
		return "XS"
	case total < 30:
		return "S"
	case total < 100:
		return "M"
	case total < 500:
		return "L"
	default:
		return "XL"
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/enrich/ -run TestBucketSize -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add packages/pg-pr/internal/enrich/
git commit -m "feat(pg-pr): enrich package + size bucketing

Refs pg2-4c5i.10"
```

---

### Task 2: enrich — kind classification

**Files:**

- Modify: `packages/pg-pr/internal/enrich/enrich.go`
- Test: `packages/pg-pr/internal/enrich/enrich_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestClassifyKind(t *testing.T) {
	cases := []struct {
		name    string
		title   string
		branch  string
		commits []string
		want    string
	}{
		{"title conventional fix", "fix(store): wrong scan", "anything", nil, "bugfix"},
		{"title feat with bang", "feat!: breaking change", "x", nil, "feature"},
		{"branch prefix when title plain", "tidy things up", "refactor/cleanup", nil, "refactor"},
		{"branch feature alias", "stuff", "feature/new-ui", nil, "feature"},
		{"commit majority when title+branch plain", "wip", "wip", []string{"fix: a", "fix: b", "docs: c"}, "bugfix"},
		{"fallback other", "random work", "wip", nil, "other"},
		{"title wins over branch", "docs: readme", "fix/typo", nil, "docs"},
	}
	for _, c := range cases {
		if got := classifyKind(c.title, c.branch, c.commits); got != c.want {
			t.Errorf("%s: classifyKind(%q,%q,%v) = %q; want %q", c.name, c.title, c.branch, c.commits, got, c.want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/enrich/ -run TestClassifyKind -v`
Expected: FAIL — `undefined: classifyKind`.

- [ ] **Step 3: Write minimal implementation**

Append to `enrich.go`:

```go
import (
	"regexp"
	"strings"
)

// conventional-commit header: optional leading space, a type word, optional
// (scope), optional !, then a colon.
var ccTypeRe = regexp.MustCompile(`^\s*([a-zA-Z]+)(\([^)]*\))?!?:`)

// classifyKind returns the single dominant change kind. Precedence: PR-title
// conventional-commit prefix, then branch prefix, then commit-type majority,
// then "other". Title/branch tiers work on every code path; the commit tier
// only has data when commit messages were fetched (GraphQL path).
func classifyKind(title, branch string, commits []string) string {
	if k := kindFromConventional(title); k != "" {
		return k
	}
	if k := kindFromBranch(branch); k != "" {
		return k
	}
	if k := kindFromCommitMajority(commits); k != "" {
		return k
	}
	return "other"
}

func kindFromConventional(s string) string {
	m := ccTypeRe.FindStringSubmatch(s)
	if m == nil {
		return ""
	}
	return mapCCType(strings.ToLower(m[1]))
}

func kindFromBranch(b string) string {
	seg := b
	if i := strings.IndexByte(b, '/'); i >= 0 {
		seg = b[:i]
	}
	return mapCCType(strings.ToLower(seg))
}

// kindFromCommitMajority classifies each commit's first line and returns the
// most common classified kind, with a deterministic alphabetical tiebreak.
// Returns "" when no commit is classifiable.
func kindFromCommitMajority(commits []string) string {
	counts := map[string]int{}
	for _, c := range commits {
		first := strings.SplitN(c, "\n", 2)[0]
		if k := kindFromConventional(first); k != "" {
			counts[k]++
		}
	}
	best := ""
	for k, n := range counts {
		if n > counts[best] || (n == counts[best] && (best == "" || k < best)) {
			best = k
		}
	}
	return best
}

func mapCCType(t string) string {
	switch t {
	case "feat", "feature":
		return "feature"
	case "fix", "bugfix", "hotfix":
		return "bugfix"
	case "refactor", "perf":
		return "refactor"
	case "docs", "doc":
		return "docs"
	case "test", "tests":
		return "test"
	case "chore", "build", "ci", "style":
		return "chore"
	}
	return ""
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/enrich/ -run TestClassifyKind -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add packages/pg-pr/internal/enrich/
git commit -m "feat(pg-pr): enrich kind classification (title/branch/commit precedence)

Refs pg2-4c5i.10"
```

---

### Task 3: enrich — urgency scoring

**Files:**

- Modify: `packages/pg-pr/internal/enrich/enrich.go`
- Test: `packages/pg-pr/internal/enrich/enrich_test.go`

- [ ] **Step 1: Write the failing test**

```go
import (
	"reflect"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
)

func failingRun() api.CIRun  { return api.CIRun{Status: "completed", Conclusion: "failure"} }
func successRun2() api.CIRun { return api.CIRun{Status: "completed", Conclusion: "success"} }

func TestScoreUrgency(t *testing.T) {
	t.Run("none → low", func(t *testing.T) {
		lvl, score, reasons := scoreUrgency(Input{PR: api.PR{Title: "feat: x", Body: "normal"}, CIRuns: []api.CIRun{successRun2()}})
		if lvl != "low" || score != 0 || len(reasons) != 0 {
			t.Fatalf("got %q score=%d reasons=%v; want low/0/[]", lvl, score, reasons)
		}
	})
	t.Run("urgency label → high", func(t *testing.T) {
		lvl, score, reasons := scoreUrgency(Input{PR: api.PR{Title: "x"}, Labels: []string{"P0"}})
		if lvl != "high" || score != 3 || !reflect.DeepEqual(reasons, []string{"label:p0"}) {
			t.Fatalf("got %q score=%d reasons=%v; want high/3/[label:p0]", lvl, score, reasons)
		}
	})
	t.Run("keyword → medium", func(t *testing.T) {
		lvl, _, reasons := scoreUrgency(Input{PR: api.PR{Title: "Fix for production incident"}})
		if lvl != "medium" || !reflect.DeepEqual(reasons, []string{"keyword:production incident"}) {
			t.Fatalf("got %q reasons=%v; want medium/[keyword:production incident]", lvl, reasons)
		}
	})
	t.Run("bugfix commit alone → medium", func(t *testing.T) {
		lvl, score, _ := scoreUrgency(Input{PR: api.PR{Title: "wip"}, Commits: []string{"fix: a"}})
		if lvl != "medium" || score != 1 {
			t.Fatalf("got %q score=%d; want medium/1", lvl, score)
		}
	})
	t.Run("ci failing → medium", func(t *testing.T) {
		lvl, score, _ := scoreUrgency(Input{PR: api.PR{Title: "x"}, CIRuns: []api.CIRun{failingRun()}})
		if lvl != "medium" || score != 2 {
			t.Fatalf("got %q score=%d; want medium/2", lvl, score)
		}
	})
	t.Run("keyword + ci failing → high", func(t *testing.T) {
		lvl, score, _ := scoreUrgency(Input{PR: api.PR{Title: "hotfix outage"}, CIRuns: []api.CIRun{failingRun()}})
		if lvl != "high" || score < 3 {
			t.Fatalf("got %q score=%d; want high/>=3", lvl, score)
		}
	})
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/enrich/ -run TestScoreUrgency -v`
Expected: FAIL — `undefined: scoreUrgency` / `undefined: Input`.

- [ ] **Step 3: Write minimal implementation**

Append to `enrich.go` (add `"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"` to imports):

```go
// Input is everything the enrichment computation needs, decoupled from how it
// was fetched. Files/Commits/Labels may be empty (partial data degrades, never
// errors).
type Input struct {
	PR      api.PR
	Files   []string    // changed-file paths
	Commits []string    // commit messages
	Labels  []string    // PR label names
	CIRuns  []api.CIRun // the PR's own CI runs
}

var urgencyLabels = map[string]bool{
	"urgent": true, "p0": true, "p1": true, "hotfix": true,
	"security": true, "incident": true, "critical": true, "sev1": true, "sev2": true,
}

var urgencyKeywords = []string{
	"production incident", "outage", "hotfix", "sev1", "sev2",
	"regression", "revert", "asap", "urgent", "critical",
}

// scoreUrgency returns an urgency level (low|medium|high), the additive score
// behind it, and the list of signals that fired (for transparency). Scoring:
// urgency label +3, title/body keyword +2, failing CI +2, bugfix commit +1.
// high at score>=3, medium at score>=1, else low.
func scoreUrgency(in Input) (string, int, []string) {
	score := 0
	var reasons []string

	for _, l := range in.Labels {
		ll := strings.ToLower(strings.TrimSpace(l))
		if urgencyLabels[ll] {
			score += 3
			reasons = append(reasons, "label:"+ll)
			break
		}
	}

	hay := strings.ToLower(in.PR.Title + "\n" + in.PR.Body)
	for _, kw := range urgencyKeywords {
		if strings.Contains(hay, kw) {
			score += 2
			reasons = append(reasons, "keyword:"+kw)
			break
		}
	}

	if anyCIFailing(in.CIRuns) {
		score += 2
		reasons = append(reasons, "ci-failing")
	}

	for _, c := range in.Commits {
		first := strings.SplitN(c, "\n", 2)[0]
		if kindFromConventional(first) == "bugfix" {
			score++
			reasons = append(reasons, "bugfix-commit")
			break
		}
	}

	level := "low"
	switch {
	case score >= 3:
		level = "high"
	case score >= 1:
		level = "medium"
	}
	return level, score, reasons
}

// anyCIFailing reports whether any completed run has a non-success conclusion
// (failure/timed_out/cancelled/action_required/...). Pending/neutral/skipped
// runs do not count as failing.
func anyCIFailing(runs []api.CIRun) bool {
	for _, r := range runs {
		if !strings.EqualFold(r.Status, "completed") {
			continue
		}
		switch strings.ToLower(r.Conclusion) {
		case "", "success", "neutral", "skipped":
		default:
			return true
		}
	}
	return false
}
```

Note: keyword scan runs before CI in the implementation, but the per-signal tests assert independently; the `none → low` and `keyword + ci` cases pin the ordering of reasons (`keyword` before `ci-failing`).

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/enrich/ -run TestScoreUrgency -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add packages/pg-pr/internal/enrich/
git commit -m "feat(pg-pr): enrich urgency scoring (labels/keywords/bugfix-commits/CI)

Refs pg2-4c5i.10"
```

---

### Task 4: enrich — language detection (adds go-enry dependency)

**Files:**

- Create: `packages/pg-pr/internal/enrich/languages.go`
- Test: `packages/pg-pr/internal/enrich/languages_test.go`
- Modify: `packages/pg-pr/go.mod`, `packages/pg-pr/go.sum`, `packages/pg-pr/gomod2nix.toml`

- [ ] **Step 1: Add the go-enry dependency**

Run from `packages/pg-pr`:

```bash
go get github.com/go-enry/go-enry/v2@v2.9.6
go mod tidy
nix run github:nix-community/gomod2nix -- generate
```

Expected: `go.mod`/`go.sum` updated, `gomod2nix.toml` regenerated. Verify the default (pure-Go, no-CGO) build is used — do NOT add an `oniguruma` build tag.

- [ ] **Step 2: Write the failing test**

```go
package enrich

import (
	"reflect"
	"testing"
)

func TestDetectLanguages(t *testing.T) {
	t.Run("ranked by count", func(t *testing.T) {
		got := detectLanguages([]string{"a.go", "b.go", "c.py"})
		if !reflect.DeepEqual(got, []string{"Go", "Python"}) {
			t.Fatalf("got %v; want [Go Python]", got)
		}
	})
	t.Run("empty input", func(t *testing.T) {
		if got := detectLanguages(nil); got != nil {
			t.Fatalf("got %v; want nil", got)
		}
	})
	t.Run("nix recognized", func(t *testing.T) {
		got := detectLanguages([]string{"flake.nix"})
		if !reflect.DeepEqual(got, []string{"Nix"}) {
			t.Fatalf("got %v; want [Nix]", got)
		}
	})
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/enrich/ -run TestDetectLanguages -v`
Expected: FAIL — `undefined: detectLanguages`.

- [ ] **Step 4: Write minimal implementation**

```go
package enrich

import (
	"sort"

	enry "github.com/go-enry/go-enry/v2"
)

// detectLanguages maps changed-file paths to languages using go-enry's
// path-only detection (no blob contents), tallies by file count, and returns
// the languages sorted by count desc then name asc. Unrecognized paths are
// skipped. Returns nil for no input (or no recognized files).
func detectLanguages(files []string) []string {
	if len(files) == 0 {
		return nil
	}
	counts := map[string]int{}
	for _, f := range files {
		if lang := enry.GetLanguage(f, nil); lang != "" {
			counts[lang]++
		}
	}
	if len(counts) == 0 {
		return nil
	}
	out := make([]string, 0, len(counts))
	for l := range counts {
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool {
		if counts[out[i]] != counts[out[j]] {
			return counts[out[i]] > counts[out[j]]
		}
		return out[i] < out[j]
	})
	return out
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/enrich/ -run TestDetectLanguages -v`
Expected: PASS. (If go-enry returns a different canonical name for a case, adjust the expected value to match enry's output — the test documents enry's behavior.)

- [ ] **Step 6: Commit**

```bash
git add packages/pg-pr/internal/enrich/ packages/pg-pr/go.mod packages/pg-pr/go.sum packages/pg-pr/gomod2nix.toml
git commit -m "feat(pg-pr): enrich language detection via go-enry (path-only)

Refs pg2-4c5i.10"
```

---

### Task 5: enrich — Compute (integrate all four signals)

**Files:**

- Modify: `packages/pg-pr/internal/enrich/enrich.go`
- Test: `packages/pg-pr/internal/enrich/enrich_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestCompute(t *testing.T) {
	in := Input{
		PR:      api.PR{Title: "fix(api): null deref", Body: "production incident", Additions: 40, Deletions: 5, Branch: "fix/null"},
		Files:   []string{"a.go", "b.go", "c.py"},
		Commits: []string{"fix: handle nil"},
		Labels:  []string{"p1"},
		CIRuns:  []api.CIRun{failingRun()},
	}
	got := Compute(in)
	if got.Kind != "bugfix" {
		t.Errorf("Kind = %q; want bugfix", got.Kind)
	}
	if !reflect.DeepEqual(got.Languages, []string{"Go", "Python"}) {
		t.Errorf("Languages = %v; want [Go Python]", got.Languages)
	}
	if got.Size != "M" { // 45 lines
		t.Errorf("Size = %q; want M", got.Size)
	}
	if got.Urgency != "high" {
		t.Errorf("Urgency = %q; want high", got.Urgency)
	}
	if got.UrgencyScore < 3 || len(got.UrgencyReasons) == 0 {
		t.Errorf("UrgencyScore=%d reasons=%v; want >=3 and non-empty", got.UrgencyScore, got.UrgencyReasons)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/enrich/ -run TestCompute -v`
Expected: FAIL — `undefined: Compute` / `undefined: Result`.

- [ ] **Step 3: Write minimal implementation**

Append to `enrich.go`:

```go
// Result is the computed enrichment for a PR.
type Result struct {
	Kind           string
	Languages      []string
	Size           string
	Urgency        string
	UrgencyScore   int
	UrgencyReasons []string
}

// Compute derives all four enrichment fields from Input. Pure and deterministic.
func Compute(in Input) Result {
	r := Result{
		Kind:      classifyKind(in.PR.Title, in.PR.Branch, in.Commits),
		Languages: detectLanguages(in.Files),
		Size:      bucketSize(in.PR.Additions + in.PR.Deletions),
	}
	r.Urgency, r.UrgencyScore, r.UrgencyReasons = scoreUrgency(in)
	return r
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/enrich/ -v`
Expected: PASS (all enrich tests).

- [ ] **Step 5: Commit**

```bash
git add packages/pg-pr/internal/enrich/
git commit -m "feat(pg-pr): enrich.Compute integrates kind/languages/size/urgency

Refs pg2-4c5i.10"
```

---

### Task 6: store — migration v2 (enrichment columns)

**Files:**

- Modify: `packages/pg-pr/internal/store/migrate.go:7` (schemaVersion) and the `migrations` slice
- Test: `packages/pg-pr/internal/store/migrate_test.go`

- [ ] **Step 1: Write the failing test**

Append to `migrate_test.go`:

```go
func TestMigrate_V2EnrichmentColumns(t *testing.T) {
	db := OpenForTest(t)
	var v int
	if err := db.sql.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		t.Fatalf("user_version: %v", err)
	}
	if v != schemaVersion || schemaVersion != 2 {
		t.Fatalf("user_version=%d schemaVersion=%d; want both 2", v, schemaVersion)
	}
	for _, col := range []string{"kind", "languages", "size", "urgency", "urgency_score", "urgency_reasons"} {
		var cnt int
		if err := db.sql.QueryRow(
			"SELECT COUNT(*) FROM pragma_table_info('pull_request') WHERE name=?", col).Scan(&cnt); err != nil {
			t.Fatalf("pragma_table_info %s: %v", col, err)
		}
		if cnt != 1 {
			t.Errorf("column %q missing from pull_request", col)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/store/ -run TestMigrate_V2EnrichmentColumns -v`
Expected: FAIL — `schemaVersion=1` and columns missing.

- [ ] **Step 3: Write minimal implementation**

In `migrate.go`, change `const schemaVersion = 1` to `const schemaVersion = 2`, and append a second element to the `migrations` slice (after the v1 string):

```go
	// v1 -> v2: PR enrichment columns (kind/languages/size/urgency). One
	// column per ALTER (SQLite limit); defaults backfill existing rows so
	// the new Scan targets are never NULL.
	`
ALTER TABLE pull_request ADD COLUMN kind            TEXT    NOT NULL DEFAULT '';
ALTER TABLE pull_request ADD COLUMN languages       TEXT    NOT NULL DEFAULT '[]';
ALTER TABLE pull_request ADD COLUMN size            TEXT    NOT NULL DEFAULT '';
ALTER TABLE pull_request ADD COLUMN urgency         TEXT    NOT NULL DEFAULT '';
ALTER TABLE pull_request ADD COLUMN urgency_score   INTEGER NOT NULL DEFAULT 0;
ALTER TABLE pull_request ADD COLUMN urgency_reasons TEXT    NOT NULL DEFAULT '[]';
`,
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/store/ -run TestMigrate -v`
Expected: PASS (new test + existing migration tests).

- [ ] **Step 5: Commit**

```bash
git add packages/pg-pr/internal/store/migrate.go packages/pg-pr/internal/store/migrate_test.go
git commit -m "feat(pg-pr): store migration v2 — PR enrichment columns

Refs pg2-4c5i.10"
```

---

### Task 7: store — PullRequest fields, shared scan, SetEnrichment (no-clobber)

**Files:**

- Modify: `packages/pg-pr/internal/store/pull_request.go`
- Test: `packages/pg-pr/internal/store/pull_request_test.go`

- [ ] **Step 1: Write the failing test**

Append to `pull_request_test.go`:

```go
func TestSetEnrichment_RoundTripAndNoClobber(t *testing.T) {
	ctx := context.Background()
	db := OpenForTest(t)

	base := PullRequest{Repo: "o/r", Number: 5, Ownership: "mine", Author: "me", State: "open", Branch: "b"}
	if _, err := db.UpsertPR(ctx, base); err != nil {
		t.Fatalf("seed UpsertPR: %v", err)
	}

	enr := Enrichment{
		Kind: "bugfix", Languages: []string{"Go", "Nix"}, Size: "M",
		Urgency: "high", UrgencyScore: 5, UrgencyReasons: []string{"label:p0", "ci-failing"},
	}
	if err := db.SetEnrichment(ctx, "o/r", 5, enr); err != nil {
		t.Fatalf("SetEnrichment: %v", err)
	}

	got, err := db.GetPR(ctx, "o/r", 5)
	if err != nil || got == nil {
		t.Fatalf("GetPR: %v %v", got, err)
	}
	if got.Kind != "bugfix" || got.Size != "M" || got.Urgency != "high" || got.UrgencyScore != 5 {
		t.Fatalf("enrichment not persisted: %+v", got)
	}
	if !reflect.DeepEqual(got.Languages, []string{"Go", "Nix"}) || !reflect.DeepEqual(got.UrgencyReasons, []string{"label:p0", "ci-failing"}) {
		t.Fatalf("json columns not persisted: langs=%v reasons=%v", got.Languages, got.UrgencyReasons)
	}

	// A subsequent plain UpsertPR (as the lifecycle emit / ingest does) MUST
	// NOT clobber the enrichment columns.
	if _, err := db.UpsertPR(ctx, base); err != nil {
		t.Fatalf("re-UpsertPR: %v", err)
	}
	got2, err := db.GetPR(ctx, "o/r", 5)
	if err != nil || got2 == nil {
		t.Fatalf("GetPR2: %v %v", got2, err)
	}
	if got2.Kind != "bugfix" || got2.Urgency != "high" || !reflect.DeepEqual(got2.Languages, []string{"Go", "Nix"}) {
		t.Fatalf("UpsertPR clobbered enrichment: %+v", got2)
	}
}
```

Add `"context"` and `"reflect"` to the test imports if not present.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/store/ -run TestSetEnrichment_RoundTripAndNoClobber -v`
Expected: FAIL — `undefined: Enrichment` / `db.SetEnrichment undefined` / enrichment fields missing on PullRequest.

- [ ] **Step 3: Write minimal implementation**

In `pull_request.go`:

(a) Add fields to the `PullRequest` struct (after `LastSyncedAt`):

```go
	Kind           string
	Languages      []string
	Size           string
	Urgency        string
	UrgencyScore   int
	UrgencyReasons []string
```

(b) Add `"encoding/json"` to the imports. Add the shared column list + scan helper + the Enrichment type and SetEnrichment:

```go
// prColumns is the canonical SELECT column order; scanPR must match it.
const prColumns = `id, repo, number, ownership, author, state, branch, base, url,
	head_sha, last_synced_at, kind, languages, size, urgency, urgency_score, urgency_reasons`

type rowScanner interface{ Scan(dest ...any) error }

// scanPR scans one pull_request row (in prColumns order), decoding the JSON
// languages/urgency_reasons columns.
func scanPR(s rowScanner) (PullRequest, error) {
	var pr PullRequest
	var langs, reasons string
	if err := s.Scan(&pr.ID, &pr.Repo, &pr.Number, &pr.Ownership, &pr.Author,
		&pr.State, &pr.Branch, &pr.Base, &pr.URL, &pr.HeadSHA, &pr.LastSyncedAt,
		&pr.Kind, &langs, &pr.Size, &pr.Urgency, &pr.UrgencyScore, &reasons); err != nil {
		return pr, err
	}
	pr.Languages = decodeJSONSlice(langs)
	pr.UrgencyReasons = decodeJSONSlice(reasons)
	return pr, nil
}

func decodeJSONSlice(s string) []string {
	if s == "" || s == "[]" {
		return nil
	}
	var out []string
	_ = json.Unmarshal([]byte(s), &out)
	return out
}

// Enrichment is the computed enrichment payload persisted by SetEnrichment.
// Kept store-local (no dependency on internal/enrich) so the store package
// stays free of go-enry.
type Enrichment struct {
	Kind           string
	Languages      []string
	Size           string
	Urgency        string
	UrgencyScore   int
	UrgencyReasons []string
}

// SetEnrichment writes ONLY the enrichment columns for an existing PR row via a
// targeted UPDATE. These columns are deliberately absent from UpsertPR, so a
// lifecycle upsert (or ingest's full-row upsert) cannot clobber them. A missing
// row is a no-op (0 rows affected); the lifecycle emit always creates the row
// first.
func (db *DB) SetEnrichment(ctx context.Context, repo string, number int, e Enrichment) error {
	langs, err := json.Marshal(nonNilSlice(e.Languages))
	if err != nil {
		return fmt.Errorf("store: marshal languages: %w", err)
	}
	reasons, err := json.Marshal(nonNilSlice(e.UrgencyReasons))
	if err != nil {
		return fmt.Errorf("store: marshal urgency_reasons: %w", err)
	}
	_, err = db.sql.ExecContext(ctx, `
UPDATE pull_request SET kind=?, languages=?, size=?, urgency=?, urgency_score=?, urgency_reasons=?, updated_at=?
WHERE repo=? AND number=?`,
		e.Kind, string(langs), e.Size, e.Urgency, e.UrgencyScore, string(reasons), nowRFC3339(), repo, number)
	if err != nil {
		return fmt.Errorf("store: set enrichment %s#%d: %w", repo, number, err)
	}
	return nil
}

func nonNilSlice(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
```

(c) Replace the three SELECT bodies to use `prColumns` + `scanPR`. `GetPR`:

```go
func (db *DB) GetPR(ctx context.Context, repo string, number int) (*PullRequest, error) {
	row := db.sql.QueryRowContext(ctx,
		"SELECT "+prColumns+" FROM pull_request WHERE repo=? AND number=?", repo, number)
	pr, err := scanPR(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: get pr %s#%d: %w", repo, number, err)
	}
	return &pr, nil
}
```

`GetPRByID`:

```go
func (db *DB) GetPRByID(ctx context.Context, id int64) (*PullRequest, error) {
	row := db.sql.QueryRowContext(ctx, "SELECT "+prColumns+" FROM pull_request WHERE id=?", id)
	pr, err := scanPR(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: get pr id=%d: %w", id, err)
	}
	return &pr, nil
}
```

`ListOpenPRs`:

```go
func (db *DB) ListOpenPRs(ctx context.Context, repo string) ([]PullRequest, error) {
	rows, err := db.sql.QueryContext(ctx,
		"SELECT "+prColumns+" FROM pull_request WHERE repo=? AND state IN ('open','draft')", repo)
	if err != nil {
		return nil, fmt.Errorf("store: list open prs %s: %w", repo, err)
	}
	defer func() { _ = rows.Close() }()
	var out []PullRequest
	for rows.Next() {
		pr, err := scanPR(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan open pr: %w", err)
		}
		out = append(out, pr)
	}
	return out, rows.Err()
}
```

`UpsertPR` is left UNCHANGED (it must not list enrichment columns).

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/store/ -v`
Expected: PASS (new test + all existing store tests, proving the SELECT refactor didn't regress).

- [ ] **Step 5: Commit**

```bash
git add packages/pg-pr/internal/store/pull_request.go packages/pg-pr/internal/store/pull_request_test.go
git commit -m "feat(pg-pr): store enrichment columns + SetEnrichment (decoupled, no-clobber)

Refs pg2-4c5i.10"
```

---

### Task 8: api.PR + EnrichedPR fields for the new raw data

**Files:**

- Modify: `packages/pg-pr/pkg/api/pr.go` (PR struct)
- Modify: `packages/pg-pr/pkg/provider/vcs/iface.go` (EnrichedPR struct)

- [ ] **Step 1: Add fields (no behavior change; covered by Task 9/10 tests)**

In `pkg/api/pr.go`, add to the `PR` struct (after `HeadSHA`):

```go
	// Body is the PR description. Used by enrichment's urgency keyword scan.
	Body string `json:"body,omitempty"`
	// Labels are the PR's label names. Used by enrichment's urgency signal.
	Labels []string `json:"labels,omitempty"`
```

In `pkg/provider/vcs/iface.go`, add to the `EnrichedPR` struct (after `CIRuns`):

```go
	// Files are the changed-file paths (for language detection). Empty when
	// not fetched (REST fallback) or truncated on a very large PR.
	Files []string
	// Commits are the PR's commit messages (for kind/urgency). Empty on the
	// REST fallback path.
	Commits []string
```

- [ ] **Step 2: Verify it compiles**

Run: `go build ./...`
Expected: success.

- [ ] **Step 3: Commit**

```bash
git add packages/pg-pr/pkg/api/pr.go packages/pg-pr/pkg/provider/vcs/iface.go
git commit -m "feat(pg-pr): api.PR.Body/Labels + EnrichedPR.Files/Commits

Refs pg2-4c5i.10"
```

---

### Task 9: GitHub GraphQL enrich — fetch body/labels/files/commits

**Files:**

- Modify: `packages/pg-pr/pkg/provider/vcs/github/enrich.go`
- Test: `packages/pg-pr/pkg/provider/vcs/github/enrich_test.go`

Context: `enrich.go` holds the GraphQL query string, a response-node struct (around `:200-220`), the node→`EnrichedPR` mapping (around `:360-420`), and `truncationFlags` (around `:482-504`). Read those before editing.

- [ ] **Step 1: Write the failing test**

Add a test that feeds a sample GraphQL JSON node through the existing decode/map path and asserts the new fields land. Mirror the existing decode test in `enrich_test.go` (find how it constructs a node — reuse that helper). Assert:

```go
func TestEnrich_MapsBodyLabelsFilesCommits(t *testing.T) {
	// Build a node JSON with body, labels, files, commits populated, run it
	// through the same unmarshal+map path the provider uses, and assert:
	//   ep.PR.Body == "production incident"
	//   ep.PR.Labels == []string{"p0"}
	//   ep.Files == []string{"a.go", "b.py"}
	//   ep.Commits == []string{"fix: x"}
	// (Use the existing test's node-construction + map helper; assert the four
	// new fields on the resulting vcs.EnrichedPR.)
}
```

Implement the test body using the SAME decode entry point the existing `enrich_test.go` tests call (e.g. the exported `EnrichedPRs` with a fake GraphQL runner, or the internal node-map function — match the file's existing pattern). The JSON must include:

```json
{
  "body": "production incident",
  "labels": { "nodes": [{ "name": "p0" }] },
  "files": { "nodes": [{ "path": "a.go" }, { "path": "b.py" }] },
  "commits": { "nodes": [{ "commit": { "message": "fix: x" } }] }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/provider/vcs/github/ -run TestEnrich_MapsBodyLabelsFilesCommits -v`
Expected: FAIL — fields empty (query/struct/map not updated).

- [ ] **Step 3: Write minimal implementation**

(a) Add to the GraphQL query (the `... on PullRequest { ... }` selection):

```graphql
        body
        labels(first: 20) { totalCount pageInfo { hasNextPage } nodes { name } }
        files(first: 100) { totalCount pageInfo { hasNextPage } nodes { path } }
        commits(last: 20) { totalCount pageInfo { hasNextPage } nodes { commit { message } } }
```

(b) Add matching fields to the response-node struct:

```go
	Body   string `json:"body"`
	Labels struct {
		PageInfo struct{ HasNextPage bool } `json:"pageInfo"`
		Nodes    []struct{ Name string }   `json:"nodes"`
	} `json:"labels"`
	Files struct {
		PageInfo struct{ HasNextPage bool } `json:"pageInfo"`
		Nodes    []struct{ Path string }   `json:"nodes"`
	} `json:"files"`
	Commits struct {
		PageInfo struct{ HasNextPage bool }            `json:"pageInfo"`
		Nodes    []struct{ Commit struct{ Message string } } `json:"nodes"`
	} `json:"commits"`
```

(c) In the node→`EnrichedPR` mapping, populate the new fields:

```go
	ep.PR.Body = n.Body
	for _, l := range n.Labels.Nodes {
		ep.PR.Labels = append(ep.PR.Labels, l.Name)
	}
	for _, f := range n.Files.Nodes {
		ep.Files = append(ep.Files, f.Path)
	}
	for _, c := range n.Commits.Nodes {
		ep.Commits = append(ep.Commits, c.Commit.Message)
	}
```

(d) In `truncationFlags`, add flags for the new connections (match the existing pattern):

```go
	if n.Files.PageInfo.HasNextPage {
		flags = append(flags, "files")
	}
	if n.Commits.PageInfo.HasNextPage {
		flags = append(flags, "commits")
	}
	if n.Labels.PageInfo.HasNextPage {
		flags = append(flags, "labels")
	}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/provider/vcs/github/ -v`
Expected: PASS (new test + existing).

- [ ] **Step 5: Commit**

```bash
git add packages/pg-pr/pkg/provider/vcs/github/
git commit -m "feat(pg-pr): GraphQL enrich fetches body/labels/files/commits (+truncation flags)

Refs pg2-4c5i.10"
```

---

### Task 10: GitHub REST fallback — body + labels on the list path

**Files:**

- Modify: `packages/pg-pr/pkg/provider/vcs/github/github.go` (`prListFields:118`, the list-node struct + map around `:136-156`)
- Test: `packages/pg-pr/pkg/provider/vcs/github/github_test.go`

- [ ] **Step 1: Write the failing test**

Find the existing test that decodes a `gh pr list --json` payload into `api.PR` (search `github_test.go` for `prListFields` or the list struct). Add a case asserting `body` and `labels` map through:

```go
func TestListPRs_MapsBodyAndLabels(t *testing.T) {
	// Feed a gh-pr-list JSON item with "body" and "labels":[{"name":"p0"}]
	// through the same decode+map path; assert pr.Body and pr.Labels==["p0"].
	// (gh returns labels as objects with a "name" field.)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/provider/vcs/github/ -run TestListPRs_MapsBodyAndLabels -v`
Expected: FAIL.

- [ ] **Step 3: Write minimal implementation**

(a) Append `,body,labels` to `prListFields` (`github.go:118`).
(b) Add to the list-node struct:

```go
	Body   string `json:"body"`
	Labels []struct {
		Name string `json:"name"`
	} `json:"labels"`
```

(c) In the node→`api.PR` map (around `:153`):

```go
	out.Body = p.Body
	for _, l := range p.Labels {
		out.Labels = append(out.Labels, l.Name)
	}
```

Do NOT add per-PR `files`/`commits` fetches on the REST path (extra round-trips / quota) — languages and the commit-majority kind tier degrade to empty there, which is acceptable.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/provider/vcs/github/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add packages/pg-pr/pkg/provider/vcs/github/
git commit -m "feat(pg-pr): REST PR list fetches body + labels for enrichment

Refs pg2-4c5i.10"
```

---

### Task 11: sync — compute + persist enrichment per observed PR

**Files:**

- Create: `packages/pg-pr/internal/sync/enrich.go` (engine helper)
- Modify: `packages/pg-pr/internal/sync/sync.go` (full-sync per-PR loop; `applyFetchedPR`)
- Test: `packages/pg-pr/internal/sync/enrich_test.go`

- [ ] **Step 1: Write the failing test**

```go
package sync

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/provider/vcs"
)

func TestEnrichAndStore_PersistsToRow(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	pr := api.PR{Repo: "o/r", Number: 3, Title: "fix: boom", Body: "production incident",
		Branch: "fix/boom", Author: "me", State: "open", Additions: 20, Deletions: 5}
	if _, err := db.UpsertPR(ctx, store.PullRequest{Repo: "o/r", Number: 3, Ownership: "mine", Author: "me", State: "open"}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	e := &Engine{deps: Deps{Store: db, Now: func() time.Time { return time.Unix(0, 0).UTC() }}}
	enriched := &vcs.EnrichedPR{PR: pr, Files: []string{"a.go"}, Commits: []string{"fix: boom"}}
	e.enrichAndStore(ctx, "o/r", pr, enriched)

	got, err := db.GetPR(ctx, "o/r", 3)
	if err != nil || got == nil {
		t.Fatalf("GetPR: %v %v", got, err)
	}
	if got.Kind != "bugfix" || got.Size != "S" || got.Urgency == "low" || len(got.Languages) == 0 {
		t.Fatalf("enrichment not persisted: %+v", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/sync/ -run TestEnrichAndStore_PersistsToRow -v`
Expected: FAIL — `e.enrichAndStore undefined`.

- [ ] **Step 3: Write minimal implementation**

Create `internal/sync/enrich.go`:

```go
package sync

import (
	"context"
	"fmt"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/enrich"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/provider/vcs"
)

// enrichAndStore computes enrichment for an observed open PR and persists it via
// the dedicated store.SetEnrichment write (decoupled from the lifecycle emit;
// the row already exists). No-op when Store is nil. Errors are non-fatal — they
// are not lifecycle events and self-heal on the next tick — so callers record
// them into summary.Errors. enriched may be nil (REST path): files/commits are
// then empty and enrichment degrades gracefully.
func (e *Engine) enrichAndStore(ctx context.Context, repo string, pr api.PR, enriched *vcs.EnrichedPR) error {
	if e.deps.Store == nil {
		return nil
	}
	in := enrich.Input{PR: pr, Labels: pr.Labels}
	if enriched != nil {
		in.Files = enriched.Files
		in.Commits = enriched.Commits
		in.CIRuns = enriched.CIRuns
		if len(pr.Labels) == 0 {
			in.Labels = enriched.PR.Labels
		}
	}
	r := enrich.Compute(in)
	if err := e.deps.Store.SetEnrichment(ctx, repo, pr.Number, store.Enrichment{
		Kind: r.Kind, Languages: r.Languages, Size: r.Size,
		Urgency: r.Urgency, UrgencyScore: r.UrgencyScore, UrgencyReasons: r.UrgencyReasons,
	}); err != nil {
		return fmt.Errorf("PR #%d enrich: %w", pr.Number, err)
	}
	return nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/sync/ -run TestEnrichAndStore_PersistsToRow -v`
Expected: PASS.

- [ ] **Step 5: Wire into the full-sync per-PR loop**

In `sync.go`, in the per-PR loop, AFTER the `emitPREvent` block succeeds (right after the `summary.BeadsCreated++ / BeadsUpdated++` accounting and before `processFeedback`), add:

```go
				// Compute + persist enrichment (decoupled from the lifecycle
				// emit; self-heals each tick). enrichByRepo carries files/commits
				// when the GraphQL bulk fetch ran; nil → graceful degradation.
				var prEnr *vcs.EnrichedPR
				if byNum := enrichByRepo[key.Repo]; byNum != nil {
					if ep, ok := byNum[pr.Number]; ok {
						prEnr = &ep
					}
				}
				if err := e.enrichAndStore(prCtx, key.Repo, pr, prEnr); err != nil {
					summary.Errors = append(summary.Errors, SummaryError{Repo: key.Repo, Message: err.Error()})
				}
```

Note: a `prEnriched` variable is already computed a few lines below for `processFeedback`; if it is in scope at this point, reuse it instead of recomputing `prEnr`. Otherwise hoist the existing `prEnriched` computation above this block and reuse it (DRY — do not duplicate the lookup).

- [ ] **Step 6: Wire into `applyFetchedPR`**

In `applyFetchedPR` (`sync.go`), after the `emitPREvent` call succeeds and before `processFeedback`, add:

```go
	if err := e.enrichAndStore(ctx, rcfg.Remote, *pr, enriched); err != nil {
		summary.Errors = append(summary.Errors, SummaryError{Repo: rcfg.Remote, Message: err.Error()})
	}
```

(`enriched *vcs.EnrichedPR` is already a parameter of `applyFetchedPR`.)

- [ ] **Step 7: Run the full sync + enrich + store suites**

Run: `go test ./internal/sync/ ./internal/store/ ./internal/enrich/ -count=1`
Expected: PASS (existing sync tests still green; close/summary unaffected).

- [ ] **Step 8: Commit**

```bash
git add packages/pg-pr/internal/sync/enrich.go packages/pg-pr/internal/sync/enrich_test.go packages/pg-pr/internal/sync/sync.go
git commit -m "feat(pg-pr): compute + persist PR enrichment during sync

Refs pg2-4c5i.10"
```

---

### Task 12: CLI — surface enrichment in `pg-pr pr info`

**Files:**

- Modify: `packages/pg-pr/cmd/pg-pr/pr.go` (`prInfoCmd:146-156`)
- Test: `packages/pg-pr/cmd/pg-pr/pr_test.go` (add; or the existing pr command test file)

Context: `prShowCmd` (`:120`) resolves repo via `resolveRepo` and the PR number from `args[0]`; reuse that. `prInfoCmd` currently has a stub `RunE`. The CLI does not open the store today — add a read-only `store.Open(store.DefaultPath())` + `GetPR`.

- [ ] **Step 1: Write the failing test**

Add a test that renders enrichment from a store row. Prefer testing a small pure renderer so the test needs no cobra wiring:

```go
func TestRenderEnrichment(t *testing.T) {
	pr := &store.PullRequest{
		Repo: "o/r", Number: 7, Kind: "bugfix", Size: "M", Urgency: "high",
		Languages: []string{"Go", "Nix"}, UrgencyReasons: []string{"label:p0"},
	}
	var b strings.Builder
	renderEnrichment(&b, pr)
	out := b.String()
	for _, want := range []string{"bugfix", "M", "high", "Go", "Nix", "label:p0"} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered output missing %q:\n%s", want, out)
		}
	}
}
```

Add imports `"strings"`, `"testing"`, and the `store` package.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/pg-pr/ -run TestRenderEnrichment -v`
Expected: FAIL — `undefined: renderEnrichment`.

- [ ] **Step 3: Write minimal implementation**

In `pr.go`, add the renderer and wire `prInfoCmd.RunE` to open the store and call it:

```go
// renderEnrichment writes the persisted enrichment fields for a PR.
func renderEnrichment(w io.Writer, pr *store.PullRequest) {
	fmt.Fprintf(w, "Kind:      %s\n", orDash(pr.Kind))
	fmt.Fprintf(w, "Size:      %s\n", orDash(pr.Size))
	fmt.Fprintf(w, "Languages: %s\n", orDash(strings.Join(pr.Languages, ", ")))
	fmt.Fprintf(w, "Urgency:   %s", orDash(pr.Urgency))
	if len(pr.UrgencyReasons) > 0 {
		fmt.Fprintf(w, " (%s)", strings.Join(pr.UrgencyReasons, ", "))
	}
	fmt.Fprintln(w)
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
```

Wire `prInfoCmd.RunE` (replace the stub):

```go
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		repo, err := resolveRepo(ctx, prRepoFlag)
		if err != nil {
			return err
		}
		num, err := strconv.Atoi(args[0])
		if err != nil {
			return fmt.Errorf("invalid PR number %q: %w", args[0], err)
		}
		db, err := store.Open(store.DefaultPath())
		if err != nil {
			return fmt.Errorf("open store: %w", err)
		}
		defer func() { _ = db.Close() }()
		pr, err := db.GetPR(ctx, repo, num)
		if err != nil {
			return err
		}
		if pr == nil {
			fmt.Fprintf(cmd.OutOrStdout(), "PR %s#%d not yet synced (no enrichment)\n", repo, num)
			return nil
		}
		renderEnrichment(cmd.OutOrStdout(), pr)
		return nil
	},
```

Add imports to `pr.go` as needed: `"strconv"`, `"strings"`, and the `store` package import path. Confirm `prRepoFlag` (or the equivalent repo flag var used by `prShowCmd`) — reuse the same flag variable `prShowCmd` uses; if it is a local flag, mirror `prShowCmd`'s repo resolution exactly. Ensure `prInfoCmd` declares `Args: cobra.ExactArgs(1)` (mirror `prShowCmd`).

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/pg-pr/ -run TestRenderEnrichment -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add packages/pg-pr/cmd/pg-pr/pr.go packages/pg-pr/cmd/pg-pr/pr_test.go
git commit -m "feat(pg-pr): surface enrichment in 'pg-pr pr info' (reads store)

Refs pg2-4c5i.10"
```

---

### Task 13: Full verification + gates

- [ ] **Step 1: Full module tests**

Run: `go test ./...`
Expected: all PASS.

- [ ] **Step 2: Vet + race on the touched packages**

Run: `go vet ./... && go test -race ./internal/enrich/ ./internal/store/ ./internal/sync/ -count=1`
Expected: clean / PASS.

- [ ] **Step 3: Pre-commit hooks on changed files**

Run (repo root): `prek run --files <all changed files>`
Expected: gofmt / golangci-lint / treefmt PASS. If treefmt reformats, `git add -A` and re-commit.

- [ ] **Step 4: Nix build (formal gate)**

Run (repo root): `nix build .#pg-pr --no-link`
Expected: exit 0 (confirms go-enry builds under gomod2nix, no-CGO).

- [ ] **Step 5: Close the bead**

```bash
bd close pg2-4c5i.10 --reason "PR enrichment (kind/languages/size/urgency) computed during sync, persisted via decoupled SetEnrichment, surfaced in 'pg-pr pr info'. Follow-ups pg2-4c5i.25/.26/.27."
bd dolt commit -m "close pg2-4c5i.10 (pg-pr #2 PR enrichment)"
```

---

## Self-Review (completed by plan author)

- **Spec coverage:** size→T1, kind→T2, urgency→T3, languages→T4, Compute→T5, migration→T6, store fields/SetEnrichment/no-clobber→T7, api/EnrichedPR fields→T8, GraphQL fetch+truncation→T9, REST body/labels→T10, sync compute+persist (decoupled, graceful degradation)→T11, CLI exposure→T12, gates+bead→T13. Non-goals (bead/dashboard projection) intentionally absent. Follow-up beads created (pg2-4c5i.25/.26/.27).
- **Decoupling invariant:** enrichment columns are written only by `SetEnrichment` and are absent from `UpsertPR`; T7's no-clobber test enforces this.
- **Type consistency:** `enrich.Input`/`enrich.Result` (T1–T5) ↔ `store.Enrichment` (T7) ↔ `enrichAndStore` conversion (T11) use matching field names; `store.PullRequest` enrichment fields (T7) ↔ `scanPR`/`renderEnrichment` (T7/T12).
- **Degradation honesty:** size always computes (counts on both paths); kind uses title/branch always, commit tier only on GraphQL; languages empty on REST/truncation — encoded in T10/T11 and the spec.
