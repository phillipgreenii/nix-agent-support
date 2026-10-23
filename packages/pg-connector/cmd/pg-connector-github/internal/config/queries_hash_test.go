package config

import (
	"encoding/json"
	"strings"
	"testing"
)

// A deployment that uses only bare query forms hashes as it did before
// config.PR.Queries became a map of schema.QueryDef: an unwatched definition
// marshals as the bare list.
func TestBareQueryFormsMarshalAsBeforeAndHashEqually(t *testing.T) {
	a, err := Load(write(t, `{"state_dir":"/tmp/s","pr":{"queries":{"mine":"q1","team":["q2","q3"]}}}`), noEnv)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Load(write(t, `{"state_dir":"/tmp/s","pr":{"queries":{"mine":["q1"],"team":["q2","q3"]}}}`), noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if a.Hash == "" || a.Hash != b.Hash {
		t.Fatalf("string and one-element list spellings hash differently: %q vs %q", a.Hash, b.Hash)
	}
	out, err := json.Marshal(a.PR)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"queries":{"mine":["q1"],"team":["q2","q3"]}`) {
		t.Fatalf("bare forms marshal as %s", out)
	}
}

func TestWatchedQueryObjectFormIsParsedAndChangesTheHash(t *testing.T) {
	a, err := Load(write(t, `{"state_dir":"/tmp/s","pr":{"queries":{"mine":"q1"}}}`), noEnv)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Load(write(t, `{"state_dir":"/tmp/s","pr":{"queries":{"mine":{"expr":"q1","watched":true}}}}`), noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if !b.PR.Queries["mine"].Watched || len(b.PR.Queries["mine"].Expr) != 1 {
		t.Fatalf("mine = %#v", b.PR.Queries["mine"])
	}
	if a.Hash == b.Hash {
		t.Fatal("marking a query watched must change the hash")
	}
}
