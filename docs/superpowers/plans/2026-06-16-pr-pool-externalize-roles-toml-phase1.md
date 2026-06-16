# pr-pool externalize roles/prompts/queries to TOML — Phase 1 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move pr-pool's roles, prompts, and queries out of the Go binary into a repo-local `<RepoRoot>/.pr-pool/config.toml` (typed tagged unions), deleting the `RoleKind` enum in favor of code-owned config enums — with the no-config path byte-for-byte identical to today.

**Architecture:** New leaf packages (`item`, `report`, `prompt`, `query`), an instance-scoped factory `Registry` that decodes TOML via `BurntSushi/toml` `Primitive` (two-pass for per-element typo validation), an ordered `roles.RoleSet`, and a **thin `switch role.Type`** inside the existing orchestrator. Phase 1 deliberately does NOT extract an `Executor` interface or touch the `pg2-c1vp` watchdog/single-terminal code — that is Phase 2 (separate bead). `config.Load()` becomes `(Config, error)`.

**Tech Stack:** Go 1.25, `github.com/BurntSushi/toml` (new pr-pool dep), `text/template`, the existing `beads`/`ccpool`/`budget`/`eventlog`/`usage`/`watchdog` packages.

**Spec:** `docs/superpowers/specs/2026-06-16-pr-pool-externalize-roles-prompts-queries-toml-design.md`

---

## File Structure (Phase 1)

Import direction is strictly downward (no cycles):

```
internal/item/item.go              NEW  Item{ID,Type,Title,Metadata} (leaf, no in-repo imports)
internal/report/report.go          NEW  Verb/Ref/Action/Result value types (leaf)
internal/prompt/prompt.go          NEW  Parse/Render (text/template, missingkey=error) + safety preamble; imports item
internal/query/query.go            NEW  Query iface, Env, Commander, QueryFormat; imports item, beads
internal/query/beads.go            NEW  beads-ready / beads-list queries
internal/query/command.go          NEW  command query (+ os/exec Commander default)
internal/query/stubs.go            NEW  github-issues / jira-issues stubs
internal/query/factory.go          NEW  queryFactory type + RegisterDefaults(into a Registry)
internal/roles/enums.go            NEW  Completion / FailureAction / DispatchFailAction typed enums (UnmarshalText)
internal/roles/roles.go            MOD  Role (new shape) + RoleSet + CCPoolConfig/CommandConfig; DELETE RoleKind/Registry/Nudge consts; keep ExternalID/DisplayName
internal/roles/builtin.go          NEW  BuiltinRoleSet() — the in-Go default == canonical config.toml
internal/config/registry.go        NEW  instance Registry (role+query factory maps), two-pass decode
internal/config/config.go          MOD  Config gains RoleSet + [pool] scalars; Load()->(Config,error); Validate() over RoleSet
internal/config/example.go         NEW  RoleSet -> canonical TOML string (powers `config --print-defaults`)
internal/complete/complete.go      MOD  DoneSignal(Completion,...) / OnFailure(FailureAction,...)
internal/discover/discover.go      MOD  Discover/ForRole iterate RoleSet, run role.Query
internal/orchestrator/orchestrator.go  MOD  Reg->RoleSet; DrainOnce ranges; workOneWithID switches role.Type; emits report.Result
cmd/pr-pool/args.go                MOD  CLI tokens = role Name; knownRoles check deferred to handler; help text
cmd/pr-pool/config_cmd.go          NEW  `pr-pool config --print-defaults | --show`
cmd/pr-pool/runrole.go             MOD  resolveRole over configured RoleSet; precheck warnings
cmd/pr-pool/drain.go               MOD  config.Load() error handling
```

**Test command convention:** run a single test with
`(cd packages/pr-pool && go test ./internal/<pkg>/ -run <TestName> -v)`.
Full suite gate: `(cd packages/pr-pool && go test ./...)`.

---

## Task 1: `internal/item` — generalized work item

**Files:**

- Create: `packages/pr-pool/internal/item/item.go`
- Test: `packages/pr-pool/internal/item/item_test.go`

- [ ] **Step 1: Write the failing test**

```go
package item

import "testing"

func TestItem_zeroValueAndFields(t *testing.T) {
	it := Item{ID: "pg2-x", Type: "task", Title: "t", Metadata: map[string]any{"author": "me"}}
	if it.ID != "pg2-x" || it.Type != "task" || it.Title != "t" {
		t.Fatalf("fields not set: %+v", it)
	}
	if it.Metadata["author"] != "me" {
		t.Fatalf("metadata not set: %+v", it.Metadata)
	}
	var zero Item
	if zero.Metadata != nil {
		t.Fatalf("zero metadata should be nil")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `(cd packages/pr-pool && go test ./internal/item/ -run TestItem -v)`
Expected: FAIL — `undefined: Item` (build error).

- [ ] **Step 3: Write minimal implementation**

```go
// Package item holds pr-pool's generalized unit of work. A query yields Items;
// bead-backed queries map beads.Issue -> Item, command/future queries build it
// from their own source. Metadata carries source-specific fields exposed to prompt
// interpolation. Status/labels/created-by are NOT carried here — flows re-fetch
// them by ID (DoneSignal reads bd status; the created-marker diff reads bd list).
// This package is a leaf: it imports nothing in-repo (keeps the import DAG acyclic).
package item

type Item struct {
	ID       string
	Type     string
	Title    string
	Metadata map[string]any
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `(cd packages/pr-pool && go test ./internal/item/ -run TestItem -v)`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add packages/pr-pool/internal/item/
git commit -m "feat(pr-pool): add generalized item.Item (spec C phase 1)"
```

---

## Task 2: `internal/report` — structured dispatch action log

**Files:**

- Create: `packages/pr-pool/internal/report/report.go`
- Test: `packages/pr-pool/internal/report/report_test.go`

- [ ] **Step 1: Write the failing test**

```go
package report

import "testing"

func TestResult_actionsCarryVerbAndRefs(t *testing.T) {
	r := Result{Actions: []Action{
		{Verb: Created, Refs: []Ref{{Type: "bead", ID: "Y"}, {Type: "bead", ID: "Z"}}},
		{Verb: Closed, Refs: []Ref{{Type: "bead", ID: "X"}}},
	}}
	if r.Actions[0].Verb != Created || len(r.Actions[0].Refs) != 2 {
		t.Fatalf("created action wrong: %+v", r.Actions[0])
	}
	if r.Actions[1].Verb != Closed || r.Actions[1].Refs[0].ID != "X" {
		t.Fatalf("closed action wrong: %+v", r.Actions[1])
	}
}

func TestVerb_vocabulary(t *testing.T) {
	for _, v := range []Verb{Created, Closed, HandedBack, Unclaimed, Escalated, Indeterminate} {
		if v == "" {
			t.Fatal("verb constant is empty")
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `(cd packages/pr-pool && go test ./internal/report/ -v)`
Expected: FAIL — `undefined: Result`.

- [ ] **Step 3: Write minimal implementation**

```go
// Package report holds the structured, high-level summary of what one dispatch
// did (closed bead X, created beads Y/Z, ...). It supersedes the ad-hoc slog
// "created" markers. It is a pure-value leaf: it imports nothing in-repo, so any
// package (roles, orchestrator, complete, eventlog) can share these types without
// an import cycle. The vocabulary is closed and code-produced (not operator input).
package report

type Verb string

const (
	Created       Verb = "created"
	Closed        Verb = "closed"
	HandedBack    Verb = "handed-back"
	Unclaimed     Verb = "unclaimed"
	Escalated     Verb = "escalated"     // add-human
	Indeterminate Verb = "indeterminate" // preserves today's created="unknown" (snapshot read failed)
)

type Ref struct {
	Type string // "bead" today; expandable
	ID   string
}

type Action struct {
	Verb Verb
	Refs []Ref
}

type Result struct {
	Actions []Action
}

// Fields renders the Result for eventlog.Emit's flat fields map: a slice of
// {verb, refs:[{type,id}]} objects under the "actions" key.
func (r Result) Fields() map[string]any {
	acts := make([]map[string]any, 0, len(r.Actions))
	for _, a := range r.Actions {
		refs := make([]map[string]any, 0, len(a.Refs))
		for _, ref := range a.Refs {
			refs = append(refs, map[string]any{"type": ref.Type, "id": ref.ID})
		}
		acts = append(acts, map[string]any{"verb": string(a.Verb), "refs": refs})
	}
	return map[string]any{"actions": acts}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `(cd packages/pr-pool && go test ./internal/report/ -v)`
Expected: PASS.

- [ ] **Step 5: Add the no-in-repo-import guard test**

```go
// in report_test.go
func TestResult_fieldsForEventlog(t *testing.T) {
	r := Result{Actions: []Action{{Verb: Closed, Refs: []Ref{{Type: "bead", ID: "X"}}}}}
	f := r.Fields()
	acts, ok := f["actions"].([]map[string]any)
	if !ok || len(acts) != 1 || acts[0]["verb"] != "closed" {
		t.Fatalf("Fields() shape wrong: %#v", f)
	}
}
```

Run: `(cd packages/pr-pool && go test ./internal/report/ -v)` — Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add packages/pr-pool/internal/report/
git commit -m "feat(pr-pool): add report.Result dispatch action log (spec C phase 1)"
```

---

## Task 3: `internal/prompt` — interpolation + safety preamble

**Files:**

- Create: `packages/pr-pool/internal/prompt/prompt.go`
- Test: `packages/pr-pool/internal/prompt/prompt_test.go`

The render context is defined here and reused by roles/orchestrator.

- [ ] **Step 1: Write the failing test**

```go
package prompt

import (
	"strings"
	"testing"

	"github.com/phillipgreenii/pr-pool/internal/item"
)

func TestRender_namedFieldsAndMetadata(t *testing.T) {
	tmpl, err := Parse("worker", "bead {{.BeadID}} in {{.WorktreeDir}} by {{.SelfLogin}}; meta {{.Item.Metadata.author}}")
	if err != nil {
		t.Fatal(err)
	}
	ctx := Context{
		Item:        item.Item{ID: "pg2-x", Metadata: map[string]any{"author": "phillipg"}},
		WorktreeDir: "/wt",
		SelfLogin:   "phillipg",
	}
	got, err := Render(tmpl, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got != "bead pg2-x in /wt by phillipg; meta phillipg" {
		t.Fatalf("render = %q", got)
	}
}

func TestRender_missingKeyIsError(t *testing.T) {
	tmpl, err := Parse("x", "hi {{.Item.Metadata.nope}}")
	if err != nil {
		t.Fatal(err)
	}
	_, err = Render(tmpl, Context{Item: item.Item{Metadata: map[string]any{}}})
	if err == nil {
		t.Fatal("expected error on missing metadata key")
	}
}

func TestAuthorshipPreamble_present(t *testing.T) {
	p := AuthorshipPreamble()
	for _, want := range []string{"phillipg.", "git push --force", "human"} {
		if !strings.Contains(p, want) {
			t.Fatalf("preamble missing %q: %s", want, p)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `(cd packages/pr-pool && go test ./internal/prompt/ -v)`
Expected: FAIL — `undefined: Parse`.

- [ ] **Step 3: Write minimal implementation**

```go
// Package prompt renders role prompts via text/template (parsed once, reused) and
// supplies the code-owned, non-editable safety preamble. missingkey=error makes a
// typo'd variable fail loudly at render rather than silently inserting "".
package prompt

import (
	"strings"
	"text/template"

	"github.com/phillipgreenii/pr-pool/internal/item"
)

// Context is the interpolation surface available to every prompt template.
type Context struct {
	Item        item.Item
	WorktreeDir string
	SkillMD     string
	SelfLogin   string
	RepoRoot    string
}

// BeadID is a convenience alias for {{.BeadID}} == {{.Item.ID}}.
func (c Context) BeadID() string { return c.Item.ID }

// Parse compiles a prompt template once; callers store the result and Render per
// dispatch. name is used only in error messages.
func Parse(name, body string) (*template.Template, error) {
	return template.New(name).Option("missingkey=error").Parse(body)
}

// Render executes a parsed template against ctx.
func Render(t *template.Template, ctx Context) (string, error) {
	var sb strings.Builder
	if err := t.Execute(&sb, ctx); err != nil {
		return "", err
	}
	return sb.String(), nil
}

// AuthorshipPreamble is the fixed, code-owned safety block prepended to a ccpool
// role's task prompt when ccpool.authorship_guard is true. It is NOT in any
// prompt_file, so editing config cannot weaken it. (Spec C decision 4.)
func AuthorshipPreamble() string {
	return "Before doing anything: resolve this bead's PR + head branch from the " +
		"parent merge-request bead's metadata (repo, pr_number, branch). Assert " +
		"metadata.author is me AND the branch starts with 'phillipg.'. If you cannot " +
		"resolve the PR, it is not mine, or the branch is not phillipg.-prefixed, make " +
		"NO changes, comment why, and add the human label (bd update <bead> --add-label " +
		"human). NEVER git push --force (use --force-with-lease only if instructed).\n\n"
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `(cd packages/pr-pool && go test ./internal/prompt/ -v)`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add packages/pr-pool/internal/prompt/
git commit -m "feat(pr-pool): add prompt render + authorship preamble (spec C phase 1)"
```

---

## Task 4: `internal/query` — Query interface, Env, Commander, beads queries

**Files:**

- Create: `packages/pr-pool/internal/query/query.go`, `beads.go`
- Test: `packages/pr-pool/internal/query/beads_test.go`

- [ ] **Step 1: Write the failing test (beads-ready incl. post-filters)**

```go
package query

import (
	"context"
	"testing"
)

// fakeBD records args and returns canned bd JSON.
type fakeBD struct {
	args []string
	out  string
	err  error
}

func (f *fakeBD) Run(_ context.Context, args ...string) (string, error) {
	f.args = args
	return f.out, f.err
}

func TestBeadsReady_argsAndPostFilter(t *testing.T) {
	bd := &fakeBD{out: `[
	  {"id":"c1","issue_type":"task","title":"process-feedback: x"},
	  {"id":"c2","issue_type":"task","title":"not a cycle"},
	  {"id":"c3","issue_type":"bug","title":"process-feedback: y"}
	]`}
	q := BeadsReady{Labels: []string{"mine"}, ExcludeLabels: []string{"human"},
		TitlePrefix: "process-feedback:", ItemType: "task"}
	items, err := q.Run(context.Background(), Env{BD: bd})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != "c1" {
		t.Fatalf("post-filter wrong: %+v", items)
	}
	want := "ready --json --limit 0 --label mine --exclude-label human"
	if got := join(bd.args); got != want {
		t.Fatalf("args = %q want %q", got, want)
	}
}

func join(a []string) string {
	s := ""
	for i, x := range a {
		if i > 0 {
			s += " "
		}
		s += x
	}
	return s
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `(cd packages/pr-pool && go test ./internal/query/ -run TestBeadsReady -v)`
Expected: FAIL — `undefined: BeadsReady`.

- [ ] **Step 3: Write `query.go`**

```go
// Package query is pr-pool's typed-union of work sources. Each concrete query
// returns []item.Item; bead-backed queries map beads.Issue -> Item. Run errors are
// propagated, never returned as "no work" (pg2-qq9v): a bd/exec failure must not
// masquerade as an idle pool.
package query

import (
	"context"

	"github.com/phillipgreenii/pr-pool/internal/beads"
	"github.com/phillipgreenii/pr-pool/internal/item"
)

type QueryFormat string

const (
	FormatJSONL QueryFormat = "jsonl"
	FormatJSON  QueryFormat = "json"
)

// Commander runs an executable and returns its stdout (one-method interface, like
// beads.Runner / ccpool.Runner — not a bare func field).
type Commander interface {
	Run(ctx context.Context, argv []string) ([]byte, error)
}

// Env carries the capabilities a query needs. The orchestrator builds it from its
// own fields in phase 1 (the Deps bag arrives in phase 2).
type Env struct {
	BD       beads.Runner
	RepoRoot string
	Cmd      Commander
}

type Query interface {
	Validate() error
	Run(ctx context.Context, env Env) ([]item.Item, error)
}

// fromIssues maps bd issues to items (keeps item a leaf — the adapter lives here).
func fromIssues(in []beads.Issue) []item.Item {
	out := make([]item.Item, 0, len(in))
	for _, i := range in {
		out = append(out, item.Item{ID: i.ID, Type: i.Type, Title: i.Title, Metadata: i.Metadata})
	}
	return out
}
```

- [ ] **Step 4: Write `beads.go`**

```go
package query

import (
	"context"
	"fmt"
	"strings"

	"github.com/phillipgreenii/pr-pool/internal/beads"
	"github.com/phillipgreenii/pr-pool/internal/item"
)

// BeadsReady runs `bd ready` with label filters, then applies optional client-side
// title_prefix / item_type post-filters (the former feedback cycle-identity guard).
type BeadsReady struct {
	Labels        []string `toml:"labels"`
	ExcludeLabels []string `toml:"exclude_labels"`
	TitlePrefix   string   `toml:"title_prefix"`
	ItemType      string   `toml:"item_type"`
}

func (q BeadsReady) Validate() error { return nil }

func (q BeadsReady) Run(ctx context.Context, env Env) ([]item.Item, error) {
	issues, err := beads.Ready(ctx, env.BD, labelArgs(q.Labels, q.ExcludeLabels)...)
	if err != nil {
		return nil, fmt.Errorf("beads-ready query: %w", err)
	}
	return fromIssues(postFilter(issues, q.TitlePrefix, q.ItemType)), nil
}

// BeadsList runs `bd list` with the same filter shape.
type BeadsList struct {
	Labels        []string `toml:"labels"`
	ExcludeLabels []string `toml:"exclude_labels"`
	TitlePrefix   string   `toml:"title_prefix"`
	ItemType      string   `toml:"item_type"`
}

func (q BeadsList) Validate() error { return nil }

func (q BeadsList) Run(ctx context.Context, env Env) ([]item.Item, error) {
	issues, err := beads.List(ctx, env.BD, labelArgs(q.Labels, q.ExcludeLabels)...)
	if err != nil {
		return nil, fmt.Errorf("beads-list query: %w", err)
	}
	return fromIssues(postFilter(issues, q.TitlePrefix, q.ItemType)), nil
}

func labelArgs(labels, exclude []string) []string {
	var a []string
	for _, l := range labels {
		a = append(a, "--label", l)
	}
	for _, l := range exclude {
		a = append(a, "--exclude-label", l)
	}
	return a
}

func postFilter(in []beads.Issue, titlePrefix, itemType string) []beads.Issue {
	if titlePrefix == "" && itemType == "" {
		return in
	}
	var out []beads.Issue
	for _, i := range in {
		if itemType != "" && i.Type != itemType {
			continue
		}
		if titlePrefix != "" && !strings.HasPrefix(i.Title, titlePrefix) {
			continue
		}
		out = append(out, i)
	}
	return out
}
```

Note: `beads.Ready` prepends `ready --json --limit 0` itself (verify against `internal/beads/issue.go:45`); `labelArgs` supplies only the `--label`/`--exclude-label` tail, matching today's `beads.Ready(ctx, br, "--label", "mine", "--exclude-label", "human")` call in `discover.go:84`.

- [ ] **Step 5: Run test to verify it passes**

Run: `(cd packages/pr-pool && go test ./internal/query/ -run TestBeadsReady -v)`
Expected: PASS. (If the `want` arg string mismatches, align it to `beads.Ready`'s actual prefix from `issue.go:45` and re-run.)

- [ ] **Step 6: Commit**

```bash
git add packages/pr-pool/internal/query/query.go packages/pr-pool/internal/query/beads.go packages/pr-pool/internal/query/beads_test.go
git commit -m "feat(pr-pool): query.Query + beads-ready/beads-list (spec C phase 1)"
```

---

## Task 5: `internal/query` — command query + Commander default + stubs

**Files:**

- Create: `packages/pr-pool/internal/query/command.go`, `stubs.go`
- Test: `packages/pr-pool/internal/query/command_test.go`

- [ ] **Step 1: Write the failing test (exit codes + formats + propagation)**

```go
package query

import (
	"context"
	"errors"
	"testing"
)

type fakeCmd struct {
	out []byte
	err error
}

func (f fakeCmd) Run(_ context.Context, _ []string) ([]byte, error) { return f.out, f.err }

func TestCommandQuery_jsonl(t *testing.T) {
	q := CommandQuery{Argv: []string{"x"}, Format: FormatJSONL}
	cmd := fakeCmd{out: []byte(`{"id":"a","type":"task","title":"A","metadata":{"k":"v"}}` + "\n" +
		`{"id":"b","type":"bug","title":"B"}` + "\n")}
	items, err := q.Run(context.Background(), Env{Cmd: cmd})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].ID != "a" || items[0].Metadata["k"] != "v" || items[1].ID != "b" {
		t.Fatalf("items wrong: %+v", items)
	}
}

func TestCommandQuery_nonZeroExitPropagates(t *testing.T) {
	q := CommandQuery{Argv: []string{"x"}, Format: FormatJSONL}
	_, err := q.Run(context.Background(), Env{Cmd: fakeCmd{err: errors.New("exit 1")}})
	if err == nil {
		t.Fatal("non-zero exit must propagate as error, not empty items")
	}
}

func TestCommandQuery_emptyStdoutIsZeroItems(t *testing.T) {
	q := CommandQuery{Argv: []string{"x"}, Format: FormatJSONL}
	items, err := q.Run(context.Background(), Env{Cmd: fakeCmd{out: []byte("")}})
	if err != nil || len(items) != 0 {
		t.Fatalf("empty stdout + exit 0 = zero items, no error; got items=%v err=%v", items, err)
	}
}

func TestCommandQuery_malformedIsError(t *testing.T) {
	q := CommandQuery{Argv: []string{"x"}, Format: FormatJSONL}
	_, err := q.Run(context.Background(), Env{Cmd: fakeCmd{out: []byte("{not json}\n")}})
	if err == nil {
		t.Fatal("malformed output must error")
	}
}

func TestCommandQuery_missingIDIsError(t *testing.T) {
	q := CommandQuery{Argv: []string{"x"}, Format: FormatJSONL}
	_, err := q.Run(context.Background(), Env{Cmd: fakeCmd{out: []byte(`{"type":"task"}` + "\n")}})
	if err == nil {
		t.Fatal("record missing id must error")
	}
}

func TestStubQuery_runNotImplemented(t *testing.T) {
	for _, q := range []Query{GitHubIssues{Repo: "o/r"}, JiraIssues{Project: "P"}} {
		if err := q.Validate(); err != nil {
			t.Fatalf("stub Validate must pass: %v", err)
		}
		if _, err := q.Run(context.Background(), Env{}); err == nil {
			t.Fatal("stub Run must return not-implemented error")
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `(cd packages/pr-pool && go test ./internal/query/ -run 'TestCommandQuery|TestStubQuery' -v)`
Expected: FAIL — `undefined: CommandQuery`.

- [ ] **Step 3: Write `command.go`**

```go
package query

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"

	"github.com/phillipgreenii/pr-pool/internal/item"
)

// CommandQuery runs an executable and parses its stdout into items.
type CommandQuery struct {
	Argv   []string    `toml:"argv"`
	Format QueryFormat `toml:"format"`
}

func (q CommandQuery) Validate() error {
	if len(q.Argv) == 0 {
		return fmt.Errorf("command query: argv is required")
	}
	if q.Format != FormatJSONL && q.Format != FormatJSON {
		return fmt.Errorf("command query: format must be jsonl or json, got %q", q.Format)
	}
	return nil
}

type rawItem struct {
	ID       string         `json:"id"`
	Type     string         `json:"type"`
	Title    string         `json:"title"`
	Metadata map[string]any `json:"metadata"`
}

func (q CommandQuery) Run(ctx context.Context, env Env) ([]item.Item, error) {
	cmd := env.Cmd
	if cmd == nil {
		cmd = OSCommander{}
	}
	out, err := cmd.Run(ctx, q.Argv)
	if err != nil {
		return nil, fmt.Errorf("command query %v: %w", q.Argv, err)
	}
	var raws []rawItem
	switch q.Format {
	case FormatJSON:
		if len(bytes.TrimSpace(out)) == 0 {
			return nil, nil
		}
		if err := json.Unmarshal(out, &raws); err != nil {
			return nil, fmt.Errorf("command query: parse json: %w", err)
		}
	default: // jsonl
		sc := bufio.NewScanner(bytes.NewReader(out))
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			line := bytes.TrimSpace(sc.Bytes())
			if len(line) == 0 {
				continue
			}
			var r rawItem
			if err := json.Unmarshal(line, &r); err != nil {
				return nil, fmt.Errorf("command query: parse jsonl line: %w", err)
			}
			raws = append(raws, r)
		}
		if err := sc.Err(); err != nil {
			return nil, fmt.Errorf("command query: read output: %w", err)
		}
	}
	items := make([]item.Item, 0, len(raws))
	for _, r := range raws {
		if r.ID == "" {
			return nil, fmt.Errorf("command query: record missing required \"id\"")
		}
		items = append(items, item.Item{ID: r.ID, Type: r.Type, Title: r.Title, Metadata: r.Metadata})
	}
	return items, nil
}

// OSCommander is the default Commander: shells out via os/exec.
type OSCommander struct{}

func (OSCommander) Run(ctx context.Context, argv []string) ([]byte, error) {
	if len(argv) == 0 {
		return nil, fmt.Errorf("empty argv")
	}
	return exec.CommandContext(ctx, argv[0], argv[1:]...).Output()
}
```

- [ ] **Step 4: Write `stubs.go`**

```go
package query

import (
	"context"
	"fmt"

	"github.com/phillipgreenii/pr-pool/internal/item"
)

// GitHubIssues / JiraIssues are decode/validate stubs (spec C scope): they
// establish the union shape; Run is not yet implemented. Follow-up: see spec C
// "Out of scope / deferred".
type GitHubIssues struct {
	Repo   string   `toml:"repo"`
	Labels []string `toml:"labels"`
}

func (q GitHubIssues) Validate() error {
	if q.Repo == "" {
		return fmt.Errorf("github-issues query: repo is required")
	}
	return nil
}
func (q GitHubIssues) Run(context.Context, Env) ([]item.Item, error) {
	return nil, fmt.Errorf("github-issues query not yet implemented (spec C deferred)")
}

type JiraIssues struct {
	Project string   `toml:"project"`
	JQL     string   `toml:"jql"`
	Labels  []string `toml:"labels"`
}

func (q JiraIssues) Validate() error {
	if q.Project == "" && q.JQL == "" {
		return fmt.Errorf("jira-issues query: project or jql is required")
	}
	return nil
}
func (q JiraIssues) Run(context.Context, Env) ([]item.Item, error) {
	return nil, fmt.Errorf("jira-issues query not yet implemented (spec C deferred)")
}

// IsStub reports whether a query type is a not-yet-implemented stub (pre-flight
// warns on these while Validate still passes).
func IsStub(q Query) bool {
	switch q.(type) {
	case GitHubIssues, JiraIssues:
		return true
	}
	return false
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `(cd packages/pr-pool && go test ./internal/query/ -run 'TestCommandQuery|TestStubQuery' -v)`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add packages/pr-pool/internal/query/command.go packages/pr-pool/internal/query/stubs.go packages/pr-pool/internal/query/command_test.go
git commit -m "feat(pr-pool): command query (+OSCommander) and github/jira stubs (spec C phase 1)"
```

---

## Task 6: `internal/query` — query factory registration

**Files:**

- Create: `packages/pr-pool/internal/query/factory.go`
- Test: `packages/pr-pool/internal/query/factory_test.go`

The decode reads a `[role.query]` sub-table's `type`, then the factory decodes the
same-named sub-table held as a `toml.Primitive`.

- [ ] **Step 1: Add the toml dependency**

```bash
(cd packages/pr-pool && go get github.com/BurntSushi/toml@v1.6.0 && go mod tidy)
```

Then run the repo lock update per CLAUDE.md ("Third-party deps bump only via update-locks.sh"): `./packages/pr-pool/update-deps.sh` (or the workspace `update-locks.sh`). Verify `go.sum` now lists BurntSushi.

- [ ] **Step 2: Write the failing test**

```go
package query

import (
	"testing"

	"github.com/BurntSushi/toml"
)

func TestQueryFactory_decodesByType(t *testing.T) {
	body := `
type = "beads-ready"
[beads-ready]
labels = ["worker-ready"]
exclude_labels = ["human"]
`
	var md toml.MetaData
	var raw struct {
		Type      string                  `toml:"type"`
		Subtables map[string]toml.Primitive `toml:"-"`
	}
	// decode common + capture sub-tables as primitives
	prims := map[string]toml.Primitive{}
	md, err := toml.Decode(body, &struct {
		Type       string                    `toml:"type"`
		BeadsReady toml.Primitive            `toml:"beads-ready"`
	}{})
	_ = prims
	_ = raw
	if err != nil {
		t.Fatal(err)
	}
	reg := NewQueryFactories()
	q, err := reg.Decode("beads-ready", md, mustPrimitive(t, body, "beads-ready"))
	if err != nil {
		t.Fatal(err)
	}
	br, ok := q.(BeadsReady)
	if !ok || len(br.Labels) != 1 || br.Labels[0] != "worker-ready" {
		t.Fatalf("decoded query wrong: %#v", q)
	}
	if _, err := reg.Decode("nope", md, toml.Primitive{}); err == nil {
		t.Fatal("unknown query type must error")
	}
}

// mustPrimitive decodes body capturing the named sub-table as a Primitive.
func mustPrimitive(t *testing.T, body, key string) toml.Primitive {
	t.Helper()
	holder := map[string]toml.Primitive{}
	if _, err := toml.Decode(body, &holder); err != nil {
		t.Fatal(err)
	}
	return holder[key]
}
```

Note: the test exercises the factory contract (`type` string + `Primitive` → concrete `Query`). The exact two-pass plumbing is finalized in Task 9 (config decode); this task only proves the factory map + `PrimitiveDecode`.

- [ ] **Step 3: Run test to verify it fails**

Run: `(cd packages/pr-pool && go test ./internal/query/ -run TestQueryFactory -v)`
Expected: FAIL — `undefined: NewQueryFactories`.

- [ ] **Step 4: Write `factory.go`**

```go
package query

import (
	"fmt"
	"sort"

	"github.com/BurntSushi/toml"
)

// Factory decodes a query type's same-named sub-table (held as a Primitive) into a
// concrete Query, then validates it.
type Factory func(md toml.MetaData, prim toml.Primitive) (Query, error)

// Factories is an instance-scoped registry (NOT package-global init() maps — that
// fights the codebase's constructor-injection convention). NewQueryFactories seeds
// the built-in query types; adding a type is one line here.
type Factories struct{ m map[string]Factory }

func NewQueryFactories() *Factories {
	f := &Factories{m: map[string]Factory{}}
	f.m["beads-ready"] = decodeInto(func() Query { return &BeadsReady{} })
	f.m["beads-list"] = decodeInto(func() Query { return &BeadsList{} })
	f.m["command"] = decodeInto(func() Query { return &CommandQuery{} })
	f.m["github-issues"] = decodeInto(func() Query { return &GitHubIssues{} })
	f.m["jira-issues"] = decodeInto(func() Query { return &JiraIssues{} })
	return f
}

// decodeInto builds a Factory that PrimitiveDecodes into a fresh pointer of the
// concrete type, validates, and returns the value (dereferenced).
func decodeInto(make func() Query) Factory {
	return func(md toml.MetaData, prim toml.Primitive) (Query, error) {
		q := make()
		if err := md.PrimitiveDecode(prim, q); err != nil {
			return nil, err
		}
		if err := q.Validate(); err != nil {
			return nil, err
		}
		return derefQuery(q), nil
	}
}

func (f *Factories) Decode(typ string, md toml.MetaData, prim toml.Primitive) (Query, error) {
	fn, ok := f.m[typ]
	if !ok {
		return nil, fmt.Errorf("unknown query type %q (known: %s)", typ, f.known())
	}
	return fn(md, prim)
}

func (f *Factories) known() string {
	ks := make([]string, 0, len(f.m))
	for k := range f.m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return fmt.Sprint(ks)
}

// derefQuery converts the *T used for decoding back to the value form the rest of
// the package compares against (BeadsReady, not *BeadsReady).
func derefQuery(q Query) Query {
	switch v := q.(type) {
	case *BeadsReady:
		return *v
	case *BeadsList:
		return *v
	case *CommandQuery:
		return *v
	case *GitHubIssues:
		return *v
	case *JiraIssues:
		return *v
	}
	return q
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `(cd packages/pr-pool && go test ./internal/query/ -run TestQueryFactory -v)`
Expected: PASS. Also run the whole query package: `(cd packages/pr-pool && go test ./internal/query/ -v)`.

- [ ] **Step 6: Commit**

```bash
git add packages/pr-pool/go.mod packages/pr-pool/go.sum packages/pr-pool/internal/query/factory.go packages/pr-pool/internal/query/factory_test.go
git commit -m "feat(pr-pool): instance-scoped query factory registry + BurntSushi dep (spec C phase 1)"
```

---

## Task 7: `internal/roles` — typed enums (UnmarshalText)

**Files:**

- Create: `packages/pr-pool/internal/roles/enums.go`
- Test: `packages/pr-pool/internal/roles/enums_test.go`

- [ ] **Step 1: Write the failing test**

```go
package roles

import "testing"

func TestCompletion_unmarshalText(t *testing.T) {
	var c Completion
	if err := c.UnmarshalText([]byte("close-or-handback")); err != nil || c != CloseOrHandback {
		t.Fatalf("valid completion failed: c=%q err=%v", c, err)
	}
	if err := c.UnmarshalText([]byte("bogus")); err == nil {
		t.Fatal("invalid completion must error at decode")
	}
}

func TestFailureAction_unmarshalText(t *testing.T) {
	var a FailureAction
	if err := a.UnmarshalText([]byte("add-human")); err != nil || a != AddHuman {
		t.Fatalf("valid failure action failed: %v", err)
	}
	if err := a.UnmarshalText([]byte("nuke")); err == nil {
		t.Fatal("invalid failure action must error")
	}
}

func TestDispatchFailAction_unmarshalText(t *testing.T) {
	var d DispatchFailAction
	if err := d.UnmarshalText([]byte("leave")); err != nil || d != DispatchLeave {
		t.Fatalf("valid dispatch-fail failed: %v", err)
	}
	if err := d.UnmarshalText([]byte("x")); err == nil {
		t.Fatal("invalid dispatch-fail must error")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `(cd packages/pr-pool && go test ./internal/roles/ -run 'TestCompletion|TestFailureAction|TestDispatchFailAction' -v)`
Expected: FAIL — `undefined: Completion`.

- [ ] **Step 3: Write `enums.go`**

```go
package roles

import "fmt"

// Completion selects the bead-done semantics for a ccpool role. Go owns each value's
// implementation (incl. the seenClaimed startup-race guard for close-or-handback).
type Completion string

const (
	CloseOnly       Completion = "close-only"
	CloseOrHandback Completion = "close-or-handback"
)

func (c *Completion) UnmarshalText(b []byte) error {
	switch Completion(b) {
	case CloseOnly, CloseOrHandback:
		*c = Completion(b)
		return nil
	}
	return fmt.Errorf("invalid completion %q (valid: close-only, close-or-handback)", b)
}

// FailureAction is what to do to the bead when a dispatch is flagged.
type FailureAction string

const (
	Unclaim  FailureAction = "unclaim"
	AddHuman FailureAction = "add-human"
)

func (a *FailureAction) UnmarshalText(b []byte) error {
	switch FailureAction(b) {
	case Unclaim, AddHuman:
		*a = FailureAction(b)
		return nil
	}
	return fmt.Errorf("invalid on_failure %q (valid: unclaim, add-human)", b)
}

// DispatchFailAction is what to do when the nudge could not be SENT.
type DispatchFailAction string

const (
	DispatchUnclaim DispatchFailAction = "unclaim"
	DispatchLeave   DispatchFailAction = "leave"
)

func (d *DispatchFailAction) UnmarshalText(b []byte) error {
	switch DispatchFailAction(b) {
	case DispatchUnclaim, DispatchLeave:
		*d = DispatchFailAction(b)
		return nil
	}
	return fmt.Errorf("invalid on_dispatch_fail %q (valid: unclaim, leave)", b)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `(cd packages/pr-pool && go test ./internal/roles/ -run 'TestCompletion|TestFailureAction|TestDispatchFailAction' -v)`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add packages/pr-pool/internal/roles/enums.go packages/pr-pool/internal/roles/enums_test.go
git commit -m "feat(pr-pool): typed role behavior enums with decode-time validation (spec C phase 1)"
```

---

## Task 8: `internal/roles` — Role/RoleSet/configs + delete RoleKind; built-in default set

This task rewrites `roles.go` and is the hinge of the refactor. It deletes
`RoleKind`, `Registry`, `NewRegistry`, and the `Nudge`/`workerNudge`/`feedbackNudge`
consts, and removes the `config` import (config will now import roles, not vice
versa). It ports `roles_test.go`.

**Files:**

- Modify: `packages/pr-pool/internal/roles/roles.go` (full rewrite)
- Create: `packages/pr-pool/internal/roles/builtin.go`
- Modify: `packages/pr-pool/internal/roles/roles_test.go` (port)

- [ ] **Step 1: Write the new `roles_test.go` (ported + new)**

```go
package roles

import (
	"strings"
	"testing"
)

func TestExternalID_andDisplayName(t *testing.T) {
	r := Role{Name: "worker"}
	if got := r.ExternalID("pr-pool-", "zr-w.2", "STAMP"); got != "pr-pool-worker-zr-w.2-STAMP" {
		t.Fatalf("ExternalID = %q", got)
	}
	if got := r.DisplayName("pr-pool-", "zr-w.2"); got != "pr-pool-worker-zr-w.2" {
		t.Fatalf("DisplayName = %q", got)
	}
}

func TestBuiltinRoleSet_shape(t *testing.T) {
	rs := BuiltinRoleSet(BuiltinParams{WorktreeDir: "/wt", SkillMD: "S", WorkerSkillMD: "W", MaxFeedback: 1, MaxWorker: 1})
	if len(rs) != 2 || rs[0].Name != "feedback" || rs[1].Name != "worker" {
		t.Fatalf("builtin set wrong: %+v", rs)
	}
	fb, wk := rs[0], rs[1]
	if fb.CCPool.Completion != CloseOnly || fb.CCPool.OnFailure != Unclaim || fb.CCPool.OnDispatchFail != DispatchUnclaim || fb.CCPool.AuthorshipGuard {
		t.Fatalf("feedback behavior wrong: %+v", fb.CCPool)
	}
	if wk.CCPool.Completion != CloseOrHandback || wk.CCPool.OnFailure != AddHuman || wk.CCPool.OnDispatchFail != DispatchLeave || !wk.CCPool.AuthorshipGuard {
		t.Fatalf("worker behavior wrong: %+v", wk.CCPool)
	}
	if fb.CCPool.Actor != "pgii-pool__process-feedback" || wk.CCPool.Actor != "pgii-pool__worker" {
		t.Fatalf("actors wrong")
	}
}

func TestBuiltinWorkerPrompt_taskBodyHasNoRails(t *testing.T) {
	rs := BuiltinRoleSet(BuiltinParams{WorktreeDir: "/wt", MaxWorker: 1, MaxFeedback: 1})
	body := rs[1].CCPool.PromptBody
	for _, rail := range []string{"phillipg.", "git push --force"} {
		if strings.Contains(body, rail) {
			t.Fatalf("worker task body must NOT contain rail %q (it lives in the injected preamble)", rail)
		}
	}
	if !strings.Contains(body, "{{.BeadID}}") {
		t.Fatalf("worker task body should interpolate the bead id")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `(cd packages/pr-pool && go test ./internal/roles/ -run 'TestExternalID|TestBuiltin' -v)`
Expected: FAIL — `undefined: Role` / `BuiltinRoleSet`.

- [ ] **Step 3: Rewrite `roles.go`**

```go
// Package roles is pr-pool's role model: an ordered RoleSet of typed roles. A role
// carries a query and a type-specific config block (ccpool or command). RoleKind is
// gone — behavior is declared by config enums. This package does NOT import config
// (config imports roles to build the RoleSet), keeping the import DAG acyclic.
package roles

import (
	"text/template"

	"github.com/phillipgreenii/pr-pool/internal/budget"
	"github.com/phillipgreenii/pr-pool/internal/query"
)

// RoleSet is the ordered list of roles a drain dispatches (config order).
type RoleSet []Role

type Role struct {
	Name    string
	Type    string // "ccpool" | "command"
	Cap     int
	Enabled bool
	Query   query.Query
	CCPool  *CCPoolConfig  // set iff Type == "ccpool"
	Command *CommandConfig // set iff Type == "command"
}

// CCPoolConfig is the ccpool role type's behavior + launch config.
type CCPoolConfig struct {
	Actor           string
	SkillMD         string
	Completion      Completion
	OnFailure       FailureAction
	OnDispatchFail  DispatchFailAction
	AuthorshipGuard bool
	PromptBody      string             // the task prompt template source (no rails)
	Prompt          *template.Template // parsed PromptBody (missingkey=error)
	Budget          budget.Budget      // finite => watchdog + prompt line; unlimited => neither
}

// CommandConfig is the command role type's config.
type CommandConfig struct {
	Argv     []string
	ArgvTmpl []*template.Template // parsed Argv elements
}

// ExternalID builds the per-attempt ccpool external_id:
// <prefix><name>-<beadid>-<stamp>. The stamp makes it unique per attempt (ADR 0015).
func (r Role) ExternalID(prefix, beadID, stamp string) string {
	return prefix + r.Name + "-" + beadID + "-" + stamp
}

// DisplayName builds the stable per-bead ccpool --name label: <prefix><name>-<beadid>.
func (r Role) DisplayName(prefix, beadID string) string {
	return prefix + r.Name + "-" + beadID
}
```

- [ ] **Step 4: Write `builtin.go` (the no-config default == canonical config.toml)**

```go
package roles

import (
	"github.com/phillipgreenii/pr-pool/internal/budget"
	"github.com/phillipgreenii/pr-pool/internal/prompt"
	"github.com/phillipgreenii/pr-pool/internal/query"
)

// BuiltinParams carries the scalars the built-in roles need from config defaults.
type BuiltinParams struct {
	WorktreeDir   string
	SkillMD       string
	WorkerSkillMD string
	MaxFeedback   int
	MaxWorker     int
	WorkerBudget  budget.Budget
}

// feedbackPromptBody / workerPromptBody are the task prompts (worker rails removed —
// they are injected by the authorship preamble). Copied from the former
// roles.feedbackNudge / workerNudge, re-expressed as text/template.
const feedbackPromptBody = `Read {{.SkillMD}} and process process-feedback cycle {{.BeadID}}: claim it, read its feedback children (bd children {{.BeadID}}), resolve the parent PR bead and review the PR's existing open work beads (bd children <PR> --status=open). For each feedback, create a work bead (task/bug) as a child of the PR bead, discovered-from the feedback, and label it worker-ready (bd update <work> --add-label worker-ready) so the worker role will pick it up — but if that work matches an existing open work bead, link/update it (ensuring it is labeled worker-ready) instead of creating a duplicate. Do NOT apply fixes and do NOT work the new work beads. Close each feedback bead, then close the cycle with a one-line summary.`

const workerPromptBody = `Read {{.SkillMD}} and implement work bead {{.BeadID}}. Claim it (bd update {{.BeadID}} --claim). Resolve its PR + head branch bead-first from the parent merge-request bead's metadata (repo, pr_number, branch — no gh needed). Work in a clean isolated git worktree for that branch under {{.WorktreeDir}} (never start or leave it dirty), implement the change the bead describes, and commit it. Push ONLY if the bead's instructions say to (git push or git push --force-with-lease). Record what you did with bd comment FIRST, then end by EITHER closing the bead (bd close {{.BeadID}} — including when the work is already present at HEAD) OR, if handing it back, unclaiming it (bd update {{.BeadID}} --status=open --assignee=""). NEVER leave the bead in_progress; do not push by default.`

// BuiltinRoleSet returns the in-Go default role set (feedback then worker), identical
// in behavior to today. It is also what config/example.go serializes to TOML.
func BuiltinRoleSet(p BuiltinParams) RoleSet {
	fbTmpl, _ := prompt.Parse("feedback", feedbackPromptBody)
	wkTmpl, _ := prompt.Parse("worker", workerPromptBody)
	return RoleSet{
		{
			Name: "feedback", Type: "ccpool", Cap: p.MaxFeedback, Enabled: true,
			Query: query.BeadsReady{Labels: []string{"mine"}, ExcludeLabels: []string{"human"},
				TitlePrefix: "process-feedback:", ItemType: "task"},
			CCPool: &CCPoolConfig{
				Actor: "pgii-pool__process-feedback", SkillMD: p.SkillMD,
				Completion: CloseOnly, OnFailure: Unclaim, OnDispatchFail: DispatchUnclaim,
				AuthorshipGuard: false, PromptBody: feedbackPromptBody, Prompt: fbTmpl,
				Budget: budget.Budget{}, // unlimited => no watchdog
			},
		},
		{
			Name: "worker", Type: "ccpool", Cap: p.MaxWorker, Enabled: true,
			Query: query.BeadsReady{Labels: []string{"worker-ready"}, ExcludeLabels: []string{"human"}},
			CCPool: &CCPoolConfig{
				Actor: "pgii-pool__worker", SkillMD: p.WorkerSkillMD,
				Completion: CloseOrHandback, OnFailure: AddHuman, OnDispatchFail: DispatchLeave,
				AuthorshipGuard: true, PromptBody: workerPromptBody, Prompt: wkTmpl,
				Budget: p.WorkerBudget,
			},
		},
	}
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `(cd packages/pr-pool && go test ./internal/roles/ -v)`
Expected: PASS. (The package will not yet build against its old consumers — those are fixed in Tasks 9–13; `go test ./internal/roles/` compiles only this package + its deps, which is clean.)

- [ ] **Step 6: Commit**

```bash
git add packages/pr-pool/internal/roles/
git commit -m "refactor(pr-pool)!: roles RoleSet + config blocks, delete RoleKind; built-in default set (spec C phase 1)"
```

---

## Task 9: `internal/config` — decode, instance Registry, Load()->(Config,error)

This is the largest task: the two-pass TOML decode, the instance `Registry`, the
`[pool]` scalar overlay, role-set resolution (built-in vs TOML), and `Validate()`.

**Files:**

- Create: `packages/pr-pool/internal/config/registry.go`
- Modify: `packages/pr-pool/internal/config/config.go`
- Modify: `packages/pr-pool/internal/config/config_test.go` (add resolution tests)

- [ ] **Step 1: Write the failing tests (resolution + layering + validation)**

```go
// config_test.go additions
package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeCfg(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoad_noFile_builtinRoleSet(t *testing.T) {
	t.Setenv("PR_POOL_CONFIG", filepath.Join(t.TempDir(), "absent.toml"))
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Roles) != 2 || c.Roles[0].Name != "feedback" || c.Roles[1].Name != "worker" {
		t.Fatalf("no-file must yield built-in feedback+worker: %+v", c.Roles)
	}
}

func TestLoad_tomlReplacesBuiltins(t *testing.T) {
	p := writeCfg(t, `
[[role]]
name = "solo"
type = "ccpool"
cap = 2
enabled = true
[role.query]
type = "beads-ready"
[role.query.beads-ready]
labels = ["worker-ready"]
[role.ccpool]
actor = "a"
completion = "close-or-handback"
on_failure = "add-human"
on_dispatch_fail = "leave"
prompt = "do {{.BeadID}}"
`)
	t.Setenv("PR_POOL_CONFIG", p)
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Roles) != 1 || c.Roles[0].Name != "solo" || c.Roles[0].Cap != 2 {
		t.Fatalf("toml must replace built-ins: %+v", c.Roles)
	}
}

func TestLoad_malformedIsHardError(t *testing.T) {
	p := writeCfg(t, "this is = not valid toml [[[")
	t.Setenv("PR_POOL_CONFIG", p)
	if _, err := Load(); err == nil {
		t.Fatal("malformed config must be a hard error, not a silent fallback")
	}
}

func TestLoad_singleBracketRoleTypoIsError(t *testing.T) {
	p := writeCfg(t, "[role]\nname = \"x\"\n") // single bracket = the classic typo
	t.Setenv("PR_POOL_CONFIG", p)
	if _, err := Load(); err == nil {
		t.Fatal("[role] single-bracket table must error, not fall back to built-ins")
	}
}

func TestLoad_unknownTypeIsError(t *testing.T) {
	p := writeCfg(t, `
[[role]]
name = "x"
type = "weird"
cap = 1
[role.query]
type = "beads-ready"
[role.query.beads-ready]
labels = ["a"]
[role.weird]
foo = "bar"
`)
	t.Setenv("PR_POOL_CONFIG", p)
	if _, err := Load(); err == nil {
		t.Fatal("unknown role type must error")
	}
}

func TestLoad_promptXorPromptFile(t *testing.T) {
	p := writeCfg(t, `
[[role]]
name = "x"
type = "ccpool"
cap = 1
[role.query]
type = "beads-ready"
[role.query.beads-ready]
labels = ["a"]
[role.ccpool]
actor = "a"
completion = "close-only"
on_failure = "unclaim"
on_dispatch_fail = "unclaim"
prompt = "hi"
prompt_file = "x.md"
`)
	t.Setenv("PR_POOL_CONFIG", p)
	if _, err := Load(); err == nil {
		t.Fatal("prompt AND prompt_file must error (XOR)")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `(cd packages/pr-pool && go test ./internal/config/ -run TestLoad_ -v)`
Expected: FAIL — `Load()` returns one value / `c.Roles` undefined.

- [ ] **Step 3: Write `registry.go` (two-pass decode + role factories)**

```go
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/BurntSushi/toml"
	"github.com/phillipgreenii/pr-pool/internal/budget"
	"github.com/phillipgreenii/pr-pool/internal/prompt"
	"github.com/phillipgreenii/pr-pool/internal/query"
	"github.com/phillipgreenii/pr-pool/internal/roles"
)

// fileShape is the typed first-pass decode target.
type fileShape struct {
	Pool  poolTOML       `toml:"pool"`
	Roles []roleTOML     `toml:"role"`
	// Role catches the single-bracket [role] typo (a table, not an array).
	Role  *toml.Primitive `toml:"-"`
}

type poolTOML struct {
	SelfLogin string         `toml:"self_login"`
	MaxWait   *duration      `toml:"max_wait"`
	Budget    *budgetTOML    `toml:"budget"`
	// (extend with other pool scalars as needed; presence-checked via MetaData)
}

type budgetTOML struct {
	Tokens *int64    `toml:"tokens"`
	Cost   *int64    `toml:"cost"`
	Time   *duration `toml:"time"`
}

type roleTOML struct {
	Name    string         `toml:"name"`
	Type    string         `toml:"type"`
	Cap     int            `toml:"cap"`
	Enabled *bool          `toml:"enabled"`
	Query   queryTOML      `toml:"query"`
	CCPool  toml.Primitive `toml:"ccpool"`
	Command toml.Primitive `toml:"command"`
}

type queryTOML struct {
	Type string `toml:"type"`
	// concrete sub-tables held as primitives, decoded by the query factory
	BeadsReady   toml.Primitive `toml:"beads-ready"`
	BeadsList    toml.Primitive `toml:"beads-list"`
	Command      toml.Primitive `toml:"command"`
	GitHubIssues toml.Primitive `toml:"github-issues"`
	JiraIssues   toml.Primitive `toml:"jira-issues"`
}

// ccpoolTOML is decoded from the [role.ccpool] primitive (enums validate at decode).
type ccpoolTOML struct {
	Actor           string                   `toml:"actor"`
	SkillMD         string                   `toml:"skill_md"`
	Completion      roles.Completion         `toml:"completion"`
	OnFailure       roles.FailureAction      `toml:"on_failure"`
	OnDispatchFail  roles.DispatchFailAction `toml:"on_dispatch_fail"`
	AuthorshipGuard bool                     `toml:"authorship_guard"`
	Prompt          string                   `toml:"prompt"`
	PromptFile      string                   `toml:"prompt_file"`
	Budget          *budgetTOML              `toml:"budget"`
}

type commandTOML struct {
	Argv []string `toml:"argv"`
}

// Registry decodes the file's roles+queries. Instance-scoped (no init() globals).
type Registry struct{ queries *query.Factories }

func NewRegistry() *Registry { return &Registry{queries: query.NewQueryFactories()} }

// decodeRoleSet performs the two-pass decode and returns the resolved RoleSet, or
// nil to signal "no roles configured -> use built-ins".
func (r *Registry) decodeRoleSet(path, configDir string, def Config) (roles.RoleSet, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	// Pass 1: typed.
	var shape fileShape
	md, err := toml.Decode(string(body), &shape)
	if err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	// single-bracket [role] typo guard: a [role] table lands under key "role" as a
	// non-array. MetaData.Keys reports it; if present AND no [[role]] array, error.
	if hasSingleBracketRole(md) && len(shape.Roles) == 0 {
		return nil, fmt.Errorf("decode %s: found [role] (single bracket); did you mean [[role]] (array of tables)?", path)
	}
	if len(shape.Roles) == 0 {
		return nil, nil // pool-only / empty => built-ins
	}
	// Pass 2: per-element sub-table key enumeration (typo detection).
	var rawElems []map[string]toml.Primitive
	if _, err := toml.Decode(string(body), &struct {
		Role []map[string]toml.Primitive `toml:"role"`
	}{Role: rawElems}); err != nil {
		// best-effort; pass-1 already validated structure
	}

	var out roles.RoleSet
	var errs []error
	seen := map[string]bool{}
	for i, rt := range shape.Roles {
		role, err := r.buildRole(md, rt, configDir, def)
		if err != nil {
			errs = append(errs, fmt.Errorf("role[%d] %q: %w", i, rt.Name, err))
			continue
		}
		if seen[role.Name] {
			errs = append(errs, fmt.Errorf("duplicate role name %q", role.Name))
			continue
		}
		seen[role.Name] = true
		out = append(out, role)
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return out, nil
}

func (r *Registry) buildRole(md toml.MetaData, rt roleTOML, configDir string, def Config) (roles.Role, error) {
	if rt.Name == "" {
		return roles.Role{}, fmt.Errorf("name is required")
	}
	q, err := r.decodeQuery(md, rt.Query)
	if err != nil {
		return roles.Role{}, err
	}
	enabled := true
	if rt.Enabled != nil {
		enabled = *rt.Enabled
	}
	role := roles.Role{Name: rt.Name, Type: rt.Type, Cap: rt.Cap, Enabled: enabled, Query: q}
	switch rt.Type {
	case "ccpool":
		cc, err := buildCCPool(md, rt.CCPool, configDir, def)
		if err != nil {
			return roles.Role{}, err
		}
		role.CCPool = cc
	case "command":
		cmd, err := buildCommand(md, rt.Command)
		if err != nil {
			return roles.Role{}, err
		}
		role.Command = cmd
	default:
		return roles.Role{}, fmt.Errorf("unknown role type %q (known: ccpool, command)", rt.Type)
	}
	return role, nil
}

func (r *Registry) decodeQuery(md toml.MetaData, qt queryTOML) (query.Query, error) {
	prim, ok := map[string]toml.Primitive{
		"beads-ready":   qt.BeadsReady,
		"beads-list":    qt.BeadsList,
		"command":       qt.Command,
		"github-issues": qt.GitHubIssues,
		"jira-issues":   qt.JiraIssues,
	}[qt.Type]
	if !ok {
		return nil, fmt.Errorf("unknown query type %q", qt.Type)
	}
	return r.queries.Decode(qt.Type, md, prim)
}

func buildCCPool(md toml.MetaData, prim toml.Primitive, configDir string, def Config) (*roles.CCPoolConfig, error) {
	var ct ccpoolTOML
	if err := md.PrimitiveDecode(prim, &ct); err != nil {
		return nil, err
	}
	if (ct.Prompt == "") == (ct.PromptFile == "") {
		return nil, fmt.Errorf("ccpool role: exactly one of prompt / prompt_file is required")
	}
	body := ct.Prompt
	if ct.PromptFile != "" {
		b, err := os.ReadFile(filepath.Join(configDir, ct.PromptFile))
		if err != nil {
			return nil, fmt.Errorf("ccpool role: prompt_file: %w", err)
		}
		body = string(b)
	}
	tmpl, err := prompt.Parse("role-prompt", body)
	if err != nil {
		return nil, fmt.Errorf("ccpool role: prompt template: %w", err)
	}
	// dry-render so a {{.Typo}} fails at load, not mid-drain.
	if _, err := prompt.Render(tmpl, prompt.Context{Item: itemSentinel()}); err != nil {
		return nil, fmt.Errorf("ccpool role: prompt references unknown variable: %w", err)
	}
	b := def.WorkerBudget() // pool default budget
	overlayBudget(&b, ct.Budget)
	return &roles.CCPoolConfig{
		Actor: ct.Actor, SkillMD: ct.SkillMD,
		Completion: ct.Completion, OnFailure: ct.OnFailure, OnDispatchFail: ct.OnDispatchFail,
		AuthorshipGuard: ct.AuthorshipGuard, PromptBody: body, Prompt: tmpl, Budget: b,
	}, nil
}

func buildCommand(md toml.MetaData, prim toml.Primitive) (*roles.CommandConfig, error) {
	var ct commandTOML
	if err := md.PrimitiveDecode(prim, &ct); err != nil {
		return nil, err
	}
	if len(ct.Argv) == 0 {
		return nil, fmt.Errorf("command role: argv is required")
	}
	tmpls := make([]*templateT, 0) // see note below
	_ = tmpls
	return &roles.CommandConfig{Argv: ct.Argv}, nil // argv templates parsed in Task 11
}

func overlayBudget(b *budget.Budget, t *budgetTOML) {
	if t == nil {
		return
	}
	if t.Tokens != nil {
		b.Tokens = budget.Limit(*t.Tokens)
	}
	if t.Cost != nil {
		b.Cost = budget.Limit(*t.Cost)
	}
	if t.Time != nil {
		b.Time = t.Time.D
	}
}
```

Note: `hasSingleBracketRole(md)`, `duration`, `templateT`, and `itemSentinel()` are
small helpers — define `duration` as a `time.Duration` wrapper with `UnmarshalText`
(copy ccpool's `Duration`, `ccpool/internal/config/config.go:62-72`), `itemSentinel`
returns an `item.Item{ID:"x", Metadata: map[string]any{}}` for dry-render (use a
permissive render that tolerates missing metadata keys — render against a context
whose `Metadata` contains the keys referenced; simplest: skip metadata keys in the
dry-render by rendering with a `template.Option("missingkey=zero")` _copy_ for the
dry pass only, so unknown top-level fields still fail but metadata lookups don't).
`hasSingleBracketRole` checks `md.Keys()` for an exact `"role"` key that is a table
(not array) — if BurntSushi reports `role` as defined but `len(shape.Roles)==0`, treat
it as the typo.

- [ ] **Step 4: Modify `config.go` — add fields, change `Load()` signature, resolution**

```go
// add to Config:
//   Roles      roles.RoleSet
//   SelfLogin  string
//   ConfigPath string  // resolved path (for `config --show`)

// replace Load():
func Load() (Config, error) {
	c := Default()
	// pool-scalar env overlay (unchanged set) ...
	c.RepoRoot = envStr("PR_POOL_REPO_ROOT", c.RepoRoot)
	// ... (keep all existing PR_POOL_* scalar overlays EXCEPT the removed role ones:
	//      drop PR_POOL_MAX_FEEDBACK, PR_POOL_MAX_WORKER, PR_POOL_FEEDBACK_ENABLED,
	//      PR_POOL_WORKER_ENABLED, PR_POOL_SKILL_MD, PR_POOL_WORKER_SKILL_MD) ...

	path := envStr("PR_POOL_CONFIG", filepath.Join(c.RepoRoot, ".pr-pool", "config.toml"))
	c.ConfigPath = path
	reg := NewRegistry()
	if _, err := os.Stat(path); err == nil {
		// overlay [pool] scalars from TOML BEFORE building roles (so role budget
		// can inherit pool budget). Then decode roles.
		if err := overlayPool(&c, path); err != nil {
			return Config{}, err
		}
		rs, err := reg.decodeRoleSet(path, filepath.Dir(path), c)
		if err != nil {
			return Config{}, err
		}
		if rs != nil {
			c.Roles = rs
		}
	} else if !os.IsNotExist(err) {
		return Config{}, fmt.Errorf("stat %s: %w", path, err)
	}
	if c.Roles == nil {
		c.Roles = roles.BuiltinRoleSet(roles.BuiltinParams{
			WorktreeDir: c.WorktreeDir, SkillMD: c.SkillMD, WorkerSkillMD: c.WorkerSkillMD,
			MaxFeedback: c.MaxFeedback, MaxWorker: c.MaxWorker, WorkerBudget: c.WorkerBudget(),
		})
	}
	if err := c.Validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}
```

`overlayPool` decodes only the `[pool]` table over `c` using presence checks
(`MetaData.IsDefined("pool","key")`) for any non-zero-default bool/int scalar (the
zero-value-vs-unset gotcha). Keep `MaxFeedback`/`MaxWorker`/`SkillMD`/`WorkerSkillMD`
fields on `Config` (they feed `BuiltinParams`) but they are now set only by
`Default()` (env overlay removed) — built-in-only knobs.

Extend `Validate()` to range `c.Roles` and `errors.Join` each role's query
`Validate()` plus the existing `PermissionMode` check.

- [ ] **Step 5: Run tests to verify they pass**

Run: `(cd packages/pr-pool && go test ./internal/config/ -run TestLoad_ -v)`
Expected: PASS for all six. Iterate on `hasSingleBracketRole`/`overlayPool` until green.

- [ ] **Step 6: Commit**

```bash
git add packages/pr-pool/internal/config/
git commit -m "feat(pr-pool): config TOML decode, instance Registry, Load()->(Config,error) (spec C phase 1)"
```

---

## Task 10: `internal/config` — example serializer (`--print-defaults`)

**Files:**

- Create: `packages/pr-pool/internal/config/example.go`
- Test: `packages/pr-pool/internal/config/example_test.go`

- [ ] **Step 1: Write the failing test (round-trip: serialize built-ins -> re-decode -> same shape)**

```go
package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExampleTOML_roundTrips(t *testing.T) {
	body := ExampleTOML()
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PR_POOL_CONFIG", p)
	c, err := Load()
	if err != nil {
		t.Fatalf("example config must load: %v\n---\n%s", err, body)
	}
	if len(c.Roles) != 2 || c.Roles[0].Name != "feedback" || c.Roles[1].Name != "worker" {
		t.Fatalf("example must reproduce built-in feedback+worker: %+v", c.Roles)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `(cd packages/pr-pool && go test ./internal/config/ -run TestExampleTOML -v)`
Expected: FAIL — `undefined: ExampleTOML`.

- [ ] **Step 3: Write `example.go`**

Implement `ExampleTOML() string` returning a heavily-commented TOML literal whose
`[[role]]` entries are the exact serialization of `roles.BuiltinRoleSet(...)` (the
§8 defaults): the two roles with their `beads-ready` queries (worker; feedback with
`title_prefix`/`item_type`), `[role.ccpool]` enums, and an inline task prompt for
each (the `feedbackPromptBody`/`workerPromptBody` text). Lead with comments
explaining `[[role]]` (double-bracket = array element), that `[role.query.<type>]`
is a peer of `[role.ccpool]`, and that rails are auto-injected when
`authorship_guard = true`.

- [ ] **Step 4: Run test to verify it passes**

Run: `(cd packages/pr-pool && go test ./internal/config/ -run TestExampleTOML -v)`
Expected: PASS.

- [ ] **Step 5: Write `config.example.toml` to disk + commit**

```bash
(cd packages/pr-pool && go run ./internal/config/cmd/printdefaults > config.example.toml) 2>/dev/null || true
# If no helper main exists yet, generate it in Task 13 via `pr-pool config --print-defaults`
# and commit config.example.toml then. For now commit the serializer:
git add packages/pr-pool/internal/config/example.go packages/pr-pool/internal/config/example_test.go
git commit -m "feat(pr-pool): canonical config.example.toml serializer (spec C phase 1)"
```

---

## Task 11: `internal/complete` — enum-based DoneSignal/OnFailure (port tests)

**Files:**

- Modify: `packages/pr-pool/internal/complete/complete.go`
- Modify: `packages/pr-pool/internal/complete/complete_test.go` (port from RoleKind)

- [ ] **Step 1: Port `complete_test.go` to the enums**

```go
package complete

import (
	"context"
	"testing"

	"github.com/phillipgreenii/pr-pool/internal/roles"
)

func TestDoneSignal(t *testing.T) {
	cases := []struct {
		name        string
		completion  roles.Completion
		status      string
		seenClaimed bool
		want        bool
	}{
		{"close-only closed", roles.CloseOnly, "closed", false, true},
		{"close-only open not done", roles.CloseOnly, "open", false, false},
		{"close-only in_progress not done", roles.CloseOnly, "in_progress", true, false},
		{"handback closed", roles.CloseOrHandback, "closed", false, true},
		{"handback open after claim = done", roles.CloseOrHandback, "open", true, true},
		{"handback open pre-claim NOT done (startup race)", roles.CloseOrHandback, "open", false, false},
		{"handback in_progress not done", roles.CloseOrHandback, "in_progress", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DoneSignal(tc.completion, tc.status, tc.seenClaimed); got != tc.want {
				t.Errorf("DoneSignal(%q,%q,%v)=%v want %v", tc.completion, tc.status, tc.seenClaimed, got, tc.want)
			}
		})
	}
}

func TestOnFailure_addHuman(t *testing.T) {
	fr := &recRunner{}
	if err := OnFailure(context.Background(), fr, roles.AddHuman, "zr-w1"); err != nil {
		t.Fatal(err)
	}
	if !fr.has("update zr-w1 --add-label human") {
		t.Errorf("add-human failure must add human; calls=%v", fr.calls)
	}
	if fr.has("--status=open") {
		t.Errorf("add-human must NOT unclaim; calls=%v", fr.calls)
	}
}

func TestOnFailure_unclaim(t *testing.T) {
	fr := &recRunner{}
	if err := OnFailure(context.Background(), fr, roles.Unclaim, "zr-c1"); err != nil {
		t.Fatal(err)
	}
	if !fr.has("update zr-c1 --status=open --assignee=") {
		t.Errorf("unclaim failure must unclaim; calls=%v", fr.calls)
	}
	if fr.has("--add-label human") {
		t.Errorf("unclaim must NOT add human; calls=%v", fr.calls)
	}
}

// recRunner unchanged from the original test file (keep it).
```

(Keep the existing `recRunner`/`has`/`join` helpers from the current
`complete_test.go`.)

- [ ] **Step 2: Run test to verify it fails**

Run: `(cd packages/pr-pool && go test ./internal/complete/ -v)`
Expected: FAIL — `DoneSignal`/`OnFailure` signatures still take `RoleKind`/`Role`.

- [ ] **Step 3: Rewrite `complete.go`**

```go
// Package complete holds pr-pool's completion semantics, now driven by config enums
// instead of RoleKind. The polling loop lives in the orchestrator.
package complete

import (
	"context"

	"github.com/phillipgreenii/pr-pool/internal/beads"
	"github.com/phillipgreenii/pr-pool/internal/roles"
)

// DoneSignal reports whether a bead has completed for the given completion mode.
//   - close-only:        done iff status == "closed".
//   - close-or-handback: done iff "closed", OR (seenClaimed && "open") — the
//     seenClaimed guard prevents a freshly-dispatched, not-yet-claimed "open" bead
//     from being mistaken for a hand-back (the startup race).
func DoneSignal(c roles.Completion, status string, seenClaimed bool) bool {
	if status == "closed" {
		return true
	}
	if c == roles.CloseOrHandback && seenClaimed && status == "open" {
		return true
	}
	return false
}

// OnFailure applies the configured failure action:
//   - add-human: add the `human` label, never unclaim (a dead worker may hold a
//     half-built worktree; blind retry is unsafe).
//   - unclaim:   status=open, assignee cleared, so the next pass retries.
func OnFailure(ctx context.Context, br beads.Runner, action roles.FailureAction, beadID string) error {
	switch action {
	case roles.AddHuman:
		return beads.AddHuman(ctx, br, beadID)
	default: // unclaim
		return beads.Unclaim(ctx, br, beadID)
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `(cd packages/pr-pool && go test ./internal/complete/ -v)`
Expected: PASS (case count matches the original 7 DoneSignal + 2 OnFailure).

- [ ] **Step 5: Commit**

```bash
git add packages/pr-pool/internal/complete/
git commit -m "refactor(pr-pool): complete uses config enums, not RoleKind (spec C phase 1)"
```

---

## Task 12: `internal/discover` — iterate RoleSet, run role.Query

**Files:**

- Modify: `packages/pr-pool/internal/discover/discover.go`
- Create: `packages/pr-pool/internal/discover/discover_test.go`

- [ ] **Step 1: Write the failing test (order, Enabled skip, error propagation)**

```go
package discover

import (
	"context"
	"errors"
	"testing"

	"github.com/phillipgreenii/pr-pool/internal/item"
	"github.com/phillipgreenii/pr-pool/internal/query"
	"github.com/phillipgreenii/pr-pool/internal/roles"
)

type fakeQuery struct {
	items []item.Item
	err   error
}

func (f fakeQuery) Validate() error { return nil }
func (f fakeQuery) Run(context.Context, query.Env) ([]item.Item, error) {
	return f.items, f.err
}

func TestDiscover_orderAndEnabled(t *testing.T) {
	rs := roles.RoleSet{
		{Name: "a", Enabled: true, Query: fakeQuery{items: []item.Item{{ID: "1"}}}},
		{Name: "b", Enabled: false, Query: fakeQuery{items: []item.Item{{ID: "2"}}}},
		{Name: "c", Enabled: true, Query: fakeQuery{items: []item.Item{{ID: "3"}}}},
	}
	got, err := Discover(context.Background(), query.Env{}, rs)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Role.Name != "a" || got[0].Item.ID != "1" || got[1].Role.Name != "c" {
		t.Fatalf("order/enabled wrong: %+v", got)
	}
}

func TestDiscover_queryErrorPropagates(t *testing.T) {
	rs := roles.RoleSet{{Name: "a", Enabled: true, Query: fakeQuery{err: errors.New("bd down")}}}
	if _, err := Discover(context.Background(), query.Env{}, rs); err == nil {
		t.Fatal("a query error must propagate, not be swallowed as no-work")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `(cd packages/pr-pool && go test ./internal/discover/ -v)`
Expected: FAIL — `Discover` signature mismatch / `DispatchContext.Item` undefined.

- [ ] **Step 3: Rewrite `discover.go`**

```go
// Package discover turns each role's configured query into role→item dispatches,
// in config order, honoring each role's Enabled flag. Query errors propagate
// (pg2-qq9v): a query failure must not masquerade as "no ready work".
package discover

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/phillipgreenii/pr-pool/internal/item"
	"github.com/phillipgreenii/pr-pool/internal/query"
	"github.com/phillipgreenii/pr-pool/internal/roles"
)

// DispatchContext is one (role, item) dispatch plus the resolved template inputs.
type DispatchContext struct {
	Role roles.Role
	Item item.Item
}

func (d DispatchContext) Validate() error {
	var missing []string
	if d.Role.Name == "" {
		missing = append(missing, "role")
	}
	if d.Item.ID == "" {
		missing = append(missing, "item")
	}
	if len(missing) > 0 {
		return fmt.Errorf("dispatch context missing required field(s): %s", strings.Join(missing, ", "))
	}
	return nil
}

// Discover runs each enabled role's query in config order.
func Discover(ctx context.Context, env query.Env, rs roles.RoleSet) ([]DispatchContext, error) {
	var out []DispatchContext
	for _, role := range rs {
		if !role.Enabled {
			slog.Info("role disabled; skipping discovery", "role", role.Name)
			continue
		}
		dcs, err := ForRole(ctx, env, role)
		if err != nil {
			return nil, err
		}
		out = append(out, dcs...)
	}
	return out, nil
}

// ForRole runs ONE role's query regardless of Enabled (the smoke harness needs it).
func ForRole(ctx context.Context, env query.Env, role roles.Role) ([]DispatchContext, error) {
	items, err := role.Query.Run(ctx, env)
	if err != nil {
		return nil, fmt.Errorf("discover %s: %w", role.Name, err)
	}
	out := make([]DispatchContext, 0, len(items))
	for _, it := range items {
		out = append(out, DispatchContext{Role: role, Item: it})
	}
	return out, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `(cd packages/pr-pool && go test ./internal/discover/ -v)`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add packages/pr-pool/internal/discover/
git commit -m "refactor(pr-pool): discover iterates RoleSet and runs role queries (spec C phase 1)"
```

---

## Task 13: `internal/orchestrator` — RoleSet range + type-switch + report.Result (port tests)

This wires everything: `Reg roles.Registry` → `Reg roles.RoleSet`, `DrainOnce` ranges
over roles, `workOneWithID` switches `role.Type` and reads `role.CCPool.*` enums, and
each dispatch returns a `report.Result`. The watchdog/single-terminal code is CALLED
unchanged (Phase 2 extracts it). Port `orchestrator_test.go`.

**Files:**

- Modify: `packages/pr-pool/internal/orchestrator/orchestrator.go`
- Modify: `packages/pr-pool/internal/orchestrator/orchestrator_test.go` (port `reg` construction + ext-id names)

- [ ] **Step 1: Port test fixtures** — replace `roles.NewRegistry(cfg)` / `reg.Feedback` / `reg.Worker` with a helper building `roles.BuiltinRoleSet(...)`; replace `o.Reg = reg` with `o.Reg = builtinRoleSet`; keep `testStamp`. Existing ext-id strings (`pr-pool-feedback-processor-...`) change to the new role names (`pr-pool-feedback-...`, `pr-pool-worker-...`) — update the literals at `orchestrator_test.go:660,678,701,722`. Keep the two `TestWaitDone_lostRace_*` and `TestDrainOnce_noStarvation` cases (now over the RoleSet).

- [ ] **Step 2: Run tests to verify they fail to compile**

Run: `(cd packages/pr-pool && go test ./internal/orchestrator/ -v)`
Expected: FAIL — `o.Reg.Feedback` undefined / `roles.Worker` undefined.

- [ ] **Step 3: Edit `orchestrator.go`**

Changes (keep everything else, esp. `workerWaitWithWatchdog`/`waitDone`, byte-for-byte):

```go
// struct field:
//   Reg roles.RoleSet   // was roles.Registry

// DrainOnce: replace the two explicit drains with a range:
func (o *Orchestrator) DrainOnce(ctx context.Context) error {
	if o.gated() {
		slog.Info("gated; pausing without dispatch")
		return nil
	}
	defer o.teardownAll(ctx)
	env := query.Env{BD: o.BD, RepoRoot: o.Cfg.RepoRoot, Cmd: query.OSCommander{}}
	dispatches, err := discover.Discover(ctx, env, o.Reg)
	if err != nil {
		return fmt.Errorf("discover: %w", err)
	}
	slog.Info("discover", "found", len(dispatches))
	var complete, flagged int
	for _, role := range o.Reg {
		c, f := o.drain(ctx, role, dispatches)
		complete += c
		flagged += f
	}
	slog.Info("done", "complete", complete, "flagged", flagged)
	return nil
}

// drain: filter by Name (was role.Kind):
//   if d.Role.Name != role.Name { continue }

// workOneWithID: replace the RoleKind branches with role.Type + CCPool reads.
```

`workOneWithID` body (ccpool path; command path added next step):

```go
func (o *Orchestrator) workOneWithID(ctx context.Context, d discover.DispatchContext, externalID string) (report.Result, error) {
	switch d.Role.Type {
	case "command":
		return o.runCommand(ctx, d)
	default: // ccpool
		return o.runCCPool(ctx, d, externalID)
	}
}

func (o *Orchestrator) runCCPool(ctx context.Context, d discover.DispatchContext, externalID string) (report.Result, error) {
	cc := d.Role.CCPool
	display := d.Role.DisplayName(o.Cfg.SessionPrefix, d.Item.ID)
	env := map[string]string{
		"BEADS_ACTOR":    cc.Actor,
		"BEADS_DIR":      o.Cfg.RepoRoot + "/.beads",
		"WORKSPACE_ROOT": o.Cfg.RepoRoot,
	}
	if err := o.CC.Ensure(ctx, externalID, display, o.Cfg.RepoRoot, env); err != nil {
		o.escalateLaunchFailure(ctx, d.Item.ID)
		return report.Result{}, fmt.Errorf("ensure %s: %w", externalID, err)
	}
	_ = beads.RemoveLabel(ctx, o.BD, d.Item.ID, "pool-launch-fail")
	nudge := o.renderNudge(cc, d) // preamble (if guard) + rendered task + budget line (if finite)
	if err := o.CC.Send(ctx, externalID, nudge, ccpool.ModeNoWait); err != nil {
		if cc.OnDispatchFail == roles.DispatchUnclaim {
			_ = beads.Unclaim(ctx, o.BD, d.Item.ID)
		}
		return report.Result{}, fmt.Errorf("send %s: %w", externalID, err)
	}
	if cc.Budget.Tokens.Unlimited() && cc.Budget.Cost.Unlimited() && cc.Budget.Time <= 0 {
		return o.waitDoneR(ctx, nil, d, externalID) // no watchdog
	}
	return o.workerWaitWithWatchdogR(ctx, d, externalID)
}
```

`renderNudge` builds `prompt.Context` from `d` + cfg (`WorktreeDir`, `SkillMD` from
`cc.SkillMD`, `SelfLogin` from `o.Cfg.SelfLogin`, `RepoRoot`), renders `cc.Prompt`,
prepends `prompt.AuthorshipPreamble()` when `cc.AuthorshipGuard`, and appends
`cc.Budget.PromptLine()` (returns "" when unlimited, so safe to always append).

`waitDoneR`/`workerWaitWithWatchdogR` are thin wrappers that call the existing
`waitDone`/`workerWaitWithWatchdog` (UNCHANGED) and translate the terminal
status/outcome into a `report.Result` (Closed/HandedBack/Created-via-snapshot/
Escalated/Unclaimed/Indeterminate). The existing `DoneSignal`/`OnFailure` calls
inside `waitDone`/`fail` now pass `d.Role.CCPool.Completion` / `.OnFailure`.

- [ ] **Step 4: Add the command path**

```go
func (o *Orchestrator) runCommand(ctx context.Context, d discover.DispatchContext) (report.Result, error) {
	argv := o.renderArgv(d.Role.Command, d) // interpolate {{.BeadID}} etc. into each argv element
	_, err := query.OSCommander{}.Run(ctx, argv)
	if err != nil {
		// on_failure applies only when the role's query is bead-backed (config-time)
		if isBeadsQuery(d.Role.Query) && d.Role.CCPool == nil {
			// command roles have no CCPool; carry on_failure on CommandConfig if needed.
		}
		return report.Result{}, fmt.Errorf("command role %q item %s: %w", d.Role.Name, d.Item.ID, err)
	}
	return report.Result{}, nil
}
```

Note: in Phase 1 a `command` role's failure action is a no-op-or-log (the spec scopes
`on_failure` for command roles to bead-backed queries only; the built-in set has no
command role, so this path is exercised only by tests/explicit config). Keep
`renderArgv` minimal (parse+execute each argv element through `prompt`).

- [ ] **Step 5: Update `RunOne`/`drain` to thread the `report.Result`** — `drain`
      records `complete++`/`flagged++` from the returned `error` (unchanged semantics) and
      emits the `Result`: `o.emitResult(ctx, role, d.Item.ID, res)` which calls
      `o.Log.Emit("info","dispatch",msg,res.Fields())` when `o.Log != nil`, else prints to
      stdout (run-role path). This supersedes `logCreated`; keep `snapshotIDs`/
      `createdByActor` and fold their output into `res` (Created / Indeterminate).

- [ ] **Step 6: Run the orchestrator suite**

Run: `(cd packages/pr-pool && go test ./internal/orchestrator/ -v)`
Expected: PASS — including the ported `TestWaitDone_lostRace_*` (single-terminal
`pg2-c1vp`) and `TestDrainOnce_noStarvation`. Fix ext-id literals until green.

- [ ] **Step 7: Commit**

```bash
git add packages/pr-pool/internal/orchestrator/
git commit -m "refactor(pr-pool): orchestrator RoleSet range + role.Type switch + report.Result (spec C phase 1)"
```

---

## Task 14: Backward-compat golden (structural + literal)

**Files:**

- Create: `packages/pr-pool/internal/orchestrator/golden_test.go`

- [ ] **Step 1: Write the golden test**

```go
package orchestrator

import (
	"strings"
	"testing"

	"github.com/phillipgreenii/pr-pool/internal/discover"
	"github.com/phillipgreenii/pr-pool/internal/item"
	"github.com/phillipgreenii/pr-pool/internal/prompt"
	"github.com/phillipgreenii/pr-pool/internal/roles"
)

func TestGolden_workerDispatchShape(t *testing.T) {
	rs := roles.BuiltinRoleSet(roles.BuiltinParams{WorktreeDir: "/wt", WorkerSkillMD: "WSKILL", MaxWorker: 1, MaxFeedback: 1})
	wk := rs[1]
	// external_id with a pinned stamp:
	if got := wk.ExternalID("pr-pool-", "zr-w.2", testStamp); got != "pr-pool-worker-zr-w.2-"+testStamp {
		t.Fatalf("external_id = %q", got)
	}
	// rendered nudge = preamble + task; rails ONLY in preamble.
	ctx := prompt.Context{Item: item.Item{ID: "zr-w.2"}, WorktreeDir: "/wt", SkillMD: "WSKILL"}
	task, err := prompt.Render(wk.CCPool.Prompt, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(task, "phillipg.") || strings.Contains(task, "git push --force") {
		t.Fatal("task body must NOT contain rails")
	}
	full := prompt.AuthorshipPreamble() + task
	for _, want := range []string{"phillipg.", "NEVER git push --force", "human", "zr-w.2", "/wt"} {
		if !strings.Contains(full, want) {
			t.Fatalf("full nudge missing %q", want)
		}
	}
	_ = discover.DispatchContext{Role: wk, Item: item.Item{ID: "zr-w.2"}}
}
```

- [ ] **Step 2: Run + verify PASS**

Run: `(cd packages/pr-pool && go test ./internal/orchestrator/ -run TestGolden -v)`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add packages/pr-pool/internal/orchestrator/golden_test.go
git commit -m "test(pr-pool): structural backward-compat golden for worker dispatch (spec C phase 1)"
```

---

## Task 15: `cmd/pr-pool` — CLI tokens, deferred name check, config subcommand, prechecks

**Files:**

- Modify: `packages/pr-pool/cmd/pr-pool/args.go`, `runrole.go`, `drain.go`
- Create: `packages/pr-pool/cmd/pr-pool/config_cmd.go`
- Modify: `packages/pr-pool/cmd/pr-pool/args_test.go`, `runrole_test.go`

- [ ] **Step 1: Flip `args_test.go`** — the `run-role-unknown-role` / `run-query-unknown-role`
      cases that currently expect `routeUsageErr` at parse time must change: parse-time only
      rejects MISSING role token / extra args; an unknown NAME is no longer a parse error
      (it's validated post-config-load). Update those two cases accordingly.

- [ ] **Step 2: Edit `args.go`** — delete the static `knownRoles` map; `parseRunRoleArgs`/
      `parseRunQueryArgs` only check token presence + arg count. Update `helpText` to drop
      the removed role env vars, document `<RepoRoot>/.pr-pool/config.toml` + `PR_POOL_CONFIG`,
      and add `config` to the subcommand list.

- [ ] **Step 3: Edit `runrole.go`** — `config.Load()` now returns `(Config, error)`
      (handle it). Replace the `feedback`/`worker` `switch` in `resolveRole` with a lookup
      over `cfg.Roles` by `Name`; on miss, `printUsageErr("run-role: unknown role \"" + name +
"\" (configured: " + strings.Join(names, ", ") + ")")` and return `exitUsage`. Build
      `query.Env` and pass it to `discover.ForRole` (whose signature changed).

- [ ] **Step 4: Edit `drain.go`** — handle `cfg, err := config.Load()`; add precheck
      warnings: (a) if `<RepoRoot>/.pr-pool/config.toml` is tracked by git (`git ls-files
--error-unmatch`), `slog.Warn` that prompts may be committed; (b) if any removed role
      env var (`PR_POOL_MAX_WORKER`, etc.) is set, `slog.Warn` naming the config replacement;
      (c) if any configured query `query.IsStub(...)`, `slog.Warn` it is a stub. Set
      `o.Reg = cfg.Roles`.

- [ ] **Step 5: Write `config_cmd.go`** — `pr-pool config --print-defaults` prints
      `config.ExampleTOML()`; `pr-pool config --show` loads config and prints the resolved
      `ConfigPath` + each role's `Name`/`Type`/`Cap`/`Enabled`/query type. Route `config`
      in `args.go`'s `route()`.

- [ ] **Step 6: Generate and commit `config.example.toml`**

```bash
(cd packages/pr-pool && go build -o /tmp/pr-pool ./cmd/pr-pool && /tmp/pr-pool config --print-defaults > config.example.toml)
```

- [ ] **Step 7: Run the cmd suite + full build**

Run: `(cd packages/pr-pool && go test ./cmd/... -v && go build ./...)`
Expected: PASS + clean build.

- [ ] **Step 8: Commit**

```bash
git add packages/pr-pool/cmd/pr-pool/ packages/pr-pool/config.example.toml
git commit -m "feat(pr-pool): config-driven CLI, config subcommand, prechecks, example config (spec C phase 1)"
```

---

## Task 16: Full-suite gate + docs + bead bookkeeping

**Files:**

- Modify: `packages/pr-pool/README.md`
- (no code)

- [ ] **Step 1: Run the whole suite + lints**

Run:

```bash
(cd packages/pr-pool && go test ./... && go vet ./...)
prek run --all-files   # or: pre-commit run --all-files
nix flake check
```

Expected: all PASS (per the repo's "before claiming complete" rule).

- [ ] **Step 2: Update `README.md`** — document `.pr-pool/config.toml`, the typed
      role/query model, the removed env vars + their config replacements, `config
--print-defaults`/`--show`, and the `.git/info/exclude` step for the ZipRecruiter
      monorepo.

- [ ] **Step 3: Commit docs**

```bash
git add packages/pr-pool/README.md
git commit -m "docs(pr-pool): document TOML role/query config (spec C phase 1)"
```

- [ ] **Step 4: Bead bookkeeping** — comment progress on `pg2-kplb`; create the
      **Phase 2** bead ("pr-pool: extract Executor interface; move watchdog/single-terminal
      into ccpoolExecutor/commandExecutor — pure refactor behind a race+golden test"),
      discovered-from `pg2-kplb`; and the real-github/jira follow-up bead the stub errors
      reference. Close/coordinate `pg2-wgg0` (budget seam delivered here).

---

## Self-Review

**Spec coverage** (spec C §1–§10):

- §1 resolution/layering/Load-error/validation → Tasks 9, 10, 15. ✓
- §2 Item → Task 1. ✓
- §3 two-pass decode/instance Registry/typed enums/errors.Join → Tasks 6, 7, 9. ✓
- §4 query types (real + stubs) + Commander + propagation → Tasks 4, 5, 6, 12. ✓
- §5 RoleSet/Role/CCPool+Command/type-switch/budget overlay → Tasks 8, 9, 13. ✓
- §5a report.Result + Indeterminate + eventlog/nil-Log → Tasks 2, 13. ✓
- §6 prompt render/preamble/self_login → Tasks 3, 13. ✓
- §7 CLI tokens/deferred check/config cmd/prechecks/example → Tasks 10, 15. ✓
- §8 built-in defaults + structural golden → Tasks 8, 14. ✓
- §9 port-then-refactor + all test classes → Tasks 11, 13 (ports), 4–14 (new). ✓
- §10 import DAG → enforced by package layout (Task file-structure map). ✓
- Phase 2 (Executor extraction) → explicitly deferred (Task 16 bead). ✓

**Placeholder scan:** no "TBD/TODO"; the two genuinely-deferred-to-implementation
helpers (`hasSingleBracketRole`, `renderArgv`, `waitDoneR`/`workerWaitWithWatchdogR`
wrappers) are described with their exact contract + the existing code they wrap.

**Type consistency:** `Completion`/`FailureAction`/`DispatchFailAction` (roles),
`QueryFormat`/`Query`/`Env`/`Commander` (query), `Role`/`RoleSet`/`CCPoolConfig`/
`CommandConfig` (roles), `report.Verb`/`Ref`/`Action`/`Result`, `DispatchContext{Role,
Item}` — all defined once (Tasks 1–8) and referenced consistently downstream.

> NOTE on Task 13: it is the largest task and edits the `pg2-c1vp`-sensitive file.
> Although Phase 1 does NOT extract the Executor, the wrappers (`waitDoneR` etc.)
> must call the existing `waitDone`/`workerWaitWithWatchdog` UNCHANGED. If during
> execution that proves to require editing those functions' bodies, STOP and split
> Task 13 — the watchdog body changes belong in the Phase 2 bead, not here.
