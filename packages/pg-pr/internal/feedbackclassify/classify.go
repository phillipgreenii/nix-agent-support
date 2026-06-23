// Package feedbackclassify classifies comment authors (human vs agent, which
// agent, whether it's ours) from the config-driven agent registry. Used by
// ingestion to populate feedback rows.
package feedbackclassify

import (
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/marker"
)

// BotPolicy is the per-agent config (subset; full policy lives in agentregistry).
type BotPolicy struct {
	AgentName       string
	BodyMarker      string // optional substring to disambiguate a shared login (e.g. github-actions[bot])
	ManagedUpstream bool
	DefaultSeverity string
}

// Registry maps known agent logins to policy. Built from config.
type Registry struct {
	bots map[string]BotPolicy
}

// NewRegistry builds a classifier registry.
func NewRegistry(bots map[string]BotPolicy) Registry { return Registry{bots: bots} }

// Author is the classification result.
type Author struct {
	Kind            string // human | agent
	AgentName       string
	IsOurs          bool
	ManagedUpstream bool
	DefaultSeverity string
}

// Classify decides the author classification. self is the configured SelfLogin.
func (r Registry) Classify(login, typename, body, self string) Author {
	// "Ours" (pg-pr/agent-posted) is marker-detected, NOT login-based — pg-pr
	// posts under the user's own login.
	if marker.IsOurs(body) {
		return Author{Kind: "agent", AgentName: "pg-pr", IsOurs: true}
	}
	// Known configured bot?
	if pol, ok := r.bots[login]; ok {
		if pol.BodyMarker == "" || strings.Contains(body, pol.BodyMarker) {
			return Author{
				Kind: "agent", AgentName: pol.AgentName,
				ManagedUpstream: pol.ManagedUpstream, DefaultSeverity: pol.DefaultSeverity,
			}
		}
	}
	// Bot/Mannequin typename → agent fallback.
	if typename == "Bot" || typename == "Mannequin" {
		return Author{Kind: "agent", AgentName: "other"}
	}
	// Otherwise human (the self manual-note case included; author_role is set by
	// the caller, not here).
	return Author{Kind: "human"}
}
