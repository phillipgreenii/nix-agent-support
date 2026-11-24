package query_test

import (
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/query"
)

// countingRes counts how many copies a query opens and how many it reads
// through the per-row Line path.
type countingRes struct {
	query.Resolver
	opens, lines int
}

func (c *countingRes) Line(file int64, gen int, off int64, ln int) ([]byte, error) {
	c.lines++
	return c.Resolver.Line(file, gen, off, ln)
}

func (c *countingRes) OpenLines(file int64, gen int) (query.LineReader, error) {
	c.opens++
	return c.Resolver.(query.LineOpener).OpenLines(file, gen)
}

// A raw query opens each (file, generation) copy once for the whole answer, not
// once per returned row, and returns the same lines the per-row read does.
func TestRawQueryOpensEachCopyOnce(t *testing.T) {
	k := kit(t)
	want := q(t, k, `{"where":{"field":"type","eq":"assistant"},"select":["uuid"],"raw":true,"limit":100}`)
	if len(want.Raw) != 3 {
		t.Fatalf("want 3 raw lines, got %d", len(want.Raw))
	}
	cr := &countingRes{Resolver: k.Engine.Res}
	k.Engine.Res = cr
	got := q(t, k, `{"where":{"field":"type","eq":"assistant"},"select":["uuid"],"raw":true,"limit":100}`)
	if cr.opens != 1 || cr.lines != 0 {
		t.Fatalf("opened %d copies and read %d lines per row, want 1 and 0", cr.opens, cr.lines)
	}
	if js(got.Raw) != js(want.Raw) {
		t.Fatalf("raw %s, want %s", js(got.Raw), js(want.Raw))
	}
}
