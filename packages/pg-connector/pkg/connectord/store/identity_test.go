package store

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"testing"
)

var identityShape = regexp.MustCompile(`^[0-9a-f]{32}$`)

func mustIdentity(t *testing.T, s *Store) string {
	t.Helper()
	id, err := s.Identity(context.Background())
	if err != nil {
		t.Fatalf("Identity: %v", err)
	}
	if !identityShape.MatchString(id) {
		t.Fatalf("identity %q is not 32 lowercase hex characters", id)
	}
	return id
}

func TestIdentityIsCreatedWithANewStoreAndIsStableAcrossReopens(t *testing.T) {
	path := dbPath(t)
	s, _ := mustOpen(t, path, fastOpts())
	id := mustIdentity(t, s)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2, res := mustOpen(t, path, fastOpts())
	if res.Outcome != OutcomeOpened {
		t.Fatalf("result = %+v, want opened", res)
	}
	if got := mustIdentity(t, s2); got != id {
		t.Errorf("identity after reopen = %s, want %s", got, id)
	}
}

func TestIdentityDiffersBetweenTwoStores(t *testing.T) {
	a, _ := mustOpen(t, dbPath(t), fastOpts())
	b, _ := mustOpen(t, dbPath(t), fastOpts())
	if mustIdentity(t, a) == mustIdentity(t, b) {
		t.Error("two stores share one identity")
	}
}

func TestIdentityIsCreatedForAPopulatedV3StoreAtItsFirstOpen(t *testing.T) {
	fx, ok := fixtures(t)[3]
	if !ok {
		t.Fatal("no version 3 fixture")
	}
	path := dbPath(t)
	copyFile(t, fx, path)

	// Before the rung the fixture has no identity: opened with the v3 ladder
	// the row is absent and Identity says so.
	old, _ := mustOpen(t, path, Options{Synchronous: "OFF", Migrations: BuiltinMigrations()[:3]})
	if _, err := old.Identity(context.Background()); !errors.Is(err, ErrNoIdentity) {
		t.Fatalf("Identity of a v3 store = %v, want ErrNoIdentity", err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}

	s, res := mustOpen(t, path, fastOpts())
	if res.Outcome != OutcomeMigrated || res.FromVersion != 3 {
		t.Fatalf("result = %+v, want migrated from 3", res)
	}
	id := mustIdentity(t, s)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2, _ := mustOpen(t, path, fastOpts())
	if got := mustIdentity(t, s2); got != id {
		t.Errorf("identity after reopen = %s, want %s", got, id)
	}
}

func TestIdentityErrorsWhenTheRowIsAbsent(t *testing.T) {
	s, _ := mustOpen(t, dbPath(t), fastOpts())
	if _, err := s.DB().Exec(`DELETE FROM store_meta WHERE key = ?`, StoreIDKey); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Identity(context.Background()); !errors.Is(err, ErrNoIdentity) {
		t.Errorf("Identity without a row = %v, want ErrNoIdentity", err)
	}
}

func TestRungFourKeepsAnExistingIdentityAndAddsNoDDL(t *testing.T) {
	// A store that already holds a store_id (a copy written by a later binary
	// and replayed through the rung) keeps it: the rung is INSERT OR IGNORE.
	s, _ := mustOpen(t, dbPath(t), fastOpts())
	id := mustIdentity(t, s)
	tx, err := s.DB().Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := migrateV4(tx, func(string) {}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if got := mustIdentity(t, s); got != id {
		t.Errorf("rung 4 replaced the identity: %s, want %s", got, id)
	}
}

func TestAMovedAsideStoreGetsANewIdentity(t *testing.T) {
	path := dbPath(t)
	first, _ := mustOpen(t, path, fastOpts())
	oldID := mustIdentity(t, first)
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	failures := 0
	ladder := BuiltinMigrations()
	ladder = append(ladder, Migration{
		Version: len(ladder) + 1, Name: "fails once",
		Apply: func(*sql.Tx, func(string)) error {
			if failures == 0 {
				failures++
				return errors.New("boom")
			}
			return nil
		},
	})
	opts := fastOpts()
	opts.Migrations = ladder
	s, res := mustOpen(t, path, opts)
	if res.Outcome != OutcomeMovedAside {
		t.Fatalf("result = %+v, want moved aside", res)
	}
	if got := mustIdentity(t, s); got == oldID {
		t.Errorf("the fresh store kept the moved-aside identity %s", got)
	}

	// The moved-aside copy still carries the old one.
	old, err := sql.Open("sqlite", "file:"+res.MovedAsideTo+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = old.Close() }()
	var got string
	if err := old.QueryRow(`SELECT value FROM store_meta WHERE key = ?`, StoreIDKey).Scan(&got); err != nil || got != oldID {
		t.Errorf("moved-aside identity = %q err=%v, want %q", got, err, oldID)
	}
}
