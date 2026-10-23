package groups

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/summary"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/engine"
)

// The keys of engine.Primary.Attrs the Describer sets and the group fetchers
// read back through the shared groupsvc.Target.
const (
	// AttrHeadRef is the pull request's head branch, which the runs group needs.
	AttrHeadRef = "head_ref"
	// AttrState is the pull request's state as GitHub spells it.
	AttrState = "state"
)

// Describer decodes the stored summary content into the engine.Primary the
// shared change engine and the shared group refresh service read. It is the
// Describe half of the daemon's engine.Classifier and a leaf of the GitHub
// tree: it imports neither the changes nor the query package, so both can
// delegate to it.
type Describer struct{}

// Describe decodes content (a stored summary) and fills every field of the
// Primary: the reference, the head and base stamps, the origin time, Gone,
// Terminal (merged or closed) and the head branch and state as Attrs.
func (Describer) Describe(content []byte) (engine.Primary, error) {
	var s summary.Summary
	if err := json.Unmarshal(content, &s); err != nil {
		return engine.Primary{}, fmt.Errorf("changes: decode stored summary: %w", err)
	}
	return engine.Primary{
		Ref: s.Ref(), Stamp: s.HeadRefOid, Stamp2: s.BaseRefOid,
		OriginAt: OriginTime(s), Gone: s.Gone,
		Terminal: summary.IsTerminal(s.State),
		Attrs:    map[string]string{AttrHeadRef: s.HeadRefName, AttrState: s.State},
	}, nil
}

// OriginTime is the summary's updated_at when it parses, else zero.
func OriginTime(s summary.Summary) time.Time {
	t, err := time.Parse(time.RFC3339, s.UpdatedAt)
	if err != nil {
		return time.Time{}
	}
	return t
}

// FoldRef is a wire reference with owner/repo folded to lower case and the number
// kept exactly: the one fold two references of one pull request compare equal
// under (the repository part lower-cased, the #number part kept; a reference
// without # is lower-cased whole).
func FoldRef(ref string) string {
	i := strings.LastIndex(ref, "#")
	if i < 0 {
		return strings.ToLower(ref)
	}
	return strings.ToLower(ref[:i]) + ref[i:]
}
