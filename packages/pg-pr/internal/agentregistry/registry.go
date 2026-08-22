// Package agentregistry classifies PR participants as agents vs humans,
// and matches agent-authored comment bodies against per-agent approval
// regexes. Config is loaded from the pg-pr config file.
package agentregistry

import (
	"fmt"
	"regexp"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/feedbackclassify"
)

// Policy is the per-agent behavioral policy.
type Policy struct {
	Ingest          bool   `yaml:"ingest" json:"ingest"`
	ManagedUpstream bool   `yaml:"managed_upstream" json:"managed_upstream"`
	Reply           bool   `yaml:"reply" json:"reply"`
	Resolve         bool   `yaml:"resolve" json:"resolve"`
	Minimize        bool   `yaml:"minimize" json:"minimize"`
	DefaultSeverity string `yaml:"default_severity,omitempty" json:"default_severity,omitempty"`
}

// Entry describes one known agent account.
type Entry struct {
	Login         string `yaml:"login" json:"login"`
	AgentName     string `yaml:"agent_name,omitempty" json:"agent_name,omitempty"`
	BodyMarker    string `yaml:"body_marker,omitempty" json:"body_marker,omitempty"`
	ApprovalRegex string `yaml:"approval_regex,omitempty" json:"approval_regex,omitempty"`
	// Approver marks this entry as counting toward PR approval. It is a
	// SEPARATE notion from agent registration: every Entry here is a
	// registered/ingested agent (IsAgent reports true for its Login, and
	// comment ingestion treats it as before), but Approver being false —
	// the default, and the value for any pre-existing entry that predates
	// this field — means its verdict NEVER counts toward approval,
	// regardless of ApprovalRegex. Approver membership is ADDITIVE and is
	// NEVER implied by a non-empty ApprovalRegex; it must be set to true
	// explicitly. See Registry.IsApprover.
	Approver bool   `yaml:"approver,omitempty" json:"approver,omitempty"`
	Policy   Policy `yaml:"policy" json:"policy"`
}

// entry is the internal representation after compilation.
type entry struct {
	e  Entry
	re *regexp.Regexp // nil when ApprovalRegex is empty
}

// Registry classifies logins and bodies. Safe for concurrent reads.
type Registry struct {
	byLogin map[string]*entry
}

// New compiles entries; returns an error on the first invalid regex.
// Entries with an empty ApprovalRegex are accepted without error — they
// register in the agent set but MatchApproval always returns false for them.
func New(entries []Entry) (*Registry, error) {
	r := &Registry{byLogin: make(map[string]*entry, len(entries))}
	for _, e := range entries {
		var re *regexp.Regexp
		if e.ApprovalRegex != "" {
			var err error
			re, err = regexp.Compile(e.ApprovalRegex)
			if err != nil {
				return nil, fmt.Errorf("agent %q: compile approval_regex: %w", e.Login, err)
			}
		}
		r.byLogin[e.Login] = &entry{e: e, re: re}
	}
	return r, nil
}

// IsAgent reports whether login is a registered agent account.
func (r *Registry) IsAgent(login string) bool {
	_, ok := r.byLogin[login]
	return ok
}

// IsApprover reports whether login is a registered agent explicitly marked
// as counting toward PR approval (Entry.Approver == true). This is
// structurally distinct from IsAgent: a login can be a registered,
// ingested agent (IsAgent true) while never counting as an approver.
// Approver status is never inferred from ApprovalRegex or any other
// field — it must have been set explicitly on the Entry. Returns false
// when login is not a registered agent at all.
func (r *Registry) IsApprover(login string) bool {
	ent, ok := r.byLogin[login]
	if !ok {
		return false
	}
	return ent.e.Approver
}

// MatchApproval reports whether `body` constitutes an approval verdict
// authored by `login`. Returns false when login is not a registered agent
// or when the agent's approval_regex is empty.
func (r *Registry) MatchApproval(login, body string) bool {
	ent, ok := r.byLogin[login]
	if !ok || ent.re == nil {
		return false
	}
	return ent.re.MatchString(body)
}

// PolicyFor returns the Policy for the given login. Returns false when the
// login is not a registered agent.
func (r *Registry) PolicyFor(login string) (Policy, bool) {
	ent, ok := r.byLogin[login]
	if !ok {
		return Policy{}, false
	}
	return ent.e.Policy, true
}

// ToClassifyRegistry builds a feedbackclassify.Registry from this registry,
// mapping each agent entry's login to a BotPolicy.
func (r *Registry) ToClassifyRegistry() feedbackclassify.Registry {
	bots := make(map[string]feedbackclassify.BotPolicy, len(r.byLogin))
	for login, ent := range r.byLogin {
		bots[login] = feedbackclassify.BotPolicy{
			AgentName:       ent.e.AgentName,
			BodyMarker:      ent.e.BodyMarker,
			ManagedUpstream: ent.e.Policy.ManagedUpstream,
			DefaultSeverity: ent.e.Policy.DefaultSeverity,
		}
	}
	return feedbackclassify.NewRegistry(bots)
}
