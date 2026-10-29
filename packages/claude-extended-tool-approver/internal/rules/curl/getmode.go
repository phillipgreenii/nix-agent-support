package curl

import (
	"net/url"
	"path/filepath"
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/patheval"
)

func setOf(items ...string) map[string]bool {
	m := make(map[string]bool, len(items))
	for _, it := range items {
		m[it] = true
	}
	return m
}

// curlShortValueFlags are the short options that take an operand; curlShortBoolFlags
// those that do not (curl 8.7.1 `curl --help all`). `-:` (--next) is deliberately in
// neither: it restarts option parsing, so the walk rejects it.
const (
	curlShortValueFlags = "EKCbcdDFPHhmoxUQreXYytzTuAw"
	curlShortBoolFlags  = "aqfGgI0ik46jlLMnNZpJORSs231BvV"
)

// getInvocation is what the strict walk learned about one curl leaf.
type getInvocation struct {
	get        bool // -G / --get seen
	head       bool // -I / --head seen
	method     string
	urls       []string // positionals and --url operands, in order
	outputs    []string // -o / --output operands
	outputDir  string   // --output-dir operand
	remoteName bool     // -O / --remote-name / --remote-name-all
	config     bool     // -K / --config seen
	// blocked names the first option that disqualifies the invocation from the
	// get-mode path (a body that is not query data, a config file, a write-capable
	// or host-redirecting option, ...). Empty when none.
	blocked string
	// fileData is true when a data operand names a local file (@file, or the
	// --data-urlencode name@file form), which get mode refuses to send.
	fileData bool
	// seen records every long option name the walk noted, so the legacy refusal can
	// ask which disqualifying options were present, not just the first.
	seen map[string]bool
}

// getModeDisqualifiers are options that disqualify an invocation from the get-mode
// approval. Each is one of: a request body that -G does NOT turn into a query (-F,
// -T, --json); an option that changes WHERE the request goes so the loopback host
// check no longer describes it (a proxy, a unix socket, --resolve/--connect-to);
// a config file (-K), whose content curl obeys as if typed; follow-redirects (a
// loopback server could bounce curl to any host); an option that writes a file
// other than through -o (cookie jar, header dump, trace, --stderr, --libcurl,
// etag/hsts/alt-svc caches); or --variable/--expand-*, which can read files and
// the environment into the request.
var getModeDisqualifiers = map[string]string{
	"--config": "-K/--config", "--form": "-F/--form", "--form-string": "--form-string",
	"--upload-file": "-T/--upload-file", "--json": "--json",
	"--location": "-L/--location", "--location-trusted": "--location-trusted",
	"--proxy": "-x/--proxy", "--preproxy": "--preproxy", "--socks4": "--socks4", "--socks4a": "--socks4a",
	"--socks5": "--socks5", "--socks5-hostname": "--socks5-hostname",
	"--unix-socket": "--unix-socket", "--abstract-unix-socket": "--abstract-unix-socket",
	"--connect-to": "--connect-to", "--resolve": "--resolve", "--doh-url": "--doh-url",
	"--cookie-jar": "-c/--cookie-jar", "--dump-header": "-D/--dump-header",
	"--trace": "--trace", "--trace-ascii": "--trace-ascii", "--stderr": "--stderr",
	"--libcurl": "--libcurl", "--etag-save": "--etag-save", "--hsts": "--hsts",
	"--alt-svc": "--alt-svc", "--variable": "--variable", "--create-file-mode": "--create-file-mode",
	"--remote-header-name": "-J/--remote-header-name", "--xattr": "--xattr",
}

// shortToLong maps the short options the get-mode logic cares about to their long
// names, so each is handled in one place.
var shortToLong = map[byte]string{
	'G': "--get", 'I': "--head", 'X': "--request", 'o': "--output", 'O': "--remote-name",
	'd': "--data", 'F': "--form", 'T': "--upload-file", 'K': "--config", 'L': "--location",
	'x': "--proxy", 'c': "--cookie-jar", 'D': "--dump-header", 'J': "--remote-header-name",
	'H': "--header", 'w': "--write-out",
}

// parseInvocation walks args with curl's own option grammar. ok is false when
// the walk cannot account for every token (an option missing from the tables,
// `--next`, a value-taking option with no operand), in which case the caller MUST
// fall back to the legacy scan.
func parseInvocation(args []string) (inv getInvocation, ok bool) {
	note := func(long, value string) {
		if inv.seen == nil {
			inv.seen = map[string]bool{}
		}
		inv.seen[long] = true
		switch long {
		case "--config":
			inv.config = true
		case "--get":
			inv.get = true
		case "--head":
			inv.head = true
		case "--request":
			inv.method = strings.ToUpper(value)
		case "--output":
			inv.outputs = append(inv.outputs, value)
		case "--output-dir":
			inv.outputDir = value
		case "--remote-name", "--remote-name-all":
			inv.remoteName = true
		case "--url":
			inv.urls = append(inv.urls, value)
		case "--data", "--data-ascii", "--data-binary":
			if strings.HasPrefix(value, "@") {
				inv.fileData = true
			}
		case "--data-urlencode", "--url-query":
			if urlencodeNamesFile(value) {
				inv.fileData = true
			}
		case "--header", "--write-out":
			if strings.HasPrefix(value, "@") {
				inv.fileData = true
			}
		}
		if why, bad := getModeDisqualifiers[long]; bad && inv.blocked == "" {
			inv.blocked = why
		}
	}

	for i := 0; i < len(args); i++ {
		tok := args[i]
		switch {
		case tok == "--":
			inv.urls = append(inv.urls, args[i+1:]...)
			return inv, true
		case strings.HasPrefix(tok, "--"):
			name, value, hasEq := strings.Cut(tok, "=")
			switch {
			case curlLongValueFlags[name]:
				if !hasEq {
					if i+1 >= len(args) {
						return inv, false
					}
					i++
					value = args[i]
				}
				note(name, value)
			case curlLongBoolFlags[name]:
				if hasEq {
					return inv, false
				}
				note(name, "")
			default:
				return inv, false
			}
		case len(tok) > 1 && tok[0] == '-':
			// A cluster: each letter is a flag until one takes an operand, which is
			// the rest of the token or, when nothing follows, the next token.
			for j := 1; j < len(tok); j++ {
				c := tok[j]
				switch {
				case strings.IndexByte(curlShortValueFlags, c) >= 0:
					value := tok[j+1:]
					if value == "" {
						if i+1 >= len(args) {
							return inv, false
						}
						i++
						value = args[i]
					}
					note(shortToLong[c], value)
					j = len(tok)
				case strings.IndexByte(curlShortBoolFlags, c) >= 0:
					note(shortToLong[c], "")
				default:
					return inv, false
				}
			}
		default:
			inv.urls = append(inv.urls, tok)
		}
	}
	return inv, true
}

// legacyRefusedOptions are the getModeDisqualifiers the legacy scan ALSO refuses
// (pg2-ra5o5s, operator ruling 2026-10-29 on pg2-rh805n item 1): follow-redirects,
// the options that change where the request goes (unix socket, --resolve,
// --connect-to) and the options that write a file other than through -o (cookie
// jar, header dump, trace, --stderr, --libcurl, etag/hsts/alt-svc caches). The
// reasons come from getModeDisqualifiers, so the two paths cannot drift apart.
// Body options (-F, -T, --json) are NOT here: the legacy method logic already
// treats them as a POST, which only a configured DomainMethods grant approves.
var legacyRefusedOptions = []string{
	"--location", "--location-trusted",
	"--unix-socket", "--abstract-unix-socket", "--connect-to", "--resolve",
	"--cookie-jar", "--dump-header", "--trace", "--trace-ascii", "--stderr",
	"--libcurl", "--etag-save", "--hsts", "--alt-svc",
}

// legacyRefusal reports why the legacy scan must refuse an invocation the strict
// walk understood, or "" when it has no objection. It refuses (1) any option in
// legacyRefusedOptions and (2) any URL operand (positional or --url) that does not
// start with http:// or https://: the legacy scan only looks at scheme-prefixed
// arguments, so a second scheme-less URL (`curl http://127.0.0.1/x evil.example`)
// slipped past its host check, and curl would request it over its default scheme.
func legacyRefusal(inv getInvocation) string {
	for _, name := range legacyRefusedOptions {
		if inv.seen[name] {
			return getModeDisqualifiers[name]
		}
	}
	for _, raw := range inv.urls {
		if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
			return "scheme-less URL " + raw
		}
	}
	return ""
}

// urlencodeNamesFile reports whether a --data-urlencode / --url-query operand reads
// a local file. curl's own rule (tool_getparam.c): the operand is `content`,
// `=content`, `name=content`, `@filename` or `name@filename`; the first '=' wins
// and makes the rest literal content, only when there is no '=' does an '@' name a
// file.
func urlencodeNamesFile(operand string) bool {
	if strings.Contains(operand, "=") {
		return false
	}
	return strings.Contains(operand, "@")
}

// loopbackHost reports whether host is the loopback interface or a .localhost
// name: the ONLY hosts get mode approves (the ruling says "still loopback or
// .localhost only"), narrower than the legacy rule's GitHub and configured-domain
// read allowance.
func loopbackHost(host string) bool {
	host = strings.ToLower(host)
	return host == "localhost" || host == "127.0.0.1" || host == "::1" || strings.HasSuffix(host, ".localhost")
}

// getModeApproves reports whether a parsed curl leaf is a get-mode invocation this
// rule approves. It is ADDITIVE ONLY: false means "get mode does not approve it",
// and the caller then applies the legacy scan exactly as before, so no invocation
// the legacy scan approved can lose its approval through this path (the two
// rule-wide refusals in Evaluate, -K and -o, are separate and named there).
func (r *Rule) getModeApproves(inv getInvocation, cwd string) bool {
	if !inv.get {
		return false
	}
	if inv.blocked != "" || inv.fileData {
		return false
	}
	method := inv.method
	if method == "" {
		method = "GET"
		if inv.head {
			method = "HEAD"
		}
	}
	if method != "GET" && method != "HEAD" {
		return false
	}
	if len(inv.urls) == 0 {
		return false
	}
	for _, raw := range inv.urls {
		if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
			return false
		}
		u, err := url.Parse(raw)
		if err != nil || !loopbackHost(u.Hostname()) {
			return false
		}
	}
	return true
}

// outputsWritable reports whether every file curl would write for this invocation
// (-o operands, and the remote-name file -O/--remote-name-all create in the
// working directory or --output-dir) lands where the path evaluator allows a
// write. /dev/null and `-` (stdout) are always fine. Evaluate calls it only when a
// path evaluator is injected; with none, get mode approves only those two.
func (r *Rule) outputsWritable(inv getInvocation, cwd string) bool {
	for _, out := range inv.outputs {
		if out == "-" || out == "/dev/null" {
			continue
		}
		if !r.writable(out, inv.outputDir, cwd) {
			return false
		}
	}
	if inv.remoteName {
		if !r.writable("", inv.outputDir, cwd) {
			return false
		}
	}
	return true
}

// getModeOutputsOK is the output check as get mode applies it: without an injected
// evaluator only /dev/null and `-` pass, never a guessed write zone.
func (r *Rule) getModeOutputsOK(inv getInvocation, cwd string) bool {
	return r.outputsWritable(inv, cwd)
}

func (r *Rule) writable(target, outputDir, cwd string) bool {
	if r.eval == nil {
		return false
	}
	p := target
	switch {
	case filepath.IsAbs(p):
	case outputDir != "":
		p = filepath.Join(absFrom(outputDir, cwd), p)
	default:
		p = filepath.Join(cwd, p)
	}
	return r.eval.Evaluate(p).CanWrite()
}

func absFrom(dir, cwd string) string {
	if filepath.IsAbs(dir) {
		return dir
	}
	return filepath.Join(cwd, dir)
}

// WithEvaluator injects the path evaluator the get-mode output check consults and
// returns the rule. Without it get mode approves only -o /dev/null and -o -.
func (r *Rule) WithEvaluator(pe *patheval.PathEvaluator) *Rule {
	r.eval = pe
	return r
}
