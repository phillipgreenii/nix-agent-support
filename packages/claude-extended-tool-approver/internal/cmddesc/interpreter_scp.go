package cmddesc

import (
	"fmt"
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
)

// scpInterpreter dispatches scp's own bespoke positional shape (slice 3ad,
// tc-lc8f item 4i; tc-vn5z item 4 follow-up): scpSchema's Flags table and the
// generic scan/resolve machinery handle every FLAG operand exactly like any
// other schema (so -i/-F's roles fire through the ordinary operand()
// switch), but the POSITIONALS need bespoke handling no PositionalSpec shape
// can express — scp takes one-or-more SOURCES followed by ONE DESTINATION,
// and EACH of those operands (not just a fixed pair, unlike
// kubectlCpInterpreter's exactly-2-positional shape) must be classified
// LOCAL or REMOTE from its OWN text before it means anything.
//
// Classification mirrors production's existing classifier
// (internal/rules/ssh/ssh.go's isRemoteToken) rather than inventing a new
// one: a `:` that appears before any `/` in the token makes it remote
// ([user@]host:path); no colon, or a colon that comes AFTER a `/` (a local
// path that happens to contain a literal colon, e.g. "./file:name"), is
// local. A `scp://host[:port]/path` URI is remote too, for free — its own
// `:` (right after "scp") also precedes the URI's first `/`, so the SAME
// test classifies it correctly; only the host/path EXTRACTION needs
// scheme-aware handling (scpRemoteHostPath). Real scp applies this same
// text-only rule even when the token also happens to name an existing LOCAL
// file (`./a:b` aside — a leading `/` before the colon disambiguates it
// explicitly, per scp(1)'s own "Local file names can be made explicit using
// absolute or relative pathnames to avoid scp treating file names
// containing ':' as host specifiers"), so this interpreter does not
// consult the filesystem to break the tie either.
//
// A REMOTE operand's path effect is stamped Remote DIRECTLY on the effect
// (cmddesc.Effect.Remote's own doc comment covers both producers): unlike
// ssh, where the remote command becomes a separate CHILD invocation in its
// own scope, scp's local and remote operands live on the SAME node — there
// is no remote scope to stamp through, so the interpreter sets the field
// itself. Once set, it reaches effectpolicy.remotePathGuard exactly as a
// builder-stamped one would, and abstains by default under the SAME
// operator ruling ssh's own slice 3aa applied (Phillip, 2026-09-07,
// verbatim, tc-vn5z): "for ssh, abstain for paths should be thr default.
// however, we should allow some way to spexify a list of categorized
// paths." — the categorized-path hook (PolicyContext.RemotePaths) is
// consulted identically, keyed by the SAME host name this interpreter
// resolves.
//
// Access class: the LAST positional is always the destination (a write —
// AccessTruncate, mirroring kubectlCpInterpreter's own "last positional is
// PathTruncate" simplification, itself inherited from cpSchema's documented
// simplification of not resolving whether the destination is an existing
// directory the source lands inside); every other positional is a source
// (a read — AccessRead). `-r` (recursive) does not change this: breadth is
// not a factor in this policy set (effectpolicy.DeleteAccess's own ruling,
// extended here by the same reasoning) — a tree copy gets the SAME per-path
// classification a single-file copy would.
//
// One EffectNet{Direction: NetOutbound} is emitted per DISTINCT remote host
// referenced by ANY operand (source or destination) — Outbound
// unconditionally, mirroring sshInterpreter's own connection effect (see its
// doc comment for why: the SAFE direction, since scp's connection can carry
// local content out regardless of which way a given transfer's files move),
// so — exactly like ssh — NO scp invocation can ever reach a full Approve in
// this slice: NetworkAccess never Permits an outbound effect, vetted host or
// not. This is squarely inside the operator's own "abstain by default"
// ruling; the value is in the per-node marks (a categorized remote path
// flipping to Permitted) and in genuine Reject cases (a LOCAL secret read or
// write, judged by the ordinary local policies exactly as if scp were cp).
//
// A fully DYNAMIC operand (the whole token is a runtime expansion, e.g.
// `"$F"`) cannot be classified local or remote at all — it is emitted as an
// ordinary Dynamic path effect (never assumed remote, never given an
// EffectNet), so the existing "dynamic path is Unknown" policies apply, the
// same fail-closed treatment a dynamic value gets everywhere else in this
// package.
type scpInterpreter struct{}

// Interpret implements Interpreter.
func (scpInterpreter) Interpret(leaf cmdparse.ParsedCommand, schema CommandSchema, ctx Context) Interpretation {
	st := scan(leaf, schema, ctx)
	st.finish()
	if !st.scanned {
		return st.result()
	}
	pos := st.positionals()
	if len(pos) < 2 {
		st.fail("scp needs at least 2 positionals (one or more sources, then a destination), got %d", len(pos))
		return st.result()
	}

	seenHost := map[string]bool{}
	noteHost := func(host string, idx int) {
		if seenHost[host] {
			return
		}
		seenHost[host] = true
		st.effects = append(st.effects, Effect{
			Kind: EffectNet, Direction: NetOutbound, Host: host,
			Source: fmt.Sprintf("arg %d", idx),
			// NetProducer (slice 3ao, tc-8og1 item 4a; tc-hjtb Q1): marks
			// this connection as scp's own, mirroring sshConnection's
			// identical marking — see Effect.NetProducer's own doc comment.
			NetProducer: "scp",
		})
	}

	last := len(pos) - 1
	for i, op := range pos {
		access := AccessRead
		if i == last {
			access = AccessTruncate
		}
		st.scpOperand(op, access, noteHost)
	}
	return st.result()
}

// scpOperand emits the effect for one scp positional operand, given the
// access class its POSITION already determined (source vs destination — see
// scpInterpreter's own doc comment). A fully dynamic token is emitted as a
// Dynamic, local-shaped path effect (never assumed remote — see the type
// doc's own paragraph on this). A REMOTE operand (scpOperandIsRemote) is
// split into (host, path) by scpRemoteHostPath, emitted as a path effect
// stamped Remote directly, and reported to noteHost so the caller emits
// exactly one EffectNet per distinct host. Anything else is an ordinary
// LOCAL path effect, judged by the local policies exactly like cp's own
// positionals.
func (st *interpState) scpOperand(op pendingOp, access PathAccess, noteHost func(host string, idx int)) {
	source := fmt.Sprintf("arg %d", op.idx)
	if st.leaf.ArgIsLiveExpansion(op.idx) {
		st.effects = append(st.effects, Effect{
			Kind: EffectPath, Path: op.tok, Access: access, Dynamic: true,
			Source: source, FromPositional: true,
		})
		return
	}
	if scpOperandIsRemote(op.tok) {
		host, path := scpRemoteHostPath(op.tok)
		if host == "" {
			st.fail("scp: cannot determine the host of %q at %s", op.tok, source)
			return
		}
		st.effects = append(st.effects, Effect{
			Kind: EffectPath, Path: path, Access: access, Source: source,
			FromPositional: true, Remote: host,
		})
		noteHost(host, op.idx)
		return
	}
	st.effects = append(st.effects, Effect{
		Kind: EffectPath, Path: op.tok, Access: access, Source: source,
		FromPositional: true,
	})
}

// scpOperandIsRemote mirrors production's own classifier
// (internal/rules/ssh/ssh.go's isRemoteToken) exactly: a `:` that appears
// before any `/` in tok makes it remote. This also classifies a bare
// `scp://host/path` URI remote for free (its colon, right after "scp",
// precedes the URI's own first `/`), so no separate scheme special case is
// needed HERE — only scpRemoteHostPath's host/path extraction needs to know
// about the scheme.
func scpOperandIsRemote(tok string) bool {
	colon := strings.IndexByte(tok, ':')
	if colon < 0 {
		return false
	}
	slash := strings.IndexByte(tok, '/')
	return slash < 0 || slash >= colon
}

// scpRemoteHostPath extracts (host, path) from a token already known remote
// (scpOperandIsRemote). A leading "scp://" scheme is stripped first so the
// authority/path split happens on the URI's OWN first "/" rather than the
// ordinary "[user@]host:path" colon; either way the host half is handed to
// urlHost (shared with curl/ssh) so "user@host", a bracketed IPv6 literal,
// and a trailing ":port" are all normalised identically to how ssh's own
// host operand is. A missing path segment (a bare "host:" or a schemeless
// "scp://host" with no "/") means the remote HOME directory, per this
// slice's brief — scp itself treats an empty remote path this way. An
// unresolvable host (urlHost's ok is false — an empty or malformed
// authority) reports "" so the caller can fail the interpretation closed,
// exactly like sshInterpreter does when it cannot determine its own host
// operand.
func scpRemoteHostPath(tok string) (host, path string) {
	if rest, ok := strings.CutPrefix(tok, "scp://"); ok {
		hostPart, pathPart := rest, ""
		if i := strings.IndexByte(rest, '/'); i >= 0 {
			hostPart, pathPart = rest[:i], rest[i:]
		}
		h, _ := urlHost(hostPart)
		if pathPart == "" {
			pathPart = "~"
		}
		return h, pathPart
	}
	colon := strings.IndexByte(tok, ':')
	hostPart, pathPart := tok[:colon], tok[colon+1:]
	h, _ := urlHost(hostPart)
	if pathPart == "" {
		pathPart = "~"
	}
	return h, pathPart
}
