package groups

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/engine"
)

// updatedLayout is the layout of Jira's own updated text, such as
// 2026-10-05T10:11:12.000+0000.
const updatedLayout = "2006-01-02T15:04:05.000-0700"

// Describer decodes a stored summary into the engine.Primary the shared change
// engine reads.
type Describer struct{}

// Describe decodes content (a stored summary) into a Primary with the issue key
// as Ref, the parsed updated token as OriginAt (zero when unparsable) and Gone.
// Stamp, Stamp2, Terminal and Attrs are left zero: an issue becomes terminal
// only after being closed for the terminal TTL, which the caller judges.
func (Describer) Describe(content []byte) (engine.Primary, error) {
	var c SummaryContent
	if err := json.Unmarshal(content, &c); err != nil {
		return engine.Primary{}, fmt.Errorf("groups: decode stored summary: %w", err)
	}
	return engine.Primary{Ref: c.Issue.ID, OriginAt: originTime(c.Updated), Gone: c.Gone}, nil
}

// originTime parses Jira's updated text, then RFC 3339; zero when neither fits.
func originTime(s string) time.Time {
	if t, err := time.Parse(updatedLayout, s); err == nil {
		return t
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}
	return time.Time{}
}

// FoldRef folds a wire reference so two spellings of one Jira key compare equal.
func FoldRef(ref string) string { return strings.ToUpper(ref) }
