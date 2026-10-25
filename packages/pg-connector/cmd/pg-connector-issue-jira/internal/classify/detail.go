package classify

import (
	"slices"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/engine"
)

// Detail classifies a refreshed detail against the one stored before it.
//
// description_changed (field description, each side cut to bodyCap bytes on a
// UTF-8 boundary; a bodyCap of zero or less means no cut) is carried when the
// description differs, and deps_changed (field deps, the direct edges as a
// sorted set) when the direct edges differ. Nothing else is compared: comment
// bodies, the two operator facts and the comment summary are not. When two
// descriptions differ only beyond bodyCap the kind is still carried, with the
// description field absent (its two cut values are equal).
func Detail(prev, next groups.DetailContent, bodyCap int) engine.Result {
	p, n := groups.NormaliseDetail(prev, bodyCap), groups.NormaliseDetail(next, bodyCap)
	var kinds []string
	var fields []Field

	if p.Description != n.Description {
		kinds = append(kinds, KindDescriptionChanged)
		fields = append(fields, Field{
			Name:   "description",
			Before: jsonOf(cutUTF8(p.Description, bodyCap)),
			After:  jsonOf(cutUTF8(n.Description, bodyCap)),
		})
	}
	if pe, ne := edgeSet(p.Deps), edgeSet(n.Deps); !slices.Equal(pe, ne) {
		kinds = append(kinds, KindDepsChanged)
		fields = append(fields, Field{Name: "deps", Before: jsonOf(pe), After: jsonOf(ne)})
	}
	return engine.Result{Kinds: engine.OrderKinds(Kinds(), kinds), Fields: engine.Normalize(fields)}
}
