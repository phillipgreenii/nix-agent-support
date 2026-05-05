package hookio

import "encoding/json"

func FormatOutput(result RuleResult, updatedInput map[string]interface{}) []byte {
	if result.Decision == Abstain {
		return []byte("{}")
	}
	decisionStr := ""
	switch result.Decision {
	case Approve:
		decisionStr = "allow"
	case Reject:
		decisionStr = "deny"
	case Ask:
		decisionStr = "ask"
	default:
		return []byte("{}")
	}
	hookOutput := map[string]interface{}{
		"hookEventName":            "PreToolUse",
		"permissionDecision":       decisionStr,
		"permissionDecisionReason": result.Reason,
	}
	if updatedInput != nil {
		hookOutput["updatedInput"] = updatedInput
	}
	out := map[string]interface{}{
		"hookSpecificOutput": hookOutput,
	}
	data, err := json.Marshal(out)
	if err != nil {
		return []byte("{}")
	}
	return data
}
