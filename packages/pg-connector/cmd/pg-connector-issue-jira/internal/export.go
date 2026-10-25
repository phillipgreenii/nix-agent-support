// export.go: the exported forms of the decode, mapping and classification
// helpers of the Backend, so the daemon's own packages (the origin runner, the
// summary and detail fetchers) decode and map exactly as the one-shot Backend
// does. Nothing here changes behaviour: each is an alias or a thin wrapper.
package internal

import (
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

// PjiraIssue is one decoded `pjira issue` / `pjira search` item.
type PjiraIssue = pjiraIssue

// PjiraSearchResult is one decoded `pjira search` answer: Items, Truncated
// and the next page token.
type PjiraSearchResult = pjiraSearchResult

// DecodeIssue decodes `pjira issue <KEY>`'s stdout; an answer with an empty
// key is an error.
func DecodeIssue(raw string) (*PjiraIssue, error) { return decodePJIRAIssue(raw) }

// DecodeSearchResult decodes `pjira search`'s stdout.
func DecodeSearchResult(raw string) (*PjiraSearchResult, error) {
	return decodePJIRASearchResult(raw)
}

// ToSchemaIssue maps a decoded issue onto the issue wire shape; asOf passes
// through unchanged as the issue's AsOf.
func ToSchemaIssue(iss *PjiraIssue, asOf time.Time) *schema.Issue { return toSchemaIssue(iss, asOf) }

// ClassifyPjiraError maps a pjira error text onto the scriptout error
// taxonomy (not found, unauthenticated, invalid argument, else unavailable).
func ClassifyPjiraError(msg string) error { return classifyPJIRAErrorMessage(msg) }

// IsJiraKey reports whether id has the shape of a Jira issue key (project key,
// hyphen, number): the one key shape every part of the daemon uses.
func IsJiraKey(id string) bool { return jiraKeyRE.MatchString(id) }

// WithGetenv returns a copy of b whose environment lookups (the operator
// email, the create project) go through getenv instead of the process
// environment, so the daemon answers them from its configured pjira_env.
func (b *Backend) WithGetenv(getenv func(string) string) *Backend {
	cp := *b
	cp.getenv = getenv
	return &cp
}
