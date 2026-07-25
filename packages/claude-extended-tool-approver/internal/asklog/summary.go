package asklog

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
)

const maxSummaryLen = 120

// ToolSummary returns a short, human-readable DISPLAY label for a tool
// invocation (used by `show`/`evaluate` output columns). It is truncated for
// brevity and MUST NOT be used as a grouping key — use CommandClass for that.
func ToolSummary(toolName string, toolInput json.RawMessage) string {
	switch toolName {
	case "Bash":
		return bashSummary(toolInput)
	case "Write", "Edit", "Read", "Delete", "MultiEdit":
		return fileSummary(toolName, toolInput)
	case "WebFetch":
		return webFetchSummary(toolInput)
	default:
		if strings.HasPrefix(toolName, "mcp__") {
			return mcpSummary(toolName)
		}
		return toolName
	}
}

// CommandClass returns a stable grouping key for a decision row: a canonical,
// NON-truncated representation of the tool invocation. Analysis that buckets
// rows by "same command" (e.g. the identify-hook-misses taxonomy) MUST group on
// this, not on ToolSummary — ToolSummary truncates Bash commands at the first
// newline and at maxSummaryLen, which fabricated phantom buckets (bead
// pg2-okd13.3): of ~9,700 "cd …" summary rows, none were genuinely a lone cd —
// the real command lived after the newline / past 120 chars.
//
// For Bash it returns the full normalized command (cmdparse.NormalizeCommand),
// so a multi-line "cd foo\nwork" row buckets under its real command class, the
// same as "cd foo && work". For every other tool it reuses ToolSummary: file
// paths, URLs and mcp names are already stable and are not subject to the bash
// first-newline / length cut. cwd threads through to executable normalization;
// projectRoot is unknown at analysis time so "" is passed.
func CommandClass(toolName string, toolInput json.RawMessage, cwd string) string {
	if toolName == "Bash" {
		var ti struct {
			Command string `json:"command"`
		}
		if err := json.Unmarshal(toolInput, &ti); err == nil {
			if key := cmdparse.NormalizeCommand(ti.Command, "", cwd); key != "" {
				return key
			}
		}
	}
	return ToolSummary(toolName, toolInput)
}

func bashSummary(input json.RawMessage) string {
	var ti struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(input, &ti); err != nil {
		return "Bash"
	}
	cmd := ti.Command
	// Collapse newlines to spaces so a multi-line compound command renders as a
	// single representative summary line, instead of being cut to its (often
	// "cd …") first line, which misrepresented the command. This length
	// truncation is DISPLAY ONLY; grouping/bucketing uses the full, untruncated
	// normalized command via CommandClass. See bead pg2-okd13.3.
	cmd = strings.ReplaceAll(cmd, "\r\n", "\n")
	cmd = strings.ReplaceAll(cmd, "\n", " ")
	cmd = strings.TrimSpace(cmd)
	if len(cmd) > maxSummaryLen {
		cmd = cmd[:maxSummaryLen] + "..."
	}
	return cmd
}

func fileSummary(toolName string, input json.RawMessage) string {
	var ti struct {
		FilePath string `json:"file_path"`
	}
	if err := json.Unmarshal(input, &ti); err != nil {
		return toolName
	}
	return fmt.Sprintf("%s: %s", toolName, ti.FilePath)
}

func webFetchSummary(input json.RawMessage) string {
	var ti struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(input, &ti); err != nil {
		return "WebFetch"
	}
	return "WebFetch: " + ti.URL
}

func mcpSummary(toolName string) string {
	parts := strings.SplitN(toolName, "__", 3)
	if len(parts) == 3 {
		return fmt.Sprintf("mcp: %s__%s", parts[1], parts[2])
	}
	return toolName
}
