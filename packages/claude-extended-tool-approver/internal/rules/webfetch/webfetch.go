package webfetch

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

var allowedDomains = []string{
	"github.com",
	"raw.githubusercontent.com",
	"api.github.com",
	"objects.githubusercontent.com",
	"registry.npmjs.org",
	"docs.anthropic.com",
	"code.claude.com",
	"marketplace.visualstudio.com",
	"nodejs.org",
	"developer.mozilla.org",
	"pkg.go.dev",
	"pypi.org",
	"crates.io",
	"nixos.org",
	"wiki.nixos.org",
}

type Rule struct{}

func New() *Rule {
	return &Rule{}
}

func (r *Rule) Name() string {
	return "webfetch"
}

func (r *Rule) Evaluate(input *hookio.HookInput) (hookio.RuleResult, error) {
	if input.ToolName != "WebFetch" {
		return hookio.NotApplicable()
	}
	uStr := input.WebFetchURL()
	if uStr == "" {
		return hookio.NotApplicable()
	}
	u, err := url.Parse(uStr)
	if err != nil {
		return hookio.RuleResult{}, fmt.Errorf("webfetch: parse url: %w", err)
	}
	host := strings.ToLower(u.Hostname())

	if !matchesDomain(host) {
		return hookio.NotApplicable()
	}

	// GitHub-specific: approve most pages, block release binary downloads
	if host == "github.com" {
		path := strings.Trim(u.Path, "/")
		segments := strings.Split(path, "/")
		// Block release binary downloads (e.g. /owner/repo/releases/download/v1.0/binary.tar.gz)
		if len(segments) >= 5 && segments[2] == "releases" && segments[3] == "download" {
			// Not applicable (ADR 0043): the chain must continue. Former Reason,
			// kept because it is the only record of WHY: "webfetch: GitHub release binary download (deferred to claude-code)"
			return hookio.NotApplicable()
		}
		return hookio.RuleResult{
			Decision: hookio.Approve,
			Reason:   "webfetch: GitHub page",
			Module:   r.Name(),
		}, nil
	}

	return hookio.RuleResult{
		Decision: hookio.Approve,
		Reason:   "webfetch: approved domain " + host,
		Module:   r.Name(),
	}, nil
}

func matchesDomain(host string) bool {
	for _, d := range allowedDomains {
		if host == d || strings.HasSuffix(host, "."+d) {
			return true
		}
	}
	return false
}
