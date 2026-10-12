package effectpolicy

import "github.com/phillipgreenii/claude-extended-tool-approver/internal/cmddesc"

// GitConfigKeyPolicy is P16's POLICY half (docket tc-o14i5.3, packet
// tc-o14i5.3.9): it maps the FACT class cmddesc.LookupGitConfigKey assigned
// a `git config` write (Effect.ConfigClass) onto a verdict. Per P14 the
// verdict comes only from here, never from the table.
//
//   - exec-sink / interlock-disable / push-fetch-redirect / trust-widening
//     => Forbidden (Reject);
//   - inert-metadata => Permitted (Approve) - EXCEPT that --global/--system
//     scope turns that Approve into Unknown (Abstain): the write affects
//     every repository, not just this one;
//   - alias-shell (`alias.<n>` = `!cmd`) => Permitted at repo scope, because
//     the hidden command line is judged as its own child (cmddesc emits it),
//     and Unknown at --global/--system like any other approve;
//   - unlisted key (including `alias.*` without `!`: an alias can hide a
//     forbidden verb) => Unknown (Abstain).
//
// `git config --file PATH` is additionally a path WRITE judged by P5
// (the schema gives --file's operand the PathModify role), so this policy
// does not itself need to judge the path.
type GitConfigKeyPolicy struct{}

// Name implements Policy.
func (GitConfigKeyPolicy) Name() string { return "git-config-key" }

// Judge implements Policy.
func (GitConfigKeyPolicy) Judge(e cmddesc.Effect, _ PolicyContext) (Finding, bool) {
	if e.Kind != cmddesc.EffectConfigKey {
		return Finding{}, false
	}
	if e.Dynamic {
		return Finding{Verdict: Unknown, Reason: "git config key or value is a runtime expansion"}, true
	}
	key := e.ConfigKey
	switch e.ConfigClass {
	case cmddesc.GitConfigExecSink, cmddesc.GitConfigInterlockDisable, cmddesc.GitConfigPushFetchRedirect, cmddesc.GitConfigTrustWidening:
		return Finding{Verdict: Forbidden, Reason: "git config " + key + " is " + string(e.ConfigClass) + " (P16)"}, true
	case cmddesc.GitConfigInertMetadata, cmddesc.GitConfigAliasShell:
		switch e.ConfigScope {
		case cmddesc.GitConfigScopeGlobal, cmddesc.GitConfigScopeSystem:
			return Finding{Verdict: Unknown, Reason: "git config " + key + " at " + string(e.ConfigScope) + " scope affects every repository"}, true
		}
		return Finding{Verdict: Permitted, Reason: "git config " + key + " is " + string(e.ConfigClass) + " (P16)"}, true
	default:
		return Finding{Verdict: Unknown, Reason: "git config key " + key + " has no class (P16)"}, true
	}
}
