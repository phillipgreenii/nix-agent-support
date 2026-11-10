package interpret

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
)

// The interpretation stamp (docs/behavior/pg-desk/reinterpret.md, "The
// stamp"): the string `<build version>/<config digest>` stored on every
// interpretation row to say which build and which interpretation-relevant
// configuration computed it.

// StampConfig is the PROJECTION of config.Config that the digest covers:
// exactly the fields this package reads from the configuration, and nothing
// else. In particular it excludes Config.Path (the discovered file path, which
// differs between the heartbeat, a router role and the operator's shell and
// would make the stamp flap) and every field interpret does not read, so an
// unrelated configuration edit does not re-run everything.
//
// Every field has omitempty, so a nil and an empty list digest alike, and the
// canonical form is encoding/json's (struct fields in declaration order, map
// keys sorted), so a reordered or re-spaced configuration file digests the
// same. TestStampConfig_CoversEveryFieldInterpretReads fails when interpret
// starts reading a configuration field this struct does not hold.
type StampConfig struct {
	SelfLogin          string                          `json:"self_login,omitempty"`
	SelfIssueOwner     string                          `json:"self_issue_owner,omitempty"`
	TeamMembers        []string                        `json:"team_members,omitempty"`
	WatchLabels        []string                        `json:"watch_labels,omitempty"`
	ApproverAllowlist  []string                        `json:"approver_allowlist,omitempty"`
	VerdictGenerations []config.VerdictGeneration      `json:"verdict_generations,omitempty"`
	CheckInterpreters  []config.CheckInterpreterConfig `json:"check_interpreters,omitempty"`
	ReviewExemptChecks []string                        `json:"review_exempt_checks,omitempty"`
	Jira               *config.JiraConfig              `json:"jira,omitempty"`
	CategoryVocabulary map[string][]string             `json:"category_vocabulary,omitempty"`
	Urgency            *config.UrgencyConfig           `json:"urgency,omitempty"`

	// InProgressStatuses and DoneStatuses are the RESOLVED values of the
	// methods config.Config.InProgressStatuses and DoneStatuses (the
	// configured list, or the default when none is configured).
	InProgressStatuses []string `json:"in_progress_statuses,omitempty"`
	DoneStatuses       []string `json:"done_statuses,omitempty"`
}

// NewStampConfig projects cfg. A nil cfg projects the zero configuration
// (with the defaulted status lists), as interpretation itself treats it.
func NewStampConfig(cfg *config.Config) StampConfig {
	p := StampConfig{
		InProgressStatuses: cfg.InProgressStatuses(),
		DoneStatuses:       cfg.DoneStatuses(),
	}
	if cfg == nil {
		return p
	}
	p.SelfLogin = cfg.SelfLogin
	p.SelfIssueOwner = cfg.SelfIssueOwner
	p.TeamMembers = cfg.TeamMembers
	p.WatchLabels = cfg.WatchLabels
	p.ApproverAllowlist = cfg.ApproverAllowlist
	p.VerdictGenerations = cfg.VerdictGenerations
	p.CheckInterpreters = cfg.CheckInterpreters
	p.ReviewExemptChecks = cfg.ReviewExemptChecks
	p.Jira = cfg.Jira
	p.CategoryVocabulary = cfg.CategoryVocabulary
	p.Urgency = cfg.Urgency
	return p
}

// ConfigDigest is the first eight hexadecimal characters of the SHA-256 of the
// canonical JSON of the projection of cfg.
func ConfigDigest(cfg *config.Config) string {
	// json.Marshal cannot fail for this struct: every field is a string, a
	// list, a map keyed by string, or a config struct of the same kinds.
	b, _ := json.Marshal(NewStampConfig(cfg))
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:8]
}

// Stamp returns `<version>/<config digest>`. version is the build version
// (main.Version, a content digest of the package, or "dev" for `go run` and a
// test build, which therefore stamps `dev/<digest>`); it is passed in so this
// package does not depend on main.
func Stamp(version string, cfg *config.Config) string {
	return version + "/" + ConfigDigest(cfg)
}
