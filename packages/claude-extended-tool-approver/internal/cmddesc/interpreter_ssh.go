package cmddesc

import (
	"fmt"
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
)

// sshInterpreter dispatches ssh's own bespoke positional shape (slice 3aa,
// tc-lc8f item 4g; tc-vn5z item 4): sshSchema's Flags table and the generic
// scan/resolve machinery handle every FLAG operand exactly like any other
// schema (so -i/-F/-E's roles below fire through the ordinary operand()
// switch), but the POSITIONALS need bespoke handling no PositionalSpec shape
// can express — the FIRST positional is a network destination (HOST, judged
// by NetworkAccess against VettedHosts, the analogue of production's
// per-host allowlist in internal/rules/ssh), and EVERY REMAINING positional
// is not an operand of ssh's own at all: it is one WORD of the remote
// command, which ssh joins with spaces and hands to the remote login shell —
// ssh(1)'s own DESCRIPTION states this verbatim (see internal/rules/ssh/
// ssh.go's evaluateSSH doc comment, which cites the same behaviour for the
// identical reason: modeling what the remote shell will actually receive,
// not degrading it further). That joined text becomes ONE "shell"-dialect
// ChildInvocation, tagged Remote: host, so effectgraph's builder gives it
// (and everything nested inside it, transitively) a REMOTE scope — see
// build.go's newScope/child.
//
// PositionalsEndOptions (sshSchema) ends ssh's OWN flag scanning at the
// first positional (HOST), so a remote command beginning with a token that
// LOOKS like a flag (`ssh host -rf /`) is never mistaken for an ssh flag —
// getopt's own `+`/POSIXLY_CORRECT convention, the same one xargs/a wrapper
// needs (schema.go's own PositionalsEndOptions doc comment).
type sshInterpreter struct{}

// Interpret implements Interpreter.
func (sshInterpreter) Interpret(leaf cmdparse.ParsedCommand, schema CommandSchema, ctx Context) Interpretation {
	st := scan(leaf, schema, ctx)
	st.finish()
	if !st.scanned {
		return st.result()
	}
	pos := st.positionals()
	if len(pos) == 0 {
		st.fail("ssh: no host operand")
		return st.result()
	}
	st.sshConnection(pos[0], pos[1:])
	return st.result()
}

// sshConnection emits the EffectNet for the connection to hostOp and, when a
// remote command was given, the ChildInvocation carrying it.
func (st *interpState) sshConnection(hostOp pendingOp, remoteWords []pendingOp) {
	live := st.leaf.ArgIsLiveExpansion(hostOp.idx)
	host := hostOp.tok
	dynamic := live
	if !live {
		if h, ok := urlHost(hostOp.tok); ok {
			host = h
		} else {
			st.fail("cannot determine the host of %q at arg %d", hostOp.tok, hostOp.idx)
		}
	}
	// Direction is OUTBOUND, mirroring curl's own upload treatment (slice
	// 3b's curlInterpreter): NetworkAccess (effectpolicy/policy.go) never
	// Permits an outbound effect, vetted host or not — "an upload needs
	// explicit consent in this slice". An ssh session can carry LOCAL
	// content out (piped into its stdin — see sshSchema's Stdin: StdinAlways
	// doc comment and NoContentFlowToUnvettedNetwork's own upstream walk),
	// so Outbound is the SAFE direction, not merely the literal one: judging
	// it Inbound (like curl's read-only GET) would let a genuine
	// `cat secret | ssh host 'cat > file'` upload sail past the one graph
	// policy built to catch exactly this shape. One documented consequence
	// (recorded on the golden cases, not a bug): because Outbound is never
	// Permitted, NO top-level `ssh HOST ...` invocation can ever reach a
	// full Approve in this slice, host vetted or not — the top-level
	// Decision for every ssh golden case tops out at Abstain (or Reject, if
	// some OTHER effect in the graph is Forbidden), which is squarely inside
	// the operator's own "abstain for paths by default" ruling.
	st.effects = append(st.effects, Effect{
		Kind: EffectNet, Direction: NetOutbound, Host: host, Dynamic: dynamic,
		Source: fmt.Sprintf("arg %d", hostOp.idx),
	})
	if len(remoteWords) == 0 {
		st.fail("ssh: no remote command given (interactive session)")
		return
	}
	toks := make([]string, len(remoteWords))
	for i, w := range remoteWords {
		toks[i] = w.tok
	}
	st.children = append(st.children, ChildInvocation{
		Dialect: "shell",
		Program: strings.Join(toks, " "),
		Source:  "ssh " + host,
		Remote:  host,
	})
}
