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
	parsed, err := hookio.LeavesOf(input)
	if err != nil {
		return hookio.RuleResult{}, fmt.Errorf("curl: read bash command: %w", err)
	}

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
//
// EXACT-TOKEN "--request" IS NOT THE pg2-os1kq/pg2-1xq3m BUG CLASS HERE —
// MEASURED NOT AFFECTED. That class needs a long-flag parser that accepts
// unambiguous PREFIX abbreviations (GNU getopt_long / GNU coreutils'
// parse-options); curl's own option parser (lib/tool_getparam.c's findlongopt)
// does not. MEASURED on this tree, 2026-08-18, curl 8.7.1 (`curl 8.7.1
// (x86_64-apple-darwin25.0) libcurl/8.7.1 …`, this machine's `curl`):
// `curl --requ POST -o /dev/null -s http://127.0.0.1:1/` answered `curl: option
// --requ: is unknown` (exit 2) — the IDENTICAL error and exit code as a
// deliberately bogus `curl --zzzzznotaflag`, and identical again for `--req`
// and `--dat` (a would-be abbreviation of `--data`). The full spelling, by
// contrast, is accepted and proceeds to actually try the connection: `curl
// --request POST … http://127.0.0.1:1/` exits 7 ("couldn't connect"), not 2. So
// curl requires an EXACT long-flag spelling with no abbreviation of any length,
// and widening this test to a prefix matcher would be dead code for spellings
// curl itself rejects — effectiveMethod's method-detection risk (an
// unrecognised explicit method silently defaulting to GET/POST-by-body) is not
// reachable through this flag on the curl binary this rule gates on.
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

// curlValueFlags are curl flags (short and long) that consume a SEPARATE
// following token as their value, when spelled with a space rather than glued
// (`-m10`) or `=`-joined (`--max-time=10`). IsReadOnlyToBaseHost needs this to
// tell a flag's VALUE apart from a bare positional URL argument — without it,
// `curl -s -m 10 localhost:9100/metrics` would misread the value "10" as a
// URL candidate (host "10", allowed by neither baseExactHosts nor
// baseHostSuffixes) and refuse the whole request.
var curlValueFlags = map[string]bool{
	"-m": true, "--max-time": true,
	"-o": true, "--output": true,
	"-A": true, "--user-agent": true,
	"-H": true, "--header": true,
	"-e": true, "--referer": true,
	"-b": true, "--cookie": true,
	"-c": true, "--cookie-jar": true,
	"-w": true, "--write-out": true,
	"-x": true, "--proxy": true,
	"-u": true, "--user": true,
	"-X": true, "--request": true,
	"-d": true, "--data": true, "--data-raw": true, "--data-binary": true, "--data-urlencode": true,
	"-F": true, "--form": true, "--form-string": true,
	"-T": true, "--upload-file": true,
	"--connect-timeout": true, "--retry": true, "--retry-delay": true,
	"-K": true, "--config": true,
	"-E": true, "--cert": true,
	"--cacert": true, "--capath": true, "--interface": true, "--limit-rate": true,
}

// IsReadOnlyToBaseHost reports whether args is a curl invocation whose
// effective HTTP method is read-only (GET/HEAD) and whose every positional
// URL argument targets a BASE generic host (loopback/localhost —
// baseExactHosts/baseHostSuffixes; never a consumer AllowedDomainSuffixes or
// DomainMethods host, which need an injected CurlConfig this function does
// not take). It exists so another rule can recognize an EMBEDDED `curl`
// invocation as read-only by reusing (mirroring) this rule's own domain/method
// logic, rather than re-deriving or duplicating it — built for
// rules/ssh's remote-command recognizer (tc-heokt; evidence: `curl -s
// localhost:9100/metrics` and `curl -s -m 10 localhost:9100/metrics` inside
// an ssh remote command, tc-w3xl7).
//
// Unlike allURLsAllowed/hostAllowed above, a positional argument need NOT
// carry an explicit "http://"/"https://" prefix to be read as a URL: curl
// itself defaults a schemeless positional argument to http:// (curl(1)'s URL
// section), and the ssh-embedded evidence this function was built for is
// exactly that schemeless shorthand (`localhost:9100/metrics`, no scheme).
// curlValueFlags lets the scan skip a flag's OWN value token so it is never
// misread as a positional URL.
func IsReadOnlyToBaseHost(args []string) bool {
	method := effectiveMethod(args)
	if method != "GET" && method != "HEAD" {
		return false
	}
	found := false
	skipNext := false
	for _, a := range args {
		if skipNext {
			skipNext = false
			continue
		}
		if strings.HasPrefix(a, "-") {
			if curlValueFlags[a] {
				skipNext = true
			}
			continue
		}
		host, ok := baseHostCandidate(a)
		if !ok {
			continue
		}
		found = true
		if !baseHostAllowed(host) {
			return false
		}
	}
	return found
}

// baseHostCandidate extracts the lowercased hostname from a curl positional
// argument, treating it as a URL whether or not it carries an explicit
// scheme (see IsReadOnlyToBaseHost's own doc for why the schemeless form
// matters here). ok is false when the argument does not parse as a URL with
// a non-empty host at all.
func baseHostCandidate(arg string) (host string, ok bool) {
	if arg == "" {
		return "", false
	}
	target := arg
	if !strings.HasPrefix(target, "http://") && !strings.HasPrefix(target, "https://") {
		target = "http://" + target
	}
	u, err := url.Parse(target)
	if err != nil || u.Hostname() == "" {
		return "", false
	}
	return strings.ToLower(u.Hostname()), true
}

// baseHostAllowed reports whether host is one of the BASE generic hosts
// (loopback/localhost) — the same tier hostAllowed grants read-only requests
// with no consumer config at all.
func baseHostAllowed(host string) bool {
	if baseExactHosts[host] {
		return true
	}
	for _, suffix := range baseHostSuffixes {
		if strings.HasSuffix(host, suffix) {
			return true
		}
	}
	return false
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
