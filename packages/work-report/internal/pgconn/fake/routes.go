package fake

import (
	"encoding/json"
	"fmt"
)

// The helpers below build Routes whose Stdout has the JSON wire shape of the
// pg-connector verbs work-report's degraded-source path runs (`issue list`,
// `issue create|comment|close`, `config validate`). They are conveniences over
// the generic Route; a test may equally write the Route by hand.

// IssueEntity is one element of an `issue list` entities[] array.
type IssueEntity struct {
	ID     string   `json:"id"`
	Title  string   `json:"title"`
	Labels []string `json:"labels,omitempty"`
	State  string   `json:"state,omitempty"`
}

// IssueListRoute answers `issue list` with the given entities.
func IssueListRoute(entities ...IssueEntity) Route {
	if entities == nil {
		entities = []IssueEntity{}
	}
	return Route{Match: []string{"issue", "list"}, Stdout: mustJSON(map[string]any{"entities": entities, "sources": []any{}})}
}

// IssueCreateRoute answers `issue create` with a created issue of the given id
// in the targeted-op envelope.
func IssueCreateRoute(id string) Route {
	return Route{Match: []string{"issue", "create"}, Stdout: mustJSON(map[string]any{"result": map[string]any{"id": id}})}
}

// IssueCommentRoute answers `issue comment`.
func IssueCommentRoute() Route {
	return Route{Match: []string{"issue", "comment"}, Stdout: `{"result":{}}`}
}

// IssueCloseRoute answers `issue close`.
func IssueCloseRoute() Route {
	return Route{Match: []string{"issue", "close"}, Stdout: `{"result":{}}`}
}

// ConfigValidateRoute answers `config validate` with one sources[] row per
// (source, status, reason) triple given as ConfigRow values.
func ConfigValidateRoute(rows ...ConfigRow) Route {
	if rows == nil {
		rows = []ConfigRow{}
	}
	return Route{Match: []string{"config", "validate"}, Stdout: mustJSON(map[string]any{"sources": rows})}
}

// ConfigRow is one `config validate` sources[] row.
type ConfigRow struct {
	Source string `json:"source"`
	Status string `json:"status"`
	Count  int    `json:"count"`
	Reason string `json:"reason,omitempty"`
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("fake: marshal route: %v", err))
	}
	return string(b)
}
