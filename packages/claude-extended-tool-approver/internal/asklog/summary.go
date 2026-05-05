package asklog

import (
	"encoding/json"
	"fmt"
	"strings"
)

const maxSummaryLen = 120

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

func bashSummary(input json.RawMessage) string {
	var ti struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(input, &ti); err != nil {
		return "Bash"
	}
	cmd := ti.Command
	if idx := strings.Index(cmd, "\n"); idx >= 0 {
		cmd = cmd[:idx]
	}
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
