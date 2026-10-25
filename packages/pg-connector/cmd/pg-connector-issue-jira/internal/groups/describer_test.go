package groups

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/engine"
)

var _ engine.Classifier // the Describer is the Describe half of a Classifier

func mustJSON(t *testing.T, c SummaryContent) []byte {
	t.Helper()
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestDescribe(t *testing.T) {
	c := base()
	p, err := Describer{}.Describe(mustJSON(t, c))
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 10, 5, 10, 11, 12, 0, time.UTC)
	if p.Ref != "PROJ-12" || !p.OriginAt.Equal(want) || p.Gone {
		t.Fatalf("primary = %+v", p)
	}
	if p.Stamp != "" || p.Stamp2 != "" || p.Attrs != nil {
		t.Fatalf("stamps/attrs must be zero: %+v", p)
	}
}

func TestDescribeGoneAndRFC3339(t *testing.T) {
	c := base()
	c.Gone = true
	c.Updated = "2026-10-05T10:11:12Z"
	p, err := Describer{}.Describe(mustJSON(t, c))
	if err != nil {
		t.Fatal(err)
	}
	if !p.Gone || !p.OriginAt.Equal(time.Date(2026, 10, 5, 10, 11, 12, 0, time.UTC)) {
		t.Fatalf("primary = %+v", p)
	}
}

func TestDescribeUnparsableUpdatedAndBadJSON(t *testing.T) {
	c := base()
	c.Updated = "yesterday"
	p, err := Describer{}.Describe(mustJSON(t, c))
	if err != nil || !p.OriginAt.IsZero() {
		t.Fatalf("p=%+v err=%v", p, err)
	}
	if _, err := (Describer{}).Describe([]byte("{")); err == nil {
		t.Fatal("bad JSON must error")
	}
}

func TestDescribeOffset(t *testing.T) {
	c := base()
	c.Updated = "2026-10-05T10:11:12.000-0500"
	p, _ := Describer{}.Describe(mustJSON(t, c))
	if !p.OriginAt.Equal(time.Date(2026, 10, 5, 15, 11, 12, 0, time.UTC)) {
		t.Fatalf("OriginAt = %v", p.OriginAt)
	}
}

func TestFoldRef(t *testing.T) {
	if FoldRef("proj-12") != FoldRef("PROJ-12") || FoldRef("proj-12") != "PROJ-12" {
		t.Fatal("FoldRef must upper-case")
	}
}
