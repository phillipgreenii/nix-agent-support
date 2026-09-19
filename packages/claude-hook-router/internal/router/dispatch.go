package router

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// TotalBudget is the whole dispatch chain's shared time budget (ADR 0071
// §2.5 "Total time budget: 6 seconds for the whole chain"), split across
// delegates rather than each delegate independently claiming its own full
// budget.
const TotalBudget = 6 * time.Second

// delegateWaitDelay bounds how long cmd.Wait() will wait for a killed
// delegate's stdout pipe to close once the process itself is gone. Without
// this, a delegate killed via context cancellation that leaves a grandchild
// process holding the pipe open can hang cmd.Wait() indefinitely — ADR 0071
// §4 Phase B, B1 "Adversarial delegate behavior".
const delegateWaitDelay = 2 * time.Second

// Result is what Dispatch hands back: the merged hookSpecificOutput plus
// the per-delegate attribution entries recorded along the way.
type Result struct {
	Output  HookOutput
	Entries []AttributionEntry
}

// Dispatch runs the sequential dispatch/merge loop for one hook event's
// ALREADY event-selected, matcher-filtered delegate list (see
// EventMatchField/MatchesEvent) against real delegate subprocesses, per
// ADR 0071 §2.4 (dispatch) and §2.5 (merge policy).
//
// payload is the incoming hook JSON's top-level fields; each delegate
// receives it verbatim except for whichever rewrite-shaped field(s) a prior
// delegate in this same chain already updated — "the CURRENT cumulative
// command text (the original, or a prior delegate's rewrite)". projectDir
// is forwarded to every delegate as CLAUDE_PROJECT_DIR unchanged; dataDir is
// this router's own CLAUDE_PLUGIN_DATA, under which each delegate gets its
// own vendored/<name>/ subdirectory (ADR 0071 §2.7).
func Dispatch(ctx context.Context, event string, delegates []Delegate, payload map[string]json.RawMessage, projectDir, dataDir string) Result {
	sorted := make([]Delegate, len(delegates))
	copy(sorted, delegates)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Priority != sorted[j].Priority {
			return sorted[i].Priority < sorted[j].Priority
		}
		return sorted[i].Name < sorted[j].Name
	})

	allObserve := len(sorted) > 0
	for _, d := range sorted {
		if d.Contract != "observe" {
			allObserve = false
			break
		}
	}

	ctx, cancel := context.WithTimeout(ctx, TotalBudget)
	defer cancel()

	var result Result
	var merge mergeState
	current := payload

	for _, d := range sorted {
		if ctx.Err() != nil {
			// Total budget already exhausted before this delegate's turn:
			// it is Skipped, never invoked. Whatever merge accumulated so
			// far is still returned, not discarded — the "partial-chain
			// failure" semantics (ADR 0071 §4 Phase B, B1).
			result.Entries = append(result.Entries, AttributionEntry{
				Timestamp:     time.Now(),
				HookEventName: event,
				DelegateName:  d.Name,
				Contract:      d.Contract,
				Verdict:       VerdictSkipped,
			})
			continue
		}

		start := time.Now()
		out, verdict := runDelegate(ctx, d, current, projectDir, filepath.Join(dataDir, "vendored", d.Name))
		result.Entries = append(result.Entries, AttributionEntry{
			Timestamp:     start,
			HookEventName: event,
			DelegateName:  d.Name,
			Contract:      d.Contract,
			Verdict:       verdict,
			DurationMS:    time.Since(start).Milliseconds(),
		})

		if d.Contract == "observe" || verdict != VerdictApplied {
			// observe: response always discarded, never merged (ADR 0071
			// §2.4 "observe … invoked purely for its side effect").
			// Abstain/Error: contributes nothing, chain continues — Abstain
			// never short-circuits.
			continue
		}

		merge.apply(d.Contract, out)
		current = advanceCumulativePayload(current, out)
	}

	if allObserve {
		// All-observe short-circuit: the merge computation never ran at
		// all — every observer was still dispatched sequentially above (for
		// determinism and per-call budget accounting), but every response
		// is discarded and the router returns {} unconditionally.
		return Result{Entries: result.Entries}
	}

	result.Output = merge.result()
	return result
}

// runDelegate invokes one delegate as a real subprocess, sending payload as
// its stdin JSON and reading its stdout as the expected hookSpecificOutput
// shape.
func runDelegate(ctx context.Context, d Delegate, payload map[string]json.RawMessage, projectDir, delegateDataDir string) (HookOutput, Verdict) {
	body, err := json.Marshal(payload)
	if err != nil {
		return HookOutput{}, VerdictError
	}

	if err := os.MkdirAll(delegateDataDir, 0o755); err != nil {
		// The delegate still runs even if its own data subdirectory
		// couldn't be created — only writes it attempts under that
		// directory would fail, not this dispatch call.
		delegateDataDir = ""
	}

	cmd := exec.CommandContext(ctx, "sh", "-c", d.Command)
	cmd.Stdin = bytes.NewReader(body)
	cmd.Env = delegateEnv(projectDir, delegateDataDir)
	cmd.WaitDelay = delegateWaitDelay

	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	if err := cmd.Run(); err != nil {
		// Non-zero exit, a command that doesn't exist/isn't executable
		// (os/exec "binary not found"), or a context-cancelled kill — all
		// degrade to Abstain-for-this-call, never fatal to the chain and
		// never a panic (ADR 0071 §2.4; §4 Phase B, B1 "Adversarial
		// delegate behavior").
		return HookOutput{}, VerdictError
	}

	var out HookOutput
	// json.Unmarshal already ignores unknown fields by default, so a
	// delegate returning valid JSON with unexpected EXTRA fields is handled
	// gracefully here without any special-casing (ADR 0071 Binding
	// decisions, "forward-compat").
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		return HookOutput{}, VerdictError
	}
	if out.IsZero() {
		return out, VerdictAbstain
	}
	return out, VerdictApplied
}

// advanceCumulativePayload returns the payload the NEXT delegate in the
// chain should see: current, with whichever rewrite-shaped field(s) out set
// replaced by out's value, so each delegate sees the current cumulative
// rewrite rather than the original untouched payload.
func advanceCumulativePayload(current map[string]json.RawMessage, out HookOutput) map[string]json.RawMessage {
	rewroteInput := len(out.UpdatedInput) > 0 || (out.Decision != nil && len(out.Decision.UpdatedInput) > 0)
	rewroteOutput := len(out.UpdatedToolOutput) > 0
	if !rewroteInput && !rewroteOutput {
		return current
	}

	next := make(map[string]json.RawMessage, len(current))
	for k, v := range current {
		next[k] = v
	}
	if len(out.UpdatedInput) > 0 {
		next["tool_input"] = out.UpdatedInput
	}
	if out.Decision != nil && len(out.Decision.UpdatedInput) > 0 {
		next["tool_input"] = out.Decision.UpdatedInput
	}
	if len(out.UpdatedToolOutput) > 0 {
		next["tool_response"] = out.UpdatedToolOutput
	}
	return next
}

// delegateEnv builds the environment for one delegate subprocess: the
// router's own environment, minus CLAUDE_PLUGIN_DATA (replaced with the
// delegate's own private subdirectory) and CLAUDE_ENV_FILE (scoped only to
// SessionStart/CwdChanged/FileChanged, never forwarded to any event this
// router handles). CLAUDE_PROJECT_DIR is forwarded unchanged — it is
// already present via inheritance, but is set explicitly here too so the
// forwarding is not merely incidental to how os.Environ() happens to behave
// (ADR 0071 Binding decisions, "Per-delegate environment").
func delegateEnv(projectDir, delegateDataDir string) []string {
	inherited := os.Environ()
	env := inherited[:0]
	for _, kv := range inherited {
		if strings.HasPrefix(kv, "CLAUDE_PLUGIN_DATA=") || strings.HasPrefix(kv, "CLAUDE_ENV_FILE=") {
			continue
		}
		env = append(env, kv)
	}
	if projectDir != "" {
		env = setEnv(env, "CLAUDE_PROJECT_DIR", projectDir)
	}
	if delegateDataDir != "" {
		env = setEnv(env, "CLAUDE_PLUGIN_DATA", delegateDataDir)
	}
	return env
}

func setEnv(env []string, key, value string) []string {
	prefix := key + "="
	for i, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			env[i] = prefix + value
			return env
		}
	}
	return append(env, prefix+value)
}
