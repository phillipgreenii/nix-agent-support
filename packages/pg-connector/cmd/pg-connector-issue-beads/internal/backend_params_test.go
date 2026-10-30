package internal

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/issue"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

var _ issue.QueryParamsSupporter = (*Backend)(nil)

// perPRExprs are the three per-PR expressions the decider's query carries
// (E1 metadata, E2 focus item, E3 title), bead pg2-eclw3j.3.
var perPRExprs = schema.QueryExpr{
	"list --all --metadata-field repo={repo} --metadata-field pr_number={pr_number}",
	"list --all --metadata-field source_type=pr --metadata-field source_id={repo}#{pr_number}",
	"list --all --title-contains {repo}#{pr_number}",
}

func perPRCtx(values map[string]string) context.Context {
	def := schema.QueryDef{Expr: perPRExprs, Params: map[string]string{
		"repo":      `[A-Za-z0-9][A-Za-z0-9_.-]*/[A-Za-z0-9][A-Za-z0-9_.-]*`,
		"pr_number": `[0-9]{1,9}`,
	}}
	return schema.WithQueryParams(context.Background(), schema.QueryParams{Def: def, Values: values})
}

func TestBackend_SupportsQueryParams(t *testing.T) {
	if !New(&fakeRunner{}).SupportsQueryParams() {
		t.Fatal("the beads backend must opt in to query params")
	}
}

func TestBackend_List_ExpandsEachExpressionWithValues(t *testing.T) {
	fr := &fakeRunner{handle: func([]string) (string, error) { return `{"data":[],"schema_version":1}`, nil }}
	ctx := perPRCtx(map[string]string{"repo": "ZR-Private/ziprecruiter", "pr_number": "113446"})
	if _, err := New(fr).List(ctx, perPRExprs, false, nil); err != nil {
		t.Fatalf("List: %v", err)
	}
	want := [][]string{
		{"list", "--all", "--metadata-field", "repo=ZR-Private/ziprecruiter", "--metadata-field", "pr_number=113446", "--json", "--limit", "0"},
		{"list", "--all", "--metadata-field", "source_type=pr", "--metadata-field", "source_id=ZR-Private/ziprecruiter#113446", "--json", "--limit", "0"},
		{"list", "--all", "--title-contains", "ZR-Private/ziprecruiter#113446", "--json", "--limit", "0"},
	}
	if !reflect.DeepEqual(fr.calls, want) {
		t.Fatalf("bd calls = %q\nwant      %q", fr.calls, want)
	}
}

// TestBackend_List_SubstitutionHappensAfterSplit: a value that contains spaces
// and flag-looking text (a loose declared pattern) is ONE argv element and adds
// no flag.
func TestBackend_List_SubstitutionHappensAfterSplit(t *testing.T) {
	fr := &fakeRunner{handle: func([]string) (string, error) { return `{"data":[],"schema_version":1}`, nil }}
	def := schema.QueryDef{Params: map[string]string{"t": ".*"}}
	ctx := schema.WithQueryParams(context.Background(), schema.QueryParams{Def: def, Values: map[string]string{"t": "x --limit 5 --all"}})
	if _, err := New(fr).List(ctx, schema.QueryExpr{"list --title-contains {t}"}, false, nil); err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []string{"list", "--title-contains", "x --limit 5 --all", "--json", "--limit", "0"}
	if len(fr.calls) != 1 || !reflect.DeepEqual(fr.calls[0], want) {
		t.Fatalf("bd calls = %q, want one call %q", fr.calls, want)
	}
}

func TestBackend_List_UnreplacedPlaceholderIsRefusedBeforeBD(t *testing.T) {
	for name, ctx := range map[string]context.Context{
		"no params in context":   context.Background(),
		"placeholder undeclared": perPRCtx(map[string]string{"repo": "a/b", "pr_number": "1"}),
		"declared without value": perPRCtx(nil),
	} {
		exprs := schema.QueryExpr{"list --title-contains {repo}#{pr_number}"}
		if name == "placeholder undeclared" {
			exprs = schema.QueryExpr{"list --title-contains {nope}"}
		}
		fr := &fakeRunner{handle: func([]string) (string, error) { t.Fatalf("%s: bd was invoked", name); return "", nil }}
		_, err := New(fr).List(ctx, exprs, false, nil)
		if !errors.Is(err, scriptout.ErrUnavailable) {
			t.Errorf("%s: err = %v, want unavailable (a config or caller defect, never a bd call with a brace)", name, err)
		}
		if len(fr.calls) != 0 {
			t.Errorf("%s: bd calls = %q", name, fr.calls)
		}
	}
}

// TestBackend_List_OneFailingExpressionFailsTheWholeSource pins the documented
// behavior: expressions run serially under one deadline and the first failure
// is the source's failure, so a decider sees a non-succeeded source and fails
// closed rather than acting on a partial union.
func TestBackend_List_OneFailingExpressionFailsTheWholeSource(t *testing.T) {
	n := 0
	fr := &fakeRunner{handle: func([]string) (string, error) {
		n++
		if n == 2 {
			return "", scriptout.WrapError(scriptout.ErrUnavailable, "bd timed out")
		}
		return `{"data":[{"id":"tp-1","title":"a","status":"open"}],"schema_version":1}`, nil
	}}
	ctx := perPRCtx(map[string]string{"repo": "a/b", "pr_number": "1"})
	got, err := New(fr).List(ctx, perPRExprs, false, nil)
	if !errors.Is(err, scriptout.ErrUnavailable) || got != nil {
		t.Fatalf("got %+v, err %v; want no partial result and unavailable", got, err)
	}
	if len(fr.calls) != 2 {
		t.Fatalf("bd calls = %d, want 2 (the third expression must not run after a failure)", len(fr.calls))
	}
	if !strings.Contains(strings.Join(fr.calls[1], " "), "source_id=a/b#1") {
		t.Fatalf("second call = %q", fr.calls[1])
	}
}
