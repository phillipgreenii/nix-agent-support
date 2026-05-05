package hookio

import (
	"encoding/json"
	"fmt"
	"io"
)

func ParseInput(r io.Reader) (*HookInput, error) {
	var input HookInput
	if err := json.NewDecoder(r).Decode(&input); err != nil {
		return nil, fmt.Errorf("decode JSON: %w", err)
	}
	return &input, nil
}

func (h *HookInput) BashCommand() (string, error) {
	if h.ToolName != "Bash" {
		return "", fmt.Errorf("tool is not Bash, got %s", h.ToolName)
	}
	var bash BashToolInput
	if err := json.Unmarshal(h.ToolInput, &bash); err != nil {
		return "", fmt.Errorf("parse Bash tool_input: %w", err)
	}
	return bash.Command, nil
}

func (h *HookInput) WebFetchURL() string {
	var ti WebFetchToolInput
	if err := json.Unmarshal(h.ToolInput, &ti); err != nil {
		return ""
	}
	return ti.URL
}

func (h *HookInput) FilePath() (string, error) {
	switch h.ToolName {
	case "Write", "Read", "Edit", "MultiEdit", "Delete":
		// file_path is used by these tools
	default:
		return "", fmt.Errorf("tool is not a file tool, got %s", h.ToolName)
	}
	var file FileToolInput
	if err := json.Unmarshal(h.ToolInput, &file); err != nil {
		return "", fmt.Errorf("parse file tool_input: %w", err)
	}
	return file.FilePath, nil
}

func (h *HookInput) SearchPath() (string, error) {
	switch h.ToolName {
	case "Glob", "Grep":
		// path is optional for search tools
	default:
		return "", fmt.Errorf("tool is not a search tool, got %s", h.ToolName)
	}
	var search SearchToolInput
	if err := json.Unmarshal(h.ToolInput, &search); err != nil {
		return "", fmt.Errorf("parse search tool_input: %w", err)
	}
	return search.Path, nil
}
