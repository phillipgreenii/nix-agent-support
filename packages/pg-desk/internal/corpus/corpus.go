// Package corpus is the committed golden corpus of pg-desk's interpretation
// tests: a set of synthetic gather payloads (one JSON file per fixture under
// testdata, a full gather.Facts, gather.IssueFacts or gather.ThreadFacts value
// each) plus the configuration and clock every golden test interprets them
// under, and the byte-exact row rendering the row goldens use.
//
// It is test support: only _test.go files import it. The fixtures are written
// by the generator in corpus_gen_test.go (PG_DESK_UPDATE_GOLDEN=1) and are the
// committed source of truth afterwards; a test fails when the generator and the
// committed files disagree.
package corpus

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/interpret"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

//go:embed testdata/*.json
var files embed.FS

// Repo is the one configured repository of the corpus.
const Repo = "acme/widgets"

// UpdateEnv is the environment variable that rewrites goldens (the module's
// existing golden-refresh switch).
const UpdateEnv = "PG_DESK_UPDATE_GOLDEN"

// Fixture is one corpus entry.
type Fixture struct {
	// Name is the file stem, "<type>-<description>".
	Name string
	// Type is the entity type: pr, issue or thread.
	Type string
	// ID is the entity id the fixture is stored under.
	ID string
	// Payload is the compacted gather payload (the entity.facts text).
	Payload []byte
}

// Fixtures returns every fixture, sorted by name.
func Fixtures() ([]Fixture, error) {
	entries, err := files.ReadDir("testdata")
	if err != nil {
		return nil, err
	}
	var out []Fixture
	for _, e := range entries {
		raw, err := files.ReadFile("testdata/" + e.Name())
		if err != nil {
			return nil, err
		}
		var buf bytes.Buffer
		if err := json.Compact(&buf, raw); err != nil {
			return nil, fmt.Errorf("corpus: %s: %w", e.Name(), err)
		}
		name := strings.TrimSuffix(e.Name(), ".json")
		typ, _, _ := strings.Cut(name, "-")
		f := Fixture{Name: name, Type: typ, Payload: buf.Bytes()}
		if f.ID, err = idOf(typ, f.Payload); err != nil {
			return nil, fmt.Errorf("corpus: %s: %w", e.Name(), err)
		}
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// idOf derives the entity id from the payload: a PR is "<repo>#<number>", an
// issue and a thread carry their own id in the show result. A pr payload with
// no pr_show (the empty-facts case) is stored under a fixed id.
func idOf(typ string, payload []byte) (string, error) {
	switch typ {
	case "pr":
		var f struct {
			PRShow struct {
				Number int `json:"number"`
			} `json:"pr_show"`
			AsOf string `json:"as_of"`
		}
		if err := json.Unmarshal(payload, &f); err != nil {
			return "", err
		}
		if f.PRShow.Number == 0 {
			return Repo + "#900", nil
		}
		return fmt.Sprintf("%s#%d", Repo, f.PRShow.Number), nil
	case "issue":
		var f struct {
			Show struct {
				ID string `json:"id"`
			} `json:"issue_show"`
		}
		if err := json.Unmarshal(payload, &f); err != nil {
			return "", err
		}
		if f.Show.ID == "" {
			return "PROJ-900", nil
		}
		return f.Show.ID, nil
	case "thread":
		var f struct {
			Show struct {
				ID string `json:"id"`
			} `json:"thread_show"`
		}
		if err := json.Unmarshal(payload, &f); err != nil {
			return "", err
		}
		if f.Show.ID == "" {
			return "C900/1.0", nil
		}
		return f.Show.ID, nil
	}
	return "", fmt.Errorf("unknown fixture type %q", typ)
}

// Clock is the fixed clock every golden test uses.
func Clock() interpret.Clock {
	return interpret.FixedClock(time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC))
}

// Config is the configuration the corpus is interpreted under.
func Config() *config.Config {
	return &config.Config{
		SelfLogin:         "alice-dev",
		SelfIssueOwner:    "alice-dev",
		TeamMembers:       []string{"teammate", "teammate-b"},
		ApproverAllowlist: []string{"review-bot"},
		Repos:             []config.RepoConfig{{Remote: Repo}},
	}
}

// RenderRow renders an interpretation row column by column, one "name: value"
// line per column in table order, as exact bytes. It is not a dump of the Go
// struct, and it leaves SyncError out so removing that field changes no golden.
func RenderRow(r store.Interpretation) []byte {
	var b bytes.Buffer
	line := func(name, value string) {
		if value == "" {
			fmt.Fprintf(&b, "%s:\n", name) // no trailing space on an empty column
			return
		}
		fmt.Fprintf(&b, "%s: %s\n", name, value)
	}
	line("repo", r.Repo)
	line("entity_type", r.EntityType)
	line("entity_id", r.EntityID)
	line("ownership", r.Ownership)
	line("enrichment", r.Enrichment)
	line("urgency", r.Urgency)
	line("category", r.Category)
	line("dispositions", r.Dispositions)
	line("approvals", r.Approvals)
	line("gate_state", r.GateState)
	line("match_reasons", r.MatchReasons)
	line("panel", r.Panel)
	line("ready_to_promote", fmt.Sprint(r.ReadyToPromote))
	line("degraded", fmt.Sprint(r.Degraded))
	line("as_of", r.AsOf)
	return b.Bytes()
}

// AssertBytes compares got with the golden file at path as exact bytes (a
// golden is not decoded: key order and whitespace count). With UpdateEnv=1 it
// writes the file first.
func AssertBytes(t testing.TB, path string, got []byte) {
	t.Helper()
	if os.Getenv(UpdateEnv) == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("golden %s: %v (create it with %s=1)", path, err, UpdateEnv)
	}
	if !bytes.Equal(want, got) {
		t.Errorf("%s drifted from its golden.\n--- want ---\n%s\n--- got ---\n%s", path, want, got)
	}
}

// ModuleDir returns the absolute path of the pg-desk module root, so a test in
// any package can name a shared golden directory.
func ModuleDir() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..")
}
