package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/freshness"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

var freshnessTestNow = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

// withHeartbeatLedger stubs the connector read the heartbeat performs.
func withHeartbeatLedger(t *testing.T, out string, err error) {
	t.Helper()
	orig := heartbeatRunLedgerShow
	t.Cleanup(func() { heartbeatRunLedgerShow = orig })
	heartbeatRunLedgerShow = func(context.Context) ([]byte, error) { return []byte(out), err }
}

func runHeartbeat(t *testing.T) (stderr string, err error) {
	t.Helper()
	c, _, ferr := rootCmd.Find([]string{"heartbeat"})
	if ferr != nil {
		t.Fatalf("rootCmd has no heartbeat subcommand: %v", ferr)
	}
	var errBuf bytes.Buffer
	c.SetContext(context.Background())
	c.SetOut(&bytes.Buffer{})
	c.SetErr(&errBuf)
	err = c.RunE(c, nil)
	return errBuf.String(), err
}

func runFreshness(t *testing.T) (stdout, stderr string, err error) {
	t.Helper()
	c, _, ferr := rootCmd.Find([]string{"freshness"})
	if ferr != nil || c.Name() != "freshness" {
		t.Fatalf("rootCmd has no freshness subcommand: %v", ferr)
	}
	_ = c.Flags().Set("json", "true")
	t.Cleanup(func() { _ = c.Flags().Set("json", "false") })
	var out, errBuf bytes.Buffer
	c.SetContext(context.Background())
	c.SetOut(&out)
	c.SetErr(&errBuf)
	err = c.RunE(c, nil)
	return out.String(), errBuf.String(), err
}

const heartbeatLedgerJSON = `[
  {"type":"pr","backend":"pg-connector-pr-github","query":"mine","cursor":null,"index_size":1,"version":1,"consumers":{},
   "refreshed_at":"2026-10-06T11:59:00Z","last_error":null},
  {"type":"pr","backend":"pg-connector-pr-github","query":"team","cursor":null,"index_size":1,"version":1,"consumers":{},
   "refreshed_at":"2026-10-06T11:40:00Z","last_error":{"at":"2026-10-06T11:55:00Z","code":"unavailable"}},
  {"type":"issue","backend":"pg-connector-issue-jira","query":"mine","cursor":null,"index_size":0,"version":0,"consumers":{},
   "refreshed_at":null,"last_error":null}
]`

func TestHeartbeatRecordsSourceFetchesFromTheLedger(t *testing.T) {
	st, openFresh := openTestStore(t)
	withOpenSeams(t, openTestConfig("o/r"), openFresh)
	withHeartbeatLedger(t, heartbeatLedgerJSON, nil)
	orig := heartbeatNow
	t.Cleanup(func() { heartbeatNow = orig })
	heartbeatNow = func() string { return "2026-10-06T12:00:00Z" }

	if _, err := runHeartbeat(t); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	if hb, found, _ := st.GetMeta(store.MetaKeyLastHeartbeat); !found || hb != "2026-10-06T12:00:00Z" {
		t.Errorf("last_heartbeat = %q, found=%v", hb, found)
	}
	kv, err := st.ListMetaPrefix(freshness.MetaKeyPrefix)
	if err != nil || len(kv) != 3 {
		t.Fatalf("source_fetch keys = %v, %v; want one per (backend, query)", kv, err)
	}
	var rec freshness.Record
	if err := json.Unmarshal([]byte(kv["source_fetch.pg-connector-pr-github.team"]), &rec); err != nil {
		t.Fatal(err)
	}
	if rec.LastError == nil || rec.LastError.Code != "unavailable" || rec.LastSuccessAt == nil {
		t.Errorf("team record = %+v, want its success and its last error kept", rec)
	}

	// Idempotent: a second heartbeat over the same ledger rewrites nothing.
	if _, err := runHeartbeat(t); err != nil {
		t.Fatalf("second heartbeat: %v", err)
	}
	again, _ := st.ListMetaPrefix(freshness.MetaKeyPrefix)
	for k, v := range kv {
		if again[k] != v {
			t.Errorf("%s changed on a repeated heartbeat", k)
		}
	}
}

// The liveness stamp never depends on the connector: an absent, failing or
// garbled `ledger show` costs only the source-age refresh.
func TestHeartbeatStillStampsWhenTheLedgerReadFails(t *testing.T) {
	for name, tc := range map[string]struct {
		out string
		err error
	}{
		"connector missing": {"", errors.New("executable file not found in $PATH")},
		"garbled output":    {"not json", nil},
	} {
		t.Run(name, func(t *testing.T) {
			st, openFresh := openTestStore(t)
			withOpenSeams(t, openTestConfig("o/r"), openFresh)
			withHeartbeatLedger(t, tc.out, tc.err)
			orig := heartbeatNow
			t.Cleanup(func() { heartbeatNow = orig })
			heartbeatNow = func() string { return "2026-10-06T12:00:00Z" }

			stderr, err := runHeartbeat(t)
			if err != nil {
				t.Fatalf("heartbeat returned %v, want nil (a ledger read failure is not a heartbeat failure)", err)
			}
			if hb, _, _ := st.GetMeta(store.MetaKeyLastHeartbeat); hb != "2026-10-06T12:00:00Z" {
				t.Errorf("last_heartbeat = %q, want it stamped", hb)
			}
			if !strings.Contains(stderr, "source ages not refreshed") {
				t.Errorf("stderr = %q, want the skipped refresh named", stderr)
			}
			if kv, _ := st.ListMetaPrefix(freshness.MetaKeyPrefix); len(kv) != 0 {
				t.Errorf("recorded %v from a failed read", kv)
			}
		})
	}
}

func seedSourceFetches(t *testing.T, st *store.Store) {
	t.Helper()
	at := func(d time.Duration) *time.Time { u := freshnessTestNow.Add(d); return &u }
	if err := freshness.Update(st, []freshness.LedgerRow{
		{Type: "pr", Backend: "pg-connector-pr-github", Query: "mine", RefreshedAt: at(-2 * time.Minute)},
		{Type: "issue", Backend: "pg-connector-issue-jira", Query: "mine", RefreshedAt: at(-40 * time.Minute)},
		{Type: "thread", Backend: "pg-connector-thread-slack", Query: "mine"},
	}); err != nil {
		t.Fatal(err)
	}
}

func withFreshnessSeams(t *testing.T, cfg *config.Config, cfgErr error, open func() (*store.Store, error)) {
	t.Helper()
	origCfg, origOpen, origNow := deskConfigLoad, deskStoreOpenReadOnly, freshnessNow
	t.Cleanup(func() { deskConfigLoad, deskStoreOpenReadOnly, freshnessNow = origCfg, origOpen, origNow })
	deskConfigLoad = func(context.Context) (*config.Config, error) { return cfg, cfgErr }
	deskStoreOpenReadOnly = open
	freshnessNow = func() time.Time { return freshnessTestNow }
}

func TestFreshnessVerbReportsPerSourceAge(t *testing.T) {
	st, openFresh := openTestStore(t)
	seedSourceFetches(t, st)
	withFreshnessSeams(t, openTestConfig("o/r"), nil, openFresh)

	out, _, err := runFreshness(t)
	if err != nil {
		t.Fatalf("freshness: %v", err)
	}
	var rep freshness.Report
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("not the freshness JSON: %v\n%s", err, out)
	}
	if rep.SchemaVersion != 1 || rep.Now != "2026-10-06T12:00:00Z" || rep.StaleAfterSeconds != 900 || !rep.AnyStale || len(rep.Sources) != 3 {
		t.Fatalf("report = %+v", rep)
	}
	byLabel := map[string]freshness.Row{}
	for _, r := range rep.Sources {
		byLabel[r.Label] = r
	}
	if r := byLabel["pr-github"]; r.Stale || *r.AgeSeconds != 120 {
		t.Errorf("pr-github = %+v, want fresh at 120s", r)
	}
	if r := byLabel["issue-jira"]; !r.Stale || *r.AgeSeconds != 2400 {
		t.Errorf("issue-jira = %+v, want stale at 2400s", r)
	}
	if r := byLabel["thread-slack"]; !r.Stale || r.AgeSeconds != nil || r.LastSuccessAt != nil {
		t.Errorf("thread-slack = %+v, want unknown and stale", r)
	}
	if !strings.Contains(out, `"age_seconds": null`) {
		t.Errorf("an unknown age must serialize as null:\n%s", out)
	}
}

func TestFreshnessVerbOnAnEmptyStoreIsNotStale(t *testing.T) {
	_, openFresh := openTestStore(t)
	withFreshnessSeams(t, openTestConfig("o/r"), nil, openFresh)
	out, _, err := runFreshness(t)
	if err != nil {
		t.Fatalf("freshness: %v", err)
	}
	var rep freshness.Report
	if err := json.Unmarshal([]byte(out), &rep); err != nil || rep.AnyStale || rep.Sources == nil || len(rep.Sources) != 0 {
		t.Errorf("report = %+v, %v; want no sources and any_stale false", rep, err)
	}
}

// A config that will not load degrades to defaults, so the indicator cannot
// vanish into a false all-clear; the failure is named on stderr.
func TestFreshnessVerbDegradesToDefaultsWhenConfigFails(t *testing.T) {
	st, openFresh := openTestStore(t)
	seedSourceFetches(t, st)
	withFreshnessSeams(t, nil, errors.New("config: no config file found"), openFresh)

	out, stderr, err := runFreshness(t)
	if err != nil {
		t.Fatalf("freshness: %v", err)
	}
	if !strings.Contains(stderr, "using defaults") {
		t.Errorf("stderr = %q, want the config failure named", stderr)
	}
	var rep freshness.Report
	if err := json.Unmarshal([]byte(out), &rep); err != nil || len(rep.Sources) != 3 || rep.StaleAfterSeconds != 900 {
		t.Errorf("report = %+v, %v", rep, err)
	}
}

func TestFreshnessVerbFailsWhenTheStoreCannotBeRead(t *testing.T) {
	withFreshnessSeams(t, openTestConfig("o/r"), nil, func() (*store.Store, error) {
		return nil, errors.New("no such file")
	})
	if _, _, err := runFreshness(t); err == nil || !strings.Contains(err.Error(), "open store") {
		t.Fatalf("err = %v, want an open-store failure (exit 1)", err)
	}
}

// INV-FRESH-1 and the verb's contract: it never writes the store.
func TestFreshnessVerbIsReadOnly(t *testing.T) {
	store.SetSynchronousForTests("OFF")
	path := filepath.Join(t.TempDir(), "ro.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	seedSourceFetches(t, st)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	withFreshnessSeams(t, openTestConfig("o/r"), nil, func() (*store.Store, error) { return store.OpenReadOnly(path) })
	if _, _, err := runFreshness(t); err != nil {
		t.Fatalf("freshness on a read-only handle: %v", err)
	}
}
