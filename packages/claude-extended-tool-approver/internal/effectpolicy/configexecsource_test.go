package effectpolicy

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmddesc"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/evalcontract"
)

// TestConfigExecSource pins packet tc-o14i5.3.5 end to end: the effect FACTS
// come from cmddesc, the verdicts only from ConfigExecSourcePolicy and the
// ordinary child/path policies (P14).
//
//   - a source outside the R2 repo-trust boundary, a non-local flake
//     reference, or text the engine cannot interpret => Abstain (P3);
//   - a source inside the boundary, or a command line whose child is inert
//     => Approve;
//   - a source the engine CAN see and whose effects violate policy => Reject.
func TestConfigExecSource(t *testing.T) {
	root, home := fixture(t)
	reg := cmddesc.DefaultRegistry()
	secret := filepath.Join(home, ".aws", "credentials")
	rows := []struct {
		command string
		want    evalcontract.Decision
		note    string
	}{
		// config-source: inside / outside / secret.
		{"treefmt --config-file treefmt.toml", evalcontract.Approve, "inside the repo-trust boundary"},
		{"treefmt --config-file <ROOT>/sub/treefmt.toml", evalcontract.Approve, "absolute, still inside"},
		{"treefmt --config-file /opt/other/treefmt.toml", evalcontract.Abstain, "outside the boundary: code the engine cannot see"},
		{"treefmt --config-file ../elsewhere.toml", evalcontract.Abstain, "relative path that leaves the boundary"},
		{`treefmt --config-file "$CFG"`, evalcontract.Abstain, "runtime value"},
		{"treefmt --config-file " + secret, evalcontract.Reject, "a secret path is visible and forbidden"},
		{"sqlite3 -init " + secret, evalcontract.Reject, "secret path as init script"},
		{"sqlite3 -init init.sql", evalcontract.Approve, "inside the boundary"},
		{"sqlite3 -init /opt/init.sql", evalcontract.Abstain, "outside the boundary"},
		{"sqlite3 -cmd '.shell id'", evalcontract.Abstain, "sqlite3 command text is opaque"},
		{"curl -K " + secret + " https://example.invalid/", evalcontract.Reject, "curl config that is a secret path"},
		{"curl -K /opt/curlrc https://example.invalid/", evalcontract.Abstain, "outside + unvetted host"},
		{"ssh -F /opt/ssh_config host uptime", evalcontract.Abstain, "outside the boundary"},

		// non-local nix refs.
		{"nix develop", evalcontract.Abstain, "interactive shell is unmodeled even for the local flake"},
		{"nix develop github:evil/x#shell", evalcontract.Abstain, "remote flake reference"},
		{"nix shell nixpkgs#hello", evalcontract.Abstain, "registry shorthand"},
		{"nix build .#foo", evalcontract.Approve, "local build is unchanged"},
		{"nix build --post-build-hook ./hook .#foo", evalcontract.Abstain, "daemon-privileged hook is an opaque exec source"},
		{"nix build --option post-build-hook ./hook .#foo", evalcontract.Abstain, "same via --option"},
		{"nix build --option sandbox false .#foo", evalcontract.Abstain, "unmodeled setting"},

		// exec-program: command lines are judged as children.
		{"go test -exec 'echo hi' ./...", evalcontract.Approve, "the wrapper command is inert"},
		{"go test -exec ./runner ./...", evalcontract.Abstain, "unknown wrapper: no spec"},
		{"go test -exec 'git push --force origin main' ./...", evalcontract.Reject, "the wrapper's own effects violate policy"},
		{"go build -toolexec 'git push --force origin main' ./...", evalcontract.Reject, "same via -toolexec"},
		{`go test -exec "$X" ./...`, evalcontract.Abstain, "runtime value"},
		{"ssh -o 'ProxyCommand=git push --force origin main' host uptime", evalcontract.Reject, "ProxyCommand child violates policy"},
		{"ssh -o ProxyCommand='nc %h %p' host uptime", evalcontract.Abstain, "unknown child, and ssh never approves"},
		{"ssh -o LocalCommand=./lc host uptime", evalcontract.Abstain, "unknown child"},
		{"ssh -o PKCS11Provider=/opt/lib.so host uptime", evalcontract.Abstain, "outside the boundary"},

		// git: -c per key via K9's table, --exec-path via the exec-path rule.
		{"git -c core.pager=less status", evalcontract.Reject, "P16 exec-sink"},
		{"git -c core.fsmonitor=/tmp/x status", evalcontract.Reject, "P16 value fact: non-boolean fsmonitor is a program"},
		{"git -c core.fsmonitor=false status", evalcontract.Approve, "P16 inert-metadata"},
		{"git -c user.name=x status", evalcontract.Abstain, "P16 unlisted key"},
		{"git --config-env=core.pager=PAGER status", evalcontract.Reject, "exec-sink key, value from env"},
		{"git --exec-path=<ROOT>/bin status", evalcontract.Approve, "inside the boundary"},
		{"git --exec-path=/opt/evil status", evalcontract.Abstain, "outside the boundary"},
		{"git --exec-path status", evalcontract.Approve, "bare --exec-path takes no value (git prints its exec path), so no exec source is named"},
	}
	for _, r := range rows {
		cmd := strings.ReplaceAll(r.command, "<ROOT>", root)
		resp := Evaluate(evalcontract.Request{Command: cmd, CWD: root, ProjectRoot: root}, reg, DefaultPolicies(), DefaultGraphPolicies())
		if resp.Decision != r.want {
			t.Errorf("%s\n  decision = %s, want %s (%s)\n  reason: %s", r.command, resp.Decision, r.want, r.note, resp.Reason)
		}
	}
}

// TestConfigExecSourceEnv: GIT_EXEC_PATH and SSH_ASKPASS* assignments yield
// an exec-program effect (an EXEC-SOURCE fact, independent of the PATH-hijack
// env allowlist), judged by the same boundary rule.
func TestConfigExecSourceEnv(t *testing.T) {
	root, _ := fixture(t)
	reg := cmddesc.DefaultRegistry()
	eval := func(cmd string) evalcontract.Response {
		return Evaluate(evalcontract.Request{Command: cmd, CWD: root, ProjectRoot: root}, reg, DefaultPolicies(), DefaultGraphPolicies())
	}
	for _, cmd := range []string{"GIT_EXEC_PATH=/opt/evil git status", "SSH_ASKPASS=/opt/ap true", "export GIT_EXEC_PATH=/opt/evil"} {
		resp := eval(cmd)
		found := false
		for _, n := range resp.Interpreted.Nodes {
			for _, e := range n.Effects {
				if e.Kind == cmddesc.EffectExecProgram && strings.Contains(e.Source, "env ") {
					found = true
				}
			}
		}
		if cmd != "export GIT_EXEC_PATH=/opt/evil" && !found {
			t.Errorf("%s: no env exec-program effect in the interpreted graph", cmd)
		}
		if resp.Decision == evalcontract.Approve {
			t.Errorf("%s: approved (%s), want an outside-boundary exec source never approved", cmd, resp.Reason)
		}
	}
}
