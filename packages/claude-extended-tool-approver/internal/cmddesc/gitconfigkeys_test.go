package cmddesc

import "testing"

func TestLookupGitConfigKey(t *testing.T) {
	for _, tc := range []struct {
		key, value string
		want       GitConfigClass
	}{
		{"core.hooksPath", "x", GitConfigExecSink},
		{"CORE.HOOKSPATH", "x", GitConfigExecSink},
		{"core.fsmonitor", "/bin/hook", GitConfigExecSink},
		{"core.fsmonitor", "true", GitConfigInertMetadata},
		{"core.fsmonitor", "off", GitConfigInertMetadata},
		{"core.editor", "true", GitConfigInertMetadata},
		{"core.editor", ":", GitConfigInertMetadata},
		{"core.editor", "vim", GitConfigExecSink},
		{"diff.external", "x", GitConfigExecSink},
		{"diff.foo.command", "x", GitConfigExecSink},
		{"diff.foo.textconv", "x", GitConfigExecSink},
		{"merge.foo.driver", "x", GitConfigExecSink},
		{"filter.foo.process", "x", GitConfigExecSink},
		{"credential.https://h.helper", "x", GitConfigExecSink},
		{"gpg.program", "x", GitConfigExecSink},
		{"gpg.ssh.program", "x", GitConfigExecSink},
		{"pager.log", "less", GitConfigExecSink},
		{"clean.requireForce", "false", GitConfigInterlockDisable},
		{"clean.requireForce", "true", GitConfigInertMetadata},
		{"http.https://h.sslVerify", "false", GitConfigInterlockDisable},
		{"receive.denyCurrentBranch", "updateInstead", GitConfigInterlockDisable},
		{"protocol.ext.allow", "always", GitConfigInterlockDisable},
		{"url.https://h/.insteadOf", "x", GitConfigPushFetchRedirect},
		{"url.https://h/.pushInsteadOf", "x", GitConfigPushFetchRedirect},
		{"remote.o.url", "x", GitConfigPushFetchRedirect},
		{"remote.o.pushurl", "x", GitConfigPushFetchRedirect},
		{"remote.o.push", "x", GitConfigPushFetchRedirect},
		{"remote.o.mirror", "true", GitConfigPushFetchRedirect},
		{"remote.o.uploadpack", "x", GitConfigExecSink},
		{"remote.o.receivepack", "x", GitConfigExecSink},
		{"include.path", "x", GitConfigTrustWidening},
		{"includeIf.gitdir:/x/.path", "y", GitConfigTrustWidening},
		{"safe.directory", "*", GitConfigTrustWidening},
		{"rerere.enabled", "true", GitConfigInertMetadata},
		{"rerere.enabled", "x", GitConfigUnlisted},
		{"alias.x", "!rm -rf /", GitConfigAliasShell},
		{"alias.x", "log", GitConfigUnlisted},
		{"user.name", "x", GitConfigUnlisted},
		{"nodots", "x", GitConfigUnlisted},
		{".bad", "x", GitConfigUnlisted},
		{"bad.", "x", GitConfigUnlisted},
	} {
		if got := LookupGitConfigKey(tc.key, tc.value, GitConfigScopeLocal); got != tc.want {
			t.Errorf("LookupGitConfigKey(%q, %q) = %q, want %q", tc.key, tc.value, got, tc.want)
		}
		// The class is scope-independent: scope is policy (P16).
		if got := LookupGitConfigKey(tc.key, tc.value, GitConfigScopeGlobal); got != tc.want {
			t.Errorf("LookupGitConfigKey(%q, %q, global) = %q, want %q (scope must not change the class)", tc.key, tc.value, got, tc.want)
		}
	}
}
