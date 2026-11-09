package query_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store/countdriver"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/testkit"
)

// An aggregate collapses the matching records in SQL, so the rows that reach
// the Go side track the number of groups, not the number of records. The
// assertion is a read count through the counting driver, never wall-clock.
func TestAggregateReadsGroupsNotRecords(t *testing.T) {
	const lines = 300
	var sb strings.Builder
	for i := 0; i < lines; i++ {
		model := "example-model-a"
		if i%3 == 0 {
			model = "example-model-b"
		}
		fmt.Fprintf(&sb, `{"type":"assistant","uuid":"u%d","sessionId":"example-session-1","timestamp":"2026-10-29T20:%02d:%02dZ","message":{"id":"m%d","model":%q,"role":"assistant","usage":{"output_tokens":2},"content":[{"type":"text","text":"x"}]}}`+"\n",
			i, i/60, i%60, i, model)
	}
	c := countdriver.Register("query-aggregate-reads")
	k := testkit.NewWithStoreOptions(t, store.Options{DriverName: "query-aggregate-reads"})
	k.Add("example-proj", "example-session-1.jsonl", sb.String())
	k.Parse()

	c.Reset()
	list := q(t, k, `{"where":{"field":"type","eq":"assistant"},"select":["model"],"limit":10000}`)
	recordsRead := c.RowsRead()
	if len(list.Rows) != lines || recordsRead < lines {
		t.Fatalf("listing: %d rows, %d read, want at least %d", len(list.Rows), recordsRead, lines)
	}

	c.Reset()
	a := q(t, k, `{"where":{"field":"type","eq":"assistant"},"group":["model"],"aggregate":[{"fn":"count","as":"n"},{"fn":"sum","field":"usage.output_tokens","as":"out"}],"order":[{"by":"model"}]}`)
	aggRead := c.RowsRead()
	if js(a.Rows) != `[["example-model-a",200,400],["example-model-b",100,200]]` {
		t.Fatalf("rows %s", js(a.Rows))
	}
	if aggRead > 10 || aggRead*10 > recordsRead {
		t.Fatalf("aggregate read %d rows, the matching records are %d", aggRead, recordsRead)
	}

	c.Reset()
	n := q(t, k, `{"where":{"field":"type","eq":"assistant"},"aggregate":[{"fn":"count","as":"n"}]}`)
	if js(n.Rows) != `[[300]]` || c.RowsRead() > 5 {
		t.Fatalf("count %s read %d rows", js(n.Rows), c.RowsRead())
	}
}
