package settingseval

import (
	"encoding/json"
	"os"
	"strings"
)

// SettingsEvaluator replicates Claude Code's permission matching logic
// to evaluate whether a tool invocation would be allowed/denied/asked
// by a given settings file.
type SettingsEvaluator struct {
	allow []rule
	deny  []rule
	ask   []rule
}

type rule struct {
	raw     string
	matcher matcher
}

type matcher interface {
	matches(toolName string, toolInput json.RawMessage, cwd string) bool
}

type settingsJSON struct {
	Permissions struct {
		Allow []string `json:"allow"`
		Deny  []string `json:"deny"`
		Ask   []string `json:"ask"`
	} `json:"permissions"`
}

// NewSettingsEvaluator parses a Claude Code settings file and creates
// an evaluator that can test tool invocations against its permission rules.
func NewSettingsEvaluator(settingsPath string) (*SettingsEvaluator, error) {
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		return nil, err
	}

	var settings settingsJSON
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil, err
	}

	se := &SettingsEvaluator{}
	settingsDir := settingsPath[:strings.LastIndex(settingsPath, "/")+1]
	for _, raw := range settings.Permissions.Allow {
		se.allow = append(se.allow, rule{raw: raw, matcher: parseMatcher(raw, settingsDir)})
	}
	for _, raw := range settings.Permissions.Deny {
		se.deny = append(se.deny, rule{raw: raw, matcher: parseMatcher(raw, settingsDir)})
	}
	for _, raw := range settings.Permissions.Ask {
		se.ask = append(se.ask, rule{raw: raw, matcher: parseMatcher(raw, settingsDir)})
	}
	return se, nil
}

// Evaluate returns "allow", "deny", "ask", or "" (no match).
// Precedence: deny > ask > allow.
func (se *SettingsEvaluator) Evaluate(toolName string, toolInput json.RawMessage, cwd string) string {
	for _, r := range se.deny {
		if r.matcher.matches(toolName, toolInput, cwd) {
			return "deny"
		}
	}
	for _, r := range se.ask {
		if r.matcher.matches(toolName, toolInput, cwd) {
			return "ask"
		}
	}
	for _, r := range se.allow {
		if r.matcher.matches(toolName, toolInput, cwd) {
			return "allow"
		}
	}
	return ""
}

// Rules returns the raw rule strings for a given decision type.
func (se *SettingsEvaluator) Rules(decision string) []string {
	var rules []rule
	switch decision {
	case "allow":
		rules = se.allow
	case "deny":
		rules = se.deny
	case "ask":
		rules = se.ask
	default:
		return nil
	}
	result := make([]string, len(rules))
	for i, r := range rules {
		result[i] = r.raw
	}
	return result
}
