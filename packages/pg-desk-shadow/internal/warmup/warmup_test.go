package warmup

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-desk-shadow/internal/testutil"
)

const listing1 = `{"entities":[{"id":"acme/api#1","title":"t","stale":false},{"id":"acme/api#2","title":"t","stale":true},{"id":"acme/api#3","title":"t","stale":false}],
"sources":[{"source":"pg-connector-pr-github","status":"succeeded"}],"truncated":false,
"fingerprints":{"acme/api#1":"fp1-mine","acme/api#2":"fp2","acme/api#3":"fp3"}}`

const listing2 = `{"entities":[{"id":"acme/api#1","stale":false},{"id":"acme/api#4","stale":false},{"id":"acme/api#2","stale":false}],
"fingerprints":{"acme/api#1":"fp1-team","acme/api#4":"fp4","acme/api#2":"fp2-team"}}`

func TestParseListingAndPrime(t *testing.T) {
	l1, err := ParseListing("mine", []byte(listing1))
	if err != nil {
		t.Fatal(err)
	}
	l2, err := ParseListing("team", []byte(listing2))
	if err != nil {
		t.Fatal(err)
	}
	p := Prime([]Listing{l1, l2})
	if p.FP["acme/api#1"] != "fp1-mine" {
		t.Errorf("an id in both queries must take the FIRST query's fingerprint, got %q", p.FP["acme/api#1"])
	}
	if p.FP["acme/api#2"] != "fp2-team" {
		t.Errorf("an id stale in the first query and fresh in the second takes the fresh one, got %q", p.FP["acme/api#2"])
	}
	if _, ok := p.FP["acme/api#4"]; !ok || len(p.FP) != 4 {
		t.Errorf("primed set wrong: %v", p.FP)
	}
	// An id stale everywhere is skipped but still listed.
	only, _ := ParseListing("q", []byte(`{"entities":[{"id":"acme/api#9","stale":true}],"fingerprints":{"acme/api#9":"x"}}`))
	pp := Prime([]Listing{only})
	if _, ok := pp.FP["acme/api#9"]; ok || !pp.Listed["acme/api#9"] || len(pp.Stale) != 1 {
		t.Errorf("stale-only id: %+v", pp)
	}
	if _, err := ParseListing("q", []byte(`{"entities":[{"id":""}]}`)); err == nil {
		t.Error("an entity with no id must be refused")
	}
	if _, err := ParseListing("q", []byte(`not json`)); err == nil {
		t.Error("garbage must be refused")
	}
}

func TestSeedSQLRefusals(t *testing.T) {
	if _, err := SeedSQL(Prime(nil), time.Now()); err == nil {
		t.Error("an empty listing must refuse to seed")
	}
	bad := Primed{FP: map[string]string{"x'; DROP TABLE entity;--": "a"}, Listed: map[string]bool{"x'; DROP TABLE entity;--": true}}
	if _, err := SeedSQL(bad, time.Now()); err == nil {
		t.Error("an id of unexpected shape must be refused")
	}
}

func TestSeedOnFakeStore(t *testing.T) {
	db := testutil.FakeStore(t, t.TempDir()+"/store.db")
	ctx := context.Background()
	// Version-0, never-hydrated rows as a cutover leaves them: 1,3 listed; 5,6 absent; 2 stale.
	var sql strings.Builder
	for i := 1; i <= 6; i++ {
		fmt.Fprintf(&sql, "INSERT INTO entity (repo, entity_type, entity_id, facts, as_of) VALUES ('acme/api','pr','acme/api#%d','{}','x');\n", i)
	}
	sql.WriteString("INSERT INTO entity (repo, entity_type, entity_id, facts, as_of, active) VALUES ('acme/api','pr','acme/api#7','{}','x',0);\n")
	if err := db.Exec(ctx, sql.String()); err != nil {
		t.Fatal(err)
	}
	if n, _ := db.Int(ctx, NeverHydratedSQL); n != 6 {
		t.Fatalf("precondition: %d never-hydrated active rows", n)
	}
	l1, _ := ParseListing("mine", []byte(listing1)) // 1, 2(stale), 3
	active, err := Seed(ctx, db, Prime([]Listing{l1}), time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if active != 3 {
		t.Errorf("active rows after seeding = %d, want 3 (1, 2 stale-listed, 3)", active)
	}
	if n, _ := db.Int(ctx, NeverHydratedSQL); n != 0 {
		t.Errorf("never-hydrated active rows = %d, want 0", n)
	}
	if n, _ := db.Int(ctx, "SELECT count(*) FROM entity WHERE list_fp IS NOT NULL"); n != 2 {
		t.Errorf("list_fp seeded on %d rows, want 2 (the non-stale listed ids)", n)
	}
	if n, _ := db.Int(ctx, "SELECT count(*) FROM entity WHERE entity_id IN ('acme/api#4','acme/api#5','acme/api#6') AND active=1"); n != 0 {
		t.Errorf("rows absent from both listings must be inactive, %d are not", n)
	}
	rows, _ := db.Query(ctx, "SELECT hydrated_at FROM entity WHERE entity_id='acme/api#1'")
	if len(rows) != 1 || rows[0]["hydrated_at"] != "2026-01-05T09:00:00Z" {
		t.Errorf("hydrated_at = %v", rows)
	}
}
