// Package feedbackclassify classifies comment authors (human vs agent, which
// agent, whether it's ours) from the config-driven agent registry. Used by
// ingestion to populate feedback rows.
package feedbackclassify

import (
	"crypto/sha256"
	"encoding/hex"
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

// FPParts carries the inputs to Fingerprint. Which fields matter depends on kind.
type FPParts struct {
	File       string
	Line       int
	Body       string
	CheckName  string
	SubjectSHA string
	ExternalID string
	// ThreadID, when set, is used as the primary key for code-comment-thread
	// fingerprints instead of file+body. This makes the fingerprint stable
	// across force-pushes and body edits for the same review thread.
	ThreadID string
}

// Fingerprint computes a stable dedup key for a feedback item, per-kind:
//   - ci-failure: revision-SCOPED (check_name + subject_sha) — each failure per
//     revision is distinct (build-failure history).
//   - code-comment-thread: revision-STABLE (file + normalized body) — one row
//     survives force-pushes; staleness is tracked separately via is_outdated.
//   - pr-comments: revision-stable (external id if present, else normalized body).
//   - review-request / jira-link: keyed by external id.
//
// Body whitespace is collapsed so trivial reflow doesn't churn the key.
func Fingerprint(kind string, p FPParts) string {
	norm := strings.Join(strings.Fields(p.Body), " ")
	var key string
	switch kind {
	case "ci-failure":
		key = "ci-failure\x00" + p.CheckName + "\x00" + p.SubjectSHA
	case "code-comment-thread":
		if p.ThreadID != "" {
			key = "code-comment-thread\x00thread\x00" + p.ThreadID
		} else {
			key = "code-comment-thread\x00" + p.File + "\x00" + norm
		}
	case "pr-comments":
		key = "pr-comments\x00" + p.ExternalID + "\x00" + norm
	case "review-request":
		key = "review-request\x00" + p.ExternalID
	case "jira-link":
		key = "jira-link\x00" + p.ExternalID
	default:
		key = kind + "\x00" + p.ExternalID + "\x00" + norm
	}
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}
