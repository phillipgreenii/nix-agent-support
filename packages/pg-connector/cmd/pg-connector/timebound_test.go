package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
	"github.com/spf13/cobra"
)

func TestParseTimeBound(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		in      string
		want    time.Time
		wantErr bool
	}{
		{"rfc3339 utc", "2026-10-01T00:00:00Z", time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), false},
		{"rfc3339 offset normalized to utc", "2026-10-01T02:00:00+02:00", time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), false},
		{"go duration hours", "168h", now.Add(-168 * time.Hour), false},
		{"go duration minutes", "90m", now.Add(-90 * time.Minute), false},
		{"go duration zero", "0s", now, false},
		{"day suffix", "7d", now.Add(-7 * 24 * time.Hour), false},
		{"day suffix one", "1d", now.Add(-24 * time.Hour), false},
		{"zero days", "0d", now, false},
		{"surrounding whitespace", " 7d ", now.Add(-7 * 24 * time.Hour), false},
		{"empty", "", time.Time{}, true},
		{"garbage", "yesterday", time.Time{}, true},
		{"negative duration", "-1h", time.Time{}, true},
		{"negative days", "-7d", time.Time{}, true},
		{"plus days", "+7d", time.Time{}, true},
		{"fractional days", "1.5d", time.Time{}, true},
		{"bare d", "d", time.Time{}, true},
		{"days overflow", "99999999999d", time.Time{}, true},
		{"unit without number", "h", time.Time{}, true},
		{"date only is not rfc3339", "2026-10-01", time.Time{}, true},
		{"uppercase D", "7D", time.Time{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseTimeBound(tt.in, now)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && !got.Equal(tt.want) {
				t.Fatalf("got %s, want %s", got, tt.want)
			}
		})
	}
}

// DST-free arithmetic: "1d" across a spring-forward transition is exactly 24h
// of elapsed time, never a calendar day.
func TestParseTimeBound_DayIsFixed24Hours(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("tzdata unavailable: %v", err)
	}
	now := time.Date(2026, 3, 8, 12, 0, 0, 0, ny) // after the 2026-03-08 spring-forward
	got, err := parseTimeBound("1d", now)
	if err != nil {
		t.Fatal(err)
	}
	if d := now.Sub(got); d != 24*time.Hour {
		t.Fatalf("1d spanned %s, want exactly 24h", d)
	}
	if got.Location() != time.UTC {
		t.Fatalf("result location = %v, want UTC", got.Location())
	}
}

func newBoundsTestCmd() (*cobra.Command, *timeBoundFlags) {
	cmd := &cobra.Command{Use: "x"}
	return cmd, addTimeBoundFlags(cmd, "things updated", "asymmetry note")
}

func TestTimeBoundFlags_Resolve(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		args       []string
		wantSince  bool
		wantBefore bool
		wantErr    bool
	}{
		{"no flags is zero range", nil, false, false, false},
		{"since only", []string{"--since", "7d"}, true, false, false},
		{"before only", []string{"--before", "1d"}, false, true, false},
		{"both ordered", []string{"--since", "7d", "--before", "1d"}, true, true, false},
		{"both reversed", []string{"--since", "1d", "--before", "7d"}, false, false, true},
		{"equal bounds", []string{"--since", "2026-10-01T00:00:00Z", "--before", "2026-10-01T00:00:00Z"}, false, false, true},
		{"bad since", []string{"--since", "nope"}, false, false, true},
		{"bad before", []string{"--before", "-3h"}, false, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd, f := newBoundsTestCmd()
			if err := cmd.ParseFlags(tt.args); err != nil {
				t.Fatal(err)
			}
			r, err := f.resolveInvalidArgument(now)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				if !errors.Is(err, scriptout.ErrInvalidArgument) {
					t.Fatalf("err = %v, want invalid_argument", err)
				}
				return
			}
			if !r.Since.IsZero() != tt.wantSince || !r.Before.IsZero() != tt.wantBefore {
				t.Fatalf("range = %+v, want since=%v before=%v", r, tt.wantSince, tt.wantBefore)
			}
		})
	}
}

func TestListBackendConfig_KeysOnlyWhenBounded(t *testing.T) {
	writeBackendsConfig(t, "pr", "be-a", `{"queries":{"mine":"x"}}`)
	reg, err := LoadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	unbounded, err := listBackendConfig(reg, "be-a", scriptout.TimeRange{})
	if err != nil {
		t.Fatal(err)
	}
	static, _ := reg.BackendConfig("be-a")
	if string(unbounded) != string(static) {
		t.Fatalf("unbounded config %s differs from static %s", unbounded, static)
	}
	var m map[string]any
	_ = json.Unmarshal(unbounded, &m)
	if _, ok := m[scriptout.ConfigKeyListSince]; ok {
		t.Fatalf("list_since present without a flag: %v", m)
	}

	since := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	bounded, err := listBackendConfig(reg, "be-a", scriptout.TimeRange{Since: since})
	if err != nil {
		t.Fatal(err)
	}
	m = nil
	_ = json.Unmarshal(bounded, &m)
	if m[scriptout.ConfigKeyListSince] != "2026-09-28T00:00:00Z" {
		t.Fatalf("list_since = %v", m[scriptout.ConfigKeyListSince])
	}
	if _, ok := m[scriptout.ConfigKeyListBefore]; ok {
		t.Fatalf("list_before present without --before: %v", m)
	}
	if _, ok := m["queries"]; !ok {
		t.Fatalf("static queries block lost: %v", m)
	}
}

// --- CLI-level behavior: list/search flags, config-key presence, changes
// rejection (bead pg2-ttk9t) ---------------------------------------------

// writeBackendsConfig writes a registry registering backend under
// connector.<entityType> (or search.sources when entityType is "search")
// with the given backends.<backend> block (JSON is valid YAML) and points
// $PG_PR_CONFIG / $XDG_STATE_HOME at temp paths.
func writeBackendsConfig(t *testing.T, entityType, backend, blockJSON string) {
	t.Helper()
	dir := t.TempDir()
	var body string
	if entityType == "search" {
		body = "search:\n  sources:\n    - " + backend + "\n"
	} else {
		body = "connector:\n  " + entityType + ":\n    - " + backend + "\n"
	}
	if blockJSON != "" {
		body += "backends:\n  " + backend + ": " + blockJSON + "\n"
	}
	cfg := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfg, []byte(body), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("PG_PR_CONFIG", cfg)
	t.Setenv("XDG_STATE_HOME", dir)
}

// writeCapturingFakeBackend installs a fake backend that records the last
// request it received to capturePath and replies with stdout.
func writeCapturingFakeBackend(t *testing.T, name, capturePath, stdout string) {
	t.Helper()
	dir := t.TempDir()
	content := "#!/bin/sh\ncat > '" + capturePath + "'\ncat <<'FAKE_BACKEND_EOF'\n" + stdout + "\nFAKE_BACKEND_EOF\n"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o755); err != nil {
		t.Fatalf("write capturing backend: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// capturedConfig decodes the "config" member of the request a capturing
// backend recorded (nil when the request carried none).
func capturedConfig(t *testing.T, capturePath string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatalf("backend was never invoked: %v", err)
	}
	var req struct {
		Op     string         `json:"op"`
		Config map[string]any `json:"config"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatalf("decode captured request %q: %v", raw, err)
	}
	return req.Config
}

const (
	emptyPRListResp    = `{"protocolVersion":1,"schemaVersion":1,"result":{"entities":[],"present_ids":[],"cursor":null,"truncated":false}}`
	emptyIssueListResp = `{"protocolVersion":1,"schemaVersion":1,"result":{"entities":[],"present_ids":[],"cursor":null,"truncated":false}}`
)

func TestPrList_Since_DeliversListSinceOnly(t *testing.T) {
	capture := filepath.Join(t.TempDir(), "req.json")
	writeCapturingFakeBackend(t, "be-pr-bounded", capture, emptyPRListResp)
	writeBackendsConfig(t, "pr", "be-pr-bounded", `{"queries":{"mine":"x"}}`)

	before := time.Now()
	stdout, _, code := executePr(t, []string{"pr", "list", "--query", "mine", "--since", "7d"})
	if code != 0 {
		t.Fatalf("exit = %d; stdout=%s", code, stdout)
	}
	cfg := capturedConfig(t, capture)
	got, ok := cfg[scriptout.ConfigKeyListSince].(string)
	if !ok {
		t.Fatalf("list_since missing from config: %v", cfg)
	}
	ts, err := time.Parse(time.RFC3339, got)
	if err != nil {
		t.Fatalf("list_since %q is not RFC3339 (the backend must never see a raw 7d): %v", got, err)
	}
	if want := before.Add(-7 * 24 * time.Hour); ts.Before(want.Add(-time.Minute)) || ts.After(want.Add(time.Minute)) {
		t.Fatalf("list_since = %s, want about %s", ts, want)
	}
	if _, present := cfg[scriptout.ConfigKeyListBefore]; present {
		t.Fatalf("list_before present without --before: %v", cfg)
	}
	if _, present := cfg["queries"]; !present {
		t.Fatalf("static queries block was dropped: %v", cfg)
	}
}

func TestPrList_NoFlags_ConfigHasNoRangeKeys(t *testing.T) {
	capture := filepath.Join(t.TempDir(), "req.json")
	writeCapturingFakeBackend(t, "be-pr-unbounded", capture, emptyPRListResp)
	writeBackendsConfig(t, "pr", "be-pr-unbounded", `{"queries":{"mine":"x"}}`)

	if stdout, _, code := executePr(t, []string{"pr", "list", "--query", "mine"}); code != 0 {
		t.Fatalf("exit = %d; stdout=%s", code, stdout)
	}
	cfg := capturedConfig(t, capture)
	for _, k := range []string{scriptout.ConfigKeyListSince, scriptout.ConfigKeyListBefore, scriptout.ConfigKeySearchSince, scriptout.ConfigKeySearchBefore} {
		if _, present := cfg[k]; present {
			t.Fatalf("%s present on an unbounded call: %v", k, cfg)
		}
	}
	if len(cfg) != 1 {
		t.Fatalf("config = %v, want exactly the static block", cfg)
	}
}

func TestIssueList_SinceAndBefore_RFC3339(t *testing.T) {
	capture := filepath.Join(t.TempDir(), "req.json")
	writeCapturingFakeBackend(t, "be-issue-bounded", capture, emptyIssueListResp)
	writeBackendsConfig(t, "issue", "be-issue-bounded", "")

	stdout, _, code := executePr(t, []string{
		"issue", "list", "--query", "mine",
		"--since", "2026-10-01T00:00:00Z", "--before", "2026-10-05T12:00:00+02:00",
	})
	if code != 0 {
		t.Fatalf("exit = %d; stdout=%s", code, stdout)
	}
	cfg := capturedConfig(t, capture)
	if cfg[scriptout.ConfigKeyListSince] != "2026-10-01T00:00:00Z" || cfg[scriptout.ConfigKeyListBefore] != "2026-10-05T10:00:00Z" {
		t.Fatalf("config = %v, want both bounds as UTC RFC3339", cfg)
	}
}

func TestList_InvalidBound_IsInvalidArgumentBeforeAnyDispatch(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"pr garbage since", []string{"pr", "list", "--query", "mine", "--since", "yesterday"}},
		{"issue negative before", []string{"issue", "list", "--query", "mine", "--before", "-2h"}},
		{"pr since after before", []string{"pr", "list", "--query", "mine", "--since", "1d", "--before", "7d"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			capture := filepath.Join(t.TempDir(), "req.json")
			writeCapturingFakeBackend(t, "be-never-called", capture, emptyPRListResp)
			writeBackendsConfig(t, tc.args[0], "be-never-called", "")
			stdout, _, code := executePr(t, tc.args)
			if code != 1 {
				t.Fatalf("exit = %d, want 1; stdout=%s", code, stdout)
			}
			if !strings.Contains(stdout, `"invalid_argument"`) {
				t.Fatalf("stdout = %s, want an invalid_argument error envelope", stdout)
			}
			if _, err := os.Stat(capture); err == nil {
				t.Fatal("backend was invoked despite an invalid bound")
			}
		})
	}
}

// A bounded list never serves the entity-cache fallback: cached entries are
// not window-filtered, so serving them would return entities outside the
// requested range.
func TestPrList_Bounded_SkipsCacheFallback(t *testing.T) {
	writeOpAwareFakeBackend(t, "be-pr-down", map[string]string{
		"list": `{"protocolVersion":1,"schemaVersion":1,"error":{"code":"unavailable","message":"backend down"}}`,
	}, `{}`)
	writeBackendsConfig(t, "pr", "be-pr-down", "")
	seedCache(t, CacheKey{Type: "pr", Backend: "be-pr-down"}, &Cache{Entries: map[string]CacheEntry{
		"o/r#1": {
			Content:    json.RawMessage(`{"id":"o/r#1","repo":"o/r","number":1,"title":"t","state":"open","branch":"b","base":"main","author":"a","url":"u","draft":false,"merged":false,"as_of":"2026-01-01T00:00:00Z","stale":false}`),
			AsOf:       time.Now(),
			LastAccess: time.Now(),
		},
	}})

	stdout, _, code := executePr(t, []string{"pr", "list", "--query", "mine", "--since", "7d"})
	if code != 3 {
		t.Fatalf("exit = %d, want 3 (sole backend degraded, no fallback); stdout=%s", code, stdout)
	}
	var outcome prListOutcome
	if err := json.Unmarshal([]byte(stdout), &outcome); err != nil {
		t.Fatal(err)
	}
	if len(outcome.Entities) != 0 || len(outcome.PresentIDs) != 0 {
		t.Fatalf("bounded call served cached entities: %+v", outcome)
	}
}

func TestChanges_RejectsTimeBounds(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"pr since", []string{"pr", "changes", "--query", "mine", "--consumer", "c1", "--since", "7d"}},
		{"issue before", []string{"issue", "changes", "--query", "mine", "--consumer", "c1", "--before", "2026-10-01T00:00:00Z"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			capture := filepath.Join(t.TempDir(), "req.json")
			writeCapturingFakeBackend(t, "be-changes-never-called", capture, emptyPRListResp)
			writeBackendsConfig(t, tc.args[0], "be-changes-never-called", "")
			stdout, _, code := executePr(t, tc.args)
			if code != 1 {
				t.Fatalf("exit = %d, want 1; stdout=%s", code, stdout)
			}
			if !strings.Contains(stdout, `"invalid_argument"`) || !strings.Contains(stdout, "tombstone") {
				t.Fatalf("stdout = %s, want an invalid_argument envelope explaining the tombstone hazard", stdout)
			}
			if _, err := os.Stat(capture); err == nil {
				t.Fatal("changes invoked a backend despite the rejected flag")
			}
		})
	}
}

func TestChanges_HelpDoesNotAdvertiseTimeBounds(t *testing.T) {
	stdout, _, code := executePr(t, []string{"pr", "changes", "--help"})
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if strings.Contains(stdout, "--since") || strings.Contains(stdout, "--before") {
		t.Fatalf("changes --help advertises a time bound:\n%s", stdout)
	}
}

func TestListHelp_DocumentsAsymmetry(t *testing.T) {
	for _, typ := range []string{"pr", "issue"} {
		stdout, _, _ := executePr(t, []string{typ, "list", "--help"})
		for _, want := range []string{"--since", "--before", "list_since/list_before", "unbounded"} {
			if !strings.Contains(stdout, want) {
				t.Errorf("%s list --help missing %q:\n%s", typ, want, stdout)
			}
		}
	}
}

func TestSearch_Bounds_DeliveredAsSearchKeys(t *testing.T) {
	capture := filepath.Join(t.TempDir(), "req.json")
	writeCapturingFakeBackend(t, "be-search-bounded", capture, `{"protocolVersion":1,"schemaVersion":1,"result":[]}`)
	writeBackendsConfig(t, "search", "be-search-bounded", `{"rate_reserve_points":5}`)

	stdout, _, code := executePr(t, []string{"search", "flaky", "--since", "7d", "--before", "1d"})
	if code != 0 {
		t.Fatalf("exit = %d; stdout=%s", code, stdout)
	}
	cfg := capturedConfig(t, capture)
	for _, k := range []string{scriptout.ConfigKeySearchSince, scriptout.ConfigKeySearchBefore} {
		s, ok := cfg[k].(string)
		if !ok {
			t.Fatalf("%s missing: %v", k, cfg)
		}
		if _, err := time.Parse(time.RFC3339, s); err != nil {
			t.Fatalf("%s = %q is not RFC3339: %v", k, s, err)
		}
	}
	if _, present := cfg[scriptout.ConfigKeyListSince]; present {
		t.Fatalf("list_since leaked into a search call: %v", cfg)
	}
	if cfg["rate_reserve_points"] != float64(5) {
		t.Fatalf("static block not merged: %v", cfg)
	}
}

func TestSearch_NoFlags_NoRangeKeys(t *testing.T) {
	capture := filepath.Join(t.TempDir(), "req.json")
	writeCapturingFakeBackend(t, "be-search-unbounded", capture, `{"protocolVersion":1,"schemaVersion":1,"result":[]}`)
	writeBackendsConfig(t, "search", "be-search-unbounded", "")

	if stdout, _, code := executePr(t, []string{"search", "flaky"}); code != 0 {
		t.Fatalf("exit = %d; stdout=%s", code, stdout)
	}
	cfg := capturedConfig(t, capture)
	for _, k := range []string{scriptout.ConfigKeySearchSince, scriptout.ConfigKeySearchBefore} {
		if _, present := cfg[k]; present {
			t.Fatalf("%s present on an unbounded search: %v", k, cfg)
		}
	}
}

func TestSearch_InvalidBound_IsInvalidArgument(t *testing.T) {
	capture := filepath.Join(t.TempDir(), "req.json")
	writeCapturingFakeBackend(t, "be-search-never", capture, `{"protocolVersion":1,"schemaVersion":1,"result":[]}`)
	writeBackendsConfig(t, "search", "be-search-never", "")
	stdout, _, code := executePr(t, []string{"search", "flaky", "--since", "soon"})
	if code != 1 || !strings.Contains(stdout, `"invalid_argument"`) {
		t.Fatalf("exit = %d, stdout=%s; want 1 + invalid_argument", code, stdout)
	}
	if _, err := os.Stat(capture); err == nil {
		t.Fatal("search invoked a backend despite an invalid bound")
	}
}

func TestSearchHelp_DocumentsAsymmetry(t *testing.T) {
	stdout, _, _ := executePr(t, []string{"search", "--help"})
	for _, want := range []string{"--since", "--before", "search_since/search_before", "unbounded"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("search --help missing %q:\n%s", want, stdout)
		}
	}
}
