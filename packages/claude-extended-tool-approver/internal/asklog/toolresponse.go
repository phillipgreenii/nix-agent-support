package asklog

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// maxToolResponseExcerptLen bounds the scrubbed excerpt stored alongside a
// resolved tool_decisions row (see migration 9), mirroring the same
// "keep it short" posture as maxSummaryLen in summary.go — the excerpt is a
// debugging aid, not a full-fidelity replay of the tool's output.
const maxToolResponseExcerptLen = 200

// secretLikePattern matches common credential-shaped substrings so the
// excerpt stored in the DB cannot leak a token/password/key that happened to
// appear in a tool's stdout/stderr. It is intentionally conservative (a few
// well-known key=value/key: value shapes, plus long opaque base64-ish
// tokens) rather than an attempt at exhaustive secret detection — the
// packet's "Freedom" note leaves the exact scrubbing method unspecified, and
// no existing secret-scrubbing helper was found elsewhere in this module to
// reuse.
var secretLikePattern = regexp.MustCompile(
	`(?i)(api[_-]?key|access[_-]?key|secret|password|passwd|token|bearer)("?\s*[:=]\s*"?|\s+)[A-Za-z0-9_\-./+=]{6,}` +
		`|[A-Za-z0-9+/]{32,}={0,2}`,
)

// scrubExcerpt redacts secret-shaped substrings from s and truncates the
// result to maxToolResponseExcerptLen.
func scrubExcerpt(s string) string {
	scrubbed := secretLikePattern.ReplaceAllString(s, "[REDACTED]")
	scrubbed = strings.TrimSpace(scrubbed)
	if len(scrubbed) > maxToolResponseExcerptLen {
		scrubbed = scrubbed[:maxToolResponseExcerptLen] + "..."
	}
	return scrubbed
}

// summarizeToolResponse replaces the previous "store the whole raw
// tool_response" behavior (recorder.go's ResolveApproved). It returns four
// values suitable for binding directly as db.Exec args — each either nil
// (for SQL NULL) or a concrete string/int, following the same interface{}
// convention as Store.sandboxEnabledArg:
//
//   - hash: sha256 hex digest of the raw payload, so a specific response can
//     still be correlated/deduped without keeping its content.
//   - size: byte length of the raw payload.
//   - exitCode: a 0/1 proxy derived from the payload's is_error key
//     (references/database-schema.md's "tool_response shape" documents
//     is_error as the cross-tool failure signal — there is no literal
//     numeric process exit code in this payload). nil when is_error is
//     absent or unparseable.
//   - excerpt: a scrubbed, truncated excerpt — stdout+stderr concatenated
//     when the payload exposes those fields, otherwise the raw payload
//     text.
//
// All four are nil when raw is empty (no PostToolUse payload to summarize).
func summarizeToolResponse(raw json.RawMessage) (hash, size, exitCode, excerpt interface{}) {
	if len(raw) == 0 {
		return nil, nil, nil, nil
	}

	sum := sha256.Sum256(raw)
	hash = fmt.Sprintf("%x", sum)
	size = len(raw)

	var probe map[string]json.RawMessage
	var stdout, stderr string
	if err := json.Unmarshal(raw, &probe); err == nil {
		if isErrRaw, ok := probe["is_error"]; ok {
			var isErr bool
			if err := json.Unmarshal(isErrRaw, &isErr); err == nil {
				if isErr {
					exitCode = 1
				} else {
					exitCode = 0
				}
			}
		}
		if so, ok := probe["stdout"]; ok {
			_ = json.Unmarshal(so, &stdout)
		}
		if se, ok := probe["stderr"]; ok {
			_ = json.Unmarshal(se, &stderr)
		}
	}

	text := strings.TrimSpace(stdout + stderr)
	if text == "" {
		text = string(raw)
	}
	excerpt = scrubExcerpt(text)

	return hash, size, exitCode, excerpt
}
