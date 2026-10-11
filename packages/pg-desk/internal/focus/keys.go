package focus

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/ticketkey"
)

// Key resolution for a reply token (spec section 7.2 step 2): a key is
// written in any form pg-desk's typed verbs already resolve. The PR forms are
// re-implemented here because the helpers of the command package are in
// package main and cannot be imported; the two must agree (same accepted
// forms, same refusal of a repository other than the configured one).
//
// Telemetry (D24): this file emits no OpenTelemetry or Prometheus signal and
// logs nothing; it is pure.

var (
	keyDigitsRE = regexp.MustCompile(`^[0-9]+$`)
	// keyPRURLRE matches a PR URL: the OWNER/REPO that precedes the trailing
	// /pull/<N> (with an optional trailing slash).
	keyPRURLRE = regexp.MustCompile(`([^/]+)/([^/]+)/pull/([0-9]+)/?$`)
	// keyPRShortRE matches OWNER/REPO#N and the owner-less #N.
	keyPRShortRE = regexp.MustCompile(`^(?:([^/#\s]+)/([^/#\s]+))?#([0-9]+)$`)
)

const keyRemedy = "write a PR number, a PR URL, OWNER/REPO#N or a tracker key"

// ParseKey resolves raw to the canonical Key: a bare positive integer, a URL
// ending /pull/N and OWNER/REPO#N (or #N) are PR keys, whose id is the
// configured repository, `#` and the number; a string that matches
// bead_id_pattern, or is shaped like a configured ticket key, is an issue key
// and keeps its text (a ticket key that matches only after upper-casing is
// upper-cased). A PR reference that names another repository than the
// configured one is a usage error, so a number is never silently resolved
// against the wrong repository. Every failure is an ErrUsage.
func ParseKey(raw string, cfg *config.Config) (Key, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return Key{}, usageErr(keyRemedy, "empty key")
	}
	if keyDigitsRE.MatchString(s) {
		return prKey(s, "", s, cfg)
	}
	if m := keyPRURLRE.FindStringSubmatch(s); m != nil {
		return prKey(m[3], m[1]+"/"+m[2], s, cfg)
	}
	if m := keyPRShortRE.FindStringSubmatch(s); m != nil {
		named := ""
		if m[1] != "" {
			named = m[1] + "/" + m[2]
		}
		return prKey(m[3], named, s, cfg)
	}
	if cfg != nil {
		if re := cfg.BeadIDRegexp(); re != nil && re.MatchString(s) {
			return Key{Type: entityTypeIssue, ID: s}, nil
		}
		if ticketkey.MatchesShape(s, cfg.TicketPatterns) {
			return Key{Type: entityTypeIssue, ID: s}, nil
		}
		if up := strings.ToUpper(s); up != s && ticketkey.MatchesShape(up, cfg.TicketPatterns) {
			return Key{Type: entityTypeIssue, ID: up}, nil
		}
	}
	return Key{}, usageErr(keyRemedy, "cannot resolve key %q", raw)
}

// prKey builds the PR key of number digits in the configured repository. named
// is the OWNER/REPO the reference itself names, or "".
func prKey(digits, named, raw string, cfg *config.Config) (Key, error) {
	n, err := strconv.Atoi(digits)
	if err != nil || n < 1 {
		return Key{}, usageErr(keyRemedy, "%q is not a positive PR number", raw)
	}
	if cfg == nil || len(cfg.Repos) == 0 || strings.TrimSpace(cfg.Repos[0].Remote) == "" {
		return Key{}, usageErr("set repos in the pg-desk config", "no repository is configured to resolve %q", raw)
	}
	repo := cfg.Repos[0].Remote
	if named != "" && !strings.EqualFold(named, repo) {
		return Key{}, usageErr(
			"name the configured repository or give the bare number",
			"PR reference %q names repository %q, but the only configured repository is %q", raw, named, repo,
		)
	}
	return Key{Type: entityTypePR, ID: repo + "#" + strconv.Itoa(n)}, nil
}
