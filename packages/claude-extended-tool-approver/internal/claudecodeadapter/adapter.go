// Package claudecodeadapter is the Claude Code adapter package tc-8og1 item
// 7 (second half) calls for: it converts a real Claude Code
// hookio.HookInput into the effect-graph spike's evalcontract.Request/
// evalcontract.Response shape.
//
// A Bash HookInput is the easy case — its tool_input.command IS shell text,
// so it maps onto evalcontract.Request.Command unchanged and flows through
// effectpolicy.Evaluate exactly as any other spike caller's shell command
// does (internal/cmdparse builds the graph from the parse, same as always).
//
// A Write/Edit/MultiEdit HookInput is NOT shell text — it is a structured
// tool call carrying a file path and content directly, and cmdparse would
// never be asked to parse it. The bead's explicit requirement is that this
// package represent such a call as a ONE-NODE graph (BuildFileToolGraph)
// rather than inventing a parallel, tool-specific judgment path: the one
// node carries exactly the cmddesc.Effect a shell command touching the same
// path would carry, and is folded to a Decision by
// effectpolicy.EvaluateGraph — the IDENTICAL node/graph-policy fold a shell
// command's multi-node graph goes through (see that function's own doc
// comment). This is what makes file policies (NoWriteToSecretPath,
// NoWriteToReadOnlyPath, DeleteAccess, the deletable.Kind.Secrecy
// classification, ...) single-sourced between a Write/Edit tool call and an
// equivalent shell command (`echo x > path`, `sed -i ... path`): both
// become one cmddesc.Effect judged by the same Policy.Judge implementations,
// never two parallel policy trees that could silently drift apart.
//
// Import-cycle note: internal/effectpolicy, internal/effectgraph,
// internal/evalcontract and internal/cmddesc all guard against importing
// internal/hookio directly (effectpolicy's imports_guard_test.go) — the
// effect-graph spike is deliberately hook-independent. This package is the
// one place in the tree that is EXPECTED to import both: it is the seam
// between the hook boundary and the spike, not a spike-internal package
// itself.
package claudecodeadapter

import (
	"fmt"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmddesc"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/effectgraph"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/effectpolicy"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/evalcontract"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

// RequestConfig carries the OPERATOR-configured fields evalcontract.Request
// holds that a bare hookio.HookInput has no way to express — VettedHosts,
// RemoteLifecycle, KubeContexts/KubeContextDefaultAllow, RemotePaths,
// BuildToolVerbs (see evalcontract.Request's own doc comment for each), plus
// an explicit ProjectRoot override. These are deployment/operator
// configuration threaded in by whoever wires this adapter up (a PreToolUse
// hook binary, a CLI, a test), never data a hook payload itself carries, so
// they are kept on a separate type rather than folded into HookInput.
type RequestConfig struct {
	ProjectRoot             string
	VettedHosts             []string
	RemoteLifecycle         map[string]string
	KubeContexts            map[string]evalcontract.KubeContextRule
	KubeContextDefaultAllow []string
	RemotePaths             map[string][]evalcontract.RemotePathRule
	BuildToolVerbs          []evalcontract.VerbScopedApproval
}

// request builds the evalcontract.Request shell shared by both the Bash and
// the file-tool paths: every RequestConfig field plus the HookInput's own
// CWD. Command is left unset here — BuildBashRequest is the one caller that
// fills it in from BashCommand(); the file-tool path never sets it at all,
// since EvaluateGraph (unlike Evaluate) never reads Request.Command.
func request(input *hookio.HookInput, cfg RequestConfig) evalcontract.Request {
	return evalcontract.Request{
		CWD:                     input.CWD,
		ProjectRoot:             cfg.ProjectRoot,
		VettedHosts:             cfg.VettedHosts,
		RemoteLifecycle:         cfg.RemoteLifecycle,
		KubeContexts:            cfg.KubeContexts,
		KubeContextDefaultAllow: cfg.KubeContextDefaultAllow,
		RemotePaths:             cfg.RemotePaths,
		BuildToolVerbs:          cfg.BuildToolVerbs,
	}
}

// BuildBashRequest returns the evalcontract.Request for a Bash HookInput:
// Command is the tool's own tool_input.command, unparsed — cmdparse runs
// inside effectpolicy.Evaluate itself, exactly as it does for every other
// caller of that function. Returns an error when input.ToolName is not
// "Bash" or tool_input cannot be decoded (hookio.HookInput.BashCommand's own
// errors).
func BuildBashRequest(input *hookio.HookInput, cfg RequestConfig) (evalcontract.Request, error) {
	cmd, err := input.BashCommand()
	if err != nil {
		return evalcontract.Request{}, fmt.Errorf("claudecodeadapter: %w", err)
	}
	req := request(input, cfg)
	req.Command = cmd
	return req, nil
}

// fileToolAccess maps a Claude Code file-editing tool name to the
// cmddesc.PathAccess the EQUIVALENT shell command would carry — this one
// lookup is the entire single-sourcing mechanism (see the package doc
// comment): once an access class is picked here, every downstream path
// policy treats the tool call and the equivalent shell command identically,
// because both end up as a cmddesc.Effect carrying the same PathAccess,
// judged by the same Policy.Judge implementation.
//
//   - "Write" -> cmddesc.AccessTruncate. The Write tool creates a new file,
//     or wholesale REPLACES an existing file's content — the write-side
//     behaviour of a plain, non-appending shell redirect (`echo x > path`;
//     effectgraph's own redirectionEffect classifies a bare `>` the
//     identical way — see build.go). AccessCreate would judge identically
//     under every policy in DefaultPolicies today (NoWriteToReadOnlyPath
//     and NoWriteToSecretPath both treat AccessCreate and AccessTruncate
//     the same — see NoWriteToSecretPath's own doc comment, "stays
//     unconditionally Forbidden for AccessCreate and AccessTruncate"), so
//     AccessTruncate is picked as the literal match for the shell
//     equivalent the bead itself names, not as an arbitrary tie-break.
//   - "Edit"/"MultiEdit" -> cmddesc.AccessModify. Both tools change an
//     EXISTING file's content in place (a search/replace, or an ordered
//     list of them for MultiEdit) without touching the rest of the file —
//     matching cmddesc.Effect's own doc comment on AccessModify verbatim
//     ("changes an existing file in place (append, edit)") and the same
//     access class `sed -i` carries (see golden_test.go's
//     sed_inplace_readme). MultiEdit is included alongside Edit because
//     hookio.HookInput.FilePath itself already treats the two uniformly
//     (both read tool_input.file_path the same way) and a MultiEdit is,
//     from the policy layer's perspective, indistinguishable from a single
//     Edit on the same path: neither this package nor cmddesc.Effect
//     carries a "how many edits" fact, and no policy in DefaultPolicies
//     would judge one differently from the other regardless.
var fileToolAccess = map[string]cmddesc.PathAccess{
	"Write":     cmddesc.AccessTruncate,
	"Edit":      cmddesc.AccessModify,
	"MultiEdit": cmddesc.AccessModify,
}

// IsFileEditTool reports whether toolName is one BuildFileToolGraph handles
// (Write, Edit, MultiEdit) — a caller composing Evaluate-shaped routing
// checks this (or calls Evaluate below directly) to decide whether a
// HookInput takes the shell-command path or the one-node-graph path.
func IsFileEditTool(toolName string) bool {
	_, ok := fileToolAccess[toolName]
	return ok
}

// BuildFileToolGraph builds the ONE-NODE graph tc-8og1 item 7 calls for from
// a Write/Edit/MultiEdit HookInput: a single effectgraph.Node of Kind
// NodeCommand carrying exactly one cmddesc.Effect (Kind EffectPath, Path the
// tool's own tool_input.file_path, Access from fileToolAccess), plus the
// File node and Writes edge effectgraph's own builder would have produced
// for the equivalent shell write (build.go's interpret, the "for _, e :=
// range effects" loop at the end). No cmdparse involved anywhere: the path
// comes straight off the tool's structured JSON input, so there is no shell
// text to parse and nothing to be "dynamic" about — Effect.Dynamic is
// always false here, unlike a shell argument that might carry a runtime
// expansion.
//
// Both the structural and interpreted graphs returned are the SAME graph
// value. Unlike effectgraph.BuildStructural/BuildInterpreted's split (which
// exists to show a shell command's pre-interpretation parse structure
// separately from its post-interpretation effects), a file-edit tool call
// has no un-interpreted "structure" of its own: the one fact worth graphing
// — which file, what class of write — IS the effect. Returning the same
// graph for both keeps evalcontract.Response's Structural/Interpreted shape
// uniform for a consumer that renders both (cmd_evaluate's Mermaid output,
// golden-style tests) without a special empty-structural case.
func BuildFileToolGraph(input *hookio.HookInput) (structural, interpreted effectgraph.Graph, err error) {
	access, ok := fileToolAccess[input.ToolName]
	if !ok {
		return effectgraph.Graph{}, effectgraph.Graph{}, fmt.Errorf("claudecodeadapter: %s is not a one-node file-edit tool", input.ToolName)
	}
	path, err := input.FilePath()
	if err != nil {
		return effectgraph.Graph{}, effectgraph.Graph{}, fmt.Errorf("claudecodeadapter: %w", err)
	}

	const commandID, fileID = "n0", "n1"
	g := effectgraph.Graph{
		Nodes: []effectgraph.Node{
			{
				ID:    commandID,
				Kind:  effectgraph.NodeCommand,
				Label: input.ToolName + "(" + path + ")",
				Effects: []cmddesc.Effect{{
					Kind:   cmddesc.EffectPath,
					Path:   path,
					Access: access,
					Source: "tool_input.file_path",
				}},
			},
			{ID: fileID, Kind: effectgraph.NodeFile, Label: path},
		},
		Edges: []effectgraph.Edge{
			{From: commandID, To: fileID, Kind: effectgraph.EdgeWrites, Label: access.String()},
		},
	}
	return g, g, nil
}

// Evaluate is the composition root: routes a Bash HookInput through
// BuildBashRequest + effectpolicy.Evaluate (shell parse, unchanged spike
// behaviour) and a Write/Edit/MultiEdit HookInput through
// BuildFileToolGraph + effectpolicy.EvaluateGraph — both paths are folded to
// a Decision by the SAME effectpolicy code, which is the whole point (see
// the package doc comment). Any other ToolName is not one this adapter
// package models and returns an error; a caller wiring a real hook decides
// separately what to do with a tool this adapter has no opinion on (defer
// to Claude Code's own prompt, as production's rule chain already does for
// tools it does not govern).
func Evaluate(input *hookio.HookInput, cfg RequestConfig, reg cmddesc.Registry, policies []effectpolicy.Policy, graphPolicies []effectpolicy.GraphPolicy) (evalcontract.Response, error) {
	switch {
	case input.ToolName == "Bash":
		req, err := BuildBashRequest(input, cfg)
		if err != nil {
			return evalcontract.Response{}, err
		}
		return effectpolicy.Evaluate(req, reg, policies, graphPolicies), nil
	case IsFileEditTool(input.ToolName):
		structural, interpreted, err := BuildFileToolGraph(input)
		if err != nil {
			return evalcontract.Response{}, err
		}
		pctx := effectpolicy.BuildPolicyContext(request(input, cfg))
		return effectpolicy.EvaluateGraph(structural, interpreted, policies, graphPolicies, pctx), nil
	default:
		return evalcontract.Response{}, fmt.Errorf("claudecodeadapter: unsupported tool %s", input.ToolName)
	}
}
