package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/links"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

const linksTestTemplate = "https://tracker.example.invalid/browse/%s"

// linksFixture builds a migrated store file with one PR (known, with a Jira
// edge) and returns its path plus a config for the verb.
func linksFixture(t *testing.T, cutover bool) (string, *config.Config) {
	t.Helper()
	store.SetSynchronousForTests("OFF")
	path := filepath.Join(t.TempDir(), "links.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertEntity(store.Entity{
		Repo: "acme/api", EntityType: "pr", EntityID: "acme/api#123", AsOf: "2026-10-01T10:00:00Z",
		Facts: `{"pr_show":{"url":"https://scm.example.invalid/acme/api/pull/123"}}`,
	}); err != nil {
		t.Fatal(err)
	}
	if cutover {
		if err := st.Cutover(); err != nil {
			t.Fatal(err)
		}
		if err := st.ReplaceDerivedXrefs("acme/api", "pr", "acme/api#123", []store.XrefLink{{
			Repo: "acme/api", FromType: "pr", FromID: "acme/api#123", ToType: "issue", ToID: "ABC-42",
			Relation: "jira", Origin: "derived:jira-key", FirstSeen: "2026-10-01T00:00:00Z", LastConfirmed: "2026-10-01T00:00:00Z",
		}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		SelfLogin:      "me",
		Repos:          []config.RepoConfig{{Remote: "acme/api"}},
		TicketPatterns: []string{"ABC-[0-9]+"},
		Links:          config.LinksConfig{IssueURLTemplate: linksTestTemplate},
	}
	return path, cfg
}

func withLinksSeams(t *testing.T, cfg *config.Config, open func() (*store.Store, error)) {
	t.Helper()
	origCfg, origOpen := deskConfigLoad, deskStoreOpenReadOnly
	t.Cleanup(func() { deskConfigLoad, deskStoreOpenReadOnly = origCfg, origOpen })
	deskConfigLoad = func(context.Context) (*config.Config, error) { return cfg, nil }
	deskStoreOpenReadOnly = open
}

func runLinks(t *testing.T, stdin string, flags []string, args ...string) (string, error) {
	t.Helper()
	c, _, ferr := rootCmd.Find([]string{"links"})
	if ferr != nil || c.Name() != "links" {
		t.Fatalf("rootCmd has no links subcommand: %v", ferr)
	}
	_ = c.Flags().Parse(nil)
	for _, f := range []string{"stdin", "json"} {
		_ = c.Flags().Set(f, "false")
	}
	for _, f := range flags {
		if err := c.Flags().Set(f, "true"); err != nil {
			t.Fatalf("set --%s: %v", f, err)
		}
	}
	t.Cleanup(func() {
		for _, f := range []string{"stdin", "json"} {
			_ = c.Flags().Set(f, "false")
		}
	})
	var out bytes.Buffer
	c.SetContext(context.Background())
	c.SetOut(&out)
	c.SetIn(strings.NewReader(stdin))
	err := c.RunE(c, args)
	return out.String(), err
}

func decodeLinks(t *testing.T, out string) links.Result {
	t.Helper()
	var r links.Result
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatalf("output is not the links JSON: %v\n%s", err, out)
	}
	return r
}

func TestLinksVerbArgsAndStdinProduceSchemaV1(t *testing.T) {
	path, cfg := linksFixture(t, true)
	withLinksSeams(t, cfg, func() (*store.Store, error) { return store.OpenReadOnly(path) })

	for name, run := range map[string]func() (string, error){
		"args": func() (string, error) {
			return runLinks(t, "", []string{"json"}, "pr:acme/api#123", "alert:grafana:abc")
		},
		"stdin": func() (string, error) {
			return runLinks(t, "pr:acme/api#123\n\nalert:grafana:abc\n", []string{"json", "stdin"})
		},
	} {
		t.Run(name, func(t *testing.T) {
			out, err := run()
			if err != nil {
				t.Fatalf("links: %v", err)
			}
			r := decodeLinks(t, out)
			if r.SchemaVersion != 1 || r.AsOf != "2026-10-01T10:00:00Z" || r.Degraded {
				t.Errorf("envelope = %+v", r)
			}
			pr := r.Items["pr:acme/api#123"]
			if !pr.Known || len(pr.Links) != 2 ||
				pr.Links[0] != (links.Link{Kind: "pr", Relation: "self", Label: "PR #123", URL: "https://scm.example.invalid/acme/api/pull/123"}) ||
				pr.Links[1] != (links.Link{Kind: "issue", Relation: "jira", Label: "ABC-42", URL: "https://tracker.example.invalid/browse/ABC-42"}) {
				t.Errorf("pr item = %+v", pr)
			}
			// INV-LINKS-2: an unknown ref is data, not an error (the call returned nil above).
			if a := r.Items["alert:grafana:abc"]; a.Known || len(a.Links) != 0 {
				t.Errorf("alert item = %+v", a)
			}
			if !strings.Contains(out, `"links": []`) {
				t.Errorf("unknown ref must serialize links as [], got:\n%s", out)
			}
		})
	}
}

// INV-LINKS-1: the verb never writes the store file.
func TestLinksVerbDoesNotWriteTheStore(t *testing.T) {
	path, cfg := linksFixture(t, true)
	withLinksSeams(t, cfg, func() (*store.Store, error) { return store.OpenReadOnly(path) })
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runLinks(t, "", []string{"json"}, "pr:acme/api#123"); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if sha256.Sum256(before) != sha256.Sum256(after) {
		t.Error("store file changed after the links verb ran")
	}
}

func TestLinksVerbDegradesOnUnmigratedStore(t *testing.T) {
	path, cfg := linksFixture(t, false)
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertXref(store.Xref{Repo: "acme/api", FromType: "pr", FromID: "acme/api#123", ToType: "issue", ToID: "ABC-42", Evidence: "title", FirstSeen: "2026-10-01T00:00:00Z", LastConfirmed: "2026-10-01T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	_ = st.Close()
	withLinksSeams(t, cfg, func() (*store.Store, error) { return store.OpenReadOnly(path) })

	out, err := runLinks(t, "", []string{"json"}, "pr:acme/api#123")
	if err != nil {
		t.Fatalf("links on an unmigrated store must not fail: %v", err)
	}
	r := decodeLinks(t, out)
	if !r.Degraded || len(r.Items["pr:acme/api#123"].Links) != 2 {
		t.Errorf("result = %+v; want degraded with self + Jira link", r)
	}
}

// Exit 1 (a returned error) means the store cannot be read at all.
func TestLinksVerbUnreadableStoreFails(t *testing.T) {
	_, cfg := linksFixture(t, true)
	withLinksSeams(t, cfg, func() (*store.Store, error) { return nil, errors.New("no such store") })
	if _, err := runLinks(t, "", []string{"json"}, "pr:acme/api#123"); err == nil {
		t.Fatal("links succeeded with an unreadable store")
	}
}

func TestLinksVerbNeedsRefsOrStdin(t *testing.T) {
	path, cfg := linksFixture(t, true)
	withLinksSeams(t, cfg, func() (*store.Store, error) { return store.OpenReadOnly(path) })
	if _, err := runLinks(t, "", []string{"json"}); err == nil {
		t.Fatal("links with no refs and no --stdin must be a usage error")
	}
	out, err := runLinks(t, "", []string{"json", "stdin"})
	if err != nil {
		t.Fatalf("--stdin with empty input: %v", err)
	}
	if r := decodeLinks(t, out); len(r.Items) != 0 {
		t.Errorf("items = %+v; want none", r.Items)
	}
}

// The attention item's pr id is pg-connector's "<owner>/<repo>#<n>"; it MUST
// equal the pg-desk pr entity id the pipeline writes (resolvePRRef rebuilds
// the same form), or every PR silently comes back unknown.
func TestAttentionPRIDEqualsPgDeskPREntityID(t *testing.T) {
	var feed []schema.AttentionItem
	if err := json.Unmarshal([]byte(`[{"type":"pr","id":"acme/api#123","summary":"re-review: Add retry"}]`), &feed); err != nil {
		t.Fatal(err)
	}
	item := feed[0]

	cfg := &config.Config{SelfLogin: "me", Repos: []config.RepoConfig{{Remote: "acme/api"}}}
	_, entityID, err := resolvePRRef(cfg, "123")
	if err != nil {
		t.Fatal(err)
	}
	if item.ID != entityID {
		t.Fatalf("attention pr id %q != pg-desk pr entity id %q", item.ID, entityID)
	}

	path, linkCfg := linksFixture(t, true)
	withLinksSeams(t, linkCfg, func() (*store.Store, error) { return store.OpenReadOnly(path) })
	out, err := runLinks(t, "", []string{"json"}, item.Type+":"+item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !decodeLinks(t, out).Items[item.Type+":"+item.ID].Known {
		t.Errorf("the attention item %s:%s is unknown to the links verb", item.Type, item.ID)
	}
}

// The verb wires config check_interpreters into the build links: the failing
// run an interpreter pattern excludes is not offered, the other one is.
func TestLinksVerbBuildLinksHonorConfigCheckInterpreters(t *testing.T) {
	store.SetSynchronousForTests("OFF")
	path := filepath.Join(t.TempDir(), "links-ci.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertEntity(store.Entity{
		Repo: "acme/api", EntityType: "pr", EntityID: "acme/api#123", AsOf: "2026-10-01T10:00:00Z",
		Facts: `{"pr_show":{"url":"https://scm.example.invalid/acme/api/pull/123","head_sha":"h"},` +
			`"ci":{"runs":[` +
			`{"id":"1","name":"policy-bot: approvals","status":"completed","conclusion":"failure","head_sha":"h","attempt":1,"url":"https://ci.example.invalid/run/1"},` +
			`{"id":"2","name":"unit-tests","status":"completed","conclusion":"failure","head_sha":"h","attempt":1,"url":"https://ci.example.invalid/run/2"}]}}`,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Cutover(); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Repos:             []config.RepoConfig{{Remote: "acme/api"}},
		CheckInterpreters: []config.CheckInterpreterConfig{{Patterns: []string{"^policy-bot"}, Type: "approval"}},
	}
	withLinksSeams(t, cfg, func() (*store.Store, error) { return store.OpenReadOnly(path) })
	out, err := runLinks(t, "", []string{"json"}, "pr:acme/api#123")
	if err != nil {
		t.Fatalf("links: %v", err)
	}
	var builds []links.Link
	for _, l := range decodeLinks(t, out).Items["pr:acme/api#123"].Links {
		if l.Kind == links.KindBuild {
			builds = append(builds, l)
		}
	}
	want := links.Link{Kind: "build", Relation: "ci", Label: "unit-tests (failure)", URL: "https://ci.example.invalid/run/2", State: "failure"}
	if len(builds) != 1 || builds[0] != want {
		t.Fatalf("build links = %+v; want [%+v]", builds, want)
	}
}
