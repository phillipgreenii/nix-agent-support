// Package curl is a config-driven MECHANISM for approving read-only (and
// explicitly whitelisted) curl requests. It follows the kubectl/buildtools
// template: the classification logic lives here in ceta-core, and the
// consumer-specific domain DATA (allowed domain suffixes + per-domain HTTP
// methods) arrives via an injected configrules.CurlConfig — the rules.json
// `curl` block, wired in by internal/setup/factory.go.
//
// The rule only ever Approves or Abstains; a non-matching request Abstains
// (defers to Claude). A request is Approved when EVERY URL it targets is
// allowed for its effective HTTP method:
//   - a base generic host (localhost/loopback, well-known GitHub read hosts) or
//     a configured AllowedDomainSuffixes domain, with a read-only method
//     (GET/HEAD); OR
//   - a configured DomainMethods domain whose Methods include the effective
//     method (the mechanism for allowing, e.g., a POST to an internal API).
//
// SAFE DEFAULT: with an empty config only the base generic hosts are approved
// (read-only). Before this wiring the consumer domain list was an empty
// hardcoded slice with no loading path — the live bug this rule fixes: consumer
// domains now flow in from rules.json instead of being unreachable.
package curl

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/rules/configrules"
)

// baseExactHosts lists hostnames allowed via exact match (not suffix match) for
// read-only requests. These are generic developer defaults (loopback + the
// well-known GitHub read endpoints), NOT consumer-specific — consumer domains
// belong in the rules.json `curl` block.
var baseExactHosts = map[string]bool{
	"localhost":                 true,
	"127.0.0.1":                 true,
	"github.com":                true,
	"raw.githubusercontent.com": true,
	"api.github.com":            true,
}

// baseHostSuffixes lists hostname suffixes for localhost-like domains, allowed
// for read-only requests.
var baseHostSuffixes = []string{
	".localhost",
}

// shortBodyFlags are curl short flags that carry a request body / upload (-d
// data, -F form, -T upload-file). Their value may be a separate token (`-d x`)
// or glued to the flag (`-dx`), so they are matched by PREFIX, not exact token
// equality — an earlier exact-only match let glued forms (`-Tfile`, `-dhello`,
// `-Ffield=val`) slip through as GET and get approved to an allowlisted domain.
// This closes that write-method bypass now that WS3 makes the domain list live.
var shortBodyFlags = []string{"-d", "-F", "-T"}

// longBodyFlags are curl long options that carry a request body / upload. Their
// value may be a separate token (`--data x`) or attached with `=`
// (`--data=x`) — the `=` form is likewise matched after stripping the value.
var longBodyFlags = map[string]bool{
	"--data": true, "--data-raw": true, "--data-binary": true,
	"--data-urlencode": true, "--form": true, "--form-string": true,
	"--upload-file": true, "--json": true,
}

// isBodyFlag reports whether tok is a body/upload flag in any of curl's accepted
// spellings (short spaced/glued, or long spaced/`=`).
func isBodyFlag(tok string) bool {
	for _, f := range shortBodyFlags {
		if strings.HasPrefix(tok, f) {
			return true
		}
	}
	name := tok
	if i := strings.IndexByte(tok, '='); i >= 0 {
		name = tok[:i]
	}
	return longBodyFlags[name]
}

type domainMethods struct {
	suffix  string
	methods map[string]bool
}

type Rule struct {
	allowedDomainSuffixes []string
	domainMethods         []domainMethods
}

// New constructs the curl rule from cfg (the rules.json `curl` block). A zero
// cfg yields the base generic hosts only.
func New(cfg configrules.CurlConfig) *Rule {
	r := &Rule{
		allowedDomainSuffixes: cfg.AllowedDomainSuffixes,
	}
	for _, dm := range cfg.DomainMethods {
		methods := make(map[string]bool, len(dm.Methods))
		for _, m := range dm.Methods {
			methods[strings.ToUpper(m)] = true
		}
		r.domainMethods = append(r.domainMethods, domainMethods{suffix: dm.DomainSuffix, methods: methods})
	}
	return r
}

func (r *Rule) Name() string {
	return "curl"
}

func (r *Rule) Evaluate(input *hookio.HookInput) (hookio.RuleResult, error) {
	if input.ToolName != "Bash" {
		return hookio.NotApplicable()
	}
	cmdStr, err := input.BashCommand()
	if err != nil {
		return hookio.RuleResult{}, fmt.Errorf("curl: read bash command: %w", err)
	}
	parsed := cmdparse.Parse(cmdStr)

	foundCurl := false
	for _, pc := range parsed {
		if filepath.Base(pc.Executable) != "curl" {
			continue
		}
		foundCurl = true
		if !r.allURLsAllowed(pc.Args) {
			return hookio.NotApplicable()
		}
	}
	if !foundCurl {
		return hookio.NotApplicable()
	}
	return hookio.RuleResult{
		Decision: hookio.Approve,
		Reason:   "curl: allowed request to allowed domain",
		Module:   r.Name(),
	}, nil
}

// effectiveMethod returns the uppercase HTTP method curl would use: an explicit
// method (-X/--request, spaced `-X POST`, glued `-XPOST`, or `--request=POST`)
// wins; otherwise a request-body/upload flag (in any spelling) implies POST;
// otherwise GET.
func effectiveMethod(args []string) string {
	hasBody := false
	for i, a := range args {
		if a == "-X" || a == "--request" {
			if i+1 < len(args) {
				return strings.ToUpper(args[i+1])
			}
		}
		if m, ok := strings.CutPrefix(a, "--request="); ok {
			return strings.ToUpper(m)
		}
		if strings.HasPrefix(a, "-X") && len(a) > 2 {
			return strings.ToUpper(a[2:])
		}
		if isBodyFlag(a) {
			hasBody = true
		}
	}
	if hasBody {
		return "POST"
	}
	return "GET"
}

// allURLsAllowed returns true when at least one URL argument is present and
// every URL argument is allowed for the command's effective method. Returns
// false if no URL is found (safety: don't approve a curl with no recognisable
// URL).
func (r *Rule) allURLsAllowed(args []string) bool {
	method := effectiveMethod(args)
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
		if !r.hostAllowed(u.Hostname(), method) {
			return false
		}
	}
	return found
}

// hostAllowed reports whether host may be requested with the given uppercase
// HTTP method.
func (r *Rule) hostAllowed(host, method string) bool {
	host = strings.ToLower(host)
	readOnly := method == "GET" || method == "HEAD"

	if readOnly {
		if baseExactHosts[host] {
			return true
		}
		for _, suffix := range baseHostSuffixes {
			if strings.HasSuffix(host, suffix) {
				return true
			}
		}
		for _, dom := range r.allowedDomainSuffixes {
			if matchesDomain(host, dom) {
				return true
			}
		}
	}
	// Per-domain method grants apply to ANY method (including read-only, which is
	// why an all-read-only DomainMethods entry is a valid, if redundant, way to
	// spell an allowed read domain).
	for _, dm := range r.domainMethods {
		if matchesDomain(host, dm.suffix) && dm.methods[method] {
			return true
		}
	}
	return false
}

// matchesDomain reports whether host matches a configured domain entry. An entry
// WITHOUT a leading dot (e.g. "nixos.org") matches the apex itself AND its
// subdomains; an entry WITH a leading dot (e.g. ".internal.example") matches
// subdomains ONLY. The leading-dot form is what prevents a partial-label match
// (e.g. "notnixos.org" must not match "nixos.org").
func matchesDomain(host, entry string) bool {
	if entry == "" {
		return false
	}
	if strings.HasPrefix(entry, ".") {
		return strings.HasSuffix(host, entry)
	}
	return host == entry || strings.HasSuffix(host, "."+entry)
}
