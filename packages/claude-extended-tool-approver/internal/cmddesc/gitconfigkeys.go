package cmddesc

import "strings"

// P16: the `git config` KEY fact table (docket tc-o14i5.3, packet
// tc-o14i5.3.9). Per P14 this file holds FACTS only: each key pattern carries
// a fact CLASS, plus value facts that OVERRIDE the key's default class when
// they match. It never returns a verdict; effectpolicy.GitConfigKeyPolicy
// maps a class to one (exec-sink / interlock-disable / push-fetch-redirect /
// trust-widening => Reject, inert-metadata => Approve, unlisted => Abstain,
// --global/--system turns Approve into Abstain).
//
// SEED: the FULL old sink/interlock/redirect table (internal/rules/git/
// git.go, gatedConfigKeys + gatedConfigSections, read-only reference here)
// plus the additional keys P16 names. The old table's classes map as
// configSink/configSinkReject => exec-sink, configInterlock/
// configInterlockReject => interlock-disable, configRedirect =>
// push-fetch-redirect. The old engine's Reject-vs-Ask split for the five
// pg2-3zgcf sinks (core.hooksPath, core.pager, core.sshCommand,
// core.fsmonitor, diff.external, diff.textconv) does not carry forward: P16's
// exec-sink => Reject is uniform. include*.path was an exec-sink-like entry
// in the old table; P16 reclassifies it (and the new safe.directory) as
// trust-widening - a design-decided split, not a judgment call.
//
// Key syntax (git help config, "SYNTAX"): section and variable names are
// case-INSENSITIVE, the optional subsection is case-SENSITIVE. Matching here
// lower-cases the whole key, which can only WIDEN a pattern match (every
// pattern wildcards its subsection), the safe direction.

// GitConfigClass is a P16 fact class. The zero value (GitConfigUnlisted)
// means no class: the policy abstains.
type GitConfigClass string

// The P16 fact classes, plus the alias-shell class for `alias.<n>` whose
// value begins with `!`.
const (
	// GitConfigUnlisted: no table entry (including `alias.*` WITHOUT a
	// leading `!`: an alias can hide a forbidden verb, P16).
	GitConfigUnlisted GitConfigClass = ""
	// GitConfigExecSink: git runs the value, or something under it.
	GitConfigExecSink GitConfigClass = "exec-sink"
	// GitConfigInterlockDisable: the key disables a refusal git makes by default.
	GitConfigInterlockDisable GitConfigClass = "interlock-disable"
	// GitConfigPushFetchRedirect: the key changes where fetches/pushes go.
	GitConfigPushFetchRedirect GitConfigClass = "push-fetch-redirect"
	// GitConfigTrustWidening: the key widens what git (or its config) trusts.
	GitConfigTrustWidening GitConfigClass = "trust-widening"
	// GitConfigInertMetadata: the key (under the matched value fact) cannot
	// name a program, path, remote or trust relaxation.
	GitConfigInertMetadata GitConfigClass = "inert-metadata"
	// GitConfigAliasShell: `alias.<name>` whose value starts with `!` - git
	// runs the rest as a shell command line. The class is not itself a
	// verdict input; the interpreter additionally emits the hidden shell
	// text as a child invocation so it is classified by ITS effects.
	GitConfigAliasShell GitConfigClass = "alias-shell"
)

// GitConfigScope is where a `git config` write lands.
type GitConfigScope string

// The write scopes. P16: --global/--system turn Approve into Abstain (they
// affect every repo); --file is a PATH write, additionally judged as one.
const (
	GitConfigScopeLocal  GitConfigScope = "local"
	GitConfigScopeGlobal GitConfigScope = "global"
	GitConfigScopeSystem GitConfigScope = "system"
	GitConfigScopeFile   GitConfigScope = "file"
)

// gitConfigPattern is one row: section.<any subsection>.variable.
type gitConfigPattern struct {
	section  string
	variable string // "" matches every variable in the section
	class    GitConfigClass
}

// gitConfigPatterns is the key table. Every row matches with OR without a
// subsection (`diff.command` and `diff.<driver>.command` alike), exactly as
// the old table's single lowercase key did. Source for the key names: git
// version 2.54.0, git help config (each variable's own entry) and the old
// engine table cited above.
var gitConfigPatterns = []gitConfigPattern{
	// exec-sink: old table.
	{"core", "hookspath", GitConfigExecSink},
	{"core", "pager", GitConfigExecSink},
	{"core", "editor", GitConfigExecSink},
	{"sequence", "editor", GitConfigExecSink},
	{"core", "sshcommand", GitConfigExecSink},
	{"core", "askpass", GitConfigExecSink},
	{"core", "fsmonitor", GitConfigExecSink},
	{"diff", "external", GitConfigExecSink},
	{"diff", "textconv", GitConfigExecSink},
	{"diff", "command", GitConfigExecSink},
	{"merge", "driver", GitConfigExecSink},
	{"filter", "clean", GitConfigExecSink},
	{"filter", "smudge", GitConfigExecSink},
	{"filter", "process", GitConfigExecSink},
	{"credential", "helper", GitConfigExecSink},
	{"init", "templatedir", GitConfigExecSink},
	{"core", "gitproxy", GitConfigExecSink},
	{"remote", "uploadpack", GitConfigExecSink},
	{"remote", "receivepack", GitConfigExecSink},
	{"uploadpack", "packobjectshook", GitConfigExecSink},
	// `pager.<cmd>` sets one command's pager: the old table gates the WHOLE
	// section so `pager.log` is not a one-word bypass of core.pager.
	{"pager", "", GitConfigExecSink},
	// exec-sink: new in P16. gpg.program / gpg.<fmt>.program (git help
	// config: "gpg.program ... use this custom program instead of 'gpg'",
	// and the gpg.ssh/x509 forms); the same *.program pattern as the rest.
	{"gpg", "program", GitConfigExecSink},

	// interlock-disable: old table.
	{"clean", "requireforce", GitConfigInterlockDisable},
	{"http", "sslverify", GitConfigInterlockDisable},
	{"receive", "denycurrentbranch", GitConfigInterlockDisable},
	{"protocol", "allow", GitConfigInterlockDisable},

	// push-fetch-redirect: old table, plus remote.<n>.push|mirror (git help
	// config: remote.<name>.push is the default refspec set pushed,
	// remote.<name>.mirror makes a push mirror all refs - both change what
	// and where a plain `git push` sends).
	{"url", "insteadof", GitConfigPushFetchRedirect},
	{"url", "pushinsteadof", GitConfigPushFetchRedirect},
	{"remote", "url", GitConfigPushFetchRedirect},
	{"remote", "pushurl", GitConfigPushFetchRedirect},
	{"remote", "push", GitConfigPushFetchRedirect},
	{"remote", "mirror", GitConfigPushFetchRedirect},

	// trust-widening: NOT exec-sink-like any more. include.path /
	// includeIf.<cond>.path pull another file's config in; safe.directory
	// lifts git's ownership refusal (git help config).
	{"include", "path", GitConfigTrustWidening},
	{"includeif", "path", GitConfigTrustWidening},
	{"safe", "directory", GitConfigTrustWidening},
}

// gitBooleanLiterals is ADR 0050's closed boolean spelling set for
// core.fsmonitor ("true/false/1/0/yes/no/on/off"), exact tokens only.
var gitBooleanLiterals = map[string]bool{
	"true": true, "false": true, "1": true, "0": true,
	"yes": true, "no": true, "on": true, "off": true,
}

// gitConfigValueFact is one value fact: (section, variable, value predicate)
// => the class that OVERRIDES the key's default when the predicate matches.
type gitConfigValueFact struct {
	section, variable string
	match             func(value string) bool
	class             GitConfigClass
	// source cites where the fact comes from (P14).
	source string
}

var gitConfigValueFacts = []gitConfigValueFact{
	{
		"core", "fsmonitor", func(v string) bool { return gitBooleanLiterals[v] }, GitConfigInertMetadata,
		"ADR 0050 (pg2-arfw6, git 2.54.0): a boolean value names no hook program; only a non-boolean is a pathname that is executed",
	},
	{
		"core", "editor", func(v string) bool { return v == "true" || v == ":" }, GitConfigInertMetadata,
		"ADR 0047 (pg2-agprs): exact literals `true` and `:` deliberately disable the editor",
	},
	{
		"sequence", "editor", func(v string) bool { return v == "true" || v == ":" }, GitConfigInertMetadata,
		"ADR 0047 (pg2-agprs): exact literals `true` and `:` deliberately disable the editor",
	},
	{
		"rerere", "enabled", func(v string) bool { return v == "true" || v == "false" }, GitConfigInertMetadata,
		"git help config: rerere.enabled is a plain boolean; ADR 0050 pair set (pg2-23z9w)",
	},
	// Restoring a safety interlock to its documented default is inert. git help config:
	// clean.requireForce defaults to true; http.sslVerify defaults to true; receive.denyCurrentBranch
	// defaults to refuse; protocol.<name>.allow=never forbids a transport.
	{
		"clean", "requireforce", func(v string) bool { return v == "true" }, GitConfigInertMetadata,
		"git help config: clean.requireForce defaults to true",
	},
	{
		"http", "sslverify", func(v string) bool { return v == "true" }, GitConfigInertMetadata,
		"git help config: http.sslVerify defaults to true",
	},
	{
		"receive", "denycurrentbranch", func(v string) bool { return v == "refuse" }, GitConfigInertMetadata,
		"git help config: receive.denyCurrentBranch defaults to refuse",
	},
	{
		"protocol", "allow", func(v string) bool { return v == "never" }, GitConfigInertMetadata,
		"git help config: protocol.<name>.allow=never disallows the transport",
	},
}

// splitGitConfigKey splits a lower-cased key into section, subsection and
// variable. ok is false for a key with no section (no dot) or an empty part.
func splitGitConfigKey(key string) (section, sub, variable string, ok bool) {
	first := strings.Index(key, ".")
	last := strings.LastIndex(key, ".")
	if first <= 0 || last == len(key)-1 {
		return "", "", "", false
	}
	section, variable = key[:first], key[last+1:]
	if last > first {
		sub = key[first+1 : last]
	}
	return section, sub, variable, true
}

// LookupGitConfigKey is the P16 lookup: (key, value, scope) -> fact class.
// scope is accepted so callers pass the full write triple, but the CLASS is
// scope-independent by P16: --global/--system is POLICY (it demotes an
// Approve to Abstain in effectpolicy.GitConfigKeyPolicy), not a fact about
// the key. value is the proposed value ("" when none is given).
//
// A value fact that matches OVERRIDES the key's default class
// (core.fsmonitor=true is inert-metadata though core.fsmonitor is an
// exec-sink). `alias.<name>` is GitConfigAliasShell when the value begins
// with `!` and GitConfigUnlisted otherwise.
func LookupGitConfigKey(key, value string, scope GitConfigScope) GitConfigClass {
	_ = scope
	section, _, variable, ok := splitGitConfigKey(strings.ToLower(key))
	if !ok {
		return GitConfigUnlisted
	}
	if section == "alias" {
		if strings.HasPrefix(value, "!") {
			return GitConfigAliasShell
		}
		return GitConfigUnlisted
	}
	for _, vf := range gitConfigValueFacts {
		if vf.section == section && vf.variable == variable && vf.match(value) {
			return vf.class
		}
	}
	for _, p := range gitConfigPatterns {
		if p.section == section && (p.variable == "" || p.variable == variable) {
			return p.class
		}
	}
	return GitConfigUnlisted
}
