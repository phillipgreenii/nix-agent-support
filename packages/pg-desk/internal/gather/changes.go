package gather

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// ListChangesSource is one row of pg-connector's `<type> changes` sources[]:
// one per pg-connector backend, with pg-connector's own status strings
// (succeeded|degraded|disabled; disabled counts as healthy).
type ListChangesSource struct {
	Backend string
	Status  string
	Reason  string
}

// ListedChange is one entry of pg-connector's `<type> changes` changes[]:
// the kind pg-connector reported and the entity's canonical id and title.
type ListedChange struct {
	Change   ChangeKind
	EntityID string
	Title    string
}

// ListChangesResult is what one ListChanges call returns.
type ListChangesResult struct {
	Sources []ListChangesSource
	Changes []ListedChange
}

// changesWireOut is the subset of pg-connector's `<type> changes` response
// ({"sources":[{"backend","status","reason"}],"changes":[{"change","source",
// "entity"}]}) this package decodes.
type changesWireOut struct {
	Sources []struct {
		Backend string `json:"backend"`
		Status  string `json:"status"`
		Reason  string `json:"reason"`
	} `json:"sources"`
	Changes []struct {
		Change string          `json:"change"`
		Entity json.RawMessage `json:"entity"`
	} `json:"changes"`
}

// changesEntityWire is the part of a changes[] entity this package reads. A
// thread entity has no title field, only text.
type changesEntityWire struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Text  string `json:"text"`
}

// threadChangeTitleMaxRunes bounds a thread's derived title (matches
// changes.TitleFromFacts, which this package cannot import).
const threadChangeTitleMaxRunes = 80

// ListChanges is the ONE place pg-desk execs `pg-connector <type> changes
// --query <query> --consumer <consumer> --output json`; it goes through the
// package's single exec chokepoint (run). It classifies the call by
// pg-connector's fan-out exit scheme: 0 ok, 2 partial (the partial result is
// returned, its degraded sources visible in Sources), 3 total failure (an
// error). A failure to start pg-connector, any other exit code, or an
// undecodable response is an error too.
func (g *Gatherer) ListChanges(ctx context.Context, entityType, query, consumer string) (ListChangesResult, error) {
	if entityType == "" || query == "" || consumer == "" {
		return ListChangesResult{}, fmt.Errorf("gather: list changes: type, query and consumer are required")
	}
	var env []string
	if entityType == "issue" {
		env = g.issueBeadsDirEnv()
	}
	args := []string{entityType, "changes", "--query", query, "--consumer", consumer, "--output", "json"}
	res, err := g.run(ctx, args, env)
	if err != nil {
		return ListChangesResult{}, err
	}
	switch res.exitCode {
	case 0, 2:
	default:
		return ListChangesResult{}, fmt.Errorf("pg-connector %v: exit %d: %s", args, res.exitCode, wireErrorMessage(res.stdout))
	}
	var wire changesWireOut
	if err := json.Unmarshal(res.stdout, &wire); err != nil {
		return ListChangesResult{}, fmt.Errorf("decode pg-connector %v stdout: %w", args, err)
	}
	out := ListChangesResult{
		Sources: make([]ListChangesSource, 0, len(wire.Sources)),
		Changes: make([]ListedChange, 0, len(wire.Changes)),
	}
	for _, s := range wire.Sources {
		out.Sources = append(out.Sources, ListChangesSource{Backend: s.Backend, Status: s.Status, Reason: s.Reason})
	}
	for _, c := range wire.Changes {
		var ent changesEntityWire
		if err := json.Unmarshal(c.Entity, &ent); err != nil {
			return ListChangesResult{}, fmt.Errorf("decode pg-connector %v entity: %w", args, err)
		}
		if ent.ID == "" {
			return ListChangesResult{}, fmt.Errorf("pg-connector %v reported a %q change for an entity with no id", args, c.Change)
		}
		title := ent.Title
		if entityType == "thread" && title == "" {
			title = firstNonEmptyLine(ent.Text)
		}
		out.Changes = append(out.Changes, ListedChange{Change: ChangeKind(c.Change), EntityID: ent.ID, Title: title})
	}
	return out, nil
}

// firstNonEmptyLine is the first non-empty trimmed line of text, truncated to
// threadChangeTitleMaxRunes runes.
func firstNonEmptyLine(text string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if r := []rune(line); len(r) > threadChangeTitleMaxRunes {
			return string(r[:threadChangeTitleMaxRunes])
		}
		return line
	}
	return ""
}
