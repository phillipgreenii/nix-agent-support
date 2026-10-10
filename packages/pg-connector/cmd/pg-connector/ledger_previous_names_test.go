package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// Legacy-name ledger adoption (bead pg2-ik9ew): renaming a backend changes
// the ledger filename, so loadLedgerAdopting falls back to the file of a
// declared previous name (same type, query and hex(instance)) and copies it
// under the flock when the new-name file is absent.

const (
	adoptOldName = "pg-connector-issue-beads"
	adoptNewName = "pg-connector-issue-beads-zr"
)

func adoptKeys(instance string) (legacy, current LedgerKey) {
	return LedgerKey{Type: "issue", Backend: adoptOldName, Query: "work-beads", Instance: instance},
		LedgerKey{Type: "issue", Backend: adoptNewName, Query: "work-beads", Instance: instance}
}

func legacyFixtureLedger(version int64) *Ledger {
	return &Ledger{
		Cursor: json.RawMessage(`"cur"`),
		Entries: map[string]LedgerEntry{
			"zr-1": {Hash: "h1", VersionLastChanged: 2},
			"zr-2": {Hash: "h2", VersionLastChanged: version},
		},
		Version:   version,
		Consumers: map[string]ConsumerState{"pg-router": {Cursor: version, LastSeen: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)}},
	}
}

func readLedgerBytes(t *testing.T, key LedgerKey) []byte {
	t.Helper()
	path, err := ledgerPath(key)
	if err != nil {
		t.Fatalf("ledgerPath: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}

func ledgerFileExists(t *testing.T, key LedgerKey) bool {
	t.Helper()
	path, err := ledgerPath(key)
	if err != nil {
		t.Fatalf("ledgerPath: %v", err)
	}
	_, err = os.Stat(path)
	if err == nil {
		return true
	}
	if errors.Is(err, fs.ErrNotExist) {
		return false
	}
	t.Fatalf("stat %s: %v", path, err)
	return false
}

func TestLoadLedgerAdopting_SeedsMissingNewNameFromPreviousName(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	legacy, current := adoptKeys("/zr-tracker")
	seedLedger(t, legacy, legacyFixtureLedger(171))

	got, err := loadLedgerAdopting(current, []string{adoptOldName})
	if err != nil {
		t.Fatalf("loadLedgerAdopting: %v", err)
	}
	if got.Version != 171 || len(got.Entries) != 2 || got.Consumers["pg-router"].Cursor != 171 || string(got.Cursor) != `"cur"` {
		t.Fatalf("adopted ledger = %+v, want the legacy ledger's state (version 171, 2 entries, consumer cursor 171)", got)
	}
	if !ledgerFileExists(t, current) {
		t.Fatal("the new-name file was not created by adoption")
	}
	if !bytes.Equal(readLedgerBytes(t, legacy), readLedgerBytes(t, current)) {
		t.Error("the new-name file is not a byte-for-byte copy of the legacy file")
	}
	if !ledgerFileExists(t, legacy) {
		t.Error("the legacy file was removed; adoption must COPY, never move")
	}
}

func TestLoadLedgerAdopting_ExistingNewNameIsNeverOverwritten(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	legacy, current := adoptKeys("/zr-tracker")
	seedLedger(t, legacy, legacyFixtureLedger(171))
	seedLedger(t, current, legacyFixtureLedger(5))
	before := readLedgerBytes(t, current)
	legacyBefore := readLedgerBytes(t, legacy)

	got, err := loadLedgerAdopting(current, []string{adoptOldName})
	if err != nil {
		t.Fatalf("loadLedgerAdopting: %v", err)
	}
	if got.Version != 5 {
		t.Fatalf("Version = %d, want 5 (the existing new-name ledger), legacy ignored", got.Version)
	}
	if !bytes.Equal(before, readLedgerBytes(t, current)) {
		t.Error("the existing new-name file changed")
	}
	if !bytes.Equal(legacyBefore, readLedgerBytes(t, legacy)) {
		t.Error("the legacy file changed")
	}
}

func TestLoadLedgerAdopting_DifferentInstanceDiscriminatorIsNotAdopted(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	// The old backend served BOTH trackers, so its ledger exists once per
	// tracker (hex(beads-dir) suffix). Only the matching one may be adopted.
	legacyPg2, _ := adoptKeys("/pg2-tracker")
	seedLedger(t, legacyPg2, legacyFixtureLedger(171))
	_, currentZr := adoptKeys("/zr-tracker")

	got, err := loadLedgerAdopting(currentZr, []string{adoptOldName})
	if err != nil {
		t.Fatalf("loadLedgerAdopting: %v", err)
	}
	if got.Version != 0 || len(got.Entries) != 0 {
		t.Fatalf("ledger = %+v, want a fresh empty ledger: the legacy file carries a different instance discriminator", got)
	}
	if ledgerFileExists(t, currentZr) {
		t.Error("a new-name file was created from a legacy file with a different instance discriminator")
	}

	// And with the empty discriminator against a legacy file that has one.
	_, currentNoInstance := adoptKeys("")
	if got, err := loadLedgerAdopting(currentNoInstance, []string{adoptOldName}); err != nil || got.Version != 0 {
		t.Fatalf("empty-instance load = %+v, %v; want empty", got, err)
	}
}

func TestLoadLedgerAdopting_DifferentQueryOrTypeIsNotAdopted(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	legacy, current := adoptKeys("/zr-tracker")
	otherQuery := legacy
	otherQuery.Query = "escalated-work"
	otherType := legacy
	otherType.Type = "pr"
	seedLedger(t, otherQuery, legacyFixtureLedger(9))
	seedLedger(t, otherType, legacyFixtureLedger(9))

	got, err := loadLedgerAdopting(current, []string{adoptOldName})
	if err != nil {
		t.Fatalf("loadLedgerAdopting: %v", err)
	}
	if got.Version != 0 || ledgerFileExists(t, current) {
		t.Fatalf("ledger = %+v (file exists = %v), want nothing adopted from another query or type", got, ledgerFileExists(t, current))
	}
}

func TestLoadLedgerAdopting_IsIdempotentAndLaterWritesWin(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	legacy, current := adoptKeys("/zr-tracker")
	seedLedger(t, legacy, legacyFixtureLedger(171))

	first, err := loadLedgerAdopting(current, []string{adoptOldName})
	if err != nil {
		t.Fatal(err)
	}
	adoptedBytes := readLedgerBytes(t, current)

	second, err := loadLedgerAdopting(current, []string{adoptOldName})
	if err != nil {
		t.Fatal(err)
	}
	if first.Version != second.Version || !bytes.Equal(adoptedBytes, readLedgerBytes(t, current)) {
		t.Fatal("a second load changed the adopted ledger")
	}

	// The first run after the rename advances the new-name ledger; a later
	// load must see that, never re-adopt the legacy snapshot over it.
	second.Version = 200
	if err := saveLedger(current, second); err != nil {
		t.Fatal(err)
	}
	third, err := loadLedgerAdopting(current, []string{adoptOldName})
	if err != nil {
		t.Fatal(err)
	}
	if third.Version != 200 {
		t.Fatalf("Version = %d, want 200: the legacy snapshot was re-adopted over a newer new-name ledger", third.Version)
	}
}

func TestLoadLedgerAdopting_NoPreviousNamesBehavesLikeLoadLedger(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	legacy, current := adoptKeys("/zr-tracker")
	seedLedger(t, legacy, legacyFixtureLedger(171))
	for _, previous := range [][]string{nil, {}} {
		got, err := loadLedgerAdopting(current, previous)
		if err != nil || got.Version != 0 || ledgerFileExists(t, current) {
			t.Fatalf("previous=%v: ledger = %+v, %v; want empty and no file", previous, got, err)
		}
	}
	if got, err := loadLedger(current); err != nil || got.Version != 0 {
		t.Fatalf("loadLedger = %+v, %v; want empty", got, err)
	}
}

func TestLoadLedgerAdopting_FirstExistingPreviousNameWins(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	_, current := adoptKeys("/zr-tracker")
	older := LedgerKey{Type: "issue", Backend: "older-beads", Query: "work-beads", Instance: "/zr-tracker"}
	old := LedgerKey{Type: "issue", Backend: "old-beads", Query: "work-beads", Instance: "/zr-tracker"}
	seedLedger(t, older, legacyFixtureLedger(3))
	seedLedger(t, old, legacyFixtureLedger(7))

	// "missing-beads" has no file and is skipped; "old-beads" precedes "older-beads".
	got, err := loadLedgerAdopting(current, []string{"missing-beads", "old-beads", "older-beads"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != 7 {
		t.Fatalf("Version = %d, want 7 (old-beads, the first previous name with a file)", got.Version)
	}
}

func TestLoadLedgerAdopting_UndecodableLegacyFileIsSkipped(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	legacy, current := adoptKeys("/zr-tracker")
	seedLedger(t, legacy, legacyFixtureLedger(171))
	path, err := ledgerPath(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := loadLedgerAdopting(current, []string{adoptOldName})
	if err != nil {
		t.Fatalf("an undecodable legacy file must not fail the new name's load: %v", err)
	}
	if got.Version != 0 || ledgerFileExists(t, current) {
		t.Fatalf("ledger = %+v, file exists = %v; want nothing adopted from a corrupt legacy file", got, ledgerFileExists(t, current))
	}
}

// Many first loads racing on the same absent new-name file: every one sees
// the legacy state, the adopted file is the legacy bytes, and a writer that
// advances the new-name ledger after adopting is never clobbered by a late
// adopter (the copy happens only under the flock and only when the new-name
// file is absent). Run under -race.
func TestLoadLedgerAdopting_ConcurrentFirstLoadsAdoptOnceAndNeverClobberLaterWrites(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	legacy, current := adoptKeys("/zr-tracker")
	seedLedger(t, legacy, legacyFixtureLedger(171))
	legacyBytes := readLedgerBytes(t, legacy)

	const loaders = 16
	var wg sync.WaitGroup
	start := make(chan struct{})
	versions := make([]int64, loaders)
	errs := make([]error, loaders)
	for i := 0; i < loaders; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			l, err := loadLedgerAdopting(current, []string{adoptOldName})
			if err != nil {
				errs[i] = err
				return
			}
			versions[i] = l.Version
			if i == 0 {
				// One of them is the first changes run after the rename.
				l.Version = 999
				errs[i] = saveLedger(current, l)
			}
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("loader %d: %v", i, err)
		}
	}
	for i, v := range versions {
		if v != 171 && v != 999 {
			t.Errorf("loader %d saw version %d, want the legacy 171 or the advanced 999 (never empty or torn)", i, v)
		}
	}
	final, err := loadLedgerAdopting(current, []string{adoptOldName})
	if err != nil {
		t.Fatal(err)
	}
	if final.Version != 999 {
		t.Fatalf("final Version = %d, want 999: a late adopter clobbered the advanced new-name ledger", final.Version)
	}
	if !bytes.Equal(legacyBytes, readLedgerBytes(t, legacy)) {
		t.Error("the legacy file changed")
	}
	entries, err := os.ReadDir(filepath.Dir(mustLedgerPath(t, current)))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Errorf("leftover temp file %s", e.Name())
		}
	}
}

func mustLedgerPath(t *testing.T, key LedgerKey) string {
	t.Helper()
	path, err := ledgerPath(key)
	if err != nil {
		t.Fatal(err)
	}
	return path
}

// The acceptance scenario end to end: a backend renamed in the registry
// (old plain name -> instance with previous_names) must not re-emit its live
// entries as added on its first changes run; without previous_names it does.
func TestRun_IssueChanges_RenamedBackendAdoptsLegacyLedger_NoAddedBurst(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv(envIssueBeadsDir, "/zr-tracker")
	const binary = "backend-issue-rename"
	writeOpAwareFakeBackend(t, binary, map[string]string{
		"list": `{"protocolVersion":1,"schemaVersion":1,"result":{"entities":[{"id":"zr-1","title":"t","state":"open"},{"id":"zr-2","title":"t2","state":"open"}],"present_ids":["zr-1","zr-2"],"cursor":null,"truncated":false}}`,
	}, `{}`)
	writeRegistryConfig := func(body string) {
		cfg := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(cfg, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PG_PR_CONFIG", cfg)
	}
	run := func() changesWire {
		t.Helper()
		stdout, _, code := executePr(t, []string{"issue", "changes", "--query", "work-beads", "--consumer", "pg-router"})
		if code != 0 {
			t.Fatalf("issue changes: exit %d; stdout=%s", code, stdout)
		}
		return decodeChangesWire(t, stdout)
	}

	// Before the rename: the plain-named backend establishes its ledger.
	writeRegistryConfig("connector:\n  issue:\n    - " + binary + "\n")
	if w := run(); changeKindsFor(w.Changes)["added"] != 2 {
		t.Fatalf("pre-rename first run: Changes = %+v, want 2 added", w.Changes)
	}
	if w := run(); len(w.Changes) != 0 {
		t.Fatalf("pre-rename second run: Changes = %+v, want none", w.Changes)
	}

	// Control: renamed WITHOUT previous_names, the ledger is orphaned and
	// every live entry re-emits as added (the burst this bead removes).
	renamed := "connector:\n  issue:\n    - {name: " + binary + "-control, command: [" + binary + "]}\n"
	writeRegistryConfig(renamed)
	if w := run(); changeKindsFor(w.Changes)["added"] != 2 {
		t.Fatalf("control (no previous_names): Changes = %+v, want the 2-entry added burst", w.Changes)
	}

	// Renamed WITH previous_names: the legacy ledger is adopted, no burst.
	writeRegistryConfig("connector:\n  issue:\n    - {name: " + binary + "-zr, command: [" + binary + "], previous_names: [" + binary + "]}\n")
	if w := run(); len(w.Changes) != 0 {
		t.Fatalf("renamed with previous_names: Changes = %+v, want NO added burst", w.Changes)
	}
	if w := run(); len(w.Changes) != 0 {
		t.Fatalf("renamed with previous_names, second run: Changes = %+v, want none", w.Changes)
	}
}
