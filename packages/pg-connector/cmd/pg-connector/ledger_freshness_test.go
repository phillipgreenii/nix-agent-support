package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// Freshness stamps on the delta ledger (INV-LEDGER-FRESH-1..4, bead
// pg2-ll4dw.1; spec 2026-10-05 pg-desk attention evaluator and connector
// refresh cache, INV-FRESH-1..4).

var freshnessNow = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

func TestLedgerRecordFetchSuccess_StampsRefreshedAt(t *testing.T) {
	l := newEmptyLedger()
	if l.RefreshedAt != nil || l.LastError != nil {
		t.Fatalf("fresh ledger carries stamps: %+v / %+v", l.RefreshedAt, l.LastError)
	}
	l.RecordFetchSuccess(freshnessNow)
	if l.RefreshedAt == nil || !l.RefreshedAt.Equal(freshnessNow) {
		t.Fatalf("RefreshedAt = %v, want %v", l.RefreshedAt, freshnessNow)
	}
}

func TestLedgerRecordFetchError_KeepsPriorSuccessAndRecordsError(t *testing.T) {
	l := newEmptyLedger()
	l.RecordFetchSuccess(freshnessNow)
	later := freshnessNow.Add(time.Minute)
	l.RecordFetchError(later, "unavailable")
	if l.RefreshedAt == nil || !l.RefreshedAt.Equal(freshnessNow) {
		t.Fatalf("a failure moved RefreshedAt: %v, want %v", l.RefreshedAt, freshnessNow)
	}
	if l.LastError == nil || l.LastError.Code != "unavailable" || !l.LastError.At.Equal(later) {
		t.Fatalf("LastError = %+v, want {at=%v code=unavailable}", l.LastError, later)
	}
}

func TestLedgerRecordFetch_TruncatedIsNotASuccess(t *testing.T) {
	l := newEmptyLedger()
	l.RecordFetchOutcome(freshnessNow, true, nil)
	if l.RefreshedAt != nil {
		t.Fatalf("a truncated answer stamped RefreshedAt = %v (INV-FRESH-3)", l.RefreshedAt)
	}
	if l.LastError == nil || l.LastError.Code != ledgerErrorTruncated {
		t.Fatalf("LastError = %+v, want code %q", l.LastError, ledgerErrorTruncated)
	}
}

func TestLedgerRecordFetchOutcome_Classification(t *testing.T) {
	cases := []struct {
		name        string
		truncated   bool
		err         error
		wantStamp   bool
		wantErrCode string // "" means no last_error written
	}{
		{"complete answer", false, nil, true, ""},
		{"truncated answer", true, nil, false, ledgerErrorTruncated},
		{"unavailable", false, scriptout.WrapError(scriptout.ErrUnavailable, "down"), false, "unavailable"},
		{"unauthenticated", false, scriptout.WrapError(scriptout.ErrUnauthenticated, "no"), false, "unauthenticated"},
		{"unwrapped error falls back to unavailable", false, errors.New("boom"), false, "unavailable"},
		{"unknown_op is not applicable, not a failure", false, scriptout.WrapError(scriptout.ErrUnknownOp, "x"), false, ""},
		{"query_not_recognized is not a failure of the source", false, scriptout.WrapError(scriptout.ErrQueryNotRecognized, "x"), false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l := newEmptyLedger()
			l.RecordFetchOutcome(freshnessNow, tc.truncated, tc.err)
			if got := l.RefreshedAt != nil; got != tc.wantStamp {
				t.Fatalf("stamped = %v, want %v", got, tc.wantStamp)
			}
			gotCode := ""
			if l.LastError != nil {
				gotCode = l.LastError.Code
			}
			if gotCode != tc.wantErrCode {
				t.Fatalf("last_error code = %q, want %q", gotCode, tc.wantErrCode)
			}
		})
	}
}

func TestSaveLoadLedger_RoundTripsFreshnessStamps(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	key := LedgerKey{Type: "pr", Backend: "b", Query: "mine"}
	l := newEmptyLedger()
	l.RecordFetchSuccess(freshnessNow)
	l.RecordFetchError(freshnessNow.Add(time.Minute), "unavailable")
	if err := saveLedger(key, l); err != nil {
		t.Fatalf("saveLedger: %v", err)
	}
	got, err := loadLedger(key)
	if err != nil {
		t.Fatalf("loadLedger: %v", err)
	}
	if got.RefreshedAt == nil || !got.RefreshedAt.Equal(freshnessNow) {
		t.Fatalf("RefreshedAt = %v after round trip", got.RefreshedAt)
	}
	if got.LastError == nil || got.LastError.Code != "unavailable" {
		t.Fatalf("LastError = %+v after round trip", got.LastError)
	}
}

func TestLoadLedger_LegacyFileWithoutStampsHasNilStamps(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	key := LedgerKey{Type: "pr", Backend: "b", Query: "mine"}
	path, err := ledgerPath(key)
	if err != nil {
		t.Fatalf("ledgerPath: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"entries":{},"version":2,"consumers":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	l, err := loadLedger(key)
	if err != nil {
		t.Fatalf("loadLedger: %v", err)
	}
	if l.RefreshedAt != nil || l.LastError != nil {
		t.Fatalf("legacy ledger gained stamps: %v / %+v (INV-FRESH-4: no recorded success stays unknown)", l.RefreshedAt, l.LastError)
	}
}

// loadStamps reloads key's ledger and returns its freshness stamps.
func loadStamps(t *testing.T, key LedgerKey) (*time.Time, *LedgerError) {
	t.Helper()
	l, err := loadLedger(key)
	if err != nil {
		t.Fatalf("loadLedger %+v: %v", key, err)
	}
	return l.RefreshedAt, l.LastError
}

func TestRun_PrChanges_CompleteFetch_StampsRefreshedAt(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	writeOpAwareFakeBackend(t, "backend-fresh-ok", map[string]string{
		"list": `{"protocolVersion":1,"schemaVersion":1,"result":{"entities":[],"present_ids":[],"cursor":null,"truncated":false}}`,
	}, `{}`)
	writeConfigFor(t, "backend-fresh-ok")
	key := LedgerKey{Type: "pr", Backend: "backend-fresh-ok", Query: "mine"}

	before := time.Now().Add(-time.Second)
	if _, _, code := executePr(t, []string{"pr", "changes", "--query", "mine", "--consumer", "c1"}); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	after := time.Now().Add(time.Second)

	at, lastErr := loadStamps(t, key)
	if at == nil || at.Before(before) || at.After(after) {
		t.Fatalf("RefreshedAt = %v, want within [%v, %v]", at, before, after)
	}
	if lastErr != nil {
		t.Fatalf("LastError = %+v after a clean fetch, want none", lastErr)
	}
}

// INV-FRESH-2: a cache-served answer MUST NOT advance freshness.
func TestRun_PrChanges_Cached_DoesNotStamp(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	marker := filepath.Join(t.TempDir(), "invoked.log")
	writeMarkerBackend(t, "backend-fresh-cached", marker)
	writeConfigFor(t, "backend-fresh-cached")
	key := LedgerKey{Type: "pr", Backend: "backend-fresh-cached", Query: "mine"}

	old := freshnessNow.Add(-48 * time.Hour)
	seedLedger(t, key, &Ledger{
		Entries:     map[string]LedgerEntry{"o/r#1": {Hash: "h1", VersionLastChanged: 1}},
		Version:     1,
		Consumers:   map[string]ConsumerState{},
		RefreshedAt: &old,
	})

	if _, _, code := executePr(t, []string{"pr", "changes", "--query", "mine", "--consumer", "c1", "--cached"}); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	at, lastErr := loadStamps(t, key)
	if at == nil || !at.Equal(old) {
		t.Fatalf("RefreshedAt = %v after a --cached call, want unchanged %v (INV-FRESH-2)", at, old)
	}
	if lastErr != nil {
		t.Fatalf("a --cached call wrote LastError = %+v", lastErr)
	}
}

func TestRun_PrChanges_CachedOnFreshLedger_LeavesRefreshedAtUnknown(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	marker := filepath.Join(t.TempDir(), "invoked.log")
	writeMarkerBackend(t, "backend-fresh-cached-new", marker)
	writeConfigFor(t, "backend-fresh-cached-new")
	key := LedgerKey{Type: "pr", Backend: "backend-fresh-cached-new", Query: "mine"}
	seedLedger(t, key, &Ledger{Entries: map[string]LedgerEntry{}, Consumers: map[string]ConsumerState{}})

	if _, _, code := executePr(t, []string{"pr", "changes", "--query", "mine", "--consumer", "c1", "--cached"}); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if at, _ := loadStamps(t, key); at != nil {
		t.Fatalf("RefreshedAt = %v, want nil (no recorded success, INV-FRESH-4)", at)
	}
}

// INV-FRESH-3: a truncated answer is not a success.
func TestRun_PrChanges_Truncated_DoesNotStamp(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	writeOpAwareFakeBackend(t, "backend-fresh-trunc", map[string]string{
		"list": `{"protocolVersion":1,"schemaVersion":1,"result":{"entities":[],"present_ids":[],"cursor":null,"truncated":true}}`,
	}, `{}`)
	writeConfigFor(t, "backend-fresh-trunc")
	key := LedgerKey{Type: "pr", Backend: "backend-fresh-trunc", Query: "mine"}

	if _, _, code := executePr(t, []string{"pr", "changes", "--query", "mine", "--consumer", "c1"}); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	at, lastErr := loadStamps(t, key)
	if at != nil {
		t.Fatalf("RefreshedAt = %v after a truncated answer, want nil (INV-FRESH-3)", at)
	}
	if lastErr == nil || lastErr.Code != ledgerErrorTruncated {
		t.Fatalf("LastError = %+v, want code %q", lastErr, ledgerErrorTruncated)
	}
}

func TestRun_PrChanges_BackendFailure_RecordsLastErrorAndKeepsPriorSuccess(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	writeOpAwareFakeBackend(t, "backend-fresh-down", map[string]string{
		"list": `{"protocolVersion":1,"schemaVersion":1,"error":{"code":"unavailable","message":"down"}}`,
	}, `{}`)
	writeConfigFor(t, "backend-fresh-down")
	key := LedgerKey{Type: "pr", Backend: "backend-fresh-down", Query: "mine"}

	prior := freshnessNow.Add(-time.Hour)
	seedLedger(t, key, &Ledger{
		Entries:     map[string]LedgerEntry{"o/r#1": {Hash: "h1", VersionLastChanged: 1}},
		Version:     1,
		Consumers:   map[string]ConsumerState{},
		RefreshedAt: &prior,
	})

	before := time.Now().Add(-time.Second)
	if _, _, code := executePr(t, []string{"pr", "changes", "--query", "mine", "--consumer", "c1"}); code == 0 {
		t.Fatalf("exit code = 0, want non-zero for a failed backend")
	}
	at, lastErr := loadStamps(t, key)
	if at == nil || !at.Equal(prior) {
		t.Fatalf("RefreshedAt = %v after a failed fetch, want unchanged %v", at, prior)
	}
	if lastErr == nil || lastErr.Code != "unavailable" || lastErr.At.Before(before) {
		t.Fatalf("LastError = %+v, want {code=unavailable, at>=%v}", lastErr, before)
	}
	// The failure must not disturb the index or version.
	l, err := loadLedger(key)
	if err != nil {
		t.Fatal(err)
	}
	if l.Version != 1 || len(l.Entries) != 1 {
		t.Fatalf("failed fetch disturbed the ledger: version=%d entries=%d", l.Version, len(l.Entries))
	}
}

func TestRun_PrChanges_FirstFetchFails_RecordsLastErrorWithNoSuccess(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	writeOpAwareFakeBackend(t, "backend-fresh-never", map[string]string{
		"list": `{"protocolVersion":1,"schemaVersion":1,"error":{"code":"unauthenticated","message":"no token"}}`,
	}, `{}`)
	writeConfigFor(t, "backend-fresh-never")
	key := LedgerKey{Type: "pr", Backend: "backend-fresh-never", Query: "mine"}

	if _, _, code := executePr(t, []string{"pr", "changes", "--query", "mine", "--consumer", "c1"}); code == 0 {
		t.Fatalf("exit code = 0, want non-zero")
	}
	at, lastErr := loadStamps(t, key)
	if at != nil {
		t.Fatalf("RefreshedAt = %v, want nil (never succeeded)", at)
	}
	if lastErr == nil || lastErr.Code != "unauthenticated" {
		t.Fatalf("LastError = %+v, want code unauthenticated", lastErr)
	}
}

func TestRun_LedgerShow_ExposesRefreshedAtAndLastError(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	okKey := LedgerKey{Type: "pr", Backend: "b", Query: "mine"}
	badKey := LedgerKey{Type: "pr", Backend: "b", Query: "team"}
	okAt := freshnessNow
	errAt := freshnessNow.Add(time.Minute)
	seedLedger(t, okKey, &Ledger{Entries: map[string]LedgerEntry{}, Consumers: map[string]ConsumerState{}, RefreshedAt: &okAt})
	seedLedger(t, badKey, &Ledger{Entries: map[string]LedgerEntry{}, Consumers: map[string]ConsumerState{}, LastError: &LedgerError{At: errAt, Code: "unavailable"}})

	stdout, _, code := executePr(t, []string{"ledger", "show"})
	if code != 0 {
		t.Fatalf("exit code = %d; stdout=%s", code, stdout)
	}
	byQuery := map[string]ledgerShowRow{}
	for _, r := range decodeLedgerShowRows(t, stdout) {
		byQuery[r.Query] = r
	}
	if r := byQuery["mine"]; r.RefreshedAt == nil || !r.RefreshedAt.Equal(okAt) || r.LastError != nil {
		t.Fatalf("mine row = %+v, want refreshed_at=%v and no last_error", r, okAt)
	}
	if r := byQuery["team"]; r.RefreshedAt != nil || r.LastError == nil || r.LastError.Code != "unavailable" || !r.LastError.At.Equal(errAt) {
		t.Fatalf("team row = %+v, want refreshed_at null and last_error {unavailable, %v}", r, errAt)
	}

	// Raw JSON: refreshed_at is present as null (never omitted) so a
	// consumer can tell "unknown" from "field missing".
	if !contains(stdout, `"refreshed_at": null`) && !contains(stdout, `"refreshed_at":null`) {
		t.Fatalf("ledger show JSON lacks an explicit refreshed_at null for a never-succeeded key: %s", stdout)
	}
}

func TestRun_LedgerShow_HumanOutput_ShowsFreshness(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	at := freshnessNow
	seedLedger(t, LedgerKey{Type: "pr", Backend: "b", Query: "mine"}, &Ledger{
		Entries: map[string]LedgerEntry{}, Consumers: map[string]ConsumerState{},
		RefreshedAt: &at, LastError: &LedgerError{At: at.Add(time.Minute), Code: "unavailable"},
	})
	stdout, _, code := executePr(t, []string{"--output", "human", "ledger", "show"})
	if code != 0 {
		t.Fatalf("exit code = %d; stdout=%s", code, stdout)
	}
	for _, want := range []string{"refreshed_at=2026-10-06T12:00:00Z", "last_error=unavailable@2026-10-06T12:01:00Z"} {
		if !contains(stdout, want) {
			t.Fatalf("human output missing %q:\n%s", want, stdout)
		}
	}
}
