package curl

import (
	"net/url"
	"path/filepath"
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

// allowedDomainSuffixes lists domain suffixes whose endpoints may be fetched
// without user confirmation. Matched as hostname suffix (with leading dot to
// avoid partial-label matches, e.g. "evil-zr.org" must not match ".zr.org").
var allowedDomainSuffixes = []string{
	".zr.org",
	".ziprecruiter.com",
	".zipaws.com",
}

// allowedExactHosts lists hostnames that are allowed via exact match
// (not suffix match), such as localhost and loopback addresses.
var allowedExactHosts = []string{
	"localhost",
	"127.0.0.1",
	"github.com",
	"raw.githubusercontent.com",
	"api.github.com",
}

// allowedHostSuffixes lists hostname suffixes for localhost-like domains.
var allowedHostSuffixes = []string{
	".localhost",
}

// writeFlagSet contains curl flags that imply a non-read-only request (POST,
// PUT, PATCH, DELETE, or upload). The presence of any of these causes the rule
// to abstain so the user is prompted.
var writeFlagSet = map[string]bool{
	"-d": true, "--data": true, "--data-raw": true,
	"--data-binary": true, "--data-urlencode": true,
	"-F": true, "--form": true, "--form-string": true,
	"-T": true, "--upload-file": true,
	"--json": true,
}

type Rule struct{}

func New() *Rule {
	return &Rule{}
}

func (r *Rule) Name() string {
	return "curl"
}

func (r *Rule) Evaluate(input *hookio.HookInput) hookio.RuleResult {
	if input.ToolName != "Bash" {
		return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
	}
	cmdStr, err := input.BashCommand()
	if err != nil {
		return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
	}
	parsed := cmdparse.Parse(cmdStr)

	foundCurl := false
	for _, pc := range parsed {
		if filepath.Base(pc.Executable) != "curl" {
			continue
		}
		foundCurl = true
		if !isReadOnly(pc.Args) {
			return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
		}
		if !allURLsAllowed(pc.Args) {
			return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
		}
	}
	if !foundCurl {
		return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
	}
	return hookio.RuleResult{
		Decision: hookio.Approve,
		Reason:   "curl: read-only request to allowed domain",
		Module:   r.Name(),
	}
}

// isReadOnly returns false if any arg signals a write operation.
func isReadOnly(args []string) bool {
	for i, a := range args {
		if writeFlagSet[a] {
			return false
		}
		// -X POST / --request DELETE etc.
		if a == "-X" || a == "--request" {
			if i+1 < len(args) {
				method := strings.ToUpper(args[i+1])
				if method != "GET" && method != "HEAD" {
					return false
				}
			}
		}
		// combined form: -XPOST
		if strings.HasPrefix(a, "-X") && len(a) > 2 {
			method := strings.ToUpper(a[2:])
			if method != "GET" && method != "HEAD" {
				return false
			}
		}
	}
	return true
}

// allURLsAllowed returns true when every URL argument targets an allowed domain.
// Returns false if a URL targets a non-allowed domain, or if no URL is found
// (safety: don't approve a curl with no recognisable URL).
func allURLsAllowed(args []string) bool {
	found := false
	for _, a := range args {
		if !strings.HasPrefix(a, "http://") && !strings.HasPrefix(a, "https://") {
			continue
		}
		found = true
		u, err := url.Parse(a)
		if err != nil {
			return false
		}
		if !isDomainAllowed(u.Hostname()) {
			return false
		}
	}
	return found
}

func isDomainAllowed(host string) bool {
	host = strings.ToLower(host)
	for _, exact := range allowedExactHosts {
		if host == exact {
			return true
		}
	}
	for _, suffix := range allowedDomainSuffixes {
		if strings.HasSuffix(host, suffix) {
			return true
		}
	}
	for _, suffix := range allowedHostSuffixes {
		if strings.HasSuffix(host, suffix) {
			return true
		}
	}
	return false
}
