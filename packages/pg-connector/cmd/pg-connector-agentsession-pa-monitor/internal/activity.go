// activity.go: Backend's activity.Provider implementation. list_activity is
// range-shaped and stateless: each call execs `pa-monitor sessions --json
// --since .. --before ..` and maps one record per Claude Code session onto
// one "session" activity item. This backend reads no transcript itself.
//
// Attribution: every session on this single-user machine is the operator's,
// so no identity key is consulted and every session that meets min_user_turns
// is included.
//
// Config (this backend's own opaque block, decoded per call): min_user_turns,
// a JSON integer, default 1; 0 includes every session; a non-integer or
// negative value is invalid_argument.
//
// Documented item "fields": the whole record exactly as `pa-monitor sessions`
// emitted it (session_id, cwd, branch, model, started_at, ended_at,
// user_turns, assistant_turns, first_prompt, tokens, cost_usd?).
package internal

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/activity"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

var _ activity.Provider = (*Backend)(nil)

// KindSession is the one activity kind this backend emits.
const KindSession = "session"

// ActivityKinds is the vocabulary this backend contributes to capabilities
// vocabulary.activity_kinds.
var ActivityKinds = []string{KindSession}

const (
	defaultMinUserTurns = 1
	// summaryPromptMax is the most runes of the first prompt kept in summary.
	summaryPromptMax = 80
	noPromptText     = "(no prompt)"
)

// sessionRecordJSON is the subset of a `pa-monitor sessions` record this
// backend interprets; the whole record rides in fields untouched. Dispatched
// is optional (absent today, treated as false).
type sessionRecordJSON struct {
	SessionID   string `json:"session_id"`
	Cwd         string `json:"cwd"`
	Branch      string `json:"branch"`
	StartedAt   string `json:"started_at"`
	UserTurns   int    `json:"user_turns"`
	FirstPrompt string `json:"first_prompt"`
	Dispatched  bool   `json:"dispatched"`
}

type sessionsJSONDoc struct {
	Sessions []json.RawMessage `json:"sessions"`
}

// minUserTurnsFromConfig decodes this backend's config block (never cached).
func minUserTurnsFromConfig(raw json.RawMessage) (int, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return defaultMinUserTurns, nil
	}
	var cfg map[string]json.RawMessage
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return 0, scriptout.WrapError(scriptout.ErrInvalidArgument, "pa-monitor: decode config: "+err.Error())
	}
	v, ok := cfg["min_user_turns"]
	if !ok {
		return defaultMinUserTurns, nil
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(v)), 10, 32)
	if err != nil {
		return 0, scriptout.WrapError(scriptout.ErrInvalidArgument, "pa-monitor: config min_user_turns must be an integer")
	}
	if n < 0 {
		return 0, scriptout.WrapError(scriptout.ErrInvalidArgument, "pa-monitor: config min_user_turns must not be negative")
	}
	return int(n), nil
}

// ListActivity implements activity.Provider.
func (b *Backend) ListActivity(ctx context.Context, since, before time.Time) (*schema.ActivityListResult, error) {
	minTurns, err := minUserTurnsFromConfig(scriptout.ConfigFromContext(ctx))
	if err != nil {
		return nil, err
	}
	raw, err := b.runner.Sessions(ctx, since, before)
	if err != nil {
		return nil, classifyPaMonitorError(err)
	}
	var doc sessionsJSONDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, classifyPaMonitorError(err)
	}
	asOf := b.now().Format(time.RFC3339)
	result := &schema.ActivityListResult{Items: []schema.ActivityItem{}, Truncated: false}
	for _, rec := range doc.Sessions {
		var s sessionRecordJSON
		if json.Unmarshal(rec, &s) != nil || s.SessionID == "" {
			continue
		}
		started, perr := time.Parse(time.RFC3339, s.StartedAt)
		if perr != nil || s.UserTurns < minTurns {
			continue
		}
		result.Items = append(result.Items, schema.ActivityItem{
			ID:         "session:" + s.SessionID,
			Kind:       KindSession,
			EntityType: "agentsession",
			EntityID:   s.SessionID,
			OccurredAt: started.UTC().Format(time.RFC3339),
			Summary:    sessionSummary(s),
			Labels:     sessionLabels(s),
			Fields:     rec,
			AsOf:       asOf,
			Stale:      false,
		})
	}
	return result, nil
}

// cwdBase is the last path element of cwd, "" for an empty cwd.
func cwdBase(cwd string) string {
	if strings.TrimSpace(cwd) == "" {
		return ""
	}
	return filepath.Base(cwd)
}

// sessionSummary renders "<first prompt, truncated> (<cwd basename>, <branch>)".
func sessionSummary(s sessionRecordJSON) string {
	prompt := strings.Join(strings.Fields(s.FirstPrompt), " ")
	if prompt == "" {
		prompt = noPromptText
	} else if r := []rune(prompt); len(r) > summaryPromptMax {
		prompt = string(r[:summaryPromptMax]) + "..."
	}
	var ctxParts []string
	if base := cwdBase(s.Cwd); base != "" {
		ctxParts = append(ctxParts, base)
	}
	if s.Branch != "" {
		ctxParts = append(ctxParts, s.Branch)
	}
	if len(ctxParts) == 0 {
		return prompt
	}
	return prompt + " (" + strings.Join(ctxParts, ", ") + ")"
}

func sessionLabels(s sessionRecordJSON) []string {
	var labels []string
	if base := cwdBase(s.Cwd); base != "" {
		labels = append(labels, "cwd:"+base)
	}
	if s.Branch != "" {
		labels = append(labels, "branch:"+s.Branch)
	}
	if s.Dispatched {
		labels = append(labels, "agent:dispatched")
	}
	return labels
}
