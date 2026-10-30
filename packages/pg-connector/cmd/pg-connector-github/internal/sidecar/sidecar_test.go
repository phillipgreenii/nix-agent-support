package sidecar

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/posted"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, _, err := store.Open(filepath.Join(t.TempDir(), "store.sqlite"), store.Options{Synchronous: "OFF"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func openSidecar(t *testing.T) (*Store, *store.Store) {
	t.Helper()
	st := openStore(t)
	s, err := Open(st)
	if err != nil {
		t.Fatal(err)
	}
	return s, st
}

func TestOpenRefusesANilStore(t *testing.T) {
	if s, err := Open(nil); err == nil || s != nil {
		t.Fatalf("Open(nil) = %v, %v; want an error", s, err)
	}
}

func TestLoadOfAMissingRecordIsEmpty(t *testing.T) {
	s, _ := openSidecar(t)
	st, err := s.Load(context.Background(), "foo", "bar", 1)
	if err != nil || len(st.Fingerprints) != 0 || st.LastAppend != nil || st.Version != posted.StateVersion {
		t.Fatalf("got %+v %v, want an empty state", st, err)
	}
}

func TestSaveThenLoadRoundTrips(t *testing.T) {
	ctx := context.Background()
	s, _ := openSidecar(t)
	want := posted.State{
		Fingerprints: []string{"0123456789abcdef"},
		BodyHeads:    []string{"abc"},
		LastAppend:   &posted.LastAppend{At: "2026-10-30T00:00:00Z", Added: 1, Head: "abc"},
	}
	if err := s.Save(ctx, "foo", "bar", 7, want); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load(ctx, "foo", "bar", 7)
	if err != nil {
		t.Fatal(err)
	}
	want.Version = posted.StateVersion
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	// Another PR of the same repo stays empty.
	if other, err := s.Load(ctx, "foo", "bar", 8); err != nil || len(other.Fingerprints) != 0 {
		t.Fatalf("other PR = %+v %v", other, err)
	}
}

func TestSaveReplacesTheRecordAndStampsUpdatedAt(t *testing.T) {
	ctx := context.Background()
	s, st := openSidecar(t)
	s.now = func() time.Time { return time.UnixMilli(1760000000000) }
	if err := s.Save(ctx, "foo", "bar", 1, posted.State{Fingerprints: []string{"aaaaaaaaaaaaaaaa"}}); err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return time.UnixMilli(1760000001000) }
	if err := s.Save(ctx, "foo", "bar", 1, posted.State{Fingerprints: []string{"bbbbbbbbbbbbbbbb"}}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load(ctx, "foo", "bar", 1)
	if err != nil || !reflect.DeepEqual(got.Fingerprints, []string{"bbbbbbbbbbbbbbbb"}) {
		t.Fatalf("got %+v %v, want the second save", got, err)
	}
	var n, at int64
	if err := st.DB().QueryRow(`SELECT count(*), max(updated_at) FROM posted_review`).Scan(&n, &at); err != nil || n != 1 || at != 1760000001000 {
		t.Fatalf("rows = %d updated_at = %d err = %v", n, at, err)
	}
}

func TestCorruptRowWrapsErrCorruptAndStays(t *testing.T) {
	ctx := context.Background()
	s, st := openSidecar(t)
	if _, err := st.DB().Exec(
		`INSERT INTO posted_review (owner, repo, number, state, updated_at) VALUES ('foo', 'bar', 1, '{not json', 1)`,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load(ctx, "foo", "bar", 1); !errors.Is(err, posted.ErrCorrupt) {
		t.Fatalf("err = %v, want ErrCorrupt", err)
	}
	var text string
	if err := st.DB().QueryRow(`SELECT state FROM posted_review WHERE owner='foo' AND repo='bar' AND number=1`).Scan(&text); err != nil || text != "{not json" {
		t.Fatalf("the corrupt row must stay: %q %v", text, err)
	}
	// The operator's repost lever is a raw DELETE; the record then reads empty.
	if _, err := st.DB().Exec(`DELETE FROM posted_review WHERE owner = ? AND repo = ? AND number = ?`, "foo", "bar", 1); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Load(ctx, "foo", "bar", 1); err != nil || len(got.Fingerprints) != 0 {
		t.Fatalf("after delete: %+v %v", got, err)
	}
}

func TestUnsafeKeysAreRefused(t *testing.T) {
	ctx := context.Background()
	s, _ := openSidecar(t)
	if _, err := s.Load(ctx, "", "bar", 1); err == nil {
		t.Error("empty owner accepted")
	}
	if _, err := s.Load(ctx, "foo", "", 1); err == nil {
		t.Error("empty repo accepted")
	}
	if _, err := s.Load(ctx, "foo", "bar", 0); err == nil {
		t.Error("non-positive PR accepted by Load")
	}
	if err := s.Save(ctx, "foo", "bar", -1, posted.State{}); err == nil {
		t.Error("non-positive PR accepted by Save")
	}
}

func TestSaveOnAClosedStoreIsAnError(t *testing.T) {
	s, st := openSidecar(t)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(context.Background(), "foo", "bar", 1, posted.State{}); err == nil {
		t.Fatal("a failed write must be an error")
	}
}
