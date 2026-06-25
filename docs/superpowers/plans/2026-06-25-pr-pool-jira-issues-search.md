# pr-pool jira-issues JQL search Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make pr-pool's `jira-issues` query return items end-to-end by adding a non-interactive JQL `search` mode to `pg-pr-issues-jira-zr` (new `/rest/api/3/search/jql` endpoint, normalized JSON envelope) and repointing pr-pool at it.

**Architecture:** The ZR Jira binary gains a `search` subcommand that POSTs to `/rest/api/3/search/jql`, reusing its existing `Provider`/basic-auth, and prints a `{items,truncated}` envelope on stdout. pr-pool's `JiraIssues.Run` shells out to it via the existing `Commander` seam and maps the envelope to `[]item.Item`. Deployment enables the existing `zr.pgPrZr` credential wrapper.

**Tech Stack:** Go (stdlib only: `net/http`, `flag`, `encoding/json`), `httptest` for tests, Nix (gomod2nix Pattern B cross-repo build, home-manager module).

**Spec:** `docs/superpowers/specs/2026-06-25-pr-pool-jira-issues-search-design.md`
**Bead:** `pg2-gpao` (blocks `pg2-5b4l`).

**Repos & branches:**

- `phillipg-nix-ziprecruiter` (jira-zr tool + deploy) — create branch `pg2-gpao-jira-search`.
- `phillipgreenii-nix-agent-support` (pr-pool) — branch `pg2-gpao-jira-issues-search` (already exists; holds this plan + spec).

## File Structure

| File                                                     | Repo          | Responsibility                                                          |
| -------------------------------------------------------- | ------------- | ----------------------------------------------------------------------- |
| `modules/pg-pr-zr/cmd/pg-pr-issues-jira-zr/main.go`      | ziprecruiter  | Add `SearchIssues`, envelope types, `runSearch` seam, `main()` dispatch |
| `modules/pg-pr-zr/cmd/pg-pr-issues-jira-zr/main_test.go` | ziprecruiter  | `httptest` tests for `SearchIssues` + `runSearch`                       |
| `modules/pg-pr-zr/cmd/pg-pr-issues-jira-zr/README.md`    | ziprecruiter  | Document the `search` subcommand                                        |
| `packages/pr-pool/internal/query/issues.go`              | agent-support | Repoint `JiraIssues.Run` to the new tool + envelope parse               |
| `packages/pr-pool/internal/query/issues_test.go`         | agent-support | Update jira tests to the envelope contract                              |
| `packages/pr-pool/README.md` (jira section)              | agent-support | Document the new jira source contract                                   |
| machine config + `modules/pg-pr-zr/default.nix` import   | ziprecruiter  | Enable `zr.pgPrZr`, wire `realBinary`/`tokenFile`                       |

---

## Task 1: `pg-pr-issues-jira-zr` — `SearchIssues` + envelope types

**Files:**

- Modify: `phillipg-nix-ziprecruiter/modules/pg-pr-zr/cmd/pg-pr-issues-jira-zr/main.go`
- Test: `phillipg-nix-ziprecruiter/modules/pg-pr-zr/cmd/pg-pr-issues-jira-zr/main_test.go`

First create the branch:

```bash
cd ~/phillipg_mbp/phillipg-nix-ziprecruiter && git switch -c pg2-gpao-jira-search
```

- [ ] **Step 1: Write the failing tests**

Append to `main_test.go` (and add `"bytes"`, `"encoding/json"`, `"io"` to its import block):

```go
func TestSearchIssues_mapsAndTruncates(t *testing.T) {
	expectedAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("user@example.com:test-token"))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/rest/api/3/search/jql" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != expectedAuth {
			t.Errorf("auth header = %q, want %q", got, expectedAuth)
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["jql"] != "project = FOO" {
			t.Errorf("jql in body = %v, want 'project = FOO'", body["jql"])
		}
		_, _ = w.Write([]byte(`{
			"issues": [
				{"key":"FOO-1","fields":{"summary":"Do X","status":{"name":"To Do"},"issuetype":{"name":"Bug"},"labels":["a"]}}
			],
			"nextPageToken": "tok2"
		}`))
	}))
	defer srv.Close()

	items, truncated, err := newTestProvider(srv).SearchIssues(context.Background(), "project = FOO", 100)
	if err != nil {
		t.Fatalf("SearchIssues: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("want 1 item, got %d", len(items))
	}
	got := items[0]
	if got.Key != "FOO-1" || got.Summary != "Do X" || got.Status != "To Do" || got.IssueType != "Bug" {
		t.Errorf("item mapped wrong: %+v", got)
	}
	if len(got.Labels) != 1 || got.Labels[0] != "a" {
		t.Errorf("labels wrong: %+v", got.Labels)
	}
	if !strings.HasSuffix(got.URL, "/browse/FOO-1") {
		t.Errorf("url = %q, want suffix /browse/FOO-1", got.URL)
	}
	if !truncated {
		t.Error("nextPageToken present must report truncated=true")
	}
}

func TestSearchIssues_notTruncatedWhenIsLast(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"issues":[],"isLast":true}`))
	}))
	defer srv.Close()
	_, truncated, err := newTestProvider(srv).SearchIssues(context.Background(), "project = FOO", 100)
	if err != nil {
		t.Fatalf("SearchIssues: %v", err)
	}
	if truncated {
		t.Error("isLast=true must report truncated=false")
	}
}

func TestSearchIssues_non2xxErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	if _, _, err := newTestProvider(srv).SearchIssues(context.Background(), "x", 100); err == nil {
		t.Fatal("401 must error")
	}
}

func TestSearchIssues_missingKeyErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"issues":[{"fields":{"summary":"no key"}}],"isLast":true}`))
	}))
	defer srv.Close()
	if _, _, err := newTestProvider(srv).SearchIssues(context.Background(), "x", 100); err == nil {
		t.Fatal("issue missing key must error")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd ~/phillipg_mbp/phillipg-nix-ziprecruiter/modules/pg-pr-zr && go test ./cmd/pg-pr-issues-jira-zr/ -run TestSearchIssues 2>&1 | tail -20`
Expected: FAIL — `p.SearchIssues undefined` (compile error).

- [ ] **Step 3: Implement `SearchIssues` + envelope types**

Add `"bytes"` and `"io"` to `main.go`'s import block, then append this block to `main.go` (after the `var _ issues.Provider` line, before `func run()`):

```go
// --- JQL search mode (backs pr-pool's jira-issues query) ---

// searchItem is one normalized issue in the search envelope. The tool owns the
// Atlassian wire-format mapping so consumers (pr-pool) never parse Jira REST JSON.
type searchItem struct {
	Key       string   `json:"key"`
	Summary   string   `json:"summary"`
	Status    string   `json:"status"`
	IssueType string   `json:"issuetype"`
	Labels    []string `json:"labels"`
	URL       string   `json:"url"`
}

// searchEnvelope is the stdout contract: mapped items + an authoritative truncation
// flag. Truncation MUST travel on stdout (consumers discard our stderr on success).
type searchEnvelope struct {
	Items     []searchItem `json:"items"`
	Truncated bool         `json:"truncated"`
}

// jiraSearchResponse is the subset of POST /rest/api/3/search/jql we map. IsLast is
// a *bool so an absent field is distinguishable from an explicit false.
type jiraSearchResponse struct {
	Issues []struct {
		Key    string `json:"key"`
		Fields struct {
			Summary   string   `json:"summary"`
			Labels    []string `json:"labels"`
			IssueType struct {
				Name string `json:"name"`
			} `json:"issuetype"`
			Status struct {
				Name string `json:"name"`
			} `json:"status"`
		} `json:"fields"`
	} `json:"issues"`
	NextPageToken string `json:"nextPageToken"`
	IsLast        *bool  `json:"isLast"`
}

// SearchIssues runs a JQL search via POST /rest/api/3/search/jql and maps the first
// page. The bool is "truncated": more results exist than were returned. The new
// endpoint caps a fielded page at 100 regardless of maxResults, so the authoritative
// truncation signal is nextPageToken/isLast, never a count.
func (p *Provider) SearchIssues(ctx context.Context, jql string, limit int) ([]searchItem, bool, error) {
	if strings.TrimSpace(jql) == "" {
		return nil, false, fmt.Errorf("jira-zr: empty jql")
	}
	reqBody, err := json.Marshal(map[string]any{
		"jql":        jql,
		"maxResults": limit,
		"fields":     []string{"summary", "status", "labels", "issuetype"},
	})
	if err != nil {
		return nil, false, err
	}
	endpoint := p.BaseURL + "/rest/api/3/search/jql"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(reqBody))
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("Authorization", p.basicAuth())
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.HTTP.Do(req)
	if err != nil {
		return nil, false, fmt.Errorf("jira-zr: search: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return nil, false, fmt.Errorf("jira-zr: search: status %s", resp.Status)
	}
	var raw jiraSearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, false, fmt.Errorf("jira-zr: search: decode: %w", err)
	}
	items := make([]searchItem, 0, len(raw.Issues))
	for _, is := range raw.Issues {
		if is.Key == "" {
			return nil, false, fmt.Errorf("jira-zr: search: issue missing key")
		}
		labels := is.Fields.Labels
		if labels == nil {
			labels = []string{}
		}
		items = append(items, searchItem{
			Key:       is.Key,
			Summary:   is.Fields.Summary,
			Status:    is.Fields.Status.Name,
			IssueType: is.Fields.IssueType.Name,
			Labels:    labels,
			URL:       p.BaseURL + "/browse/" + url.PathEscape(is.Key),
		})
	}
	truncated := raw.NextPageToken != "" || (raw.IsLast != nil && !*raw.IsLast)
	return items, truncated, nil
}
```

(The `io` import is used in Task 2; add both `bytes` and `io` now to avoid a second import edit.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd ~/phillipg_mbp/phillipg-nix-ziprecruiter/modules/pg-pr-zr && go test ./cmd/pg-pr-issues-jira-zr/ -run TestSearchIssues -v 2>&1 | tail -20`
Expected: PASS (4 tests) + existing `GetIssue` tests still pass.

- [ ] **Step 5: Commit**

```bash
cd ~/phillipg_mbp/phillipg-nix-ziprecruiter
git add modules/pg-pr-zr/cmd/pg-pr-issues-jira-zr/main.go modules/pg-pr-zr/cmd/pg-pr-issues-jira-zr/main_test.go
git commit -m "feat(pg-pr-issues-jira-zr): SearchIssues via /rest/api/3/search/jql"
```

---

## Task 2: `pg-pr-issues-jira-zr` — `runSearch` seam + `main()` dispatch

**Files:**

- Modify: `phillipg-nix-ziprecruiter/modules/pg-pr-zr/cmd/pg-pr-issues-jira-zr/main.go`
- Test: `phillipg-nix-ziprecruiter/modules/pg-pr-zr/cmd/pg-pr-issues-jira-zr/main_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `main_test.go`:

```go
func TestRunSearch_writesEnvelope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"issues":[{"key":"FOO-1","fields":{"summary":"X","status":{"name":"Open"},"issuetype":{"name":"Bug"},"labels":[]}}],"isLast":true}`))
	}))
	defer srv.Close()
	t.Setenv(envBaseURL, srv.URL)
	t.Setenv(envEmail, "user@example.com")
	t.Setenv(envToken, "test-token")

	var buf bytes.Buffer
	if err := runSearch(context.Background(), []string{"--jql", "project = FOO"}, &buf); err != nil {
		t.Fatalf("runSearch: %v", err)
	}
	var env searchEnvelope
	if err := json.Unmarshal(buf.Bytes(), &env); err != nil {
		t.Fatalf("envelope not valid JSON: %v\n%s", err, buf.String())
	}
	if len(env.Items) != 1 || env.Items[0].Key != "FOO-1" {
		t.Errorf("envelope items wrong: %+v", env.Items)
	}
	if env.Truncated {
		t.Error("isLast=true => truncated must be false")
	}
}

func TestRunSearch_requiresJQL(t *testing.T) {
	if err := runSearch(context.Background(), nil, io.Discard); err == nil {
		t.Fatal("missing --jql must error (before touching the network)")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd ~/phillipg_mbp/phillipg-nix-ziprecruiter/modules/pg-pr-zr && go test ./cmd/pg-pr-issues-jira-zr/ -run TestRunSearch 2>&1 | tail -20`
Expected: FAIL — `runSearch undefined`.

- [ ] **Step 3: Implement `runSearch` and wire `main()`**

Add `"flag"` to `main.go`'s import block. Add this function (after `SearchIssues`):

```go
// runSearch is the `search` subcommand: parse flags, run the JQL search, and write
// the {items,truncated} envelope as JSON to stdout. The JQL check runs BEFORE
// New() so a missing flag never requires credentials. Errors are returned (main
// turns them into a non-zero exit + stderr); an envelope is NEVER written on error.
func runSearch(ctx context.Context, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("search", flag.ContinueOnError)
	jql := fs.String("jql", "", "JQL query (required)")
	limit := fs.Int("limit", 100, "max results to request for the page")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*jql) == "" {
		return fmt.Errorf("jira-zr search: --jql is required")
	}
	p, err := New()
	if err != nil {
		return err
	}
	items, truncated, err := p.SearchIssues(ctx, *jql, *limit)
	if err != nil {
		return err
	}
	if truncated {
		fmt.Fprintln(os.Stderr, "jira-zr search: results truncated; backlog exceeds one page")
	}
	return json.NewEncoder(stdout).Encode(searchEnvelope{Items: items, Truncated: truncated})
}
```

Replace the existing `main()` with:

```go
func main() {
	// `search` is the non-interactive JQL mode used by pr-pool. With no args the
	// binary keeps its existing (Phase-0 stub) scriptout behaviour. The length
	// guard is mandatory: a bare os.Args[1] panics when invoked with no args.
	if len(os.Args) > 1 && os.Args[1] == "search" {
		if err := runSearch(context.Background(), os.Args[2:], os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
```

- [ ] **Step 4: Run the full package test suite**

Run: `cd ~/phillipg_mbp/phillipg-nix-ziprecruiter/modules/pg-pr-zr && go test -race ./cmd/pg-pr-issues-jira-zr/ 2>&1 | tail -10`
Expected: PASS (all `GetIssue`, `SearchIssues`, `runSearch`, `New` tests).

- [ ] **Step 5: Run pre-commit (gofmt/golangci-lint) for the module**

Run: `cd ~/phillipg_mbp/phillipg-nix-ziprecruiter && gofmt -l modules/pg-pr-zr/cmd/pg-pr-issues-jira-zr/ ; echo "gofmt clean if empty above"`
Expected: no files listed.

- [ ] **Step 6: Commit**

```bash
cd ~/phillipg_mbp/phillipg-nix-ziprecruiter
git add modules/pg-pr-zr/cmd/pg-pr-issues-jira-zr/main.go modules/pg-pr-zr/cmd/pg-pr-issues-jira-zr/main_test.go
git commit -m "feat(pg-pr-issues-jira-zr): add 'search' subcommand emitting {items,truncated} envelope"
```

---

## Task 3: Verify the nix cross-repo build still works

**Files:** none (build verification only).

- [ ] **Step 1: Confirm no gomod2nix change is needed**

The search path adds only stdlib imports (`bytes`, `io`, `flag`). `modules/pg-pr-zr/go.mod` has a single local-replace require and an empty `gomod2nix.toml` (third-party deps come transitively through `pg-pr`). Do NOT run `gomod2nix generate`.

- [ ] **Step 2: Build the package via nix**

Run: `cd ~/phillipg_mbp/phillipg-nix-ziprecruiter && nix build .#pg-pr-zr --print-out-paths 2>&1 | tail -20`
Expected: a `/nix/store/...-pg-pr-zr-...` path; build succeeds (Pattern-B sandbox copies `pg-pr-src`).

- [ ] **Step 3: Smoke-test the built binary's arg guard**

Run: `BIN=$(nix build .#pg-pr-zr --no-link --print-out-paths)/bin/pg-pr-issues-jira-zr; "$BIN" search --jql 2>&1 | head -3; echo "exit=$?"`
Expected: a flag-parse/`--jql is required` style error and non-zero exit — NOT a panic, NOT the scriptout stub message. (No creds needed; it fails before/at flag parsing or at `New()`.)

---

## Task 4: pr-pool — repoint `JiraIssues.Run` to the envelope contract

**Files:**

- Modify: `phillipgreenii-nix-agent-support/packages/pr-pool/internal/query/issues.go`
- Test: `phillipgreenii-nix-agent-support/packages/pr-pool/internal/query/issues_test.go`

(Work on the existing branch `pg2-gpao-jira-issues-search` in `~/phillipg_mbp/phillipgreenii-nix-agent-support`.)

- [ ] **Step 1: Replace the jira tests with the envelope contract**

In `issues_test.go`, DELETE the two old tests: `TestJiraIssues_mapsResultsAndRequestsRaw` and the old `TestJiraIssues_missingKeyIsError` (the `--raw`/`{"issues":[…]}` versions). The add-block below RE-CREATES `TestJiraIssues_missingKeyIsError` with a new envelope body — after editing, verify exactly ONE `func TestJiraIssues_missingKeyIsError` exists (a duplicate is a `redeclared in this block` compile error). Then add:

```go
func TestJiraIssues_mapsEnvelopeAndBuildsArgs(t *testing.T) {
	cmd := &recordingCmd{out: []byte(`{"items":[
	  {"key":"PROJ-1","summary":"Do X","status":"To Do","issuetype":"Bug","labels":["a","b"],"url":"https://x/browse/PROJ-1"},
	  {"key":"PROJ-2","summary":"Do Y","status":"In Progress","issuetype":"Task","labels":[],"url":"https://x/browse/PROJ-2"}
	],"truncated":false}`)}
	q := JiraIssues{Project: "PROJ", Labels: []string{"worker-ready"}}
	items, err := q.Run(context.Background(), Env{Cmd: cmd})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("want 2 items, got %d: %+v", len(items), items)
	}
	got := items[0]
	if got.ID != "PROJ-1" || got.Type != "jira-issue" || got.Title != "Do X" {
		t.Errorf("item[0] mapped wrong: %+v", got)
	}
	if got.Metadata["key"] != "PROJ-1" || got.Metadata["project"] != "PROJ" ||
		got.Metadata["issuetype"] != "Bug" || got.Metadata["status"] != "To Do" ||
		got.Metadata["url"] != "https://x/browse/PROJ-1" {
		t.Errorf("item[0] metadata wrong: %+v", got.Metadata)
	}
	// argv: invokes the host jira tool's search subcommand with jql + limit.
	if len(cmd.argv) < 2 || cmd.argv[0] != "pg-pr-issues-jira-zr" || cmd.argv[1] != "search" {
		t.Errorf("argv must invoke 'pg-pr-issues-jira-zr search': %v", cmd.argv)
	}
	if !argvHasPair(cmd.argv, "--limit", "100") || !slices.Contains(cmd.argv, "--jql") {
		t.Errorf("argv missing --jql/--limit: %v", cmd.argv)
	}
}

func TestJiraIssues_missingKeyIsError(t *testing.T) {
	cmd := &recordingCmd{out: []byte(`{"items":[{"summary":"no key"}],"truncated":false}`)}
	if _, err := (JiraIssues{Project: "PROJ"}).Run(context.Background(), Env{Cmd: cmd}); err == nil {
		t.Fatal("item missing key must error")
	}
}

func TestJiraIssues_truncatedStillReturnsItems(t *testing.T) {
	cmd := &recordingCmd{out: []byte(`{"items":[{"key":"PROJ-1","summary":"X"}],"truncated":true}`)}
	items, err := (JiraIssues{Project: "PROJ"}).Run(context.Background(), Env{Cmd: cmd})
	if err != nil || len(items) != 1 {
		t.Fatalf("truncated=true must still return items, no error; got items=%v err=%v", items, err)
	}
}
```

(`TestJiraIssues_jqlExplicitTakesPrecedence`, `TestJiraIssues_jqlBuiltFromProjectAndLabels`, `TestJiraIssues_validateRequiresProjectOrJQL`, `TestJiraIssues_nonZeroExitPropagates`, and `TestIsStub_noStubTypesRemain` stay unchanged — `jql()`, `Validate()`, and error propagation are unchanged.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd ~/phillipg_mbp/phillipgreenii-nix-agent-support/packages/pr-pool && go test ./internal/query/ -run TestJiraIssues 2>&1 | tail -20`
Expected: FAIL — the new test expects argv `pg-pr-issues-jira-zr search` / envelope mapping the old code doesn't produce.

- [ ] **Step 3: Rewrite `JiraIssues.Run` + types in `issues.go`**

Leave `warnIfTruncated` and `issueListLimit = 200` intact — `GitHubIssues.Run` still uses both (only the jira path stops calling `warnIfTruncated`). Do not "clean them up."

In `issues.go`: add the jira page-cap const near `issueListLimit`:

```go
// jiraListLimit bounds a single jira-issues page. The /rest/api/3/search/jql
// endpoint caps a fielded page at 100 regardless of maxResults, so 100 is the
// effective ceiling; truncation past it is reported via the envelope's flag.
const jiraListLimit = 100
```

DELETE the `jiraSearchResult` struct and the existing `JiraIssues.Run`. Add the new envelope types and `Run` (keep `JiraIssues`, `Validate`, and `jql()` as-is):

```go
// jiraSearchItem is one item in pg-pr-issues-jira-zr's search envelope.
type jiraSearchItem struct {
	Key       string   `json:"key"`
	Summary   string   `json:"summary"`
	Status    string   `json:"status"`
	IssueType string   `json:"issuetype"`
	Labels    []string `json:"labels"`
	URL       string   `json:"url"`
}

// jiraSearchEnvelope is the stdout contract of `pg-pr-issues-jira-zr search`:
// normalized items the tool already mapped from Jira's REST response, plus a
// truncation flag (the tool owns the wire format; pr-pool stays decoupled).
type jiraSearchEnvelope struct {
	Items     []jiraSearchItem `json:"items"`
	Truncated bool             `json:"truncated"`
}

func (q JiraIssues) Run(ctx context.Context, env Env) ([]item.Item, error) {
	argv := []string{
		"pg-pr-issues-jira-zr", "search",
		"--jql", q.jql(),
		"--limit", strconv.Itoa(jiraListLimit),
	}
	out, err := commander(env).Run(ctx, argv)
	if err != nil {
		return nil, fmt.Errorf("jira-issues query: %w", err)
	}
	if len(bytes.TrimSpace(out)) == 0 {
		return nil, nil
	}
	var envl jiraSearchEnvelope
	if err := json.Unmarshal(out, &envl); err != nil {
		return nil, fmt.Errorf("jira-issues query: parse pg-pr-issues-jira-zr output: %w", err)
	}
	items := make([]item.Item, 0, len(envl.Items))
	for _, ji := range envl.Items {
		if ji.Key == "" {
			return nil, fmt.Errorf("jira-issues query: item missing required \"key\"")
		}
		items = append(items, item.Item{
			ID:    ji.Key,
			Type:  "jira-issue",
			Title: ji.Summary,
			Metadata: map[string]any{
				"project":   q.Project,
				"key":       ji.Key,
				"issuetype": ji.IssueType,
				"status":    ji.Status,
				"labels":    ji.Labels,
				"url":       ji.URL,
			},
		})
	}
	if envl.Truncated {
		slog.Warn("jira-issues query truncated; backlog exceeds one page",
			"project", q.Project, "limit", jiraListLimit)
	}
	return items, nil
}
```

Also update the `JiraIssues` doc comment to describe the new tool/contract (replace the `ankitpokhrel/jira-cli` reference with: "lists unresolved issues by running `pg-pr-issues-jira-zr search --jql <jql>`, which queries `/rest/api/3/search/jql` and returns a normalized `{items,truncated}` envelope").

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd ~/phillipg_mbp/phillipgreenii-nix-agent-support/packages/pr-pool && go test ./internal/query/ -v 2>&1 | tail -25`
Expected: PASS — all jira + github + IsStub tests.

- [ ] **Step 5: Run the full module under -race**

Run: `cd ~/phillipg_mbp/phillipgreenii-nix-agent-support/packages/pr-pool && go test -race ./... 2>&1 | tail -25`
Expected: all packages `ok`.

- [ ] **Step 6: Commit**

```bash
cd ~/phillipg_mbp/phillipgreenii-nix-agent-support
git add packages/pr-pool/internal/query/issues.go packages/pr-pool/internal/query/issues_test.go
git commit -m "feat(pr-pool): jira-issues queries pg-pr-issues-jira-zr search (envelope contract)"
```

---

## Task 5: pr-pool — docs + module-wide green

**Files:**

- Modify: `phillipgreenii-nix-agent-support/packages/pr-pool/README.md` (jira-issues section)

- [ ] **Step 1: Update the README jira-issues description**

Find the `jira-issues` query description in `packages/pr-pool/README.md` (grep: `grep -n "jira-issues" packages/pr-pool/README.md`) and update it to state that `jira-issues` shells out to `pg-pr-issues-jira-zr search --jql <jql> --limit 100`, which queries the Atlassian `/rest/api/3/search/jql` endpoint and returns a normalized `{items,truncated}` JSON envelope; `pr-pool` maps `items` to `jira-issue` items and warns when `truncated` is true. Remove any mention of a `jira` CLI / `--raw` / `--paginate`.

- [ ] **Step 2: Run pre-commit for the agent-support repo**

Run: `cd ~/phillipg_mbp/phillipgreenii-nix-agent-support && prek run --all-files 2>&1 | tail -25` (or `pre-commit run --all-files`)
Expected: all hooks pass (gofmt, golangci-lint pr-pool, treefmt).

- [ ] **Step 3: Commit**

```bash
cd ~/phillipg_mbp/phillipgreenii-nix-agent-support
git add packages/pr-pool/README.md
git commit -m "docs(pr-pool): document jira-issues via pg-pr-issues-jira-zr search"
```

---

## Task 6: Deploy — enable `zr.pgPrZr` with the jira wrapper

**Files:**

- Modify: machine config in `phillipg-nix-ziprecruiter` (the module-import + option-set site)

- [ ] **Step 1: Locate the import site**

Run: `cd ~/phillipg_mbp/phillipg-nix-ziprecruiter && grep -rn "modules/" machines/phillipg-mbp-02/ machines/default.nix 2>/dev/null | grep -iE "import|pg-pr|zr\." | head -30`
Identify the file that aggregates `modules/*` into the machine (the same place other `modules/...` are imported). Confirm `self` is in the module args (`grep -rn "specialArgs" machines/`).

- [ ] **Step 2: Import the module and set options**

In that machine config, add `../../modules/pg-pr-zr` (adjust relative path to the import site) to `imports`, and add:

```nix
zr.pgPrZr = {
  enable = true;
  jira = {
    baseUrl = "https://ziprecruiter.atlassian.net";
    email = "phillipg@ziprecruiter.com";
    tokenFile = /Users/phillipg/.jira_api_token;
    realBinary = "${self.packages.${pkgs.system}.pg-pr-zr}/bin/pg-pr-issues-jira-zr";
  };
};
```

Do NOT add `self.packages.*.pg-pr-zr` to `home.packages` anywhere — the wrapper and real binary share the name `pg-pr-issues-jira-zr`; the real one must stay referenced by store path only, or `exec` recurses.

- [ ] **Step 3: Build (no activate) to validate**

Run: `cd ~/phillipg_mbp/phillipg-nix-ziprecruiter && zn-self-build 2>&1 | tail -25`
Expected: build succeeds. (If sandboxed, this is the stopping point — ask the operator to `zn-self-apply`.)

- [ ] **Step 4: Apply**

Run: `cd ~/phillipg_mbp/phillipg-nix-ziprecruiter && zn-self-apply 2>&1 | tail -25`
Expected: activation succeeds.

- [ ] **Step 5: Confirm the wrapper is on PATH**

Run: `command -v pg-pr-issues-jira-zr && pg-pr-issues-jira-zr search --jql 2>&1 | head -3`
Expected: a profile path is printed; `search --jql` errors on the missing value (proves the search dispatch is wired), NOT "real binary not found" and NOT a panic.

- [ ] **Step 6: Commit**

```bash
cd ~/phillipg_mbp/phillipg-nix-ziprecruiter
git add -A && git commit -m "feat(zr): enable pg-pr-issues-jira-zr wrapper for pr-pool jira-issues"
```

---

## Task 7: End-to-end verification (token-gated)

**Precondition:** the operator has minted a fresh Atlassian API token into `~/.jira_api_token` (the old one returns HTTP 401).

- [ ] **Step 1: Confirm the token is valid (operator action)**

The operator runs (prints only HTTP status, never the token):

```
! printf 'user = "%s:%s"\n' "phillipg@ziprecruiter.com" "$(tr -d '\n' < ~/.jira_api_token)" | curl -sS -K - -H "Accept: application/json" -o /dev/null -w 'HTTP %{http_code}\n' "https://ziprecruiter.atlassian.net/rest/api/3/myself"
```

Expected: `HTTP 200`. If 401, mint a new token before continuing.

- [ ] **Step 2: Run a jira-issues query through the deployed pr-pool**

Create a temp config with a `jira-issues` role (project `FINDEV`) and run from a beads-reachable dir:

```bash
cd ~/phillipg_mbp  # bd-reachable (pg2 workspace)
cat > /tmp/claude-502/jira-verify.toml <<'EOF'
[[role]]
name = "jira-test"
type = "ccpool"
cap = 1
enabled = true
[role.query]
type = "jira-issues"
[role.query.jira-issues]
project = "FINDEV"
[role.ccpool]
actor = "verify"
completion = "close-only"
on_failure = "unclaim"
on_dispatch_fail = "unclaim"
prompt = "noop"
[role.ccpool.budget]
tokens = 0
cost = 0
time = "0s"
EOF
PR_POOL_CONFIG=/tmp/claude-502/jira-verify.toml PR_POOL_REPO_ROOT="$PWD" PR_POOL_BEADS_PREFIX=pg2 pr-pool run-query jira-test 2>&1 | head -30
```

Expected: zero or more `FINDEV-…  jira-issue  <summary>` rows — NOT `executable file not found`, NOT `not yet implemented`, NOT a stub. An auth error here means the token/env wiring needs a fix, not the code.

- [ ] **Step 3: Record the result on the bead**

```bash
cd ~/phillipg_mbp && bd comment pg2-gpao "End-to-end verified: pr-pool run-query jira-test (FINDEV) returned <N> jira-issue items via pg-pr-issues-jira-zr search."
```

---

## Task 8: Close out

- [ ] **Step 1: Finish the agent-support branch**

Per `superpowers:finishing-a-development-branch` — present merge/PR options for branch `pg2-gpao-jira-issues-search` (pr-pool code + spec + plan) and the ziprecruiter branch `pg2-gpao-jira-search` (tool + deploy).

- [ ] **Step 2: Update beads**

```bash
cd ~/phillipg_mbp
bd close pg2-gpao   # once all tasks land + end-to-end verified
# pg2-5b4l criterion (d) is now satisfiable — re-run its verification and close if all 4 hold.
```

---

## Self-Review notes

- **Spec coverage:** Component A → Tasks 1–3; Component B → Tasks 4–5; Component C → Tasks 6–7. Truncation-via-envelope, exit-code contract, `os.Args` guard, `realBinary=${self.packages…}`, exec-recursion invariant, PATH check — all present.
- **Type consistency:** tool emits `searchItem`/`searchEnvelope` (`key,summary,status,issuetype,labels,url` / `items,truncated`); pr-pool parses `jiraSearchItem`/`jiraSearchEnvelope` with the identical JSON tags. `SearchIssues` signature `([]searchItem, bool, error)` matches its callers in Tasks 1–2.
- **Known soft spots (inherent, not placeholders):** Task 6 import-site path is discovered on the host (Step 1) because the machine module layout isn't pinned here; Task 7 is gated on the operator's fresh token.
