package internal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/activity"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

var (
	testSince  = time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	testBefore = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
)

func TestListActivity_UnavailableNamesAuthorEmails(t *testing.T) {
	cases := map[string]json.RawMessage{
		"no config":   nil,
		"null":        json.RawMessage(`null`),
		"missing key": json.RawMessage(`{"repo_paths":["/x"]}`),
		"empty list":  json.RawMessage(`{"author_emails":[]}`),
		"blank only":  json.RawMessage(`{"author_emails":["", "  "]}`),
	}
	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			b := New(Options{Stderr: &bytes.Buffer{}})
			called := false
			b.collect = func(context.Context, Config, []string, time.Time, time.Time) ([]schema.ActivityItem, bool, error) {
				called = true
				return nil, false, nil
			}
			ctx := scriptout.WithConfig(context.Background(), cfg)
			res, err := b.ListActivity(ctx, testSince, testBefore)
			if res != nil {
				t.Errorf("result = %+v, want nil", res)
			}
			if !errors.Is(err, scriptout.ErrUnavailable) {
				t.Fatalf("err = %v, want ErrUnavailable", err)
			}
			if !strings.Contains(err.Error(), "author_emails") {
				t.Errorf("err = %q, want it to name author_emails", err)
			}
			if called {
				t.Error("collector ran before identity was established")
			}
		})
	}
}

func TestListActivity_MalformedConfigIsInvalidArgument(t *testing.T) {
	b := New(Options{Stderr: &bytes.Buffer{}})
	ctx := scriptout.WithConfig(context.Background(), json.RawMessage(`{"author_emails":"x"}`))
	_, err := b.ListActivity(ctx, testSince, testBefore)
	if !errors.Is(err, scriptout.ErrInvalidArgument) {
		t.Fatalf("err = %v, want ErrInvalidArgument", err)
	}
}

func TestListActivity_NoReposReturnsEmptyWellFormedResult(t *testing.T) {
	b := New(Options{Stderr: &bytes.Buffer{}})
	ctx := scriptout.WithConfig(context.Background(), json.RawMessage(`{"author_emails":["me@example.test"]}`))
	res, err := b.ListActivity(ctx, testSince, testBefore)
	if err != nil {
		t.Fatalf("ListActivity: %v", err)
	}
	raw, _ := json.Marshal(res)
	if want := `{"items":[],"truncated":false}`; string(raw) != want {
		t.Errorf("result = %s, want %s", raw, want)
	}
}

// TestConfigKeysDecodeThroughDispatchTable sends all four config keys as a
// JSON config block over the wire through the activity dispatch table, so a
// wrong JSON tag fails.
func TestConfigKeysDecodeThroughDispatchTable(t *testing.T) {
	b := New(Options{Stderr: &bytes.Buffer{}})
	var gotCfg Config
	var gotEmails []string
	b.collect = func(_ context.Context, cfg Config, emails []string, _, _ time.Time) ([]schema.ActivityItem, bool, error) {
		gotCfg, gotEmails = cfg, emails
		return []schema.ActivityItem{}, false, nil
	}
	table := activity.NewDispatchTable(b)

	req := `{"op":"list_activity","args":{"since":"2026-09-01T00:00:00Z","before":"2026-10-01T00:00:00Z"},` +
		`"config":{"author_emails":["a@example.test"," b@example.test "],"repo_paths":["/r/one","/r/two"],` +
		`"repo_search_paths":["/search"],"include_merges":true}}`
	var out bytes.Buffer
	if code := scriptout.ServeOne(table, strings.NewReader(req), &out); code != 0 {
		t.Fatalf("ServeOne exit = %d, out = %s", code, out.String())
	}

	want := Config{
		AuthorEmails:    []string{"a@example.test", " b@example.test "},
		RepoPaths:       []string{"/r/one", "/r/two"},
		RepoSearchPaths: []string{"/search"},
		IncludeMerges:   true,
	}
	if !reflect.DeepEqual(gotCfg, want) {
		t.Errorf("decoded config = %+v, want %+v", gotCfg, want)
	}
	if wantEmails := []string{"a@example.test", "b@example.test"}; !reflect.DeepEqual(gotEmails, wantEmails) {
		t.Errorf("emails = %v, want %v (trimmed)", gotEmails, wantEmails)
	}
}

func TestConfigDefaults(t *testing.T) {
	var cfg Config
	if err := scriptout.Decode(json.RawMessage(`{"author_emails":["a@example.test"]}`), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.IncludeMerges {
		t.Error("include_merges must default to false")
	}
}

func TestActivityKinds(t *testing.T) {
	if !reflect.DeepEqual(ActivityKinds, []string{"commit"}) {
		t.Errorf("ActivityKinds = %v, want [commit]", ActivityKinds)
	}
}
