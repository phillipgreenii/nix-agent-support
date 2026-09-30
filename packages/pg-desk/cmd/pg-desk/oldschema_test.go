package main

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite" // same driver internal/store registers

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/sync"
)

// storeAtVersion builds a store file in a temp dir at the requested state
// and returns its path: "old" (schema 1, seeded), "new" (schema 2, seeded)
// or "empty" (no schema at all).
func storeAtVersion(t *testing.T, kind string) string {
	t.Helper()
	store.SetSynchronousForTests("OFF")
	path := filepath.Join(t.TempDir(), "store.db")
	if kind == "empty" {
		s, err := store.OpenRaw(path)
		if err != nil {
			t.Fatalf("OpenRaw: %v", err)
		}
		if err := s.Close(); err != nil {
			t.Fatalf("close: %v", err)
		}
		return path
	}
	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := s.UpsertEntity(store.Entity{Repo: "o/r", EntityType: entityTypePR, EntityID: "o/r#1", Facts: `{}`, AsOf: "2026-09-16T00:00:00Z"}); err != nil {
		t.Fatalf("seed entity: %v", err)
	}
	if err := s.UpsertInterpretation(store.Interpretation{Repo: "o/r", EntityType: entityTypePR, EntityID: "o/r#1", Degraded: true, AsOf: "2026-09-16T00:00:00Z"}); err != nil {
		t.Fatalf("seed interpretation: %v", err)
	}
	if kind == "new" {
		if err := s.Cutover(); err != nil {
			t.Fatalf("Cutover: %v", err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return path
}

// withRawStoreAt points the RAW store seam at path and makes the normal
// (migrating, version-gated) seam fail the test if anything calls it: the
// commands under test must reach the store only through the raw seam.
func withRawStoreAt(t *testing.T, path string) {
	t.Helper()
	// sync.mode "plan" is the config that makes status read the ledger, the
	// riskiest thing to do to a store without one.
	cfg := openTestConfig("o/r")
	cfg.Sync.Mode = sync.ModePlan
	withOpenSeams(t, cfg, func() (*store.Store, error) { return store.OpenRaw(path) })
	deskStoreOpen = func() (*store.Store, error) {
		t.Errorf("deskStoreOpen (Open: migrates and version-gates) was called; this command must open through deskStoreOpenRaw")
		return nil, errors.New("deskStoreOpen must not be used here")
	}
	deskStoreOpenRaw = func() (*store.Store, error) { return store.OpenRaw(path) }
}

// TestStatusAndDoctorDoNotCrashOnOldSchemaStore proves status and doctor
// open the store through the raw seam and report on an old-schema (1), a
// cut-over (2) and an uninitialized store rather than crash or fail. What
// doctor should REPORT about an unmigrated store is a later phase's; this
// only pins that they survive it.
func TestStatusAndDoctorDoNotCrashOnOldSchemaStore(t *testing.T) {
	for _, tc := range []struct {
		kind        string
		wantVersion string
		wantEntity  string
	}{
		{"old", "schema_version: 1", "entities: 1"},
		{"new", "schema_version: 2", "entities: 1"},
		{"empty", "schema_version: -", "entities: 0"},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			withRawStoreAt(t, storeAtVersion(t, tc.kind))

			origLedger := statusRunPgConnectorLedgerShow
			t.Cleanup(func() { statusRunPgConnectorLedgerShow = origLedger })
			statusRunPgConnectorLedgerShow = func(ctx context.Context) (string, error) { return "(empty)\n", nil }

			stdout, err := runStatusCmd(t)
			if err != nil {
				t.Fatalf("status on a %s store: %v", tc.kind, err)
			}
			if !strings.Contains(stdout, tc.wantVersion) {
				t.Errorf("status output missing %q:\n%s", tc.wantVersion, stdout)
			}
			if !strings.Contains(stdout, tc.wantEntity) {
				t.Errorf("status output missing %q:\n%s", tc.wantEntity, stdout)
			}

			stubDoctorSeams(t, nil, nil, nil)
			out, err := runDoctorCmd(t)
			if err != nil {
				t.Fatalf("doctor on a %s store: %v\n%s", tc.kind, err, out)
			}
			for _, want := range []string{"sync_error rows: 0", "stranded cycles: 0"} {
				if !strings.Contains(out, want) {
					t.Errorf("doctor output missing %q:\n%s", want, out)
				}
			}
		})
	}
}

// TestDefaultRawStoreSeamOpensWithoutTheVersionGate pins the production
// wiring of the raw seam: with the seams left at their defaults, a store
// newer than this binary knows (which Open refuses) still opens through
// deskStoreOpenRaw.
func TestDefaultRawStoreSeamOpensWithoutTheVersionGate(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_STATE_HOME", xdg)
	path := store.DefaultPath()
	s, err := store.OpenRaw(path)
	if err != nil {
		t.Fatalf("OpenRaw: %v", err)
	}
	_ = s.Close()

	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	if _, err := raw.Exec("PRAGMA user_version = 3"); err != nil {
		t.Fatalf("set user_version: %v", err)
	}
	_ = raw.Close()

	if st, err := deskStoreOpen(); err == nil {
		_ = st.Close()
		t.Fatalf("deskStoreOpen accepted a version-3 store; it must keep the version gate")
	}
	st, err := deskStoreOpenRaw()
	if err != nil {
		t.Fatalf("deskStoreOpenRaw refused a version-3 store: %v", err)
	}
	defer func() { _ = st.Close() }()
	if v, err := st.SchemaVersion(); err != nil || v != 3 {
		t.Fatalf("SchemaVersion = %d, %v; want 3", v, err)
	}
}
