// Package agentregistry classifies PR participants as agents vs humans,
// and matches agent-authored comment bodies against per-agent approval
// regexes. Config is loaded from the pg-pr config file.
package agentregistry

import (
	"fmt"
	"regexp"
)

// Entry describes one known agent account.
type Entry struct {
	Login         string `yaml:"login" json:"login"`
	ApprovalRegex string `yaml:"approval_regex" json:"approval_regex"`
}

// Registry classifies logins and bodies. Safe for concurrent reads.
type Registry struct {
	byLogin map[string]*regexp.Regexp
}

// New compiles entries; returns an error on the first invalid regex.
func New(entries []Entry) (*Registry, error) {
	r := &Registry{byLogin: make(map[string]*regexp.Regexp, len(entries))}
	for _, e := range entries {
		re, err := regexp.Compile(e.ApprovalRegex)
		if err != nil {
			return nil, fmt.Errorf("agent %q: compile approval_regex: %w", e.Login, err)
		}
		r.byLogin[e.Login] = re
	}
	return r, nil
}

// IsAgent reports whether login is a registered agent account.
func (r *Registry) IsAgent(login string) bool {
	_, ok := r.byLogin[login]
	return ok
}

// MatchApproval reports whether `body` constitutes an approval verdict
// authored by `login`. Returns false when login is not a registered agent.
func (r *Registry) MatchApproval(login, body string) bool {
	re, ok := r.byLogin[login]
	if !ok {
		return false
	}
	return re.MatchString(body)
}
