package mcp

import (
	"strings"
	"unicode"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

// allowedMCPTools is an exact-name allowlist for read-only MCP tools whose name
// carries no recognizable read verb (so the verb heuristic below can't classify
// them), e.g. atlassianUserInfo.
var allowedMCPTools = map[string]bool{
	"mcp__Atlassian-MCP-Server__atlassianUserInfo":               true,
	"mcp__Atlassian-MCP-Server__getAccessibleAtlassianResources": true,
	"mcp__Atlassian-MCP-Server__getJiraIssue":                    true,
	"mcp__Atlassian-MCP-Server__search":                          true,
	"mcp__Notion__notion-fetch":                                  true,
}

// readVerbs mark a non-mutating lookup. Approval requires one among the FIRST
// TWO tokens of the tool name — MCP tool names lead with the verb, optionally
// after a one-token server prefix (e.g. "notion-" / "slack_").
var readVerbs = map[string]bool{
	"search": true, "get": true, "list": true, "read": true,
	"fetch": true, "check": true,
}

// mutatingVerbs mark a side-effecting call. A mutating verb ANYWHERE in the tool
// name forces Abstain, even alongside a read verb (e.g. getAndDelete). Kept broad
// on purpose: extra entries only ever make the rule MORE conservative, never
// approve more.
var mutatingVerbs = map[string]bool{
	"create": true, "edit": true, "update": true, "transition": true,
	"delete": true, "add": true, "remove": true, "set": true,
	"post": true, "put": true, "patch": true, "write": true,
	"insert": true, "replace": true, "upload": true, "send": true,
	"archive": true, "move": true, "rename": true, "cancel": true,
	"close": true, "reopen": true, "assign": true, "publish": true,
	"unpublish": true, "duplicate": true, "revoke": true, "grant": true,
	"merge": true, "purge": true, "clear": true, "drop": true, "comment": true,
}

type Rule struct{}

func New() *Rule {
	return &Rule{}
}

func (r *Rule) Name() string {
	return "mcp"
}

func (r *Rule) Evaluate(input *hookio.HookInput) hookio.RuleResult {
	if !strings.HasPrefix(input.ToolName, "mcp__") {
		return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
	}
	if allowedMCPTools[input.ToolName] {
		return hookio.RuleResult{
			Decision: hookio.Approve,
			Reason:   "allowed MCP tool",
			Module:   r.Name(),
		}
	}
	if isReadOnlyMCPTool(input.ToolName) {
		return hookio.RuleResult{
			Decision: hookio.Approve,
			Reason:   "read-only MCP tool (non-mutating verb)",
			Module:   r.Name(),
		}
	}
	return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
}

// isReadOnlyMCPTool reports whether an mcp__server__tool name denotes a
// non-mutating lookup: a read verb appears among the first two tokens of the
// tool segment AND no mutating verb appears anywhere in it. It is server-agnostic
// (works for every server id, including the two Notion ids), and fails safe — an
// unrecognized verb yields Abstain, not Approve.
func isReadOnlyMCPTool(toolName string) bool {
	toks := tokens(toolSegment(toolName))
	if len(toks) == 0 {
		return false
	}
	for _, t := range toks {
		if mutatingVerbs[t] {
			return false
		}
	}
	lead := toks
	if len(lead) > 2 {
		lead = lead[:2]
	}
	for _, t := range lead {
		if readVerbs[t] {
			return true
		}
	}
	return false
}

// toolSegment returns the <tool> portion of an "mcp__<server>__<tool>" name
// (everything after the last "__").
func toolSegment(toolName string) string {
	name := strings.TrimPrefix(toolName, "mcp__")
	if i := strings.LastIndex(name, "__"); i >= 0 {
		return name[i+2:]
	}
	return name
}

// tokens splits an identifier into lowercase word tokens, breaking on '-', '_',
// and camelCase boundaries (a lowercase/digit followed by an uppercase letter).
func tokens(s string) []string {
	var b strings.Builder
	var prev rune
	for i, r := range s {
		switch {
		case r == '-' || r == '_':
			b.WriteByte(' ')
		case unicode.IsUpper(r) && i > 0 && (unicode.IsLower(prev) || unicode.IsDigit(prev)):
			b.WriteByte(' ')
			b.WriteRune(unicode.ToLower(r))
		default:
			b.WriteRune(unicode.ToLower(r))
		}
		prev = r
	}
	return strings.Fields(b.String())
}
