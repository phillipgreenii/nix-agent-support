package cmddesc

import (
	"fmt"
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
)

// kubectlInterpreter dispatches kubectl's global flags and subcommand exactly
// like the generic Subcommands mechanism (interpretSubcommand) — reusing
// scanGlobal/LookupInterpreter/applyTransform unchanged — but ALSO bridges
// the ONE piece of information a kube-context-bearing EffectRemote needs
// that interpretSubcommand's own generic dispatch has no channel for: the
// --context flag's VALUE. --context is a GLOBAL flag (scanned by THIS
// schema, before the subcommand token); the EffectRemote a subcommand
// declares (registry_breadth.go's kubectlRemoteVerb/kubectlManifestVerb/
// kubectlExecClassVerb, all via ImplicitEffect{RemoteFamily: "kubectl"}, plus
// kubectlCpInterpreter's own manual one) is built by a SEPARATE Interpret
// call, one level down, with no visibility into what the PARENT scanned.
// This wrapper runs the parent scan itself, then patches Resource/Dynamic on
// every Family=="kubectl" EffectRemote the subcommand produced (however
// deep — "config X" nests one level further through the ordinary
// interpretSubcommand recursion, which this wrapper does not otherwise
// change) with the context it found.
//
// Operator ruling (Phillip, 2026-09-07, verbatim, recorded on bead tc-vn5z,
// item 3): "kubectl should be configured to vary per context. ie, there
// could be a "dev" cluster which would allow most anythkng vs a "prod"
// which could be more restricted." Normalized: the policy
// (effectpolicy.KubeContextPolicy) needs the ACTUAL context name; this is
// where it is read off the command line. When the context cannot be
// determined statically — no --context at all (a bare `kubectl get pods`,
// or `--server`/`--cluster` naming the cluster some OTHER way), or its
// value is itself a runtime expansion — Resource stays "" (or Dynamic is
// set), which KubeContextPolicy reads as Unknown/Abstain, per the ruling's
// own worked example implying a context-aware default rather than a
// blanket read permission.
type kubectlInterpreter struct{}

// Interpret implements Interpreter.
func (kubectlInterpreter) Interpret(leaf cmdparse.ParsedCommand, schema CommandSchema, ctx Context) Interpretation {
	st, subIdx := scanGlobal(leaf, schema, ctx)
	if !st.scanned {
		return st.result()
	}
	if subIdx < 0 {
		st.fail("no subcommand given")
		return st.result()
	}
	if leaf.ArgIsLiveExpansion(subIdx) {
		st.fail("subcommand token at arg %d is a runtime expansion", subIdx)
		return st.result()
	}
	name := leaf.Args[subIdx]
	start := subIdx + 1
	subSchema, ok := schema.Subcommands[name]
	if !ok {
		st.fail("unmodeled subcommand %s", name)
		return st.result()
	}
	subIn, ok := LookupInterpreter(subSchema.Interpreter)
	if !ok {
		st.fail("unknown interpreter %s for subcommand %s", subSchema.Interpreter, name)
		return st.result()
	}

	// Global flag OPERANDS (only --kubeconfig's PathRead in this schema) are
	// resolved here — the plain interpretSubcommand dispatch never calls
	// resolve()/operand() on its own parent scan at all (CommandSchema's own
	// doc comment: a Subcommands-shaped schema's Positionals/Stdin/Stdout/
	// ImplicitEffects fields are UNUSED), which is harmless for every
	// PRE-EXISTING Subcommands schema (none gives a global flag a real path
	// role), but --kubeconfig FILE needs a genuine PathRead effect. Calling
	// resolve() here is safe specifically BECAUSE kubectlSchema's own
	// Positionals/ImplicitEffects/Stdin/Stdout stay at their zero value (no
	// positional slots to misresolve, nothing implicit or stdio-shaped to
	// double-emit).
	st.resolve()

	childArgs := append([]string(nil), leaf.Args[start:]...)
	childLive := make([]bool, len(childArgs))
	for i := range childArgs {
		childLive[i] = leaf.ArgIsLiveExpansion(start + i)
	}
	childLeaf := cmdparse.ParsedCommand{Executable: name, Args: childArgs, ArgLiveExpansion: childLive}
	sub := subIn.Interpret(childLeaf, subSchema, ctx)

	effects := append(append([]Effect(nil), st.effects...), sub.Effects...)
	for _, t := range st.transforms {
		var ok bool
		effects, ok = applyTransform(t, effects)
		if !ok {
			st.fail("unrecognised effect transform %d", t.Kind)
			break
		}
	}

	contextName, contextDynamic := kubectlContextValue(st)
	for i := range effects {
		if effects[i].Kind == EffectRemote && effects[i].Family == "kubectl" {
			effects[i].Resource = contextName
			effects[i].Dynamic = contextDynamic
		}
	}

	insufficiency := st.insuff
	if insufficiency == "" {
		insufficiency = sub.Insufficiency
	}
	return Interpretation{
		Effects:       effects,
		Children:      sub.Children,
		Sufficient:    st.insuff == "" && sub.Sufficient,
		Insufficiency: insufficiency,
	}
}

// kubectlContextValue reads the --context global flag's value as scanned by
// scanGlobal: the LAST occurrence wins (ordinary CLI convention); absent
// entirely, name is "" — Unknown to KubeContextPolicy regardless of whether
// --server/--cluster named the cluster some other way (this interpreter
// never reads THEIR values for context purposes, so their mere presence
// changes nothing here, which is exactly the ruling's "--server/--cluster
// make the context Unknown unless --context is also given").
func kubectlContextValue(st *interpState) (name string, dynamic bool) {
	vals := st.flagValues("--context")
	if len(vals) == 0 {
		return "", false
	}
	last := vals[len(vals)-1]
	return last.tok, st.leaf.ArgIsLiveExpansion(last.idx)
}

// kubectlManifestInterpreter runs the ordinary generic scan (so -f/-k's
// PathRead, --dry-run's TransformDryRun and every inert flag all work
// exactly as schema data says) and then special-cases exactly one shape the
// generic operand() cannot: `-f -`/`--filename -` reads STANDARD INPUT, not
// a file literally named "-" (operand()'s own StdinToken convention only
// ever fires for a POSITIONAL path operand — cat/head/wc/sort/tail's own
// "-" — never a FLAG's value, and broadening that shared check would wrongly
// swallow sort's unrelated `-o -` "write to stdout" spelling, so this is a
// deliberately LOCAL, kubectl-only fix instead of a generic interpreter
// change). Covers `kubectl apply -f -` / `kubectl diff -f -` etc. (brief
// point 1's "Also `kubectl apply -f -` reads stdin").
type kubectlManifestInterpreter struct{}

// Interpret implements Interpreter.
func (kubectlManifestInterpreter) Interpret(leaf cmdparse.ParsedCommand, schema CommandSchema, ctx Context) Interpretation {
	st := scan(leaf, schema, ctx)
	stdinFromFile := false
	kept := st.ops[:0]
	for _, op := range st.ops {
		if !op.positional && (op.flag == "-f" || op.flag == "--filename") &&
			op.tok == "-" && !leaf.ArgIsLiveExpansion(op.idx) {
			stdinFromFile = true
			continue
		}
		kept = append(kept, op)
	}
	st.ops = kept
	st.finish()
	res := st.result()
	if stdinFromFile {
		res.Effects = append(res.Effects, Effect{Kind: EffectStdio, Stream: StreamStdin, Source: "-f -"})
	}
	return res
}

// kubectlCpInterpreter models `kubectl cp`'s two positionals (source,
// destination) directly off the raw argv rather than through
// PositionalSpec/OperandRole: EITHER may be a REMOTE `[namespace/]pod:path`
// (kubectl's own colon convention) or a LOCAL filesystem path, and which one
// is which can only be told apart by inspecting the token's own text — not
// something a static Leading/Trailing role table can express. The LOCAL
// operand is modeled as a real PathRead (source, index 0) or PathTruncate
// (destination, index 1) effect — the brief's `kubectl --context dev cp
// pod:/etc/x ~/.ssh/id_rsa` golden needs the local secret-path write judged
// Forbidden — but the interpretation is UNCONDITIONALLY marked insufficient
// regardless (the remote pod:path operand's own semantics — which container
// runtime, which pod — are not modeled at all, matching the exec-class
// family's "leaf is insufficient/Unknown" treatment, brief point 1). An
// insufficient interpretation still carries the effects it understood
// (Interpretation's own doc comment), so a Forbidden local write still wins
// the fold (judgeNode's Forbidden-outranks-Insufficient precedence,
// consistent with slice 3v's bash_c_reject_beats_insufficient).
type kubectlCpInterpreter struct{}

// Interpret implements Interpreter.
func (kubectlCpInterpreter) Interpret(leaf cmdparse.ParsedCommand, schema CommandSchema, ctx Context) Interpretation {
	st := scan(leaf, schema, ctx)
	if !st.scanned {
		return st.result()
	}
	pos := st.positionals()
	if len(pos) != 2 {
		st.fail("kubectl cp needs exactly 2 positionals (source, destination), got %d", len(pos))
		return st.result()
	}
	for i, op := range pos {
		if strings.Contains(op.tok, ":") {
			continue // remote [namespace/]pod:path operand — not modeled, see type doc
		}
		access := AccessRead
		if i == 1 {
			access = AccessTruncate
		}
		st.effects = append(st.effects, Effect{
			Kind:           EffectPath,
			Path:           op.tok,
			Access:         access,
			Dynamic:        leaf.ArgIsLiveExpansion(op.idx),
			Source:         fmt.Sprintf("arg %d", op.idx),
			FromPositional: true,
		})
	}
	st.effects = append(st.effects, Effect{Kind: EffectRemote, Operation: "exec", Family: "kubectl"})
	st.fail("kubectl cp's remote pod:path operand and container-runtime semantics are not modeled (documented follow-up)")
	return st.result()
}
