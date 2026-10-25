package classify

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

func baseDetail() groups.DetailContent {
	return groups.DetailContent{
		Description:     "Adds a widget.",
		Comments:        []groups.Comment{{ID: "1", Author: "ada", Body: "first", Created: "2026-10-01T00:00:00Z"}},
		Deps:            []schema.IssueDependency{{ID: "PROJ-2", Type: "blocks"}, {ID: "PROJ-3", Type: "relates"}},
		StatusChangedAt: "2026-10-01T00:00:00Z", OperatorUpdatedAt: "2026-10-02T00:00:00Z",
	}
}

func TestDetailDescription(t *testing.T) {
	prev, next := baseDetail(), baseDetail()
	next.Description = "Adds a gadget."
	res := Detail(prev, next, testCap)
	if want := []string{KindDescriptionChanged}; !slices.Equal(res.Kinds, want) {
		t.Fatalf("kinds = %v", res.Kinds)
	}
	if len(res.Fields) != 1 || res.Fields[0].Name != "description" ||
		string(res.Fields[0].Before) != `"Adds a widget."` || string(res.Fields[0].After) != `"Adds a gadget."` {
		t.Fatalf("fields = %+v", res.Fields)
	}
}

func TestDetailDescriptionIsCutToBodyCap(t *testing.T) {
	prev, next := baseDetail(), baseDetail()
	prev.Description = "abcdefgh"
	next.Description = "abcdXfgh"
	res := Detail(prev, next, 6)
	if len(res.Fields) != 1 || string(res.Fields[0].Before) != `"abcdef"` || string(res.Fields[0].After) != `"abcdXf"` {
		t.Fatalf("fields = %+v", res.Fields)
	}
	// The cut never lands inside a multi-byte rune.
	prev.Description = "ab" + strings.Repeat("é", 4)
	next.Description = "ab" + strings.Repeat("ü", 4)
	res = Detail(prev, next, 5)
	var before string
	if err := json.Unmarshal(res.Fields[0].Before, &before); err != nil || before != "abé" {
		t.Fatalf("before = %q (%v)", before, err)
	}
	// A difference beyond the cap still carries the kind.
	prev.Description, next.Description = "abcdef-1", "abcdef-2"
	res = Detail(prev, next, 6)
	if !slices.Equal(res.Kinds, []string{KindDescriptionChanged}) {
		t.Fatalf("kinds = %v", res.Kinds)
	}
}

func TestDetailDeps(t *testing.T) {
	prev, next := baseDetail(), baseDetail()
	next.Deps = []schema.IssueDependency{{ID: "PROJ-2", Type: "blocks"}}
	res := Detail(prev, next, testCap)
	if want := []string{KindDepsChanged}; !slices.Equal(res.Kinds, want) {
		t.Fatalf("kinds = %v", res.Kinds)
	}
	if len(res.Fields) != 1 || res.Fields[0].Name != "deps" ||
		string(res.Fields[0].After) != `[{"id":"PROJ-2","type":"blocks"}]` {
		t.Fatalf("fields = %+v", res.Fields)
	}
	// A type change on the same target is a difference.
	next.Deps = []schema.IssueDependency{{ID: "PROJ-2", Type: "relates"}, {ID: "PROJ-3", Type: "relates"}}
	if res := Detail(prev, next, testCap); !slices.Equal(res.Kinds, []string{KindDepsChanged}) {
		t.Fatalf("kinds = %v", res.Kinds)
	}
	// Order is not a difference; nil and empty are the same.
	next.Deps = []schema.IssueDependency{{ID: "PROJ-3", Type: "relates"}, {ID: "PROJ-2", Type: "blocks"}}
	if res := Detail(prev, next, testCap); !res.Empty() {
		t.Fatalf("reordered edges: %+v", res)
	}
	prev.Deps, next.Deps = nil, []schema.IssueDependency{}
	if res := Detail(prev, next, testCap); !res.Empty() {
		t.Fatalf("nil vs empty edges: %+v", res)
	}
}

func TestDetailBothDescriptionAndDepsInCanonicalOrder(t *testing.T) {
	prev, next := baseDetail(), baseDetail()
	next.Description = "x"
	next.Deps = nil
	res := Detail(prev, next, testCap)
	if want := []string{KindDescriptionChanged, KindDepsChanged}; !slices.Equal(res.Kinds, want) {
		t.Fatalf("kinds = %v, want %v", res.Kinds, want)
	}
	if got, want := names(res.Fields), []string{"deps", "description"}; !slices.Equal(got, want) {
		t.Fatalf("fields = %v, want %v", got, want)
	}
}

func TestDetailIgnoresCommentsAndOperatorFacts(t *testing.T) {
	prev, next := baseDetail(), baseDetail()
	next.Comments = []groups.Comment{
		{ID: "1", Author: "ada", Body: "edited", Created: "2026-10-01T00:00:00Z"},
		{ID: "2", Author: "grace", Body: "new", Created: "2026-10-02T00:00:00Z"},
	}
	next.StatusChangedAt = "2030-01-01T00:00:00Z"
	next.OperatorUpdatedAt = "2030-01-01T00:00:00Z"
	if res := Detail(prev, next, testCap); !res.Empty() {
		t.Fatalf("result = %+v, want empty", res)
	}
	if res := Detail(prev, baseDetail(), testCap); !res.Empty() {
		t.Fatalf("identical: %+v", res)
	}
}

func TestDetailIsDeterministic(t *testing.T) {
	prev, next := baseDetail(), baseDetail()
	next.Description = "other"
	next.Deps = []schema.IssueDependency{{ID: "B", Type: "x"}, {ID: "A", Type: "x"}}
	a, _ := json.Marshal(Detail(prev, next, testCap))
	b, _ := json.Marshal(Detail(prev, next, testCap))
	if string(a) != string(b) {
		t.Fatalf("runs differ:\n%s\n%s", a, b)
	}
}
